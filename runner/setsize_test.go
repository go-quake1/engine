// Copyright (c) 2026 the go-quake1/engine authors.
// SPDX-License-Identifier: BSD-3-Clause

package runner

import (
	"testing"

	"github.com/go-quake1/engine/bspfile"
	"github.com/go-quake1/engine/bsptrace"
	enginehost "github.com/go-quake1/engine/host"
	"github.com/go-quake1/engine/model"
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

// progsForChangeYawTest builds a progs with angles/ideal_yaw/yaw_speed fields
// and a `self` entity global, enough to drive builtinChangeYaw.
func progsForChangeYawTest() *progs.Progs {
	strs := []byte{0}
	add := func(s string) int32 {
		o := int32(len(strs))
		strs = append(strs, []byte(s)...)
		strs = append(strs, 0)
		return o
	}
	anglesN := add("angles")
	idealN := add("ideal_yaw")
	speedN := add("yaw_speed")
	selfN := add("self")
	const entityFields = 8
	return &progs.Progs{
		Header:  progs.Header{EntityFields: entityFields},
		Strings: strs,
		FieldDefs: []progs.Def{
			{Type: uint16(progs.EvVector), Ofs: 1, SName: anglesN},
			{Type: uint16(progs.EvFloat), Ofs: 4, SName: idealN},
			{Type: uint16(progs.EvFloat), Ofs: 5, SName: speedN},
		},
		GlobalDefs: []progs.Def{
			{Type: uint16(progs.EvEntity), Ofs: 1, SName: selfN},
		},
		Globals:    make([]byte, 32*4),
		Functions:  []progs.Function{{FirstStatement: 0, SName: 0}},
		Statements: []progs.Statement{{Op: progs.OP_DONE}},
	}
}

func TestBuiltinChangeYaw(t *testing.T) {
	p := progsForChangeYawTest()
	arena := progs.NewEdictArena(p, 8)
	vm := progs.NewVM(p)
	vm.SetArena(arena)

	const slot = 3
	ent, err := arena.Get(slot)
	if err != nil {
		t.Fatalf("arena.Get: %v", err)
	}
	ev, err := progs.NewEntVars(p, ent)
	if err != nil {
		t.Fatalf("NewEntVars: %v", err)
	}
	_ = ev.WriteVec3("angles", [3]float32{0, 0, 0})
	_ = ev.WriteFloat("ideal_yaw", 90)
	_ = ev.WriteFloat("yaw_speed", 20)
	selfDef := p.FindGlobal("self")
	if selfDef == nil {
		t.Fatal("no self global")
	}
	if err := vm.SetGlobalInt(int(selfDef.Ofs), arena.MakePointer(slot, 0)); err != nil {
		t.Fatalf("set self: %v", err)
	}

	fn := builtinChangeYaw(&enginehost.Host{})
	yaw := func() float32 {
		a, _ := ev.ReadVec3("angles")
		return a[1]
	}
	// AngleMod uses Quake's 360/65536 fixed-point wrap, so results land within
	// ~0.01 deg of the integer target rather than exactly on it.
	near := func(got, want float32, msg string) {
		if d := got - want; d > 0.05 || d < -0.05 {
			t.Errorf("%s: yaw=%v want ~%v", msg, got, want)
		}
	}

	// Turn yaw_speed (20) toward ideal 90.
	if err := fn(vm); err != nil {
		t.Fatalf("changeyaw: %v", err)
	}
	near(yaw(), 20, "first turn")
	// Second turn -> 40.
	_ = fn(vm)
	near(yaw(), 40, "second turn")
	// Already aligned (ideal ~ current) -> no move past ~40.
	_ = ev.WriteFloat("ideal_yaw", yaw())
	_ = fn(vm)
	near(yaw(), 40, "aligned (no move)")
	// Shorter way around: from ~40 toward 350 is -20 (through 0), not +310.
	_ = ev.WriteFloat("ideal_yaw", 350)
	_ = fn(vm)
	near(yaw(), 20, "wrap short way")
}

// floorWorldAt builds a minimal BrushModel that is empty above z=floorZ and
// solid below -- an infinite flat floor, enough for a walking MoveStep.
func floorWorldAt(floorZ float32) *model.BrushModel {
	h := bsptrace.Hull{
		ClipNodes:     []bspfile.ClipNode{{PlaneNum: 0, Children: [2]int16{bspfile.ContentsEmpty, bspfile.ContentsSolid}}},
		Planes:        []bspfile.Plane{{Normal: [3]float32{0, 0, 1}, Dist: floorZ, Type: bspfile.PlaneZ}},
		FirstClipNode: 0,
		LastClipNode:  0,
	}
	bm := &model.BrushModel{}
	bm.Hulls[0] = h
	bm.Hulls[1] = h
	bm.Hulls[2] = h
	return bm
}

// progsForMoveTest builds a progs with the fields movetogoal reads
// (origin/mins/maxs/angles/flags/ideal_yaw/goalentity) + a `self` global.
func progsForMoveTest() *progs.Progs {
	strs := []byte{0}
	add := func(s string) int32 {
		o := int32(len(strs))
		strs = append(strs, []byte(s)...)
		strs = append(strs, 0)
		return o
	}
	originN := add("origin")
	minsN := add("mins")
	maxsN := add("maxs")
	anglesN := add("angles")
	flagsN := add("flags")
	idealN := add("ideal_yaw")
	goalN := add("goalentity")
	solidN := add("solid")
	selfN := add("self")
	const entityFields = 20
	return &progs.Progs{
		Header:  progs.Header{EntityFields: entityFields},
		Strings: strs,
		FieldDefs: []progs.Def{
			{Type: uint16(progs.EvVector), Ofs: 1, SName: originN},
			{Type: uint16(progs.EvVector), Ofs: 4, SName: minsN},
			{Type: uint16(progs.EvVector), Ofs: 7, SName: maxsN},
			{Type: uint16(progs.EvVector), Ofs: 10, SName: anglesN},
			{Type: uint16(progs.EvFloat), Ofs: 13, SName: flagsN},
			{Type: uint16(progs.EvFloat), Ofs: 14, SName: idealN},
			{Type: uint16(progs.EvEntity), Ofs: 15, SName: goalN},
			{Type: uint16(progs.EvFloat), Ofs: 16, SName: solidN},
		},
		GlobalDefs: []progs.Def{
			{Type: uint16(progs.EvEntity), Ofs: 1, SName: selfN},
		},
		Globals:    make([]byte, 32*4),
		Functions:  []progs.Function{{FirstStatement: 0, SName: 0}},
		Statements: []progs.Statement{{Op: progs.OP_DONE}},
	}
}

// TestBuiltinMoveToGoalWalksTowardGoal proves the movetogoal glue: a grounded
// monster at the origin, its goalentity 100 units away in +X, steps toward the
// goal over a flat floor (world.MoveToGoal machinery). A no-op left every
// monster stationary; this asserts the origin actually advances.
func TestBuiltinMoveToGoalWalksTowardGoal(t *testing.T) {
	p := progsForMoveTest()
	arena := progs.NewEdictArena(p, 8)
	vm := progs.NewVM(p)
	vm.SetArena(arena)

	h := &enginehost.Host{
		Server: engineserver.NewServer(),
		World:  world.New(),
		Static: engineserver.NewStatic(4),
	}
	h.SetProgs(p)
	h.Server.Arena = arena
	h.Server.WorldModel = floorWorldAt(0) // floor at z=0
	h.Server.Edicts = make([]*progs.Edict, 8)
	for i := range h.Server.Edicts {
		ed, err := arena.Get(i)
		if err != nil {
			t.Fatalf("arena.Get(%d): %v", i, err)
		}
		h.Server.Edicts[i] = ed
	}
	h.World.Clear([3]float32{-1024, -1024, -1024}, [3]float32{1024, 1024, 1024})

	const mon, goal = 2, 3
	mev, _ := progs.NewEntVars(p, h.Server.Edicts[mon])
	_ = mev.WriteVec3("origin", [3]float32{0, 0, 24}) // feet (mins.z=-24) on the floor
	_ = mev.WriteVec3("mins", [3]float32{-16, -16, -24})
	_ = mev.WriteVec3("maxs", [3]float32{16, 16, 40})
	_ = mev.WriteFloat("flags", float32(int32(engineserver.FlagOnGround)))
	_ = mev.WriteFloat("ideal_yaw", 0) // face +X toward the goal
	_ = mev.WriteInt32("goalentity", arena.MakePointer(goal, 0))

	gev, _ := progs.NewEntVars(p, h.Server.Edicts[goal])
	_ = gev.WriteVec3("origin", [3]float32{100, 0, 24})

	selfDef := p.FindGlobal("self")
	if err := vm.SetGlobalInt(int(selfDef.Ofs), arena.MakePointer(mon, 0)); err != nil {
		t.Fatalf("set self: %v", err)
	}
	if err := vm.SetGlobalFloat(progs.OfsParm0, 20); err != nil { // dist
		t.Fatalf("set dist: %v", err)
	}

	before, _ := mev.ReadVec3("origin")
	if err := builtinMoveToGoal(h)(vm); err != nil {
		t.Fatalf("movetogoal: %v", err)
	}
	after, _ := mev.ReadVec3("origin")
	if after[0] <= before[0] {
		t.Errorf("origin X did not advance toward the +X goal: before=%v after=%v", before, after)
	}
}

func TestFaceFrontFacing(t *testing.T) {
	up := [3]float32{0, 0, 1} // plane z=0, outward normal +Z
	// Side 0: outward normal is +normal. Visible from above, not below.
	if !faceFrontFacing(up, 0, 0, [3]float32{0, 0, 10}) {
		t.Error("side 0, camera above: want front-facing")
	}
	if faceFrontFacing(up, 0, 0, [3]float32{0, 0, -10}) {
		t.Error("side 0, camera below: want culled")
	}
	// PLANEBACK (side != 0): outward normal is -normal. Flipped.
	if faceFrontFacing(up, 0, 1, [3]float32{0, 0, 10}) {
		t.Error("PLANEBACK, camera above: want culled")
	}
	if !faceFrontFacing(up, 0, 1, [3]float32{0, 0, -10}) {
		t.Error("PLANEBACK, camera below: want front-facing")
	}
	// dist offset: plane z=50, camera at z=10 is BELOW it (dot<0) -> culled at side 0.
	if faceFrontFacing(up, 50, 0, [3]float32{0, 0, 10}) {
		t.Error("side 0, camera below offset plane: want culled")
	}
}

// moveTestHost builds a host with a flat floor + a grounded monster at slot 2
// facing +X, ready for a walkmove(yaw=0, dist) call. Returns everything the
// caller needs to add blockers, drive the builtin, and read the result back.
func moveTestHost(t *testing.T, dist float32) (*enginehost.Host, *progs.VM, *progs.EdictArena, *progs.Progs) {
	t.Helper()
	p := progsForMoveTest()
	arena := progs.NewEdictArena(p, 8)
	vm := progs.NewVM(p)
	vm.SetArena(arena)
	h := &enginehost.Host{
		Server: engineserver.NewServer(),
		World:  world.New(),
		Static: engineserver.NewStatic(4),
	}
	h.SetProgs(p)
	h.Server.Arena = arena
	h.Server.WorldModel = floorWorldAt(0)
	h.Server.Edicts = make([]*progs.Edict, 8)
	for i := range h.Server.Edicts {
		ed, err := arena.Get(i)
		if err != nil {
			t.Fatalf("arena.Get(%d): %v", i, err)
		}
		h.Server.Edicts[i] = ed
	}
	h.World.Clear([3]float32{-2048, -2048, -2048}, [3]float32{2048, 2048, 2048})
	mev, _ := progs.NewEntVars(p, h.Server.Edicts[2])
	_ = mev.WriteVec3("origin", [3]float32{0, 0, 24})
	_ = mev.WriteVec3("mins", [3]float32{-16, -16, -24})
	_ = mev.WriteVec3("maxs", [3]float32{16, 16, 40})
	_ = mev.WriteFloat("flags", float32(int32(engineserver.FlagOnGround)))
	selfDef := p.FindGlobal("self")
	_ = vm.SetGlobalInt(int(selfDef.Ofs), arena.MakePointer(2, 0))
	_ = vm.SetGlobalFloat(progs.OfsParm0, 0)    // yaw = +X
	_ = vm.SetGlobalFloat(progs.OfsParm1, dist) // dist
	return h, vm, arena, p
}

// TestBuiltinWalkMoveClipsAgainstEntity proves monster moves clip against OTHER
// solid entities (not just the world): a solid blocker in the +X path stops the
// walk that would otherwise succeed.
func TestBuiltinWalkMoveClipsAgainstEntity(t *testing.T) {
	// A: no blocker -> the walk succeeds and advances +X.
	hA, vmA, _, pA := moveTestHost(t, 40)
	if err := builtinWalkMove(hA)(vmA); err != nil {
		t.Fatalf("walkmove A: %v", err)
	}
	mevA, _ := progs.NewEntVars(pA, hA.Server.Edicts[2])
	freeX, _ := mevA.ReadVec3("origin")
	if freeX[0] <= 0 {
		t.Fatalf("unblocked walk did not advance: %v", freeX)
	}

	// B: a solid slidebox blocker sitting in the destination stops the walk.
	hB, vmB, arenaB, pB := moveTestHost(t, 40)
	blk, _ := progs.NewEntVars(pB, hB.Server.Edicts[3])
	_ = blk.WriteVec3("origin", [3]float32{55, 0, 24})
	_ = blk.WriteVec3("mins", [3]float32{-16, -16, -24})
	_ = blk.WriteVec3("maxs", [3]float32{16, 16, 40})
	_ = blk.WriteFloat("solid", float32(int32(engineserver.SolidSlideBox)))
	hB.LinkEdict(hB.Server.Edicts[3])
	_ = arenaB
	if err := builtinWalkMove(hB)(vmB); err != nil {
		t.Fatalf("walkmove B: %v", err)
	}
	mevB, _ := progs.NewEntVars(pB, hB.Server.Edicts[2])
	blockedX, _ := mevB.ReadVec3("origin")

	if blockedX[0] >= freeX[0] {
		t.Errorf("blocker did not clip the move: free X=%v blocked X=%v", freeX[0], blockedX[0])
	}
	if blockedX[0]+16 > 55-16 {
		t.Errorf("monster front (%v) overlapped the blocker back (%v)", blockedX[0]+16, 55-16)
	}
}
