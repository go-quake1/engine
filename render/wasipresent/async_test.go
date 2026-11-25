// Copyright (c) 2026 the go-quake1/engine authors.
// SPDX-License-Identifier: GPL-2.0-or-later

package wasipresent

import (
	"errors"
	"testing"
	"time"

	"github.com/go-quake1/engine/render"
)

// simClock is a clock the test moves: a sleep and a blocking host call advance
// it, and an output that plays in real time drains with it.
type simClock struct {
	t      time.Time
	queued time.Duration // sound queued in the output
	dry    int           // times the output ran dry
}

func (c *simClock) advance(d time.Duration) {
	c.t = c.t.Add(d)
	if d > c.queued {
		c.dry++
		c.queued = 0
		return
	}
	c.queued -= d
}

// refill is S_ExtraUpdate: the mix is topped up to the mix-ahead (0.1 s).
func (c *simClock) refill() error {
	c.queued = 100 * time.Millisecond
	return nil
}

// stallDriver is a driver whose frame takes the browser stall to submit and
// present: a Draw blocks the guest for all of it, a BeginDraw hands it over and
// the frame is over once stall of the clock has passed.
type stallDriver struct {
	fakeDriver
	clock    *simClock
	stall    time.Duration
	asyncErr error // returned by End
	begins   int
	ends     int
	pending  *stallPending
}

func (s *stallDriver) Draw() error {
	s.clock.advance(s.stall) // the guest is held for the whole stall
	return s.fakeDriver.Draw()
}

type stallPending struct {
	d       *stallDriver
	startAt time.Time
}

func (s *stallDriver) BeginDraw() (PendingDraw, error) {
	if err := s.fail["begin"]; err != nil {
		return nil, err
	}
	s.begins++
	s.pending = &stallPending{d: s, startAt: s.clock.t}
	return s.pending, nil
}

func (p *stallPending) Ready() bool { return !p.d.clock.t.Before(p.startAt.Add(p.d.stall)) }

func (p *stallPending) End() error {
	p.d.ends++
	if !p.Ready() { // End waits for the host
		p.d.clock.advance(p.startAt.Add(p.d.stall).Sub(p.d.clock.t))
	}
	return p.d.asyncErr
}

func stallPresenter(t *testing.T, stall time.Duration) (*Presenter, *stallDriver, *simClock) {
	t.Helper()
	clock := &simClock{t: time.Unix(1000, 0), queued: 100 * time.Millisecond}
	drv := &stallDriver{clock: clock, stall: stall}
	p, err := New(drv, 8, 4)
	if err != nil {
		t.Fatal(err)
	}
	p.now = func() time.Time { return clock.t }
	p.sleep = clock.advance
	p.SetIdle(clock.refill)
	return p, drv, clock
}

// The browser holds the submit and the present for 250 ms, on the 0.1 s queue
// the update before left. The frame is begun and the sound is refilled in slices
// until the host is done: the output never runs dry. (The presenter before this
// one blocked in Draw for the whole stall: the queue was gone 100 ms into it.)
func TestAsyncDrawKeepsTheOutputFedThrough250msOfHostStall(t *testing.T) {
	p, drv, clock := stallPresenter(t, 250*time.Millisecond)
	px, pal := idleFrame()
	if err := p.Present(px, 8, 4, 8, pal); err != nil {
		t.Fatal(err)
	}
	if clock.dry != 0 {
		t.Errorf("the output ran dry %d times during a 250 ms frame", clock.dry)
	}
	if drv.begins != 1 || drv.ends != 1 || drv.draws != 0 {
		t.Errorf("%d begins, %d ends, %d blocking draws; want 1, 1, 0", drv.begins, drv.ends, drv.draws)
	}
	if got := p.LastPresent(); got.Slices != 125 {
		t.Errorf("%d idle slices, want 125 (250 ms in 2 ms slices)", got.Slices)
	}
}

// The control: the same stall through a driver that can only block (a Draw) runs
// the output dry, so the test above measures the change.
func TestBlockingDrawRunsTheOutputDryThrough250msOfHostStall(t *testing.T) {
	clock := &simClock{t: time.Unix(1000, 0), queued: 100 * time.Millisecond}
	drv := &blockingOnly{stallDriver: stallDriver{clock: clock, stall: 250 * time.Millisecond}}
	p, _ := New(drv, 8, 4)
	p.now = func() time.Time { return clock.t }
	p.sleep = clock.advance
	p.SetIdle(clock.refill)
	px, pal := idleFrame()
	if err := p.Present(px, 8, 4, 8, pal); err != nil {
		t.Fatal(err)
	}
	if clock.dry == 0 {
		t.Error("a blocking 250 ms Draw did not run the output dry: the test measures nothing")
	}
}

// blockingOnly hides BeginDraw: a Driver and nothing more.
type blockingOnly struct{ stallDriver }

func (blockingOnly) BeginDraw() {}

// A host that finishes at once costs no slice; one that never does costs MaxWait
// of slices and then End waits for it.
func TestAsyncDrawBounds(t *testing.T) {
	p, drv, _ := stallPresenter(t, 0)
	px, pal := idleFrame()
	if err := p.Present(px, 8, 4, 8, pal); err != nil || p.LastPresent().Slices != 0 || drv.ends != 1 {
		t.Fatalf("a frame over at once: %v, %d slices, %d ends", err, p.LastPresent().Slices, drv.ends)
	}
	p, drv, clock := stallPresenter(t, 5*time.Second)
	if err := p.Present(px, 8, 4, 8, pal); err != nil {
		t.Fatal(err)
	}
	if got := p.LastPresent().Slices; got != int(MaxWait/Slice) {
		t.Errorf("%d slices for a frame that never ends, want %d", got, int(MaxWait/Slice))
	}
	if drv.ends != 1 || clock.t != time.Unix(1005, 0) {
		t.Errorf("%d ends, clock at +%v: End must wait out the frame", drv.ends, clock.t.Sub(time.Unix(1000, 0)))
	}
}

// presentNoPanic is Present with a panic reported as an assertion, so a guard that
// is missing fails on what it was for and not by a crash.
func presentNoPanic(t *testing.T, what string, p *Presenter, px []byte, w, h, pitch int, pal *render.Palette) (err error) {
	t.Helper()
	defer func() {
		if r := recover(); r != nil {
			t.Errorf("%s: Present panicked: %v", what, r)
			err = errors.New("panic")
		}
	}()
	return p.Present(px, w, h, pitch, pal)
}

// The guards of Present: a bad frame is refused before anything is drawn, a
// driver that cannot say is never waited for, a begin that fails has no frame to
// collect.
func TestPresentGuards(t *testing.T) {
	px, pal := idleFrame()
	for _, c := range []struct {
		name        string
		w, h, pitch int
		pixels      []byte
		pal         *render.Palette
	}{
		{"zero width", 0, 4, 8, px, pal},
		{"zero height", 8, 0, 8, px, pal},
		{"pitch under width", 8, 4, 7, px, pal},
		{"no palette", 8, 4, 8, px, nil},
		{"short frame", 8, 4, 8, px[:8], pal},
	} {
		drv := &fakeDriver{}
		p, _ := New(drv, 8, 4)
		if err := presentNoPanic(t, c.name, p, c.pixels, c.w, c.h, c.pitch, c.pal); !errors.Is(err, ErrBadFrame) || drv.draws != 0 {
			t.Errorf("%s: %v, %d draws; want ErrBadFrame and none", c.name, err, drv.draws)
		}
	}
	plain := &fakeDriver{}
	p, _ := New(plain, 8, 4)
	p.SetIdle(func() error { return nil })
	if err := presentNoPanic(t, "a driver that cannot say", p, px, 8, 4, 8, pal); err != nil || plain.draws != 1 {
		t.Errorf("a driver that cannot say: %v, %d draws", err, plain.draws)
	}
	noIdle := &readyDriver{waits: 5}
	p, _ = New(noIdle, 8, 4)
	if err := presentNoPanic(t, "no idle hook", p, px, 8, 4, 8, pal); err != nil || noIdle.polls != 0 || noIdle.draws != 1 {
		t.Errorf("no idle hook: %v, %d polls, %d draws", err, noIdle.polls, noIdle.draws)
	}
	boom := errors.New("boom")
	p, drv, _ := stallPresenter(t, 250*time.Millisecond)
	drv.fail = map[string]error{"begin": boom}
	if err := presentNoPanic(t, "begin error", p, px, 8, 4, 8, pal); !errors.Is(err, boom) || drv.ends != 0 {
		t.Errorf("begin error: %v, %d ends", err, drv.ends)
	}
}

// The frame is collected whatever happens in the wait: an idle error ends it
// (End releases what it held) and comes back; a failed frame comes back from End;
// a begin that fails is the error, with nothing to collect.
func TestAsyncDrawErrors(t *testing.T) {
	px, pal := idleFrame()
	boom := errors.New("boom")

	p, drv, _ := stallPresenter(t, 250*time.Millisecond)
	p.SetIdle(func() error { return boom })
	if err := p.Present(px, 8, 4, 8, pal); !errors.Is(err, boom) || drv.ends != 1 {
		t.Errorf("idle error: %v, %d ends; want the error and the frame collected", err, drv.ends)
	}

	p, drv, _ = stallPresenter(t, 4*time.Millisecond)
	drv.asyncErr = boom
	if err := p.Present(px, 8, 4, 8, pal); !errors.Is(err, boom) {
		t.Errorf("failed frame: %v, want the error of End", err)
	}

	p, drv, _ = stallPresenter(t, 250*time.Millisecond)
	drv.fail = map[string]error{"begin": boom}
	if err := p.Present(px, 8, 4, 8, pal); !errors.Is(err, boom) || drv.ends != 0 {
		t.Errorf("begin error: %v, %d ends", err, drv.ends)
	}

	// With no idle hook there is nothing to do while the frame runs: the blocking
	// Draw is used.
	p, drv, _ = stallPresenter(t, 4*time.Millisecond)
	p.SetIdle(nil)
	if err := p.Present(px, 8, 4, 8, pal); err != nil || drv.begins != 0 || drv.draws != 1 {
		t.Errorf("no idle hook: %v, %d begins, %d draws; want the blocking Draw", err, drv.begins, drv.draws)
	}
}
