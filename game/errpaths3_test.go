// Copyright (c) 1996-1997 Id Software, Inc.
// Copyright (c) 2026 the go-quake1/engine authors.
// SPDX-License-Identifier: GPL-2.0-or-later

package game

import (
	"testing"

	enginesound "github.com/go-quake1/engine/sound"

	"github.com/go-quake1/engine/progs"
)

// ---- dispatchPutClientInServer: every guard ----

func TestDispatchPutClientInServerGuards(t *testing.T) {
	// All-nil args.
	dispatchPutClientInServer(nil, nil, nil)

	// Host with < 2 edicts.
	p := buildCustomProgs([]fieldSpec{{"health", 1, progs.EvFloat}})
	hShort, _ := hostWithProgs(t, p)
	hShort.Server.Edicts = hShort.Server.Edicts[:1]
	dispatchPutClientInServer(hShort, hShort.VM, p)

	// Host with a nil player edict at slot 1.
	hNilPlayer, _ := hostWithProgs(t, p)
	hNilPlayer.Server.Edicts[1] = nil
	dispatchPutClientInServer(hNilPlayer, hNilPlayer.VM, p)

	// Host whose progs lacks PutClientInServer + SetNewParms -> the
	// "function not found" early-return path. The crafted progs has no
	// functions at all.
	hNoFn, _ := hostWithProgs(t, p)
	dispatchPutClientInServer(hNoFn, hNoFn.VM, p)
}

// ---- builtinAmbientSound: reserved <= 0 branch ----

func TestBuiltinAmbientSoundNoReserved(t *testing.T) {
	h, vm, p := buildRealHost(t)
	wavOfs := findStringOfs(p, ".wav")
	if wavOfs == 0 {
		t.Skip("no .wav string")
	}
	// Replace the pool with one that reserves no static channels so the
	// reserved<=0 early-return runs (precache still succeeds first).
	pool, err := enginesound.NewPool(0)
	if err != nil {
		t.Fatalf("NewPool(0): %v", err)
	}
	h.SetSoundPool(pool)
	fn := builtinAmbientSound(h)
	must(t, vm.SetGlobalVector(progs.OfsParm0, [3]float32{0, 0, 0}))
	must(t, vm.SetGlobalInt(progs.OfsParm1, wavOfs))
	must(t, vm.SetGlobalFloat(progs.OfsParm2, 0.5))
	must(t, vm.SetGlobalFloat(progs.OfsParm3, 3.0))
	if err := fn(vm); err != nil {
		t.Fatalf("ambient no-reserved: %v", err)
	}
}

// ---- resolveModelBBox: nil-worldmodel + nil-resolver branches ----

func TestResolveModelBBoxNilSources(t *testing.T) {
	p := buildCustomProgs([]fieldSpec{{"health", 1, progs.EvFloat}})
	h, _ := hostWithProgs(t, p)
	cache := &setModelCache{mdlBBox: map[int][2][3]float32{}}

	// BSP name with a nil WorldModel -> not ok.
	if _, _, ok := resolveModelBBox(h, cache, "maps/x.bsp", 1); ok {
		t.Fatal("nil worldmodel must be !ok")
	}
	// "*N" submodel with nil worldmodel -> not ok.
	if _, _, ok := resolveModelBBox(h, cache, "*1", 2); ok {
		t.Fatal("nil worldmodel submodel must be !ok")
	}
	// Alias .mdl with a nil resolver -> not ok.
	h.Resolver = nil
	if _, _, ok := resolveModelBBox(h, cache, "progs/x.mdl", 3); ok {
		t.Fatal("nil resolver must be !ok")
	}
}
