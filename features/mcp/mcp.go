// minimal-web-kit: bundled-feature mcp

// Package mcp は、AIエージェントがMCPで機能を操作するためのサーバーです。
// 読み取りは確認なしで実行し、書き込みはクライアントの利用者の確認を経てから実行します。
// 機能ごとの有効・無効はシステム管理者が決めます。ツールは features.MCPFeature を実装した機能が提供します。
package mcp

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"html/template"
	"log/slog"
	"net/http"
	"regexp"
	"strings"
	"time"

	"minimal-web-kit/features"
	"minimal-web-kit/internal/federation"
	"minimal-web-kit/internal/web"
)

//go:embed migration.sql
var migrationSQL string

//go:embed views/*.html
var viewsFS embed.FS

const (
	scopeRead  = "mcp:read"
	scopeWrite = "mcp:write"
	endpoint   = "/mcp"
)

// entry は、登録したツールと、提供する機能の組です。
type entry struct {
	tool    features.MCPTool
	feature string
}

// Feature は同梱機能のMCPサーバーです。
type Feature struct {
	logger  *slog.Logger
	admin   features.Admin
	auth    features.AgentAuth
	store   *settingsStore
	confirm *confirmer
	limits  *limiter
	tools   map[string]entry
	order   []string
	sources []features.MCPFeature
	pages   map[string]*template.Template
	slots   chan struct{}
	now     func() time.Time
}

// NewFeature は機能を返します。
func NewFeature(logger *slog.Logger) *Feature {
	return &Feature{logger: logger, now: time.Now, limits: newLimiter(time.Now), slots: make(chan struct{}, maxConcurrent)}
}

// Name は機能の名前です。
func (f *Feature) Name() string { return "mcp" }

// Init はマイグレーションを適用します。確認の鍵は起動ごとにメモリで作ります。
func (f *Feature) Init(db *sql.DB) error {
	if _, err := db.Exec(migrationSQL); err != nil {
		return fmt.Errorf("mcp migration: %w", err)
	}
	f.store = &settingsStore{db: db, now: func() time.Time { return f.now() }}
	var err error
	f.confirm, err = newConfirmer(func() time.Time { return f.now() })
	return err
}

// RegisterRoutes はCoreの経路に何も登録しません。
func (f *Feature) RegisterRoutes(mux *http.ServeMux, sm *web.SessionManager) {}

// SetAdmin は管理者認証の口を受け取ります。
func (f *Feature) SetAdmin(a features.Admin) error {
	f.admin = a
	var err error
	f.pages, err = web.ParsePages(a.Layout(), viewsFS, "admin_mcp.html", "admin_mcp_client.html", "admin_mcp_enable.html")
	return err
}

// AdminNavLinks は管理画面のナビのリンクです。
func (f *Feature) AdminNavLinks() []web.NavLink {
	return []web.NavLink{{Label: "MCPの設定", Href: "/admin/mcp"}}
}

// AgentAuthConfig は、保護するリソースと権限を宣言します。
func (f *Feature) AgentAuthConfig() features.AgentAuthConfig {
	return features.AgentAuthConfig{Path: endpoint, Scopes: []features.AgentScope{
		{Name: scopeRead, Label: "有効な機能のうち、このアカウントが使えるものの内容を読む（下書きを含む）"},
		{Name: scopeWrite, Label: "有効な機能のうち、このアカウントが使えるものの内容を変更する（実行のたびに確認する）"},
	}}
}

// SetAgentAuth はエージェントの認証の口を受け取ります。
func (f *Feature) SetAgentAuth(a features.AgentAuth) error {
	f.auth = a
	return nil
}

var toolName = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)

// SetMCPFeatures は、ツールを検査して登録します。宣言の不備は起動を止めます。
func (f *Feature) SetMCPFeatures(list []features.MCPFeature) error {
	f.sources = list
	f.tools = map[string]entry{}
	f.order = nil
	for _, m := range list {
		for _, t := range m.MCPTools() {
			if err := validTool(m.Name(), t); err != nil {
				return err
			}
			if _, dup := f.tools[t.Name]; dup {
				return fmt.Errorf("mcp tool %s declared twice", t.Name)
			}
			f.tools[t.Name] = entry{tool: t, feature: m.Name()}
			f.order = append(f.order, t.Name)
		}
	}
	return nil
}

// validTool は、読み取りは Read だけ、書き込みは Prepare と Apply だけを持つことを確かめます（MCP-INV-1）。
func validTool(feature string, t features.MCPTool) error {
	if !toolName.MatchString(t.Name) || !strings.HasPrefix(t.Name, feature+"_") || len(t.InputSchema) == 0 {
		return fmt.Errorf("mcp tool %s of %s: invalid declaration", t.Name, feature)
	}
	switch t.Effect {
	case features.MCPRead:
		if t.Read == nil || t.Prepare != nil || t.Apply != nil {
			return fmt.Errorf("mcp tool %s: a read tool has only Read", t.Name)
		}
	case features.MCPWrite:
		if t.Read != nil || t.Prepare == nil || t.Apply == nil {
			return fmt.Errorf("mcp tool %s: a write tool has only Prepare and Apply", t.Name)
		}
	default:
		return fmt.Errorf("mcp tool %s: unknown effect", t.Name)
	}
	return nil
}

// RegisterIsolatedRoutes は、MCPの経路、保護リソースの情報、認可の経路、管理画面を登録します。
// Cookieを照合しない独立経路で、認証はBearerトークンで行います。
func (f *Feature) RegisterIsolatedRoutes(mux *web.IsolatedMux) {
	if f.auth == nil {
		return
	}
	mux.HandleFunc("POST "+endpoint, f.handle)
	mux.HandleFunc("GET "+endpoint, methodNotAllowed)
	mux.HandleFunc("DELETE "+endpoint, methodNotAllowed)
	mux.HandleFunc("GET /.well-known/oauth-protected-resource", f.protectedResource)
	mux.HandleFunc("GET /.well-known/oauth-protected-resource"+endpoint, f.protectedResource)
	f.auth.RegisterAgentRoutes(mux)
	if f.admin != nil {
		mux.HandleFunc("GET /admin/mcp", f.admin.Page(f.handleSettings))
		mux.HandleFunc("GET /admin/mcp/features/{feature}/enable", f.admin.AdminPage(f.handleEnablePage))
		mux.HandleFunc("POST /admin/mcp/{feature}/enable", f.admin.AdminForm(f.handleChange(true)))
		mux.HandleFunc("POST /admin/mcp/{feature}/disable", f.admin.AdminForm(f.handleChange(false)))
		mux.HandleFunc("POST /admin/mcp/clients", f.admin.AdminForm(f.handleAddClient))
		mux.HandleFunc("GET /admin/mcp/clients/{id}", f.admin.AdminPage(f.handleClient))
		mux.HandleFunc("POST /admin/mcp/clients/{id}/enable", f.admin.AdminForm(f.handleClientEnabled(true)))
		mux.HandleFunc("POST /admin/mcp/clients/{id}/disable", f.admin.AdminForm(f.handleClientEnabled(false)))
		mux.HandleFunc("POST /admin/mcp/clients/{id}/accounts/{account}/allow", f.admin.AdminForm(f.handleClientGrant(true)))
		mux.HandleFunc("POST /admin/mcp/clients/{id}/accounts/{account}/revoke", f.admin.AdminForm(f.handleClientGrant(false)))
	}
}

func methodNotAllowed(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Allow", "POST")
	http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
}

func (f *Feature) metadataURL() string {
	return f.auth.Origin() + "/.well-known/oauth-protected-resource"
}

// protectedResource は、保護リソースの情報を返します（RFC 9728）。
func (f *Feature) protectedResource(w http.ResponseWriter, r *http.Request) {
	federation.JSON(w, http.StatusOK, map[string]any{
		"resource":                 f.auth.Resource(),
		"authorization_servers":    f.auth.AuthorizationServers(),
		"scopes_supported":         []string{scopeRead, scopeWrite},
		"bearer_methods_supported": []string{"header"},
	})
}

// errFeatureDisabled は、機能が無効なことを表します。
var errFeatureDisabled = errors.New("mcp: feature disabled")

// featureGeneration は、機能が有効なら設定の世代を返します。
func (f *Feature) featureGeneration(ctx context.Context, feature string) (int64, error) {
	settings, err := f.store.all(ctx)
	if err != nil {
		return 0, err
	}
	s, ok := settings[feature]
	if !ok || !s.Enabled {
		return 0, errFeatureDisabled
	}
	return s.Generation, nil
}
