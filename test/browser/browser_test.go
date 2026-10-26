// Package browser は、画面部品をChrome・Chromiumで実際に操作して確かめるテストです。
// キット本体の go.mod に依存を足さないよう、独立したモジュールにしています。make test-ui で実行します。
// Chromeが見つからない場合（CHROME_PATH、または PATH の chromium・google-chrome）はテストを飛ばします。
package browser

import (
	"context"
	"net/http"
	"os"
	"os/exec"
	"sync"
	"testing"
	"time"

	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/emulation"
	"github.com/chromedp/cdproto/input"
	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/chromedp"
)

// browser は、テスト1つ分のChromeと、そこで出たコンソールのエラー・CSP違反の記録です。
type browser struct {
	ctx    context.Context
	mu     sync.Mutex
	errors []string
}

func chromePath(t *testing.T) string {
	t.Helper()
	if path := os.Getenv("CHROME_PATH"); path != "" {
		return path
	}
	for _, name := range []string{"chromium", "chromium-browser", "google-chrome", "google-chrome-stable"} {
		if path, err := exec.LookPath(name); err == nil {
			return path
		}
	}
	t.Skip("Chrome not found; set CHROME_PATH or install chromium")
	return ""
}

func newBrowser(t *testing.T) *browser {
	t.Helper()
	opts := append(chromedp.DefaultExecAllocatorOptions[:], chromedp.ExecPath(chromePath(t)), chromedp.UserDataDir(t.TempDir()))
	allocCtx, cancelAlloc := chromedp.NewExecAllocator(context.Background(), opts...)
	ctx, cancel := chromedp.NewContext(allocCtx)
	t.Cleanup(func() {
		cancel()
		cancelAlloc()
	})
	b := &browser{ctx: ctx}
	messages := chromedp.Console(ctx)
	go func() {
		for m, err := range messages {
			if err != nil {
				return
			}
			if m.Type == chromedp.ConsoleError || m.IsException() {
				b.mu.Lock()
				b.errors = append(b.errors, m.String())
				b.mu.Unlock()
			}
		}
	}()
	return b
}

// noErrors は、JSのエラーとCSP違反（ブラウザのログのエラー）が出ていないことを確かめます。
func (b *browser) noErrors(t *testing.T) {
	t.Helper()
	time.Sleep(100 * time.Millisecond)
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.errors) > 0 {
		t.Fatalf("console errors: %v", b.errors)
	}
}

func (b *browser) do(t *testing.T, steps ...chromedp.Action[chromedp.Void]) {
	t.Helper()
	ctx, cancel := context.WithTimeout(b.ctx, 20*time.Second)
	defer cancel()
	if err := chromedp.Do(ctx, steps...); err != nil {
		t.Fatal(err)
	}
}

func evaluate[T any](t *testing.T, b *browser, expr string) T {
	t.Helper()
	ctx, cancel := context.WithTimeout(b.ctx, 10*time.Second)
	defer cancel()
	v, err := chromedp.Run(ctx, chromedp.Evaluate[T](expr))
	if err != nil {
		t.Fatalf("evaluate %s: %v", expr, err)
	}
	return v
}

// waitFor は、式が真になるまで待ちます。chromedp.Poll はページ内で new Function を使い、
// キットのCSP（unsafe-eval を許さない）で止められるため、DevTools側からの評価を繰り返します。
func (b *browser) waitFor(t *testing.T, expr string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if evaluate[bool](t, b, "!!("+expr+")") {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", expr)
}

// specialKeys は、実際のキーボードと同じく keyDown と keyUp の2つで送るキーです。
// chromedp.KeyEvent はEnterを keyDown と char に分けて送り、char がフォームを送信してしまうため使いません
// （実際のブラウザでは、keydown で既定の動作を止めれば送信されません）。
var specialKeys = map[string]struct {
	key, code, text string
	vk              int64
}{
	"Enter": {"Enter", "Enter", "\r", 13}, "ArrowDown": {"ArrowDown", "ArrowDown", "", 40}, "ArrowUp": {"ArrowUp", "ArrowUp", "", 38},
	"Escape": {"Escape", "Escape", "", 27}, "Backspace": {"Backspace", "Backspace", "", 8}, "Tab": {"Tab", "Tab", "", 9},
}

// press は、名前で指定したキーを順に押します（例: "ArrowDown", "Enter"）。
func (b *browser) press(t *testing.T, names ...string) {
	t.Helper()
	for _, name := range names {
		k, ok := specialKeys[name]
		if !ok {
			t.Fatalf("unknown key %s", name)
		}
		b.do(t, chromedp.Func(func(ctx context.Context, target *chromedp.Target) error {
			for _, typ := range []input.DispatchKeyEventType{input.DispatchKeyEventTypeKeyDown, input.DispatchKeyEventTypeKeyUp} {
				p := input.DispatchKeyEventParams{Type: typ, Key: k.key, Code: k.code, WindowsVirtualKeyCode: k.vk, NativeVirtualKeyCode: k.vk}
				if typ == input.DispatchKeyEventTypeKeyDown {
					p.Text = k.text
				}
				if _, err := cdp.Call(ctx, target, input.DispatchKeyEvent, p); err != nil {
					return err
				}
			}
			return nil
		}))
	}
}

// typeText は、日本語入力で確定した文字と同じく、フォーカスのある入力欄に文字を挿入します。
func (b *browser) typeText(t *testing.T, text string) {
	t.Helper()
	b.do(t, chromedp.Func(func(ctx context.Context, target *chromedp.Target) error {
		_, err := cdp.Call(ctx, target, input.InsertText, input.InsertTextParams{Text: text})
		return err
	}))
}

// composingEnter は、日本語入力の変換を確定するEnter（keyCode 229）を送ります。
func (b *browser) composingEnter(t *testing.T) {
	t.Helper()
	b.do(t, chromedp.Func(func(ctx context.Context, target *chromedp.Target) error {
		for _, typ := range []input.DispatchKeyEventType{input.DispatchKeyEventTypeRawKeyDown, input.DispatchKeyEventTypeKeyUp} {
			if _, err := cdp.Call(ctx, target, input.DispatchKeyEvent, input.DispatchKeyEventParams{Type: typ, Key: "Enter", Code: "Enter", WindowsVirtualKeyCode: 229}); err != nil {
				return err
			}
		}
		return nil
	}))
}

func disableScripts() chromedp.Action[chromedp.Void] {
	return chromedp.Func(func(ctx context.Context, target *chromedp.Target) error {
		_, err := cdp.Call(ctx, target, emulation.SetScriptExecutionDisabled, emulation.SetScriptExecutionDisabledParams{Value: true})
		return err
	})
}

func blockURL(pattern string) chromedp.Action[chromedp.Void] {
	return chromedp.Func(func(ctx context.Context, target *chromedp.Target) error {
		if _, err := cdp.Call(ctx, target, network.Enable, network.EnableParams{}); err != nil {
			return err
		}
		_, err := cdp.Call(ctx, target, network.SetBlockedURLs, network.SetBlockedURLsParams{URLPatterns: []*network.BlockPattern{{URLPattern: pattern, Block: true}}})
		return err
	})
}

func setCookie(serverURL string, c *http.Cookie) chromedp.Action[chromedp.Void] {
	return chromedp.Func(func(ctx context.Context, target *chromedp.Target) error {
		if _, err := cdp.Call(ctx, target, network.Enable, network.EnableParams{}); err != nil {
			return err
		}
		_, err := cdp.Call(ctx, target, network.SetCookie, network.SetCookieParams{Name: c.Name, Value: c.Value, URL: serverURL, SameSite: network.CookieSameSiteStrict})
		return err
	})
}

// 入力欄・候補・選択済みの状態を読む式。prefix は部品のidの接頭辞。
func expanded(prefix string) string {
	return `document.getElementById("` + prefix + `-input").getAttribute("aria-expanded") === "true"`
}
func activeID(prefix string) string {
	return `document.getElementById("` + prefix + `-input").getAttribute("aria-activedescendant") || ""`
}
func chips(prefix string) string {
	return `Array.from(document.getElementById("` + prefix + `-input").closest("[data-combobox]").querySelectorAll(".combobox-chip [data-chip-label]")).map(e => e.textContent).join(",")`
}
func visibleOptions(prefix string) string {
	return `Array.from(document.querySelectorAll("#` + prefix + `-list [role=option]")).filter(o => !o.hidden).map(o => o.dataset.label).join(",")`
}

const focused = `document.activeElement ? document.activeElement.id : ""`
