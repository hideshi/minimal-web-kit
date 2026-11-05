package records

// FieldType は項目の論理の型です。値は手順の文字列にそのまま入るので、変えません（docs/domain/records.md の8節）。
type FieldType string

const (
	Text     FieldType = "text"
	LongText FieldType = "long_text"
	Integer  FieldType = "integer"
	Date     FieldType = "date"
	Select   FieldType = "select"
	Bool     FieldType = "bool"
)

// fieldTypes は、決まった値の論理の型の一覧です。
var fieldTypes = map[FieldType]bool{Text: true, LongText: true, Integer: true, Date: true, Select: true, Bool: true}

// sqlType は、論理の型に合う列の宣言の型です。
func (t FieldType) sqlType() string {
	switch t {
	case Integer, Bool:
		return "INTEGER"
	default:
		return "TEXT"
	}
}

// RecordAccess は種類ごとの権限の型です（docs/domain/records.md の7節）。ゼロ値は決まった値ではないので、書き忘れは定義の検査で止まります。
type RecordAccess int

const (
	OwnerOnly RecordAccess = iota + 1
	OwnerWriteAdminRead
	OwnerWriteAllRead
	AllReadWrite
)

// Add は、項目を足す手順です。
type Add struct {
	Field string
	Type  FieldType
}

// Rename は、項目の名前を変える手順です。
type Rename struct {
	From, To string
}

// Remove は、項目を消す手順です。
type Remove struct {
	Field string
}

// ChangeType は、項目の論理の型を変える手順です。
type ChangeType struct {
	Field    string
	From, To FieldType
}

// RecordStep は、種類の項目の構造の変更の1つです。操作はちょうど1つだけ持ちます。
// 番号は1から1ずつ増やし、一度でも起動した環境のある手順は、消さず、書き換えません。
type RecordStep struct {
	Version    int
	Add        *Add
	Rename     *Rename
	Remove     *Remove
	ChangeType *ChangeType
}

// RecordField は、項目の見せ方です。論理の型は手順から決まります。
type RecordField struct {
	Name     string
	Label    string
	Required bool
	// MaxLen は Text・LongText の文字数の上限です。0なら既定（Text は200、LongText は4000）。
	MaxLen int
	// Min・Max は Integer の範囲です。nil なら制限しません。
	Min, Max *int64
	// Options は Select の選択肢です。
	Options []string
}

// RecordDef は、記録の1つの種類の定義です。
type RecordDef struct {
	Name      string
	Title     string
	Purpose   string
	Retention string
	Access    RecordAccess
	MCP       bool
	Steps     []RecordStep
	Fields    []RecordField
	// ListFields は一覧に出す項目です。
	ListFields []string
	// SortBy は既定の並びの項目です。空なら更新日時。
	SortBy string
}

// RetiredDef は、やめた種類です。名前と、表を消した印だけを持ちます。消しません。
type RetiredDef struct {
	Name    string
	Dropped bool
}

// 既定の文字数の上限です。
const (
	defaultTextMaxLen     = 200
	defaultLongTextMaxLen = 4000
)
