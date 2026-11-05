package records

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// viewer は、操作する人です。ロールと状態は要求ごとに読みます（停止中のアカウントは認証の層で通らない）。
type viewer struct {
	ID    int64
	Admin bool // システム管理者
}

type action int

const (
	actRead action = iota
	actEdit
	actDelete
)

// readableCondition は、一覧で全部の記録を読めるか（false なら自分の記録だけ）を返します。
// 不変条件: RECORDS-INV-3
func readableCondition(a RecordAccess, v viewer) bool {
	switch a {
	case OwnerWriteAllRead, AllReadWrite:
		return true
	case OwnerWriteAdminRead:
		return v.Admin
	}
	return false
}

// checkRecordAccess は、権限の型の表（docs/domain/records.md の7節）のとおりに、操作できるかを返します。
// 読めない記録は、呼び出し側で無い記録と同じ404にします。
// 不変条件: RECORDS-INV-3, RECORDS-INV-4
func checkRecordAccess(a RecordAccess, v viewer, owner int64, act action) bool {
	own := owner == v.ID
	switch a {
	case OwnerOnly:
		return own
	case OwnerWriteAdminRead:
		switch act {
		case actEdit:
			return own
		default:
			return own || v.Admin
		}
	case OwnerWriteAllRead:
		switch act {
		case actRead:
			return true
		case actEdit:
			return own
		default:
			return own || v.Admin
		}
	case AllReadWrite:
		return true
	}
	return false
}

// maxLen は、文字の項目の上限です。
func maxLen(f RecordField, t FieldType) int {
	switch {
	case f.MaxLen > 0:
		return f.MaxLen
	case t == LongText:
		return defaultLongTextMaxLen
	default:
		return defaultTextMaxLen
	}
}

// validateFieldValue は、入力を項目の型と見せ方で検査し、保存する値にします。空は nil（NULL）です。
// エラーなら、画面に出す文を返します（入力の値は文に入れない）。
// 不変条件: RECORDS-INV-6
func validateFieldValue(f RecordField, t FieldType, raw string) (any, string) {
	if t == Bool {
		if raw == "" {
			return int64(0), ""
		}
		if raw != "1" {
			return nil, "選び直してください。"
		}
		return int64(1), ""
	}
	if t != LongText {
		raw = strings.TrimSpace(raw)
	} else {
		raw = strings.ReplaceAll(raw, "\r\n", "\n")
	}
	if strings.TrimSpace(raw) == "" {
		if f.Required {
			return nil, f.Label + "を入力してください。"
		}
		return nil, ""
	}
	if !utf8.ValidString(raw) {
		return nil, f.Label + "の文字を確認してください。"
	}
	switch t {
	case Text:
		if strings.ContainsAny(raw, "\r\n\t") {
			return nil, f.Label + "は改行やタブを含めずに入力してください。"
		}
		if n := maxLen(f, t); utf8.RuneCountInString(raw) > n {
			return nil, fmt.Sprintf("%sは%d文字以内で入力してください。", f.Label, n)
		}
		return raw, ""
	case LongText:
		if n := maxLen(f, t); utf8.RuneCountInString(raw) > n {
			return nil, fmt.Sprintf("%sは%d文字以内で入力してください。", f.Label, n)
		}
		return raw, ""
	case Integer:
		if !integerForm.MatchString(raw) {
			return nil, f.Label + "は整数で入力してください。"
		}
		n, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || (f.Min != nil && n < *f.Min) || (f.Max != nil && n > *f.Max) {
			return nil, f.Label + "は" + rangeText(f) + "整数で入力してください。"
		}
		return n, ""
	case Date:
		d, err := time.Parse("2006-01-02", raw)
		if err != nil || d.Format("2006-01-02") != raw {
			return nil, f.Label + "は実在する日付（例: 2026-11-06）で入力してください。"
		}
		return raw, ""
	case Select:
		if !slices.Contains(f.Options, raw) {
			return nil, f.Label + "は選択肢から選んでください。"
		}
		return raw, ""
	}
	return nil, f.Label + "を確認してください。"
}

func rangeText(f RecordField) string {
	switch {
	case f.Min != nil && f.Max != nil:
		return fmt.Sprintf("%d〜%dの", *f.Min, *f.Max)
	case f.Min != nil:
		return fmt.Sprintf("%d以上の", *f.Min)
	case f.Max != nil:
		return fmt.Sprintf("%d以下の", *f.Max)
	}
	return ""
}
