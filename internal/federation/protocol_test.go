package federation

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestEpochs_Valid(t *testing.T) {
	good := Epochs{"1", "22", "9223372036854775807"}
	if !good.Valid() {
		t.Fatal("valid epochs rejected")
	}
	for _, s := range []string{"", "0", "-1", "+1", "01", "1.5", "9223372036854775808", " 1"} {
		if (Epochs{s, "1", "1"}).Valid() || (Epochs{"1", s, "1"}).Valid() || (Epochs{"1", "1", s}).Valid() {
			t.Errorf("%q accepted", s)
		}
	}
}

func TestDecode_RejectsAmbiguousJSON(t *testing.T) {
	type target struct {
		A string `json:"a"`
		B struct {
			C string `json:"c"`
		} `json:"b"`
	}
	var ok target
	if err := Decode([]byte(`{"a":"1","b":{"c":"2"}}`), &ok); err != nil || ok.B.C != "2" {
		t.Fatalf("valid JSON: %v", err)
	}
	for _, body := range []string{
		`{"a":"1","a":"2"}`, `{"b":{"c":"1","c":"2"}}`, `{"a":"1"}{}`, `{"x":1}`, `{"a":1}`, `{`,
		strings.Repeat("[", 20) + strings.Repeat("]", 20),
	} {
		var v target
		if Decode([]byte(body), &v) == nil {
			t.Errorf("%q accepted", body)
		}
	}
	var v target
	if Decode([]byte(`{"a":"`+strings.Repeat("x", 70000)+`"}`), &v) == nil {
		t.Error("oversized JSON accepted")
	}
}

func TestClient_PinsURLsAndLimitsBody(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/ok":
			_, _ = io.WriteString(w, "ok")
		case "/big":
			_, _ = io.WriteString(w, strings.Repeat("x", 100))
		case "/redirect":
			http.Redirect(w, r, "/ok", http.StatusFound)
		}
	}))
	defer srv.Close()
	allowed := []string{srv.URL + "/ok", srv.URL + "/big", srv.URL + "/redirect"}
	client := Client(allowed, time.Second, 10, srv.Client().Transport)

	res, err := client.Get(srv.URL + "/ok")
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(res.Body)
	res.Body.Close()
	if err != nil || string(body) != "ok" {
		t.Fatalf("%q %v", body, err)
	}
	if _, err := client.Get(srv.URL + "/other"); err == nil {
		t.Error("unlisted URL was requested")
	}
	if _, err := client.Get(srv.URL + "/ok?x=1"); err == nil {
		t.Error("changed query was requested")
	}
	if _, err := client.Get(srv.URL + "/redirect"); err == nil {
		t.Error("redirect was followed")
	}
	res, err = client.Get(srv.URL + "/big")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if _, err := io.ReadAll(res.Body); err == nil {
		t.Error("oversized response was read")
	}
}

func TestLimiter_Burst(t *testing.T) {
	var l Limiter
	for i := range 16 {
		if !l.Allow() {
			t.Fatalf("request %d rejected", i)
		}
	}
	if l.Allow() {
		t.Fatal("burst limit not applied")
	}
}
