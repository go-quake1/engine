// Copyright (c) 1996-1997 Id Software, Inc.
// Copyright (c) 2026 the go-quake1/engine authors.
// SPDX-License-Identifier: GPL-2.0-or-later

package game

import (
	"bytes"
	"errors"
	"io"
	"testing"

	"github.com/go-quake1/engine/embedpak"
	enginehost "github.com/go-quake1/engine/host"
	"github.com/go-quake1/engine/model"
	"github.com/go-quake1/engine/progs"
)

// fieldSpec names a field, its offset (in 4-byte words), and its type.
type fieldSpec struct {
	name string
	ofs  uint16
	typ  progs.Etype
}

// buildCustomProgs hand-builds a *progs.Progs exposing exactly the given
// fields. EntityFields is sized generously so every present field write
// lands in range.
func buildCustomProgs(fields []fieldSpec) *progs.Progs {
	strs := []byte{0}
	add := func(s string) int32 {
		ofs := int32(len(strs))
		strs = append(strs, []byte(s)...)
		strs = append(strs, 0)
		return ofs
	}
	defs := make([]progs.Def, 0, len(fields))
	for _, f := range fields {
		defs = append(defs, progs.Def{
			Type:  uint16(f.typ),
			Ofs:   f.ofs,
			SName: add(f.name),
		})
	}
	return &progs.Progs{
		Header:    progs.Header{EntityFields: 128},
		Strings:   strs,
		FieldDefs: defs,
	}
}

// hostWithProgs returns a host whose Progs() is p and whose
// Server.Edicts holds [world, player] (both allocated from a matching
// arena). Used to drive writePlayerOrigin / initPlayerForPhysicsWalk
// against crafted field sets so every EntVars error branch fires.
func hostWithProgs(t *testing.T, p *progs.Progs) (*enginehost.Host, *progs.Edict) {
	t.Helper()
	pakFS, err := embedpak.OpenAsFS()
	if err != nil {
		t.Skipf("no real pak (%v)", err)
	}
	rb, _ := tryReadPakFile(pakFS, "progs.dat")
	realP, err := progs.Load(bytes.NewReader(rb), int64(len(rb)))
	if err != nil {
		t.Fatalf("progs.Load: %v", err)
	}
	vm := progs.NewVM(realP)
	cache := model.NewCache()
	res := func(string) (int64, io.ReaderAt, error) { return 0, nil, io.EOF }
	h, err := enginehost.NewHost(vm, cache, res, 1)
	if err != nil {
		t.Fatalf("NewHost: %v", err)
	}
	h.SetProgs(p)

	arena := progs.NewEdictArena(p, 4)
	arena.Reset()
	world, _, err := arena.Alloc()
	if err != nil {
		t.Fatalf("Alloc world: %v", err)
	}
	player, _, err := arena.Alloc()
	if err != nil {
		t.Fatalf("Alloc player: %v", err)
	}
	h.Server.Edicts = []*progs.Edict{world, player}
	return h, player
}

// allInitFields is the full field set initPlayerForPhysicsWalk writes,
// in write order. gravity is intentionally typed EvFloat here; tests
// vary it.
func allInitFields() []fieldSpec {
	return []fieldSpec{
		{"movetype", 8, progs.EvFloat},
		{"solid", 9, progs.EvFloat},
		{"mins", 10, progs.EvVector},
		{"maxs", 13, progs.EvVector},
		{"velocity", 16, progs.EvVector},
		{"v_angle", 19, progs.EvVector},
		{"flags", 22, progs.EvFloat},
		{"gravity", 23, progs.EvFloat},
		{"origin", 24, progs.EvVector},
	}
}

// TestWritePlayerOriginErrPaths covers every writePlayerOrigin guard.
func TestWritePlayerOriginErrPaths(t *testing.T) {
	full := allInitFields()

	// slot out of range (bare host, empty Edicts).
	hEmpty, _ := hostWithProgs(t, buildCustomProgs(full))
	hEmpty.Server.Edicts = nil
	if err := writePlayerOrigin(hEmpty, 0, [3]float32{1, 2, 3}); !errors.Is(err, enginehost.ErrNoEdict) {
		t.Fatalf("out-of-range slot: %v", err)
	}

	// ent == nil.
	hNil, _ := hostWithProgs(t, buildCustomProgs(full))
	hNil.Server.Edicts = []*progs.Edict{nil, nil}
	if err := writePlayerOrigin(hNil, 1, [3]float32{1, 2, 3}); !errors.Is(err, enginehost.ErrNoEdict) {
		t.Fatalf("nil edict: %v", err)
	}

	// p == nil: a host that never had a Progs bound, but with a real
	// edict in the slot.
	hNoProgs := hostNoProgs(t)
	if err := writePlayerOrigin(hNoProgs, 1, [3]float32{1, 2, 3}); !errors.Is(err, enginehost.ErrNoProgs) {
		t.Fatalf("nil progs: %v", err)
	}

	// NewEntVars err is not reachable for a valid (p, ent) pair; the
	// WriteVec3-error branch fires when "origin" is absent.
	hNoOrigin, _ := hostWithProgs(t, buildCustomProgs(full[:len(full)-1])) // drop origin
	if err := writePlayerOrigin(hNoOrigin, 1, [3]float32{1, 2, 3}); !errors.Is(err, progs.ErrFieldNotFound) {
		t.Fatalf("missing origin: %v", err)
	}

	// Success path.
	hOK, _ := hostWithProgs(t, buildCustomProgs(full))
	if err := writePlayerOrigin(hOK, 1, [3]float32{4, 5, 6}); err != nil {
		t.Fatalf("write origin OK: %v", err)
	}
}

// hostNoProgs builds a host with a real edict in slot 1 but no Progs
// bound (h.Progs() == nil) so the ErrNoProgs branch fires.
func hostNoProgs(t *testing.T) *enginehost.Host {
	t.Helper()
	pakFS, err := embedpak.OpenAsFS()
	if err != nil {
		t.Skipf("no real pak (%v)", err)
	}
	rb, _ := tryReadPakFile(pakFS, "progs.dat")
	realP, _ := progs.Load(bytes.NewReader(rb), int64(len(rb)))
	vm := progs.NewVM(realP)
	cache := model.NewCache()
	res := func(string) (int64, io.ReaderAt, error) { return 0, nil, io.EOF }
	h, err := enginehost.NewHost(vm, cache, res, 1)
	if err != nil {
		t.Fatalf("NewHost: %v", err)
	}
	// Do NOT SetProgs -> h.Progs() stays nil.
	arena := progs.NewEdictArena(realP, 4)
	arena.Reset()
	_, _, _ = arena.Alloc()
	player, _, _ := arena.Alloc()
	h.Server.Edicts = []*progs.Edict{nil, player}
	return h
}

// TestInitPlayerErrPaths covers every initPlayerForPhysicsWalk guard +
// each per-field write error.
func TestInitPlayerErrPaths(t *testing.T) {
	full := allInitFields()

	// slot out of range.
	hEmpty, _ := hostWithProgs(t, buildCustomProgs(full))
	hEmpty.Server.Edicts = nil
	if err := initPlayerForPhysicsWalk(hEmpty, 0); !errors.Is(err, enginehost.ErrNoEdict) {
		t.Fatalf("out-of-range slot: %v", err)
	}
	// ent nil.
	hNil, _ := hostWithProgs(t, buildCustomProgs(full))
	hNil.Server.Edicts = []*progs.Edict{nil, nil}
	if err := initPlayerForPhysicsWalk(hNil, 1); !errors.Is(err, enginehost.ErrNoEdict) {
		t.Fatalf("nil edict: %v", err)
	}
	// p nil.
	if err := initPlayerForPhysicsWalk(hostNoProgs(t), 1); !errors.Is(err, enginehost.ErrNoProgs) {
		t.Fatalf("nil progs: %v", err)
	}

	// Each per-field write error: drop one field at a time so the write
	// up to it succeeds and that field's write returns ErrFieldNotFound.
	// (velocity/v_angle/origin offsets are HIGHER than the preceding
	// writes so dropping just that field still reaches its write.)
	for _, drop := range []string{"movetype", "solid", "mins", "maxs", "velocity", "v_angle", "flags"} {
		fields := dropField(full, drop)
		h, _ := hostWithProgs(t, buildCustomProgs(fields))
		if err := initPlayerForPhysicsWalk(h, 1); !errors.Is(err, progs.ErrFieldNotFound) {
			t.Fatalf("drop %q: want ErrFieldNotFound, got %v", drop, err)
		}
	}

	// gravity present but wrong type -> WriteFloat returns
	// ErrFieldTypeMismatch (not ErrFieldNotFound) -> the gravity branch
	// returns the error.
	bad := allInitFields()
	for i := range bad {
		if bad[i].name == "gravity" {
			bad[i].typ = progs.EvVector // wrong type for WriteFloat
		}
	}
	hGrav, _ := hostWithProgs(t, buildCustomProgs(bad))
	if err := initPlayerForPhysicsWalk(hGrav, 1); err == nil || errors.Is(err, progs.ErrFieldNotFound) {
		t.Fatalf("gravity type mismatch: want non-NotFound error, got %v", err)
	}

	// gravity absent -> tolerated (ErrFieldNotFound swallowed) -> success.
	noGrav := dropField(full, "gravity")
	hNoGrav, _ := hostWithProgs(t, buildCustomProgs(noGrav))
	if err := initPlayerForPhysicsWalk(hNoGrav, 1); err != nil {
		t.Fatalf("absent gravity should succeed: %v", err)
	}

	// Full set -> success (all writes land).
	hOK, _ := hostWithProgs(t, buildCustomProgs(full))
	if err := initPlayerForPhysicsWalk(hOK, 1); err != nil {
		t.Fatalf("full init should succeed: %v", err)
	}
}

func dropField(in []fieldSpec, name string) []fieldSpec {
	out := make([]fieldSpec, 0, len(in))
	for _, f := range in {
		if f.name == name {
			continue
		}
		out = append(out, f)
	}
	return out
}
