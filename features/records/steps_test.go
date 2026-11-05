package records

import (
	"strings"
	"testing"
)

// validDef は、検査に通る最小の定義です。テストはこれを1か所ずつ壊して使います。
func validDef() RecordDef {
	return RecordDef{
		Name: "visit_log", Title: "訪問記録", Purpose: "訪問の記録のため", Retention: "消すまで", Access: OwnerWriteAllRead,
		Steps: []RecordStep{
			{Version: 1, Add: &Add{Field: "visited_on", Type: Date}},
			{Version: 2, Add: &Add{Field: "customer", Type: Text}},
			{Version: 3, Add: &Add{Field: "result", Type: Select}},
		},
		Fields: []RecordField{
			{Name: "visited_on", Label: "訪問日", Required: true},
			{Name: "customer", Label: "訪問先", MaxLen: 100},
			{Name: "result", Label: "結果", Options: []string{"受注", "継続", "見送り"}},
		},
		ListFields: []string{"visited_on", "customer"},
		SortBy:     "visited_on",
	}
}

// TestStepString_FixedForm は、手順の文字列の形が決まった値のまま変わらないことを確かめます。
// 形を変えると、全部の環境で正しい更新まで止まるため、値をそのまま固定します。
// 不変条件: RECORDS-INV-15
func TestStepString_FixedForm(t *testing.T) {
	cases := []struct {
		step RecordStep
		want string
	}{
		{RecordStep{Add: &Add{Field: "work_date", Type: Date}}, "add:work_date:date"},
		{RecordStep{Add: &Add{Field: "body", Type: LongText}}, "add:body:long_text"},
		{RecordStep{Rename: &Rename{From: "customer", To: "client"}}, "rename:customer:client"},
		{RecordStep{Remove: &Remove{Field: "memo"}}, "remove:memo"},
		{RecordStep{ChangeType: &ChangeType{Field: "qty", From: Text, To: Integer}}, "change_type:qty:text:integer"},
		{RecordStep{ChangeType: &ChangeType{Field: "done", From: Integer, To: Bool}}, "change_type:done:integer:bool"},
		{RecordStep{}, ""},
		{RecordStep{Add: &Add{Field: "a", Type: Text}, Remove: &Remove{Field: "a"}}, ""},
	}
	for _, c := range cases {
		if got := stepString(c.step); got != c.want {
			t.Errorf("stepString = %q, want %q", got, c.want)
		}
		if c.want == "" {
			continue
		}
		back, ok := parseStep(c.want)
		if !ok || stepString(back) != c.want {
			t.Errorf("parseStep(%q) did not round-trip: %q %v", c.want, stepString(back), ok)
		}
	}
}

// TestParseStep_RejectsUnknownForms は、決まった形でない文字列を読めない手順として扱うことを確かめます。
// 読めない手順は、前の版の実行ファイルでは壊す手順として扱います。
// 不変条件: RECORDS-INV-15
func TestParseStep_RejectsUnknownForms(t *testing.T) {
	for _, s := range []string{
		"", "add", "add:a", "add:a:text:x", "add:A:text", "add:a:string", "add:a\";drop:text",
		"rename:a", "rename:a:B", "remove:a:text", "change_type:a:text", "change_type:a:text:float",
		"split:a:b", "ADD:a:text", "add::text",
	} {
		if _, ok := parseStep(s); ok {
			t.Errorf("parseStep(%q) accepted", s)
		}
	}
}

// TestReplaySteps_FollowsStepsInOrder は、改名・削除・型の変換を順に当てた結果を確かめます。
// 不変条件: RECORDS-INV-2
func TestReplaySteps_FollowsStepsInOrder(t *testing.T) {
	sc, err := replaySteps([]RecordStep{
		{Version: 1, Add: &Add{Field: "a", Type: Text}},
		{Version: 2, Add: &Add{Field: "b", Type: Bool}},
		{Version: 3, Rename: &Rename{From: "a", To: "c"}},
		{Version: 4, ChangeType: &ChangeType{Field: "b", From: Bool, To: Integer}},
		{Version: 5, ChangeType: &ChangeType{Field: "b", From: Integer, To: Text}},
		{Version: 6, Add: &Add{Field: "d", Type: Date}},
		{Version: 7, Remove: &Remove{Field: "c"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, c := range sc.columns {
		got = append(got, c.name+":"+string(c.typ))
	}
	if strings.Join(got, ",") != "b:text,d:date" {
		t.Fatalf("columns = %v", got)
	}
}

// TestReplaySteps_RejectsBrokenSteps は、前提の成り立たない手順と、番号の誤りを拒否することを確かめます。
// 不変条件: RECORDS-INV-2
func TestReplaySteps_RejectsBrokenSteps(t *testing.T) {
	add := func(v int, f string, typ FieldType) RecordStep {
		return RecordStep{Version: v, Add: &Add{Field: f, Type: typ}}
	}
	cases := map[string][]RecordStep{
		"番号が1から始まらない":  {add(2, "a", Text)},
		"番号が飛ぶ":        {add(1, "a", Text), add(3, "b", Text)},
		"操作が無い":        {{Version: 1}},
		"操作が2つ":        {{Version: 1, Add: &Add{Field: "a", Type: Text}, Remove: &Remove{Field: "a"}}},
		"同じ名前を2回足す":    {add(1, "a", Text), add(2, "a", Text)},
		"消した名前を使い回す":   {add(1, "a", Text), {Version: 2, Remove: &Remove{Field: "a"}}, add(3, "a", Text)},
		"改名の前の名前を使い回す": {add(1, "a", Text), {Version: 2, Rename: &Rename{From: "a", To: "b"}}, add(3, "a", Text)},
		"改名の元が無い":      {add(1, "a", Text), {Version: 2, Rename: &Rename{From: "x", To: "b"}}},
		"改名の先が使用中":     {add(1, "a", Text), add(2, "b", Text), {Version: 3, Rename: &Rename{From: "a", To: "b"}}},
		"消す項目が無い":      {add(1, "a", Text), {Version: 2, Remove: &Remove{Field: "x"}}},
		"変換の前の型が違う":    {add(1, "a", Text), {Version: 2, ChangeType: &ChangeType{Field: "a", From: Integer, To: Text}}},
		"許されない変換":      {add(1, "a", Date), {Version: 2, ChangeType: &ChangeType{Field: "a", From: Date, To: Integer}}},
		"不正な名前":        {add(1, `a"; DROP TABLE x; --`, Text)},
		"大文字の名前":       {add(1, "Name", Text)},
		"全角の名前":        {add(1, "なまえ", Text)},
		"31字の名前":       {add(1, "a"+strings.Repeat("b", 30), Text)},
		"空の名前":         {add(1, "", Text)},
		"知らない型":        {add(1, "a", FieldType("float"))},
		"改名の先が不正":      {add(1, "a", Text), {Version: 2, Rename: &Rename{From: "a", To: "B"}}},
	}
	for name, steps := range cases {
		if _, err := replaySteps(steps); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

// TestConversionOf_MatchesTheTable は、型の変換の区分が仕様の表どおりであることを確かめます。
// 不変条件: RECORDS-INV-17
func TestConversionOf_MatchesTheTable(t *testing.T) {
	direct := "text>long_text text>select select>text select>long_text date>text date>long_text integer>text integer>long_text bool>integer"
	checked := "text>date text>integer long_text>date long_text>integer select>date select>integer long_text>text long_text>select integer>bool"
	want := map[string]conversion{}
	for _, p := range strings.Fields(direct) {
		want[p] = convDirect
	}
	for _, p := range strings.Fields(checked) {
		want[p] = convChecked
	}
	for from := range fieldTypes {
		for to := range fieldTypes {
			key := string(from) + ">" + string(to)
			if got := conversionOf(from, to); got != want[key] {
				t.Errorf("%s: got %d, want %d", key, got, want[key])
			}
		}
	}
}

// TestValidateRecordDefs_AcceptsValid は、検査に通る定義が通ることを確かめます（他のテストの前提）。
// 不変条件: RECORDS-INV-2
func TestValidateRecordDefs_AcceptsValid(t *testing.T) {
	if err := validateRecordDefs([]RecordDef{validDef()}, []RetiredDef{{Name: "old_kind"}}); err != nil {
		t.Fatal(err)
	}
}

// TestValidateRecordDefs_RejectsBrokenDefs は、定義の誤りを1つずつ入れて、起動を止めるエラーになることを確かめます。
// 不変条件: RECORDS-INV-2
func TestValidateRecordDefs_RejectsBrokenDefs(t *testing.T) {
	min, max := int64(10), int64(1)
	cases := map[string]func(d *RecordDef){
		"不正な種類の名前":      func(d *RecordDef) { d.Name = `visit"log` },
		"予約した名前 tables": func(d *RecordDef) { d.Name = "tables" },
		"予約した名前 steps":  func(d *RecordDef) { d.Name = "steps" },
		"表示名が空":         func(d *RecordDef) { d.Title = " " },
		"目的が空":          func(d *RecordDef) { d.Purpose = "" },
		"保持が空":          func(d *RecordDef) { d.Retention = "" },
		"権限の型が無い":       func(d *RecordDef) { d.Access = 0 },
		"権限の型が範囲外":      func(d *RecordDef) { d.Access = AllReadWrite + 1 },
		"手順が無い":         func(d *RecordDef) { d.Steps, d.Fields, d.ListFields, d.SortBy = nil, nil, nil, "" },
		"手順の前提が崩れる":     func(d *RecordDef) { d.Steps[1].Add.Field = "visited_on" },
		"見せ方が足りない":      func(d *RecordDef) { d.Fields = d.Fields[:2] },
		"見せ方が余る":        func(d *RecordDef) { d.Fields = append(d.Fields, RecordField{Name: "extra", Label: "余り"}) },
		"見せ方が重複":        func(d *RecordDef) { d.Fields = append(d.Fields, d.Fields[0]) },
		"表示名の無い項目":      func(d *RecordDef) { d.Fields[0].Label = "" },
		"日付に上限":         func(d *RecordDef) { d.Fields[0].MaxLen = 10 },
		"負の上限":          func(d *RecordDef) { d.Fields[1].MaxLen = -1 },
		"文字に範囲":         func(d *RecordDef) { d.Fields[1].Min = &min },
		"選択肢が無い":        func(d *RecordDef) { d.Fields[2].Options = nil },
		"選択肢の重複":        func(d *RecordDef) { d.Fields[2].Options = []string{"a", "a"} },
		"選択肢に改行":        func(d *RecordDef) { d.Fields[2].Options = []string{"a\nb"} },
		"文字に選択肢":        func(d *RecordDef) { d.Fields[1].Options = []string{"a"} },
		"一覧の項目が無い":      func(d *RecordDef) { d.ListFields = []string{"nothing"} },
		"並びの項目が無い":      func(d *RecordDef) { d.SortBy = "nothing" },
		"一覧の項目が空":       func(d *RecordDef) { d.ListFields = nil },
		"一覧の項目が重複":      func(d *RecordDef) { d.ListFields = []string{"customer", "customer"} },
	}
	for name, mutate := range cases {
		d := validDef()
		mutate(&d)
		if err := validateRecordDefs([]RecordDef{d}, nil); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	intDef := validDef()
	intDef.Steps = append(intDef.Steps, RecordStep{Version: 4, Add: &Add{Field: "qty", Type: Integer}})
	intDef.Fields = append(intDef.Fields, RecordField{Name: "qty", Label: "数", Min: &min, Max: &max})
	if err := validateRecordDefs([]RecordDef{intDef}, nil); err == nil {
		t.Error("Min > Max: accepted")
	}
}

// TestValidateRecordDefs_RejectsNameClashes は、種類の名前の重複と、やめた種類との重複を拒否することを確かめます。
// 不変条件: RECORDS-INV-2
func TestValidateRecordDefs_RejectsNameClashes(t *testing.T) {
	d := validDef()
	if err := validateRecordDefs([]RecordDef{d, d}, nil); err == nil {
		t.Error("duplicate definitions: accepted")
	}
	if err := validateRecordDefs([]RecordDef{d}, []RetiredDef{{Name: d.Name}}); err == nil {
		t.Error("retired name used by a definition: accepted")
	}
	if err := validateRecordDefs(nil, []RetiredDef{{Name: "a"}, {Name: "a"}}); err == nil {
		t.Error("retired listed twice: accepted")
	}
	if err := validateRecordDefs(nil, []RetiredDef{{Name: "tables"}}); err == nil {
		t.Error("reserved retired name: accepted")
	}
}
