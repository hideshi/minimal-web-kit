package app

import (
	"fmt"
	"log/slog"
	"net/http"

	"minimal-web-kit/features"
	"minimal-web-kit/internal/store"
	"minimal-web-kit/internal/web"
)

// BuildRoutes は、有効な機能どうしをつなぎ（ナビのリンク、管理者認証、sitemap、レイアウト）、
// Coreと機能の経路を合成したハンドラを返します。機能の Init は呼びません（起動処理で先に呼ぶ）。
// テストからも使い、起動時と同じつなぎ方を確かめられるようにしています。
func BuildRoutes(st store.SessionStore, reg *features.Registry, secureCookie bool, logger *slog.Logger) (http.Handler, error) {
	if reg == nil {
		reg = features.NewRegistry()
	}
	sm := web.NewSessionManager(st, secureCookie)
	handler, err := web.NewHandler(st, sm, logger)
	if err != nil {
		return nil, fmt.Errorf("init handler: %w", err)
	}

	// 有効な機能が宣言したナビゲーションのリンクを、Coreの画面と独自のナビを持つ機能に配る
	{
		links, err := reg.NavLinkSource()
		if err != nil {
			logger.Error("feature nav links invalid")
			return nil, fmt.Errorf("init nav links: %w", err)
		}
		handler.SetNavLinks(links)
		for _, f := range reg.All() {
			if c, ok := f.(features.NavLinkConsumer); ok {
				c.SetNavLinks(links)
			}
		}
	}

	// 機能が保存する情報（閲覧者以外の人の情報）を、プライバシーポリシーの画面に載せる
	{
		var notices []web.PrivacyNotice
		for _, f := range reg.All() {
			if p, ok := f.(features.PrivacyFeature); ok {
				notices = append(notices, p.PrivacyNotices()...)
			}
		}
		handler.SetPrivacyNotices(notices)
	}

	// お問い合わせの行き先を、ボタンを描く機能とプライバシーポリシーに配る。申告は1つまで
	{
		var link *web.ContactLink
		var from string
		for _, f := range reg.All() {
			cf, ok := f.(features.ContactLinkFeature)
			if !ok {
				continue
			}
			l, ok := cf.ContactLink()
			if !ok {
				continue
			}
			if link != nil {
				logger.Error("contact link declared twice", slog.String("feature", from), slog.String("other", f.Name()))
				return nil, fmt.Errorf("contact is provided by both %s and %s (unset CONTACT_FORM_URL to use the contact feature, or run: make remove-contact)", from, f.Name())
			}
			link, from = &l, f.Name()
		}
		if link != nil {
			handler.SetContactLink(*link)
			for _, f := range reg.All() {
				if c, ok := f.(features.ContactLinkConsumer); ok {
					c.SetContactLink(*link)
				}
			}
		}
	}

	// 管理者認証を、管理画面を持つ機能に配る。管理画面のナビのリンクを管理者認証に集める
	{
		var admin features.Admin
		var provider string
		var navLinks []web.NavLink
		for _, f := range reg.All() {
			if p, ok := f.(features.AdminProvider); ok {
				if admin != nil {
					logger.Error("admin authentication declared twice", slog.String("feature", provider), slog.String("other", f.Name()))
					return nil, fmt.Errorf("admin authentication is provided by both %s and %s; register only one", provider, f.Name())
				}
				admin, provider = p.Admin(), f.Name()
			}
			if n, ok := f.(features.AdminNavFeature); ok {
				navLinks = append(navLinks, n.AdminNavLinks()...)
			}
		}
		for _, f := range reg.All() {
			if c, ok := f.(features.AdminNavConsumer); ok {
				c.SetAdminNavLinks(navLinks)
			}
			c, ok := f.(features.AdminConsumer)
			if !ok {
				continue
			}
			if admin == nil {
				logger.Error("feature requires admin authentication", slog.String("feature", f.Name()))
				return nil, fmt.Errorf("feature %s requires the auth feature (run: make add-auth)", f.Name())
			}
			if err := c.SetAdmin(admin); err != nil {
				return nil, fmt.Errorf("init admin for %s: %w", f.Name(), err)
			}
		}
	}

	// sitemap.xml に載せるページを持つ機能を、sitemap.xml を返す機能に配る
	{
		var sources []features.SitemapFeature
		for _, f := range reg.All() {
			if sf, ok := f.(features.SitemapFeature); ok {
				sources = append(sources, sf)
			}
		}
		for _, f := range reg.All() {
			if c, ok := f.(features.SitemapConsumer); ok {
				c.SetSitemapSources(sources)
			}
		}
	}

	// サイト全体のレイアウトを、Coreの画面と公開ページを持つ機能に配る
	{
		for _, f := range reg.All() {
			lf, ok := f.(features.LayoutFeature)
			if !ok {
				continue
			}
			layout := lf.Layout()
			if err := handler.SetLayout(layout); err != nil {
				return nil, fmt.Errorf("init layout: %w", err)
			}
			for _, c := range reg.All() {
				if lc, ok := c.(features.LayoutConsumer); ok {
					if err := lc.SetLayout(layout); err != nil {
						return nil, fmt.Errorf("init layout for %s: %w", c.Name(), err)
					}
				}
			}
			break
		}
	}

	// ルートパス（GET /）の差し替え判定
	var rootHandler http.HandlerFunc
	{
		for _, f := range reg.All() {
			if cr, ok := f.(features.CustomRootFeature); ok {
				rootHandler = cr.HandleRoot
				break
			}
		}
	}

	// 受け皿（どの経路にも当たらないGET・HEAD）と、404の画面を配る
	{
		var fallback features.FallbackFeature
		notFound := http.NotFound
		for _, f := range reg.All() {
			if fb, ok := f.(features.FallbackFeature); ok {
				if fallback != nil {
					logger.Error("fallback declared twice", slog.String("feature", fallback.Name()), slog.String("other", f.Name()))
					return nil, fmt.Errorf("both %s and %s handle unmatched paths; enable only one", fallback.Name(), f.Name())
				}
				fallback = fb
			}
			if nf, ok := f.(features.NotFoundFeature); ok {
				notFound = nf.HandleNotFound
			}
		}
		for _, f := range reg.All() {
			if c, ok := f.(features.NotFoundConsumer); ok {
				c.SetNotFound(notFound)
			}
		}
		if fallback != nil {
			handler.SetFallback(http.HandlerFunc(fallback.HandleFallback))
		}
	}

	// ルーティング構築
	httpHandler, err := handler.RoutesWithIsolated(rootHandler, func(mux *http.ServeMux, sm *web.SessionManager) {
		{
			for _, f := range reg.All() {
				f.RegisterRoutes(mux, sm)
			}
		}
	}, func(mux *web.IsolatedMux) {
		{
			for _, f := range reg.All() {
				if iso, ok := f.(features.IsolatedRoutesFeature); ok {
					iso.RegisterIsolatedRoutes(mux)
				}
			}
		}
	})
	if err != nil {
		return nil, fmt.Errorf("init routes: %w", err)
	}
	for _, f := range reg.All() {
		if c, ok := f.(features.RouteCheckConsumer); ok {
			c.SetRouteTaken(handler.RouteTaken)
		}
	}
	return httpHandler, nil
}
