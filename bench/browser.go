// Copyright (c) 2026 the go-quake1/engine authors.
// SPDX-License-Identifier: GPL-2.0-or-later

//go:build !js

package bench

import (
	"context"
	_ "embed"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
)

//go:embed page.html
var pageHTML string

// maxResultBytes bounds what the page may POST.
const maxResultBytes = 1 << 20

// Args returns the command line a `quake-bench run` takes for cfg.
func (c Config) Args() []string {
	a := []string{
		"run", "--json",
		"--frames", strconv.Itoa(c.Frames),
		"--warmup", strconv.Itoa(c.Warmup),
		"--width", strconv.Itoa(c.Width),
		"--height", strconv.Itoa(c.Height),
		"--renderer", c.Renderer,
		"--map", c.Map,
	}
	if c.Present != PresentNone {
		a = append(a, "--present", c.Present)
	}
	if c.RequireGPU {
		a = append(a, "--require-gpu")
	}
	return a
}

// Launch starts a browser on url and returns a function that stops it.
type Launch func(ctx context.Context, url string) (stop func(), err error)

// posted is what the page sends back.
type posted struct{ body, ua string }

// newHandler serves the page (with argv for the wasm program), the
// js/wasm build at wasmPath and wasm_exec.js at execPath, and takes the
// result the page POSTs to /result into got (a second POST is dropped).
// Every response carries COOP/COEP headers so the browser's timer
// resolution is the finest it offers.
func newHandler(argv []string, wasmPath, execPath string, got chan<- posted) http.Handler {
	js, _ := json.Marshal(argv)
	page := strings.Replace(pageHTML, "__ARGV__", string(js), 1)
	mux := http.NewServeMux()
	mux.HandleFunc("/{$}", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = io.WriteString(w, page)
	})
	mux.HandleFunc("/bench.wasm", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/wasm")
		http.ServeFile(w, r, wasmPath)
	})
	mux.HandleFunc("/wasm_exec.js", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/javascript")
		http.ServeFile(w, r, execPath)
	})
	mux.HandleFunc("POST /result", func(w http.ResponseWriter, r *http.Request) {
		b, err := io.ReadAll(io.LimitReader(r.Body, maxResultBytes))
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		select {
		case got <- posted{string(b), r.Header.Get("X-UA")}:
		default:
		}
	})
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cross-Origin-Opener-Policy", "same-origin")
		w.Header().Set("Cross-Origin-Embedder-Policy", "require-corp")
		mux.ServeHTTP(w, r)
	})
}

// listen is a seam for the tests.
var listen = func() (net.Listener, error) { return net.Listen("tcp", "127.0.0.1:0") }

// RunInBrowser serves the page and the js/wasm build on a loopback port,
// starts the browser through launch, and returns the Result the page
// posts back, with the browser's user agent in Browser.
func RunInBrowser(ctx context.Context, wasmPath, execPath string, cfg Config, launch Launch) (Result, error) {
	out, ua, err := RunProgramInBrowser(ctx, wasmPath, execPath, append([]string{"quake-bench"}, cfg.Args()...), launch)
	if err != nil {
		return Result{}, err
	}
	res, err := ParseResult(out)
	res.Browser = ua
	return res, err
}

// RunProgramInBrowser serves the page and the js/wasm program at wasmPath
// on a loopback port, runs it with argv in the browser started through
// launch, and returns what it printed on standard output and the
// browser's user agent. The page has a canvas#quake for the program.
func RunProgramInBrowser(ctx context.Context, wasmPath, execPath string, argv []string, launch Launch) (stdout, userAgent string, err error) {
	got := make(chan posted, 1)
	ln, err := listen()
	if err != nil {
		return "", "", err
	}
	srv := &http.Server{Handler: newHandler(argv, wasmPath, execPath, got)}
	go func() { _ = srv.Serve(ln) }()
	defer srv.Close()

	stop, err := launch(ctx, "http://"+ln.Addr().String()+"/")
	if err != nil {
		return "", "", err
	}
	defer stop()
	select {
	case p := <-got:
		return p.body, p.ua, nil
	case <-ctx.Done():
		return "", "", ctx.Err()
	}
}
