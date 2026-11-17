// Copyright (c) 2026 the go-quake1/engine authors.
// SPDX-License-Identifier: GPL-2.0-or-later

package main

import (
	"bytes"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/go-quake1/engine/render"
	"github.com/go-quake1/engine/runloop"
)

type fakeWriteTimer struct{ d time.Duration }

func (f *fakeWriteTimer) TakeWriteTime() time.Duration { d := f.d; f.d = 0; return d }

// A slow tic is reported with where its time went; the fast ones are not. The
// numbers are chosen so each part of the 0.4 s tic is distinct: sim 20 ms, world
// 300 ms of which 250 is the GPU (200 submit and read-back, 50 copy), 2d 10,
// present 20, 30 ms unstaged (the audio mix), 20 outside the frame.
func TestWriteSlowTics(t *testing.T) {
	var st [render.NumStages]float64
	// A sample's Interval is the wall time to the NEXT frame's start, as the next
	// sample records it: frame 2 does the work, frame 3's Interval is its 0.4 s.
	fast := render.FrameSample{Interval: 0.03, Work: 0.03}
	slow := render.FrameSample{Interval: 0.03, Work: 0.38}
	st[render.StageSim], st[render.StageWorld], st[render.Stage2D], st[render.StagePresent] = 0.02, 0.30, 0.01, 0.02
	slow.Stages = st
	after := render.FrameSample{Interval: 0.4, Work: 0.03}
	samples := []render.FrameSample{fast, fast, slow, after, fast}
	gpu := &gpuTimes{}
	for range samples {
		gpu.add(time.Millisecond, time.Millisecond)
	}
	gpu.gpu[2], gpu.copy[2] = 200*time.Millisecond, 50*time.Millisecond
	p := &frameProbe{GC: []int{0, 0, 2, 0, 0}, GCPause: []time.Duration{0, 0, 3 * time.Millisecond, 0, 0},
		AudioWrite: []time.Duration{0, 0, 4 * time.Millisecond, 0, 0}, Steps: []int{1, 0, 3, 0, 1},
		Yield: []time.Duration{0, 0, 15 * time.Millisecond, 0, 0}, ProbeCost: []time.Duration{0, 0, 2 * time.Millisecond, 0, 0},
		YieldGC: []time.Duration{0, 0, 6 * time.Millisecond, 0, 0}, Goroutines: []int{3, 3, 7, 3, 3},
		YieldScav: []time.Duration{0, 0, 2 * time.Millisecond, 0, 0}, YieldCleanups: []int{0, 0, 40, 0, 0},
		YieldFinalizers: []int{0, 0, 1, 0, 0}, Cleanups: []int{0, 5, 90, 3, 0}}

	var out bytes.Buffer
	writeSlowTics(&out, samples, gpu, p)
	got := out.String()
	if n := strings.Count(got, "slowtic frame="); n != 1 {
		t.Fatalf("%d slow-tic lines, want 1:\n%s", n, got)
	}
	for _, want := range []string{"slowtic frame=2 steps=3 interval-ms=400.0 sim=20.0 world=50.0 gpu=200.0 copy=50.0", "composite=10.0 present=20.0 unstaged=30.0 outside=20.0 yield=15.00 probe=2.00 yield-gc=6.00 gor=7 scav=2.00 cleanups=40 finalizers=1 frame-cleanups=90 audio-write=4.00 gc-cycles=2 gc-pause-ms=3.00",
		"yieldwho: of the other goroutines' 9 ms: scavenger 2 ms; 40 cleanups and 1 finalizers ran in those yields (90 cleanups in the whole frames of the slow tics); sweeper and the rest 7 ms",
		"yieldshare: of the 15 ms the slow tics spent in the yield, collector background mark 6 ms (40%), other goroutines 9 ms (60%)",
		"slowtics: 1 over 100 ms, 400 ms in all; share of that time: sim 5% world 12% gpu 50% copy 12%"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
	out.Reset()
	writeSlowTics(&out, samples[:2], gpu, &frameProbe{GC: []int{0, 0}})
	if !strings.Contains(out.String(), "slowtics: none over 100 ms in 2 frames") {
		t.Errorf("no slow tic: %q", out.String())
	}
}

// --slowtics-brief keeps the summary lines and drops the line of every tic and of
// every dry write, the yield split and the goroutine dump.
func TestSlowticsBrief(t *testing.T) {
	slowBrief = true
	defer func() { slowBrief = false }()
	var st [render.NumStages]float64
	slow := render.FrameSample{Interval: 0.03, Work: 0.38, Stages: st}
	samples := []render.FrameSample{{Interval: 0.03, Work: 0.03}, slow, {Interval: 0.4, Work: 0.03}}
	p := &frameProbe{GC: []int{0, 0, 0}, GCPause: make([]time.Duration, 3), AudioWrite: make([]time.Duration, 3), Yield: []time.Duration{0, 15 * time.Millisecond, 0}}
	var out bytes.Buffer
	writeSlowTics(&out, samples, nil, p)
	got := out.String()
	if strings.Contains(got, "slowtic frame=") || strings.Contains(got, "yieldshare") || strings.Contains(got, "yieldwho") {
		t.Errorf("a per-tic or yield line in brief mode:\n%s", got)
	}
	if !strings.Contains(got, "slowtics: 1 over 100 ms, 400 ms in all") {
		t.Errorf("no summary line:\n%s", got)
	}
	goroutinesDumped = false
	out.Reset()
	dumpGoroutines(&out)
	if out.Len() != 0 {
		t.Errorf("the goroutine dump in brief mode: %q", out.String())
	}
	// A dry write is counted and not listed.
	l := &fakeWriteLog{}
	q := &frameProbe{wl: l}
	base := time.Unix(1000, 0)
	for _, w := range []struct {
		after time.Duration
		q     int
	}{{0, 882}, {300 * time.Millisecond, 882}} {
		l.at, l.q = base.Add(w.after), w.q
		q.frame(0)
	}
	out.Reset()
	writeAudioGaps(&out, q)
	if strings.Contains(out.String(), "audiogap frame=") || !strings.Contains(out.String(), "audiogaps: 1 of 1 writes ran dry") {
		t.Errorf("the dry write in brief mode:\n%s", out.String())
	}
}

// A game with no --frames reports a window of frames and starts the next one
// empty: the report names the slow tic, and the timer, the probe and the GPU
// times are cleared so the next window's indexes line up again.
func TestReportWindow(t *testing.T) {
	now := 0.0
	r := &runloop.Runner{}
	r.Timer, _ = render.NewFrameTimer(func() float64 { return now }, 4)
	gpu := &gpuTimes{}
	p := newFrameProbe(nil, func() int { return 2 })
	for _, gap := range []float64{0.03, 0.03, 0.25, 0.03} { // frame 2 lasts 0.25 s
		r.Timer.Begin()
		now += gap
		r.Timer.Mark(render.StageSim)
		r.Timer.End()
		gpu.add(time.Millisecond, 0)
		p.frame(0)
	}
	var out bytes.Buffer
	reportWindow(&out, r, gpu, p)
	if !strings.Contains(out.String(), "slowtic frame=2 steps=2 interval-ms=250.0") || !strings.Contains(out.String(), "slowtics: 1 over 100 ms") {
		t.Errorf("the window's report:\n%s", out.String())
	}
	if r.Timer.Len() != 0 || len(p.GC) != 0 || len(p.Steps) != 0 || len(gpu.gpu) != 0 || len(gpu.copy) != 0 {
		t.Errorf("the next window is not empty: timer %d, probe %d, gpu %d", r.Timer.Len(), len(p.GC), len(gpu.gpu))
	}
	var nilProbe *frameProbe
	nilProbe.reset() // no probe, no panic
	reportWindow(&out, r, nil, p)
}

// The probe sees the collector and the sound write: a forced collection shows
// as a cycle in its frame, the write time is taken once.
func TestFrameProbe(t *testing.T) {
	w := &fakeWriteTimer{}
	steps := 0
	p := newFrameProbe(w, func() int { return steps })
	p.frame(time.Millisecond)
	steps = 3
	w.d = 7 * time.Millisecond
	runtime.GC()
	p.frame(0)
	p.frame(0)
	if p.Yield[0] != time.Millisecond || len(p.ProbeCost) != 3 || p.ProbeCost[1] <= 0 {
		t.Errorf("yield %v and probe cost %v per frame", p.Yield, p.ProbeCost)
	}
	if len(p.GC) != 3 || p.GC[0] != 0 || p.GC[1] < 1 || p.GC[2] != 0 {
		t.Errorf("collector cycles per frame %v, want 0, at least 1, 0", p.GC)
	}
	if p.AudioWrite[1] != 7*time.Millisecond || p.AudioWrite[2] != 0 {
		t.Errorf("sound write time per frame %v", p.AudioWrite)
	}
	if len(p.Steps) != 3 || p.Steps[0] != 0 || p.Steps[1] != 3 {
		t.Errorf("server ticks per frame %v, want 0, 3, 3", p.Steps)
	}
	if q := newFrameProbe(nil, nil); q.steps != nil {
		t.Error("no step source must stay nil")
	} else if q.frame(0); q.Steps[0] != 0 {
		t.Errorf("no step source: %v", q.Steps)
	}
	var nilProbe *frameProbe
	nilProbe.frame(0) // no --frames: no probe, no panic
}

// The goroutine dump prints once, each line prefixed, and names this test's own
// goroutine.
func TestDumpGoroutines(t *testing.T) {
	goroutinesDumped = false
	var out bytes.Buffer
	dumpGoroutines(&out)
	first := out.String()
	if !strings.HasPrefix(first, "goroutines: goroutine profile: total ") || !strings.Contains(first, "TestDumpGoroutines") {
		t.Errorf("the dump:\n%s", first)
	}
	for _, l := range strings.Split(strings.TrimRight(first, "\n"), "\n") {
		if !strings.HasPrefix(l, "goroutines: ") {
			t.Errorf("an unprefixed line %q", l)
		}
	}
	dumpGoroutines(&out)
	if out.String() != first {
		t.Error("the dump printed twice")
	}
}

// timeYield times the yield and notes what ran in it: the goroutines alive, and
// the collector's mark time (zero here: nothing forces a cycle in a short sleep).
func TestTimeYield(t *testing.T) {
	p := newFrameProbe(nil, nil)
	d := p.timeYield(func() { time.Sleep(5 * time.Millisecond) })
	if d < 5*time.Millisecond {
		t.Errorf("the yield took %v, want at least 5 ms", d)
	}
	p.frame(d)
	if len(p.YieldGC) != 1 || p.YieldGC[0] < 0 || len(p.Goroutines) != 1 || p.Goroutines[0] < 1 {
		t.Errorf("yield-gc %v, goroutines %v", p.YieldGC, p.Goroutines)
	}
	if len(p.YieldScav) != 1 || len(p.YieldCleanups) != 1 || len(p.YieldFinalizers) != 1 || len(p.Cleanups) != 1 {
		t.Errorf("scavenger %v, cleanups %v, finalizers %v, frame cleanups %v", p.YieldScav, p.YieldCleanups, p.YieldFinalizers, p.Cleanups)
	}
}

// A cleanup (runtime.AddCleanup, as the GPU handle ids use) that runs in the
// yield is counted in it and in its frame; the counters are the runtime's own.
func TestYieldCountsCleanups(t *testing.T) {
	p := newFrameProbe(nil, nil)
	func() {
		for i := 0; i < 50; i++ {
			o := new([64]byte)
			runtime.AddCleanup(o, func(int) {}, i)
		}
	}()
	d := p.timeYield(func() {
		runtime.GC() // the objects are unreachable: their cleanups are queued
		for i := 0; i < 100; i++ {
			runtime.Gosched() // and run by the cleanup goroutine
			time.Sleep(time.Millisecond)
		}
	})
	p.frame(d)
	if p.YieldCleanups[0] < 1 || p.Cleanups[0] < p.YieldCleanups[0] {
		t.Errorf("cleanups in the yield %v and in the frame %v, want some", p.YieldCleanups, p.Cleanups)
	}
	var nilProbe *frameProbe
	if d := nilProbe.timeYield(func() {}); d < 0 {
		t.Error("a nil probe still times the yield")
	}
}

type fakeWriteLog struct {
	at time.Time
	q  int
}

func (f *fakeWriteLog) LastWrite() (time.Time, int, bool) { return f.at, f.q, !f.at.IsZero() }

// A gap between two writes longer than what the first left queued is a dry run:
// 80 ms of audio queued and a 250 ms gap is 170 ms short; the 40 ms gap on 80 ms
// is bridged, and a frame with no write (the same time again) is not a write.
func TestWriteAudioGaps(t *testing.T) {
	l := &fakeWriteLog{}
	p := &frameProbe{wl: l}
	base := time.Unix(1000, 0)
	for _, w := range []struct {
		after time.Duration
		q     int
	}{{0, 882}, {40 * time.Millisecond, 882}, {40 * time.Millisecond, 882}, {290 * time.Millisecond, 441}} { // 80 ms, 80 ms, (no write), 40 ms
		l.at, l.q = base.Add(w.after), w.q
		p.frame(0)
	}
	var out bytes.Buffer
	writeAudioGaps(&out, p)
	got := out.String()
	if n := strings.Count(got, "audiogap frame="); n != 1 || !strings.Contains(got, "audiogap frame=3 gap-ms=250.0 queued-ms=80.0 short-ms=170.0") {
		t.Errorf("the dry gaps:\n%s", got)
	}
	if !strings.Contains(got, "audiogaps: 1 of 2 writes ran dry, 170 ms short in all, worst 170.0 ms, median queue before a dry gap 80.0 ms; writes more than 50 100 200 ms after the one before: 1 1 1") {
		t.Errorf("the summary:\n%s", got)
	}
	out.Reset()
	writeAudioGaps(&out, &frameProbe{}) // a backend that does not log its writes: nothing
	writeAudioGaps(&out, nil)
	if out.Len() != 0 {
		t.Errorf("no write log, yet %q", out.String())
	}
}
