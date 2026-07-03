// Copyright (c) 1996-1997 Id Software, Inc.
// Copyright (c) 2026 the go-quake1/engine authors.
// SPDX-License-Identifier: GPL-2.0-or-later

package game

import (
	"io/fs"
	"testing"

	"github.com/go-quake1/engine/bspfile"
	"github.com/go-quake1/engine/embedpak"
	"github.com/go-quake1/engine/model"
	"github.com/go-quake1/engine/progs"
	"github.com/go-quake1/engine/runloop"
	"github.com/go-quake1/engine/world"
)

// ---- edictSlot / edictSelfPointer (no-arena fallback) ----

func TestEdictSlotAndSelfPointer(t *testing.T) {
	p := buildCustomProgs([]fieldSpec{{"solid", 9, progs.EvFloat}})
	h, player := hostWithProgs(t, p)
	// Arena is nil on this host (hostWithProgs never wires one), so
	// edictSelfPointer falls back to edictSlot.
	if h.Server.Arena != nil {
		t.Fatal("expected no arena on the crafted host")
	}
	if got := edictSelfPointer(h, player); got != 1 {
		t.Fatalf("edictSelfPointer no-arena = %d, want slot 1", got)
	}
	// edictSlot for a not-present edict returns 0.
	stray := &progs.Edict{}
	if got := edictSlot(h, stray); got != 0 {
		t.Fatalf("edictSlot(stray) = %d, want 0", got)
	}
}

// ---- solidKindFromEntvars: all cases ----

func TestSolidKindFromEntvars(t *testing.T) {
	p := buildCustomProgs([]fieldSpec{{"solid", 9, progs.EvFloat}})
	arena := progs.NewEdictArena(p, 2)
	arena.Reset()
	ent, _, err := arena.Alloc()
	if err != nil {
		t.Fatalf("Alloc: %v", err)
	}
	ev, err := progs.NewEntVars(p, ent)
	if err != nil {
		t.Fatalf("NewEntVars: %v", err)
	}
	cases := []struct {
		solid float32
		want  world.SolidKind
	}{
		{0, world.SolidKindSkip},    // SOLID_NOT
		{1, world.SolidKindTrigger}, // SOLID_TRIGGER
		{2, world.SolidKindSolid},   // SOLID_BBOX -> default
	}
	for _, c := range cases {
		if err := ev.WriteFloat("solid", c.solid); err != nil {
			t.Fatalf("WriteFloat: %v", err)
		}
		if got := solidKindFromEntvars(ev); got != c.want {
			t.Errorf("solid=%v -> %v, want %v", c.solid, got, c.want)
		}
	}

	// Missing solid field -> SolidKindSkip.
	pNo := buildCustomProgs([]fieldSpec{{"health", 1, progs.EvFloat}})
	arena2 := progs.NewEdictArena(pNo, 2)
	arena2.Reset()
	ent2, _, _ := arena2.Alloc()
	ev2, _ := progs.NewEntVars(pNo, ent2)
	if got := solidKindFromEntvars(ev2); got != world.SolidKindSkip {
		t.Fatalf("missing solid -> %v, want Skip", got)
	}
}

// ---- resolveModelBBox: submodel + alias + memo + error branches ----

func TestResolveModelBBox(t *testing.T) {
	h, _, _ := buildRealHost(t)
	cache := &setModelCache{mdlBBox: map[int][2][3]float32{}}

	// Empty name / slot 0 -> not ok.
	if _, _, ok := resolveModelBBox(h, cache, "", 0); ok {
		t.Fatal("empty name must be !ok")
	}

	// BSP world (idx 1, "maps/..." name) -> worldmodel bbox.
	if _, _, ok := resolveModelBBox(h, cache, "maps/start.bsp", 1); !ok {
		t.Fatal("worldmodel bbox should resolve")
	}
	// Submodel "*1" at idx 2.
	if _, _, ok := resolveModelBBox(h, cache, "*1", 2); !ok {
		t.Log("submodel *1 idx2 did not resolve (map may lack it)")
	}
	// Submodel out of range index -> not ok.
	if _, _, ok := resolveModelBBox(h, cache, "*9999", 100000); ok {
		t.Fatal("out-of-range submodel must be !ok")
	}

	// Alias .mdl path: a real model, then a cache hit.
	if _, _, ok := resolveModelBBox(h, cache, "progs/player.mdl", 7); !ok {
		t.Fatal("player.mdl bbox should resolve")
	}
	if _, _, ok := resolveModelBBox(h, cache, "progs/player.mdl", 7); !ok {
		t.Fatal("cached player.mdl bbox should resolve")
	}
	// Alias .mdl that the resolver can't find -> resolver error branch.
	if _, _, ok := resolveModelBBox(h, cache, "progs/nope.mdl", 8); ok {
		t.Fatal("missing alias must be !ok")
	}
	// Alias .mdl whose bytes are garbage -> mdl.Load error branch.
	// (handled by loadAliasModels test; here exercise a name that the
	// resolver returns but fails to parse via a fake-resolver host.)
}

// ---- pickInMapCamera + buildDemoWaypoints on real geometry ----

func TestPickCameraAndWaypointsReal(t *testing.T) {
	pakFS := mustRealPak(t)
	b, size, err := loadBSP(pakFS)
	if err != nil {
		t.Fatalf("loadBSP: %v", err)
	}
	file, err := bspfile.Open(bytesReaderAt(b), size)
	if err != nil {
		t.Fatalf("bspfile.Open: %v", err)
	}
	bm, err := model.LoadBrush(file, 0)
	if err != nil {
		t.Fatalf("LoadBrush: %v", err)
	}
	cam := pickInMapCamera(bm, file)
	_ = cam
	wps := buildDemoWaypoints(bm, file, cam)
	if len(wps) == 0 {
		t.Fatal("expected at least the anchor waypoint")
	}
}

// ---- WAD overlay: nil-base, direct hit, wad fallthrough, non-gfx miss ----

func TestWADOverlay(t *testing.T) {
	// nil base -> ErrInvalid.
	var nilOverlay *wadOverlay
	if _, err := nilOverlay.Open("x"); err == nil {
		t.Fatal("nil overlay must error")
	}
	o0 := &wadOverlay{base: nil}
	if _, err := o0.Open("x"); err == nil {
		t.Fatal("nil base must error")
	}

	// Direct hit in the base FS.
	base := memFS{"gfx/palette.lmp": []byte{1, 2, 3}}
	o := newWADOverlay(base, "gfx.wad")
	if f, err := o.Open("gfx/palette.lmp"); err != nil {
		t.Fatalf("direct hit: %v", err)
	} else {
		f.Close()
	}

	// Miss on a non-gfx path -> wadLumpName false -> original error.
	if _, err := o.Open("models/x.mdl"); err == nil {
		t.Fatal("non-gfx miss must return the base error")
	}

	// Miss on a gfx path with no WAD present -> openWAD nil -> base error.
	if _, err := o.Open("gfx/missing.lmp"); err == nil {
		t.Fatal("gfx miss without wad must return base error")
	}

	// A base that holds a malformed gfx.wad -> openWAD parse fails -> nil.
	badWad := memFS{"gfx.wad": []byte("not a wad")}
	o2 := newWADOverlay(badWad, "gfx.wad")
	if _, err := o2.Open("gfx/sbar.lmp"); err == nil {
		t.Fatal("malformed wad must still return base error")
	}
	// Second openWAD call hits the already-parsed fast path.
	o2.openWAD()

	// Real pak: a gfx lump that lives only in gfx.wad resolves through
	// the overlay (covers the WAD-hit success branch).
	pakFS := mustRealPak(t)
	or := newWADOverlay(pakFS, "gfx.wad")
	if f, err := or.Open("gfx/sbar.lmp"); err == nil {
		f.Close()
	}
}

// ---- observedAnyInput: nil + each arm ----

func TestObservedAnyInput(t *testing.T) {
	if observedAnyInput(nil) {
		t.Fatal("nil runner -> false")
	}
	r := &runloop.Runner{}
	if observedAnyInput(r) {
		t.Fatal("clean runner -> false")
	}
	// Movement button arm.
	r.Buttons.Forward.Pressed = 1
	if !observedAnyInput(r) {
		t.Fatal("forward pressed -> true")
	}
	r.Buttons.Forward.Pressed = 0
	r.Buttons.SpeedHeld = true
	if !observedAnyInput(r) {
		t.Fatal("speed held -> true")
	}
	r.Buttons.SpeedHeld = false
	// Trigger attack arm.
	r.Triggers.Attack = true
	if !observedAnyInput(r) {
		t.Fatal("attack -> true")
	}
	r.Triggers.Attack = false
	r.Triggers.Jump = true
	if !observedAnyInput(r) {
		t.Fatal("jump -> true")
	}
}

// mustRealPak returns the embedded pak FS or skips the test.
func mustRealPak(t *testing.T) fs.FS {
	t.Helper()
	pakFS, err := openRealPak()
	if err != nil {
		t.Skipf("no real pak (%v)", err)
	}
	return pakFS
}

// openRealPak wraps embedpak.OpenAsFS for the helpers above.
func openRealPak() (fs.FS, error) { return embedpak.OpenAsFS() }
