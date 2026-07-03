// Copyright (c) 2026 the go-quake1/engine authors.
// SPDX-License-Identifier: BSD-3-Clause

package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// serve binds an HTTP server to addr serving root statically. When
// oneShot is true the server shuts itself down 50 ms after the first
// non-favicon request returns, so headless smoke tests can drive the
// "serves once + exits" loop without manual cancellation. Without
// oneShot the call blocks until the process receives a SIGINT
// (Ctrl-C) -- net/http's ListenAndServe returns http.ErrServerClosed
// on graceful shutdown, which we map to nil.
//
// out captures the human-readable startup banner ("open
// http://...") so tests can intercept it without scraping stdout.
func serve(addr, root string, oneShot bool, out io.Writer) error {
	abs, err := filepathAbs(root)
	if err != nil {
		return fmt.Errorf("filepath.Abs(%q): %w", root, err)
	}
	if _, err := osStat(abs); err != nil {
		return fmt.Errorf("root %q: %w", abs, err)
	}

	ln, err := netListen("tcp", addr)
	if err != nil {
		return fmt.Errorf("listen %q: %w", addr, err)
	}

	mux := http.NewServeMux()
	mux.Handle("/", wasmContentType(http.FileServer(http.Dir(abs))))

	srv := &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}

	if oneShot {
		var seen int32
		wrap := mux
		srv.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			wrap.ServeHTTP(w, r)
			// favicon spam shouldn't count; only the real first hit
			// triggers shutdown.
			if strings.HasSuffix(r.URL.Path, "/favicon.ico") {
				return
			}
			if atomic.CompareAndSwapInt32(&seen, 0, 1) {
				go func() {
					time.Sleep(oneShotDrainDelay)
					ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
					defer cancel()
					_ = srv.Shutdown(ctx)
				}()
			}
		})
	}

	fmt.Fprintf(out, "serve-wasm: serving %s\nserve-wasm: open http://%s/\n", abs, ln.Addr())

	err = srv.Serve(ln)
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

// oneShotDrainDelay is the wall-clock gap between "we handled the
// first request" and "begin graceful shutdown". Long enough for the
// response body bytes to flush down the TCP socket on macOS (which
// otherwise occasionally returns ECONNRESET to the curl client).
var oneShotDrainDelay = 50 * time.Millisecond

// wasmContentType wraps an http.Handler so .wasm responses carry
// application/wasm (required by Chromium-family browsers'
// WebAssembly.instantiateStreaming, otherwise they bail out with a
// MIME-type mismatch).
func wasmContentType(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, ".wasm") {
			w.Header().Set("Content-Type", "application/wasm")
		}
		next.ServeHTTP(w, r)
	})
}

// osStat / netListen are seams so tests can drive the error branches
// without invoking the host filesystem / network on the unhappy path.
var (
	osStat      = os.Stat
	filepathAbs = filepath.Abs
	netListen   = func(network, addr string) (net.Listener, error) { return net.Listen(network, addr) }
)

// muStartup serializes seam swaps in tests (set + restore from the
// same goroutine; concurrent test runs would otherwise race).
var muStartup sync.Mutex
