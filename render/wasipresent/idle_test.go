// Copyright (c) 2026 the go-quake1/engine authors.
// SPDX-License-Identifier: GPL-2.0-or-later

package wasipresent

import (
	"errors"
	"testing"
	"time"

	"github.com/go-quake1/engine/render"
)

// readyDriver is a fakeDriver that says it would wait for the page for the
// first waits calls of ReadyToPresent.
type readyDriver struct {
	fakeDriver
	waits int
	polls int
	err   error
}

func (r *readyDriver) ReadyToPresent() (bool, error) {
	r.polls++
	if r.err != nil {
		return false, r.err
	}
	return r.polls > r.waits, nil
}

func idleFrame() ([]byte, *render.Palette) {
	return make([]byte, 8*4), &render.Palette{}
}

// A frame that waits for the page's acknowledgement runs the idle work (WinQuake's
// S_ExtraUpdate) in slices until the page is ready, and draws only then: the
// old presenter blocked inside Draw and never ran it.
func TestPresentIdlesWhileThePageHasNotAcknowledged(t *testing.T) {
	drv := &readyDriver{waits: 125} // 125 slices of 2 ms: a 250 ms stall
	p, err := New(drv, 8, 4)
	if err != nil {
		t.Fatal(err)
	}
	var slept time.Duration
	p.sleep = func(d time.Duration) { slept += d }
	idles := 0
	p.SetIdle(func() error {
		if drv.draws != 0 {
			t.Error("idle ran after the draw")
		}
		idles++
		return nil
	})
	px, pal := idleFrame()
	if err := p.Present(px, 8, 4, 8, pal); err != nil {
		t.Fatal(err)
	}
	if idles != 125 || slept != 250*time.Millisecond {
		t.Errorf("%d idle slices over %v, want 125 over 250 ms", idles, slept)
	}
	if drv.draws != 1 {
		t.Errorf("%d draws, want 1", drv.draws)
	}
}

// A page that is ready at once costs no idle slice and no sleep.
func TestPresentReadyDoesNotIdle(t *testing.T) {
	drv := &readyDriver{}
	p, _ := New(drv, 8, 4)
	p.sleep = func(time.Duration) { t.Error("slept with the page ready") }
	p.SetIdle(func() error { t.Error("idle with the page ready"); return nil })
	px, pal := idleFrame()
	if err := p.Present(px, 8, 4, 8, pal); err != nil || drv.draws != 1 {
		t.Fatalf("err %v, %d draws", err, drv.draws)
	}
}

// The wait is bounded: a page that never acknowledges costs MaxWait of slices,
// then the frame is drawn (the host gives up itself after its own AckWait).
func TestPresentIdleIsBounded(t *testing.T) {
	drv := &readyDriver{waits: 1 << 30}
	p, _ := New(drv, 8, 4)
	var slept time.Duration
	p.sleep = func(d time.Duration) { slept += d }
	p.SetIdle(func() error { return nil })
	px, pal := idleFrame()
	if err := p.Present(px, 8, 4, 8, pal); err != nil {
		t.Fatal(err)
	}
	if slept != MaxWait || drv.draws != 1 {
		t.Errorf("slept %v, %d draws; want %v and 1", slept, drv.draws, MaxWait)
	}
}

// A driver that cannot say, no idle hook, or an error from either: no wait, or
// the error comes back and nothing is drawn.
func TestPresentIdleEdges(t *testing.T) {
	px, pal := idleFrame()
	plain := &fakeDriver{}
	p, _ := New(plain, 8, 4)
	p.SetIdle(func() error { t.Error("idle on a driver that cannot say"); return nil })
	if err := p.Present(px, 8, 4, 8, pal); err != nil || plain.draws != 1 {
		t.Fatalf("plain driver: %v, %d draws", err, plain.draws)
	}
	noIdle := &readyDriver{waits: 5}
	p, _ = New(noIdle, 8, 4)
	if err := p.Present(px, 8, 4, 8, pal); err != nil || noIdle.polls != 0 || noIdle.draws != 1 {
		t.Fatalf("no idle hook: %v, %d polls, %d draws", err, noIdle.polls, noIdle.draws)
	}
	boom := errors.New("boom")
	failIdle := &readyDriver{waits: 5}
	p, _ = New(failIdle, 8, 4)
	p.sleep = func(time.Duration) {}
	p.SetIdle(func() error { return boom })
	if err := p.Present(px, 8, 4, 8, pal); !errors.Is(err, boom) || failIdle.draws != 0 {
		t.Fatalf("idle error: %v, %d draws", err, failIdle.draws)
	}
	failPoll := &readyDriver{err: boom}
	p, _ = New(failPoll, 8, 4)
	p.SetIdle(func() error { return nil })
	if err := p.Present(px, 8, 4, 8, pal); !errors.Is(err, boom) || failPoll.draws != 0 {
		t.Fatalf("poll error: %v, %d draws", err, failPoll.draws)
	}
}
