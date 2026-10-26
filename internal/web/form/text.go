// Package form は、フォーム入力の検査を行う部品です。
// 利用者の入力は、保存・表示の前に必ずここで検査します（自動エスケープの回避はしない）。
package form

import (
	"errors"
	"strconv"
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

// Int はクエリ等の整数を解析します。未指定（空文字）なら def を返し、数値でない・範囲外なら ok=false を返します。
func Int(raw string, def, min, max int) (n int, ok bool) {
	if raw == "" {
		return def, true
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < min || n > max {
		return 0, false
	}
	return n, true
}

// Field は、共通の画面部品 "field"（入力欄）に渡す値です。ラベル・説明・エラーを入力欄に関連付けて描画します。
type Field struct {
	ID        string // 入力欄のid。説明は ID+"-hint"、エラーは ID+"-error" になる
	Name      string
	Label     string
	Hint      string // 入力の条件等の説明（任意）
	Error     string // この欄のエラー（任意）。表示すると aria-invalid="true" になる
	Value     string
	Type      string // 既定は "text"（例: "datetime-local"、"email"）
	MaxLength int    // 0なら指定しない
	Rows      int    // 1以上なら複数行（textarea）
	Required  bool
}

// Combobox は、共通の画面部品 "combobox"（入力で候補を絞り込み、複数を選ぶ部品）に渡す値です。
// JSが動かない場合は、同じ名前のチェックボックスの一覧として送信します。
type Combobox struct {
	ID      string // 要素のidの接頭辞。1画面に複数置くときは別の値にする
	Name    string // 送信する名前。選んだ値ごとに繰り返す
	Label   string // 選ぶものの名前（例: "タグ"）
	Hint    string // 上限等の説明（任意）
	Limit   int    // 選べる数の上限。0なら上限なし。送信内容の検査はサーバー側で別に行う
	Options []Option
}

// Option は Combobox の候補です。
type Option struct {
	Value    string
	Label    string
	Selected bool
}
