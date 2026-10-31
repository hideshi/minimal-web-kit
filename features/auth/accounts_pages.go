package auth

import (
	"bytes"
	"errors"
	"log/slog"
	"net/http"
	"strconv"

	"minimal-web-kit/internal/web"
	"minimal-web-kit/internal/web/form"
	"minimal-web-kit/internal/web/notice"
)

// accountNotices は、アカウント管理の画面の完了メッセージです。
var accountNotices = notice.Messages{
	"role_changed":    "ロールを変更しました。このアカウントのログイン状態を解除しました。",
	"disabled":        "アカウントを停止しました。このアカウントのログイン状態を解除しました。",
	"enabled":         "アカウントを再開しました。",
	"deleted":         "アカウントを削除しました。",
	"passkey_removed": "パスキーを削除しました。このアカウントのログイン状態を解除しました。",
}

// accountErrorMessages は、アカウントの操作を拒否したときに画面に出す固定の文言です。
var accountErrorMessages = map[error]string{
	ErrSelfChange:     "自分自身のアカウントは、ロールの変更・停止・削除ができません。別のシステム管理者が操作してください。",
	ErrLastAdmin:      "有効なシステム管理者（パスキーを登録済みで停止していないシステム管理者）が0人になるため、この操作はできません。先に別のシステム管理者を追加してください。",
	ErrLastCredential: "このアカウントの最後の有効なパスキーは削除できません。パスキーをなくした場合は、「登録URLを発行し直す」で、既存のパスキーを削除して発行してください。",
}

func nameField(value, errMsg string) form.Field {
	return form.Field{ID: "account-name", Name: "name", Label: "呼び名", Value: value, Error: errMsg, MaxLength: maxAccountNameRunes, Required: true,
		Hint: "アカウントを見分けるための名前です（例: 編集担当A）。実名・メールアドレスは入れないでください。50文字以内。パスキーの登録時に、認証器にも保存されます。"}
}

// roleChoices はロールの選択肢。long は作成画面の説明つきの文言。
func roleChoices(id, role string, long bool) form.Choices {
	user, member, admin := roleLabel(roleUser), roleLabel(roleMember), roleLabel(roleAdmin)
	if long {
		user += "（公開サイトの編集とアカウントの管理はできない）"
		member += "（各機能の管理画面を使える。アカウントの管理はできない）"
		admin += "（アカウントの管理もできる。必要な人に限る）"
	}
	return form.Choices{ID: id, Name: "role", Legend: "ロール", Required: long, Options: []form.Option{
		{Value: roleUser, Label: user, Selected: role == roleUser},
		{Value: roleMember, Label: member, Selected: role == roleMember},
		{Value: roleAdmin, Label: admin, Selected: role == roleAdmin},
	}}
}

func (f *Feature) handleAccountsPage(w http.ResponseWriter, r *http.Request, s session) {
	list, err := f.store.listAccounts(r.Context())
	if err != nil {
		f.logger.Error("auth list accounts failed", f.requestID(w))
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}
	data := s.PageData("アカウントの管理", "/admin/accounts")
	data["Accounts"] = list
	data["Self"] = s.accountID
	data["Notice"] = accountNotices.From(r)
	f.renderAdmin(w, "admin_accounts.html", data, false)
}

func (f *Feature) renderAccountForm(w http.ResponseWriter, s session, status int, name, role, errMsg string) {
	data := s.PageData("アカウントの追加", "/admin/accounts")
	if role == "" {
		role = roleUser
	}
	data["NameField"] = nameField(name, errMsg)
	data["RoleChoices"] = roleChoices("account-role", role, true)
	f.renderAdminStatus(w, status, "admin_account_form.html", data, false)
}

func (f *Feature) handleNewAccountPage(w http.ResponseWriter, r *http.Request, s session) {
	f.renderAccountForm(w, s, http.StatusOK, "", "", "")
}

// handleCreateAccount はアカウントを作り、その登録URLを応答の画面に1回だけ表示します（後から再表示できない）。
func (f *Feature) handleCreateAccount(w http.ResponseWriter, r *http.Request, s session) {
	name, role := r.PostForm.Get("name"), r.PostForm.Get("role")
	id, token, err := f.store.createAccountWithToken(r.Context(), name, role)
	switch {
	case errors.Is(err, ErrNameTaken):
		f.renderAccountForm(w, s, http.StatusBadRequest, name, role, "同じ呼び名のアカウントがあります。別の呼び名にしてください。")
		return
	case errors.Is(err, ErrInvalidInput):
		f.renderAccountForm(w, s, http.StatusBadRequest, name, role, "呼び名は1〜50文字（改行なし）で入力し、ロールを選んでください。")
		return
	case err != nil:
		f.writeFailed(w, "auth create account failed", err)
		return
	}
	f.logger.Info("auth account created", f.requestID(w), slog.Int64("account_id", id), slog.String("role", role), slog.Int64("by", s.accountID))
	f.renderSetupURL(w, r, s, id, token, true)
}

// renderSetupURL は、発行した登録URLを表示します。画面は再読み込みしないよう案内します。
func (f *Feature) renderSetupURL(w http.ResponseWriter, r *http.Request, s session, id int64, token string, created bool) {
	a, err := f.store.getAccount(r.Context(), id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	title := "登録URLを発行しました"
	if created {
		title = "アカウントを作成しました"
	}
	data := s.PageData(title, "/admin/accounts")
	data["Account"] = a
	data["SetupURLField"] = form.Field{ID: "setup-url", Name: "setup_url", Label: "登録URL", Value: SetupURL(f.cfg.Origin, token), Readonly: true,
		Hint: "URLを持っている人は、このアカウントとしてパスキーを登録できます。共有の場所に貼らず、本人だけに渡してください。発行し直すと、このURLは使えなくなります。"}
	f.renderAdmin(w, "admin_account_created.html", data, false)
}

func (f *Feature) renderAccount(w http.ResponseWriter, r *http.Request, s session, id int64, status int, errMsg string) {
	a, err := f.store.getAccount(r.Context(), id)
	if errors.Is(err, ErrNotFound) {
		http.NotFound(w, r)
		return
	} else if err != nil {
		f.logger.Error("auth get account failed", f.requestID(w))
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}
	creds, err := f.store.listCredentials(r.Context(), id)
	if err != nil {
		f.logger.Error("auth list passkeys failed", f.requestID(w))
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}
	data := s.PageData("アカウント: "+a.Name, "/admin/accounts")
	data["Account"] = a
	data["Credentials"] = creds
	data["IsSelf"] = id == s.accountID
	data["Notice"] = accountNotices.From(r)
	data["RoleChoices"] = roleChoices("account-role", a.Role, false)
	data["ResetKeysField"] = form.Field{ID: "reset-keys", Name: "reset_keys", Type: "checkbox", Label: "登録済みのパスキーとログイン状態を削除してから発行する（端末の紛失・盗難時）"}
	data["Error"] = errMsg
	f.renderAdminStatus(w, status, "admin_account.html", data, false)
}

func (f *Feature) handleAccountPage(w http.ResponseWriter, r *http.Request, s session) {
	id, ok := web.PathID(w, r, "id")
	if !ok {
		return
	}
	f.renderAccount(w, r, s, id, http.StatusOK, "")
}

// accountAction は、他のアカウントへの操作（ロール変更・停止・再開）のハンドラを作ります。
func (f *Feature) accountAction(event, key string, do func(r *http.Request, s session, id int64) error) func(http.ResponseWriter, *http.Request, session) {
	return func(w http.ResponseWriter, r *http.Request, s session) {
		id, ok := web.PathID(w, r, "id")
		if !ok {
			return
		}
		err := do(r, s, id)
		if f.accountActionFailed(w, r, s, id, err) {
			return
		}
		f.logger.Info(event, f.requestID(w), slog.Int64("account_id", id), slog.Int64("by", s.accountID))
		notice.Redirect(w, r, "/admin/accounts/"+strconv.FormatInt(id, 10), key)
	}
}

// accountActionFailed は、操作のエラーを画面に返します。エラーが無ければ false です。
func (f *Feature) accountActionFailed(w http.ResponseWriter, r *http.Request, s session, id int64, err error) bool {
	if err == nil {
		return false
	}
	switch {
	case errors.Is(err, ErrNotFound):
		http.NotFound(w, r)
	case errors.Is(err, ErrInvalidInput):
		http.Error(w, "Bad Request", http.StatusBadRequest)
	case errors.Is(err, ErrSetupTokenInvalid):
		f.renderAccount(w, r, s, id, http.StatusConflict, "停止中のアカウントには登録URLを発行できません。先に再開してください。")
	default:
		for target, msg := range accountErrorMessages {
			if errors.Is(err, target) {
				f.renderAccount(w, r, s, id, http.StatusConflict, msg)
				return true
			}
		}
		f.writeFailed(w, "auth account action failed", err)
	}
	return true
}

// handleReissueToken は、アカウントの登録URL（24時間）を発行し直します。reset_keys なら、既存のパスキーとログイン状態を削除します。
func (f *Feature) handleReissueToken(w http.ResponseWriter, r *http.Request, s session) {
	id, ok := web.PathID(w, r, "id")
	if !ok {
		return
	}
	resetKeys := r.PostForm.Get("reset_keys") == "1"
	if resetKeys && id == s.accountID {
		// 自分の鍵を消すと締め出されるため、自分は別のシステム管理者に頼む
		f.renderAccount(w, r, s, id, http.StatusConflict, accountErrorMessages[ErrSelfChange])
		return
	}
	token, err := f.store.reissueToken(r.Context(), id, resetKeys)
	if f.accountActionFailed(w, r, s, id, err) {
		return
	}
	f.logger.Info("auth setup url issued", f.requestID(w), slog.Int64("account_id", id), slog.Bool("reset_keys", resetKeys), slog.Int64("by", s.accountID))
	f.renderSetupURL(w, r, s, id, token, false)
}

// handleDeleteAccountPasskey は、システム管理者が他のアカウントのパスキーを削除します（そのアカウントのログイン状態も解除する）。
func (f *Feature) handleDeleteAccountPasskey(w http.ResponseWriter, r *http.Request, s session) {
	id, ok := web.PathID(w, r, "id")
	if !ok {
		return
	}
	credID, ok := credentialParamNamed(w, r, "cid")
	if !ok {
		return
	}
	keep := ""
	if id == s.accountID {
		keep = s.token
	}
	err := f.store.deleteCredential(r.Context(), id, credID, keep)
	if f.accountActionFailed(w, r, s, id, err) {
		return
	}
	f.logger.Info("auth passkey deleted", f.requestID(w), slog.Int64("account_id", id), slog.Int64("by", s.accountID))
	notice.Redirect(w, r, "/admin/accounts/"+strconv.FormatInt(id, 10), "passkey_removed")
}

// handleDeleteAccountPasskeyPage は、他のアカウントのパスキーを削除する前の確認画面です。
func (f *Feature) handleDeleteAccountPasskeyPage(w http.ResponseWriter, r *http.Request, s session) {
	id, ok := web.PathID(w, r, "id")
	if !ok {
		return
	}
	credID, ok := credentialParamNamed(w, r, "cid")
	if !ok {
		return
	}
	a, err := f.store.getAccount(r.Context(), id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	creds, err := f.store.listCredentials(r.Context(), id)
	if err != nil {
		f.logger.Error("auth list passkeys failed", f.requestID(w))
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}
	for _, c := range creds {
		if bytes.Equal(c.ID, credID) {
			data := s.PageData("パスキーの削除", "/admin/accounts")
			data["Account"] = a
			data["Credential"] = c
			f.renderAdmin(w, "admin_account_passkey_delete.html", data, false)
			return
		}
	}
	http.NotFound(w, r)
}

func (f *Feature) handleDeleteAccountPage(w http.ResponseWriter, r *http.Request, s session) {
	id, ok := web.PathID(w, r, "id")
	if !ok {
		return
	}
	a, err := f.store.getAccount(r.Context(), id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	data := s.PageData("アカウントの削除", "/admin/accounts")
	data["Account"] = a
	data["IsSelf"] = id == s.accountID
	f.renderAdmin(w, "admin_account_delete.html", data, false)
}

func (f *Feature) handleDeleteAccount(w http.ResponseWriter, r *http.Request, s session) {
	id, ok := web.PathID(w, r, "id")
	if !ok {
		return
	}
	err := f.store.deleteAccount(r.Context(), s.accountID, id)
	if f.accountActionFailed(w, r, s, id, err) {
		return
	}
	f.logger.Info("auth account deleted", f.requestID(w), slog.Int64("account_id", id), slog.Int64("by", s.accountID))
	notice.Redirect(w, r, "/admin/accounts", "deleted")
}

func (f *Feature) registerAccountRoutes(mux *web.IsolatedMux) {
	page := func(h func(http.ResponseWriter, *http.Request, session)) http.HandlerFunc {
		return f.page(adminOnly(h))
	}
	form := func(h func(http.ResponseWriter, *http.Request, session)) http.HandlerFunc {
		return f.form(adminOnly(h))
	}
	mux.HandleFunc("GET /admin/accounts", page(f.handleAccountsPage))
	mux.HandleFunc("GET /admin/accounts/new", page(f.handleNewAccountPage))
	mux.HandleFunc("POST /admin/accounts", form(f.handleCreateAccount))
	mux.HandleFunc("GET /admin/accounts/{id}", page(f.handleAccountPage))
	mux.HandleFunc("POST /admin/accounts/{id}/role", form(f.accountAction("auth account role changed", "role_changed",
		func(r *http.Request, s session, id int64) error {
			return f.store.setRole(r.Context(), s.accountID, id, r.PostForm.Get("role"))
		})))
	mux.HandleFunc("POST /admin/accounts/{id}/disable", form(f.accountAction("auth account disabled", "disabled",
		func(r *http.Request, s session, id int64) error {
			return f.store.disableAccount(r.Context(), s.accountID, id)
		})))
	mux.HandleFunc("POST /admin/accounts/{id}/enable", form(f.accountAction("auth account enabled", "enabled",
		func(r *http.Request, s session, id int64) error {
			return f.store.enableAccount(r.Context(), s.accountID, id)
		})))
	mux.HandleFunc("POST /admin/accounts/{id}/setup-url", form(f.handleReissueToken))
	mux.HandleFunc("GET /admin/accounts/{id}/passkeys/{cid}/delete", page(f.handleDeleteAccountPasskeyPage))
	mux.HandleFunc("POST /admin/accounts/{id}/passkeys/{cid}/delete", form(f.handleDeleteAccountPasskey))
	mux.HandleFunc("GET /admin/accounts/{id}/delete", page(f.handleDeleteAccountPage))
	mux.HandleFunc("POST /admin/accounts/{id}/delete", form(f.handleDeleteAccount))
}
