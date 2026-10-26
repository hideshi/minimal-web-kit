package form

import (
	"errors"
	"strings"
	"testing"
)

func TestText(t *testing.T) {
	for _, tc := range []struct {
		name, in string
		max      int
		required bool
		want     string
		ok       bool
	}{
		{"CRLFをLFに揃える", "一行目\r\n二行目", 10, true, "一行目\n二行目", true},
		{"タブは許す", "a\tb", 10, true, "a\tb", true},
		{"前後の空白を残す", "  字下げ", 10, true, "  字下げ", true},
		{"任意なら空でよい", "", 10, false, "", true},
		{"必須で空", "", 10, true, "", false},
		{"必須で空白だけ", " \n\t", 10, true, "", false},
		{"制御文字", "a\x00b", 10, false, "", false},
		{"エスケープ文字", "a\x1bb", 10, false, "", false},
		{"不正なUTF-8", "a\xffb", 10, false, "", false},
		{"文字数ちょうど", strings.Repeat("あ", 5), 5, true, strings.Repeat("あ", 5), true},
		{"文字数超過", strings.Repeat("あ", 6), 5, true, "", false},
	} {
		got, err := Text(tc.in, tc.max, tc.required)
		if tc.ok && (err != nil || got != tc.want) {
			t.Errorf("%s: got %q, %v", tc.name, got, err)
		}
		if !tc.ok && !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: want ErrInvalid, got %q, %v", tc.name, got, err)
		}
	}
}

func TestLine(t *testing.T) {
	if got, err := Line("  見出し  ", 10, true); err != nil || got != "見出し" {
		t.Fatalf("got %q, %v", got, err)
	}
	for _, bad := range []string{"一行目\n二行目", "一行目\r\n二行目", "   ", strings.Repeat("a", 11)} {
		if _, err := Line(bad, 10, true); !errors.Is(err, ErrInvalid) {
			t.Errorf("%q must be rejected", bad)
		}
	}
	// 文字数は前後の空白を除いてから数える
	if _, err := Line(" "+strings.Repeat("a", 10)+" ", 10, true); err != nil {
		t.Fatal("surrounding spaces must not count")
	}
}

func TestInt(t *testing.T) {
	for _, tc := range []struct {
		raw  string
		want int
		ok   bool
	}{
		{"", 20, true}, {"1", 1, true}, {"50", 50, true},
		{"0", 0, false}, {"51", 0, false}, {"-1", 0, false}, {"abc", 0, false}, {"1.5", 0, false}, {" 1", 0, false},
	} {
		got, ok := Int(tc.raw, 20, 1, 50)
		if ok != tc.ok || (ok && got != tc.want) {
			t.Errorf("%q: got %d, %v", tc.raw, got, ok)
		}
	}
}
