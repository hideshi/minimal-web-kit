package mcp

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"

	"minimal-web-kit/features"
	"minimal-web-kit/internal/store"
	"minimal-web-kit/internal/web"
	"minimal-web-kit/internal/web/form"
	"minimal-web-kit/internal/web/notice"
)

const (
	defaultRedirect = "http://localhost/callback"
	callbackPort    = 18767
)

var clientMessages = notice.Messages{
	"added":    "接続を追加しました。この操作をしたアカウントに、利用を許可しました。",
	"enabled":  "接続を再開しました。",
	"disabled": "接続を停止しました。発行済みのトークンも使えなくなりました。",
	"allowed":  "利用を許可しました。",
	"revoked":  "利用の許可を取り消しました。発行済みのトークンも使えなくなりました。",
}

// clientInput は、接続の追加のフォームの値です。
type clientInput struct {
	Label, Redirect string
	Invalid         string
}

func (c clientInput) fields() []form.Field {
	label := form.Field{ID: "client-label", Name: "label", Label: "名前", Value: c.Label, Required: true, MaxLength: 100, Hint: "同意の画面に出ます。例: Claude Code"}
	redirect := form.Field{ID: "client-redirect", Name: "redirect", Label: "戻り先", Value: c.Redirect, Required: true, MaxLength: 200, Hint: "手元のアプリのURLです。ポートは書きません。Claude Codeはこのままで動きます"}
	switch c.Invalid {
	case "label":
		label.Error = "名前を100文字以内で入力してください。"
	case "redirect":
		redirect.Error = "http://localhost/ で始まるURLを入力してください。ポートは書きません。"
	}
	return []form.Field{label, redirect}
}

type clientRow struct {
	features.AgentClient
	Allowed int
}

func (f *Feature) addClientData(ctx context.Context, data map[string]any, in clientInput) error {
	clients, err := f.auth.AgentClients(ctx)
	if err != nil {
		return err
	}
	rows := make([]clientRow, 0, len(clients))
	for _, c := range clients {
		row := clientRow{AgentClient: c}
		for _, a := range c.Accounts {
			if a.Allowed {
				row.Allowed++
			}
		}
		rows = append(rows, row)
	}
	data["Clients"] = rows
	data["ClientFields"] = in.fields()
	return nil
}

func (f *Feature) serverError(w http.ResponseWriter, err error) {
	if store.Busy(err) {
		web.WriteBusy(w)
		return
	}
	f.logger.Error("mcp client change failed", web.RequestIDAttr(w))
	http.Error(w, "Internal Server Error", http.StatusInternalServerError)
}

// handleAddClient は接続を追加します。同じトランザクションで、操作した管理者に利用を許可します。
func (f *Feature) handleAddClient(w http.ResponseWriter, r *http.Request, s features.AdminSession) {
	in := clientInput{Redirect: r.PostForm.Get("redirect")}
	label, err := form.Line(r.PostForm.Get("label"), 100, true)
	if err != nil {
		in.Label, in.Invalid = r.PostForm.Get("label"), "label"
	} else {
		in.Label = label
	}
	var client features.AgentClient
	if in.Invalid == "" {
		client, err = f.auth.AddAgentClient(r.Context(), in.Label, in.Redirect, s.AccountID())
		if errors.Is(err, features.ErrAgentInvalid) {
			in.Invalid = "redirect"
		} else if err != nil {
			f.serverError(w, err)
			return
		}
	}
	if in.Invalid != "" {
		f.renderSettingsError(w, r, s, in)
		return
	}
	f.logger.Info("mcp client added", web.RequestIDAttr(w))
	notice.Redirect(w, r, "/admin/mcp/clients/"+client.ID, "added")
}

// renderSettingsError は、入力を残して設定の画面を返します。
func (f *Feature) renderSettingsError(w http.ResponseWriter, r *http.Request, s features.AdminSession, in clientInput) {
	settings, err := f.store.all(r.Context())
	if err != nil {
		f.serverError(w, err)
		return
	}
	data := s.PageData("MCPの設定", "/admin/mcp")
	data["Rows"] = f.settingRows(settings)
	data["IsAdmin"] = s.IsAdmin()
	if err := f.addClientData(r.Context(), data, in); err != nil {
		f.serverError(w, err)
		return
	}
	f.render(w, http.StatusBadRequest, "admin_mcp.html", data)
}

// handleClient は接続の詳細です。client_id と、登録のコマンドを出します。
func (f *Feature) handleClient(w http.ResponseWriter, r *http.Request, s features.AdminSession) {
	f.renderClient(w, r, s, http.StatusOK, "")
}

func (f *Feature) renderClient(w http.ResponseWriter, r *http.Request, s features.AdminSession, status int, message string) {
	client, err := f.auth.AgentClient(r.Context(), r.PathValue("id"))
	if errors.Is(err, features.ErrAgentNotFound) {
		http.NotFound(w, r)
		return
	} else if err != nil {
		f.serverError(w, err)
		return
	}
	data := s.PageData("接続: "+client.Label, "/admin/mcp")
	data["Client"] = client
	data["Error"] = message
	if status == http.StatusOK {
		data["Notice"] = clientMessages.From(r)
	}
	data["IDField"] = form.Field{ID: "client-id", Name: "client_id", Label: "client_id", Value: client.ID, Readonly: true, Hint: "秘密の値ではありません"}
	data["CommandField"] = form.Field{ID: "client-command", Name: "command", Label: "Claude Codeに登録するコマンド", Rows: 3, Readonly: true,
		Value: fmt.Sprintf("claude mcp add -s user --transport http --client-id %s --callback-port %d site %s", client.ID, callbackPort, f.auth.Resource())}
	f.render(w, status, "admin_mcp_client.html", data)
}

func (f *Feature) finishClient(w http.ResponseWriter, r *http.Request, s features.AdminSession, err error, key string) {
	if errors.Is(err, features.ErrAgentConflict) {
		f.renderClient(w, r, s, http.StatusConflict, "画面を開いた後に状態が変わりました。今の状態を確かめて、もう一度操作してください。")
		return
	} else if errors.Is(err, features.ErrAgentNotFound) {
		http.NotFound(w, r)
		return
	} else if err != nil {
		f.serverError(w, err)
		return
	}
	f.logger.Info("mcp client changed", web.RequestIDAttr(w), slog.String("change", key))
	notice.Redirect(w, r, "/admin/mcp/clients/"+r.PathValue("id"), key)
}

func (f *Feature) handleClientEnabled(enabled bool) func(http.ResponseWriter, *http.Request, features.AdminSession) {
	return func(w http.ResponseWriter, r *http.Request, s features.AdminSession) {
		key := "disabled"
		if enabled {
			key = "enabled"
		}
		f.finishClient(w, r, s, f.auth.SetAgentClientEnabled(r.Context(), r.PathValue("id"), enabled), key)
	}
}

func (f *Feature) handleClientGrant(allowed bool) func(http.ResponseWriter, *http.Request, features.AdminSession) {
	return func(w http.ResponseWriter, r *http.Request, s features.AdminSession) {
		account, err := strconv.ParseInt(r.PathValue("account"), 10, 64)
		if err != nil || account <= 0 {
			http.NotFound(w, r)
			return
		}
		key := "revoked"
		if allowed {
			key = "allowed"
		}
		f.finishClient(w, r, s, f.auth.SetAgentGrant(r.Context(), r.PathValue("id"), account, allowed), key)
	}
}
