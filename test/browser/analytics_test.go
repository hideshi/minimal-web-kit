package browser

import (
	"context"
	"io"
	"log/slog"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/chromedp"

	"minimal-web-kit/features"
	"minimal-web-kit/features/analytics"
	"minimal-web-kit/features/site"
	"minimal-web-kit/internal/app"
	"minimal-web-kit/internal/store"
)

const gtagTags = `document.querySelectorAll('script[src*="googletagmanager"]').length`

func analyticsServer(t *testing.T) *httptest.Server {
	t.Helper()
	t.Setenv("ANALYTICS_GA4_ID", "G-TEST12345")
	core, err := store.NewSQLiteStore(filepath.Join(t.TempDir(), "app.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { core.Close() })
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	reg := features.NewRegistry()
	reg.Register(site.NewFeature(logger))
	reg.Register(analytics.NewFeature(logger))
	routes, err := app.BuildRoutes(core, reg, false, logger)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(routes)
	t.Cleanup(srv.Close)
	return srv
}

// 不変条件: ANALYTICS-INV-2, ANALYTICS-INV-4, ANALYTICS-INV-5
// ユースケース: UC-ANALYTICS-1, UC-ANALYTICS-2
func TestAnalytics_ConsentFlow(t *testing.T) {
	srv := analyticsServer(t)
	b := newBrowser(t)
	bannerVisible := `!document.getElementById("analytics-consent").hidden`
	// 外へは出さない。挿入された script 要素の数で確かめる
	b.do(t, blockURL("https://www.googletagmanager.com/*"), chromedp.Navigate(srv.URL+"/"))
	b.waitFor(t, bannerVisible)
	if n := evaluate[int](t, b, gtagTags); n != 0 {
		t.Fatalf("loaded before consent: %d", n)
	}
	// 拒否: 読み込まず、次のページで案内も出ない
	b.do(t, chromedp.Click(`[data-analytics-consent="denied"]`), chromedp.Navigate(srv.URL+"/"))
	b.waitFor(t, `document.readyState === "complete"`)
	if evaluate[bool](t, b, bannerVisible) || evaluate[int](t, b, gtagTags) != 0 {
		t.Fatal("a refusal must stay")
	}
	// プライバシーポリシーから同意すると読み込み、フォーカスは案内へ移って、閉じたらボタンへ戻る
	b.do(t, chromedp.Navigate(srv.URL+"/privacy"))
	b.waitFor(t, `!document.querySelector("[data-analytics-settings]").hidden`)
	b.do(t, chromedp.Click(`[data-analytics-settings]`))
	b.waitFor(t, `document.activeElement.id === "analytics-consent"`)
	b.do(t, chromedp.Click(`[data-analytics-consent="granted"]`))
	b.waitFor(t, `document.activeElement.hasAttribute("data-analytics-settings")`)
	if n := evaluate[int](t, b, gtagTags); n != 1 {
		t.Fatalf("after consent: %d", n)
	}
	// 再訪では案内なしで読み込む
	b.do(t, chromedp.Navigate(srv.URL+"/"))
	b.waitFor(t, `document.readyState === "complete"`)
	if n := evaluate[int](t, b, gtagTags); n != 1 || evaluate[bool](t, b, bannerVisible) {
		t.Fatalf("revisit: %d", n)
	}
	// 他のタブの取り消しで止まり、Cookieも消える
	evaluate[string](t, b, `document.cookie = "_ga=GA1.1.1; path=/"; document.cookie = "_ga_D=GS1.1; path=/; domain=" + location.hostname; localStorage.setItem("mwk-analytics-consent", "denied"); window.dispatchEvent(new StorageEvent("storage", {key: "mwk-analytics-consent", newValue: "denied"})); ""`)
	b.waitFor(t, `document.readyState === "complete" && !document.cookie.includes("_ga") && document.querySelectorAll('script[src*="googletagmanager"]').length === 0`)
	// 取り消しの画面の操作でもCookieが消える
	evaluate[string](t, b, `localStorage.setItem("mwk-analytics-consent", "granted"); ""`)
	b.do(t, chromedp.Navigate(srv.URL+"/privacy"))
	b.waitFor(t, `!document.querySelector("[data-analytics-settings]").hidden`)
	evaluate[string](t, b, `document.cookie = "_ga_X=GS1.1; path=/"; ""`)
	b.do(t, chromedp.Click(`[data-analytics-settings]`), chromedp.Click(`[data-analytics-consent="denied"]`))
	b.waitFor(t, `localStorage.getItem("mwk-analytics-consent") === "denied" && !document.cookie.includes("_ga") && document.querySelectorAll('script[src*="googletagmanager"]').length === 0`)
	// 古いイベントが遅れて届いても、今の値が拒否なら読み込まない
	evaluate[string](t, b, `window.dispatchEvent(new StorageEvent("storage", {key: "mwk-analytics-consent", newValue: "granted"})); ""`)
	if n := evaluate[int](t, b, gtagTags); n != 0 {
		t.Fatalf("a stale event loaded the script: %d", n)
	}
	// 同意済みで値を消されたら止める
	evaluate[string](t, b, `localStorage.setItem("mwk-analytics-consent", "granted"); ""`)
	b.do(t, chromedp.Navigate(srv.URL+"/"))
	b.waitFor(t, gtagTags+` === 1`)
	evaluate[string](t, b, `document.cookie = "_ga=GA1.1.1; path=/"; localStorage.clear(); window.dispatchEvent(new StorageEvent("storage", {key: null})); ""`)
	b.waitFor(t, `document.readyState === "complete" && !document.cookie.includes("_ga") && `+gtagTags+` === 0`)
	b.noErrors(t)
}

// 同意の後に保存領域へ書けなくなっても、取り消しでこのページの計測を止める。
// 不変条件: ANALYTICS-INV-5
func TestAnalytics_WithdrawWhenStorageFails(t *testing.T) {
	srv := analyticsServer(t)
	b := newBrowser(t)
	b.do(t, blockURL("https://www.googletagmanager.com/*"), chromedp.Navigate(srv.URL+"/privacy"))
	b.waitFor(t, `!document.getElementById("analytics-consent").hidden`)
	b.do(t, chromedp.Click(`[data-analytics-consent="granted"]`))
	b.waitFor(t, gtagTags+` === 1`)
	evaluate[string](t, b, `document.cookie = "_ga=GA1.1.1; path=/"; Storage.prototype.setItem = function () { throw new Error("full"); }; ""`)
	b.do(t, chromedp.Click(`[data-analytics-settings]`), chromedp.Click(`[data-analytics-consent="denied"]`))
	if !evaluate[bool](t, b, `window["ga-disable-G-TEST12345"] === true && !document.cookie.includes("_ga")`) {
		t.Fatal("tracking must stop even when the choice cannot be saved")
	}
	if !evaluate[bool](t, b, `document.getElementById("analytics-consent-status").textContent.includes("保存できませんでした")`) {
		t.Fatal("the failure must be told")
	}
}

// 不変条件: ANALYTICS-INV-2
func TestAnalytics_NoStorageNoBanner(t *testing.T) {
	srv := analyticsServer(t)
	b := newBrowser(t)
	// 保存領域が使えない環境を作る。回答を残せないので、案内を出さず、計測もしない
	block := chromedp.Func(func(ctx context.Context, target *chromedp.Target) error {
		_, err := cdp.Call(ctx, target, page.AddScriptToEvaluateOnNewDocument, page.AddScriptToEvaluateOnNewDocumentParams{
			Source: `Object.defineProperty(window, "localStorage", {get() { throw new Error("blocked"); }});`,
		})
		return err
	})
	b.do(t, block, chromedp.Navigate(srv.URL+"/"))
	b.waitFor(t, `document.readyState === "complete"`)
	if evaluate[bool](t, b, `!document.getElementById("analytics-consent").hidden`) || evaluate[int](t, b, gtagTags) != 0 {
		t.Fatal("no storage, no banner and no tracking")
	}
}

// JSが動かなければ、案内も設定のボタンも出ない。
func TestAnalytics_NoScriptNoBanner(t *testing.T) {
	srv := analyticsServer(t)
	b := newBrowser(t)
	b.do(t, chromedp.Navigate(srv.URL+"/privacy"))
	b.waitFor(t, `document.readyState === "complete"`)
	b.do(t, disableScripts(), chromedp.Navigate(srv.URL+"/privacy"))
	if !evaluate[bool](t, b, `document.getElementById("analytics-consent").hidden && document.querySelector("[data-analytics-settings]").hidden`) {
		t.Fatal("both must stay hidden without scripts")
	}
}
