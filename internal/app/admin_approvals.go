package app

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/SamuelSupe/mcphub/v2/internal/config"
	"github.com/SamuelSupe/mcphub/v2/internal/configstore"
)

type approvalView struct {
	ClientGrant        *configstore.GrantBinding   `json:"client_grant,omitempty"`
	CredentialsChanged bool                        `json:"credentials_changed"`
	Kind               string                      `json:"kind"`
	RequiredApprovals  int                         `json:"required_approvals"`
	ApprovedBy         []string                    `json:"approved_by"`
	AlreadyApproved    bool                        `json:"already_approved"`
	OperationID        string                      `json:"operation_id,omitempty"`
	ID                 string                      `json:"id"`
	Status             string                      `json:"status"`
	Issuer             string                      `json:"issuer"`
	Subject            string                      `json:"subject"`
	Tool               string                      `json:"tool"`
	Target             string                      `json:"target"`
	Effect             string                      `json:"effect"`
	Reviewer           string                      `json:"reviewer"`
	RequestJSON        string                      `json:"request_json"`
	Summaries          []approvalSummaryView       `json:"summaries"`
	PreviewJSON        string                      `json:"preview_json,omitempty"`
	BeforeJSON         string                      `json:"before_json,omitempty"`
	AfterJSON          string                      `json:"after_json,omitempty"`
	CreatedAt          time.Time                   `json:"created_at"`
	ExpiresAt          time.Time                   `json:"expires_at"`
	UpdatedAt          time.Time                   `json:"updated_at"`
	CanReview          bool                        `json:"can_review"`
	CanCancel          bool                        `json:"can_cancel"`
	StepUpRequired     bool                        `json:"step_up_required"`
	StepUpVerified     bool                        `json:"step_up_verified"`
	History            []configstore.ApprovalEvent `json:"history,omitempty"`
}

type approvalSummaryView struct {
	Action      string            `json:"action"`
	Environment string            `json:"environment"`
	Resources   map[string]string `json:"resources"`
	Version     string            `json:"version"`
}

func approvalJSON(raw []byte) string {
	var text bytes.Buffer
	if err := json.Indent(&text, raw, "", "  "); err != nil {
		return string(raw)
	}
	return text.String()
}

func approvalArguments(a configstore.Approval) json.RawMessage {
	var params struct {
		Arguments json.RawMessage `json:"arguments"`
	}
	_ = json.Unmarshal(a.Intent.Params, &params)
	return params.Arguments
}

func (app *App) canReviewApproval(a configstore.Approval, identity adminIdentity) bool {
	if a.Intent.Kind == "configuration" {
		return app.adminAuth.canReviewConfiguration(identity.info)
	}
	return app.adminAuth.canApprove(identity.info)
}

func (app *App) canSeeApproval(a configstore.Approval, identity adminIdentity, issuer string) bool {
	return identity.info != nil && a.Intent.Issuer == issuer && (a.Intent.Subject == identity.info.UserID || (app.canReviewApproval(a, identity) && config.CanReviewApproval(a.Intent.Rules, identity.info.UserID, a.Intent.Subject, approvalArguments(a), false)))
}

func (app *App) makeApprovalView(a configstore.Approval, identity adminIdentity) approvalView {
	view := approvalView{ClientGrant: a.Intent.ClientGrant, Kind: a.Intent.Kind, RequiredApprovals: config.ApprovalQuorum(a.Intent.Rules), ApprovedBy: a.ApprovedBy, AlreadyApproved: slices.Contains(a.ApprovedBy, identity.info.UserID), OperationID: a.Intent.OperationID, ID: a.ID, Status: a.Status, Issuer: a.Intent.Issuer, Subject: a.Intent.Subject, Tool: a.Intent.Tool, Target: a.Intent.Target, Effect: a.Intent.Effect, Reviewer: a.Reviewer, RequestJSON: approvalJSON(a.Intent.Params), PreviewJSON: approvalJSON(a.Intent.Preview), CreatedAt: a.CreatedAt, ExpiresAt: a.ExpiresAt, UpdatedAt: a.UpdatedAt}
	var preview struct {
		Before, After      json.RawMessage
		CredentialsChanged bool `json:"credentials_changed"`
	}
	if json.Unmarshal(a.Intent.Preview, &preview) == nil {
		view.BeforeJSON, view.AfterJSON = approvalJSON(preview.Before), approvalJSON(preview.After)
		view.CredentialsChanged = preview.CredentialsChanged
	}
	for _, summary := range a.Intent.Summary {
		value := approvalSummaryView{Action: summary.Action, Environment: summary.Environment, Version: summary.Version, Resources: map[string]string{}}
		for pointer, raw := range summary.Resources {
			value.Resources[pointer] = string(raw)
		}
		view.Summaries = append(view.Summaries, value)
	}
	view.CanReview = app.canReviewApproval(a, identity) && config.CanReviewApproval(a.Intent.Rules, identity.info.UserID, a.Intent.Subject, approvalArguments(a), true)
	view.CanCancel = identity.info.UserID == a.Intent.Subject
	view.StepUpRequired = config.ApprovalNeedsStepUp(a.Intent.Rules)
	_, view.StepUpVerified = identity.session.approvalProof(a.ID, false)
	return view
}

func (a *App) serveAdminApprovals(w http.ResponseWriter, req *http.Request, suffix string) {
	if !a.currentConfig().Admin.Remote() || a.adminAuth == nil {
		if suffix == "" && req.Method == http.MethodGet {
			writeJSON(w, 200, map[string]any{"enabled": false, "approvals": []approvalView{}})
		} else {
			writeAPIError(w, 403, "browser_approval_required", "写工具审批需要远程管理员登录，本地免登录模式不可审批", "")
		}
		return
	}
	identity, _ := req.Context().Value(adminIdentityKey{}).(adminIdentity)
	if identity.info == nil || identity.session == nil {
		writeAPIError(w, 403, "browser_approval_required", "审批必须使用管理员浏览器会话，不能使用调用凭证或管理 API Token", "")
		return
	}
	if suffix == "/delivery" && req.Method == http.MethodGet {
		if !a.adminAuth.canConfigure(identity.info) && !a.adminAuth.canReviewConfiguration(identity.info) {
			writeAPIError(w, 403, "forbidden", "需要配置或安全管理员权限", "")
			return
		}
		value, err := a.store.ApprovalDeliveryStatus(req.Context())
		if err != nil {
			writeAPIError(w, 500, "storage_failed", "无法读取投递状态", "")
			return
		}
		writeJSON(w, 200, value)
		return
	}
	if suffix == "" && req.Method == http.MethodGet {
		a.listAdminApprovals(w, req, identity)
		return
	}
	id, action, _ := strings.Cut(strings.TrimPrefix(suffix, "/"), "/")
	if id == "" || len(id) > 128 || (action != "" && action != "verify") {
		writeAPIError(w, 404, "not_found", "审批不存在", "")
		return
	}
	value, err := a.store.GetApproval(req.Context(), id)
	if errors.Is(err, configstore.ErrNotFound) {
		writeAPIError(w, 404, "not_found", "审批不存在", "")
		return
	}
	if err != nil {
		writeAPIError(w, 500, "storage_failed", "无法读取审批记录", "")
		return
	}
	if !a.canSeeApproval(value, identity, a.adminAuth.issuer) {
		writeAPIError(w, 404, "not_found", "审批不存在", "")
		return
	}
	view := a.makeApprovalView(value, identity)
	if req.Method == http.MethodPost {
		if action == "verify" {
			if !view.CanReview || view.AlreadyApproved || value.Status != "pending" || !view.StepUpRequired {
				writeAPIError(w, 409, "approval_conflict", "当前请求不允许加强身份验证", "")
				return
			}
			if value.Intent.Kind == "configuration" {
				query := req.URL.Query()
				query.Set("role", "security")
				copyURL := *req.URL
				copyURL.RawQuery = query.Encode()
				req = req.Clone(req.Context())
				req.URL = &copyURL
			}
			target, err := a.adminAuth.startLogin(w, req, id, identity.session)
			if err != nil {
				writeAPIError(w, 503, "verification_unavailable", "身份验证不可用，请检查 OIDC 和认证强度配置", "")
				return
			}
			writeJSON(w, 200, map[string]string{"authorization_url": target})
			return
		}
		var input struct {
			Decision string `json:"decision"`
			Reason   string `json:"reason"`
			Outcome  string `json:"outcome"`
		}
		if !decodeAdminJSON(w, req, &input) {
			return
		}
		if strings.TrimSpace(input.Reason) == "" || len(input.Reason) > 2048 {
			writeAPIError(w, 400, "validation_failed", "请填写理由，最多 2048 字节", "reason")
			return
		}
		detail := configstore.ApprovalDetail{Reason: input.Reason, Outcome: input.Outcome}
		if input.Decision == "cancelled" {
			if !view.CanCancel {
				writeAPIError(w, 403, "approval_denied", "仅申请人可以取消申请", "")
				return
			}
			err = a.store.CancelApproval(req.Context(), id, a.adminAuth.issuer, identity.info.UserID, input.Reason)
		} else {
			if !view.CanReview {
				writeAPIError(w, 403, "approval_denied", "当前账号不在审批范围内，或不允许自行审批", "")
				return
			}
			switch input.Decision {
			case "approved", "rejected", "revoked":
				if input.Decision == "approved" && view.AlreadyApproved {
					writeAPIError(w, 409, "approval_conflict", "同一审批人不能重复计票", "")
					return
				}
				if input.Decision == "approved" && view.StepUpRequired {
					proof, ok := identity.session.approvalProof(id, true)
					if !ok {
						writeAPIError(w, 403, "step_up_required", "请先完成本次审批的加强身份验证", "")
						return
					}
					detail.ACR, detail.AuthTime = proof.ACR, proof.AuthTime
				}
				err = a.store.DecideApproval(req.Context(), id, input.Decision, identity.info.UserID, detail)
			case "investigated":
				if !slices.Contains([]string{"applied", "not_applied", "uncertain"}, input.Outcome) {
					writeAPIError(w, 400, "validation_failed", "请选择核查结果", "outcome")
					return
				}
				err = a.store.InvestigateApproval(req.Context(), id, identity.info.UserID, detail)
			default:
				writeAPIError(w, 400, "validation_failed", "不支持此审批操作", "decision")
				return
			}
		}
		if errors.Is(err, configstore.ErrConflict) {
			writeAPIError(w, 409, "approval_conflict", "审批已变化、过期或开始执行，请刷新；撤销不能回滚已执行的操作", "")
			return
		}
		if err != nil {
			writeAPIError(w, 500, "storage_failed", "无法保存审批决定", "")
			return
		}
		value, err = a.store.GetApproval(req.Context(), id)
		if err != nil {
			writeAPIError(w, 500, "storage_failed", "无法读取审批记录", "")
			return
		}
		if input.Decision == "approved" && value.Status == "approved" && value.Intent.Kind == "configuration" {
			applyErr := a.applyConfigurationApproval(a.adminMutationContext(req), value)
			value, err = a.store.GetApproval(req.Context(), id)
			if err != nil {
				writeAPIError(w, 500, "storage_failed", "无法读取审批记录", "")
				return
			}
			if applyErr != nil {
				a.logger.Warn("approved configuration could not be applied", "approval_id", id)
			}
		}
		view = a.makeApprovalView(value, identity)
	} else if req.Method != http.MethodGet || action != "" {
		writeAPIError(w, 405, "method_not_allowed", "不支持此操作", "")
		return
	}
	view.History, err = a.store.ApprovalHistory(req.Context(), id)
	if err != nil {
		writeAPIError(w, 500, "storage_failed", "无法读取审批记录", "")
		return
	}
	writeJSON(w, 200, view)
}

func (a *App) listAdminApprovals(w http.ResponseWriter, req *http.Request, identity adminIdentity) {
	q := req.URL.Query()
	limit := 25
	var err error
	if q.Get("limit") != "" {
		limit, err = strconv.Atoi(q.Get("limit"))
	}
	status, subject, tool, cursor := q.Get("status"), q.Get("subject"), q.Get("tool"), q.Get("cursor")
	if err != nil || limit < 1 || limit > 100 || len(subject) > 256 || len(tool) > 128 || len(cursor) > 128 || !slices.Contains([]string{"", "pending", "approved", "rejected", "revoked", "cancelled", "expired", "executing", "succeeded", "failed", "unknown"}, status) {
		writeAPIError(w, 400, "validation_failed", "审批筛选条件无效", "")
		return
	}
	views := make([]approvalView, 0, limit)
	next := ""
	// Policies are encrypted. Scan bounded pages and authorize each row before
	// disclosure, carrying the last examined row as the continuation cursor.
	for scanned := 0; scanned < 1000; scanned += 100 {
		values, err := a.store.ListApprovals(req.Context(), configstore.ApprovalQuery{Status: status, Subject: subject, Cursor: cursor, Limit: 100})
		if err != nil {
			if errors.Is(err, configstore.ErrApprovalCursor) {
				writeAPIError(w, 400, "validation_failed", "审批筛选条件无效", "cursor")
			} else {
				writeAPIError(w, 500, "storage_failed", "无法读取审批记录", "")
			}
			return
		}
		for i, value := range values {
			cursor = configstore.ApprovalCursor(value)
			if (tool == "" || tool == value.Intent.Tool) && a.canSeeApproval(value, identity, a.adminAuth.issuer) {
				views = append(views, a.makeApprovalView(value, identity))
			}
			if len(views) == limit {
				if i < len(values)-1 || len(values) == 100 {
					next = cursor
				}
				writeJSON(w, 200, map[string]any{"enabled": true, "approvals": views, "next_cursor": next})
				return
			}
		}
		if len(values) < 100 {
			next = ""
			break
		}
		next = cursor
	}
	writeJSON(w, 200, map[string]any{"enabled": true, "approvals": views, "next_cursor": next})
}
