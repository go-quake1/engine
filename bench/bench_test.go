// Copyright (c) 2026 the go-quake1/engine authors.
// SPDX-License-Identifier: GPL-2.0-or-later

package bench

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-quake1/engine/embedpak"
)

func smallConfig() Config {
	c := DefaultConfig()
	c.Frames, c.Warmup = 12, 2
	return c
}

func TestRunProceduralScene(t *testing.T) {
	fsys, src, err := OpenPak("")
	if err != nil {
		t.Fatal(err)
	}
	if src != "procedural" {
		t.Fatalf("pak source = %q, want procedural (the embedded pak is the placeholder)", src)
	}
	res, err := Run(smallConfig(), fsys, src)
	if err != nil {
		t.Fatal(err)
	}
	if res.Summary.Frames != 12 {
		t.Fatalf("frames = %d, want 12", res.Summary.Frames)
	}
	if res.Summary.Work.Mean <= 0 {
		t.Fatalf("work mean = %v, want > 0", res.Summary.Work.Mean)
	}
	if res.AllocsPerFrame < 0 || res.BytesPerFrame < 0 {
		t.Fatalf("negative allocation figures: %+v", res)
	}
	if res.Pak != "procedural" || res.Go == "" || res.Target == "" || res.Procs < 1 {
		t.Fatalf("metadata missing: %+v", res)
	}
	if got := res.Split.EngineMS + res.Split.RendererMS; got <= 0 {
		t.Fatalf("split = %+v, want a positive total", res.Split)
	}
}

func TestRunErrors(t *testing.T) {
	fsys, _, _ := OpenPak("")
	for _, c := range []Config{
		{Frames: 0, Width: 320, Height: 200},
		{Frames: 1, Warmup: -1, Width: 320, Height: 200},
		{Frames: 1, Width: 0, Height: 200},
		{Frames: 1, Width: 320, Height: 0},
	} {
		if _, err := Run(c, fsys, "x"); !errors.Is(err, ErrConfig) {
			t.Errorf("Run(%+v) = %v, want ErrConfig", c, err)
		}
	}
	bad := smallConfig()
	bad.Renderer = "vulkan"
	if _, err := Run(bad, fsys, "x"); err == nil {
		t.Error("an unknown renderer must be an error")
	}
	// Build refuses a zero-sized display.
	be := &nullBackend{w: 0, h: 0, start: time.Now()}
	if _, err := run(smallConfig(), be, fsys, "x"); err == nil {
		t.Error("a zero-sized backend must be an error")
	}
	// A failing input poll aborts the frame loop.
	want := errors.New("poll")
	be = &nullBackend{w: 320, h: 200, start: time.Now(), pollErr: want}
	if _, err := run(smallConfig(), be, fsys, "x"); !errors.Is(err, want) {
		t.Errorf("run with a failing poll = %v, want %v", err, want)
	}
}

func TestGPURendererFallsBackToSoftware(t *testing.T) {
	fsys, _, _ := OpenPak("")
	c := smallConfig()
	c.Renderer = "gpu"
	if _, err := Run(c, fsys, "x"); err != nil {
		t.Fatal(err)
	}
}

func TestOpenPak(t *testing.T) {
	if _, _, err := OpenPak(filepath.Join(t.TempDir(), "missing.pak")); err == nil {
		t.Error("a missing pak file must be an error")
	}
	bad := filepath.Join(t.TempDir(), "bad.pak")
	if err := os.WriteFile(bad, []byte("not a pak"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := OpenPak(bad); err == nil {
		t.Error("a corrupt pak file must be an error")
	}
	// The embedded placeholder is an empty pak; a pak file with zero
	// entries is accepted as pak0.
	if _, src, err := OpenPak(emptyPak(t)); err != nil || src != "pak0" {
		t.Errorf("OpenPak(empty pak file) = %q, %v", src, err)
	}

	old := openEmbedded
	defer func() { openEmbedded = old }()
	openEmbedded = func() (fs.FS, error) { return emptyFS{}, nil }
	if _, src, err := OpenPak(""); err != nil || src != "embedded" {
		t.Errorf("OpenPak with a real embedded pak = %q, %v", src, err)
	}
	boom := errors.New("boom")
	openEmbedded = func() (fs.FS, error) { return nil, boom }
	if _, _, err := OpenPak(""); !errors.Is(err, boom) {
		t.Errorf("OpenPak = %v, want %v", err, boom)
	}
	openEmbedded = embedpak.OpenAsFS
}

func emptyPak(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "empty.pak")
	// "PACK", directory offset 12, directory length 0.
	hdr := []byte{'P', 'A', 'C', 'K', 12, 0, 0, 0, 0, 0, 0, 0}
	if err := os.WriteFile(p, hdr, 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestEmptyFSHasNoFile(t *testing.T) {
	if _, err := (emptyFS{}).Open("x"); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("Open = %v, want ErrNotExist", err)
	}
}

func TestParseLoad(t *testing.T) {
	for _, c := range []struct {
		in   string
		want float64
	}{
		{"10:38  up 1 day, 6 mins, 1 user, load averages: 6.66 5.84 4.22", 6.66},
		{"10:38  up 1 jour, load averages: 6,66 5,84 4,22", 6.66},
		{" 10:38:01 up 3 days, load average: 0.52, 0.40, 0.30", 0.52},
	} {
		got, err := ParseLoad(c.in)
		if err != nil || got != c.want {
			t.Errorf("ParseLoad(%q) = %v, %v, want %v", c.in, got, err, c.want)
		}
	}
	for _, in := range []string{"", "no such line", "load average", "load average:", "load average: x y z"} {
		if _, err := ParseLoad(in); !errors.Is(err, ErrNoLoad) {
			t.Errorf("ParseLoad(%q) = %v, want ErrNoLoad", in, err)
		}
	}
}

func TestStatOf(t *testing.T) {
	if (StatOf(nil) != Stat{}) {
		t.Error("no values must give the zero Stat")
	}
	if s := StatOf([]float64{3, 1, 2}); s != (Stat{2, 1, 3}) {
		t.Errorf("odd = %+v", s)
	}
	if s := StatOf([]float64{4, 1, 3, 2}); s != (Stat{2.5, 1, 4}) {
		t.Errorf("even = %+v", s)
	}
}

func fakeTarget(name string, work float64) Target {
	return Target{Name: name, Run: func(context.Context, Config) (Result, error) {
		var r Result
		r.Summary.Work.P50 = work
		return r, nil
	}}
}

func loads(vals ...float64) func() (float64, error) {
	i := 0
	return func() (float64, error) {
		v := vals[i%len(vals)]
		i++
		return v, nil
	}
}

func TestInterleaveOrderAndLoadLog(t *testing.T) {
	var order []string
	mk := func(n string) Target {
		return Target{Name: n, Run: func(context.Context, Config) (Result, error) {
			order = append(order, n)
			return Result{}, nil
		}}
	}
	rounds, disc, err := Interleave(context.Background(), []Target{mk("A"), mk("B")}, smallConfig(), 3, 8, 2, loads(1, 2), func() {})
	if err != nil || len(disc) != 0 {
		t.Fatalf("err=%v discards=%v", err, disc)
	}
	if got := strings.Join(order, ""); got != "ABABAB" {
		t.Fatalf("order = %s, want ABABAB", got)
	}
	log := LoadLog(rounds, disc)
	if !strings.Contains(log, "round 1 A: load1 before 1.00 after 2.00") {
		t.Errorf("load log = %q", log)
	}
}

func TestInterleaveDiscardsHighLoad(t *testing.T) {
	waits := 0
	// before 9 (discard), before 1 + after 9 (discard), before 1 + after 1 (accept).
	rounds, disc, err := Interleave(context.Background(), []Target{fakeTarget("A", 1)}, smallConfig(), 1, 8, 5,
		loads(9, 1, 9, 1, 1), func() { waits++ })
	if err != nil || len(rounds) != 1 || len(disc) != 2 || waits != 2 {
		t.Fatalf("err=%v rounds=%d discards=%v waits=%d", err, len(rounds), disc, waits)
	}
	if disc[0].After || !disc[1].After {
		t.Errorf("discard sides = %+v", disc)
	}
	log := LoadLog(rounds, disc)
	if !strings.Contains(log, "discarded A: load1 9.00 before") || !strings.Contains(log, "discarded A: load1 9.00 after") {
		t.Errorf("load log = %q", log)
	}
}

func TestInterleaveGivesUp(t *testing.T) {
	_, disc, err := Interleave(context.Background(), []Target{fakeTarget("A", 1)}, smallConfig(), 1, 8, 2, loads(9), func() {})
	if !errors.Is(err, ErrLoadTooHigh) || len(disc) != 3 {
		t.Fatalf("err=%v discards=%d, want ErrLoadTooHigh after 3", err, len(disc))
	}
}

func TestInterleaveErrors(t *testing.T) {
	boom := errors.New("boom")
	failing := Target{Name: "F", Run: func(context.Context, Config) (Result, error) { return Result{}, boom }}
	if _, _, err := Interleave(context.Background(), []Target{failing}, smallConfig(), 1, 8, 1, loads(1), func() {}); !errors.Is(err, boom) {
		t.Errorf("target error = %v", err)
	}
	bad := func() (float64, error) { return 0, boom }
	if _, _, err := Interleave(context.Background(), []Target{fakeTarget("A", 1)}, smallConfig(), 1, 8, 1, bad, func() {}); !errors.Is(err, boom) {
		t.Errorf("load error before = %v", err)
	}
	n := 0
	afterBad := func() (float64, error) {
		n++
		if n == 2 {
			return 0, boom
		}
		return 1, nil
	}
	if _, _, err := Interleave(context.Background(), []Target{fakeTarget("A", 1)}, smallConfig(), 1, 8, 1, afterBad, func() {}); !errors.Is(err, boom) {
		t.Errorf("load error after = %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := Interleave(ctx, []Target{fakeTarget("A", 1)}, smallConfig(), 1, 8, 1, loads(1), func() {}); !errors.Is(err, context.Canceled) {
		t.Errorf("cancelled = %v", err)
	}
}

func TestTable(t *testing.T) {
	rounds := []Round{
		{Target: "A", Result: resultWithWork(1)},
		{Target: "A", Result: resultWithWork(3)},
		{Target: "B", Result: resultWithWork(10)},
	}
	tbl := Table(rounds, []string{"A", "B"})
	if !strings.Contains(tbl, "| metric | A | B |") {
		t.Errorf("header missing: %s", tbl)
	}
	if !strings.Contains(tbl, "| frame work p50 (ms) | 2.000 (1.000-3.000) | 10.000 (10.000-10.000) |") {
		t.Errorf("row missing: %s", tbl)
	}
}

func resultWithWork(p50 float64) Result {
	var r Result
	r.Summary.Work.P50 = p50
	return r
}

func TestParseResult(t *testing.T) {
	out := "QUAKE: log line\n{\"pak\":\"procedural\",\"allocs_per_frame\":3}\n\n"
	res, err := ParseResult(out)
	if err != nil || res.Pak != "procedural" || res.AllocsPerFrame != 3 {
		t.Fatalf("ParseResult = %+v, %v", res, err)
	}
	long := strings.Repeat("x", 500)
	for _, in := range []string{"", "QUAKE: only logs", long} {
		if _, err := ParseResult(in); !errors.Is(err, ErrNoResult) {
			t.Errorf("ParseResult(%.20q) = %v, want ErrNoResult", in, err)
		}
	}
}

func TestNullBackendClock(t *testing.T) {
	be := &nullBackend{start: time.Now()}
	if be.Now() < 0 {
		t.Error("clock went backwards")
	}
}
