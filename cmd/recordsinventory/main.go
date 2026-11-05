// Command recordsinventory は、業務記録の定義の記録（records.lock.json）と、台帳（docs/data-inventory.md）の
// 業務記録の節を、定義から作り直します（make records-inventory）。リポジトリの最上位で実行します。
package main

import (
	"flag"
	"fmt"
	"os"

	"minimal-web-kit/features/records"
	"minimal-web-kit/features/records/defs"
)

func main() {
	reset := flag.Bool("reset", false, "ignore the previous lock (only before any environment has started these definitions)")
	flag.Parse()
	if err := records.UpdateInventory("features/records/defs/records.lock.json", "docs/data-inventory.md", defs.Defs, defs.Retired, *reset); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Println("updated features/records/defs/records.lock.json and docs/data-inventory.md")
}
