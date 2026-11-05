// Package defs は、業務記録の種類の定義です。キットは見本として「日報」と「備品」を同梱します。
// アプリでは、ここを業務に合わせて書き換えます。書き方と手順は docs/domain/records.md の8節にあります。
//
// 定義を変えたら、make records-inventory で定義の記録（records.lock.json）と台帳の節を作り直し、両方をコミットします。
// 項目の構造の変更（追加・改名・削除・型の変換）は、Steps の末尾に手順を積みます。一度でも起動した環境のある手順は、消さず、書き換えません。
package defs

import "minimal-web-kit/features/records"

// Defs は、有効な種類の一覧です。
var Defs = []records.RecordDef{DailyReport, Equipment}

// Retired は、やめた種類の一覧です。消しません。
var Retired = []records.RetiredDef{}

// DailyReport は、見本の「日報」です。書くのは本人、読むのはログインした全員です。
var DailyReport = records.RecordDef{
	Name:      "daily_report",
	Title:     "日報",
	Purpose:   "その日の業務の内容と、翌日の予定を共有するため",
	Retention: "記録を消すまで。削除したアカウントの記録はシステム管理者がまとめて消す",
	Access:    records.OwnerWriteAllRead,
	MCP:       true,
	Steps: []records.RecordStep{
		{Version: 1, Add: &records.Add{Field: "work_date", Type: records.Date}},
		{Version: 2, Add: &records.Add{Field: "body", Type: records.LongText}},
		{Version: 3, Add: &records.Add{Field: "next_plan", Type: records.LongText}},
	},
	Fields: []records.RecordField{
		{Name: "work_date", Label: "日付", Required: true},
		{Name: "body", Label: "内容", Required: true, MaxLen: 4000},
		{Name: "next_plan", Label: "翌日の予定", MaxLen: 2000},
	},
	ListFields: []string{"work_date", "body"},
	SortBy:     "work_date",
}

// Equipment は、見本の「備品」です。全員で読み書きする共有の台帳です。
var Equipment = records.RecordDef{
	Name:      "equipment",
	Title:     "備品",
	Purpose:   "備品の所在と状態を共有するため。使っている人の呼び名を書くので、個人に結びつく",
	Retention: "記録を消すまで。廃棄した備品の記録は、不要になったら消す",
	Access:    records.AllReadWrite,
	MCP:       true,
	Steps: []records.RecordStep{
		{Version: 1, Add: &records.Add{Field: "item_name", Type: records.Text}},
		{Version: 2, Add: &records.Add{Field: "asset_number", Type: records.Text}},
		{Version: 3, Add: &records.Add{Field: "status", Type: records.Select}},
		{Version: 4, Add: &records.Add{Field: "user_name", Type: records.Text}},
		{Version: 5, Add: &records.Add{Field: "memo", Type: records.LongText}},
	},
	Fields: []records.RecordField{
		{Name: "item_name", Label: "品名", Required: true, MaxLen: 100},
		{Name: "asset_number", Label: "管理番号", MaxLen: 50},
		{Name: "status", Label: "状態", Required: true, Options: []string{"在庫", "貸出中", "修理中", "廃棄"}},
		{Name: "user_name", Label: "使っている人の呼び名", MaxLen: 50},
		{Name: "memo", Label: "メモ", MaxLen: 2000},
	},
	ListFields: []string{"item_name", "asset_number", "status", "user_name"},
	SortBy:     "item_name",
}
