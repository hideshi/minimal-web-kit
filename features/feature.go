package features

import (
	"context"
	"database/sql"
	"net/http"

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
