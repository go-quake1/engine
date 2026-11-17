// Copyright (c) 2026 the go-quake1/engine authors.
// SPDX-License-Identifier: GPL-2.0-or-later

package runloop

import (
	"errors"
	"math"
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
		if ratio < 0.98 || ratio > 1.0+MixLead/elapsed+0.02 { // plus the lead it prefills
			t.Errorf("%s: real-time ratio %.3f, want 1.0 (frames painted per second of game time over the output rate)", c.name, ratio)
		}
	}
}

// The frames written track elapsed*rate: at no tic is the total offered more
// than the lead above the clock, or more than one tic below it; the queue never
// holds more than the mix-ahead; and with tics shorter than the lead the output
// never runs dry after the first fill.
func TestMixerPacing_TracksTheClock(t *testing.T) {
	dts := tics(900)
	d := &dmaBackend{Recorder: backend.NewRecorder(0, 0)}
	r, _ := newRunner(t, d)
	pool, _ := sound.NewPool(8)
	r.SoundPool, r.MixBuffer = pool, make([]sound.StereoSample, sound.MixBufferStereoFrames)
	now, maxTic := 0.0, 0.0
	for k, dt := range dts {
		d.advance(dt)
		now += dt
		if dt > maxTic {
			maxTic = dt
		}
		if err := r.RunFrame(float32(dt), float32(now)); err != nil {
			t.Fatal(err)
		}
		over := float64(d.offered) - now*sound.DefaultSampleRate
		if over > MixLead*sound.DefaultSampleRate+1 {
			t.Fatalf("tic %d: %0.f frames ahead of the clock, more than the %.0f-frame lead", k, over, MixLead*sound.DefaultSampleRate)
		}
		if -over > maxTic*sound.DefaultSampleRate+1 {
			t.Fatalf("tic %d: %0.f frames behind the clock, more than one tic (%.0f)", k, -over, maxTic*sound.DefaultSampleRate)
		}
		if d.queued > MixAhead*sound.DefaultSampleRate+1 {
			t.Fatalf("tic %d: %.0f frames queued, more than the %.0f-frame mix-ahead", k, d.queued, MixAhead*sound.DefaultSampleRate)
		}
	}
	// The first tic found the output empty (no sound has been mixed yet): one
	// underrun by construction. Tics of at most 33 ms never starve it again.
	if d.underruns > 1 {
		t.Errorf("the output ran dry %d times", d.underruns)
	}
}

// A stall (the page was in the background, a level loaded) is not a debt: the
// mixer does not burst to catch up, and never queues past the mix-ahead.
func TestMixerPacing_StallDoesNotBurst(t *testing.T) {
	dts := append(append(repeat(1.0/60, 120), 2.5), repeat(1.0/60, 120)...)
	d := &dmaBackend{Recorder: backend.NewRecorder(0, 0)}
	r, _ := newRunner(t, d)
	pool, _ := sound.NewPool(8)
	r.SoundPool, r.MixBuffer = pool, make([]sound.StereoSample, sound.MixBufferStereoFrames)
	now, biggest, atStall := 0.0, 0, 0
	for k, dt := range dts {
		d.advance(dt)
		now += dt
		before := d.offered
		if err := r.RunFrame(float32(dt), float32(now)); err != nil {
			t.Fatal(err)
		}
		n := d.offered - before
		biggest = max(biggest, n)
		if dt == 2.5 {
			atStall = n
		}
		if d.queued > MixAheadMax*sound.DefaultSampleRate+1 {
			t.Fatalf("tic %d: %.0f frames queued after the stall", k, d.queued)
		}
	}
	// The tic that ends the stall is not paid back: at most the mix-ahead it had
	// been running with (the slow-frame allowance only grows for the tics after).
	aheadFrames, maxFrames := MixAhead*sound.DefaultSampleRate, MixAheadMax*sound.DefaultSampleRate
	if limit := int(aheadFrames - 0.5); atStall > limit+1 {
		t.Errorf("the stalled tic painted %d frames, more than the mix-ahead %d: the stall was paid back in a burst", atStall, limit)
	}
	// And no tic ever paints more than the most the mixer may run ahead.
	if limit := int(maxFrames); biggest > limit {
		t.Errorf("one tic painted %d frames, more than MixAheadMax %d", biggest, limit)
	}
	// After the stall the output is refilled to the lead and the clock resumes.
	if d.queued < MixLead*sound.DefaultSampleRate/2 {
		t.Errorf("the lead was not refilled after the stall: %.0f queued", d.queued)
	}
}

// The CI shape of go-wasmcontainers/playground#63 at 27 frames a second: 35 ms
// tics with a spike of 0.1 to 0.3 s about once a second. The queue is kept at
// the allowance the spikes raise, so the output runs dry on a small fraction of
// the spikes (only one bigger than 1.5 times the recent biggest escapes); with
// the allowance off it runs dry on every spike.
func TestMixerPacing_SpikesDoNotUnderrun(t *testing.T) {
	var dts []float64
	seed := uint32(12345)
	rnd := func() float64 { seed = seed*1664525 + 1013904223; return float64(seed>>8) / float64(1<<24) }
	spikes := 0
	for len(dts) < 3000 {
		for i := 0; i < 20+int(rnd()*20); i++ {
			dts = append(dts, 0.035)
		}
		dts = append(dts, 0.1+0.2*rnd())
		spikes++
	}
	run := func(limit float32) (underruns int) {
		d := &dmaBackend{Recorder: backend.NewRecorder(0, 0)}
		r, _ := newRunner(t, d)
		pool, _ := sound.NewPool(8)
		r.SoundPool, r.MixBuffer = pool, make([]sound.StereoSample, sound.MixBufferStereoFrames)
		r.MixAheadMax = limit
		now := 0.0
		for k, dt := range dts {
			d.advance(dt)
			now += dt
			if k == 300 {
				d.underruns = 0
			}
			if err := r.RunFrame(float32(dt), float32(now)); err != nil {
				t.Fatal(err)
			}
		}
		return d.underruns
	}
	got, control := run(0), run(MixAhead)
	t.Logf("%d spikes: %d underruns with the allowance, %d without", spikes, got, control)
	if control < spikes/2 {
		t.Errorf("the control ran dry only %d times for %d spikes: the test cannot see the defect", control, spikes)
	}
	if got*5 > control {
		t.Errorf("%d underruns with the allowance against %d without: not enough better", got, control)
	}
}

// LastSimSteps is how many server ticks the last frame ran: a slow frame's
// catch-up shows as several, and the cap bounds it.
func TestLastSimSteps(t *testing.T) {
	d := &dmaBackend{Recorder: backend.NewRecorder(0, 0)}
	r, _ := newRunner(t, d)
	for _, c := range []struct {
		dt   float32
		want int
	}{{0.01, 0}, {0.045, 0}, {0.055, 1}, {0.16, 3}, {10, maxSimSubsteps}} {
		r.simAccumulator = 0
		if err := r.RunFrame(c.dt, 1); err != nil {
			t.Fatal(err)
		}
		if r.LastSimSteps != c.want {
			t.Errorf("dt %v: %d server ticks, want %d", c.dt, r.LastSimSteps, c.want)
		}
	}
}

// Slow frames must not break the sound up: a tic longer than what the output
// has queued is a gap the listener hears. The runner of go-wasmcontainers/
// playground#63 ran Quake at 14 frames a second and its audio underran 204 times
// in 66 s with the fixed 0.1 s mix-ahead. The mixer runs ahead by what the
// slowest recent tic needs, so after a warm-up the output never runs dry; the
// control, with the allowance switched off (MixAheadMax = MixAhead), runs dry on
// every long tic.
func TestMixerPacing_SlowFramesDoNotUnderrun(t *testing.T) {
	steady := repeat(0.15, 400)
	jitter := make([]float64, 400)
	for i := range jitter {
		jitter[i] = []float64{0.07, 0.16, 0.07, 0.12}[i%4]
	}
	for _, c := range []struct {
		name string
		dts  []float64
	}{{"7 frames a second", steady}, {"jittery, 9.5 a second", jitter}} {
		run := func(limit float32) (underruns int, maxQueued float64) {
			d := &dmaBackend{Recorder: backend.NewRecorder(0, 0)}
			r, _ := newRunner(t, d)
			pool, _ := sound.NewPool(8)
			r.SoundPool, r.MixBuffer = pool, make([]sound.StereoSample, sound.MixBufferStereoFrames)
			r.MixAheadMax = limit
			now := 0.0
			for k, dt := range c.dts {
				d.advance(dt)
				now += dt
				if k == 100 {
					d.underruns = 0 // the warm-up: the allowance grows after the first slow tics
				}
				if err := r.RunFrame(float32(dt), float32(now)); err != nil {
					t.Fatal(err)
				}
			}
			return d.underruns, d.maxQueued
		}
		got, maxQ := run(0)
		if got != 0 {
			t.Errorf("%s: the output ran dry %d times in 300 tics after the warm-up", c.name, got)
		}
		if maxQ > MixAheadMax*sound.DefaultSampleRate+1 {
			t.Errorf("%s: %.0f frames queued, more than MixAheadMax", c.name, maxQ)
		}
		if control, _ := run(MixAhead); control < 100 {
			t.Errorf("%s: with the allowance off the output ran dry only %d times: the test cannot see the defect", c.name, control)
		}
	}
}

// A ratio under 1 is a game below real time when the stalls account for it:
// offered + MixDropped + what is still owed is the whole of the game's time
// (elapsed * rate), up to the lead the first tic prefills. The stalls are the
// tics longer than the mix-ahead, and each drops its excess over MixAhead.
func TestMixerPacing_StallsAccountForTheRatio(t *testing.T) {
	// The second stall comes after 10 s at 30 Hz: the allowance the first one
	// raised (slow frames, below) has been forgotten.
	dts := append(append(append(repeat(1.0/30, 90), 0.35), repeat(1.0/30, 300)...), 0.2)
	d := &dmaBackend{Recorder: backend.NewRecorder(0, 0)}
	r, _ := newRunner(t, d)
	pool, _ := sound.NewPool(8)
	r.SoundPool, r.MixBuffer = pool, make([]sound.StereoSample, sound.MixBufferStereoFrames)
	now := 0.0
	for _, dt := range dts {
		d.advance(dt)
		now += dt
		if err := r.RunFrame(float32(dt), float32(now)); err != nil {
			t.Fatal(err)
		}
	}
	// The tic lengths are counted against the bounds: the stalls are the only
	// tics over 0.1 s (the 0.2 s one is 0.2 s in float32, a hair over the bound),
	// none reaches 0.35 s, and the longest is the 0.35 s one.
	if r.MixTicsOver[0] != 2 || r.MixTicsOver[2] != 0 || r.MixTicsOver[3] != 0 || math.Abs(r.MixLongest-0.35) > 1e-6 {
		t.Errorf("tics over %v s: %v, longest %v; want 2 over 0.1, none over 0.35, longest 0.35", MixTicBounds, r.MixTicsOver, r.MixLongest)
	}
	if r.MixStalls != 2 {
		t.Errorf("%d stalled tics, want 2 (0.35 s and 0.2 s against a mix-ahead of %.1f s)", r.MixStalls, MixAhead)
	}
	want := (0.35 - MixAhead + 0.2 - MixAhead) * sound.DefaultSampleRate
	if got := float64(r.MixDropped); got < want*0.98 || got > want*1.02 {
		t.Errorf("%d mix frames dropped, want about %.0f (the stalls' excess over the mix-ahead)", r.MixDropped, want)
	}
	total := now * sound.DefaultSampleRate
	got := float64(d.offered+r.MixDropped) + r.mixOwed
	if over := got - total; over < -float64(len(dts)) || over > MixLead*sound.DefaultSampleRate+float64(len(dts)) {
		t.Errorf("offered %d + dropped %d + owed %.0f = %.0f, the game's time is %.0f frames: %.0f apart", d.offered, r.MixDropped, r.mixOwed, got, total, over)
	}
	// And without a stall nothing is dropped.
	r2d := &dmaBackend{Recorder: backend.NewRecorder(0, 0)}
	r2, _ := newRunner(t, r2d)
	r2.SoundPool, r2.MixBuffer = pool, make([]sound.StereoSample, sound.MixBufferStereoFrames)
	for k, dt := range repeat(1.0/20, 100) {
		r2d.advance(dt)
		if err := r2.RunFrame(float32(dt), float32(float64(k+1)*dt)); err != nil {
			t.Fatal(err)
		}
	}
	if r2.MixStalls != 0 || r2.MixDropped != 0 {
		t.Errorf("20 tics a second dropped %d frames in %d stalls, want none", r2.MixDropped, r2.MixStalls)
	}
}

// RunUntilQuit wraps the backend in a quit observer: the pacing must survive
// the wrapper (a struct that embeds the Backend interface only promotes
// Backend's methods and would hide QueuedFrames, leaving the mixer unpaced in
// the real game loop).
func TestMixerPacing_SurvivesRunUntilQuit(t *testing.T) {
	rec := backend.NewRecorder(320, 200)
	d := &quitAfterDMA{dmaBackend: &dmaBackend{Recorder: rec}, framesUntilQuit: 40}
	r := newLoopRunner(t, rec)
	r.Backend = d
	r.SoundPool, _ = sound.NewPool(8)
	r.MixBuffer = make([]sound.StereoSample, sound.MixBufferStereoFrames)
	r.SimStep = MinFrameTime
	if err := r.RunUntilQuit(); err != nil {
		t.Fatal(err)
	}
	if got := len(rec.Audio); got == 0 {
		t.Fatal("no audio queued")
	}
	// Unpaced it would be 512 frames in every one of the 41 tics (20992).
	// Paced, with a clock that stands still (dt clamps to 1/240 s), the lead
	// is filled once and then only the clock's 46 frames a tic follow.
	if d.offered >= 41*sound.MixBufferStereoFrames/2 {
		t.Errorf("%d frames offered in 41 tics: the mixer ran unpaced under RunUntilQuit", d.offered)
	}
	lead := float64(MixLead)
	if d.offered > int(lead*sound.DefaultSampleRate)+41*47 {
		t.Errorf("%d frames offered, more than the lead plus the clock", d.offered)
	}
}

// quitAfterDMA is a dmaBackend that raises QuitRequested after n polls.
type quitAfterDMA struct {
	*dmaBackend
	framesUntilQuit int
	polls           int
}

func (q *quitAfterDMA) PollInput() (backend.InputSnapshot, error) {
	q.polls++
	if q.polls > q.framesUntilQuit {
		return backend.InputSnapshot{QuitRequested: true}, nil
	}
	return backend.InputSnapshot{}, nil
}

// A backend that cannot report its queue keeps the fixed chunk: one
// MixBufferStereoFrames per tic, exactly as before the pacing existed.
func TestMixerPacing_BackendWithoutQueueKeepsTheChunk(t *testing.T) {
	rec := backend.NewRecorder(0, 0)
	r, _ := newRunner(t, rec)
	pool, _ := sound.NewPool(8)
	r.SoundPool, r.MixBuffer = pool, make([]sound.StereoSample, sound.MixBufferStereoFrames)
	for k := 0; k < 5; k++ {
		if err := r.RunFrame(1.0/60, float32(k)); err != nil {
			t.Fatal(err)
		}
	}
	if len(rec.Audio) != 5 {
		t.Fatalf("%d QueueAudio calls for 5 tics", len(rec.Audio))
	}
	for k, a := range rec.Audio {
		if len(a) != sound.MixBufferStereoFrames {
			t.Errorf("tic %d queued %d frames, want %d", k, len(a), sound.MixBufferStereoFrames)
		}
	}
}

// ErrUnsupported from the output is ignored (the page may not play yet); any
// other error stops the frame, as in the unpaced path.
func TestMixerPacing_QueueAudioErrors(t *testing.T) {
	for _, c := range []struct {
		name    string
		err     error
		wantErr bool
	}{
		{"unsupported", backend.ErrUnsupported, false},
		{"other", errors.New("device lost"), true},
	} {
		d := &failingQueued{dmaBackend: &dmaBackend{Recorder: backend.NewRecorder(0, 0)}, err: c.err}
		r, _ := newRunner(t, d)
		pool, _ := sound.NewPool(8)
		r.SoundPool, r.MixBuffer = pool, make([]sound.StereoSample, sound.MixBufferStereoFrames)
		err := r.RunFrame(1.0/60, 1)
		if (err != nil) != c.wantErr {
			t.Errorf("%s: RunFrame error %v, want error %v", c.name, err, c.wantErr)
		}
	}
}

type failingQueued struct {
	*dmaBackend
	err error
}

func (f *failingQueued) QueueAudio(_ []sound.StereoSample) error { return f.err }

// A long tic is painted in batches no larger than the mix buffer (512 frames).
func TestMixerPacing_BatchesFitTheBuffer(t *testing.T) {
	d := &dmaBackend{Recorder: backend.NewRecorder(0, 0)}
	r, _ := newRunner(t, d)
	pool, _ := sound.NewPool(8)
	r.SoundPool, r.MixBuffer = pool, make([]sound.StereoSample, 128) // a small buffer
	if err := r.RunFrame(0.09, 1); err != nil {
		t.Fatal(err)
	}
	for k, a := range d.Recorder.Audio {
		if len(a) > 128 {
			t.Errorf("batch %d is %d frames, the buffer holds 128", k, len(a))
		}
	}
	if d.offered < 900 { // 0.09 s owed, and the lead
		t.Errorf("only %d frames painted for a 90 ms tic", d.offered)
	}
}

func repeat(v float64, n int) []float64 {
	out := make([]float64, n)
	for i := range out {
		out[i] = v
	}
	return out
}
