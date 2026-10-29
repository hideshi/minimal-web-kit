package app

import "testing"

// 書き出す HTML が参照する /static/ のファイルが、書き出しに入っていなければエラーにすること。
// 版の ?v= とフラグメントは除いて照合し、外部の URL・ページへのリンクは対象にしないこと。
func TestCheckStaticRefs(t *testing.T) {
	css := []byte("body{}")
	ok := map[string][]byte{
		"index.html":           []byte(`<link rel="stylesheet" href="/static/css/style.css?v=0123456789ab"><a href="/privacy">p</a><a href="https://example.com/static/x.css">x</a><form action="https://forms.example.com/"></form>`),
		"404.html":             []byte(`<link rel="icon" href="/static/css/style.css#top">`),
		"static/css/style.css": css,
	}
	if err := checkStaticRefs(ok); err != nil {
		t.Fatalf("versioned references to exported files must pass: %v", err)
	}

	for name, files := range map[string]map[string][]byte{
		// サーバーにはあるが、書き出しの一覧に無いファイル（combobox.js 等）
		"not exported": {"index.html": []byte(`<script src="/static/js/combobox.js?v=0123456789ab" defer></script>`), "static/css/style.css": css},
		// 404.html にだけある不足の参照
		"only in 404.html": {"index.html": []byte(`<link rel="stylesheet" href="/static/css/style.css?v=1">`), "404.html": []byte(`<img src="/static/img/missing.png?v=2">`), "static/css/style.css": css},
	} {
		if err := checkStaticRefs(files); err == nil {
			t.Errorf("%s: must fail", name)
		}
	}
}
