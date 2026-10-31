package web

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestWriteBusy_InvitesRetry(t *testing.T) {
	w := httptest.NewRecorder()
	WriteBusy(w)
	if w.Code != http.StatusServiceUnavailable || w.Header().Get("Retry-After") != "1" {
		t.Fatalf("status %d, Retry-After %q", w.Code, w.Header().Get("Retry-After"))
	}
}
