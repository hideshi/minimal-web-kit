// minimal-web-kit: bundled-feature pages
package pages

import (
	"bytes"
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"html/template"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"minimal-web-kit/features"
	"minimal-web-kit/internal/web"
	"minimal-web-kit/internal/web/ogimage"
	"minimal-web-kit/internal/web/richtext"
)

//go:embed migration.sql
var migrationSQL string

//go:embed views/*.html
var viewsFS embed.FS

// Feature は同梱機能の固定ページ（pages）です。管理画面で作った /{slug} のページを公開します。
// 公開ページは、どの経路にも当たらなかったGET・HEADを受けるCoreの受け皿（FallbackFeature）で返します（docs/plans/pages-plan.md 2.1）。
type Feature struct {
	web.PublicNavigation
	logger     *slog.Logger
	store      *Store
	admin      features.Admin
	media      features.Media
	notFound   http.HandlerFunc
	taken      func(string) bool
	layout     *web.Layout
	publicPage *template.Template
	adminPages map[string]*template.Template
	now        func() time.Time

	// rebuildMu は、ナビの作り直し（DBの読み取りからスナップショットの交換まで）を直列にします。
	// 古い読み取りの結果が、後から新しい結果を上書きしないためです。
	rebuildMu sync.Mutex
	mu        sync.RWMutex
	nav       []navEntry // 公開の状態でナビの順のあるページ（予約中を含む。スナップショット。PAGES-INV-6）
	// afterNavRead は、テストで、ナビの読み取りと交換の間に割り込むための口です。本番では nil です。
	afterNavRead func()
	// ogSlots は、OGPの画像を同時に作る数の枠です（PAGES-INV-14）。
	ogSlots chan struct{}
}

// navEntry はナビの候補です。publishAt を過ぎるまでは載せません。
type navEntry struct {
	link      web.NavLink
	publishAt int64 // 0 なら公開すればすぐ載る
}

func NewFeature(logger *slog.Logger) *Feature {
	return &Feature{
		logger:     logger,
		publicPage: template.Must(web.ParsePage(web.PublicLayout(), viewsFS, "views/page.html")),
		notFound:   http.NotFound,
		ogSlots:    make(chan struct{}, ogMaxSlots),
		now:        time.Now,
		media:      features.NoMedia{},
	}
}

// SetMedia は画像機能を受け取ります。
func (f *Feature) SetMedia(m features.Media) {
	f.media = m
	if f.store != nil {
		f.store.media = m
	}
}

// MediaIsPublic は、ページが今公開中で、本文がその画像を使っているかを返します。
func (f *Feature) MediaIsPublic(ctx context.Context, key, file string) (bool, error) {
	id, err := strconv.ParseInt(key, 10, 64)
	if err != nil {
		return false, nil
	}
	return f.store.publishedUses(ctx, id, file)
}

// MediaReferences は、全てのページと、本文が使う画像を返します。
func (f *Feature) MediaReferences(ctx context.Context) (map[string][]string, error) {
	return f.store.imageRefs(ctx)
}

// resolveImages は、本文の画像の大きさとURLを埋めます。見つからない画像は説明の文字になります。
// preview なら管理者の経路で描きます（下書きの画像は公開の経路で見えないため）。
func (f *Feature) resolveImages(ctx context.Context, doc *richtext.Doc, preview bool) error {
	images, err := f.media.Images(ctx, doc.Images())
	if err != nil {
		return err
	}
	doc.ResolveImages(func(file string) (string, int, int, bool) {
		img, ok := images[file]
		if preview {
			return img.AdminSrc, img.Width, img.Height, ok
		}
		return img.Src, img.Width, img.Height, ok
	})
	return nil
}

func (f *Feature) Name() string { return "pages" }

// Init はマイグレーションを適用し、ナビのスナップショットを作ります。
func (f *Feature) Init(db *sql.DB) error {
	if _, err := db.Exec(migrationSQL); err != nil {
		return fmt.Errorf("pages migration: %w", err)
	}
	// 予約公開の列を、既存のDBに足す。NULL は「公開すればすぐ出る」で、既存の行の公開の状態を変えない
	var hasPublishAt int
	if err := db.QueryRow("SELECT COUNT(*) FROM pragma_table_info('pages') WHERE name = 'publish_at'").Scan(&hasPublishAt); err != nil {
		return fmt.Errorf("pages migration: %w", err)
	}
	if hasPublishAt == 0 {
		if _, err := db.Exec("ALTER TABLE pages ADD COLUMN publish_at INTEGER"); err != nil {
			return fmt.Errorf("pages migration: %w", err)
		}
	}
	f.store = NewStore(db)
	f.store.now = func() time.Time { return f.now() }
	if err := f.warnLPLines(context.Background()); err != nil {
		return fmt.Errorf("pages lp check: %w", err)
	}
	f.rebuildMu.Lock()
	defer f.rebuildMu.Unlock()
	return f.refreshNav(context.Background())
}

func (f *Feature) RegisterRoutes(mux *http.ServeMux, sm *web.SessionManager) {}

// RegisterIsolatedRoutes は管理画面を登録します（公開ページは受け皿で返す）。
func (f *Feature) RegisterIsolatedRoutes(mux *web.IsolatedMux) {
	mux.HandleFunc("GET /ogp/pages/{name}", f.handleOGImage)
	if f.admin != nil {
		f.registerAdmin(mux)
	}
}

func (f *Feature) SetNotFound(h http.HandlerFunc)        { f.notFound = h }
func (f *Feature) SetRouteTaken(taken func(string) bool) { f.taken = taken }

// SetLayout は、公開ページをサイト全体のレイアウトで描くようにします。
func (f *Feature) SetLayout(layout web.Layout) error {
	t, err := web.ParsePage(layout, viewsFS, "views/page.html")
	if err != nil {
		return err
	}
	f.publicPage, f.layout = t, &layout
	return nil
}

// refreshNav は、公開の状態でナビの順のあるページ（予約中を含む）を読み直します。保存・公開・非公開・削除の後に呼びます。
// 呼ぶ側が rebuildMu を持ちます。予約中のものは、描画のたびに DynamicNavLinks が時刻で絞ります。
func (f *Feature) refreshNav(ctx context.Context) error {
	rows, err := f.store.db.QueryContext(ctx, "SELECT slug, title, nav_order, COALESCE(publish_at, 0) FROM pages WHERE status = 'published' AND nav_order IS NOT NULL")
	if err != nil {
		return err
	}
	defer rows.Close()
	type entry struct {
		navEntry
		order int
	}
	var entries []entry
	for rows.Next() {
		var e entry
		if err := rows.Scan(&e.link.Href, &e.link.Label, &e.order, &e.publishAt); err != nil {
			return err
		}
		e.link.Href = "/" + e.link.Href
		entries = append(entries, e)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	sort.SliceStable(entries, func(i, j int) bool {
		if entries[i].order != entries[j].order {
			return entries[i].order < entries[j].order
		}
		return entries[i].link.Href < entries[j].link.Href
	})
	_ = rows.Close() // 読み取りを先に終える（読み取りの間、書き込みを待たせない）
	if f.afterNavRead != nil {
		f.afterNavRead()
	}
	nav := make([]navEntry, len(entries))
	for i, e := range entries {
		nav[i] = e.navEntry
	}
	f.mu.Lock()
	f.nav = nav
	f.mu.Unlock()
	return nil
}

// Purge は、定期のGCのたびにナビを作り直します。作り直しに失敗して外れたリンクを、再起動なしで戻します。
func (f *Feature) Purge(ctx context.Context) error {
	if f.store == nil {
		return nil
	}
	f.rebuildMu.Lock()
	defer f.rebuildMu.Unlock()
	return f.refreshNav(ctx)
}

// DynamicNavLinks は、公開ナビに載せるページのリンクを返します（描画のたびに呼ばれる。DBは読まない）。
// 予約中のページは、公開日時を過ぎた後の最初の描画から載ります（PAGES-INV-6）。
func (f *Feature) DynamicNavLinks() []web.NavLink {
	now := f.now().Unix()
	f.mu.RLock()
	defer f.mu.RUnlock()
	links := make([]web.NavLink, 0, len(f.nav))
	for _, e := range f.nav {
		if e.publishAt <= now {
			links = append(links, e.link)
		}
	}
	return links
}

// SitemapEntries は、公開中のページを sitemap.xml に載せます。
func (f *Feature) SitemapEntries(ctx context.Context) ([]web.SitemapEntry, error) {
	rows, err := f.store.db.QueryContext(ctx, "SELECT slug, updated_at FROM pages WHERE "+publicPage+" ORDER BY slug", f.now().Unix())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []web.SitemapEntry
	for rows.Next() {
		var slug string
		var at int64
		if err := rows.Scan(&slug, &at); err != nil {
			return nil, err
		}
		out = append(out, web.SitemapEntry{Path: "/" + slug, LastMod: time.Unix(at, 0)})
	}
	return out, rows.Err()
}

// HandleFallback は、どの経路にも当たらなかったGET・HEADを受け、1階層で公開中のページなら描き、それ以外は404にします。
// Cookieは発行・照合しません（PAGES-INV-5）。
func (f *Feature) HandleFallback(w http.ResponseWriter, r *http.Request) {
	slug := strings.TrimPrefix(r.URL.Path, "/")
	if !slugPattern.MatchString(slug) {
		f.notFound(w, r)
		return
	}
	p, err := f.store.Published(r.Context(), slug)
	if errors.Is(err, ErrNotFound) {
		f.notFound(w, r)
		return
	} else if err != nil {
		// 取得の失敗を404にすると、障害の間に公開中のページが「無い」と扱われる（検索エンジン等）ため、500にする
		f.logger.Error("pages load failed", web.RequestIDAttr(w))
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}
	doc, err := richtext.ParseForPages(p.Body)
	if err != nil {
		// 保存のときに検査しているため、ここには来ない想定
		f.logger.Error("pages body over limit", slog.Int64("page_id", p.ID))
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}
	if doc.Hero != nil {
		doc.Hero.Title = p.Title
	}
	if err := f.resolveImages(r.Context(), &doc, false); err != nil {
		f.logger.Error("pages images failed", web.RequestIDAttr(w))
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}
	data := map[string]any{
		"Title":         p.Title,
		"CanonicalPath": "/" + p.Slug,
		"Description":   description(doc, p.Title),
		"Page":          p,
		"Doc":           doc,
	}
	f.AddNavigation(data)
	f.AddAnalytics(w, data)
	f.addOGImage(data, p, doc)
	if f.layout != nil && f.layout.Data != nil {
		f.layout.Data(data)
	}
	var buf bytes.Buffer
	if err := f.publicPage.ExecuteTemplate(&buf, "layout", data); err != nil {
		f.logger.Error("pages render failed")
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}
	// 公開・非公開をすぐ反映するため、公開キャッシュは使わない（Coreの既定の no-store のまま）
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = buf.WriteTo(w)
}

// description は、検索結果等に出す説明文を、ヒーローの導入か、無ければ本文の最初の段落から作ります（120文字まで）。
func description(doc richtext.Doc, fallback string) string {
	if doc.Hero != nil {
		return clip(richtext.Plain(doc.Hero.Inlines))
	}
	for _, b := range doc.Blocks {
		if b.Kind != "p" {
			continue
		}
		var s strings.Builder
		for _, line := range b.Lines {
			s.WriteString(richtext.Plain(line))
			s.WriteString(" ")
		}
		return clip(s.String())
	}
	return fallback
}

// clip は、空白を1つにそろえ、120文字までにします。
func clip(s string) string {
	text := []rune(strings.Join(strings.Fields(s), " "))
	if len(text) > 120 {
		return string(text[:120]) + "…"
	}
	return string(text)
}

// warnLPLines は、本文がLPの部品（ヒーロー・ボタン・カード）として描かれるページのIDを、起動の時に警告のログに出します
// （本文は出さない。何も変えない）。部品の読み方を足す前の版から上げたサイトで、見え方が変わったページに気づくため。
// 意図して部品を使うページも毎回出る。保存した時点を残すと承認境界2に当たるので、区別しない。
func (f *Feature) warnLPLines(ctx context.Context) error {
	ids, err := f.store.LPPartPages(ctx)
	if err != nil || len(ids) == 0 {
		return err
	}
	f.logger.Warn("pages render LP parts (hero, buttons, cards); if a page was written before LP parts existed, check it and escape the colon (ボタン\\:) to keep a line as text", slog.Any("page_ids", ids))
	return nil
}

// ---- OGPの画像（docs/plans/lp-plan.md 4節） ----

const (
	ogKind     = "pages"
	ogMaxSlots = 2
)

// ogName は、OGPの画像のファイル名 {スラッグ}-{版12桁}.png です。
var ogName = regexp.MustCompile(`^([a-z0-9][a-z0-9-]{0,38}[a-z0-9])-([0-9a-f]{12})\.png$`)

// ogHost は、OGPの画像に描くサイトのホスト名です。SITE_URL が無い構成では空で、自動の画像を作りません。
func (f *Feature) ogHost() string {
	if f.layout == nil || f.layout.SiteURL == "" {
		return ""
	}
	u, err := url.Parse(f.layout.SiteURL)
	if err != nil {
		return ""
	}
	return u.Host
}

// ogImagePath は、自動の画像のパスです。作れない（ホスト名が無い、フォントに無い文字がある）なら空です。
func (f *Feature) ogImagePath(p Page) string {
	host := f.ogHost()
	if host == "" || !ogimage.Drawable(p.Title, host) {
		return ""
	}
	return "/ogp/pages/" + p.Slug + "-" + ogimage.Version(ogKind, p.Title, host) + ".png"
}

// addOGImage は、公開ページのOGPの画像を決めます。本文の最初の画像が公開用に解決できればそれ、無ければ題名から作る画像、
// どちらも無ければ何も入れない（Site の共通の画像になる）。幅・高さ・説明は一式で入れます。
// 不変条件: PAGES-INV-14
func (f *Feature) addOGImage(data map[string]any, p Page, doc richtext.Doc) {
	for _, b := range doc.Blocks {
		if b.Kind == "image" {
			if b.Src != "" {
				data["OGImagePath"], data["OGImageAlt"], data["OGImageWidth"], data["OGImageHeight"] = b.Src, b.Alt, b.Width, b.Height
				return
			}
			break
		}
	}
	if path := f.ogImagePath(p); path != "" {
		data["OGImagePath"], data["OGImageAlt"], data["OGImageWidth"], data["OGImageHeight"] = path, p.Title, ogimage.Width, ogimage.Height
	}
}

// handleOGImage は、公開中のページの題名からOGPの画像を作って返します。版が今の題名と合わない、公開中でない、
// 描けない時は404。DBの障害は500、同時に作る枠が無い時は503。Cookieを読まず、セッションを作りません。
// 成功だけ長くキャッシュさせ、失敗の応答はCoreの既定（no-store）のままにします。
// 不変条件: PAGES-INV-14
func (f *Feature) handleOGImage(w http.ResponseWriter, r *http.Request) {
	m := ogName.FindStringSubmatch(r.PathValue("name"))
	if m == nil {
		f.notFound(w, r)
		return
	}
	p, err := f.store.Published(r.Context(), m[1])
	if errors.Is(err, ErrNotFound) {
		f.notFound(w, r)
		return
	} else if err != nil {
		f.logger.Error("pages og load failed", web.RequestIDAttr(w))
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}
	path := f.ogImagePath(p)
	if path == "" || path != "/ogp/pages/"+m[0] {
		f.notFound(w, r)
		return
	}
	select {
	case f.ogSlots <- struct{}{}:
		defer func() { <-f.ogSlots }()
	default:
		w.Header().Set("Retry-After", "1")
		http.Error(w, "Service Unavailable", http.StatusServiceUnavailable)
		return
	}
	png, err := ogimage.Render(p.Title, f.ogHost())
	if err != nil {
		f.logger.Error("pages og render failed", web.RequestIDAttr(w))
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Content-Length", strconv.Itoa(len(png)))
	w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	if r.Method == http.MethodHead {
		return
	}
	_, _ = w.Write(png)
}

// ReadOnlyCommands は、DBを開かないコマンドです。licenses は、OGPの画像のために組み込んだフォントの著作権表示と
// ライセンス（SIL Open Font License 1.1）の全文を出します。実行ファイルだけを配る場合に、ライセンスを一緒に示すためです。
func (f *Feature) ReadOnlyCommands() []features.ReadOnlyCommand {
	return []features.ReadOnlyCommand{{
		Name:    "licenses",
		Summary: "組み込んだフォントの著作権表示とライセンスを出す",
		Run: func(_ context.Context, _ string, _ []string, out io.Writer) error {
			_, err := fmt.Fprintf(out, "%s\n\n%s", ogimage.Copyright, ogimage.License)
			return err
		},
	}}
}
