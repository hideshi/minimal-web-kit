// minimal-web-kit: bundled-feature records

// Package records は、業務記録を、このアプリの定義（features/records/defs）で登録するための入口です。
// make add-records が cmd/server/features.go に r.Register(records.NewFeature(logger)) を足します。
// 業務記録の本体は minimal-web-kit/features/records です。
package records

import (
	"log/slog"

	core "minimal-web-kit/features/records"
	"minimal-web-kit/features/records/defs"
)

// NewFeature は、このアプリの定義の一覧とやめた種類で、業務記録の機能を作ります。
func NewFeature(logger *slog.Logger) *core.Feature {
	return core.NewFeature(logger, defs.Defs, defs.Retired)
}
