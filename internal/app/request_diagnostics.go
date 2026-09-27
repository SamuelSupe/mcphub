package app

import (
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"github.com/SamuelSupe/mcphub/v2/internal/diagnostics"
)

type diagnosticWriter struct {
	http.ResponseWriter
	status int
}

func (w *diagnosticWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }
func (w *diagnosticWriter) WriteHeader(status int) {
	if status >= 200 && w.status == 0 {
		w.status = status
	}
	w.ResponseWriter.WriteHeader(status)
}
func (w *diagnosticWriter) Write(body []byte) (int, error) {
	if w.status == 0 {
		w.WriteHeader(200)
	}
	return w.ResponseWriter.Write(body)
}
func (w *diagnosticWriter) Flush() {
	if w.status == 0 {
		w.WriteHeader(200)
	}
	_ = http.NewResponseController(w.ResponseWriter).Flush()
}

func (a *App) serveRequestDiagnostics(w http.ResponseWriter, req *http.Request) {
	if req.Method != http.MethodGet {
		writeAPIError(w, 405, "method_not_allowed", "不支持此操作", "")
		return
	}
	params := req.URL.Query()
	q := diagnostics.Query{RequestID: params.Get("request_id"), Subject: params.Get("subject"), ClientID: params.Get("client"), Endpoint: params.Get("endpoint"), Tool: params.Get("tool"), Outcome: params.Get("outcome"), Limit: 25}
	var err error
	if params.Has("limit") {
		q.Limit, err = strconv.Atoi(params.Get("limit"))
	}
	if err != nil || q.Limit < 1 || q.Limit > 100 || len(q.RequestID) > 128 || len(q.Subject) > 1024 || len(q.ClientID) > 128 || len(q.Endpoint) > 32 || len(q.Tool) > 128 || len(q.Outcome) > 64 {
		writeAPIError(w, 400, "validation_failed", "请求筛选条件无效", "")
		return
	}
	if params.Get("cursor") != "" {
		q.Before, err = strconv.ParseUint(params.Get("cursor"), 10, 63)
		if err != nil {
			writeAPIError(w, 400, "validation_failed", "请求筛选条件无效", "cursor")
			return
		}
	}
	now := time.Now().UTC()
	retention := a.currentConfig().Admin.RequestRetentionDuration()
	q.Until = now
	if params.Get("until") != "" {
		q.Until, err = time.Parse(time.RFC3339Nano, params.Get("until"))
		if err != nil {
			writeAPIError(w, 400, "validation_failed", "结束时间必须为 RFC3339 时间", "until")
			return
		}
	}
	if q.Until.After(now) {
		q.Until = now
	}
	q.Since = q.Until.Add(-24 * time.Hour)
	if params.Get("since") != "" {
		q.Since, err = time.Parse(time.RFC3339Nano, params.Get("since"))
		if err != nil {
			writeAPIError(w, 400, "validation_failed", "开始时间必须为 RFC3339 时间", "since")
			return
		}
	}
	if earliest := now.Add(-retention); q.Since.Before(earliest) {
		q.Since = earliest
	}
	if !q.Since.Before(q.Until) || (params.Get("format") != "" && params.Get("format") != "ndjson") {
		writeAPIError(w, 400, "validation_failed", "请求时间范围或导出格式无效", "")
		return
	}
	if a.store == nil {
		writeJSON(w, 200, a.requests.Query(q))
		return
	}
	if params.Get("format") == "ndjson" {
		q.Limit = 10000
		records, next, err := a.store.RequestRecords(req.Context(), q)
		if err != nil {
			writeAPIError(w, 503, "request_history_unavailable", "暂时无法读取请求历史，请重试", "")
			return
		}
		w.Header().Set("Content-Type", "application/x-ndjson")
		w.Header().Set("Content-Disposition", `attachment; filename="mcphub-requests.ndjson"`)
		w.Header().Set("X-MCPHub-Next-Cursor", strconv.FormatUint(next, 10))
		encoder := json.NewEncoder(w)
		for _, record := range records {
			if err := encoder.Encode(record); err != nil {
				return
			}
		}
		return
	}
	page, err := a.store.RequestHistory(req.Context(), q)
	if err != nil {
		writeAPIError(w, 503, "request_history_unavailable", "暂时无法读取请求历史，请重试", "")
		return
	}
	page.RetentionDays = int(retention / (24 * time.Hour))
	page.StorageFailures = a.requestWriteFailures.Load()
	writeJSON(w, 200, page)
}
