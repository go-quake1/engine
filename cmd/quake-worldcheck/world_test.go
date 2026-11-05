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
	return runInBrowserIn(t, ".", args...)
}

// runInBrowserIn is runInBrowserRaw for the program built from dir (a
// quake-worldcheck directory: this one, or the same one of a clean clone of an
// earlier tag).
func runInBrowserIn(t *testing.T, dir string, args ...string) string {
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
	build.Dir = dir
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
	if os.Getenv("QUAKE_BROWSER_LOG") != "" {
		t.Logf("browser log (tail):\n%s", tail(browserLog.String(), 4000))
	}
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

// TestWrongMipFails is the planted negative of the surface cache: the GPU
// takes every face one mip level too far while the reference keeps Quake's,
// and the comparison must fail.
func TestWrongMipFails(t *testing.T) {
	v := runInBrowser(t, "--tolerance", tolerance, "--mipBias")
	if v.Check != "FAIL" || !strings.Contains(v.Detail, "unexplained mismatch") {
		t.Fatalf("a wrong mip level was not caught: %+v", v)
	}
}

// TestNoMipFails: the GPU without the mip choice (every face at level 0, the
// old full-size lookup) against the surface-cache reference must fail too.
func TestNoMipFails(t *testing.T) {
	v := runInBrowser(t, "--tolerance", tolerance, "--noMip")
	if v.Check != "FAIL" {
		t.Fatalf("the GPU without mip levels passed: %+v", v)
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
	Frames         int     `json:"frames"`
	SoftwareMS     float64 `json:"software_ms"`
	SoftwareColdMS float64 `json:"software_cold_ms"`
	EngineMS       float64 `json:"engine_ms"`
	EngineP95MS    float64 `json:"engine_p95_ms"`
	GPUCPUMS       float64 `json:"gpu_cpu_ms"`
	GPUWallMS      float64 `json:"gpu_wall_ms"`
	SoftwareP95MS  float64 `json:"software_p95_ms"`
	GPUCPUP95MS    float64 `json:"gpu_cpu_p95_ms"`
	GPUWallP95MS   float64 `json:"gpu_wall_p95_ms"`
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
		{"engine software world (ms)", func(r benchRes) float64 { return r.EngineMS }},
		{"engine software world p95 (ms)", func(r benchRes) float64 { return r.EngineP95MS }},
		{"software frame (ms)", func(r benchRes) float64 { return r.SoftwareMS }},
		{"software frame, surface cache emptied first (ms)", func(r benchRes) float64 { return r.SoftwareColdMS }},
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

// nativeNsPerOp runs BenchmarkDrawSoftware in dir (a clone with worldview's
// world_bench_test.go) and returns its ns/op.
func nativeNsPerOp(t *testing.T, dir string) float64 {
	t.Helper()
	c := exec.Command("go", "test", "./worldview", "-run", "^$", "-bench", "BenchmarkDrawSoftware", "-benchtime", "64x", "-count", "1")
	c.Dir = dir
	out, err := c.CombinedOutput()
	if err != nil {
		t.Fatalf("native bench in %s: %v\n%s", dir, err, out)
	}
	for _, l := range strings.Split(string(out), "\n") {
		f := strings.Fields(l)
		if len(f) >= 4 && strings.HasPrefix(f[0], "BenchmarkDrawSoftware") && f[3] == "ns/op" {
			v, err := strconv.ParseFloat(f[2], 64)
			if err != nil {
				t.Fatal(err)
			}
			return v
		}
	}
	t.Fatalf("no ns/op in %s", out)
	return 0
}

// TestBenchAgainstBaseline alternates, round by round, a clean clone of an
// earlier tag (QUAKE_BASELINE_DIR, the repository root) and this tree, in the
// browser (software world and GPU world, 1280x800) and natively (the software
// world), with the order swapped every round. A round whose 1-minute load,
// read before or after, is above 8 is discarded. QUAKE_BENCH_ROUNDS rounds.
func TestBenchAgainstBaseline(t *testing.T) {
	base := os.Getenv("QUAKE_BASELINE_DIR")
	rounds, _ := strconv.Atoi(os.Getenv("QUAKE_BENCH_ROUNDS"))
	if base == "" || rounds == 0 {
		t.Skip("QUAKE_BASELINE_DIR and QUAKE_BENCH_ROUNDS not set")
	}
	type sample struct {
		b      benchRes
		native float64
	}
	run := func(dir, root string) (sample, bool) {
		before := load(t)
		if before > 8 {
			return sample{}, false
		}
		raw := runInBrowserIn(t, filepath.Join(dir, "cmd", "quake-worldcheck"), "--bench", "--width", "1280", "--height", "800")
		var v struct {
			Bench *benchRes `json:"bench"`
		}
		if err := json.Unmarshal([]byte(raw), &v); err != nil || v.Bench == nil {
			t.Fatalf("no bench in %q: %v", raw, err)
		}
		n := nativeNsPerOp(t, root)
		if load(t) > 8 {
			return sample{}, false
		}
		return sample{*v.Bench, n}, true
	}
	here, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	var baseS, newS []sample
	discarded := 0
	for r := 0; len(newS) < rounds; r++ {
		order := []string{base, here}
		if r%2 == 1 {
			order = []string{here, base}
		}
		got := map[string]sample{}
		ok := true
		for _, d := range order {
			s, good := run(d, d)
			if !good {
				ok = false
				break
			}
			got[d] = s
		}
		if !ok {
			if discarded++; discarded > 80 {
				t.Fatalf("the load stayed above 8 (%d discards)", discarded)
			}
			time.Sleep(30 * time.Second)
			continue
		}
		baseS, newS = append(baseS, got[base]), append(newS, got[here])
		t.Logf("round %d: base software %.3f gpu wall %.3f native %.1f ms | new software %.3f (cold %.3f) gpu wall %.3f native %.1f ms",
			len(newS), got[base].b.SoftwareMS, got[base].b.GPUWallMS, got[base].native/1e6,
			got[here].b.SoftwareMS, got[here].b.SoftwareColdMS, got[here].b.GPUWallMS, got[here].native/1e6)
	}
	med := func(ss []sample, f func(sample) float64) (m, lo, hi float64) {
		var xs []float64
		for _, s := range ss {
			xs = append(xs, f(s))
		}
		sort.Float64s(xs)
		return xs[len(xs)/2], xs[0], xs[len(xs)-1]
	}
	for _, c := range []struct {
		name string
		f    func(sample) float64
		both bool
	}{
		{"software world in the browser (ms)", func(s sample) float64 { return s.b.SoftwareMS }, true},
		{"software world in the browser, cache emptied first (ms)", func(s sample) float64 { return s.b.SoftwareColdMS }, false},
		{"software world, native (ms)", func(s sample) float64 { return s.native / 1e6 }, true},
		{"GPU world, CPU time (ms)", func(s sample) float64 { return s.b.GPUCPUMS }, true},
		{"GPU world, wall to GPU idle (ms)", func(s sample) float64 { return s.b.GPUWallMS }, true},
		{"engine's affine world path (ms)", func(s sample) float64 { return s.b.EngineMS }, true},
	} {
		bm, blo, bhi := med(baseS, c.f)
		nm, nlo, nhi := med(newS, c.f)
		if c.both {
			t.Logf("| %s | %.3f (%.3f-%.3f) | %.3f (%.3f-%.3f) |", c.name, bm, blo, bhi, nm, nlo, nhi)
		} else {
			t.Logf("| %s | | %.3f (%.3f-%.3f) |", c.name, nm, nlo, nhi)
		}
	}
	t.Logf("rounds %d, discarded %d", len(newS), discarded)
}

// TestAttribution reruns the comparison with one ingredient removed on both
// sides, to see which one the unexplained pixels come from (QUAKE_ATTRIBUTION=1).
func TestAttribution(t *testing.T) {
	if os.Getenv("QUAKE_ATTRIBUTION") == "" {
		t.Skip("QUAKE_ATTRIBUTION not set")
	}
	for _, c := range [][]string{{"--nolight"}, {"--flatTex"}, {"--nolight", "--flatTex"}, {"--exactSpans"}, {"--exactSpans", "--nolight"},
		// the surface cache's ingredients: no mip choice on the GPU, the
		// reference at level 0 against the GPU's choice, both at level 0, and
		// the reference of v0.6.1 (what the GPU matched before the cache)
		{"--noMip"}, {"--refMip0"}, {"--noMip", "--refMip0"}, {"--oldRef"}, {"--mipBias"}} {
		t.Logf("probe %v", c)
		runInBrowser(t, append([]string{"--tolerance", "1"}, c...)...)
	}
}
