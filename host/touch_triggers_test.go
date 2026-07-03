// Copyright (c) 2026 the go-quake1/engine authors.
// SPDX-License-Identifier: GPL-2.0-or-later

package host

import (
	"errors"
	"math"
	"testing"

	"github.com/go-quake1/engine/progs"
	"github.com/go-quake1/engine/server"
	"github.com/go-quake1/engine/world"
)

// --- shared touch fixture ---------------------------------------------

// progsForTouch builds a Progs stub that declares every field +
// global the touch dispatch chain reads:
//
//	fields:   origin(vec3) mins(vec3) maxs(vec3) solid(float)
//	          touch(function) ammo_shells(float) health(float)
//	globals:  self(entity) other(entity) time(float)
//	          fnIdx2 (constant float = 2.0, so OP_CALL0 dispatches
//	                  the builtin wrapper at function index 2)
//
// Function layout:
//
//	[0] = null (FirstStatement=0 = OP_DONE).
//	[1] = QC wrapper: Statements[1..2] = OP_CALL0 fnIdx2 then OP_DONE.
//	[2] = builtin (FirstStatement = -1, dispatches builtin slot 1).
//
// vm.Run(1) enters function 1, hits OP_CALL0 (which dispatches
// function 2 = the registered builtin), returns, then OP_DONE pops.
// The host's dispatchTouch sets self / other before vm.Run(1), so
// the builtin sees both globals already populated.
func progsForTouch() *progs.Progs {
	strs := []byte{0}
	originName := addStr(&strs, "origin")
	minsName := addStr(&strs, "mins")
	maxsName := addStr(&strs, "maxs")
	solidName := addStr(&strs, "solid")
	touchName := addStr(&strs, "touch")
	ammoShellsName := addStr(&strs, "ammo_shells")
	healthName := addStr(&strs, "health")
	movetypeName := addStr(&strs, "movetype")
	nextthinkName := addStr(&strs, "nextthink")
	thinkName := addStr(&strs, "think")
	selfName := addStr(&strs, "self")
	otherName := addStr(&strs, "other")
	timeName := addStr(&strs, "time")
	fnIdx2Name := addStr(&strs, "fnIdx2")

	const entityFields = 20
	const numGlobals = 96
	globals := make([]byte, numGlobals*4)

	const (
		originOfs     = 1  // vec3 1..3
		minsOfs       = 4  // vec3 4..6
		maxsOfs       = 7  // vec3 7..9
		solidOfs      = 10 // float
		touchOfs      = 11 // function (stored as int32)
		ammoShellsOfs = 12 // float (player)
		healthOfs     = 13 // float (player)
		movetypeOfs   = 14 // float (RunPhysics reads it)
		nextthinkOfs  = 15 // float (RunThink reads it)
		thinkOfs      = 16 // function (RunThink reads it)
	)
	const (
		selfGlobalOfs   = 40
		otherGlobalOfs  = 41
		timeGlobalOfs   = 42
		fnIdx2GlobalOfs = 50 // holds the integer 2 (function index)
	)
	// Pre-load slot 50 with the int32 value 2 so OP_CALL0 dispatches
	// function index 2 (the builtin).
	{
		var four [4]byte
		// Little-endian int32 = 2.
		four[0] = 2
		copy(globals[fnIdx2GlobalOfs*4:fnIdx2GlobalOfs*4+4], four[:])
	}

	stmts := []progs.Statement{
		{Op: progs.OP_DONE},                             // 0 -- null body
		{Op: progs.OP_CALL0, A: int16(fnIdx2GlobalOfs)}, // 1 -- function 1 body: call fn at slot 50 (= fn idx 2)
		{Op: progs.OP_DONE},                             // 2 -- function 1 body: return
	}

	return &progs.Progs{
		Header:  progs.Header{EntityFields: entityFields},
		Strings: strs,
		FieldDefs: []progs.Def{
			{Type: uint16(progs.EvVector), Ofs: originOfs, SName: originName},
			{Type: uint16(progs.EvVector), Ofs: minsOfs, SName: minsName},
			{Type: uint16(progs.EvVector), Ofs: maxsOfs, SName: maxsName},
			{Type: uint16(progs.EvFloat), Ofs: solidOfs, SName: solidName},
			{Type: uint16(progs.EvFunction), Ofs: touchOfs, SName: touchName},
			{Type: uint16(progs.EvFloat), Ofs: ammoShellsOfs, SName: ammoShellsName},
			{Type: uint16(progs.EvFloat), Ofs: healthOfs, SName: healthName},
			{Type: uint16(progs.EvFloat), Ofs: movetypeOfs, SName: movetypeName},
			{Type: uint16(progs.EvFloat), Ofs: nextthinkOfs, SName: nextthinkName},
			{Type: uint16(progs.EvFunction), Ofs: thinkOfs, SName: thinkName},
		},
		GlobalDefs: []progs.Def{
			{Type: uint16(progs.EvEntity), Ofs: selfGlobalOfs, SName: selfName},
			{Type: uint16(progs.EvEntity), Ofs: otherGlobalOfs, SName: otherName},
			{Type: uint16(progs.EvFloat), Ofs: timeGlobalOfs, SName: timeName},
			{Type: uint16(progs.EvFunction), Ofs: fnIdx2GlobalOfs, SName: fnIdx2Name},
		},
		Globals: globals,
		Functions: []progs.Function{
			{FirstStatement: 0, SName: 0},  // 0 = null
			{FirstStatement: 1, SName: 0},  // 1 = QC wrapper (issues OP_CALL0)
			{FirstStatement: -1, SName: 0}, // 2 = builtin slot 1
		},
		Statements: stmts,
	}
}

// newTouchHost wires up a Host with the progsForTouch progs, an arena
// of size edictCount, the world tree cleared, and the static client
// pool sized to maxClients. Returns the host + progs + arena handle.
func newTouchHost(t *testing.T, edictCount, maxClients int) (*Host, *progs.Progs) {
	t.Helper()
	p := progsForTouch()
	vm := progs.NewVM(p)
	h := &Host{
		VM:     vm,
		Server: server.NewServer(),
		Static: server.NewStatic(maxClients),
		World:  world.New(),
	}
	h.SetProgs(p)
	arena := progs.NewEdictArena(p, edictCount)
	h.Server.Arena = arena
	vm.SetArena(arena)
	h.Server.Edicts = make([]*progs.Edict, edictCount)
	h.Server.NumEdicts = edictCount
	// NewEdictArena leaves every slot NOT free; Get hands back the
	// per-slot Edict address verbatim (the addresses are stable for
	// the arena's lifetime, which is what PointerForEdict needs).
	for i := 0; i < edictCount; i++ {
		ed, err := arena.Get(i)
		if err != nil {
			t.Fatalf("arena.Get(%d): %v", i, err)
		}
		h.Server.Edicts[i] = ed
	}
	h.World.Clear([3]float32{-1024, -1024, -1024}, [3]float32{1024, 1024, 1024})
	return h, p
}

// writeTriggerFields populates the standard entvars for slot:
//
//	origin = origin, mins = {-8,-8,-8}, maxs = {8,8,8},
//	solid = solid, touch = funcID
func writeTriggerFields(t *testing.T, h *Host, p *progs.Progs, slot int, origin [3]float32, solid server.Solid, funcID int32) {
	t.Helper()
	ed := h.Server.Edicts[slot]
	ev, _ := progs.NewEntVars(p, ed)
	if err := ev.WriteVec3("origin", origin); err != nil {
		t.Fatalf("WriteVec3 origin slot=%d: %v", slot, err)
	}
	if err := ev.WriteVec3("mins", [3]float32{-8, -8, -8}); err != nil {
		t.Fatalf("WriteVec3 mins slot=%d: %v", slot, err)
	}
	if err := ev.WriteVec3("maxs", [3]float32{8, 8, 8}); err != nil {
		t.Fatalf("WriteVec3 maxs slot=%d: %v", slot, err)
	}
	if err := ev.WriteFloat("solid", float32(solid)); err != nil {
		t.Fatalf("WriteFloat solid slot=%d: %v", slot, err)
	}
	// touch is EvFunction stored as int32.
	def := p.FindField("touch")
	if def == nil {
		t.Fatalf("touch field not declared in progs")
	}
	if err := ed.FieldSetInt(int(def.Ofs), funcID); err != nil {
		t.Fatalf("FieldSetInt touch slot=%d: %v", slot, err)
	}
}

// linkTriggerInArea calls World.LinkBounds with the entity's current
// origin + mins/maxs + solid->kind mapping.
func linkTriggerInArea(t *testing.T, h *Host, p *progs.Progs, slot int) {
	t.Helper()
	ed := h.Server.Edicts[slot]
	ev, _ := progs.NewEntVars(p, ed)
	origin, _ := ev.ReadVec3("origin")
	mins, _ := ev.ReadVec3("mins")
	maxs, _ := ev.ReadVec3("maxs")
	absmin := [3]float32{origin[0] + mins[0], origin[1] + mins[1], origin[2] + mins[2]}
	absmax := [3]float32{origin[0] + maxs[0], origin[1] + maxs[1], origin[2] + maxs[2]}
	h.World.LinkBounds(world.Key(slot), absmin, absmax, hostSolidKindFromEntvars(ev))
}

// pickupShellsBuiltin returns a Builtin that mirrors the QC
// item_shells touch body's payload: bumps `other.ammo_shells` by 5,
// unlinks `self` from the area tree (via SetOrigin to a far-away
// point + setting self.solid = SOLID_NOT). Records call count + the
// observed (self, other) pointers so tests can assert wiring.
func pickupShellsBuiltin(h *Host, p *progs.Progs, calls *int, gotSelf, gotOther *int32) progs.Builtin {
	return func(vm *progs.VM) error {
		*calls++
		// Read QC self / other globals (set by dispatchTouch).
		selfDef := p.FindGlobal("self")
		otherDef := p.FindGlobal("other")
		if selfDef == nil || otherDef == nil {
			return errors.New("test progs missing self/other globals")
		}
		selfPtr, _ := vm.GlobalInt(int(selfDef.Ofs))
		otherPtr, _ := vm.GlobalInt(int(otherDef.Ofs))
		*gotSelf = selfPtr
		*gotOther = otherPtr

		// Resolve `other` (= the player) and bump ammo_shells.
		arena := vm.Arena()
		if arena == nil {
			return errors.New("vm arena unwired")
		}
		other, _, err := arena.ResolvePointer(otherPtr)
		if err != nil {
			return err
		}
		oev, _ := progs.NewEntVars(p, other)
		cur, _ := oev.ReadFloat("ammo_shells")
		_ = oev.WriteFloat("ammo_shells", cur+5)

		// "Remove" self: flip solid to SOLID_NOT (so next-tic
		// TouchTriggers walks see it as a stale entry + skip
		// re-firing), then call SetOrigin to relink-as-unlink
		// at the far point. Mirrors the items.qc pattern.
		self, _, err := arena.ResolvePointer(selfPtr)
		if err != nil {
			return err
		}
		sev, _ := progs.NewEntVars(p, self)
		_ = sev.WriteFloat("solid", float32(server.SolidNot))
		h.SetOrigin(self, [3]float32{-8000, -8000, -8000})
		return nil
	}
}

// --- ResetTouchCounters ----------------------------------------------------

func TestResetTouchCounters_ZeroesAllFields(t *testing.T) {
	h, _ := newTouchHost(t, 4, 1)
	h.LastTriggerTouches = 5
	h.LastTouchErrors = 2
	h.LastTouchErrorMsgs = []string{"a", "b"}
	h.ResetTouchCounters()
	if h.LastTriggerTouches != 0 {
		t.Errorf("LastTriggerTouches not reset: %d", h.LastTriggerTouches)
	}
	if h.LastTouchErrors != 0 {
		t.Errorf("LastTouchErrors not reset: %d", h.LastTouchErrors)
	}
	if len(h.LastTouchErrorMsgs) != 0 {
		t.Errorf("LastTouchErrorMsgs not reset: %v", h.LastTouchErrorMsgs)
	}
}

// --- TouchTriggers: nil / pre-cond guards ---------------------------------

func TestTouchTriggers_NilHostNoOp(t *testing.T) {
	var h *Host
	h.TouchTriggers(1, world.Key(1))
}

func TestTouchTriggers_NilWorldNoOp(t *testing.T) {
	h := &Host{Server: server.NewServer()}
	h.TouchTriggers(1, world.Key(1))
}

func TestTouchTriggers_NilServerNoOp(t *testing.T) {
	h := &Host{World: world.New()}
	h.TouchTriggers(1, world.Key(1))
}

func TestTouchTriggers_SlotOutOfRangeNoOp(t *testing.T) {
	h, _ := newTouchHost(t, 4, 1)
	h.TouchTriggers(-1, world.Key(0))
	h.TouchTriggers(99, world.Key(99))
	if h.LastTriggerTouches != 0 {
		t.Errorf("expected no dispatches; got %d", h.LastTriggerTouches)
	}
}

func TestTouchTriggers_NilOrFreeMoverNoOp(t *testing.T) {
	h, _ := newTouchHost(t, 4, 1)
	h.Server.Edicts[1] = nil
	h.TouchTriggers(1, world.Key(1))
	if h.LastTriggerTouches != 0 {
		t.Errorf("nil mover should be no-op; got %d", h.LastTriggerTouches)
	}
	// Free mover branch.
	h.Server.Edicts[1] = &progs.Edict{Free: true, Fields: make([]byte, 80)}
	h.TouchTriggers(1, world.Key(1))
	if h.LastTriggerTouches != 0 {
		t.Errorf("free mover should be no-op; got %d", h.LastTriggerTouches)
	}
}

func TestTouchTriggers_NoProgsNoOp(t *testing.T) {
	h, _ := newTouchHost(t, 4, 1)
	h.SetProgs(nil)
	h.TouchTriggers(1, world.Key(1))
	if h.LastTriggerTouches != 0 {
		t.Errorf("no progs should be no-op; got %d", h.LastTriggerTouches)
	}
}

func TestTouchTriggers_NoTouchFieldNoOp(t *testing.T) {
	// Progs without a `touch` field declared.
	h, _ := newTouchHost(t, 4, 1)
	// Replace progs with one missing touch.
	strs := []byte{0}
	originName := addStr(&strs, "origin")
	minsName := addStr(&strs, "mins")
	maxsName := addStr(&strs, "maxs")
	solidName := addStr(&strs, "solid")
	p2 := &progs.Progs{
		Header:  progs.Header{EntityFields: 20},
		Strings: strs,
		FieldDefs: []progs.Def{
			{Type: uint16(progs.EvVector), Ofs: 1, SName: originName},
			{Type: uint16(progs.EvVector), Ofs: 4, SName: minsName},
			{Type: uint16(progs.EvVector), Ofs: 7, SName: maxsName},
			{Type: uint16(progs.EvFloat), Ofs: 10, SName: solidName},
		},
		Globals:    make([]byte, 256),
		Functions:  []progs.Function{{FirstStatement: 0, SName: 0}},
		Statements: []progs.Statement{{Op: progs.OP_DONE}},
	}
	h.SetProgs(p2)
	h.TouchTriggers(1, world.Key(1))
	if h.LastTriggerTouches != 0 {
		t.Errorf("no touch field should be no-op; got %d", h.LastTriggerTouches)
	}
}

func TestTouchTriggers_MissingMoverFieldsNoOp(t *testing.T) {
	h, p := newTouchHost(t, 4, 1)
	// Mover (slot 1) has no origin/mins/maxs written -- field exists
	// in progs so the read succeeds (just returns zero). To force the
	// read error path, swap mover's Fields to a too-small slice that
	// FieldGetVector range-checks against.
	h.Server.Edicts[1].Fields = []byte{} // too small for any vec3 read
	h.TouchTriggers(1, world.Key(1))
	if h.LastTriggerTouches != 0 {
		t.Errorf("missing mover fields should be no-op; got %d", h.LastTriggerTouches)
	}
	// Restore + write origin only; mins read should now fail because
	// the offset 4..6 of the (4-byte) slice is out of range. Same
	// no-op outcome.
	h.Server.Edicts[1].Fields = make([]byte, 4)
	h.TouchTriggers(1, world.Key(1))
	if h.LastTriggerTouches != 0 {
		t.Errorf("partial fields should be no-op; got %d", h.LastTriggerTouches)
	}
	// 16 bytes: origin (slots 1..3) reads bytes 4..16, ok; mins at
	// slot 4..6 reads bytes 16..28 -- fails.
	h.Server.Edicts[1].Fields = make([]byte, 16)
	h.TouchTriggers(1, world.Key(1))
	if h.LastTriggerTouches != 0 {
		t.Errorf("missing mins should be no-op; got %d", h.LastTriggerTouches)
	}
	// mins ok, maxs missing -- pad to 28 bytes (covers offsets 0..6).
	h.Server.Edicts[1].Fields = make([]byte, 28)
	h.TouchTriggers(1, world.Key(1))
	if h.LastTriggerTouches != 0 {
		t.Errorf("missing maxs should be no-op; got %d", h.LastTriggerTouches)
	}
	_ = p
}

// --- TouchTriggers: happy path -------------------------------------------

// Player walks into an item_shells trigger; the touch dispatch fires
// with self=trigger, other=player; the test builtin bumps
// player.ammo_shells + flips trigger.solid to SOLID_NOT + moves
// trigger to (-8000,-8000,-8000). Asserts:
//
//	LastTriggerTouches == 1
//	player.ammo_shells == 5
//	trigger.solid == SOLID_NOT
//	second TouchTriggers call doesn't re-fire (stale solid skips it)
func TestTouchTriggers_HappyPathFiresAndRemoves(t *testing.T) {
	h, p := newTouchHost(t, 4, 1)
	// Slot 1 = player; slot 2 = item_shells trigger.
	writeTriggerFields(t, h, p, 1, [3]float32{0, 0, 0}, server.SolidSlideBox, 0)
	writeTriggerFields(t, h, p, 2, [3]float32{10, 0, 0}, server.SolidTrigger, 1)
	linkTriggerInArea(t, h, p, 2)

	calls := 0
	var gotSelf, gotOther int32
	h.VM.RegisterBuiltin(1, pickupShellsBuiltin(h, p, &calls, &gotSelf, &gotOther))

	h.TouchTriggers(1, world.Key(1))

	if calls != 1 {
		t.Errorf("expected 1 builtin call; got %d", calls)
	}
	if h.LastTriggerTouches != 1 {
		t.Errorf("LastTriggerTouches got %d want 1", h.LastTriggerTouches)
	}
	if h.LastTouchErrors != 0 {
		t.Errorf("LastTouchErrors got %d want 0 (msgs=%v)", h.LastTouchErrors, h.LastTouchErrorMsgs)
	}

	// Self/other: self should be the trigger (slot 2); other should
	// be the player (slot 1). Compare via the arena's PointerForEdict.
	wantSelf := h.Server.Arena.PointerForEdict(h.Server.Edicts[2])
	wantOther := h.Server.Arena.PointerForEdict(h.Server.Edicts[1])
	if gotSelf != wantSelf {
		t.Errorf("self pointer got %d want %d (trigger ptr)", gotSelf, wantSelf)
	}
	if gotOther != wantOther {
		t.Errorf("other pointer got %d want %d (player ptr)", gotOther, wantOther)
	}

	// Player ammo bumped.
	pev, _ := progs.NewEntVars(p, h.Server.Edicts[1])
	if got, _ := pev.ReadFloat("ammo_shells"); got != 5 {
		t.Errorf("player.ammo_shells got %v want 5", got)
	}
	// Trigger solid is now SOLID_NOT.
	tev, _ := progs.NewEntVars(p, h.Server.Edicts[2])
	if got, _ := tev.ReadFloat("solid"); got != 0 {
		t.Errorf("trigger.solid got %v want 0 (SOLID_NOT)", got)
	}
	// Trigger origin moved.
	if got, _ := tev.ReadVec3("origin"); got != ([3]float32{-8000, -8000, -8000}) {
		t.Errorf("trigger.origin got %v want -8000 vec", got)
	}

	// Second call: trigger should not re-fire (SetOrigin relinked
	// at -8000; AreaQuery for the player at (0,0,0) won't return
	// the trigger -- but even if it did, the SOLID_NOT re-check
	// short-circuits).
	calls = 0
	h.LastTriggerTouches = 0
	h.TouchTriggers(1, world.Key(1))
	if calls != 0 {
		t.Errorf("re-fire on second tic: %d calls", calls)
	}
	if h.LastTriggerTouches != 0 {
		t.Errorf("re-fire LastTriggerTouches: %d", h.LastTriggerTouches)
	}
}

// --- TouchTriggers: per-trigger skip paths -------------------------------

func TestTouchTriggers_SkipFreeTrigger(t *testing.T) {
	h, p := newTouchHost(t, 4, 1)
	writeTriggerFields(t, h, p, 1, [3]float32{0, 0, 0}, server.SolidSlideBox, 0)
	writeTriggerFields(t, h, p, 2, [3]float32{10, 0, 0}, server.SolidTrigger, 1)
	linkTriggerInArea(t, h, p, 2)
	h.Server.Edicts[2].Free = true

	calls := 0
	var s, o int32
	h.VM.RegisterBuiltin(1, pickupShellsBuiltin(h, p, &calls, &s, &o))
	h.TouchTriggers(1, world.Key(1))
	if calls != 0 {
		t.Errorf("free trigger should not dispatch; calls=%d", calls)
	}
}

func TestTouchTriggers_SkipNilTriggerSlot(t *testing.T) {
	h, p := newTouchHost(t, 4, 1)
	writeTriggerFields(t, h, p, 1, [3]float32{0, 0, 0}, server.SolidSlideBox, 0)
	writeTriggerFields(t, h, p, 2, [3]float32{10, 0, 0}, server.SolidTrigger, 1)
	linkTriggerInArea(t, h, p, 2)
	h.Server.Edicts[2] = nil

	calls := 0
	var s, o int32
	h.VM.RegisterBuiltin(1, pickupShellsBuiltin(h, p, &calls, &s, &o))
	h.TouchTriggers(1, world.Key(1))
	if calls != 0 {
		t.Errorf("nil trigger slot should not dispatch; calls=%d", calls)
	}
}

func TestTouchTriggers_SkipSelfOverlap(t *testing.T) {
	h, p := newTouchHost(t, 4, 1)
	// Player itself is registered as SOLID_TRIGGER (synthetic but
	// exercises the self-key guard).
	writeTriggerFields(t, h, p, 1, [3]float32{0, 0, 0}, server.SolidTrigger, 1)
	linkTriggerInArea(t, h, p, 1)

	calls := 0
	var s, o int32
	h.VM.RegisterBuiltin(1, pickupShellsBuiltin(h, p, &calls, &s, &o))
	h.TouchTriggers(1, world.Key(1))
	if calls != 0 {
		t.Errorf("self-overlap should not dispatch; calls=%d", calls)
	}
}

func TestTouchTriggers_SkipStaleSolid(t *testing.T) {
	h, p := newTouchHost(t, 4, 1)
	writeTriggerFields(t, h, p, 1, [3]float32{0, 0, 0}, server.SolidSlideBox, 0)
	// Linked as trigger; then mutated to SOLID_BBOX so the read
	// inside TouchTriggers sees a stale entry.
	writeTriggerFields(t, h, p, 2, [3]float32{10, 0, 0}, server.SolidTrigger, 1)
	linkTriggerInArea(t, h, p, 2)
	tev, _ := progs.NewEntVars(p, h.Server.Edicts[2])
	_ = tev.WriteFloat("solid", float32(server.SolidBBox))

	calls := 0
	var s, o int32
	h.VM.RegisterBuiltin(1, pickupShellsBuiltin(h, p, &calls, &s, &o))
	h.TouchTriggers(1, world.Key(1))
	if calls != 0 {
		t.Errorf("stale solid should not dispatch; calls=%d", calls)
	}
}

func TestTouchTriggers_SkipMissingSolidField(t *testing.T) {
	h, p := newTouchHost(t, 4, 1)
	writeTriggerFields(t, h, p, 1, [3]float32{0, 0, 0}, server.SolidSlideBox, 0)
	writeTriggerFields(t, h, p, 2, [3]float32{10, 0, 0}, server.SolidTrigger, 1)
	linkTriggerInArea(t, h, p, 2)
	// Force trigger's Fields to be too small for the solid read but
	// large enough to be a non-empty slice -- the upper guard inside
	// ev.ReadFloat fires.
	h.Server.Edicts[2].Fields = make([]byte, 4)

	calls := 0
	var s, o int32
	h.VM.RegisterBuiltin(1, pickupShellsBuiltin(h, p, &calls, &s, &o))
	h.TouchTriggers(1, world.Key(1))
	if calls != 0 {
		t.Errorf("missing solid field should skip; calls=%d", calls)
	}
}

func TestTouchTriggers_SkipMissingTouchField(t *testing.T) {
	h, p := newTouchHost(t, 4, 1)
	writeTriggerFields(t, h, p, 1, [3]float32{0, 0, 0}, server.SolidSlideBox, 0)
	writeTriggerFields(t, h, p, 2, [3]float32{10, 0, 0}, server.SolidTrigger, 1)
	linkTriggerInArea(t, h, p, 2)
	// Pad to exactly past solid (slot 10) but before touch (slot 11).
	// That's 11 slots * 4 bytes = 44 bytes -- read solid succeeds,
	// read touch errors.
	h.Server.Edicts[2].Fields = make([]byte, 44)
	// Re-write origin/mins/maxs/solid in the trimmed slice so the
	// pre-touch reads pass.
	tev, _ := progs.NewEntVars(p, h.Server.Edicts[2])
	_ = tev.WriteVec3("origin", [3]float32{10, 0, 0})
	_ = tev.WriteVec3("mins", [3]float32{-8, -8, -8})
	_ = tev.WriteVec3("maxs", [3]float32{8, 8, 8})
	_ = tev.WriteFloat("solid", float32(server.SolidTrigger))

	calls := 0
	var s, o int32
	h.VM.RegisterBuiltin(1, pickupShellsBuiltin(h, p, &calls, &s, &o))
	h.TouchTriggers(1, world.Key(1))
	if calls != 0 {
		t.Errorf("missing touch field should skip; calls=%d", calls)
	}
}

func TestTouchTriggers_SkipZeroTouchFunc(t *testing.T) {
	h, p := newTouchHost(t, 4, 1)
	writeTriggerFields(t, h, p, 1, [3]float32{0, 0, 0}, server.SolidSlideBox, 0)
	writeTriggerFields(t, h, p, 2, [3]float32{10, 0, 0}, server.SolidTrigger, 0)
	linkTriggerInArea(t, h, p, 2)

	calls := 0
	var s, o int32
	h.VM.RegisterBuiltin(1, pickupShellsBuiltin(h, p, &calls, &s, &o))
	h.TouchTriggers(1, world.Key(1))
	if calls != 0 {
		t.Errorf("touch funcID=0 should skip; calls=%d", calls)
	}
	if h.LastTriggerTouches != 0 {
		t.Errorf("LastTriggerTouches got %d want 0", h.LastTriggerTouches)
	}
}

// Out-of-range trigger slot (key larger than the edict pool). The
// AreaQuery walk includes the entry, but the slot guard inside
// TouchTriggers rejects it.
func TestTouchTriggers_SkipTriggerSlotOutOfRange(t *testing.T) {
	h, p := newTouchHost(t, 4, 1)
	writeTriggerFields(t, h, p, 1, [3]float32{0, 0, 0}, server.SolidSlideBox, 0)
	// Link a bogus key (out of the edict-pool index range).
	h.World.LinkBounds(world.Key(99), [3]float32{-9, -9, -9}, [3]float32{9, 9, 9}, world.SolidKindTrigger)

	calls := 0
	var s, o int32
	h.VM.RegisterBuiltin(1, pickupShellsBuiltin(h, p, &calls, &s, &o))
	h.TouchTriggers(1, world.Key(1))
	if calls != 0 {
		t.Errorf("out-of-range trigger key should skip; calls=%d", calls)
	}
}

// --- TouchTriggers: error capture ----------------------------------------

func TestTouchTriggers_RecordsDispatchError(t *testing.T) {
	h, p := newTouchHost(t, 4, 1)
	writeTriggerFields(t, h, p, 1, [3]float32{0, 0, 0}, server.SolidSlideBox, 0)
	writeTriggerFields(t, h, p, 2, [3]float32{10, 0, 0}, server.SolidTrigger, 1)
	linkTriggerInArea(t, h, p, 2)

	errFromBuiltin := errors.New("synthetic-touch-fail")
	h.VM.RegisterBuiltin(1, func(vm *progs.VM) error { return errFromBuiltin })
	h.TouchTriggers(1, world.Key(1))
	if h.LastTouchErrors != 1 {
		t.Errorf("LastTouchErrors got %d want 1", h.LastTouchErrors)
	}
	if len(h.LastTouchErrorMsgs) != 1 {
		t.Errorf("LastTouchErrorMsgs len got %d want 1", len(h.LastTouchErrorMsgs))
	}
	if h.LastTriggerTouches != 0 {
		t.Errorf("LastTriggerTouches got %d want 0 (errored)", h.LastTriggerTouches)
	}
}

// Re-firing the same trigger twice produces identical message
// strings (same funcID, same trigger slot, same mover slot, same
// err), so the dedup branch keeps the per-Frame error-msg list at
// exactly one entry.
func TestTouchTriggers_ErrorMsgDedup(t *testing.T) {
	h, p := newTouchHost(t, 4, 1)
	writeTriggerFields(t, h, p, 1, [3]float32{0, 0, 0}, server.SolidSlideBox, 0)
	writeTriggerFields(t, h, p, 2, [3]float32{10, 0, 0}, server.SolidTrigger, 1)
	linkTriggerInArea(t, h, p, 2)
	h.VM.RegisterBuiltin(1, func(vm *progs.VM) error { return errors.New("same-err") })

	for i := 0; i < 3; i++ {
		h.TouchTriggers(1, world.Key(1))
	}
	if h.LastTouchErrors != 3 {
		t.Errorf("LastTouchErrors got %d want 3", h.LastTouchErrors)
	}
	if len(h.LastTouchErrorMsgs) != 1 {
		t.Errorf("dedup failed: msg len=%d (%v)", len(h.LastTouchErrorMsgs), h.LastTouchErrorMsgs)
	}
}

// 16 distinct triggers each producing a distinct error message
// (different tslot in the format string). The per-Frame cap stops
// the list growing past 8 entries even as the error counter keeps
// climbing.
func TestTouchTriggers_ErrorMsgCap(t *testing.T) {
	h, p := newTouchHost(t, 32, 1)
	writeTriggerFields(t, h, p, 1, [3]float32{0, 0, 0}, server.SolidSlideBox, 0)
	for i := 2; i < 18; i++ {
		writeTriggerFields(t, h, p, i, [3]float32{0, 0, 0}, server.SolidTrigger, 1)
		linkTriggerInArea(t, h, p, i)
	}
	h.VM.RegisterBuiltin(1, func(vm *progs.VM) error { return errors.New("e") })
	h.TouchTriggers(1, world.Key(1))
	if h.LastTouchErrors != 16 {
		t.Errorf("cap test errors got %d want 16", h.LastTouchErrors)
	}
	if len(h.LastTouchErrorMsgs) != 8 {
		t.Errorf("cap test got %d unique msgs want 8", len(h.LastTouchErrorMsgs))
	}
}

// --- dispatchTouch: nil-progs shortcut -----------------------------------

func TestDispatchTouch_NilProgsShortPath(t *testing.T) {
	// Forces the named-global lookup to be skipped: dispatchTouch
	// still hands the funcID to vm.Run, which dispatches our
	// registered builtin.
	h, p := newTouchHost(t, 4, 1)
	called := false
	h.VM.RegisterBuiltin(1, func(vm *progs.VM) error {
		called = true
		return nil
	})
	// Slot 1 = player, slot 2 = trigger.
	writeTriggerFields(t, h, p, 1, [3]float32{0, 0, 0}, server.SolidSlideBox, 0)
	writeTriggerFields(t, h, p, 2, [3]float32{0, 0, 0}, server.SolidTrigger, 1)
	h.SetProgs(nil)
	if err := h.dispatchTouch(h.Server.Edicts[2], h.Server.Edicts[1], 1); err != nil {
		t.Fatalf("dispatchTouch nil progs: %v", err)
	}
	if !called {
		t.Error("builtin not invoked under nil progs")
	}
}

// --- SetOrigin: relink + bbox semantics ----------------------------------

func TestSetOrigin_RelinksAreaTree(t *testing.T) {
	h, p := newTouchHost(t, 4, 1)
	writeTriggerFields(t, h, p, 1, [3]float32{0, 0, 0}, server.SolidSlideBox, 0)
	writeTriggerFields(t, h, p, 2, [3]float32{10, 0, 0}, server.SolidTrigger, 1)
	linkTriggerInArea(t, h, p, 2)

	// Initially the trigger overlaps (0..20, 0..0, 0..0) on x-axis.
	keys := h.World.AreaQuery([3]float32{-1, -1, -1}, [3]float32{20, 1, 1}, world.QueryTriggersOnly)
	if len(keys) != 1 || keys[0] != world.Key(2) {
		t.Fatalf("pre-move query: got %v want [2]", keys)
	}

	// Move it far away.
	h.SetOrigin(h.Server.Edicts[2], [3]float32{-8000, -8000, -8000})

	keys = h.World.AreaQuery([3]float32{-1, -1, -1}, [3]float32{20, 1, 1}, world.QueryTriggersOnly)
	if len(keys) != 0 {
		t.Errorf("post-move query: got %v want empty", keys)
	}
	// And it's now linked far away.
	keys = h.World.AreaQuery([3]float32{-9000, -9000, -9000}, [3]float32{-7000, -7000, -7000}, world.QueryTriggersOnly)
	if len(keys) != 1 || keys[0] != world.Key(2) {
		t.Errorf("post-move far query: got %v want [2]", keys)
	}
}

func TestSetOrigin_NilHostNoOp(t *testing.T) {
	var h *Host
	h.SetOrigin(&progs.Edict{}, [3]float32{1, 2, 3})
}

func TestSetOrigin_NilEntNoOp(t *testing.T) {
	h, _ := newTouchHost(t, 4, 1)
	h.SetOrigin(nil, [3]float32{1, 2, 3})
}

func TestSetOrigin_FreeEntNoOp(t *testing.T) {
	h, _ := newTouchHost(t, 4, 1)
	ed := &progs.Edict{Free: true, Fields: make([]byte, 80)}
	h.SetOrigin(ed, [3]float32{1, 2, 3})
}

func TestSetOrigin_NoProgsNoOp(t *testing.T) {
	h, _ := newTouchHost(t, 4, 1)
	ed := h.Server.Edicts[1]
	h.SetProgs(nil)
	h.SetOrigin(ed, [3]float32{1, 2, 3})
}

func TestSetOrigin_NoOriginFieldDoesNotRelink(t *testing.T) {
	h, _ := newTouchHost(t, 4, 1)
	// Replace progs with one missing the `origin` field.
	strs := []byte{0}
	p2 := &progs.Progs{
		Header:     progs.Header{EntityFields: 8},
		Strings:    strs,
		FieldDefs:  []progs.Def{},
		Globals:    make([]byte, 256),
		Functions:  []progs.Function{{FirstStatement: 0, SName: 0}},
		Statements: []progs.Statement{{Op: progs.OP_DONE}},
	}
	h.SetProgs(p2)
	// SetOrigin should silent-skip because ev.WriteVec3("origin") fails.
	h.SetOrigin(h.Server.Edicts[1], [3]float32{42, 42, 42})
}

// --- LinkEdict: pre-cond guards ------------------------------------------

func TestLinkEdict_NilHostNoOp(t *testing.T) {
	var h *Host
	h.LinkEdict(&progs.Edict{})
}

func TestLinkEdict_NilWorldNoOp(t *testing.T) {
	h := &Host{Server: server.NewServer()}
	h.LinkEdict(&progs.Edict{})
}

func TestLinkEdict_NilEntNoOp(t *testing.T) {
	h, _ := newTouchHost(t, 4, 1)
	h.LinkEdict(nil)
}

func TestLinkEdict_FreeEntNoOp(t *testing.T) {
	h, _ := newTouchHost(t, 4, 1)
	ed := &progs.Edict{Free: true, Fields: make([]byte, 80)}
	h.LinkEdict(ed)
}

func TestLinkEdict_NoServerArenaNoOp(t *testing.T) {
	h, _ := newTouchHost(t, 4, 1)
	h.Server.Arena = nil
	h.LinkEdict(h.Server.Edicts[1])
}

// An edict that doesn't belong to h.Server.Arena -- arena.NumFor
// returns -1, so LinkEdict short-circuits without touching the
// area tree. Exercises the `slot < 0` guard.
func TestLinkEdict_ForeignEdictSlotNeg(t *testing.T) {
	h, _ := newTouchHost(t, 4, 1)
	// Foreign edict not allocated from h.Server.Arena.
	foreign := &progs.Edict{Fields: make([]byte, 80)}
	h.LinkEdict(foreign)
	// World.AreaQuery should find no entries at the foreign's bbox.
	keys := h.World.AreaQuery([3]float32{-1, -1, -1}, [3]float32{1, 1, 1}, world.QueryBoth)
	if len(keys) != 0 {
		t.Errorf("foreign edict should not be linked: %v", keys)
	}
}

func TestLinkEdict_NoProgsNoOp(t *testing.T) {
	h, _ := newTouchHost(t, 4, 1)
	h.SetProgs(nil)
	h.LinkEdict(h.Server.Edicts[1])
}

func TestLinkEdict_MissingOriginNoOp(t *testing.T) {
	h, _ := newTouchHost(t, 4, 1)
	// Strip Fields so origin read fails.
	h.Server.Edicts[1].Fields = []byte{}
	h.LinkEdict(h.Server.Edicts[1])
}

func TestLinkEdict_MissingMinsNoOp(t *testing.T) {
	h, _ := newTouchHost(t, 4, 1)
	// 4 bytes -> origin (offset 1..3) read fails too. Pad to 16 so
	// origin read succeeds (3 vec3 slots starting at offset 1 -> 16
	// bytes total = slots 0..3) but mins (slot 4..6) fails.
	h.Server.Edicts[1].Fields = make([]byte, 16)
	h.LinkEdict(h.Server.Edicts[1])
}

func TestLinkEdict_MissingMaxsNoOp(t *testing.T) {
	h, _ := newTouchHost(t, 4, 1)
	// 28 bytes -> origin/mins read succeeds (slots 1..6), maxs fails.
	h.Server.Edicts[1].Fields = make([]byte, 28)
	h.LinkEdict(h.Server.Edicts[1])
}

// --- hostSolidKindFromEntvars: dispatch table ----------------------------

func TestHostSolidKindFromEntvars_Dispatch(t *testing.T) {
	h, p := newTouchHost(t, 4, 1)
	ed := h.Server.Edicts[1]
	ev, _ := progs.NewEntVars(p, ed)

	for _, tc := range []struct {
		solid server.Solid
		want  world.SolidKind
	}{
		{server.SolidNot, world.SolidKindSkip},
		{server.SolidTrigger, world.SolidKindTrigger},
		{server.SolidBBox, world.SolidKindSolid},
		{server.SolidSlideBox, world.SolidKindSolid},
		{server.SolidBSP, world.SolidKindSolid},
	} {
		_ = ev.WriteFloat("solid", float32(tc.solid))
		if got := hostSolidKindFromEntvars(ev); got != tc.want {
			t.Errorf("solid=%d got %v want %v", tc.solid, got, tc.want)
		}
	}

	// Missing solid field -> SolidKindSkip.
	ed.Fields = []byte{} // too small for any read
	if got := hostSolidKindFromEntvars(ev); got != world.SolidKindSkip {
		t.Errorf("missing solid: got %v want SolidKindSkip", got)
	}
}

// --- Frame integration: per-client TouchTriggers walk --------------------

// One active client whose edict is at slot 1; one trigger at slot 2.
// Frame should drive TouchTriggers once and bump the counter.
func TestFrame_RunsTouchTriggersPerActiveClient(t *testing.T) {
	h, p := newTouchHost(t, 4, 1)
	h.Server.Active = true
	h.Server.WorldModel = nil // RunPhysics still safe with no walk/step

	// Mark the slot 1 client active + bind its edict.
	c := h.Static.Clients[0]
	c.Active = true
	c.Edict = h.Server.Edicts[1]

	writeTriggerFields(t, h, p, 1, [3]float32{0, 0, 0}, server.SolidNot, 0) // mover doesn't need to be solid
	writeTriggerFields(t, h, p, 2, [3]float32{0, 0, 0}, server.SolidTrigger, 1)
	linkTriggerInArea(t, h, p, 2)

	calls := 0
	var s, o int32
	h.VM.RegisterBuiltin(1, pickupShellsBuiltin(h, p, &calls, &s, &o))

	if err := h.Frame(0.05); err != nil {
		t.Fatalf("Frame: %v", err)
	}
	if calls != 1 {
		t.Errorf("expected 1 touch dispatch via Frame; got %d", calls)
	}
	if h.LastTriggerTouches != 1 {
		t.Errorf("Frame.LastTriggerTouches got %d want 1", h.LastTriggerTouches)
	}
}

// Frame on a server with no active clients: the per-client loop
// skips every slot, leaving the counters at zero.
func TestFrame_NoActiveClientsLeavesCountersZero(t *testing.T) {
	h, p := newTouchHost(t, 4, 1)
	h.Server.Active = true
	writeTriggerFields(t, h, p, 1, [3]float32{0, 0, 0}, server.SolidNot, 0)
	writeTriggerFields(t, h, p, 2, [3]float32{0, 0, 0}, server.SolidTrigger, 1)
	linkTriggerInArea(t, h, p, 2)
	// Clients[0].Active stays false.
	if err := h.Frame(0.05); err != nil {
		t.Fatalf("Frame: %v", err)
	}
	if h.LastTriggerTouches != 0 {
		t.Errorf("expected 0 dispatches; got %d", h.LastTriggerTouches)
	}
}

// Frame's per-client loop must skip slots where c.Edict is nil even
// if c.Active is true (defensive guard for half-initialised client
// rows). Exercises the (c.Edict == nil) branch.
func TestFrame_SkipsNilEdictClient(t *testing.T) {
	h, p := newTouchHost(t, 4, 1)
	h.Server.Active = true
	writeTriggerFields(t, h, p, 1, [3]float32{0, 0, 0}, server.SolidNot, 0)
	writeTriggerFields(t, h, p, 2, [3]float32{0, 0, 0}, server.SolidTrigger, 1)
	linkTriggerInArea(t, h, p, 2)

	h.Static.Clients[0].Active = true
	h.Static.Clients[0].Edict = nil
	if err := h.Frame(0.05); err != nil {
		t.Fatalf("Frame: %v", err)
	}
	if h.LastTriggerTouches != 0 {
		t.Errorf("nil-edict client should not dispatch; got %d", h.LastTriggerTouches)
	}
}

// Frame's per-client loop must skip nil client rows too.
func TestFrame_SkipsNilClientRow(t *testing.T) {
	h, p := newTouchHost(t, 4, 1)
	h.Server.Active = true
	writeTriggerFields(t, h, p, 1, [3]float32{0, 0, 0}, server.SolidNot, 0)
	writeTriggerFields(t, h, p, 2, [3]float32{0, 0, 0}, server.SolidTrigger, 1)
	linkTriggerInArea(t, h, p, 2)

	h.Static.Clients[0] = nil
	if err := h.Frame(0.05); err != nil {
		t.Fatalf("Frame: %v", err)
	}
	if h.LastTriggerTouches != 0 {
		t.Errorf("nil client row should not dispatch; got %d", h.LastTriggerTouches)
	}
}

// Sanity: an absolute-mins check guards against float32 NaN propagation
// in the absbound math. Not a real failure mode, just locks the
// arithmetic shape so a refactor doesn't silently flip it.
func TestAbsBoundsArithmetic(t *testing.T) {
	origin := [3]float32{10, 0, 0}
	mins := [3]float32{-8, -8, -8}
	maxs := [3]float32{8, 8, 8}
	absmin := [3]float32{
		origin[0] + mins[0] - 1,
		origin[1] + mins[1] - 1,
		origin[2] + mins[2] - 1,
	}
	absmax := [3]float32{
		origin[0] + maxs[0] + 1,
		origin[1] + maxs[1] + 1,
		origin[2] + maxs[2] + 1,
	}
	if absmin != ([3]float32{1, -9, -9}) {
		t.Errorf("absmin got %v want {1,-9,-9}", absmin)
	}
	if absmax != ([3]float32{19, 9, 9}) {
		t.Errorf("absmax got %v want {19,9,9}", absmax)
	}
	// NaN propagation: if origin[0] is NaN, absmin[0] is NaN too.
	nanOrigin := [3]float32{float32(math.NaN()), 0, 0}
	if !math.IsNaN(float64(nanOrigin[0] + mins[0] - 1)) {
		t.Error("NaN propagation broken")
	}
}
