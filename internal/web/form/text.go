// Package form は、フォーム入力の検査を行う部品です。
// 利用者の入力は、保存・表示の前に必ずここで検査します（自動エスケープの回避はしない）。
package form

import (
	"errors"
	"strings"
	"unicode"
	"unicode/utf8"
)

// ErrInvalid は入力が制約を満たさないことを示します。理由は利用者に返さず、固定の文言で修正を求めます。
var ErrInvalid = errors.New("form: invalid input")

// Text は複数行の文章を検査します。改行をLFに揃え、UTF-8・制御文字（改行・タブ以外）・文字数を確認します。
// required なら、空白だけの入力も拒否します。前後の空白は残します（本文の字下げ等を保つため）。
func Text(s string, maxRunes int, required bool) (string, error) {
	if !utf8.ValidString(s) {
		return "", ErrInvalid
	}
	s = strings.ReplaceAll(s, "\r\n", "\n")
	for _, r := range s {
		if unicode.IsControl(r) && r != '\n' && r != '\t' {
			return "", ErrInvalid
		}
	}
	if required && strings.TrimSpace(s) == "" {
		return "", ErrInvalid
	}
	if utf8.RuneCountInString(s) > maxRunes {
		return "", ErrInvalid
	}
	return s, nil
}

// Line は1行の値（タイトル、名前等）を検査します。前後の空白を除き、改行を含む入力を拒否します。
func Line(s string, maxRunes int, required bool) (string, error) {
	s, err := Text(strings.TrimSpace(s), maxRunes, required)
	if err != nil || strings.Contains(s, "\n") {
		return "", ErrInvalid
	}
	return s, nil
}
