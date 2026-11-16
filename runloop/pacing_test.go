// Copyright (c) 2026 the go-quake1/engine authors.
// SPDX-License-Identifier: GPL-2.0-or-later

package runloop

import (
	"testing"

	"github.com/go-quake1/engine/backend"
	"github.com/go-quake1/engine/sound"
)

// dmaBackend is an audio output that plays in real time: between two tics
// the test drains it by the tic's length (what SNDDMA_GetDMAPos reports in
// tyrquake), and QueuedFrames says how much is still queued. A runloop that
// paces its mixer reads that; one that does not paints a fixed 512 frames a
// tic whatever the tic's length (the defect this file measures).
type dmaBackend struct {
	*backend.Recorder
	queued    float64 // mix frames waiting to be played
	offered   int     // mix frames handed to QueueAudio so far
	played    float64
	underruns int // tics at which the output ran dry while the game was running
	maxQueued float64
}

func (d *dmaBackend) QueueAudio(s []sound.StereoSample) error {
	d.queued += float64(len(s))
	d.offered += len(s)
	if d.queued > d.maxQueued {
		d.maxQueued = d.queued
	}
	return d.Recorder.QueueAudio(s)
}

func (d *dmaBackend) QueuedFrames() int { return int(d.queued) }

// advance plays dt seconds of what is queued.
func (d *dmaBackend) advance(dt float64) {
	want := dt * sound.DefaultSampleRate
	if want > d.queued {
		d.underruns++
		want = d.queued
	}
	d.queued -= want
	d.played += want
}

// pacedRun runs RunFrame for tics of the given lengths (seconds) on a runner
// whose backend is a dmaBackend, and returns the backend and the simulated
// seconds. The mixer has a sound pool but nothing is playing: the frames
// painted are what is measured, not their content.
func pacedRun(t *testing.T, dts []float64) (*dmaBackend, float64) {
	t.Helper()
	d := &dmaBackend{Recorder: backend.NewRecorder(0, 0)}
	r, _ := newRunner(t, d)
	pool, err := sound.NewPool(8)
	if err != nil {
		t.Fatal(err)
	}
	r.SoundPool = pool
	r.MixBuffer = make([]sound.StereoSample, sound.MixBufferStereoFrames)
	now, elapsed := 0.0, 0.0
	for _, dt := range dts {
		d.advance(dt) // real time passes, the output plays
		now += dt
		elapsed += dt
		if err := r.RunFrame(float32(dt), float32(now)); err != nil {
			t.Fatal(err)
		}
	}
	return d, elapsed
}

// tics is n tics cycling through lengths that are not a multiple of anything:
// 60 Hz, a 30 Hz hiccup, 144 Hz, a long 33 ms tic, a tiny one.
func tics(n int) []float64 {
	cycle := []float64{1.0 / 60, 1.0 / 30, 1.0 / 144, 0.033, 0.0005, 1.0 / 60, 1.0 / 75}
	out := make([]float64, n)
	for i := range out {
		out[i] = cycle[i%len(cycle)]
	}
	return out
}

// TestMixerPacing_RealTimeRatio is the measurement of the defect: the number
// of mix frames handed to the output per second of game time, over the rate
// the output consumes them (11025 a second). Real time is 1.0. The unpaced
// runloop paints 512 frames a tic: 2.8 at 60 tics a second, and 1.0 only by
// coincidence at 21.5 tics a second.
func TestMixerPacing_RealTimeRatio(t *testing.T) {
	for _, c := range []struct {
		name string
		dts  []float64
	}{
		{"60 Hz", repeat(1.0/60, 600)},
		{"144 Hz", repeat(1.0/144, 1440)},
		{"irregular", tics(900)},
	} {
		d, elapsed := pacedRun(t, c.dts)
		ratio := float64(d.offered) / (elapsed * sound.DefaultSampleRate)
		t.Logf("%s: %d frames offered in %.2f s of game time: real-time ratio %.3f (the output consumed %.0f)", c.name, d.offered, elapsed, ratio, d.played)
		// Within the mix-ahead of exact: the paced mixer offers what the clock
		// owes plus at most one lead of prefill.
		if ratio < 0.98 || ratio > 1.0+0.1/elapsed+0.02 { // 0.1 s: the mix-ahead cap
			t.Errorf("%s: real-time ratio %.3f, want 1.0 (frames painted per second of game time over the output rate)", c.name, ratio)
		}
	}
}

func repeat(v float64, n int) []float64 {
	out := make([]float64, n)
	for i := range out {
		out[i] = v
	}
	return out
}
