package web

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// 本文の上限は、登録した経路だけが広がり、ほかの経路は既定のまま。
func TestMaxBytesByRoute(t *testing.T) {
	read := func(w http.ResponseWriter, r *http.Request) {
		if _, err := io.ReadAll(r.Body); err != nil {
			w.WriteHeader(http.StatusRequestEntityTooLarge)
		}
	}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /upload", read)
	mux.HandleFunc("POST /upload/{id}", read)
	mux.HandleFunc("POST /other", read)
	h := maxBytesByRoute(mux, map[string]int64{"POST /upload": 100}, 10)(mux)
	for _, c := range []struct {
		path string
		size int
		want int
	}{
		{"/upload", 100, http.StatusOK},
		{"/upload", 101, http.StatusRequestEntityTooLarge},
		{"/upload/1", 11, http.StatusRequestEntityTooLarge},
		{"/other", 10, http.StatusOK},
		{"/other", 11, http.StatusRequestEntityTooLarge},
	} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("POST", c.path, strings.NewReader(strings.Repeat("x", c.size))))
		if w.Code != c.want {
			t.Errorf("%s %d: %d", c.path, c.size, w.Code)
		}
	}
}

// ResponseController が、ログの包みの内側の ResponseWriter に届く。
func TestStatusResponseWriter_Unwrap(t *testing.T) {
	inner := httptest.NewRecorder()
	rw := &statusResponseWriter{ResponseWriter: inner}
	if rw.Unwrap() != http.ResponseWriter(inner) {
		t.Fatal("Unwrap must return the inner writer")
	}
}
