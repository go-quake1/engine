// Copyright (c) 2026 the go-quake1/engine authors.
// SPDX-License-Identifier: GPL-2.0-or-later

package wasisurface

import (
	"errors"
	"testing"
	"time"

	"github.com/go-quake1/engine/backend"
	"github.com/go-quake1/engine/sound"
)

// fakeOut is an AudioOut: a stream that plays in real time on the test's clock,
// as wctr_audio_out's does once the page plays; waiting makes it accept and
// discard, as before the page's first gesture.
type fakeOut struct {
	clock   *time.Time
	last    time.Time
	queued  float64
	cap     int
	waiting bool
	err     error
	delay   time.Duration // the host call's time

	writes   int
	pcm      []float32 // everything queued (not the discarded)
	accepted int
	closed   int
}

func (f *fakeOut) Write(pcm []float32) (int, int, error) {
	f.writes++
	time.Sleep(f.delay)
	if f.err != nil {
		return 0, 0, f.err
	}
	now := *f.clock
	f.queued -= now.Sub(f.last).Seconds() * sound.DefaultSampleRate
	if f.queued < 0 {
		f.queued = 0
	}
	f.last = now
	frames := len(pcm) / 2
	if f.waiting {
		return frames, 0, nil
	}
	room := f.cap - int(f.queued)
	n := frames
	if n > room {
		n = room
	}
	f.pcm = append(f.pcm, pcm[:2*n]...)
	f.queued += float64(n)
	f.accepted += n
	return n, int(f.queued), nil
}

func (f *fakeOut) Close() { f.closed++ }

func newAudioBackend(out AudioOut, clock *time.Time) *Backend {
	b := New(640, 400, nil, nil)
	b.now = func() time.Time { return *clock }
	b.start = *clock
	return b.WithAudio(out)
}

var _ backend.QueuedAudio = (*Backend)(nil)

// One write a tic, int16 over 32768, and the host's answer is the queue.
func TestQueueAudioConvertsAndWritesOnce(t *testing.T) {
	clock := time.Unix(1000, 0)
	out := &fakeOut{clock: &clock, last: clock, cap: 1024}
	b := newAudioBackend(out, &clock)
	mix := []sound.StereoSample{{L: 0, R: 0}, {L: 16384, R: -16384}, {L: 32767, R: -32768}}
	if err := b.QueueAudio(mix); err != nil {
		t.Fatal(err)
	}
	if out.writes != 1 {
		t.Errorf("%d host calls for one tic, want 1", out.writes)
	}
	want := []float32{0, 0, 0.5, -0.5, 32767.0 / 32768, -1}
	if len(out.pcm) != len(want) {
		t.Fatalf("pcm %v", out.pcm)
	}
	for i := range want {
		if out.pcm[i] != want[i] {
			t.Errorf("sample %d = %v, want %v", i, out.pcm[i], want[i])
		}
	}
	for _, v := range out.pcm {
		if v < -1 || v > 1 {
			t.Errorf("sample %v outside [-1, 1]", v)
		}
	}
	if q := b.QueuedFrames(); q != 3 {
		t.Errorf("queued %d, want 3", q)
	}
	if b.SampleRate() != sound.DefaultSampleRate {
		t.Errorf("SampleRate %d with an output, want the mixer's %d", b.SampleRate(), sound.DefaultSampleRate)
	}
	if st := b.AudioStats(); st.Offered != 3 || st.Accepted != 3 || st.Silent {
		t.Errorf("stats %+v", st)
	}
}

// TakeWriteTime is the host time of the writes since the last call: the
// diagnostic of a slow tic (stream_write), taken once.
func TestTakeWriteTime(t *testing.T) {
	clock := time.Unix(1000, 0)
	out := &fakeOut{clock: &clock, last: clock, cap: 1024, delay: 20 * time.Millisecond}
	b := newAudioBackend(out, &clock)
	if d := b.TakeWriteTime(); d != 0 {
		t.Errorf("%v before any write", d)
	}
	b.QueueAudio(make([]sound.StereoSample, 10))
	b.QueueAudio(make([]sound.StereoSample, 10))
	if d := b.TakeWriteTime(); d < 40*time.Millisecond {
		t.Errorf("two writes of 20 ms took %v", d)
	}
	if d := b.TakeWriteTime(); d != 0 {
		t.Errorf("%v on the second take: not reset", d)
	}
}

// QueuedFrames follows the clock between writes, never below 0, and never
// asks the host (one call a tic).
func TestQueuedFramesFollowsTheClock(t *testing.T) {
	clock := time.Unix(1000, 0)
	out := &fakeOut{clock: &clock, last: clock, cap: 4096}
	b := newAudioBackend(out, &clock)
	b.QueueAudio(make([]sound.StereoSample, 1100)) // 0.0998 s of mix
	if q := b.QueuedFrames(); q != 1100 {
		t.Fatalf("queued %d", q)
	}
	clock = clock.Add(50 * time.Millisecond) // 551 frames play
	if q := b.QueuedFrames(); q < 548 || q > 550 {
		t.Errorf("after 50 ms: %d queued, want about 549", q)
	}
	clock = clock.Add(time.Second)
	if q := b.QueuedFrames(); q != 0 {
		t.Errorf("after a second: %d queued, want 0", q)
	}
	if out.writes != 1 {
		t.Errorf("QueuedFrames called the host: %d writes", out.writes)
	}
}

// A partial accept is counted and the queue is the host's.
func TestPartialAccept(t *testing.T) {
	clock := time.Unix(1000, 0)
	out := &fakeOut{clock: &clock, last: clock, cap: 100}
	b := newAudioBackend(out, &clock)
	b.QueueAudio(make([]sound.StereoSample, 160))
	if st := b.AudioStats(); st.Offered != 160 || st.Accepted != 100 {
		t.Errorf("stats %+v, want 160 offered, 100 accepted", st)
	}
	if q := b.QueuedFrames(); q != 100 {
		t.Errorf("queued %d, want the host's 100", q)
	}
}

// Before the page plays the host accepts and discards, queue 0: the backend
// goes on writing (a tic's worth at a time under the pacing), nothing is kept.
func TestWaitingStream(t *testing.T) {
	clock := time.Unix(1000, 0)
	out := &fakeOut{clock: &clock, last: clock, cap: 1024, waiting: true}
	b := newAudioBackend(out, &clock)
	b.QueueAudio(make([]sound.StereoSample, 512))
	if q := b.QueuedFrames(); q != 0 {
		t.Errorf("a waiting stream reports %d queued", q)
	}
	if len(out.pcm) != 0 {
		t.Error("a waiting stream kept sound")
	}
	// The page starts playing: the next write is queued.
	out.waiting = false
	b.QueueAudio(make([]sound.StereoSample, 64))
	if q := b.QueuedFrames(); q != 64 {
		t.Errorf("queued %d after the page started, want 64", q)
	}
}

// A failing output is switched off: the game keeps running, silent, without an
// error, and the output is closed once.
func TestFailingOutputGoesSilent(t *testing.T) {
	clock := time.Unix(1000, 0)
	out := &fakeOut{clock: &clock, last: clock, cap: 1024, err: errors.New("stream closed")}
	b := newAudioBackend(out, &clock)
	if err := b.QueueAudio(make([]sound.StereoSample, 32)); err != nil {
		t.Fatalf("QueueAudio failed the frame: %v", err)
	}
	if err := b.QueueAudio(make([]sound.StereoSample, 32)); err != nil {
		t.Fatal(err)
	}
	if out.writes != 1 || out.closed != 1 {
		t.Errorf("%d writes and %d closes, want 1 and 1: a failed stream is not written again", out.writes, out.closed)
	}
	if st := b.AudioStats(); !st.Silent {
		t.Error("the backend still says it has sound")
	}
	if b.SampleRate() != 22050 {
		t.Errorf("SampleRate %d after switching off, want NoAudio's", b.SampleRate())
	}
}

// With no output at all (no grant, no import) the backend models a real-time one,
// so a silent game is paced and consumes sounds exactly as one with sound.
func TestSilentBackendModelsARealTimeOutput(t *testing.T) {
	clock := time.Unix(1000, 0)
	b := newAudioBackend(nil, &clock)
	if err := b.QueueAudio(make([]sound.StereoSample, 551)); err != nil {
		t.Fatal(err)
	}
	if q := b.QueuedFrames(); q != 551 {
		t.Fatalf("queued %d", q)
	}
	clock = clock.Add(25 * time.Millisecond) // 275 frames play
	if q := b.QueuedFrames(); q < 274 || q > 277 {
		t.Errorf("after 25 ms: %d queued, want about 276", q)
	}
	b.QueueAudio(make([]sound.StereoSample, 100))
	if q := b.QueuedFrames(); q < 374 || q > 377 {
		t.Errorf("queue %d after another 100, want about 376", q)
	}
	if st := b.AudioStats(); !st.Silent || st.Offered != 651 || st.Accepted != 0 {
		t.Errorf("stats %+v", st)
	}
	if err := b.QueueAudio(nil); err != nil {
		t.Errorf("an empty mix: %v", err)
	}
}
