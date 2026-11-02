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
	"sort"
	"strconv"
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

type viewStat struct {
	View     int     `json:"view"`
	Differ   int     `json:"differ"`
	Interior int     `json:"unexplained"`
	Drawn    int     `json:"gpu_non_clear"`
	Rate     float64 `json:"rate"`
}

type verdict struct {
	Check        string     `json:"check"`
	Detail       string     `json:"detail"`
	Adapter      string     `json:"adapter"`
	Fallback     bool       `json:"fallback"`
	Faces        int        `json:"faces_on_gpu"`
	Views        []viewStat `json:"views"`
	Pixels       int        `json:"pixels"`
	Differ       int        `json:"differ"`
	Interior     int        `json:"unexplained"`
	InteriorRate float64    `json:"interior_rate"`
	TotalRate    float64    `json:"total_rate"`
}

// runInBrowser builds the program for js/wasm and runs it in Chromium with
// the shareware pak served to the page. It is skipped unless QUAKE_PAK
// (pak0.pak, never in the repository), QUAKE_CHROMIUM (the full Chromium: the
// headless shell offers no GPU) and QUAKE_PROFILE (a durable user-data
// directory) are set. QUAKE_CHROMIUM_ARGS adds browser arguments (SwiftShader
// on Linux); QUAKE_REQUIRE_GPU=1 makes a fallback adapter an error.
func runInBrowser(t *testing.T, args ...string) verdict {
	t.Helper()
	raw := runInBrowserRaw(t, args...)
	lines := strings.Split(strings.TrimSpace(raw), "\n")
	var v verdict
	if err := json.Unmarshal([]byte(lines[len(lines)-1]), &v); err != nil {
		t.Fatalf("no verdict in %q: %v", raw, err)
	}
	t.Logf("%s %s", v.Check, v.Detail)
	t.Logf("%d faces on the GPU, %d pixels, %d differ (%.4f), %d unexplained (%.4f)", v.Faces, v.Pixels, v.Differ, v.TotalRate, v.Interior, v.InteriorRate)
	for _, s := range v.Views {
		t.Logf("view %2d: %5d differ (%.4f), %5d unexplained, %5d drawn", s.View, s.Differ, s.Rate, s.Interior, s.Drawn)
	}
	return v
}

// runInBrowserRaw returns the program's last output line.
func runInBrowserRaw(t *testing.T, args ...string) string {
	t.Helper()
	pakPath, chrome, profile := os.Getenv("QUAKE_PAK"), os.Getenv("QUAKE_CHROMIUM"), os.Getenv("QUAKE_PROFILE")
	if pakPath == "" || chrome == "" || profile == "" {
		t.Skip("QUAKE_PAK, QUAKE_CHROMIUM and QUAKE_PROFILE not all set: no pak or no browser")
	}
	if m, err := filepath.Glob(chrome); err == nil && len(m) > 0 {
		chrome = m[0]
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
		a := append([]string{"--enable-logging=stderr", "--headless", "--no-sandbox", "--enable-unsafe-webgpu"}, extra...)
		a = append(a, "--no-first-run", "--user-data-dir="+profile, url)
		c := exec.CommandContext(ctx, chrome, a...)
		c.Stderr = &browserLog
		if err := c.Start(); err != nil {
			return nil, err
		}
		return func() { _ = c.Process.Kill(); _ = c.Wait() }, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	argv := append([]string{"quake-worldcheck"}, args...)
	if os.Getenv("QUAKE_REQUIRE_GPU") == "1" {
		argv = append(argv, "--require-gpu")
	}
	out, ua, err := bench.RunProgramInBrowserWith(ctx, wasm, execJS, argv, map[string]string{"/pak0.pak": pakPath}, launch)
	if err != nil {
		t.Logf("browser log (tail):\n%s", tail(browserLog.String(), 3000))
		t.Fatal(err)
	}
	t.Logf("%s: adapter on %s", ua, os.Getenv("QUAKE_REQUIRE_GPU"))
	lines := strings.Split(strings.TrimSpace(out), "\n")
	return lines[len(lines)-1]
}

func tail(s string, n int) string {
	if len(s) > n {
		return s[len(s)-n:]
	}
	return s
}

// tolerance is the largest accepted share of pixels that differ from the
// software reference and are not a one-pixel shift of it: 2.5 %, against
// 1.5 % measured on the Apple GPU (docs/webgpu-world/).
const tolerance = "0.025"

// TestDamagedColormapFails is the planted negative: the GPU is given a
// colormap with palette entries damaged while the reference keeps the true
// one, and the comparison must fail.
func TestDamagedColormapFails(t *testing.T) {
	v := runInBrowser(t, "--tolerance", tolerance, "--corrupt")
	if v.Check != "FAIL" || !strings.Contains(v.Detail, "unexplained mismatch") {
		t.Fatalf("a damaged colormap was not caught: %+v", v)
	}
}

// TestGPUWorldMatchesTheSoftwareWorld compares the GPU world with the
// software reference on the fixed e1m1 views (see the results document for
// the measured rates and where the two differ).
func TestGPUWorldMatchesTheSoftwareWorld(t *testing.T) {
	v := runInBrowser(t, "--tolerance", tolerance)
	if v.Check != "PASS" || v.Faces == 0 {
		t.Fatalf("verdict %+v", v)
	}
	if os.Getenv("QUAKE_REQUIRE_GPU") == "1" && v.Fallback {
		t.Fatal("a fallback adapter where a GPU is required")
	}
}

type benchRes struct {
	Frames        int     `json:"frames"`
	SoftwareMS    float64 `json:"software_ms"`
	GPUCPUMS      float64 `json:"gpu_cpu_ms"`
	GPUWallMS     float64 `json:"gpu_wall_ms"`
	SoftwareP95MS float64 `json:"software_p95_ms"`
	GPUCPUP95MS   float64 `json:"gpu_cpu_p95_ms"`
	GPUWallP95MS  float64 `json:"gpu_wall_p95_ms"`
}

func load(t *testing.T) float64 {
	t.Helper()
	out, err := exec.Command("uptime").Output()
	if err != nil {
		t.Fatal(err)
	}
	l, err := bench.ParseLoad(string(out))
	if err != nil {
		t.Fatal(err)
	}
	return l
}

// TestBenchWorld measures the world at 1280x800 (QUAKE_BENCH_ROUNDS=7 for a
// publishable table): in every browser run the software reference and the
// GPU world alternate frame by frame on the fixed views. A run whose
// 1-minute load, read before or after, is above 8 is discarded and redone.
func TestBenchWorld(t *testing.T) {
	rounds, _ := strconv.Atoi(os.Getenv("QUAKE_BENCH_ROUNDS"))
	if rounds == 0 {
		t.Skip("QUAKE_BENCH_ROUNDS not set")
	}
	var all []benchRes
	discarded := 0
	for len(all) < rounds {
		before := load(t)
		if before > 8 {
			discarded++
			if discarded > 80 {
				t.Fatalf("the load stayed above 8 (%d discards)", discarded)
			}
			time.Sleep(30 * time.Second)
			continue
		}
		raw := runInBrowserRaw(t, "--bench", "--width", "1280", "--height", "800")
		after := load(t)
		if after > 8 {
			discarded++
			continue
		}
		var v struct {
			Bench *benchRes `json:"bench"`
		}
		if err := json.Unmarshal([]byte(raw), &v); err != nil || v.Bench == nil {
			t.Fatalf("no bench in %q: %v", raw, err)
		}
		t.Logf("round %d: load %.2f/%.2f: software %.3f ms, gpu cpu %.3f ms, gpu wall %.3f ms (p95 %.3f / %.3f / %.3f)", len(all)+1, before, after,
			v.Bench.SoftwareMS, v.Bench.GPUCPUMS, v.Bench.GPUWallMS, v.Bench.SoftwareP95MS, v.Bench.GPUCPUP95MS, v.Bench.GPUWallP95MS)
		all = append(all, *v.Bench)
	}
	med := func(f func(benchRes) float64) (m, lo, hi float64) {
		var xs []float64
		for _, r := range all {
			xs = append(xs, f(r))
		}
		sort.Float64s(xs)
		return xs[len(xs)/2], xs[0], xs[len(xs)-1]
	}
	for _, c := range []struct {
		name string
		f    func(benchRes) float64
	}{
		{"software frame (ms)", func(r benchRes) float64 { return r.SoftwareMS }},
		{"gpu frame, CPU time (ms)", func(r benchRes) float64 { return r.GPUCPUMS }},
		{"gpu frame, wall to GPU idle (ms)", func(r benchRes) float64 { return r.GPUWallMS }},
		{"software p95 (ms)", func(r benchRes) float64 { return r.SoftwareP95MS }},
		{"gpu CPU p95 (ms)", func(r benchRes) float64 { return r.GPUCPUP95MS }},
		{"gpu wall p95 (ms)", func(r benchRes) float64 { return r.GPUWallP95MS }},
	} {
		m, lo, hi := med(c.f)
		t.Logf("| %s | %.3f (%.3f-%.3f) |", c.name, m, lo, hi)
	}
	t.Logf("rounds %d, discarded %d", len(all), discarded)
}
