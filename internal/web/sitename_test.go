package web

import (
	"bytes"
	"os"
	"strings"
	"testing"
	"testing/fstest"
)

func TestSiteName_FromEnv(t *testing.T) {
	cases := []struct {
		name    string
		set     bool
		value   string
		want    string
		wantErr bool
	}{
		{name: "未設定は既定の名前", want: DefaultSiteName},
		{name: "設定した名前", set: true, value: "世界シミュレーション", want: "世界シミュレーション"},
		{name: "前後の空白は除く", set: true, value: "  世界シミュレーション \n", want: "世界シミュレーション"},
		{name: "40文字までは使える", set: true, value: strings.Repeat("あ", 40), want: strings.Repeat("あ", 40)},
		{name: "空白だけは拒否", set: true, value: "   ", want: DefaultSiteName, wantErr: true},
		{name: "41文字は拒否", set: true, value: strings.Repeat("あ", 41), want: DefaultSiteName, wantErr: true},
		{name: "制御文字は拒否", set: true, value: "世界\tシミュレーション", want: DefaultSiteName, wantErr: true},
		{name: "不正なUTF-8は拒否", set: true, value: "\xff\xfe", want: DefaultSiteName, wantErr: true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if c.set {
				t.Setenv("SITE_NAME", c.value)
			} else {
				t.Setenv("SITE_NAME", "")
				os.Unsetenv("SITE_NAME")
			}
			if got := SiteName(); got != c.want {
				t.Errorf("SiteName() = %q, want %q", got, c.want)
			}
			err := CheckSiteName()
			if (err != nil) != c.wantErr {
				t.Fatalf("CheckSiteName() error = %v, wantErr %v", err, c.wantErr)
			}
			if v := strings.TrimSpace(c.value); err != nil && v != "" && strings.Contains(err.Error(), v) {
				t.Errorf("error must not contain the value: %v", err)
			}
		})
	}
}

// TestSiteName_Shells は、管理画面と公開ページの枠が、サイトの名前を題名・ヘッダー・フッターに使い、HTMLとしてエスケープすることを確かめます。
func TestSiteName_Shells(t *testing.T) {
	fsys := fstest.MapFS{"page.html": {Data: []byte(
		`{{define "admin"}}{{template "admin-shell" .}}{{end}}{{define "public"}}{{template "public-shell" .}}{{end}}{{define "content"}}本文{{end}}`)}}
	tmpl, err := ParseWithComponents(fsys, "page.html")
	if err != nil {
		t.Fatal(err)
	}
	render := func(name string) string {
		t.Helper()
		var b bytes.Buffer
		if err := tmpl.ExecuteTemplate(&b, name, map[string]any{"Title": "お問い合わせ"}); err != nil {
			t.Fatal(err)
		}
		return b.String()
	}

	t.Setenv("SITE_NAME", "世界シミュレーション")
	admin := render("admin")
	for _, want := range []string{
		"<title>お問い合わせ - 世界シミュレーション 管理画面</title>",
		`<a href="/admin" class="brand-link">世界シミュレーション</a>`,
		"&copy; 2026 世界シミュレーション</p>",
	} {
		if !strings.Contains(admin, want) {
			t.Errorf("admin shell lacks %q", want)
		}
	}
	public := render("public")
	for _, want := range []string{
		"<title>お問い合わせ - 世界シミュレーション</title>",
		`<a href="/" class="brand-link">世界シミュレーション</a>`,
		"&copy; 2026 世界シミュレーション</p>",
	} {
		if !strings.Contains(public, want) {
			t.Errorf("public shell lacks %q", want)
		}
	}
	for name, out := range map[string]string{"admin": admin, "public": public} {
		if strings.Contains(out, DefaultSiteName) {
			t.Errorf("%s shell still shows %s", name, DefaultSiteName)
		}
	}

	t.Setenv("SITE_NAME", `<b>A&B</b>`)
	escaped := render("admin")
	if strings.Contains(escaped, "<b>A&B</b>") || !strings.Contains(escaped, "&lt;b&gt;A&amp;B&lt;/b&gt;") {
		t.Error("site name must be escaped as HTML")
	}

	os.Unsetenv("SITE_NAME")
	if out := render("admin"); !strings.Contains(out, "<title>お問い合わせ - minimal-web-kit 管理画面</title>") {
		t.Error("unset SITE_NAME must keep the default name")
	}
}
