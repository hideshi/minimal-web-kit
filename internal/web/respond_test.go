package web

import (
	"bytes"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestWriteJSONAndRequestIDAttr(t *testing.T) {
	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, nil))
	w := httptest.NewRecorder()
	w.Header().Set("X-Request-ID", "rid-1")
	WriteJSON(w, logger, "x encode failed", 201, map[string]string{"a": "b"})
	if w.Code != 201 || w.Body.String() != `{"a":"b"}` || w.Header().Get("Content-Type") != "application/json; charset=utf-8" {
		t.Fatalf("ok: %d %s", w.Code, w.Body.String())
	}
	w = httptest.NewRecorder()
	w.Header().Set("X-Request-ID", "rid-2")
	WriteJSON(w, logger, "x encode failed", 200, map[string]any{"secret": make(chan int)})
	if w.Code != 500 || w.Body.String() != `{"error":"internal_error"}` {
		t.Fatalf("failure: %d %s", w.Code, w.Body.String())
	}
	if !strings.Contains(logs.String(), "x encode failed") || !strings.Contains(logs.String(), "request_id=rid-2") || strings.Contains(logs.String(), "secret") {
		t.Fatalf("log: %s", logs.String())
	}
}
