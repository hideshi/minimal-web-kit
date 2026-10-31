package federatedauth

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"minimal-web-kit/features"
	"minimal-web-kit/internal/store"
	"minimal-web-kit/internal/web/guard"
)

// 共通認証でも、ファイルの送信はセッション・ロール・CSRFを確かめてから本文を渡す。
//
// 不変条件: MEDIA-INV-4
func TestAdminUpload(t *testing.T) {
	s, err := store.NewSQLiteStore(filepath.Join(t.TempDir(), "b.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err := s.DB().Exec(migrationSQL); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	var issuer string
	u, srv := newTestUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"active":true,"iss":%q,"sub":"sub","client_id":"client","mwk_auth":{"account_epoch":"1","client_epoch":"2","grant_epoch":"3"}}`, issuer)
	}, &now)
	issuer = srv.URL
	f := &Feature{
		logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
		cfg:      Config{Origin: "https://b.example"},
		csrf:     guard.NewCSRF(),
		now:      time.Now,
		upstream: u,
		store:    &sessionStore{db: s.DB(), issuer: srv.URL, now: time.Now},
	}
	token, err := f.store.login(context.Background(), "sub", testEpochs, time.Now().Unix())
	if err != nil {
		t.Fatal(err)
	}
	reached := 0
	handler := adminAPI{f}.Upload(1<<20, func(w http.ResponseWriter, r *http.Request, _ features.AdminSession, parts *multipart.Reader) {
		reached++
		if parts != nil {
			if _, err := parts.NextPart(); err != nil {
				t.Errorf("next part: %v", err)
			}
		}
	})
	send := func(session bool, csrf string, extra int64) *httptest.ResponseRecorder {
		var body bytes.Buffer
		mw := multipart.NewWriter(&body)
		if err := mw.WriteField("csrf_token", csrf); err != nil {
			t.Fatal(err)
		}
		if err := mw.WriteField("alt", "説明"); err != nil {
			t.Fatal(err)
		}
		if err := mw.Close(); err != nil {
			t.Fatal(err)
		}
		r := httptest.NewRequest(http.MethodPost, "/admin/media", &body)
		r.Header.Set("Content-Type", mw.FormDataContentType())
		r.Header.Set("Sec-Fetch-Site", "same-origin")
		r.ContentLength += extra
		if session {
			r.AddCookie(&http.Cookie{Name: sessionCookieName, Value: token})
		}
		w := httptest.NewRecorder()
		handler(w, r)
		return w
	}
	if w := send(false, "x", 0); w.Code != http.StatusSeeOther || reached != 0 {
		t.Fatalf("no session: %d", w.Code)
	}
	if w := send(true, "wrong", 0); w.Code != http.StatusForbidden || reached != 0 {
		t.Fatalf("bad token: %d", w.Code)
	}
	if send(true, f.csrf.Token(token), 0); reached != 1 {
		t.Fatalf("valid token: %d", reached)
	}
	if send(true, "x", 2<<20); reached != 2 {
		t.Fatalf("too large must reach the handler without a reader: %d", reached)
	}
}
