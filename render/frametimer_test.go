// Copyright (c) 2026 the go-quake1/engine authors.
// SPDX-License-Identifier: GPL-2.0-or-later

package render

import (
	"errors"
	"math"
	"testing"
)

// fakeClock returns a clock the test advances by hand.
func fakeClock() (*float64, func() float64) {
	now := new(float64)
	return now, func() float64 { return *now }
}

func near(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

func TestNewFrameTimerArgs(t *testing.T) {
	_, clk := fakeClock()
	if _, err := NewFrameTimer(nil, 4); !errors.Is(err, ErrFrameTimerArgs) {
		t.Errorf("nil clock: %v", err)
	}
	if _, err := NewFrameTimer(clk, 0); !errors.Is(err, ErrFrameTimerArgs) {
		t.Errorf("zero capacity: %v", err)
	}
}

func TestFrameTimerNilIsNoop(t *testing.T) {
	var ft *FrameTimer
	ft.Begin()
	ft.Mark(StageWorld)
	ft.End()
	ft.Abort()
	ft.Reset()
	if ft.Len() != 0 || ft.Samples() != nil {
		t.Fatal("nil timer recorded something")
	}
}

func TestFrameTimerStages(t *testing.T) {
	now, clk := fakeClock()
	ft, err := NewFrameTimer(clk, 3)
	if err != nil {
		t.Fatal(err)
	}
	if ft.Samples() != nil {
		t.Fatal("samples before any frame")
	}
	// Mark / End outside a frame are ignored.
	ft.Mark(StageSim)
	ft.End()
	if ft.Len() != 0 {
		t.Fatal("End outside a frame recorded a sample")
	}

	frame := func(start float64) {
		*now = start
		ft.Begin()
		*now += 0.001
		ft.Mark(StageSim)
		*now += 0.010
		ft.Mark(StageWorld)
		*now += 0.002
		ft.Mark(StageWorld) // accumulates
		ft.Mark(Stage(-1))  // ignored
		ft.Mark(NumStages)  // ignored
		*now += 0.003
		ft.Mark(StageModels)
		*now += 0.004
		ft.Mark(Stage2D)
		*now += 0.005
		ft.Mark(StagePresent)
		*now += 0.001 // unmarked tail: in Work only
		ft.End()
	}
	frame(1)
	frame(1.050)
	s := ft.Samples()
	if len(s) != 2 || ft.Len() != 2 {
		t.Fatalf("len %d/%d", len(s), ft.Len())
	}
	want := [NumStages]float64{0.001, 0.012, 0.003, 0.004, 0.005}
	for st, w := range want {
		if !near(s[1].Stages[st], w) {
			t.Errorf("stage %v = %v want %v", Stage(st), s[1].Stages[st], w)
		}
	}
	if !near(s[1].Work, 0.026) || s[0].Interval != 0 || !near(s[1].Interval, 0.050) {
		t.Errorf("work %v interval %v/%v", s[1].Work, s[0].Interval, s[1].Interval)
	}

	// The ring keeps the last 3, oldest first.
	frame(1.100)
	frame(1.170)
	s = ft.Samples()
	if len(s) != 3 || !near(s[0].Interval, 0.050) || !near(s[2].Interval, 0.070) {
		t.Fatalf("ring order: %+v", s)
	}

	// Abort drops the frame in progress.
	*now = 2
	ft.Begin()
	ft.Abort()
	ft.End()
	if ft.Len() != 3 {
		t.Fatal("aborted frame recorded")
	}

	ft.Reset()
	if ft.Len() != 0 {
		t.Fatal("Reset kept samples")
	}
	frame(3)
	if s := ft.Samples(); s[0].Interval != 0 {
		t.Fatalf("interval after Reset = %v", s[0].Interval)
	}
}

func TestStageString(t *testing.T) {
	for st, want := range []string{"sim", "world", "models", "2d", "present"} {
		if got := Stage(st).String(); got != want {
			t.Errorf("Stage(%d) = %q", st, got)
		}
	}
	if Stage(-1).String() != "unknown" || NumStages.String() != "unknown" {
		t.Error("out-of-range stage name")
	}
}

func TestSummarize(t *testing.T) {
	if s := Summarize(nil); s.Frames != 0 || s.FPS != 0 || len(s.Stages) != int(NumStages) {
		t.Fatalf("empty: %+v", s)
	}
	var samples []FrameSample
	for i := 1; i <= 100; i++ {
		var f FrameSample
		f.Interval = float64(i) / 1000 // 1..100 ms
		f.Work = f.Interval / 2
		f.Stages[StageWorld] = f.Work
		samples = append(samples, f)
	}
	samples[0].Interval = 0 // first frame: no interval, excluded
	s := Summarize(samples)
	if s.Frames != 100 {
		t.Fatalf("frames %d", s.Frames)
	}
	// Intervals 2..100 ms: mean 51, p50 = 51st of 99 -> ceil(49.5)=50th -> 51 ms.
	if !near(s.Interval.Mean, 51) || !near(s.Interval.P50, 51) || !near(s.Interval.Max, 100) {
		t.Errorf("interval %+v", s.Interval)
	}
	if !near(s.FPS, 1000.0/51) {
		t.Errorf("fps %v", s.FPS)
	}
	// Work 0.5..50 ms: p95 = 95th of 100 = 47.5 ms, p99 = 49.5 ms.
	if !near(s.Work.P95, 47.5) || !near(s.Work.P99, 49.5) {
		t.Errorf("work %+v", s.Work)
	}
	if s.Stages[StageWorld].Stage != "world" || !near(s.Stages[StageWorld].Max, 50) || s.Stages[StageSim].Max != 0 {
		t.Errorf("stages %+v", s.Stages)
	}
}
