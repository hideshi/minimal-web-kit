package test

import (
	"testing"

	"minimal-web-kit/features/records"
	"minimal-web-kit/features/records/defs"
)

// TestRecords_InventoryMatchesDefinitions は、業務記録の定義の記録と、台帳の業務記録の節が、今の定義から作ったものと同じかを確かめます。
// 定義を変えて make records-inventory を忘れると落ちます。前の定義との比較（適用済みの手順の書き換え等）も行います。
// 不変条件: RECORDS-INV-9
func TestRecords_InventoryMatchesDefinitions(t *testing.T) {
	if err := records.CheckInventory("../features/records/defs/records.lock.json", "../docs/data-inventory.md", defs.Defs, defs.Retired); err != nil {
		t.Fatal(err)
	}
}
