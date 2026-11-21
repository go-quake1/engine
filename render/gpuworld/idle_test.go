// Copyright (c) 2026 the go-quake1/engine authors.
// SPDX-License-Identifier: GPL-2.0-or-later

package gpuworld

import (
	"errors"
	"testing"
	"time"

	"github.com/go-quake1/engine/worldview"
)

// WinQuake calls S_ExtraUpdate inside the edge scan (r_edge.c:728): the frame's
// own CPU work must not leave the sound unfed. Draws runs Geometry.Idle between
// faces, when IdleEvery has passed on the clock since the last run (or the
// start); a frozen clock never makes it due; its error is returned after the
// frame's draw list is complete.
func TestDrawsRunsIdleBetweenFaces(t *testing.T) {
	w, cm := loadE1M1(t)
	g, err := Build(w, cm)
	if err != nil {
		t.Fatal(err)
	}
	views := w.AnimatedViews(1)
	if len(views) == 0 {
		t.Fatal("no view")
	}
	v := views[0]
	faces, err := w.Visible(v, 320, 200)
	if err != nil || len(faces) <= idleFaces {
		t.Fatalf("%d visible faces (%v): too few to hold two checks", len(faces), err)
	}
	checks := (len(faces)-1)/idleFaces + 1

	var now time.Time
	runs := 0
	g.Clock = func() time.Time { now = now.Add(5 * time.Millisecond); return now }
	g.IdleEvery = 2 * time.Millisecond
	g.Idle = func() error { runs++; return nil }
	plain, err := g.Draws(w, v, 320, 200, nil, Chosen)
	if err != nil {
		t.Fatal(err)
	}
	// One more than the checks: the one after the walk (below).
	if runs != checks+1 {
		t.Errorf("Idle ran %d times, want once at each of the %d checks (every %d faces of %d) and once after the walk, as the clock is always 5 ms on", runs, checks, idleFaces, len(faces))
	}

	// The walk's end feeds the sound even when nothing is drawn (r_edge.c:728 is
	// before the scan, whatever it finds): a view outside the map sees no face.
	runs = 0
	out := worldview.View{Origin: [3]float32{1e7, 1e7, 1e7}}
	if none, err := g.Draws(w, out, 320, 200, nil, Chosen); err != nil || len(none) != 0 || runs != 1 {
		t.Errorf("a view outside the map: %d draws (%v), Idle ran %d times, want 0 draws and 1 run", len(none), err, runs)
	}

	// A clock that does not move never makes it due.
	runs = 0
	frozen := time.Unix(1000, 0)
	g.Clock = func() time.Time { return frozen }
	if _, err := g.Draws(w, v, 320, 200, nil, Chosen); err != nil || runs != 0 {
		t.Errorf("a frozen clock: Idle ran %d times (%v), want 0", runs, err)
	}

	// The default slice: a clock that moves 1 ms a read runs it less often than
	// the 2 ms one asks for at every check.
	runs = 0
	g.IdleEvery = 0
	g.Clock = func() time.Time { now = now.Add(time.Millisecond); return now }
	if _, err := g.Draws(w, v, 320, 200, nil, Chosen); err != nil {
		t.Fatal(err)
	}
	if runs == 0 || runs >= checks {
		t.Errorf("with the default %v slice and 1 ms a read, Idle ran %d times out of %d checks, want some but not all", DefaultIdleEvery, runs, checks)
	}

	// Its error comes back, with the whole draw list.
	boom := errors.New("sound mix failed")
	g.IdleEvery = 2 * time.Millisecond
	g.Clock = func() time.Time { now = now.Add(5 * time.Millisecond); return now }
	g.Idle = func() error { return boom }
	got, err := g.Draws(w, v, 320, 200, nil, Chosen)
	if !errors.Is(err, boom) || len(got) != len(plain) {
		t.Errorf("a failing idle: %d draws (%v), want %d draws and the error", len(got), err, len(plain))
	}

	// Without it, nothing changes.
	g.Idle = nil
	if again, err := g.Draws(w, v, 320, 200, nil, Chosen); err != nil || len(again) != len(plain) {
		t.Errorf("no idle: %d draws (%v), want %d", len(again), err, len(plain))
	}
}

// Feed is what the entity loop and the pass recording call between items: it
// runs Idle once IdleEvery has passed since the last run, and never otherwise.
func TestFeedIsDueOnTheClock(t *testing.T) {
	var now time.Time
	runs := 0
	g := &Geometry{IdleEvery: 10 * time.Millisecond}
	g.Clock = func() time.Time { return now }
	g.Idle = func() error { runs++; return nil }
	g.fedAt = now
	for i := 0; i < 6; i++ {
		now = now.Add(4 * time.Millisecond)
		if err := g.Feed(); err != nil {
			t.Fatal(err)
		}
	}
	if runs != 2 {
		t.Errorf("24 ms in 4 ms steps with a 10 ms slice: Idle ran %d times, want 2", runs)
	}
	g.Idle = nil
	if err := g.Feed(); err != nil {
		t.Errorf("no idle: %v", err)
	}
}
