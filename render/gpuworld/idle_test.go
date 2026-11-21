// Copyright (c) 2026 the go-quake1/engine authors.
// SPDX-License-Identifier: GPL-2.0-or-later

package gpuworld

import (
	"errors"
	"testing"
	"time"
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
	if runs != checks {
		t.Errorf("Idle ran %d times, want once at each of the %d checks (every %d faces of %d) as the clock is always 5 ms on", runs, checks, idleFaces, len(faces))
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
