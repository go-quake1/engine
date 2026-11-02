// Copyright (c) 2026 the go-quake1/engine authors.
// SPDX-License-Identifier: GPL-2.0-or-later

//go:build !js

package bench

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type failingBody struct{}

func (failingBody) Read([]byte) (int, error) { return 0, errors.New("read") }

func TestHandler(t *testing.T) {
	dir := t.TempDir()
	wasm := filepath.Join(dir, "b.wasm")
	exec := filepath.Join(dir, "e.js")
	if err := os.WriteFile(wasm, []byte("\x00asm"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(exec, []byte("// js"), 0o600); err != nil {
		t.Fatal(err)
	}
	got := make(chan posted, 1)
	h := newHandler([]string{"quake-bench", "run"}, wasm, exec, got)
	do := func(method, path string, body io.Reader, hdr ...string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, body)
		for i := 0; i+1 < len(hdr); i += 2 {
			r.Header.Set(hdr[i], hdr[i+1])
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	page := do("GET", "/", nil)
	if !strings.Contains(page.Body.String(), `["quake-bench","run"]`) || strings.Contains(page.Body.String(), "__ARGV__") {
		t.Errorf("page does not carry the argv: %s", page.Body.String())
	}
	if page.Header().Get("Cross-Origin-Opener-Policy") != "same-origin" ||
		page.Header().Get("Cross-Origin-Embedder-Policy") != "require-corp" {
		t.Error("COOP/COEP headers missing")
	}
	if w := do("GET", "/bench.wasm", nil); w.Body.String() != "\x00asm" || w.Header().Get("Content-Type") != "application/wasm" {
		t.Errorf("wasm = %q %q", w.Body.String(), w.Header().Get("Content-Type"))
	}
	if w := do("GET", "/wasm_exec.js", nil); w.Body.String() != "// js" {
		t.Errorf("wasm_exec = %q", w.Body.String())
	}
	if w := do("GET", "/nope", nil); w.Code != http.StatusNotFound {
		t.Errorf("unknown path = %d", w.Code)
	}
	if w := do("POST", "/result", failingBody{}); w.Code != http.StatusBadRequest {
		t.Errorf("failing body = %d", w.Code)
	}
	do("POST", "/result", strings.NewReader("one"), "X-UA", "HeadlessChrome/1")
	do("POST", "/result", strings.NewReader("two")) // dropped
	if p := <-got; p.body != "one" || p.ua != "HeadlessChrome/1" {
		t.Errorf("posted = %+v", p)
	}
	if len(got) != 0 {
		t.Error("a second result must be dropped")
	}
}

func TestConfigArgs(t *testing.T) {
	a := strings.Join(DefaultConfig().Args(), " ")
	want := "run --json --frames 600 --warmup 60 --width 320 --height 200 --renderer soft --map start"
	if a != want {
		t.Errorf("Args = %q, want %q", a, want)
	}
}

// fakeBrowser stands in for Chromium: it fetches the page and posts body.
func fakeBrowser(body string) Launch {
	return func(ctx context.Context, url string) (func(), error) {
		go func() {
			resp, err := http.Get(url)
			if err == nil {
				resp.Body.Close()
			}
			req, _ := http.NewRequestWithContext(ctx, "POST", url+"result", strings.NewReader(body))
			req.Header.Set("X-UA", "FakeChrome/9")
			if r, err := http.DefaultClient.Do(req); err == nil {
				r.Body.Close()
			}
		}()
		return func() {}, nil
	}
}

func TestRunInBrowser(t *testing.T) {
	res, err := RunInBrowser(context.Background(), "w", "e", smallConfig(),
		fakeBrowser("log\n{\"pak\":\"procedural\"}\n"))
	if err != nil || res.Pak != "procedural" || res.Browser != "FakeChrome/9" {
		t.Fatalf("RunInBrowser = %+v, %v", res, err)
	}
	if _, err := RunInBrowser(context.Background(), "w", "e", smallConfig(), fakeBrowser("ERROR boom")); !errors.Is(err, ErrNoResult) {
		t.Errorf("a page error = %v, want ErrNoResult", err)
	}
}

func TestRunInBrowserFailures(t *testing.T) {
	boom := errors.New("boom")
	if _, err := RunInBrowser(context.Background(), "w", "e", smallConfig(),
		func(context.Context, string) (func(), error) { return nil, boom }); !errors.Is(err, boom) {
		t.Errorf("launch error = %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	_, err := RunInBrowser(ctx, "w", "e", smallConfig(), func(context.Context, string) (func(), error) {
		cancel()
		return func() {}, nil
	})
	if !errors.Is(err, context.Canceled) {
		t.Errorf("cancelled = %v", err)
	}
	old := listen
	defer func() { listen = old }()
	listen = func() (net.Listener, error) { return nil, boom }
	if _, err := RunInBrowser(context.Background(), "w", "e", smallConfig(), nil); !errors.Is(err, boom) {
		t.Errorf("listen error = %v", err)
	}
}
