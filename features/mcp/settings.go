package mcp

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"minimal-web-kit/features"
	"minimal-web-kit/internal/store"
	"minimal-web-kit/internal/web"
	"minimal-web-kit/internal/web/notice"
)

type setting struct {
	Enabled    bool
	Generation int64
}

type settingsStore struct {
	db  *sql.DB
	now func() time.Time
}

func (s *settingsStore) all(ctx context.Context) (map[string]setting, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT feature, enabled, generation FROM mcp_feature_settings")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]setting{}
	for rows.Next() {
		var name string
		var v setting
		if err := rows.Scan(&name, &v.Enabled, &v.Generation); err != nil {
			return nil, err
		}
		out[name] = v
	}
	return out, rows.Err()
}

// set は有効・無効を変え、世代を進めます。同じ値でも世代は進み、古い確認は使えなくなります（MCP-INV-5）。
func (s *settingsStore) set(ctx context.Context, feature string, enabled bool) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO mcp_feature_settings (feature, enabled, updated_at) VALUES (?, ?, ?)
		 ON CONFLICT (feature) DO UPDATE SET enabled = excluded.enabled, generation = generation + 1, updated_at = excluded.updated_at`,
		feature, enabled, s.now().Unix())
	return err
}

type settingRow struct {
	Name, Label string
	Enabled     bool
	Reads       int
	Writes      int
}

var settingsMessages = notice.Messages{"enabled": "MCPを有効にしました。", "disabled": "MCPを無効にしました。"}

func (f *Feature) handleSettings(w http.ResponseWriter, r *http.Request, s features.AdminSession) {
	settings, err := f.store.all(r.Context())
	if err != nil {
		f.logger.Error("mcp settings read failed", web.RequestIDAttr(w))
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}
	rows := f.settingRows(settings)
	data := s.PageData("MCPの設定", "/admin/mcp")
	data["Rows"] = rows
	data["IsAdmin"] = s.IsAdmin()
	data["Notice"] = settingsMessages.From(r)
	if s.IsAdmin() {
		if err := f.addClientData(r.Context(), data, clientInput{Redirect: defaultRedirect}); err != nil {
			f.logger.Error("mcp clients read failed", web.RequestIDAttr(w))
			http.Error(w, "Internal Server Error", http.StatusInternalServerError)
			return
		}
	}
	f.render(w, http.StatusOK, "admin_mcp.html", data)
}

func (f *Feature) settingRows(settings map[string]setting) []settingRow {
	var rows []settingRow
	for _, m := range f.sources {
		row := settingRow{Name: m.Name(), Label: m.MCPLabel(), Enabled: settings[m.Name()].Enabled}
		for _, t := range m.MCPTools() {
			if t.Effect == features.MCPRead {
				row.Reads++
			} else {
				row.Writes++
			}
		}
		rows = append(rows, row)
	}
	return rows
}

func (f *Feature) render(w http.ResponseWriter, status int, page string, data map[string]any) {
	web.NoTransform(w)
	var buf bytes.Buffer
	if err := f.pages[page].ExecuteTemplate(&buf, "layout", data); err != nil {
		f.logger.Error("mcp render failed", web.RequestIDAttr(w))
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	_, _ = buf.WriteTo(w)
}

func (f *Feature) handleChange(enabled bool) func(http.ResponseWriter, *http.Request, features.AdminSession) {
	return func(w http.ResponseWriter, r *http.Request, s features.AdminSession) {
		name := r.PathValue("feature")
		known := false
		for _, m := range f.sources {
			known = known || m.Name() == name
		}
		if !known {
			http.NotFound(w, r)
			return
		}
		if err := f.store.set(r.Context(), name, enabled); store.Busy(err) {
			web.WriteBusy(w)
			return
		} else if err != nil && !errors.Is(err, context.Canceled) {
			f.logger.Error("mcp settings write failed", web.RequestIDAttr(w))
			http.Error(w, "Internal Server Error", http.StatusInternalServerError)
			return
		}
		f.logger.Info("mcp setting changed", slog.String("feature", name), slog.Bool("enabled", enabled))
		key := "disabled"
		if enabled {
			key = "enabled"
		}
		notice.Redirect(w, r, "/admin/mcp", key)
	}
}
