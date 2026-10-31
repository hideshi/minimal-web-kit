package test

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// bundledLabels は、同梱機能とトップの見出しの対応です。機能を足したら、トップにカードを足してここにも足します。
var bundledLabels = map[string]string{
	"site":          "LPテンプレート",
	"news":          "お知らせ",
	"catalog":       "カタログ",
	"contact":       "お問い合わせ",
	"pages":         "固定ページ",
	"mcp":           "MCP（AIエージェント）",
	"auth":          "パスキーの管理者認証",
	"federatedauth": "共通認証（自社システム向け）",
}

// landingMark は、トップがキットの紹介ページのままであることの印です。
// キットから別のサイトを作ってトップを書き換えたら、印を消します。
const landingMark = "<!-- kit-landing:"

// TestLanding_KeepsUpWithTheKit は、機能やスキルを足してもトップが古くならないように、
// 同梱機能のカード、スキルの本数を実物と突き合わせます。文章の正しさまでは確かめません。
// キットの紹介ページだけが対象で、印が無いトップやSiteを消したアプリでは検査しません。
func TestLanding_KeepsUpWithTheKit(t *testing.T) {
	raw, err := os.ReadFile("../features/site/views/index.html")
	if errors.Is(err, fs.ErrNotExist) {
		t.Skip("Siteが無いので、紹介ページの検査はしない")
	}
	if err != nil {
		t.Fatal(err)
	}
	page := string(raw)
	if !strings.Contains(page, landingMark) {
		t.Skip("トップに印が無いので、紹介ページの検査はしない")
	}

	entries, err := os.ReadDir("../features")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		label, ok := bundledLabels[e.Name()]
		if !ok {
			t.Errorf("features/%s をトップの同梱機能に載せ、bundledLabels に足す（別のサイトならトップの印 kit-landing を消す）", e.Name())
			continue
		}
		if !strings.Contains(page, `<h3 class="card-title">`+label+`</h3>`) {
			t.Errorf("トップに %s のカードが無い", label)
		}
	}

	skills, err := filepath.Glob("../skills/*/SKILL.md")
	if err != nil {
		t.Fatal(err)
	}
	if want := fmt.Sprintf("同梱の%d本のスキル", len(skills)); !strings.Contains(page, want) {
		t.Errorf("トップに %q が無い（スキルの本数を実物に合わせる）", want)
	}

	// go.mod の直接依存は共通認証の分を含むので、構成を問わない断定をしない
	for _, file := range []string{"../features/site/views/index.html", "../features/site/views/layout.html", "../features/site/site.go"} {
		body, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		for _, claim := range []string{"Goの直接依存はSQLiteドライバのみ", "Goの直接依存はSQLiteのみ"} {
			if strings.Contains(string(body), claim) {
				t.Errorf("%s に「%s」と書かない", file, claim)
			}
		}
	}
}
