// Copyright (c) 2026 the go-quake1/engine authors.
// SPDX-License-Identifier: GPL-2.0-or-later

//go:build !js

package main

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"
)

func cli(t *testing.T, args ...string) (string, error) {
	t.Helper()
	cmd := RootCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs(args)
	err := cmd.Execute()
	return out.String(), err
}

func TestRunText(t *testing.T) {
	out, err := cli(t, "run", "--frames", "8", "--warmup", "1", "--procs", "1")
	if err != nil || !strings.Contains(out, "pak=procedural frames=8") {
		t.Fatalf("out=%q err=%v", out, err)
	}
}

func TestRunJSON(t *testing.T) {
	out, err := cli(t, "run", "--frames", "8", "--warmup", "1", "--json")
	if err != nil || !strings.Contains(out, `"allocs_per_frame"`) {
		t.Fatalf("out=%q err=%v", out, err)
	}
}

func TestRunErrors(t *testing.T) {
	if _, err := cli(t, "run", "--frames", "0"); err == nil {
		t.Error("zero frames must fail")
	}
	if _, err := cli(t, "run", "--pak", "/nonexistent/pak0.pak"); err == nil {
		t.Error("a missing pak must fail")
	}
	if _, err := cli(t, "run", "extra"); err == nil {
		t.Error("positional arguments must fail")
	}
}

func TestMainExits(t *testing.T) {
	var code int
	old := osExit
	defer func() { osExit = old }()
	osExit = func(c int) { code = c }
	oldArgs := os.Args
	defer func() { os.Args = oldArgs }()
	os.Args = []string{"quake-bench", "run", "--frames", "0"}
	main()
	if code != 1 {
		t.Errorf("exit code = %d, want 1", code)
	}
	code = 0
	os.Args = []string{"quake-bench", "run", "--frames", "3", "--warmup", "0"}
	main()
	if code != 0 {
		t.Errorf("exit code = %d, want 0", code)
	}
}

const resultLine = `{"pak":"procedural","go":"go1.27.3","summary":{"frames":5}}` + "\n"

func fakes(t *testing.T, load string) {
	t.Helper()
	oe, os_, osl, oself := execOutput, startProcess, sleep, self
	t.Cleanup(func() { execOutput, startProcess, sleep, self = oe, os_, osl, oself })
	sleep = func(time.Duration) {}
	self = func() (string, error) { return "/bin/quake-bench", nil }
	execOutput = func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name == "uptime" {
			return []byte("load averages: " + load + " 1.0 1.0"), nil
		}
		return []byte("QUAKE: log\n" + resultLine), nil
	}
	startProcess = func(ctx context.Context, name string, args ...string) (func(), error) {
		url := args[len(args)-1]
		go func() {
			req, _ := http.NewRequestWithContext(ctx, "POST", url+"result", strings.NewReader(resultLine))
			req.Header.Set("X-UA", "FakeChrome/9")
			if r, err := http.DefaultClient.Do(req); err == nil {
				r.Body.Close()
			}
		}()
		return func() {}, nil
	}
}

func TestBaselineNativeOnly(t *testing.T) {
	fakes(t, "2.0")
	out, err := cli(t, "baseline", "--rounds", "2")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"round 2 native: load1 before 2.00 after 2.00", "pak: procedural", "| metric | native |"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
}

func TestBaselineWithBrowser(t *testing.T) {
	fakes(t, "2.0")
	out, err := cli(t, "baseline", "--rounds", "1", "--wasm", "w", "--wasm-exec", "e", "--chromium", "c", "--profile", "p")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "browser: FakeChrome/9") || !strings.Contains(out, "js/wasm (Chromium)") {
		t.Errorf("output:\n%s", out)
	}
}

func TestBaselineWithPak(t *testing.T) {
	fakes(t, "2.0")
	var got []string
	execOutput = func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name == "uptime" {
			return []byte("load averages: 2.0 1 1"), nil
		}
		got = args
		return []byte(resultLine), nil
	}
	if _, err := cli(t, "baseline", "--rounds", "1", "--pak", "/x/pak0.pak"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(got, " "), "--pak /x/pak0.pak") || !strings.Contains(strings.Join(got, " "), "--procs 1") {
		t.Errorf("native args = %v", got)
	}
}

func TestBaselineErrors(t *testing.T) {
	fakes(t, "2.0")
	if _, err := cli(t, "baseline", "--wasm", "w"); err == nil {
		t.Error("--wasm without --chromium must fail")
	}
	if _, err := cli(t, "baseline", "--rounds", "0"); err == nil {
		t.Error("zero rounds must fail")
	}
	self = func() (string, error) { return "", errors.New("self") }
	if _, err := cli(t, "baseline"); err == nil {
		t.Error("an unknown executable must fail")
	}
	self = func() (string, error) { return "/bin/quake-bench", nil }

	fakes(t, "9.5")
	out, err := cli(t, "baseline", "--rounds", "1", "--max-redo", "1")
	if err == nil || !strings.Contains(out, "discarded native: load1 9.50 before") {
		t.Errorf("a loaded machine must stop with the discards listed: out=%q err=%v", out, err)
	}

	fakes(t, "2.0")
	execOutput = func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name == "uptime" {
			return nil, errors.New("no uptime")
		}
		return nil, nil
	}
	if _, err := cli(t, "baseline", "--rounds", "1"); err == nil {
		t.Error("an unreadable load must fail")
	}
	fakes(t, "2.0")
	execOutput = func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name == "uptime" {
			return []byte("load averages: 2.0 1 1"), nil
		}
		return nil, errors.New("run failed")
	}
	if _, err := cli(t, "baseline", "--rounds", "1"); err == nil {
		t.Error("a failing native run must fail")
	}
}

func TestUptimeLoadError(t *testing.T) {
	fakes(t, "2.0")
	execOutput = func(context.Context, string, ...string) ([]byte, error) { return []byte("garbage"), nil }
	if _, err := uptimeLoad(context.Background()); err == nil {
		t.Error("unparsable uptime must fail")
	}
}

func TestRealExecAndProcess(t *testing.T) {
	out, err := execOutput(context.Background(), os.Args[0], "-test.run=^$")
	if err != nil {
		t.Fatalf("execOutput = %q, %v", out, err)
	}
	if _, err := execOutput(context.Background(), "/nonexistent/binary"); err == nil {
		t.Error("a missing binary must fail")
	}
	stop, err := startProcess(context.Background(), os.Args[0], "-test.run=^$")
	if err != nil {
		t.Fatal(err)
	}
	stop()
	if _, err := startProcess(context.Background(), "/nonexistent/binary"); err == nil {
		t.Error("a missing binary must fail to start")
	}
}
