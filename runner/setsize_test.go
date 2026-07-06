// Copyright (c) 2026 the go-quake1/engine authors.
// SPDX-License-Identifier: BSD-3-Clause

package runner

import (
	"testing"

	enginehost "github.com/go-quake1/engine/host"
	"github.com/go-quake1/engine/progs"
	engineserver "github.com/go-quake1/engine/server"
	"github.com/go-quake1/engine/world"
)

// progsForSetSizeTest builds a minimal progs whose entity block carries
// origin / mins / maxs / size / solid, enough to drive builtinSetSize.
func progsForSetSizeTest() *progs.Progs {
	strs := []byte{0}
	addStr := func(s string) int32 {
		ofs := int32(len(strs))
		strs = append(strs, []byte(s)...)
		strs = append(strs, 0)
		return ofs
	}
	originName := addStr("origin")
	minsName := addStr("mins")
	maxsName := addStr("maxs")
	sizeName := addStr("size")
	solidName := addStr("solid")

	// Entity fields (slots): origin@1..3, mins@4..6, maxs@7..9,
	// size@10..12, solid@13. 16 slots = 64 bytes per edict.
	const entityFields = 16
	// Globals must cover OfsParm2's vec3 (slots 10..12); 32 slots is ample.
	globals := make([]byte, 32*4)
	return &progs.Progs{
		Header:  progs.Header{EntityFields: entityFields},
		Strings: strs,
		FieldDefs: []progs.Def{
			{Type: uint16(progs.EvVector), Ofs: 1, SName: originName},
			{Type: uint16(progs.EvVector), Ofs: 4, SName: minsName},
			{Type: uint16(progs.EvVector), Ofs: 7, SName: maxsName},
			{Type: uint16(progs.EvVector), Ofs: 10, SName: sizeName},
			{Type: uint16(progs.EvFloat), Ofs: 13, SName: solidName},
		},
		Globals:    globals,
		Functions:  []progs.Function{{FirstStatement: 0, SName: 0}},
		Statements: []progs.Statement{{Op: progs.OP_DONE}},
	}
}

func newSetSizeHost(t *testing.T, cap int) (*enginehost.Host, *progs.VM, *progs.Progs) {
	t.Helper()
	p := progsForSetSizeTest()
	arena := progs.NewEdictArena(p, cap)
	vm := progs.NewVM(p)
	vm.SetArena(arena)

	h := &enginehost.Host{Server: engineserver.NewServer(), World: world.New()}
	h.SetProgs(p)
	h.Server.Arena = arena
	h.Server.Edicts = make([]*progs.Edict, cap)
	for i := 0; i < cap; i++ {
		ed, err := arena.Get(i)
		if err != nil {
			t.Fatalf("arena.Get(%d): %v", i, err)
		}
		h.Server.Edicts[i] = ed
	}
	h.Server.NumEdicts = cap
	h.World.Clear([3]float32{-1024, -1024, -1024}, [3]float32{1024, 1024, 1024})
	return h, vm, p
}

// TestBuiltinSetSizeWritesBBoxAndLinksAtSlot is the regression guard for the
// two coupled bugs that let hitscan pass through every monster: setsize was a
// no-op (monsters never got a collision bbox or an area-tree entry), and the
// setmodel/setsize link keyed the tree by ResolvePointer's byte offset (always
// 0 for a self pointer) instead of the edict's arena slot -- collapsing every
// entity onto world.Key(0). This drives setsize for an entity at a NON-zero
// slot and asserts (a) the bbox/size are written and (b) the entity is queryable
// in the area tree at its own slot, not slot 0.
func TestBuiltinSetSizeWritesBBoxAndLinksAtSlot(t *testing.T) {
	const slot = 3
	h, vm, p := newSetSizeHost(t, 8)

	ev, err := progs.NewEntVars(p, h.Server.Edicts[slot])
	if err != nil {
		t.Fatalf("NewEntVars: %v", err)
	}
	if err := ev.WriteVec3("origin", [3]float32{100, 0, 0}); err != nil {
		t.Fatalf("write origin: %v", err)
	}
	if err := ev.WriteFloat("solid", float32(engineserver.SolidSlideBox)); err != nil {
		t.Fatalf("write solid: %v", err)
	}

	mins := [3]float32{-16, -16, -24}
	maxs := [3]float32{16, 16, 40}
	if err := vm.SetGlobalInt(progs.OfsParm0, h.Server.Arena.MakePointer(slot, 0)); err != nil {
		t.Fatalf("set parm0: %v", err)
	}
	if err := vm.SetGlobalVector(progs.OfsParm1, mins); err != nil {
		t.Fatalf("set parm1: %v", err)
	}
	if err := vm.SetGlobalVector(progs.OfsParm2, maxs); err != nil {
		t.Fatalf("set parm2: %v", err)
	}

	if err := builtinSetSize(h, func(string, ...any) {})(vm); err != nil {
		t.Fatalf("builtinSetSize: %v", err)
	}

	// (a) bbox + size written back onto the edict.
	if got, _ := ev.ReadVec3("mins"); got != mins {
		t.Errorf("mins = %v, want %v", got, mins)
	}
	if got, _ := ev.ReadVec3("maxs"); got != maxs {
		t.Errorf("maxs = %v, want %v", got, maxs)
	}
	if got, _ := ev.ReadVec3("size"); got != ([3]float32{32, 32, 64}) {
		t.Errorf("size = %v, want [32 32 64]", got)
	}

	// (b) queryable in the area tree at its OWN slot, around its origin.
	keys := h.World.AreaQuery([3]float32{80, -20, -30}, [3]float32{120, 20, 44}, world.QuerySolidsOnly)
	found := false
	for _, k := range keys {
		if int(k) == slot {
			found = true
		}
		if int(k) == 0 {
			t.Errorf("entity collapsed onto world.Key(0) -- the ResolvePointer-offset regression")
		}
	}
	if !found {
		t.Errorf("AreaQuery around the entity origin = %v, want it to contain slot %d", keys, slot)
	}
}

// TestBuiltinSetSizeSolidNotUnlinks confirms a SOLID_NOT entity is not left as
// a solid in the area tree (kind = skip), matching vanilla SV_LinkEdict.
func TestBuiltinSetSizeSolidNotUnlinks(t *testing.T) {
	const slot = 2
	h, vm, p := newSetSizeHost(t, 8)
	ev, err := progs.NewEntVars(p, h.Server.Edicts[slot])
	if err != nil {
		t.Fatalf("NewEntVars: %v", err)
	}
	_ = ev.WriteVec3("origin", [3]float32{0, 0, 0})
	_ = ev.WriteFloat("solid", float32(engineserver.SolidNot))
	_ = vm.SetGlobalInt(progs.OfsParm0, h.Server.Arena.MakePointer(slot, 0))
	_ = vm.SetGlobalVector(progs.OfsParm1, [3]float32{-8, -8, -8})
	_ = vm.SetGlobalVector(progs.OfsParm2, [3]float32{8, 8, 8})

	if err := builtinSetSize(h, func(string, ...any) {})(vm); err != nil {
		t.Fatalf("builtinSetSize: %v", err)
	}
	keys := h.World.AreaQuery([3]float32{-16, -16, -16}, [3]float32{16, 16, 16}, world.QuerySolidsOnly)
	for _, k := range keys {
		if int(k) == slot {
			t.Errorf("SOLID_NOT entity should not be a solid in the area tree; got slot %d in %v", slot, keys)
		}
	}
}

// TestBuiltinCheckClient covers checkclient(): world (0) when no client is
// active, and the active client's edict pointer otherwise -- the value QC
// FindTarget needs so monsters can see the player (a no-op here left every
// monster asleep).
func TestBuiltinCheckClient(t *testing.T) {
	p := progsForSetSizeTest()
	const capN = 8
	arena := progs.NewEdictArena(p, capN)
	vm := progs.NewVM(p)
	vm.SetArena(arena)

	h := &enginehost.Host{
		Server: engineserver.NewServer(),
		World:  world.New(),
		Static: engineserver.NewStatic(4),
	}
	h.SetProgs(p)
	h.Server.Arena = arena
	h.Server.Edicts = make([]*progs.Edict, capN)
	for i := 0; i < capN; i++ {
		ed, err := arena.Get(i)
		if err != nil {
			t.Fatalf("arena.Get(%d): %v", i, err)
		}
		h.Server.Edicts[i] = ed
	}
	fn := builtinCheckClient(h)

	// No active client -> world (0).
	if err := fn(vm); err != nil {
		t.Fatalf("checkclient (no client): %v", err)
	}
	if got, _ := vm.GlobalInt(progs.OfsReturn); got != 0 {
		t.Errorf("no active client -> %d, want 0 (world)", got)
	}

	// Active client at edict slot 1 -> that edict's QC pointer.
	h.Static.Clients[0].Active = true
	h.Static.Clients[0].Edict = h.Server.Edicts[1]
	if err := fn(vm); err != nil {
		t.Fatalf("checkclient (active): %v", err)
	}
	if got, want := mustGlobalInt(t, vm), arena.MakePointer(1, 0); got != want {
		t.Errorf("active client -> %d, want %d (slot 1 pointer)", got, want)
	}
}

func mustGlobalInt(t *testing.T, vm *progs.VM) int32 {
	t.Helper()
	v, err := vm.GlobalInt(progs.OfsReturn)
	if err != nil {
		t.Fatalf("GlobalInt(OfsReturn): %v", err)
	}
	return v
}
