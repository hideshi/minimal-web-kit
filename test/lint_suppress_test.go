package test

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// 静的解析（make lint）の指摘を、理由を書かずに黙らせていないことを確かめる。
// 行ごとの除外は、規則の番号と理由を書く。規則ごと外すときは、Makefile か staticcheck.conf のコメントに理由を書く。

var (
	// #nosec G304 -- 理由（gosec。規則は1つ以上、「--」の後に理由）
	nosecOK = regexp.MustCompile(`#nosec(\s+G\d{3})+\s+--\s+\S`)
	// //lint:ignore SA1019 理由（staticcheck。番号はカンマ区切り、その後に理由）
	lintIgnoreOK = regexp.MustCompile(`//lint:(file-)?ignore\s+[A-Z]+[0-9]+(,[A-Z]+[0-9]+)*\s+\S`)
	// //gosec:disable G304 -- 理由
	gosecDisableOK = regexp.MustCompile(`//gosec:disable(\s+G\d{3})+\s+--\s+\S`)
)

// suppressionProblems は、1つのファイルの中の、理由や番号の無い除外の行の番号を返す。
func suppressionProblems(src string) []int {
	var bad []int
	for i, line := range strings.Split(src, "\n") {
		switch {
		case strings.Contains(line, "#nosec") && !nosecOK.MatchString(line),
			strings.Contains(line, "//lint:ignore") && !lintIgnoreOK.MatchString(line),
			strings.Contains(line, "//lint:file-ignore") && !lintIgnoreOK.MatchString(line),
			strings.Contains(line, "//gosec:disable") && !gosecDisableOK.MatchString(line):
			bad = append(bad, i+1)
		}
	}
	return bad
}

// TestLint_SuppressionsHaveReasons は、Goのファイル（試験を含む。gosec は試験を見ないため）の
// 行ごとの除外に、規則の番号と理由があることを確かめる。
func TestLint_SuppressionsHaveReasons(t *testing.T) {
	checked := 0
	for _, root := range []string{"../cmd", "../features", "../internal", "../test"} {
		err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") {
				return err
			}
			b, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			checked++
			if path == "lint_suppress_test.go" || strings.HasSuffix(path, "/lint_suppress_test.go") {
				return nil // この試験の見本の文字列は対象の外
			}
			for _, n := range suppressionProblems(string(b)) {
				t.Errorf("%s:%d: 除外には規則の番号と理由を書いてください", path, n)
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	if checked < 100 {
		t.Fatalf("checked only %d files", checked)
	}
}

// TestLint_CheckerFindsMissingReasons は、検査が理由の無い除外を見つけることを確かめる。
func TestLint_CheckerFindsMissingReasons(t *testing.T) {
	for src, bad := range map[string]bool{
		"f() // #nosec G304 -- 運営者が決めるパス":  false,
		"f() // #nosec G306 G703 -- 公開の文書": false,
		"f() // #nosec":                     true,
		"f() // #nosec G304":                true,
		"f() // #nosec -- 理由だけ":             true,
		"//lint:ignore SA1019 古い版との互換のため":   false,
		"//lint:ignore SA1019,S1016 互換のため":  false,
		"//lint:ignore SA1019":              true,
		"//lint:file-ignore U1000":          true,
		"//gosec:disable G304 -- 運営者が決めるパス": false,
		"//gosec:disable G304":              true,
		"//gosec:disable":                   true,
		"x := 1 // ふつうのコメント":                false,
	} {
		if got := len(suppressionProblems(src)) > 0; got != bad {
			t.Errorf("%q: found=%v, want %v", src, got, bad)
		}
	}
}

// TestLint_ConfigExclusionsHaveReasons は、規則ごとの除外に理由があり、gosec の行ごとの除外に
// 理由を必須にする設定が外されていないことを確かめる。
func TestLint_ConfigExclusionsHaveReasons(t *testing.T) {
	mk, err := os.ReadFile("../Makefile")
	if err != nil {
		t.Fatal(err)
	}
	var gosec, comments string
	for _, line := range strings.Split(string(mk), "\n") {
		switch {
		case strings.HasPrefix(line, "#"):
			comments += line + "\n"
		case strings.HasPrefix(line, "\t$(GOSEC)"):
			gosec = line
		}
	}
	if gosec == "" {
		t.Fatal("make lint does not run gosec")
	}
	for _, want := range []string{"-nosec-require-rules", "-nosec-require-justification"} {
		if !strings.Contains(gosec, want) {
			t.Errorf("gosec must run with %s", want)
		}
	}
	if regexp.MustCompile(`\s-nosec(\s|=|$)|-nosec-tag`).MatchString(gosec) {
		t.Error("gosec must not ignore or rename #nosec")
	}
	for _, m := range regexp.MustCompile(`-exclude=([A-Z0-9,]+)`).FindAllStringSubmatch(gosec, -1) {
		for _, id := range strings.Split(m[1], ",") {
			if !strings.Contains(comments, "gosec の "+id+":") {
				t.Errorf("Makefile: gosec の %s を外す理由を「# gosec の %s: 理由」の形で書いてください", id, id)
			}
		}
	}

	conf, err := os.ReadFile("../staticcheck.conf")
	if err != nil {
		t.Fatal(err)
	}
	var confComments string
	for _, line := range strings.Split(string(conf), "\n") {
		if strings.HasPrefix(line, "#") {
			confComments += line + "\n"
			continue
		}
		if !strings.HasPrefix(strings.TrimSpace(line), "checks") {
			continue
		}
		for _, m := range regexp.MustCompile(`"-([A-Z]+[0-9]*\*?)"`).FindAllStringSubmatch(line, -1) {
			if !strings.Contains(confComments, "# "+m[1]+":") {
				t.Errorf("staticcheck.conf: %s を外す理由を「# %s: 理由」の形で書いてください", m[1], m[1])
			}
		}
	}
}
