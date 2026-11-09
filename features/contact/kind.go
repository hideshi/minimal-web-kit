package contact

import (
	"errors"
	"net/http"
	"strings"
	"unicode"
	"unicode/utf8"

	"minimal-web-kit/internal/web"
	"minimal-web-kit/internal/web/form"
)

// お問い合わせの種別（任意の選択式の1項目）。CONTACT_KIND_OPTIONS が無ければ無効で、画面・保存・受信箱のどこにも出さない。
// 保存するのは選択肢の識別子で、表示名は表示のたびに今の設定から引く（表示名を直しても過去の記録とずれない）。
// 不変条件: CONTACT-INV-14

const (
	defaultKindLabel  = "お問い合わせの種別"
	maxKindOptions    = 20
	maxKindIDBytes    = 32
	maxKindLabelRunes = 40
	kindField         = "kind"
)

// KindOption は種別の選択肢です。ID は保存する識別子、Label は表示名です。
type KindOption struct{ ID, Label string }

// KindConfig は種別の設定です。Options が空なら無効です。
type KindConfig struct {
	Label    string
	Required bool
	Options  []KindOption
}

// Enabled は種別を使うかを返します。
func (k KindConfig) Enabled() bool { return len(k.Options) > 0 }

// Valid は、id が設定した選択肢のどれかかを返します。
func (k KindConfig) Valid(id string) bool {
	for _, o := range k.Options {
		if o.ID == id {
			return true
		}
	}
	return false
}

// LabelFor は、保存した識別子の表示名を返します。空なら「（選ばない）」、設定から外した識別子はそのまま示します。
func (k KindConfig) LabelFor(id string) string {
	if id == "" {
		return "（選ばない）"
	}
	for _, o := range k.Options {
		if o.ID == id {
			return o.Label
		}
	}
	return "設定に無い値（" + id + "）"
}

// loadKindConfig は CONTACT_KIND_OPTIONS・CONTACT_KIND_LABEL・CONTACT_KIND_REQUIRED を読みます。エラーに値を含めません。
//
//	CONTACT_KIND_OPTIONS="history=歴史|region=地域・防災|other=その他"
//
// 区切りは「|」と、各選択肢の最初の「=」だけです。表示名には「、」「,」「（）」「=」を使えます。
func loadKindConfig(getenv func(string) string) (KindConfig, error) {
	raw := strings.TrimSpace(getenv("CONTACT_KIND_OPTIONS"))
	label := strings.TrimSpace(getenv("CONTACT_KIND_LABEL"))
	required := strings.TrimSpace(getenv("CONTACT_KIND_REQUIRED"))
	if raw == "" {
		if label != "" || required != "" {
			return KindConfig{}, errors.New("CONTACT_KIND_LABEL and CONTACT_KIND_REQUIRED need CONTACT_KIND_OPTIONS")
		}
		return KindConfig{}, nil
	}
	k := KindConfig{Label: defaultKindLabel}
	if label != "" {
		if !kindText(label) {
			return KindConfig{}, errors.New("CONTACT_KIND_LABEL must be at most 40 characters without control characters")
		}
		k.Label = label
	}
	switch required {
	case "", "false":
	case "true":
		k.Required = true
	default:
		return KindConfig{}, errors.New("CONTACT_KIND_REQUIRED must be true or false")
	}
	seen := map[string]bool{}
	for _, item := range strings.Split(raw, "|") {
		id, name, ok := strings.Cut(item, "=")
		id, name = strings.TrimSpace(id), strings.TrimSpace(name)
		if !ok || !kindID(id) || !kindText(name) {
			return KindConfig{}, errors.New("CONTACT_KIND_OPTIONS must be id=label pairs separated by |; ids use a-z, 0-9 and - (up to 32), labels up to 40 characters")
		}
		if seen[id] {
			return KindConfig{}, errors.New("CONTACT_KIND_OPTIONS has a duplicate id")
		}
		seen[id] = true
		k.Options = append(k.Options, KindOption{ID: id, Label: name})
	}
	if len(k.Options) > maxKindOptions {
		return KindConfig{}, errors.New("CONTACT_KIND_OPTIONS allows at most 20 options")
	}
	return k, nil
}

func kindID(s string) bool {
	if s == "" || len(s) > maxKindIDBytes {
		return false
	}
	for _, r := range s {
		if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-') {
			return false
		}
	}
	return true
}

func kindText(s string) bool {
	if s == "" || !utf8.ValidString(s) || utf8.RuneCountInString(s) > maxKindLabelRunes {
		return false
	}
	for _, r := range s {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}

// kindChoices は公開のフォームの種別の欄です。無効なら nil（欄を出さない）。任意なら先頭に「選ばない」を置きます。
func (f *Feature) kindChoices(v contactForm) *form.Choices {
	k := f.cfg.Kind
	if !k.Enabled() {
		return nil
	}
	c := &form.Choices{ID: "contact-kind", Name: kindField, Legend: k.Label, Required: k.Required, Inline: true}
	if v.Invalid == kindField {
		c.Error = k.Label + "は、選択肢から選んでください。"
	}
	if !k.Required {
		c.Options = append(c.Options, form.Option{Value: "", Label: "選ばない", Selected: v.Kind == ""})
	}
	for _, o := range k.Options {
		c.Options = append(c.Options, form.Option{Value: o.ID, Label: o.Label, Selected: v.Kind == o.ID})
	}
	return c
}

// checkKind は送られた種別を検査します。無効なら読まずに空にします。選択肢に無い値と、必須の未選択は、
// 入力を返して400にし、false を返します（呼び出し側はそこで終える）。
func (f *Feature) checkKind(w http.ResponseWriter, r *http.Request, v *contactForm) bool {
	k := f.cfg.Kind
	if !k.Enabled() {
		v.Kind = ""
		return true
	}
	v.Kind = r.PostForm.Get(kindField)
	if (v.Kind == "" && !k.Required) || k.Valid(v.Kind) {
		return true
	}
	v.Invalid = kindField
	f.logResult(w, "invalid_kind")
	f.renderForm(w, http.StatusBadRequest, *v, "", "")
	return false
}

// PrivacyNotices は、お問い合わせで保存する情報に、有効なときだけ種別を足します（項目名は設定した見出し）。
func (f *Feature) PrivacyNotices() []web.PrivacyNotice {
	notices := f.basePrivacyNotices()
	if k := f.cfg.Kind; k.Enabled() && len(notices) > 0 && len(notices[0].Rows) > 0 {
		need := "任意"
		if k.Required {
			need = "必須"
		}
		notices[0].Rows[0].Items += "、" + k.Label + "（選択式・" + need + "）"
	}
	return notices
}

// kindFilter は受信箱の絞り込みの値です。無効か、選択肢に無い値なら絞り込みません。
func (f *Feature) kindFilter(r *http.Request) string {
	id := r.URL.Query().Get(kindField)
	if f.cfg.Kind.Valid(id) {
		return id
	}
	return ""
}

// kindLinks は受信箱の絞り込みのリンクです。無効なら nil。
type kindLink struct {
	Label, Href string
	Current     bool
}

func (f *Feature) kindLinks(base, current string) []kindLink {
	k := f.cfg.Kind
	if !k.Enabled() {
		return nil
	}
	links := []kindLink{{Label: "すべて", Href: base, Current: current == ""}}
	for _, o := range k.Options {
		links = append(links, kindLink{Label: o.Label, Href: base + "?kind=" + o.ID, Current: current == o.ID})
	}
	return links
}
