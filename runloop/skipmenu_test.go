// Copyright (c) 2026 the go-quake1/engine authors.
// SPDX-License-Identifier: GPL-2.0-or-later

package runloop

import (
	"testing"

	"github.com/go-quake1/engine/backend"
	"github.com/go-quake1/engine/menu"
)

// TestSkipMenuRunsHostFromFirstTic: with the title menu up the host is
// frozen; after SkipMenu the very first RunFrame steps it, and the
// attract demo no longer takes the tic.
func TestSkipMenuRunsHostFromFirstTic(t *testing.T) {
	rec := backend.NewRecorder(0, 0)
	r, _ := newRunner(t, rec)
	r.Menu = menu.New()
	r.Demo = &Demo{}
	host := r.Host.(*fakeHost)

	if err := r.RunFrame(0.05, 1); err != nil {
		t.Fatalf("RunFrame (menu up): %v", err)
	}
	if host.calls != 0 {
		t.Fatalf("menu up: host.Frame calls = %d, want 0", host.calls)
	}

	r.SkipMenu()
	if r.Menu.Active() {
		t.Fatal("menu still active after SkipMenu")
	}
	if r.Demo != nil {
		t.Fatal("attract demo still wired after SkipMenu")
	}
	if err := r.RunFrame(0.05, 2); err != nil {
		t.Fatalf("RunFrame (skipped): %v", err)
	}
	if host.calls != 1 {
		t.Fatalf("after SkipMenu: host.Frame calls = %d, want 1", host.calls)
	}
}

func TestSkipMenuNilMenu(t *testing.T) {
	r, _ := newRunner(t, backend.NewRecorder(0, 0))
	r.Menu = nil
	r.SkipMenu() // must not panic
	if r.Menu != nil {
		t.Fatal("SkipMenu created a menu")
	}
}
