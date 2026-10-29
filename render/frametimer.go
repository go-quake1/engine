// Copyright (c) 2026 the go-quake1/engine authors.
// SPDX-License-Identifier: GPL-2.0-or-later

package render

import (
	"errors"
	"slices"
)

// Stage is one phase of a frame, as the [FrameTimer] splits it.
type Stage int

const (
	// StageSim is input, the server ticks, the client tick and the
	// particle simulation.
	StageSim Stage = iota
	// StageWorld is the BSP walk and the world surfaces (plus any part
	// of the 3D pass a renderer does not attribute itself).
	StageWorld
	// StageModels is alias models, sprites, particles and beams.
	StageModels
	// Stage2D is the status bar, the menu, the console and the other
	// 2D layers.
	Stage2D
	// StagePresent is the RGBA resolve and the hand-off to the backend.
	StagePresent
	// NumStages is the number of stages.
	NumStages
)

var stageNames = [NumStages]string{"sim", "world", "models", "2d", "present"}

// String returns the stage's short name ("sim", "world", ...).
func (s Stage) String() string {
	if s < 0 || s >= NumStages {
		return "unknown"
	}
	return stageNames[s]
}

// FrameSample is one frame's timing, in seconds.
type FrameSample struct {
	// Stages is the time spent in each stage.
	Stages [NumStages]float64
	// Work is the time from Begin to End (the sum of the stages plus
	// anything not marked).
	Work float64
	// Interval is the wall time since the previous frame's Begin: the
	// frame time a player sees. Zero for the first frame.
	Interval float64
}

// ErrFrameTimerArgs is returned by [NewFrameTimer] for a nil clock or a
// capacity below one.
var ErrFrameTimerArgs = errors.New("render: frame timer needs a clock and a capacity >= 1")

// FrameTimer measures where a frame's time goes. The loop calls Begin
// at the start of a frame, Mark(stage) at the end of each stage (the
// time since the previous Begin or Mark is added to that stage) and
// End when the frame is presented. The last Capacity frames are kept.
//
// Every method is a no-op on a nil *FrameTimer, so an unmeasured loop
// pays one nil check per call. It allocates only in [NewFrameTimer]
// and [FrameTimer.Samples].
type FrameTimer struct {
	now       func() float64
	begin     float64
	last      float64
	prevBegin float64
	started   bool
	haveBegin bool
	cur       FrameSample
	ring      []FrameSample
	next      int
	n         int
}

// NewFrameTimer returns a timer reading now (seconds, monotonic) that
// keeps the last capacity frames.
func NewFrameTimer(now func() float64, capacity int) (*FrameTimer, error) {
	if now == nil || capacity < 1 {
		return nil, ErrFrameTimerArgs
	}
	return &FrameTimer{now: now, ring: make([]FrameSample, capacity)}, nil
}

// Begin starts a frame.
func (t *FrameTimer) Begin() {
	if t == nil {
		return
	}
	now := t.now()
	t.cur = FrameSample{}
	if t.haveBegin {
		t.cur.Interval = now - t.prevBegin
	}
	t.prevBegin = now
	t.haveBegin = true
	t.begin = now
	t.last = now
	t.started = true
}

// Mark ends stage s: the time since the previous Begin or Mark is added
// to it. Outside a frame, or for an unknown stage, Mark does nothing.
func (t *FrameTimer) Mark(s Stage) {
	if t == nil || !t.started || s < 0 || s >= NumStages {
		return
	}
	now := t.now()
	t.cur.Stages[s] += now - t.last
	t.last = now
}

// End finishes the frame and records it. Outside a frame it does
// nothing.
func (t *FrameTimer) End() {
	if t == nil || !t.started {
		return
	}
	t.cur.Work = t.now() - t.begin
	t.ring[t.next] = t.cur
	t.next = (t.next + 1) % len(t.ring)
	if t.n < len(t.ring) {
		t.n++
	}
	t.started = false
}

// Abort drops the frame in progress (an error cut it short).
func (t *FrameTimer) Abort() {
	if t == nil {
		return
	}
	t.started = false
}

// Reset forgets every recorded frame. The next frame's Interval is
// zero again.
func (t *FrameTimer) Reset() {
	if t == nil {
		return
	}
	t.next, t.n = 0, 0
	t.started, t.haveBegin = false, false
}

// Len returns how many frames are recorded.
func (t *FrameTimer) Len() int {
	if t == nil {
		return 0
	}
	return t.n
}

// Samples returns the recorded frames, oldest first.
func (t *FrameTimer) Samples() []FrameSample {
	if t == nil || t.n == 0 {
		return nil
	}
	out := make([]FrameSample, 0, t.n)
	start := (t.next - t.n + len(t.ring)) % len(t.ring)
	for i := 0; i < t.n; i++ {
		out = append(out, t.ring[(start+i)%len(t.ring)])
	}
	return out
}

// Distribution summarises one series, in milliseconds.
type Distribution struct {
	Mean float64 `json:"mean_ms"`
	P50  float64 `json:"p50_ms"`
	P95  float64 `json:"p95_ms"`
	P99  float64 `json:"p99_ms"`
	Max  float64 `json:"max_ms"`
}

// StageDistribution is a [Distribution] labelled with its stage.
type StageDistribution struct {
	Stage string `json:"stage"`
	Distribution
}

// Summary summarises recorded frames.
type Summary struct {
	// Frames is the number of frames summarised.
	Frames int `json:"frames"`
	// FPS is 1 / mean Interval (frames with an Interval only).
	FPS float64 `json:"fps"`
	// Interval is the distribution of frame-to-frame wall time.
	Interval Distribution `json:"interval"`
	// Work is the distribution of Begin-to-End time.
	Work Distribution `json:"work"`
	// Stages is the distribution of each stage, in [Stage] order.
	Stages []StageDistribution `json:"stages"`
}

// Summarize computes the per-stage and whole-frame distributions of
// samples.
func Summarize(samples []FrameSample) Summary {
	s := Summary{Frames: len(samples), Stages: make([]StageDistribution, NumStages)}
	vals := make([]float64, 0, len(samples))
	series := func(get func(FrameSample) (float64, bool)) Distribution {
		vals = vals[:0]
		for _, f := range samples {
			if v, ok := get(f); ok {
				vals = append(vals, v)
			}
		}
		return distribution(vals)
	}
	s.Interval = series(func(f FrameSample) (float64, bool) { return f.Interval, f.Interval > 0 })
	if s.Interval.Mean > 0 {
		s.FPS = 1000 / s.Interval.Mean
	}
	s.Work = series(func(f FrameSample) (float64, bool) { return f.Work, true })
	for st := Stage(0); st < NumStages; st++ {
		s.Stages[st] = StageDistribution{
			Stage:        st.String(),
			Distribution: series(func(f FrameSample) (float64, bool) { return f.Stages[st], true }),
		}
	}
	return s
}

// distribution summarises vals (seconds) in milliseconds. It sorts
// vals in place. Percentiles use the nearest-rank method.
func distribution(vals []float64) Distribution {
	if len(vals) == 0 {
		return Distribution{}
	}
	slices.Sort(vals)
	sum := 0.0
	for _, v := range vals {
		sum += v
	}
	rank := func(p int) float64 {
		// nearest rank: ceil(p/100 * n), 1-based; >= 1 for every p >= 1
		// and n >= 1.
		i := (p*len(vals) + 99) / 100
		return vals[i-1] * 1000
	}
	return Distribution{
		Mean: sum / float64(len(vals)) * 1000,
		P50:  rank(50),
		P95:  rank(95),
		P99:  rank(99),
		Max:  vals[len(vals)-1] * 1000,
	}
}
