package contact

import (
	"bytes"
	"embed"
	"errors"
	"html/template"
	"log/slog"
	"net/http"

	"minimal-web-kit/internal/web"
	"minimal-web-kit/internal/web/form"
	"minimal-web-kit/internal/web/guard"
	"minimal-web-kit/internal/web/notice"
)

//go:embed views/*.html
var viewsFS embed.FS

// trapField は見えない入力欄（ハニーポット）の名前です。ブラウザの自動入力が反応しない、意味の無い名前にします。
const trapField = "contact_hp_x7"

const maxFormBytes = 64 << 10

var publicPageNames = []string{"contact.html"}

var publicNotices = notice.Messages{"sent": "お問い合わせを受け付けました。返信が必要な場合は、書いていただいた返信先へご連絡します。"}

func parsePublicTemplates() map[string]*template.Template {
	pages := map[string]*template.Template{}
	for _, name := range publicPageNames {
		pages[name] = template.Must(web.ParseWithComponents(viewsFS, "views/layout.html", "views/"+name))
	}
	return pages
}

// SetLayout は、公開のフォームをサイト全体のレイアウト（例: Siteのヘッダー・フッター）で描画するようにします。
func (f *Feature) SetLayout(layout web.Layout) error {
	for _, name := range publicPageNames {
		t, err := web.ParsePage(layout, viewsFS, "views/"+name)
		if err != nil {
			return err
		}
		f.publicPages[name] = t
	}
	f.layout = &layout
	return nil
}

// contactForm は公開のフォームの入力と、欄ごとのエラーです。
type contactForm struct {
	Input
	Token   string
	Invalid string // 誤りのある欄（InputError.Field）
}

var fieldErrors = map[string]string{
	"body":     "お問い合わせ内容を、2,000文字以内で入力してください。",
	"links":    "お問い合わせ内容のURLは2つまでにしてください。",
	"name":     "お名前は50文字以内で、改行を含めずに入力してください。",
	"reply_to": "返信先は200文字以内で、改行を含めずに入力してください。",
}

func (v contactForm) fieldError(names ...string) string {
	for _, n := range names {
		if v.Invalid == n {
			return fieldErrors[n]
		}
	}
	return ""
}

func (v contactForm) Fields() []form.Field {
	return []form.Field{
		{ID: "contact-body", Name: "body", Label: "お問い合わせ内容", Value: v.Body, Rows: 8, Required: true, Error: v.fieldError("body", "links"),
			Hint: "2,000文字以内。URLは2つまで。"},
		{ID: "contact-name", Name: "name", Label: "お名前（任意）", Value: v.Name, MaxLength: maxNameRunes, Error: v.fieldError("name"),
			Hint: "返信が必要な場合だけ書いてください。ニックネームでもかまいません。"},
		{ID: "contact-reply-to", Name: "reply_to", Label: "返信先（任意）", Value: v.ReplyTo, MaxLength: maxReplyToRunes, Error: v.fieldError("reply_to"),
			Hint: "返信が必要な場合だけ、メールアドレス・電話番号など、連絡のつく先を書いてください。"},
	}
}

func (f *Feature) handleForm(w http.ResponseWriter, r *http.Request) {
	// 応答ごとに違うフォームの値を含むため、キャッシュさせない（Coreの既定の no-store のまま）
	f.renderForm(w, http.StatusOK, contactForm{Token: f.tokens.issue(f.now())}, "", publicNotices.From(r))
}

func (f *Feature) renderForm(w http.ResponseWriter, status int, v contactForm, pageError, sent string) {
	data := map[string]any{
		"Title":         "お問い合わせ",
		"CanonicalPath": "/contact",
		"Description":   "お問い合わせのフォームです。返信が必要な場合だけ、お名前と返信先を書いてください。",
		"Form":          v,
		"Trap":          trapField,
		"Error":         pageError,
		"Notice":        sent,
		"RetentionDays": int(f.cfg.Retention.Hours() / 24),
	}
	if f.layout != nil && f.layout.Data != nil {
		f.layout.Data(data)
	}
	var buf bytes.Buffer
	if err := f.publicPages["contact.html"].ExecuteTemplate(&buf, "layout", data); err != nil {
		f.logger.Error("contact render failed")
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	_, _ = buf.WriteTo(w)
}

// logResult は送信の判定を、固定の分類だけでログに出します（CONTACT-INV-6）。
func (f *Feature) logResult(w http.ResponseWriter, result string) {
	f.logger.Info("contact submission", slog.String("request_id", w.Header().Get("X-Request-ID")), slog.String("result", result))
}

// handleSubmit は送信を受け付けます。判定の順と応答は docs/contact-plan.md の4.1。
// 受け付けたふり（完了の画面へ移す）は、見えない入力欄と3秒未満の送信だけ。他は理由と入力を返す（CONTACT-INV-8）。
func (f *Feature) handleSubmit(w http.ResponseWriter, r *http.Request) {
	if !guard.SameOrigin(r, f.cfg.Origin) {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxFormBytes)
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Bad Request", http.StatusBadRequest)
		return
	}
	now := f.now()
	v := contactForm{Input: Input{Name: r.PostForm.Get("name"), ReplyTo: r.PostForm.Get("reply_to"), Body: r.PostForm.Get("body")}, Token: r.PostForm.Get("token")}
	fake := func(result string) {
		f.logResult(w, result)
		notice.Redirect(w, r, "/contact", "sent")
	}
	retry := func(result, msg string) {
		f.logResult(w, result)
		v.Token = f.tokens.issue(now)
		f.renderForm(w, http.StatusBadRequest, v, msg, "")
	}
	if r.PostForm.Get(trapField) != "" {
		fake("honeypot")
		return
	}
	nonce, verdict := f.tokens.check(v.Token, now)
	switch verdict {
	case tokenTooFast:
		fake(string(verdict))
		return
	case tokenExpired:
		retry(string(verdict), "時間が経ったため、受け付けられませんでした。内容はそのままなので、もう一度送信してください。")
		return
	case tokenBad:
		retry(string(verdict), "受け付けられませんでした。内容はそのままなので、もう一度送信してください。")
		return
	}
	// 入力の誤りは、元のフォームの値のまま出し直す（すぐ直して送っても「3秒未満」にならないため）
	if _, err := normalize(v.Input); err != nil {
		var invalid InputError
		errors.As(err, &invalid)
		v.Invalid = invalid.Field
		f.logResult(w, "invalid_"+invalid.Field)
		f.renderForm(w, http.StatusBadRequest, v, "", "")
		return
	}
	if !f.tokens.claim(nonce, now) {
		retry(string(tokenReplayed), "このフォームからは送信済みです。内容はそのままなので、もう一度送信してください。")
		return
	}
	_, err := f.store.Save(r.Context(), v.Input, f.cfg.Limits)
	if err != nil {
		f.tokens.release(nonce)
	}
	switch {
	case errors.Is(err, ErrOverLimit):
		f.logResult(w, "over_limit")
		w.Header().Set("Retry-After", "3600")
		f.renderForm(w, http.StatusServiceUnavailable, v, "現在、お問い合わせの受付を一時停止しています。時間を置いて、もう一度送信してください。", "")
		return
	case err != nil:
		f.logger.Error("contact save failed", slog.String("request_id", w.Header().Get("X-Request-ID")))
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}
	f.logResult(w, "saved")
	notice.Redirect(w, r, "/contact", "sent")
}
