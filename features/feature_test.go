package features

import (
	"database/sql"
	"net/http"
	"testing"

	"minimal-web-kit/internal/web"
)

type linkFeature struct {
	name  string
	links []web.NavLink
}

func (f *linkFeature) Name() string                                              { return f.name }
func (f *linkFeature) Init(db *sql.DB) error                                     { return nil }
func (f *linkFeature) RegisterRoutes(mux *http.ServeMux, sm *web.SessionManager) {}
func (f *linkFeature) NavLinks() []web.NavLink                                   { return f.links }

type plainFeature struct{}

func (plainFeature) Name() string                                              { return "plain" }
func (plainFeature) Init(db *sql.DB) error                                     { return nil }
func (plainFeature) RegisterRoutes(mux *http.ServeMux, sm *web.SessionManager) {}

func TestRegistry_NavLinks(t *testing.T) {
	r := NewRegistry()
	r.Register(plainFeature{})
	r.Register(&linkFeature{name: "a", links: []web.NavLink{{Label: "お知らせ", Href: "/news"}}})
	r.Register(&linkFeature{name: "b", links: []web.NavLink{{Label: "イベント", Href: "/events?x=1"}}})
	links, err := r.NavLinks()
	if err != nil || len(links) != 2 || links[0].Href != "/news" || links[1].Href != "/events?x=1" {
		t.Fatalf("links=%v err=%v", links, err)
	}

	if links, err := NewRegistry().NavLinks(); err != nil || len(links) != 0 {
		t.Fatalf("no features: links=%v err=%v", links, err)
	}

	// サイト内のパス以外は起動時に拒否する
	for _, bad := range []web.NavLink{
		{Label: "", Href: "/news"},
		{Label: "外部", Href: "https://example.com/"},
		{Label: "外部", Href: "//example.com/"},
		{Label: "外部", Href: "/\\example.com"},
		{Label: "相対", Href: "news"},
		{Label: "JS", Href: "javascript:alert(1)"},
	} {
		r := NewRegistry()
		r.Register(&linkFeature{name: "bad", links: []web.NavLink{bad}})
		if _, err := r.NavLinks(); err == nil {
			t.Errorf("%+v must be rejected", bad)
		}
	}
}
