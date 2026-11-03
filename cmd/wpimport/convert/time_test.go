package convert

import (
	"strings"
	"testing"
	"time"
)

// TestConvert_LinearTime は、意地悪な本文でも、変換の時間が長さに見合うことを確かめる。
func TestConvert_LinearTime(t *testing.T) {
	for name, in := range map[string]string{
		"nested lists":    strings.Repeat("<ul><li>a", 3000),
		"nested strong":   strings.Repeat("<strong>a", 5000),
		"many images":     "<figure>" + strings.Repeat(`<img class="wp-image-30" alt="">a`, 3000) + "</figure>",
		"many links":      strings.Repeat(`<a href="/x">a</a>`, 10000),
		"many captions":   strings.Repeat(`[caption]<img class="wp-image-30">c[/caption]`, 3000),
		"many galleries":  strings.Repeat(`[gallery ids="30,31"]`, 3000),
		"brackets":        strings.Repeat("[", 50000),
		"stars":           strings.Repeat("*", 50000),
		"table cells":     "<table>" + strings.Repeat("<tr>"+strings.Repeat("<td>a</td>", 30), 300) + "</table>",
		"classic newline": strings.Repeat("a\n\n", 20000),
		"comments":        strings.Repeat("<!-- wp:latest-posts /-->", 10000),
		"patterns":        strings.Repeat(`<!-- wp:block {"ref":9} /-->`, 5000),
	} {
		for _, target := range []Target{Page, News} {
			start := time.Now()
			Convert(in, 5, target, fake{})
			if d := time.Since(start); d > 2*time.Second {
				t.Errorf("%s (%d): %v", name, target, d)
			}
		}
	}
}
