package web

import (
	"encoding/json"
	"log/slog"
	"net/http"

	"minimal-web-kit/internal/web/form"
)

// RequestIDAttr は、ログに添えるサーバー生成のリクエストID（RequestID ミドルウェアが応答のヘッダーに付けたもの）です。
// 利用者の入力を含まないため、ログに出してよい値です。
func RequestIDAttr(w http.ResponseWriter) slog.Attr {
	return slog.String("request_id", w.Header().Get("X-Request-ID"))
}

// WriteJSON は v をJSONで返します。変換に失敗したら、固定の文言（event）だけをログに出し、
// 500と {"error":"internal_error"} を返します（v の中身はログに出しません）。
func WriteJSON(w http.ResponseWriter, logger *slog.Logger, event string, status int, v any) {
	body, err := json.Marshal(v)
	if err != nil {
		logger.Error(event, RequestIDAttr(w))
		status, body = http.StatusInternalServerError, []byte(`{"error":"internal_error"}`)
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write(body)
}

// PathID は、経路の値（例: {id}）をデータの番号として読みます（form.ID の規則）。読めなければ404を返し、ok=false にします。
func PathID(w http.ResponseWriter, r *http.Request, name string) (id int64, ok bool) {
	id, ok = form.ID(r.PathValue(name))
	if !ok {
		http.NotFound(w, r)
	}
	return id, ok
}
