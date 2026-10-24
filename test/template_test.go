package test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestTemplate_NoInlineStylesOrScripts はHTMLテンプレートにインラインstyle属性やタグがないことを検証します。
func TestTemplate_NoInlineStylesOrScripts(t *testing.T) {
	files, err := filepath.Glob("../internal/web/views/*.html")
	if err != nil {
		t.Fatalf("failed to find templates: %v", err)
	}
	if len(files) == 0 {
		t.Fatal("no template files found")
	}

	for _, file := range files {
		content, err := os.ReadFile(file)
		if err != nil {
			t.Fatalf("failed to read %s: %v", file, err)
		}
		str := string(content)

		if strings.Contains(str, "style=") {
			t.Errorf("Inline style attribute found in %s. Styles must be defined in style.css.", file)
		}
		if strings.Contains(str, "<style") {
			t.Errorf("<style> tag found in %s. Styles must be defined in style.css.", file)
		}
		if strings.Contains(str, "<script") {
			t.Errorf("<script> tag found in %s. Inline scripts are prohibited for security and simplicity.", file)
		}
	}
}

// TestCSS_SingleFileAndNoImportant はCSSファイルが1本のみであり、!importantが使われていないことを検証します。
func TestCSS_SingleFileAndNoImportant(t *testing.T) {
	files, err := filepath.Glob("../internal/web/static/css/*.css")
	if err != nil {
		t.Fatalf("failed to find css files: %v", err)
	}

	if len(files) != 1 {
		t.Errorf("Expected exactly 1 CSS file, but found %d: %v", len(files), files)
	}

	content, err := os.ReadFile(files[0])
	if err != nil {
		t.Fatalf("failed to read css file: %v", err)
	}

	if strings.Contains(string(content), "!important") {
		t.Errorf("!important found in %s. Avoid !important to keep CSS maintainable.", files[0])
	}
}
