package app

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"

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

	// アクセス解析の設定を、計測する画面を持つ機能に配る。設定が不正なら起動を止める
	for _, f := range reg.All() {
		p, ok := f.(features.AnalyticsProvider)
		if !ok {
			continue
		}
		analytics, err := p.Analytics()
		if err != nil {
			logger.Error("analytics config invalid")
			return nil, fmt.Errorf("analytics config: %w", err)
		}
		handler.SetAnalytics(analytics)
		for _, c := range reg.All() {
			if consumer, ok := c.(features.AnalyticsConsumer); ok {
				consumer.SetAnalytics(analytics)
			}
		}
	}

	// 機能が保存する情報を、外部の人の情報はプライバシーポリシーの画面に、社内の人の情報はログイン後の画面に載せる
	{
		var public, internal []web.PrivacyNotice
		for _, f := range reg.All() {
			p, ok := f.(features.PrivacyFeature)
			if !ok {
				continue
			}
			for _, n := range p.PrivacyNotices() {
				if n.Internal {
					internal = append(internal, n)
				} else {
					public = append(public, n)
				}
			}
		}
		handler.SetPrivacyNotices(public)
		received := false
		for _, f := range reg.All() {
			if c, ok := f.(features.AccountNoticeConsumer); ok {
				c.SetAccountNotices(internal)
				received = true
			}
		}
		if len(internal) > 0 && !received {
			// 公開にもログイン後にも出ないと、本人に知らせる場所が無くなる
			logger.Error("internal notices have no page")
			return nil, fmt.Errorf("internal privacy notices need a feature that shows them after login")
		}
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

	// 公開の記事の新着を、本文にお知らせの一覧を描く機能（固定ページ）に配る。提供は1つまで
	{
		var provider features.NewsListFeature
		for _, f := range reg.All() {
			if p, ok := f.(features.NewsListFeature); ok {
				if provider != nil {
					return nil, fmt.Errorf("news list is provided by both %s and %s", provider.Name(), f.Name())
				}
				provider = p
			}
		}
		if provider != nil {
			for _, f := range reg.All() {
				if c, ok := f.(features.NewsListConsumer); ok {
					c.SetNewsList(provider.LatestPublished)
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

	// 画像機能を、画像を使う機能に配る
	{
		var provider features.MediaProvider
		var owners []features.MediaOwner
		for _, f := range reg.All() {
			if p, ok := f.(features.MediaProvider); ok {
				provider = p
			}
			if o, ok := f.(features.MediaOwner); ok {
				owners = append(owners, o)
			}
		}
		if provider != nil {
			provider.SetMediaOwners(owners)
			for _, f := range reg.All() {
				if c, ok := f.(features.MediaConsumer); ok {
					c.SetMedia(provider.Media())
				}
			}
		}
	}

	// アカウントの一覧を、相手を選ぶ機能に配る
	{
		var directory features.AccountDirectory
		for _, f := range reg.All() {
			if p, ok := f.(features.AccountDirectoryProvider); ok {
				directory = p.AccountDirectory()
			}
		}
		for _, f := range reg.All() {
			c, ok := f.(features.AccountDirectoryConsumer)
			if !ok {
				continue
			}
			if directory == nil {
				logger.Error("feature requires the account directory", slog.String("feature", f.Name()))
				return nil, fmt.Errorf("feature %s requires the auth feature with account names (federatedauth has none)", f.Name())
			}
			c.SetAccountDirectory(directory)
		}
	}

	// エージェントの認証の提供者とMCPのツールを、MCPのサーバーに配る
	{
		var mcpFeatures []features.MCPFeature
		for _, f := range reg.All() {
			if m, ok := f.(features.MCPFeature); ok {
				mcpFeatures = append(mcpFeatures, m)
			}
		}
		for _, f := range reg.All() {
			c, ok := f.(features.AgentAuthConsumer)
			if !ok {
				continue
			}
			var provider features.AgentAuthProvider
			for _, candidate := range reg.All() {
				p, ok := candidate.(features.AgentAuthProvider)
				if !ok {
					continue
				}
				if provider != nil {
					return nil, fmt.Errorf("agent authentication is provided twice; register only one")
				}
				provider = p
			}
			if provider == nil {
				logger.Error("feature requires agent authentication", slog.String("feature", f.Name()))
				return nil, fmt.Errorf("feature %s requires the auth feature (run: make add-auth)", f.Name())
			}
			auth, err := provider.AgentAuth(c.AgentAuthConfig())
			if err != nil {
				return nil, fmt.Errorf("init agent auth for %s: %w", f.Name(), err)
			}
			if err := c.SetAgentAuth(auth); err != nil {
				return nil, fmt.Errorf("init agent auth for %s: %w", f.Name(), err)
			}
		}
		for _, f := range reg.All() {
			if c, ok := f.(features.MCPConsumer); ok {
				if err := c.SetMCPFeatures(mcpFeatures); err != nil {
					return nil, fmt.Errorf("init mcp: %w", err)
				}
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

	// トップページを固定ページで描く（HOME_PAGE_SLUG）
	home, err := homePage(reg)
	if err != nil {
		logger.Error("home page", slog.String("error", err.Error()))
		return nil, err
	}
	if home != nil {
		handler.SetHome(home.ServeHomePage)
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
	if err := checkHomeSlug(home, handler.RouteTaken); err != nil {
		return nil, err
	}
	return httpHandler, nil
}

// homePage は、トップページの指定（HOME_PAGE_SLUG）を描く機能を返します。指定が無ければ nil です。
// 指定があるのに、描く機能（固定ページ）が無ければ起動を止めます（PAGES-INV-15）。
func homePage(reg *features.Registry) (features.HomePageFeature, error) {
	for _, f := range reg.All() {
		if h, ok := f.(features.HomePageFeature); ok && h.HomeSlug() != "" {
			return h, nil
		}
	}
	if os.Getenv("HOME_PAGE_SLUG") != "" {
		return nil, errors.New("HOME_PAGE_SLUG is set but the pages feature is not enabled; run make add-pages or unset it")
	}
	return nil, nil
}

// checkHomeSlug は、トップページのスラッグが既存の経路（/privacy 等）に当たらないかを確かめます（PAGES-INV-15）。
// 当たると、そのページは /{スラッグ} で公開されず、/ だけに出ることになり、設定の誤りに気づけないため起動を止めます。
func checkHomeSlug(home features.HomePageFeature, taken func(path string) bool) error {
	if home == nil {
		return nil
	}
	if slug := home.HomeSlug(); taken("/" + slug) {
		return fmt.Errorf("HOME_PAGE_SLUG %q is already used by another route; choose another page", slug)
	}
	return nil
}
