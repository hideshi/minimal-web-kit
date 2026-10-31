package guard

import (
	"bytes"
	"errors"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type part struct{ name, file, value string }

func uploadRequest(t *testing.T, parts ...part) *http.Request {
	t.Helper()
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	for _, p := range parts {
		var w io.Writer
		var err error
		if p.file != "" {
			w, err = mw.CreateFormFile(p.name, p.file)
		} else {
			w, err = mw.CreateFormField(p.name)
		}
		if err != nil {
			t.Fatal(err)
		}
		if _, err := io.WriteString(w, p.value); err != nil {
			t.Fatal(err)
		}
	}
	if err := mw.Close(); err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("POST", "/admin/media", &body)
	r.Header.Set("Content-Type", mw.FormDataContentType())
	return r
}

// UploadToken は、先頭の部品の csrf_token だけを読み、ファイルは読まない。
func TestUploadToken(t *testing.T) {
	r := uploadRequest(t, part{name: "csrf_token", value: "tok"}, part{name: "file", file: "a.jpg", value: "body"})
	reader, token, err := UploadToken(httptest.NewRecorder(), r)
	if err != nil || token != "tok" {
		t.Fatalf("token=%q err=%v", token, err)
	}
	next, err := reader.NextPart()
	if err != nil || next.FormName() != "file" {
		t.Fatalf("next part: %v", err)
	}
	if raw, _ := io.ReadAll(next); string(raw) != "body" {
		t.Fatalf("file: %q", raw)
	}
}

// CSRFトークンが先頭に無い送信は、ファイルを読まずに断る。
func TestUploadToken_Rejects(t *testing.T) {
	for name, parts := range map[string][]part{
		"ファイルが先":    {{name: "file", file: "a.jpg", value: "x"}, {name: "csrf_token", value: "tok"}},
		"トークンなし":    {{name: "alt", value: "x"}},
		"トークンがファイル": {{name: "csrf_token", file: "a", value: "tok"}},
		"トークンが長すぎる": {{name: "csrf_token", value: strings.Repeat("x", maxTokenBytes+1)}},
		"部品なし":      {},
	} {
		if _, _, err := UploadToken(httptest.NewRecorder(), uploadRequest(t, parts...)); !errors.Is(err, ErrUploadForm) {
			t.Errorf("%s: %v", name, err)
		}
	}
	r := httptest.NewRequest("POST", "/admin/media", strings.NewReader("csrf_token=tok"))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if _, _, err := UploadToken(httptest.NewRecorder(), r); !errors.Is(err, ErrUploadForm) {
		t.Errorf("not multipart: %v", err)
	}
}
