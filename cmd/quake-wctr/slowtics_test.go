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
		AudioWrite: []time.Duration{0, 0, 4 * time.Millisecond, 0, 0}}

	var out bytes.Buffer
	writeSlowTics(&out, samples, gpu, p)
	got := out.String()
	if n := strings.Count(got, "slowtic frame="); n != 1 {
		t.Fatalf("%d slow-tic lines, want 1:\n%s", n, got)
	}
	for _, want := range []string{"slowtic frame=2 interval-ms=400.0 sim=20.0 world=50.0 gpu=200.0 copy=50.0", "composite=10.0 present=20.0 unstaged=30.0 outside=20.0 audio-write=4.00 gc-cycles=2 gc-pause-ms=3.00",
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

// The probe sees the collector and the sound write: a forced collection shows
// as a cycle in its frame, the write time is taken once.
func TestFrameProbe(t *testing.T) {
	w := &fakeWriteTimer{}
	p := newFrameProbe(w)
	p.frame()
	w.d = 7 * time.Millisecond
	runtime.GC()
	p.frame()
	p.frame()
	if len(p.GC) != 3 || p.GC[0] != 0 || p.GC[1] < 1 || p.GC[2] != 0 {
		t.Errorf("collector cycles per frame %v, want 0, at least 1, 0", p.GC)
	}
	if p.AudioWrite[1] != 7*time.Millisecond || p.AudioWrite[2] != 0 {
		t.Errorf("sound write time per frame %v", p.AudioWrite)
	}
	var nilProbe *frameProbe
	nilProbe.frame() // no --frames: no probe, no panic
}
