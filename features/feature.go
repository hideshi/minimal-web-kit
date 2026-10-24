package features

import (
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
