package test

import (
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
)

var (
	domainHeading = regexp.MustCompile(`(?m)^## (?:(\d+)\. )?(.+)$`)
	termRow       = regexp.MustCompile(`(?m)^\| ([^|]+) \| ([^|]+) \| `)
)

// domainSections は標準の節の順です。required の節はどのファイルにも要ります。
// この間に機能に固有の節を足せますが、「守らない範囲」は最後に置きます。
var domainSections = []struct {
	name     string
	required bool
}{
	{"用語", true},
	{"概念と関係", false},
	{"状態の移り変わり", false},
	{"不変条件", true},
	{"権限", false},
	{"他の機能との関係", false},
	{"守らない範囲", true},
}

// TestDomain_FormatIsStandard は、docs/domain/*.md が docs/README.md の書き方に従うことを検証します。
func TestDomain_FormatIsStandard(t *testing.T) {
	docs, err := filepath.Glob("../docs/domain/*.md")
	if err != nil {
		t.Fatal(err)
	}
	if len(docs) == 0 {
		t.Fatal("no domain documents found")
	}
	source := codeSource(t)
	for _, doc := range docs {
		body, err := os.ReadFile(doc)
		if err != nil {
			t.Fatal(err)
		}
		for _, problem := range domainFormatProblems(string(body)) {
			t.Errorf("%s: %s", filepath.Base(doc), problem)
		}
		for _, row := range termRows(string(body)) {
			for _, name := range codeNames(row) {
				if !inCode(source, name) {
					t.Errorf("%s の用語の英語名 %s がコードに無い", filepath.Base(doc), name)
				}
			}
		}
	}
}

// TestDomain_FormatCheckerFindsProblems は、検査が崩れを見つけることを確かめます。
func TestDomain_FormatCheckerFindsProblems(t *testing.T) {
	good := `# ドメインモデル: 見本

## 1. 用語

| 用語 | 英語名 | 意味 |
|---|---|---|
| 記事 | ` + "`Post`" + ` | 1件 |
| 公開側 | — | 公開ページ |

## 2. 状態の移り変わり

` + "```mermaid\nstateDiagram-v2\n```" + `

## 3. 不変条件

| 番号 | 条件 | 守る場所 |
|---|---|---|

## 4. 固有の節

本文。

## 5. 守らない範囲

- なし
`
	if got := domainFormatProblems(good); len(got) != 0 {
		t.Fatalf("good sample has problems: %v", got)
	}
	cases := []struct {
		name, from, to, want string
	}{
		{"番号が無い", "## 2. 状態の移り変わり", "## 状態の移り変わり", "番号"},
		{"番号が飛ぶ", "## 3. 不変条件", "## 7. 不変条件", "番号"},
		{"古い節の名前", "## 2. 状態の移り変わり", "## 2. 状態", "状態の移り変わり"},
		{"用語の節の古い名前", "## 1. 用語", "## 1. 用語と関係", "用語"},
		{"権限の節の古い名前", "## 4. 固有の節", "## 4. ロールと権限", "権限"},
		{"順が違う", "## 1. 用語", "## 1. 不変条件", "用語"},
		{"必須の節が無い", "## 5. 守らない範囲", "## 5. 別の節", "守らない範囲"},
		{"守らない範囲の後ろに節", "- なし\n", "- なし\n\n## 6. 追記\n", "最後"},
		{"用語が2列の表", "| 用語 | 英語名 | 意味 |\n|---|---|---|", "| 用語 | 意味 |\n|---|---|", "英語名"},
		{"英語名が識別子でない", "| 記事 | `Post` |", "| 記事 | 投稿 |", "英語名"},
		{"不変条件の列の名前", "| 番号 | 条件 | 守る場所 |", "| 番号 | 条件 | 実装・検証 |", "守る場所"},
		{"状態の図が無い", "```mermaid\nstateDiagram-v2\n```", "作成 → 公開", "stateDiagram"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if !strings.Contains(good, c.from) {
				t.Fatalf("sample text not found: %q", c.from)
			}
			got := strings.Join(domainFormatProblems(strings.Replace(good, c.from, c.to, 1)), "\n")
			if !strings.Contains(got, c.want) {
				t.Errorf("not reported as %q: %q", c.want, got)
			}
		})
	}
}

// domainSectionBody は、見出しの名前の節の本文を返します。
func domainSectionBody(doc, name string) string {
	matches := domainHeading.FindAllStringSubmatchIndex(doc, -1)
	for i, m := range matches {
		if doc[m[4]:m[5]] != name {
			continue
		}
		end := len(doc)
		if i+1 < len(matches) {
			end = matches[i+1][0]
		}
		return doc[m[1]:end]
	}
	return ""
}

// termRows は、用語の表の英語名の列を返します。
func termRows(doc string) []string {
	var rows []string
	for _, m := range termRow.FindAllStringSubmatch(domainSectionBody(doc, "用語"), -1) {
		if m[1] != "用語" && !strings.HasPrefix(m[1], "-") {
			rows = append(rows, m[2])
		}
	}
	return rows
}

// domainFormatProblems は、書き方の崩れを返します。
func domainFormatProblems(doc string) []string {
	var problems []string
	var names []string
	for i, m := range domainHeading.FindAllStringSubmatch(doc, -1) {
		names = append(names, m[2])
		if m[1] != strconv.Itoa(i+1) {
			problems = append(problems, "見出しの番号が 1 から順でない: "+m[0])
		}
	}
	// 標準の節は決まった順に並び、必須の節は全てある
	next := 0
	for _, name := range names {
		if i := domainSectionIndex(name); i >= 0 {
			if i < next {
				problems = append(problems, "節の順が違う: "+name)
			}
			next = i
		}
	}
	for _, s := range domainSections {
		if s.required && !slices.Contains(names, s.name) {
			problems = append(problems, "必須の節が無い: "+s.name)
		}
	}
	for _, retired := range []struct{ from, to string }{{"用語と関係", "用語"}, {"状態", "状態の移り変わり"}, {"ロールと権限", "権限"}} {
		if slices.Contains(names, retired.from) {
			problems = append(problems, "節の名前は "+retired.to+" にする: "+retired.from)
		}
	}
	if len(names) > 0 && slices.Contains(names, "守らない範囲") && names[len(names)-1] != "守らない範囲" {
		problems = append(problems, "守らない範囲 は最後の節にする")
	}

	if body := domainSectionBody(doc, "用語"); body != "" {
		if !strings.Contains(body, "| 用語 | 英語名 | 意味 |") {
			problems = append(problems, "用語は「用語・英語名・意味」の表にする")
		}
		for _, cell := range termRows(doc) {
			if cell != "—" && len(codeNames(cell)) == 0 {
				problems = append(problems, "英語名はコードの名前か — にする: "+cell)
			}
		}
	}
	if body := domainSectionBody(doc, "不変条件"); body != "" && !strings.Contains(body, "| 番号 | 条件 | 守る場所 |") {
		problems = append(problems, "不変条件は「番号・条件・守る場所」の表にする")
	}
	if slices.Contains(names, "状態の移り変わり") {
		body := domainSectionBody(doc, "状態の移り変わり")
		if !strings.Contains(body, "stateDiagram-v2") && !strings.Contains(body, "状態の遷移は無い") {
			problems = append(problems, "状態の移り変わりには stateDiagram-v2 の図か「状態の遷移は無い」を書く")
		}
	}
	return problems
}

func domainSectionIndex(name string) int {
	for i, s := range domainSections {
		if s.name == name {
			return i
		}
	}
	return -1
}
