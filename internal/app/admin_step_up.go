package app

import (
	"net/http"
	"net/url"
	"slices"
	"time"

	"github.com/SamuelSupe/mcphub/v2/internal/configstore"
	"github.com/coreos/go-oidc/v3/oidc"
	mcpauth "github.com/modelcontextprotocol/go-sdk/auth"
	"golang.org/x/oauth2"
)

func (a *adminAuthorization) finishApprovalVerification(w http.ResponseWriter, req *http.Request, pending adminLogin, token *oauth2.Token, info *mcpauth.TokenInfo) {
	target := "/?approval=" + url.QueryEscape(pending.approvalID)
	fail := func() { http.Redirect(w, req, target+"&verification=failed#approvals", http.StatusSeeOther) }
	if a.session(req) != pending.session || info.UserID != pending.subject || !hasAdminScopes(info, pending.requiredScopes) {
		fail()
		return
	}
	raw, _ := token.Extra("id_token").(string)
	if raw == "" {
		fail()
		return
	}
	id, err := pending.idVerifier.Verify(oidc.ClientContext(req.Context(), a.client), raw)
	if err != nil || id.Subject != pending.subject || id.Nonce != pending.nonce {
		fail()
		return
	}
	if id.AccessTokenHash != "" && id.VerifyAccessToken(token.AccessToken) != nil {
		fail()
		return
	}
	var claims struct {
		ACR      string `json:"acr"`
		AuthTime int64  `json:"auth_time"`
	}
	if id.Claims(&claims) != nil || !slices.Contains(a.cfg.Approvals.StepUpACRValues, claims.ACR) {
		fail()
		return
	}
	now := time.Now()
	// IDP clock skew is bounded; silent reuse of an older login is insufficient.
	if claims.AuthTime < pending.started.Add(-30*time.Second).Unix() || claims.AuthTime > now.Add(30*time.Second).Unix() {
		fail()
		return
	}
	session := pending.session
	session.mu.Lock()
	defer session.mu.Unlock()
	if session.closed || !now.Before(session.expires) {
		fail()
		return
	}
	if session.stepUps == nil {
		session.stepUps = make(map[string]approvalVerification)
	}
	for id, proof := range session.stepUps {
		if !now.Before(proof.expires) {
			delete(session.stepUps, id)
		}
	}
	if len(session.stepUps) >= 32 {
		fail()
		return
	}
	session.token, session.oauth = token, pending.oauth
	session.stepUps[pending.approvalID] = approvalVerification{expires: now.Add(2 * time.Minute), detail: configstore.ApprovalDetail{ACR: claims.ACR, AuthTime: time.Unix(claims.AuthTime, 0).UTC()}}
	http.Redirect(w, req, target+"&verification=complete#approvals", http.StatusSeeOther)
}

func (s *adminSession) approvalProof(id string, consume bool) (configstore.ApprovalDetail, bool) {
	if s == nil {
		return configstore.ApprovalDetail{}, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	proof, ok := s.stepUps[id]
	if !ok || s.closed || !time.Now().Before(proof.expires) {
		delete(s.stepUps, id)
		return configstore.ApprovalDetail{}, false
	}
	if consume {
		delete(s.stepUps, id)
	}
	return proof.detail, true
}
