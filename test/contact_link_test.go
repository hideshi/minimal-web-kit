package test

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"minimal-web-kit/features"
	"minimal-web-kit/features/auth"
	"minimal-web-kit/features/contact"
	"minimal-web-kit/features/site"
	"minimal-web-kit/internal/app"
	"minimal-web-kit/internal/store"
)

func contactLinkRoutes(t *testing.T, withContact bool) (http.Handler, error) {
	t.Helper()
	t.Setenv("AUTH_ORIGIN", "")
	t.Setenv("AUTH_RP_ID", "")
	core, err := store.NewSQLiteStore(filepath.Join(t.TempDir(), "app.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { core.Close() })
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	reg := features.NewRegistry()
	reg.Register(site.NewFeature(logger))
	if withContact {
		reg.Register(auth.NewFeature(logger))
		reg.Register(contact.NewFeature(logger))
	}
	for _, f := range reg.All() {
		if err := f.Init(core.DB()); err != nil {
			t.Fatal(err)
		}
	}
	return app.BuildRoutes(core, reg, false, logger)
}

func body(t *testing.T, routes http.Handler, path string) string {
	t.Helper()
	w := httptest.NewRecorder()
	routes.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
	return w.Body.String()
}

// お問い合わせ機能を有効にすると、LPのボタンが同じタブで /contact を指し、プライバシーポリシーに保存する情報の表が載る。
// 外部フォーム（CONTACT_FORM_URL）とは同時に使えず、起動を止める。
func TestContactLink_SiteButtonPrivacyAndConflict(t *testing.T) {
	t.Setenv("CONTACT_FORM_URL", "")
	routes, err := contactLinkRoutes(t, true)
	if err != nil {
		t.Fatal(err)
	}
	lp := body(t, routes, "/")
	if !strings.Contains(lp, `<a href="/contact" class="btn btn-primary btn-cta">`) || strings.Contains(lp, "Googleフォーム") {
		t.Fatal("LP button must open /contact in the same tab")
	}
	privacy := body(t, routes, "/privacy")
	if !strings.Contains(privacy, "お問い合わせで保存する情報") || strings.Contains(privacy, "外部フォーム") {
		t.Fatal("privacy policy must describe the contact feature, not an external form")
	}

	t.Setenv("CONTACT_FORM_URL", "https://forms.example.com/x")
	if _, err := contactLinkRoutes(t, true); err == nil || !strings.Contains(err.Error(), "contact is provided by both") {
		t.Fatalf("both contact routes must stop the start: %v", err)
	}
	routes, err = contactLinkRoutes(t, false)
	if err != nil {
		t.Fatal(err)
	}
	if lp := body(t, routes, "/"); !strings.Contains(lp, `href="https://forms.example.com/x" target="_blank"`) {
		t.Fatal("external form must open in a new tab")
	}
	if !strings.Contains(body(t, routes, "/privacy"), "外部フォーム") {
		t.Fatal("external form section must be shown")
	}

	t.Setenv("CONTACT_FORM_URL", "")
	routes, _ = contactLinkRoutes(t, false)
	if lp := body(t, routes, "/"); !strings.Contains(lp, "準備中") {
		t.Fatal("no contact: placeholder")
	}
	if strings.Contains(body(t, routes, "/privacy"), "外部フォーム") {
		t.Fatal("no contact: no external section")
	}
}
