package features

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"net/http"
	"strings"

	"minimal-web-kit/internal/web"
)

// Feature は独立した業務機能モジュールを表すインターフェースです。
type Feature interface {
	Name() string
	Init(db *sql.DB) error
	RegisterRoutes(mux *http.ServeMux, sm *web.SessionManager)
}

// CustomRootFeature は、Baseのデフォルトトップ画面（GET /）を独自のトップ画面（LPやHPなど）に差し替える機能モジュールが実装します。
type CustomRootFeature interface {
	Feature
	HandleRoot(w http.ResponseWriter, r *http.Request)
}

// IsolatedRoutesFeature は、CoreのCSRF検査とセッション解決（匿名セッションCookieの照合）を通さない経路を持つ機能が実装します。
// Cookieを照合しない公開ページや、独自の認証・CSRF保護を持つ管理画面に使います。
// 独立経路の認証・CSRF・Origin検査は、機能自身が実装する責任を負います。
type IsolatedRoutesFeature interface {
	Feature
	RegisterIsolatedRoutes(mux *web.IsolatedMux)
}

// PurgeFeature は、期限切れデータの削除をCoreの起動時と定期のGCから呼ばれる機能が実装します。
type PurgeFeature interface {
	Feature
	Purge(ctx context.Context) error
}

// Command は、サーバーの実行ファイルのサブコマンドです（例: minimal-web-kit cms-setup）。
// 本番サーバーにGoを入れずに、運用の操作を実行できるようにします。
// サブコマンドの実行時は Init を呼ばないため、必要なマイグレーションや設定の検証はRunの中で行います。
type Command struct {
	Name    string
	Summary string
	Run     func(ctx context.Context, db *sql.DB, args []string, in io.Reader, out io.Writer) error
}

// CommandFeature は、サーバーの実行ファイルにサブコマンドを追加する機能が実装します。
type CommandFeature interface {
	Feature
	Commands() []Command
}

// NavLinkFeature は、共通のナビゲーションにリンクを足す機能が実装します（例: CMSの「お知らせ」）。
// 機能を無効にすればリンクも消えるため、他の機能が「この機能があるか」を判定する必要はありません。
type NavLinkFeature interface {
	Feature
	NavLinks() []web.NavLink
}

// NavLinkConsumer は、独自のナビゲーションを描画する機能（例: Site）が実装します。
// 起動時に、有効な全機能のリンクを受け取ります。
type NavLinkConsumer interface {
	Feature
	SetNavLinks(links []web.NavLink)
}

// Registry は有効化されている全機能のリストを保持します。
type Registry struct {
	features []Feature
}

func NewRegistry() *Registry {
	return &Registry{}
}

func (r *Registry) Register(f Feature) {
	r.features = append(r.features, f)
}

func (r *Registry) All() []Feature {
	return r.features
}

// NavLinks は、有効な機能が宣言したナビゲーションのリンクを登録順に返します。
// リンク先はサイト内のパス（"/" で始まり "//" で始まらない）に限ります。
func (r *Registry) NavLinks() ([]web.NavLink, error) {
	var links []web.NavLink
	for _, f := range r.features {
		nf, ok := f.(NavLinkFeature)
		if !ok {
			continue
		}
		for _, l := range nf.NavLinks() {
			if l.Label == "" || !strings.HasPrefix(l.Href, "/") || strings.HasPrefix(l.Href, "//") || strings.ContainsAny(l.Href, "\\ \t\r\n") {
				return nil, fmt.Errorf("feature %s: nav link must have a label and a same-site path", f.Name())
			}
			links = append(links, l)
		}
	}
	return links, nil
}
