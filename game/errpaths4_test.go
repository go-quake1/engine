// Copyright (c) 1996-1997 Id Software, Inc.
// Copyright (c) 2026 the go-quake1/engine authors.
// SPDX-License-Identifier: GPL-2.0-or-later

package game

import (
	"testing"

	"github.com/go-quake1/engine/embedpak"
)

// ---- loadBoltModels: bolt1 alias success + per-loop mdl.Load error ----

func TestLoadBoltModelsAltAndErr(t *testing.T) {
	pakFS, err := embedpak.OpenAsFS()
	if err != nil {
		t.Skipf("no real pak (%v)", err)
	}
	// Grab a real, valid .mdl blob to stand in for bolt1.mdl.
	good, ok := tryReadPakFile(pakFS, "progs/bolt.mdl")
	if !ok {
		t.Skip("no progs/bolt.mdl in pak")
	}

	// A pak with a VALID bolt1.mdl alias (slot 0 success branch +
	// startIdx=1 skip), a garbage bolt2.mdl (loop mdl.Load error), and a
	// valid bolt3.mdl.
	m := memFS{
		"progs/bolt1.mdl": good,
		"progs/bolt2.mdl": []byte("garbage"),
		"progs/bolt3.mdl": good,
	}
	models, _, loaded := loadBoltModels(m)
	if models[0] == nil {
		t.Fatal("bolt1 alias should populate slot 0")
	}
	if models[1] != nil {
		t.Fatal("garbage bolt2 should leave slot 1 nil")
	}
	if loaded < 2 {
		t.Fatalf("expected slots 0 and 3 loaded, got %d", loaded)
	}
}

// ---- WAD overlay: WAD parses but the lump is missing (Open lump miss) ----

func TestWADOverlayLumpMiss(t *testing.T) {
	pakFS, err := embedpak.OpenAsFS()
	if err != nil {
		t.Skipf("no real pak (%v)", err)
	}
	o := newWADOverlay(pakFS, "gfx.wad")
	// gfx.wad parses, but no lump named "definitely_not_a_lump" exists,
	// so w.Open(lump) errors -> the overlay returns the original base
	// error.
	if _, err := o.Open("gfx/definitely_not_a_lump.lmp"); err == nil {
		t.Fatal("missing WAD lump must surface the base error")
	}
}
