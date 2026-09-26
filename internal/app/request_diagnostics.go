package app

import (
	"net/http"
	"strconv"

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
		q.Before, err = strconv.ParseUint(params.Get("cursor"), 10, 64)
		if err != nil {
			writeAPIError(w, 400, "validation_failed", "请求筛选条件无效", "cursor")
			return
		}
	}
	writeJSON(w, 200, a.requests.Query(q))
}
