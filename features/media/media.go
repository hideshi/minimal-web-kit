// minimal-web-kit: bundled-feature media

// Package media は、運営者が登録した画像をSQLiteに保存し、記事から使い回せるようにする同梱機能です。
// 画像に公開状態は持たせず、公開中の記事から使われている間だけ公開します。仕様は docs/domain/media.md です。
package media

import (
	"bytes"
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"html/template"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"minimal-web-kit/features"
	"minimal-web-kit/internal/web"
	"minimal-web-kit/internal/web/richtext"
)

//go:embed migration.sql
var migrationSQL string

//go:embed views/*.html
var viewsFS embed.FS

const (
	defaultTotalMB = 300
	// publicCacheSeconds は、公開の画像のキャッシュの期間です。公開を取り消しても、この間は残り得ます。
	publicCacheSeconds = 86400
	pageSize           = 12
	maxPage            = 10000
)

// Feature は同梱機能の画像（media）です。
type Feature struct {
	logger     *slog.Logger
	store      *Store
	admin      features.Admin
	adminPages map[string]*template.Template
	owners     map[string]features.MediaOwner
	slots      chan struct{} // 加工の同時処理の枠
	slotWait   time.Duration // 枠が空くのを待つ長さ
	now        func() time.Time
}

func NewFeature(logger *slog.Logger) *Feature {
	return &Feature{logger: logger, owners: map[string]features.MediaOwner{}, slots: make(chan struct{}, 1), slotWait: 10 * time.Second, now: time.Now}
}

func (f *Feature) Name() string { return "media" }

// Init はマイグレーションを適用します。
func (f *Feature) Init(db *sql.DB) error {
	limit := int64(defaultTotalMB)
	if raw := os.Getenv("MEDIA_TOTAL_LIMIT_MB"); raw != "" {
		v, err := strconv.ParseInt(raw, 10, 32)
		if err != nil || v < 1 {
			return errors.New("MEDIA_TOTAL_LIMIT_MB must be a positive integer")
		}
		limit = v
	}
	if _, err := db.Exec(migrationSQL); err != nil {
		return fmt.Errorf("media migration: %w", err)
	}
	f.store = &Store{db: db, now: func() time.Time { return f.now() }, limit: limit * 1_000_000}
	return nil
}

func (f *Feature) RegisterRoutes(mux *http.ServeMux, sm *web.SessionManager) {}

// RegisterIsolatedRoutes は公開の経路と管理画面を登録します。公開の経路はCookieを読みません。
func (f *Feature) RegisterIsolatedRoutes(mux *web.IsolatedMux) {
	mux.HandleFunc("GET /media/{file}", f.handlePublic)
	if f.admin != nil {
		f.registerAdmin(mux)
	}
}

// Media は、画像を使う機能に渡す口を返します。
func (f *Feature) Media() features.Media { return api{f} }

// SetMediaOwners は、画像を使う機能を受け取ります。
func (f *Feature) SetMediaOwners(owners []features.MediaOwner) {
	for _, o := range owners {
		f.owners[o.Name()] = o
	}
}

// Startup は、待ち受けの前に、有効な機能の参照を作り直します（MEDIA-INV-7）。
// 機能を外している間に変わった記事があっても、公開の判定と使用中の判定がずれません。
func (f *Feature) Startup(ctx context.Context) error {
	for name, o := range f.owners {
		refs, err := o.MediaReferences(ctx)
		if err != nil {
			return fmt.Errorf("media references of %s: %w", name, err)
		}
		if err := f.store.Rebuild(ctx, name, refs); err != nil {
			return fmt.Errorf("media rebuild of %s: %w", name, err)
		}
	}
	return nil
}

// AdminNavLinks は管理画面のナビに「画像の管理」を加えます。
func (f *Feature) AdminNavLinks() []web.NavLink {
	return []web.NavLink{{Label: "画像の管理", Href: "/admin/media"}}
}

// PrivacyNotices は、公開ページに画像を載せることを説明します。
func (f *Feature) PrivacyNotices() []web.PrivacyNotice {
	return []web.PrivacyNotice{{
		Title: "画像機能で保存する情報",
		Lead:  "運営者が公開のために登録する画像です。閲覧者の情報は保存しません。",
		Rows: []web.PrivacyRow{{
			Kind:      "公開する画像",
			Items:     "縮小した画像、画像の説明、大きさ、登録日時（撮影位置等の付加情報は保存しません）",
			Purpose:   "お知らせ・カタログ・固定ページへの掲載",
			Retention: "運営者が削除するまで。削除した画像も、バックアップの保持期間の間は複製に残ります",
		}},
	}}
}

// isPublic は、画像が公開中の記事から使われているかを、記事の状態から判定します（MEDIA-INV-1）。
// 参照の記録は候補を探すためだけに使い、公開かどうかは記事が今その画像を使っているかで決めます。
// 外した機能の参照は公開中として数えません。
func (f *Feature) isPublic(ctx context.Context, id, file string) (bool, error) {
	refs, err := f.store.Refs(ctx, id)
	if err != nil {
		return false, err
	}
	for _, r := range refs {
		o, ok := f.owners[r.Owner]
		if !ok {
			continue
		}
		public, err := o.MediaIsPublic(ctx, r.Key, file)
		if err != nil {
			return false, err
		}
		if public {
			return true, nil
		}
	}
	return false, nil
}

// handlePublic は公開の経路です。公開中でない画像は、無い画像と同じ404にします。
// 404はキャッシュさせません（公開した直後に404が残らないように、Coreの既定の no-store のまま）。
// 不変条件: MEDIA-INV-1, MEDIA-INV-2
func (f *Feature) handlePublic(w http.ResponseWriter, r *http.Request) {
	file := r.PathValue("file")
	if !richtext.ImageFile(file) {
		http.NotFound(w, r)
		return
	}
	id, format, _ := strings.Cut(file, ".")
	it, err := f.store.Get(r.Context(), id)
	if errors.Is(err, ErrNotFound) || (err == nil && it.Format != format) {
		http.NotFound(w, r)
		return
	}
	var public bool
	if err == nil {
		public, err = f.isPublic(r.Context(), id, file)
	}
	if err != nil {
		// 取得の失敗を404にすると、障害の間に公開中の画像が「無い」と扱われる
		f.logger.Error("media public failed", web.RequestIDAttr(w))
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}
	if !public {
		http.NotFound(w, r)
		return
	}
	web.PublicCache(w, publicCacheSeconds)
	f.serve(w, r, it)
}

// serve は本体を返します。HEADでは本体を読みません。
func (f *Feature) serve(w http.ResponseWriter, r *http.Request, it Item) {
	w.Header().Set("Content-Type", contentType(it.Format))
	if r.Method == http.MethodHead {
		w.Header().Set("Content-Length", strconv.FormatInt(it.Bytes, 10))
		return
	}
	data, err := f.store.Blob(r.Context(), it.ID)
	if err != nil {
		w.Header().Del("Content-Type")
		w.Header().Set("Cache-Control", "no-store")
		f.logger.Error("media blob failed", web.RequestIDAttr(w))
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}
	http.ServeContent(w, r, "", it.CreatedAt, bytes.NewReader(data))
}

func contentType(format string) string {
	if format == formatPNG {
		return "image/png"
	}
	return "image/jpeg"
}

// api は、画像を使う機能に渡す features.Media の実装です。
type api struct{ f *Feature }

func (a api) Enabled() bool { return true }

func (a api) ReplaceRefs(ctx context.Context, c *sql.Conn, owner, key string, files []string) error {
	return replaceRefs(ctx, c, owner, key, files)
}

func (a api) DeleteRefs(ctx context.Context, c *sql.Conn, owner, key string) error {
	return replaceRefs(ctx, c, owner, key, nil)
}

func (a api) Images(ctx context.Context, files []string) (map[string]features.MediaImage, error) {
	out := make(map[string]features.MediaImage, len(files))
	for _, file := range files {
		id, format, _ := strings.Cut(file, ".")
		it, err := a.f.store.Get(ctx, id)
		if errors.Is(err, ErrNotFound) || (err == nil && it.Format != format) {
			continue
		}
		if err != nil {
			return nil, err
		}
		out[file] = it.image()
	}
	return out, nil
}

func (a api) Page(ctx context.Context, number int, selected string) (features.MediaPage, error) {
	number = max(1, min(number, maxPage))
	items, err := a.f.store.List(ctx, pageSize+1, (number-1)*pageSize)
	if err != nil {
		return features.MediaPage{}, err
	}
	p := features.MediaPage{Number: number}
	if number > 1 {
		p.Prev = number - 1
	}
	if len(items) > pageSize {
		p.Next = number + 1
		items = items[:pageSize]
	}
	for _, it := range items {
		p.Images = append(p.Images, it.image())
	}
	if selected != "" {
		found, err := a.Images(ctx, []string{selected})
		if err != nil {
			return p, err
		}
		if img, ok := found[selected]; ok {
			p.Selected = &img
		}
	}
	return p, nil
}
