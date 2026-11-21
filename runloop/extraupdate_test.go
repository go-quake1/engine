// Copyright (c) 2026 the go-quake1/engine authors.
// SPDX-License-Identifier: GPL-2.0-or-later

package runloop

import (
	"testing"
	"time"

	"github.com/go-quake1/engine/backend"
	"github.com/go-quake1/engine/sound"
)

// stallBackend is a dmaBackend whose PresentFrame waits for the page's
// acknowledgement for stall seconds, in slices: the output keeps playing during
// the wait, and each slice runs idle (WinQuake's S_ExtraUpdate) when it is set,
// as wasipresent.Presenter does between two polls of ready-to-present.
type stallBackend struct {
	*dmaBackend
	stall, slice float64
	idle         func() error
	err          error
}

func (s *stallBackend) PresentFrame(rgba []byte, w, h int) error {
	if s.stall > 0 {
		for waited := 0.0; waited < s.stall; waited += s.slice {
			s.dmaBackend.advance(s.slice) // the output plays through the wait
			if s.idle != nil {
				if err := s.idle(); err != nil {
					return err
				}
			}
		}
	}
	return s.dmaBackend.PresentFrame(rgba, w, h)
}

var _ backend.QueuedAudio = (*stallBackend)(nil)

func mixRunnerPool(t *testing.T) (*sound.Pool, []sound.StereoSample) {
	t.Helper()
	pool, err := sound.NewPool(8)
	if err != nil {
		t.Fatal(err)
	}
	return pool, make([]sound.StereoSample, sound.MixBufferStereoFrames)
}

// stallRun runs 60 tics of 1/30 s, then one whose present waits stall seconds
// for the page, and returns the output's underruns during that tic.
func stallRun(t *testing.T, stall float64, extra bool) (underruns int, queued float64) {
	t.Helper()
	d := &dmaBackend{Recorder: backend.NewRecorder(0, 0)}
	sb := &stallBackend{dmaBackend: d, slice: 0.002}
	r, _ := newRunner(t, sb)
	pool, mixBuf := mixRunnerPool(t)
	r.SoundPool, r.MixBuffer = pool, mixBuf
	if extra {
		sb.idle = r.ExtraUpdate
	}
	now := 0.0
	step := func(dt float64) {
		d.advance(dt)
		now += dt
		if err := r.RunFrame(float32(dt), float32(now)); err != nil {
			t.Fatal(err)
		}
	}
	for range 60 {
		step(1.0 / 30)
	}
	d.underruns = 0
	sb.stall = stall
	step(1.0 / 30)
	return d.underruns, d.queued
}

// The reference's slow tic: a present that waits 250 ms for the page, on the
// 0.1 s queue the update before left. With S_ExtraUpdate between the slices of
// the wait the output is refilled to the mix-ahead each time and never runs dry;
// without it (the port before this one) the queue is gone 100 ms into the wait.
func TestExtraUpdateKeepsTheOutputFedThrough250msOfAckStall(t *testing.T) {
	if u, q := stallRun(t, 0.25, true); u != 0 {
		t.Errorf("with S_ExtraUpdate in the wait: %d dry writes (%.0f frames queued after), want 0", u, q)
	}
	// The control: the same stall with no update during it runs the output dry.
	if u, _ := stallRun(t, 0.25, false); u == 0 {
		t.Error("the same stall without S_ExtraUpdate did not run the output dry: the test measures nothing")
	}
}

// snd_noextraupdate (snd_dma.c line 851): set, S_ExtraUpdate does nothing.
func TestExtraUpdateHonoursNoExtraUpdate(t *testing.T) {
	r, d := mixRunner(t)
	now := 0.0
	for range 10 {
		d.advance(1.0 / 30)
		now += 1.0 / 30
		if err := r.RunFrame(float32(1.0/30), float32(now)); err != nil {
			t.Fatal(err)
		}
	}
	d.advance(0.05)
	r.NoExtraUpdate = true
	before := d.offered
	if err := r.ExtraUpdate(); err != nil || d.offered != before {
		t.Errorf("snd_noextraupdate set: err %v, %d frames painted", err, d.offered-before)
	}
	r.NoExtraUpdate = false
	if err := r.ExtraUpdate(); err != nil || d.offered == before {
		t.Errorf("snd_noextraupdate clear: err %v, %d frames painted, want the 50 ms played refilled", err, d.offered-before)
	}
}

// Before the first update (S_Update_ does nothing until the sound is started)
// and on a backend that does not report its queue, ExtraUpdate paints nothing.
func TestExtraUpdateBeforeTheFirstUpdateIsNothing(t *testing.T) {
	r, d := mixRunner(t)
	if err := r.ExtraUpdate(); err != nil || d.offered != 0 {
		t.Errorf("before the first tic: err %v, %d frames", err, d.offered)
	}
}

// What decides an underrun is the longest interval between two consecutive refill
// ATTEMPTS: TakeRefillGap says it and the sites of its two ends, then forgets it,
// and a refill that paints nothing still closes an interval.
func TestTakeRefillGapNamesTheLongestInterval(t *testing.T) {
	d := &dmaBackend{Recorder: backend.NewRecorder(0, 0)}
	r, _ := newRunner(t, d)
	pool, mixBuf := mixRunnerPool(t)
	r.SoundPool, r.MixBuffer = pool, mixBuf
	var now time.Time
	now = time.Unix(1000, 0)
	r.Clock = func() time.Time { return now }
	for i := 0; i < 3; i++ {
		d.advance(1.0 / 30)
		if err := r.RunFrame(1.0/30, float32(i+1)/30); err != nil {
			t.Fatal(err)
		}
	}
	r.TakeRefillGap()
	at := func(after time.Duration, site string) {
		now = now.Add(after)
		if err := r.ExtraUpdateAt(site); err != nil {
			t.Fatal(err)
		}
	}
	at(0, "a")
	at(10*time.Millisecond, "b")
	at(60*time.Millisecond, "c")
	at(5*time.Millisecond, "d")
	got, from, to := r.TakeRefillGap()
	if got != 60*time.Millisecond || from != "b" || to != "c" {
		t.Errorf("the longest interval = %v from %q to %q, want 60ms from b to c", got, from, to)
	}
	if got, from, to := r.TakeRefillGap(); got != 0 || from != "" || to != "" {
		t.Errorf("not forgotten once taken: %v %q %q", got, from, to)
	}
}
