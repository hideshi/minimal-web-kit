// minimal-web-kit: bundled-feature analytics
package analytics

import (
	"database/sql"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"regexp"

	"minimal-web-kit/internal/web"
)

var ga4ID = regexp.MustCompile(`^G-[A-Z0-9]{4,20}$`)

// Feature は同梱機能のアクセス解析（GA4）です。経路もDBも持ちません。
// 同意するまでGoogleのスクリプトは読み込みません。動きは internal/web/static/js/analytics.js にあります。
type Feature struct {
	logger *slog.Logger
	id     string
}

func NewFeature(logger *slog.Logger) *Feature {
	return &Feature{logger: logger, id: os.Getenv("ANALYTICS_GA4_ID")}
}

func (f *Feature) Name() string                                       { return "analytics" }
func (f *Feature) Init(*sql.DB) error                                 { return nil }
func (f *Feature) RegisterRoutes(*http.ServeMux, *web.SessionManager) {}

// StaticFiles は、静的書き出しに含めるJSです。
func (f *Feature) StaticFiles() []string { return []string{"/static/js/analytics.js"} }

// Analytics は設定を検査して返します。機能を登録したのに測定IDが誤っていれば、起動と静的書き出しを止めます。
// 不変条件: ANALYTICS-INV-6
func (f *Feature) Analytics() (web.Analytics, error) {
	if !ga4ID.MatchString(f.id) {
		return web.Analytics{}, errors.New("ANALYTICS_GA4_ID must look like G-XXXXXXXXXX")
	}
	return web.Analytics{GA4ID: f.id}, nil
}

// PrivacyNotices は、閲覧者について外部へ送る情報を、公開のプライバシーポリシーに載せます。docs/data-inventory.md の13節と揃えます。
func (f *Feature) PrivacyNotices() []web.PrivacyNotice {
	return []web.PrivacyNotice{{
		Title: "アクセス解析で扱う情報",
		Lead:  "同意した場合だけ、Google LLC のGoogle Analyticsを使います。同意しなければ、Googleのスクリプトを読み込まず、情報も送りません。JavaScriptが動く環境では、同意をこの画面の「Cookieの設定」から変えられます。",
		Rows: []web.PrivacyRow{
			{Kind: "閲覧の情報", Items: "見たページのURL、参照元、利用環境、IPアドレス、GoogleのCookie（_ga で始まるもの）の識別子", Purpose: "利用状況の把握", Retention: "Google Analyticsの設定による。このサイトのサーバーには保存しない"},
			{Kind: "同意の結果", Items: "同意したか、しなかったか", Purpose: "同じ案内を繰り返さないため", Retention: "ブラウザの中（localStorage）にだけ保存し、サーバーへは送らない。消すまで残る"},
		},
	}}
}
