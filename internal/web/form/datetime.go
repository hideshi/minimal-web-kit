package form

import "time"

// DateTimeLayout は <input type="datetime-local"> の値の形式です。
const DateTimeLayout = "2006-01-02T15:04"

// ParseLocalDateTime は datetime-local の入力を、サーバーのタイムゾーンの日時として読みます。
// 空ならゼロ値と true を返します（変更しない）。ブラウザによっては秒まで送るため、両方の形式を受け付けます。1970年より前は誤りです。
func ParseLocalDateTime(raw string) (time.Time, bool) {
	if raw == "" {
		return time.Time{}, true
	}
	t, err := time.ParseInLocation(DateTimeLayout, raw, time.Local)
	if err != nil {
		t, err = time.ParseInLocation(DateTimeLayout+":05", raw, time.Local)
	}
	if err != nil || t.Year() < 1970 {
		return time.Time{}, false
	}
	return t, true
}

// FormatLocalDateTime は、日時を datetime-local の値にします。ゼロ値なら空です。
func FormatLocalDateTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.In(time.Local).Format(DateTimeLayout)
}
