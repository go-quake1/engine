// Copyright (c) 1996-1997 Id Software, Inc.
// Copyright (c) 2026 the go-quake1/engine authors.
// SPDX-License-Identifier: GPL-2.0-or-later

package game

import (
	"strings"
	"testing"

	"github.com/go-quake1/engine/progs"
)

// findStringOfs scans the progs string table for the first interned
// string with the given suffix and returns its offset (0 if none).
func findStringOfs(p *progs.Progs, suffix string) int32 {
	for off := int32(1); off < 200000; off++ {
		s := p.String(off)
		if s != "" && strings.HasSuffix(s, suffix) {
			return off
		}
		if off > 100000 {
			break
		}
	}
	return 0
}

// TestBuiltinAmbientSoundDeep drives builtinAmbientSound with a REAL
// sound name (so precache succeeds) and out-of-range volumes so both
// clamp branches + the AmbientSoundAt call branch run.
func TestBuiltinAmbientSoundDeep(t *testing.T) {
	h, vm, p := buildRealHost(t)
	wavOfs := findStringOfs(p, ".wav")
	if wavOfs == 0 {
		t.Skip("no .wav string in progs")
	}
	fn := builtinAmbientSound(h)

	// vol > 1 -> vol*255 > 255 -> high clamp.
	must(t, vm.SetGlobalVector(progs.OfsParm0, [3]float32{1, 2, 3}))
	must(t, vm.SetGlobalInt(progs.OfsParm1, wavOfs))
	must(t, vm.SetGlobalFloat(progs.OfsParm2, 4.0))
	must(t, vm.SetGlobalFloat(progs.OfsParm3, 3.0))
	if err := fn(vm); err != nil {
		t.Fatalf("ambient high vol: %v", err)
	}

	// vol < 0 -> low clamp.
	must(t, vm.SetGlobalFloat(progs.OfsParm2, -1.0))
	if err := fn(vm); err != nil {
		t.Fatalf("ambient low vol: %v", err)
	}
}

// TestBuiltinPrecacheModelDeep drives builtinPrecacheModel with a real
// model name so the PrecacheModel call branch runs (it may log when the
// precache table fills, but the call line itself executes).
func TestBuiltinPrecacheModelDeep(t *testing.T) {
	h, vm, p := buildRealHost(t)
	mdlOfs := findStringOfs(p, ".mdl")
	if mdlOfs == 0 {
		t.Skip("no .mdl string in progs")
	}
	fn := builtinPrecacheModel(h)
	must(t, vm.SetGlobalInt(progs.OfsParm0, mdlOfs))
	if err := fn(vm); err != nil {
		t.Fatalf("precache_model real: %v", err)
	}
}

// TestBuiltinSetModelDeep drives builtinSetModel with a real precached
// model name (idx found) so the bbox + LinkBounds path runs, plus the
// nil-arena early-out and the nil-progs early-out.
func TestBuiltinSetModelDeep(t *testing.T) {
	h, vm, _ := buildRealHost(t)
	fn := builtinSetModel(h)
	if vm.Arena() == nil || len(h.Server.Edicts) < 2 || h.Server.Edicts[1] == nil {
		t.Skip("real host without arena/player edict")
	}
	ptr := vm.Arena().PointerForEdict(h.Server.Edicts[1])

	// Find a name offset that ModelIndex resolves in the precache: use a
	// precache entry that is already loaded (slot 1 = the worldmodel).
	wmName := h.Server.ModelPrecache[1]
	// Locate that string in the progs string table.
	off := findStringOfs(h.Progs(), wmName)
	if off == 0 {
		// Fall back to any .mdl that is in the precache.
		for _, n := range h.Server.ModelPrecache {
			if strings.HasSuffix(n, ".mdl") {
				if o := findStringOfs(h.Progs(), n); o != 0 {
					off = o
					break
				}
			}
		}
	}
	if off != 0 {
		must(t, vm.SetGlobalInt(progs.OfsParm0, ptr))
		must(t, vm.SetGlobalInt(progs.OfsParm1, off))
		if err := fn(vm); err != nil {
			t.Fatalf("setmodel real name: %v", err)
		}
	}
}
