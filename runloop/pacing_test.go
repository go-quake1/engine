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

// mixRunner is a runner on a dmaBackend with a sound pool and a mix buffer.
func mixRunner(t *testing.T) (*Runner, *dmaBackend) {
	t.Helper()
	d := &dmaBackend{Recorder: backend.NewRecorder(0, 0)}
	r, _ := newRunner(t, d)
	pool, err := sound.NewPool(8)
	if err != nil {
		t.Fatal(err)
	}
	r.SoundPool = pool
	r.MixBuffer = make([]sound.StereoSample, sound.MixBufferStereoFrames)
	return r, d
}

// pacedRun runs RunFrame for tics of the given lengths (seconds) on a runner
// whose backend is a dmaBackend, and returns the backend and the simulated
// seconds. The mixer has a sound pool but nothing is playing: the frames
// painted are what is measured, not their content.
func pacedRun(t *testing.T, dts []float64) (*dmaBackend, float64) {
	t.Helper()
	r, d := mixRunner(t)
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

var mixAheadFrames = float64(MixAhead) * sound.DefaultSampleRate // 1102.5

// WinQuake's _snd_mixahead is 0.1 (snd_dma.c line 82).
func TestMixAheadIsTheReferences(t *testing.T) {
	if MixAhead != 0.1 {
		t.Fatalf("MixAhead = %v, want 0.1 (snd_dma.c: cvar_t _snd_mixahead = {\"_snd_mixahead\", \"0.1\", true})", MixAhead)
	}
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
		// Within the mix-ahead of exact: the mixer offers what the clock owes plus
		// the one mix-ahead it prefills.
		if ratio < 0.98 || ratio > 1.0+MixAhead/elapsed+0.02 {
			t.Errorf("%s: real-time ratio %.3f, want 1.0 (frames painted per second of game time over the output rate)", c.name, ratio)
		}
	}
}

// S_Update_ refills the queue to the mix-ahead at EVERY update: after each tic
// the output holds MixAhead of mix (what is left of it after the tic's time was
// played, repainted), never more, and a tic shorter than the mix-ahead never
// runs the output dry.
func TestMixerPacing_QueueIsRefilledToTheMixAheadEveryTic(t *testing.T) {
	r, d := mixRunner(t)
	now := 0.0
	for k, dt := range tics(900) {
		d.advance(dt)
		now += dt
		if err := r.RunFrame(float32(dt), float32(now)); err != nil {
			t.Fatal(err)
		}
		if math.Abs(d.queued-mixAheadFrames) > 1.5 {
			t.Fatalf("tic %d: %.1f frames queued after the update, want the mix-ahead %.1f", k, d.queued, mixAheadFrames)
		}
	}
	// The first tic found the output empty (nothing has been mixed yet): one
	// underrun by construction. No tic is longer than the mix-ahead.
	if d.underruns > 1 {
		t.Errorf("the output ran dry %d times", d.underruns)
	}
}

// The case of playground#63's runners: a 90 ms tic, on a game that otherwise
// runs at 30 frames a second. The reference (0.1 s mix-ahead) has 100 ms queued
// when the tic starts, so the output plays through it; the former 0.05 s lead
// ran dry 40 ms into it.
func TestMixerPacing_A90MsTicDoesNotRunTheOutputDry(t *testing.T) {
	r, d := mixRunner(t)
	now := 0.0
	step := func(dt float64) {
		d.advance(dt)
		now += dt
		if err := r.RunFrame(float32(dt), float32(now)); err != nil {
			t.Fatal(err)
		}
	}
	for _, dt := range repeat(1.0/30, 60) {
		step(dt)
	}
	d.underruns = 0
	step(0.09)
	if d.underruns != 0 {
		t.Errorf("a 90 ms tic ran the output dry (%d underruns): the queue held %.0f frames, the tic needed %.0f", d.underruns, mixAheadFrames, 0.09*sound.DefaultSampleRate)
	}
	if math.Abs(d.queued-mixAheadFrames) > 1.5 {
		t.Errorf("%.0f frames queued after the 90 ms tic, want the mix-ahead %.0f (the update refills it)", d.queued, mixAheadFrames)
	}
	// A tic longer than the mix-ahead is a gap of exactly its excess: 130 ms on
	// 100 ms of queue is 30 ms of silence, and the queue is refilled after it.
	d.underruns = 0
	step(0.13)
	if d.underruns != 1 {
		t.Errorf("a 130 ms tic: %d underruns, want 1", d.underruns)
	}
	if math.Abs(d.queued-mixAheadFrames) > 1.5 {
		t.Errorf("%.0f frames queued after the 130 ms tic, want the mix-ahead %.0f", d.queued, mixAheadFrames)
	}
}

// A stall (the page was in the background, a level loaded) is not a debt: the
// mixer does not burst to catch up, and the tic that ends it paints the
// mix-ahead and no more.
func TestMixerPacing_StallDoesNotBurst(t *testing.T) {
	r, d := mixRunner(t)
	now, biggest := 0.0, 0
	for k, dt := range append(append(repeat(1.0/60, 120), 2.5), repeat(1.0/60, 120)...) {
		d.advance(dt)
		now += dt
		before := d.offered
		if err := r.RunFrame(float32(dt), float32(now)); err != nil {
			t.Fatal(err)
		}
		biggest = max(biggest, d.offered-before)
		if d.queued > mixAheadFrames+1.5 {
			t.Fatalf("tic %d: %.0f frames queued, more than the mix-ahead", k, d.queued)
		}
	}
	if limit := int(mixAheadFrames) + 1; biggest > limit {
		t.Errorf("one tic painted %d frames, more than the mix-ahead %d: the stall was paid back in a burst", biggest, limit)
	}
	if math.Abs(d.queued-mixAheadFrames) > 1.5 {
		t.Errorf("the mix-ahead was not refilled after the stall: %.0f queued", d.queued)
	}
}

// The reference runs at most two server ticks of 0.05 s in a frame (id's
// Host_FilterTime clamps the frame to 0.1 s: WinQuake/host.c): a 0.4 s frame, or
// an 8 s stall, runs two and drops the rest, never the eight it used to.
func TestSimCatchUpIsTheReferences(t *testing.T) {
	if maxSimSubsteps != 2 {
		t.Fatalf("maxSimSubsteps = %d, want 2 (0.1 s of 0.05 s ticks)", maxSimSubsteps)
	}
	d := &dmaBackend{Recorder: backend.NewRecorder(0, 0)}
	r, _ := newRunner(t, d)
	for _, dt := range []float32{0.12, 0.4, 8} {
		r.simAccumulator = 0
		if err := r.RunFrame(dt, 1); err != nil {
			t.Fatal(err)
		}
		if r.LastSimSteps != 2 {
			t.Errorf("a %v s frame ran %d server ticks, want 2", dt, r.LastSimSteps)
		}
		if r.simAccumulator != 0 {
			t.Errorf("a %v s frame kept %v s of backlog: the excess is dropped", dt, r.simAccumulator)
		}
	}
	// The frame after a slow one is not a catch-up of it.
	if err := r.RunFrame(0.05, 2); err != nil {
		t.Fatal(err)
	}
	if r.LastSimSteps != 1 {
		t.Errorf("the next 0.05 s frame ran %d ticks, want 1", r.LastSimSteps)
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
	}{{0.01, 0}, {0.045, 0}, {0.055, 1}, {0.09, 1}, {0.11, 2}, {10, maxSimSubsteps}} {
		r.simAccumulator = 0
		if err := r.RunFrame(c.dt, 1); err != nil {
			t.Fatal(err)
		}
		if r.LastSimSteps != c.want {
			t.Errorf("dt %v: %d server ticks, want %d", c.dt, r.LastSimSteps, c.want)
		}
	}
}

// The tics that outlast the queue are counted with the time they left the
// output with nothing to play: offered + MixDropped is the whole of the game's
// time (elapsed * rate), up to the mix-ahead the first tic prefills.
func TestMixerPacing_StallsAccountForTheRatio(t *testing.T) {
	dts := append(append(append(repeat(1.0/30, 90), 0.35), repeat(1.0/30, 300)...), 0.2)
	r, d := mixRunner(t)
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
	// Each stall left the output dry for its excess over the 0.1 s the queue held.
	if r.MixStalls != 2 {
		t.Errorf("%d stalled tics, want 2 (0.35 s and 0.2 s against a queue of %.1f s)", r.MixStalls, MixAhead)
	}
	want := (0.35 - MixAhead + 0.2 - MixAhead) * sound.DefaultSampleRate
	if got := float64(r.MixDropped); got < want*0.98 || got > want*1.02 {
		t.Errorf("%d mix frames dropped, want about %.0f (the stalls' excess over the queue)", r.MixDropped, want)
	}
	total := now * sound.DefaultSampleRate
	got := float64(d.offered + r.MixDropped)
	if over := got - total; over < -float64(len(dts)) || over > mixAheadFrames+float64(len(dts)) {
		t.Errorf("offered %d + dropped %d = %.0f, the game's time is %.0f frames: %.0f apart", d.offered, r.MixDropped, got, total, over)
	}
	// And without a stall nothing is dropped.
	r2, r2d := mixRunner(t)
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

// _snd_mixahead is a cvar: a runner given another value refills to that.
func TestMixerPacing_MixAheadIsTheCvar(t *testing.T) {
	r, d := mixRunner(t)
	r.MixAhead = 0.25
	now := 0.0
	for _, dt := range repeat(1.0/30, 30) {
		d.advance(dt)
		now += dt
		if err := r.RunFrame(float32(dt), float32(now)); err != nil {
			t.Fatal(err)
		}
	}
	if want := 0.25 * sound.DefaultSampleRate; math.Abs(d.queued-want) > 1.5 {
		t.Errorf("%.0f frames queued with _snd_mixahead 0.25, want %.0f", d.queued, want)
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
	// Paced, with a clock that stands still (dt clamps to 1/240 s), the mix-ahead
	// is filled once and then only the clock's 46 frames a tic follow.
	if d.offered >= 41*sound.MixBufferStereoFrames/2 {
		t.Errorf("%d frames offered in 41 tics: the mixer ran unpaced under RunUntilQuit", d.offered)
	}
	if d.offered > int(mixAheadFrames)+41*47 {
		t.Errorf("%d frames offered, more than the mix-ahead plus the clock", d.offered)
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
	if d.offered < 1100 { // the whole mix-ahead, from an empty queue
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
