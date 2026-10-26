package auth

import (
	"encoding/base64"
	"errors"
	"log/slog"
	"net/http"

	"minimal-web-kit/internal/web"
	"minimal-web-kit/internal/web/form"
	"minimal-web-kit/internal/web/notice"
)

// passkeyNotices は、パスキー管理画面の完了メッセージです。
var passkeyNotices = notice.Messages{
	"added":   "パスキーを追加しました。",
	"renamed": "端末名を変更しました。",
	"removed": "パスキーを削除しました。ほかの端末でのログイン状態も解除しました。",
}

// labelField は、パスキーの端末名の入力欄です。登録画面のJSは id="passkey-label" を読みます。
func labelField(example string) form.Field {
	return form.Field{ID: "passkey-label", Name: "label", Label: "端末名", MaxLength: maxLabelRunes, Required: true,
		Hint: "あとで見分けるための名前です（例: " + example + "）。個人名は入れないでください。50文字以内。"}
}

// --- パスキーの管理 ---

// credentialParam は経路の {id}（credential IDのbase64url）を解析します。
func credentialParam(w http.ResponseWriter, r *http.Request) ([]byte, bool) {
	return credentialParamNamed(w, r, "id")
}

// credentialParamNamed は、経路の {name}（credential IDのbase64url）を解析します。
func credentialParamNamed(w http.ResponseWriter, r *http.Request, name string) ([]byte, bool) {
	id, err := base64.RawURLEncoding.Strict().DecodeString(r.PathValue(name))
	if err != nil || len(id) == 0 || len(id) > maxCredentialIDLen {
		http.NotFound(w, r)
		return nil, false
	}
	return id, true
}

// renderPasskeys はパスキー管理画面を返します。パスキーの追加にWebAuthnを使うため、認証画面と同じCSPで返します。
func (f *Feature) renderPasskeys(w http.ResponseWriter, r *http.Request, s session, status int, errMsg string) {
	data := s.PageData("パスキーの管理", "/admin/passkeys")
	creds, err := f.store.listCredentials(r.Context(), s.accountID)
	if err != nil {
		f.logger.Error("auth list passkeys failed", f.requestID(w))
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}
	active := 0
	for _, c := range creds {
		if !c.Disabled() {
			active++
		}
	}
	data["LabelField"] = labelField("予備のセキュリティキー")
	data["Credentials"] = creds
	data["ActiveCount"] = active
	data["AllowSynced"] = f.cfg.AllowSynced
	data["Error"] = errMsg
	data["Notice"] = passkeyNotices.From(r)
	f.renderAdminStatus(w, status, "admin_passkeys.html", data, true)
}

func (f *Feature) handlePasskeysPage(w http.ResponseWriter, r *http.Request, s session) {
	f.renderPasskeys(w, r, s, http.StatusOK, "")
}

func (f *Feature) handleRenamePasskey(w http.ResponseWriter, r *http.Request, s session) {
	id, ok := credentialParam(w, r)
	if !ok {
		return
	}
	err := f.store.renameCredential(r.Context(), s.accountID, id, r.PostForm.Get("label"))
	switch {
	case errors.Is(err, ErrInvalidInput):
		f.renderPasskeys(w, r, s, http.StatusBadRequest, errorMessages["label_invalid"])
	case errors.Is(err, ErrNotFound):
		http.NotFound(w, r)
	case err != nil:
		f.logger.Error("auth rename passkey failed", f.requestID(w))
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
	default:
		f.logger.Info("auth passkey renamed", f.requestID(w), slog.Int64("account_id", s.accountID))
		notice.Redirect(w, r, "/admin/passkeys", "renamed")
	}
}

func (f *Feature) handleDeletePasskeyPage(w http.ResponseWriter, r *http.Request, s session) {
	id, ok := credentialParam(w, r)
	if !ok {
		return
	}
	data := s.PageData("パスキーの削除", "/admin/passkeys")
	c, err := f.store.getCredentialInfo(r.Context(), s.accountID, id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	data["Credential"] = c
	f.renderAdmin(w, "admin_passkey_delete.html", data, false)
}

func (f *Feature) handleDeletePasskey(w http.ResponseWriter, r *http.Request, s session) {
	id, ok := credentialParam(w, r)
	if !ok {
		return
	}
	err := f.store.deleteCredential(r.Context(), s.accountID, id, s.token)
	switch {
	case errors.Is(err, ErrLastCredential):
		f.renderPasskeys(w, r, s, http.StatusConflict, "最後の有効なパスキーは削除できません。先に別のパスキーを追加してください。")
	case errors.Is(err, ErrNotFound):
		http.NotFound(w, r)
	case err != nil:
		f.logger.Error("auth delete passkey failed", f.requestID(w))
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
	default:
		f.logger.Info("auth passkey deleted", f.requestID(w), slog.Int64("account_id", s.accountID))
		notice.Redirect(w, r, "/admin/passkeys", "removed")
	}
}

func (f *Feature) registerPasskeyRoutes(mux *web.IsolatedMux) {
	mux.HandleFunc("GET /admin/passkeys", f.page(f.handlePasskeysPage))
	mux.HandleFunc("POST /admin/passkeys/{id}/label", f.form(f.handleRenamePasskey))
	mux.HandleFunc("GET /admin/passkeys/{id}/delete", f.page(f.handleDeletePasskeyPage))
	mux.HandleFunc("POST /admin/passkeys/{id}/delete", f.form(f.handleDeletePasskey))
}
