// Copyright (c) 2026 the go-quake1/engine authors.
// SPDX-License-Identifier: GPL-2.0-or-later

//go:build !js

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/go-quake1/engine/bench"
)

func TestStubMainRefusesToRunNatively(t *testing.T) {
	var code int
	old := osExit
	defer func() { osExit = old }()
	osExit = func(c int) { code = c }
	main()
	if code != 2 {
		t.Errorf("exit code = %d, want 2", code)
	}
}

func tail(s string, n int) string {
	if len(s) > n {
		return s[len(s)-n:]
	}
	return s
}

type verdict struct {
	Check    string `json:"check"`
	Detail   string `json:"detail"`
	Adapter  string `json:"adapter"`
	Fallback bool   `json:"fallback"`
	Frames   int    `json:"frames"`
	Pixels   int    `json:"pixels"`
}

// runInBrowser builds the program for js/wasm and runs it in Chromium. It
// is skipped unless QUAKE_CHROMIUM names a Chromium binary (the full
// browser: the headless shell offers no GPU) and QUAKE_PROFILE a durable
// user-data directory. QUAKE_CHROMIUM_ARGS adds browser arguments (the Linux
// SwiftShader lane needs --enable-unsafe-webgpu --enable-features=Vulkan
// --use-vulkan=swiftshader --use-angle=swiftshader). QUAKE_REQUIRE_GPU=1
// makes a fallback adapter an error: the macOS lane.
func runInBrowser(t *testing.T, args ...string) verdict {
	t.Helper()
	chrome, profile := os.Getenv("QUAKE_CHROMIUM"), os.Getenv("QUAKE_PROFILE")
	if chrome == "" || profile == "" {
		t.Skip("QUAKE_CHROMIUM and QUAKE_PROFILE not set: no browser to run in")
	}
	wasm := filepath.Join(t.TempDir(), "check.wasm")
	build := exec.Command("go", "build", "-o", wasm, ".")
	build.Env = append(os.Environ(), "GOOS=js", "GOARCH=wasm")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}
	goroot, err := exec.Command("go", "env", "GOROOT").Output()
	if err != nil {
		t.Fatal(err)
	}
	execJS := filepath.Join(strings.TrimSpace(string(goroot)), "lib", "wasm", "wasm_exec.js")
	var browserLog bytes.Buffer
	extra := strings.Fields(os.Getenv("QUAKE_CHROMIUM_ARGS"))
	launch := func(ctx context.Context, url string) (func(), error) {
		a := append([]string{"--headless", "--no-sandbox", "--enable-unsafe-webgpu"}, extra...)
		a = append(a, "--no-first-run", "--user-data-dir="+profile, url)
		a = append([]string{"--enable-logging=stderr"}, a...)
		c := exec.CommandContext(ctx, chrome, a...)
		c.Stderr = &browserLog
		if err := c.Start(); err != nil {
			return nil, err
		}
		return func() { _ = c.Process.Kill(); _ = c.Wait() }, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	argv := append([]string{"quake-presentcheck"}, args...)
	if os.Getenv("QUAKE_REQUIRE_GPU") == "1" {
		argv = append(argv, "--require-gpu")
	}
	out, ua, err := bench.RunProgramInBrowser(ctx, wasm, execJS, argv, launch)
	if err != nil {
		t.Logf("browser log (tail):\n%s", tail(browserLog.String(), 4000))
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	var v verdict
	if err := json.Unmarshal([]byte(lines[len(lines)-1]), &v); err != nil {
		t.Fatalf("no verdict in the output %q: %v", out, err)
	}
	t.Logf("%s on %s %s: adapter %q fallback %v: %s %s (%d frames, %d pixels)",
		ua, runtime.GOOS, runtime.GOARCH, v.Adapter, v.Fallback, v.Check, v.Detail, v.Frames, v.Pixels)
	return v
}

// TestPresenterMatchesTheCPUConversion: every pixel of every frame read
// back from the canvas equals the CPU's conversion, at scale 1 and 3.
func TestPresenterMatchesTheCPUConversion(t *testing.T) {
	v := runInBrowser(t)
	if v.Check != "PASS" || v.Frames != 6 {
		t.Fatalf("verdict %+v", v)
	}
	if os.Getenv("QUAKE_REQUIRE_GPU") == "1" && v.Fallback {
		t.Fatal("a fallback adapter where a GPU is required")
	}
}

// TestOneWrongPaletteIndexFails is the planted negative: the GPU is fed a
// frame with one index changed, the reference keeps the true one, and the
// check must fail. A comparison that cannot fail proves nothing.
func TestOneWrongPaletteIndexFails(t *testing.T) {
	v := runInBrowser(t, "--corrupt")
	if v.Check != "FAIL" || !strings.Contains(v.Detail, "pixel (7,5)") {
		t.Fatalf("a wrong palette index was not caught: %+v", v)
	}
}
