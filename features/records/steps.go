package records

import (
	"fmt"
	"regexp"
	"strings"
)

// namePattern は、種類の名前と項目名の規則です。表名と列名に使うので、ほかの文字は通しません。
var namePattern = regexp.MustCompile(`^[a-z][a-z0-9_]{0,29}$`)

// reservedDefNames は、記録の表（records_tables・records_steps）とぶつかる種類の名前です。
var reservedDefNames = map[string]bool{"tables": true, "steps": true}

// 手順の文字列の操作の名前です。形は docs/domain/records.md の8節で固定し、変えません。
const (
	opAdd        = "add"
	opRename     = "rename"
	opRemove     = "remove"
	opChangeType = "change_type"
)

// stepString は、手順を決まった形の文字列にします。操作がちょうど1つでない手順は空を返します。
func stepString(s RecordStep) string {
	if stepOps(s) != 1 {
		return ""
	}
	switch {
	case s.Add != nil:
		return strings.Join([]string{opAdd, s.Add.Field, string(s.Add.Type)}, ":")
	case s.Rename != nil:
		return strings.Join([]string{opRename, s.Rename.From, s.Rename.To}, ":")
	case s.Remove != nil:
		return strings.Join([]string{opRemove, s.Remove.Field}, ":")
	default:
		return strings.Join([]string{opChangeType, s.ChangeType.Field, string(s.ChangeType.From), string(s.ChangeType.To)}, ":")
	}
}

// stepOps は、手順が持つ操作の数です。
func stepOps(s RecordStep) int {
	n := 0
	for _, set := range []bool{s.Add != nil, s.Rename != nil, s.Remove != nil, s.ChangeType != nil} {
		if set {
			n++
		}
	}
	return n
}

// parseStep は、手順の文字列を手順に戻します。形が決まりどおりでなければ false を返します。
// DBから読んだ文字列にも使うので、名前と型の規則もここで確かめます。
func parseStep(text string) (RecordStep, bool) {
	parts := strings.Split(text, ":")
	name := func(s string) bool { return namePattern.MatchString(s) }
	typ := func(s string) bool { return fieldTypes[FieldType(s)] }
	switch {
	case len(parts) == 3 && parts[0] == opAdd && name(parts[1]) && typ(parts[2]):
		return RecordStep{Add: &Add{Field: parts[1], Type: FieldType(parts[2])}}, true
	case len(parts) == 3 && parts[0] == opRename && name(parts[1]) && name(parts[2]):
		return RecordStep{Rename: &Rename{From: parts[1], To: parts[2]}}, true
	case len(parts) == 2 && parts[0] == opRemove && name(parts[1]):
		return RecordStep{Remove: &Remove{Field: parts[1]}}, true
	case len(parts) == 4 && parts[0] == opChangeType && name(parts[1]) && typ(parts[2]) && typ(parts[3]):
		return RecordStep{ChangeType: &ChangeType{Field: parts[1], From: FieldType(parts[2]), To: FieldType(parts[3])}}, true
	}
	return RecordStep{}, false
}

// breaking は、前の版の実行ファイルでは扱えない手順（改名・削除・型の変換）かを返します。
func (s RecordStep) breaking() bool { return s.Add == nil }

// conversion は、1つの手順で行える型の変換の区分です。
type conversion int

const (
	convNotAllowed conversion = iota
	convDirect                // 値の形を検査せずに変換できる
	convChecked               // 全部の行の形を検査してから変換する
)

// conversionOf は、型の変換の区分を返します（docs/domain/records.md の8節の表）。
func conversionOf(from, to FieldType) conversion {
	switch from {
	case Text:
		switch to {
		case LongText, Select:
			return convDirect
		case Date, Integer:
			return convChecked
		}
	case LongText:
		switch to {
		case Date, Integer, Text, Select:
			return convChecked
		}
	case Select:
		switch to {
		case Text, LongText:
			return convDirect
		case Date, Integer:
			return convChecked
		}
	case Date, Integer:
		switch to {
		case Text, LongText:
			return convDirect
		case Bool:
			if from == Integer {
				return convChecked
			}
		}
	case Bool:
		if to == Integer {
			return convDirect
		}
	}
	return convNotAllowed
}

// column は、手順を当てた結果の1つの項目の列です。
type column struct {
	name string
	typ  FieldType
}

// schema は、手順を順に当てた結果です。列の順は、足した順です。
type schema struct {
	columns []column
	used    map[string]bool // 一度でも使った項目名（改名の前の名前、消した項目を含む）
}

func newSchema() *schema { return &schema{used: map[string]bool{}} }

func (sc *schema) index(name string) int {
	for i, c := range sc.columns {
		if c.name == name {
			return i
		}
	}
	return -1
}

// apply は、手順を1つ当てます。前提が成り立たなければエラーを返し、何も変えません。
func (sc *schema) apply(s RecordStep) error {
	if stepOps(s) != 1 {
		return fmt.Errorf("step %d must have exactly one operation", s.Version)
	}
	switch {
	case s.Add != nil:
		if !namePattern.MatchString(s.Add.Field) {
			return fmt.Errorf("step %d: invalid field name %q", s.Version, s.Add.Field)
		}
		if !fieldTypes[s.Add.Type] {
			return fmt.Errorf("step %d: unknown type %q", s.Version, s.Add.Type)
		}
		if sc.used[s.Add.Field] {
			return fmt.Errorf("step %d: field name %q is already used", s.Version, s.Add.Field)
		}
		sc.columns = append(sc.columns, column{name: s.Add.Field, typ: s.Add.Type})
		sc.used[s.Add.Field] = true
	case s.Rename != nil:
		i := sc.index(s.Rename.From)
		if i < 0 {
			return fmt.Errorf("step %d: rename source %q does not exist", s.Version, s.Rename.From)
		}
		if !namePattern.MatchString(s.Rename.To) {
			return fmt.Errorf("step %d: invalid field name %q", s.Version, s.Rename.To)
		}
		if sc.used[s.Rename.To] {
			return fmt.Errorf("step %d: field name %q is already used", s.Version, s.Rename.To)
		}
		sc.columns[i].name = s.Rename.To
		sc.used[s.Rename.To] = true
	case s.Remove != nil:
		i := sc.index(s.Remove.Field)
		if i < 0 {
			return fmt.Errorf("step %d: removed field %q does not exist", s.Version, s.Remove.Field)
		}
		sc.columns = append(sc.columns[:i:i], sc.columns[i+1:]...)
	default:
		c := s.ChangeType
		i := sc.index(c.Field)
		if i < 0 {
			return fmt.Errorf("step %d: field %q does not exist", s.Version, c.Field)
		}
		if sc.columns[i].typ != c.From {
			return fmt.Errorf("step %d: field %q is %s, not %s", s.Version, c.Field, sc.columns[i].typ, c.From)
		}
		if conversionOf(c.From, c.To) == convNotAllowed {
			return fmt.Errorf("step %d: converting %s to %s is not allowed", s.Version, c.From, c.To)
		}
		sc.columns[i].typ = c.To
	}
	return nil
}

// replaySteps は、手順を1から順に当てた結果を返します。番号が1から1ずつ増えていなければエラーです。
func replaySteps(steps []RecordStep) (*schema, error) {
	sc := newSchema()
	for i, s := range steps {
		if s.Version != i+1 {
			return nil, fmt.Errorf("step versions must start at 1 and increase by 1 (got %d at position %d)", s.Version, i+1)
		}
		if err := sc.apply(s); err != nil {
			return nil, err
		}
	}
	return sc, nil
}
