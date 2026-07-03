// Copyright (c) 2026 the go-quake1/engine authors.
// SPDX-License-Identifier: GPL-2.0-or-later

package world

import (
	"errors"
	"testing"

	"github.com/go-quake1/engine/bspfile"
	"github.com/go-quake1/engine/bsptrace"
	"github.com/go-quake1/engine/model"
	"github.com/go-quake1/engine/progs"
	"github.com/go-quake1/engine/server"
)

// --- fixtures --------------------------------------------------------------

// groundProgs builds a Progs stub with every field PhysicsStep /
// PhysicsWalk reach for: nextthink + think (RunThink), flags,
// origin, velocity, mins, maxs, v_angle (PhysicsWalk's view-angle
// source), plus an optional `gravity` field gated by withGravity
// so the absent-field branch of readStepGravityFactor is exercised.
//
//	ofs  1     nextthink   (float)
//	ofs  2     think       (function)
//	ofs  3     flags       (float; QC stores the FL_* bitfield as float)
//	ofs  4..6  origin      (vector)
//	ofs  7..9  velocity    (vector)
//	ofs 10..12 mins        (vector)
//	ofs 13..15 maxs        (vector)
//	ofs 16..18 v_angle     (vector)
//	ofs 19     gravity     (float, optional)
//
// EntityFields = 24 reserves enough room.
func groundProgs(withGravity bool) *progs.Progs {
	strs := []byte{0}
	add := func(s string) int32 {
		ofs := int32(len(strs))
		strs = append(strs, []byte(s)...)
		strs = append(strs, 0)
		return ofs
	}
	nextthink := add("nextthink")
	think := add("think")
	flags := add("flags")
	origin := add("origin")
	velocity := add("velocity")
	mins := add("mins")
	maxs := add("maxs")
	vAngle := add("v_angle")
	gravity := add("gravity")
	defs := []progs.Def{
		{Type: uint16(progs.EvFloat), Ofs: 1, SName: nextthink},
		{Type: uint16(progs.EvFunction), Ofs: 2, SName: think},
		{Type: uint16(progs.EvFloat), Ofs: 3, SName: flags},
		{Type: uint16(progs.EvVector), Ofs: 4, SName: origin},
		{Type: uint16(progs.EvVector), Ofs: 7, SName: velocity},
		{Type: uint16(progs.EvVector), Ofs: 10, SName: mins},
		{Type: uint16(progs.EvVector), Ofs: 13, SName: maxs},
		{Type: uint16(progs.EvVector), Ofs: 16, SName: vAngle},
	}
	if withGravity {
		defs = append(defs, progs.Def{Type: uint16(progs.EvFloat), Ofs: 19, SName: gravity})
	}
	return &progs.Progs{
		Header:    progs.Header{EntityFields: 24},
		Strings:   strs,
		FieldDefs: defs,
	}
}

// newGroundEnt allocates a fresh Edict on the standard groundProgs
// stub + hands back the matching EntVars + Progs.
func newGroundEnt(t *testing.T, withGravity bool) (*progs.Edict, *progs.EntVars, *progs.Progs) {
	t.Helper()
	p := groundProgs(withGravity)
	a := progs.NewEdictArena(p, 2)
	a.Reset()
	e, _, err := a.Alloc()
	if err != nil {
		t.Fatalf("Alloc: %v", err)
	}
	v, err := progs.NewEntVars(p, e)
	if err != nil {
		t.Fatalf("NewEntVars: %v", err)
	}
	return e, v, p
}

// newGroundEntFromProgs allocates a fresh Edict from a caller-supplied
// Progs (used by the missing-field / corrupt-field tests).
func newGroundEntFromProgs(t *testing.T, p *progs.Progs) (*progs.Edict, *progs.EntVars) {
	t.Helper()
	a := progs.NewEdictArena(p, 2)
	a.Reset()
	e, _, err := a.Alloc()
	if err != nil {
		t.Fatalf("Alloc: %v", err)
	}
	v, err := progs.NewEntVars(p, e)
	if err != nil {
		t.Fatalf("NewEntVars: %v", err)
	}
	return e, v
}

// groundDropField clones the standard groundProgs stub minus one
// field def by name -- so the corresponding EntVars read returns
// ErrFieldNotFound.
func groundDropField(omit string) *progs.Progs {
	p := groundProgs(true)
	kept := p.FieldDefs[:0]
	for _, d := range p.FieldDefs {
		if groundReadName(p, d.SName) == omit {
			continue
		}
		kept = append(kept, d)
	}
	p.FieldDefs = kept
	return p
}

// groundReplaceFieldType clones the standard groundProgs stub with a
// single field's QC type rewritten -- used by the type-mismatch path
// on `gravity`.
func groundReplaceFieldType(name string, t Etype) *progs.Progs {
	p := groundProgs(true)
	for i := range p.FieldDefs {
		if groundReadName(p, p.FieldDefs[i].SName) == name {
			p.FieldDefs[i].Type = uint16(t)
		}
	}
	return p
}

func groundReadName(p *progs.Progs, ofs int32) string {
	if ofs < 0 || int(ofs) >= len(p.Strings) {
		return ""
	}
	end := int(ofs)
	for end < len(p.Strings) && p.Strings[end] != 0 {
		end++
	}
	return string(p.Strings[ofs:end])
}

func groundVec3ApproxEq(a, b [3]float32, tol float32) bool {
	for i := 0; i < 3; i++ {
		d := a[i] - b[i]
		if d < 0 {
			d = -d
		}
		if d > tol {
			return false
		}
	}
	return true
}

func groundNoThink(t *testing.T) server.ThinkCaller {
	t.Helper()
	return func(*progs.Edict, int32) error {
		t.Errorf("ThinkCaller invoked unexpectedly")
		return nil
	}
}

// groundEmptyWorld returns a brushmodel whose hull 0 is "every leaf
// empty" -- traces never impact.
func groundEmptyWorld() *model.BrushModel {
	bm := &model.BrushModel{}
	hull := bsptrace.Hull{
		ClipNodes: []bspfile.ClipNode{
			{PlaneNum: 0, Children: [2]int16{bspfile.ContentsEmpty, bspfile.ContentsEmpty}},
		},
		Planes:        []bspfile.Plane{{Normal: [3]float32{1, 0, 0}, Type: bspfile.PlaneX}},
		FirstClipNode: 0,
		LastClipNode:  0,
	}
	bm.Hulls[0] = hull
	bm.Hulls[1] = hull
	bm.Hulls[2] = hull
	return bm
}

// groundFloorWorld returns a brushmodel whose hull 0 has a horizontal
// floor at z=0: z >= 0 is empty, z < 0 is solid. CheckBottom on an
// entity centered above z=0 with a small bbox supports it.
func groundFloorWorld() *model.BrushModel {
	bm := &model.BrushModel{}
	hull := bsptrace.Hull{
		ClipNodes: []bspfile.ClipNode{
			{PlaneNum: 0, Children: [2]int16{bspfile.ContentsEmpty, bspfile.ContentsSolid}},
		},
		Planes: []bspfile.Plane{
			{Normal: [3]float32{0, 0, 1}, Dist: 0, Type: bspfile.PlaneZ},
		},
		FirstClipNode: 0,
		LastClipNode:  0,
	}
	bm.Hulls[0] = hull
	bm.Hulls[1] = hull
	bm.Hulls[2] = hull
	return bm
}

// groundCorruptWorld returns a brushmodel whose hull 0 references a
// non-existent plane -- any trace returns bsptrace.ErrBadPlaneIndex.
func groundCorruptWorld() *model.BrushModel {
	bm := &model.BrushModel{}
	corrupt := bsptrace.Hull{
		ClipNodes: []bspfile.ClipNode{
			{PlaneNum: 99, Children: [2]int16{bspfile.ContentsEmpty, bspfile.ContentsEmpty}},
		},
		Planes:        []bspfile.Plane{{Normal: [3]float32{1, 0, 0}, Type: bspfile.PlaneX}},
		FirstClipNode: 0,
		LastClipNode:  0,
	}
	bm.Hulls[0] = corrupt
	bm.Hulls[1] = corrupt
	bm.Hulls[2] = corrupt
	return bm
}

// seedGroundBaseline writes a standard starting state into ev: tiny
// bounding box centred on origin, zero velocity / v_angle / flags.
func seedGroundBaseline(t *testing.T, ev *progs.EntVars) {
	t.Helper()
	if err := ev.WriteVec3("mins", [3]float32{-1, -1, -1}); err != nil {
		t.Fatal(err)
	}
	if err := ev.WriteVec3("maxs", [3]float32{1, 1, 1}); err != nil {
		t.Fatal(err)
	}
}

// --- PhysicsStep -----------------------------------------------------------

// PhysicsStep on an airborne entity (no ground/fly/swim flag): gravity
// applies, velocity.z becomes negative, origin advances by the
// post-gravity velocity over dt.
func TestPhysicsStep_AirborneAppliesGravity(t *testing.T) {
	ent, ev, _ := newGroundEnt(t, true)
	seedGroundBaseline(t, ev)
	if err := ev.WriteVec3("origin", [3]float32{0, 0, 100}); err != nil {
		t.Fatal(err)
	}
	ctx := PhysicsContext{
		Worldmodel:  groundEmptyWorld(),
		Now:         1.0,
		Dt:          0.1,
		ThinkCaller: groundNoThink(t),
	}
	alive, err := PhysicsStep(ent, ev, 0, server.DefaultPhysParams(), ctx)
	if !alive || err != nil {
		t.Fatalf("alive=%v err=%v want true, nil", alive, err)
	}
	gotV, _ := ev.ReadVec3("velocity")
	wantV := [3]float32{0, 0, -80} // 800 * 0.1
	if !groundVec3ApproxEq(gotV, wantV, 1e-3) {
		t.Errorf("velocity: got %v want %v", gotV, wantV)
	}
}

// PhysicsStep on an FL_ONGROUND entity: gravity / integration skipped
// entirely. Velocity + origin survive unchanged.
func TestPhysicsStep_OnGroundSkipsIntegration(t *testing.T) {
	ent, ev, _ := newGroundEnt(t, true)
	seedGroundBaseline(t, ev)
	if err := ev.WriteVec3("origin", [3]float32{0, 0, 50}); err != nil {
		t.Fatal(err)
	}
	if err := ev.WriteVec3("velocity", [3]float32{7, 8, 9}); err != nil {
		t.Fatal(err)
	}
	if err := ev.WriteFloat("flags", float32(int32(server.FlagOnGround))); err != nil {
		t.Fatal(err)
	}
	ctx := PhysicsContext{
		Worldmodel:  groundCorruptWorld(), // would error if any trace ran
		Now:         1.0,
		Dt:          0.1,
		ThinkCaller: groundNoThink(t),
	}
	alive, err := PhysicsStep(ent, ev, 0, server.DefaultPhysParams(), ctx)
	if !alive || err != nil {
		t.Fatalf("alive=%v err=%v want true, nil", alive, err)
	}
	if gotV, _ := ev.ReadVec3("velocity"); gotV != ([3]float32{7, 8, 9}) {
		t.Errorf("velocity mutated on grounded skip: got %v", gotV)
	}
	if gotO, _ := ev.ReadVec3("origin"); gotO != ([3]float32{0, 0, 50}) {
		t.Errorf("origin mutated on grounded skip: got %v", gotO)
	}
}

// PhysicsStep with a clear path: MoveStep commits the new origin.
// Use a falling entity well above an empty world's nonexistent floor;
// MoveStep's "walked off an edge" branch fires (Fraction == 1 ->
// !FlagOnGround so commit). Origin advances by velocity * dt.
func TestPhysicsStep_ClearMoveCommitsOrigin(t *testing.T) {
	ent, ev, _ := newGroundEnt(t, true)
	seedGroundBaseline(t, ev)
	if err := ev.WriteVec3("origin", [3]float32{0, 0, 100}); err != nil {
		t.Fatal(err)
	}
	// Start with a tiny negative velocity so post-gravity velocity is
	// non-zero but the move (velocity*dt) is small.
	if err := ev.WriteVec3("velocity", [3]float32{10, 0, 0}); err != nil {
		t.Fatal(err)
	}
	ctx := PhysicsContext{
		Worldmodel:  groundEmptyWorld(),
		Now:         1.0,
		Dt:          0.1,
		ThinkCaller: groundNoThink(t),
	}
	alive, err := PhysicsStep(ent, ev, 0, server.DefaultPhysParams(), ctx)
	if !alive || err != nil {
		t.Fatalf("alive=%v err=%v want true, nil", alive, err)
	}
	gotO, _ := ev.ReadVec3("origin")
	// MoveStep's walking algorithm: end = origin + move = (1, 0, 92).
	// The step-up/step-down trace in an empty world has Fraction == 1
	// AND FlagOnGround is NOT set (we're airborne), so MoveStep
	// commits NewOrigin = wantedXY = (1, 0, 92).
	wantO := [3]float32{1, 0, 92}
	if !groundVec3ApproxEq(gotO, wantO, 1e-3) {
		t.Errorf("origin: got %v want %v", gotO, wantO)
	}
}

// PhysicsStep when MoveStep refuses the step (floor lost, not
// PARTIALGROUND) AND the entity is airborne. We set FL_ONGROUND on
// the entity's input flags so MoveStep's "walked off an edge -> bail"
// branch fires (Fraction == 1.0 AND FlagOnGround set inside MoveStep).
// But the outer PhysicsStep guard `airborne = !(FL_ONGROUND | ...)`
// would skip integration entirely. So we need a different setup:
// airborne flag clear, but the move bumps into a wall and MoveStep
// refuses. Approach: use a floor world (z >= 0 empty, z < 0 solid)
// and place the entity at z=2 with downward move that puts the step-
// up origin in solid -> MoveStep AllSolid -> Moved=false. With
// FlagPartialGround clear, the integration arm keeps origin but
// writes the gravity-modified velocity.
func TestPhysicsStep_MoveStepRefusedNoPartialGround(t *testing.T) {
	ent, ev, _ := newGroundEnt(t, true)
	seedGroundBaseline(t, ev)
	// Origin BELOW the floor (z = -50, which is in the SOLID half-
	// space of groundFloorWorld). The step-up trace inside MoveStep
	// starts deep in solid -> AllSolid -> Moved=false.
	if err := ev.WriteVec3("origin", [3]float32{0, 0, -50}); err != nil {
		t.Fatal(err)
	}
	if err := ev.WriteVec3("velocity", [3]float32{0, 0, 0}); err != nil {
		t.Fatal(err)
	}
	ctx := PhysicsContext{
		Worldmodel:  groundFloorWorld(),
		Now:         1.0,
		Dt:          0.1,
		ThinkCaller: groundNoThink(t),
	}
	alive, err := PhysicsStep(ent, ev, 0, server.DefaultPhysParams(), ctx)
	if !alive || err != nil {
		t.Fatalf("alive=%v err=%v want true, nil", alive, err)
	}
	// Origin survives (PARTIALGROUND clear -> no commit).
	gotO, _ := ev.ReadVec3("origin")
	if !groundVec3ApproxEq(gotO, [3]float32{0, 0, -50}, 1e-3) {
		t.Errorf("origin should survive refused step (no PARTIALGROUND): got %v", gotO)
	}
	// Velocity gets the gravity-modified value (-80 z).
	gotV, _ := ev.ReadVec3("velocity")
	wantV := [3]float32{0, 0, -80}
	if !groundVec3ApproxEq(gotV, wantV, 1e-3) {
		t.Errorf("velocity: got %v want %v", gotV, wantV)
	}
}

// PhysicsStep with FL_PARTIALGROUND set + MoveStep refusal: the
// "monster had the ground pulled out" branch commits the raw move
// AND the gravity-modified velocity. Use the same buried-in-solid
// setup as above but flip the PartialGround bit on the input.
func TestPhysicsStep_MoveStepRefusedPartialGroundFalls(t *testing.T) {
	ent, ev, _ := newGroundEnt(t, true)
	seedGroundBaseline(t, ev)
	if err := ev.WriteVec3("origin", [3]float32{0, 0, -50}); err != nil {
		t.Fatal(err)
	}
	if err := ev.WriteVec3("velocity", [3]float32{2, 3, 0}); err != nil {
		t.Fatal(err)
	}
	// FL_PARTIALGROUND set BUT FL_ONGROUND clear (the airborne gate
	// requires onground|fly|swim all clear). PARTIALGROUND alone
	// passes through the airborne gate.
	if err := ev.WriteFloat("flags", float32(int32(server.FlagPartialGround))); err != nil {
		t.Fatal(err)
	}
	ctx := PhysicsContext{
		Worldmodel:  groundFloorWorld(),
		Now:         1.0,
		Dt:          0.1,
		ThinkCaller: groundNoThink(t),
	}
	alive, err := PhysicsStep(ent, ev, 0, server.DefaultPhysParams(), ctx)
	if !alive || err != nil {
		t.Fatalf("alive=%v err=%v want true, nil", alive, err)
	}
	gotO, _ := ev.ReadVec3("origin")
	// move = velocity * dt = (0.2, 0.3, -8) (post-gravity z = -80*0.1).
	// PARTIALGROUND commits origin = (-50) + move.
	wantO := [3]float32{0.2, 0.3, -58}
	if !groundVec3ApproxEq(gotO, wantO, 1e-3) {
		t.Errorf("origin: got %v want %v (PARTIALGROUND override should fall)", gotO, wantO)
	}
}

// PhysicsStep with MoveStep returning an error: the corrupt world
// surfaces bsptrace.ErrBadPlaneIndex through MoveStep -> PhysicsStep.
func TestPhysicsStep_MoveStepError(t *testing.T) {
	ent, ev, _ := newGroundEnt(t, true)
	seedGroundBaseline(t, ev)
	if err := ev.WriteVec3("origin", [3]float32{0, 0, 100}); err != nil {
		t.Fatal(err)
	}
	if err := ev.WriteVec3("velocity", [3]float32{5, 0, 0}); err != nil {
		t.Fatal(err)
	}
	ctx := PhysicsContext{
		Worldmodel:  groundCorruptWorld(),
		Now:         1.0,
		Dt:          0.1,
		ThinkCaller: groundNoThink(t),
	}
	alive, err := PhysicsStep(ent, ev, 0, server.DefaultPhysParams(), ctx)
	if alive || err == nil {
		t.Errorf("alive=%v err=%v want false + non-nil", alive, err)
	}
}

// PhysicsStep gravity-field absent: defaults to 1.0 (stepDefaultGravity).
func TestPhysicsStep_AbsentGravityFieldDefaults(t *testing.T) {
	ent, ev, _ := newGroundEnt(t, false) // no gravity field
	seedGroundBaseline(t, ev)
	if err := ev.WriteVec3("origin", [3]float32{0, 0, 100}); err != nil {
		t.Fatal(err)
	}
	ctx := PhysicsContext{
		Worldmodel:  groundEmptyWorld(),
		Now:         1.0,
		Dt:          0.1,
		ThinkCaller: groundNoThink(t),
	}
	alive, err := PhysicsStep(ent, ev, 0, server.DefaultPhysParams(), ctx)
	if !alive || err != nil {
		t.Fatalf("alive=%v err=%v want true, nil", alive, err)
	}
	gotV, _ := ev.ReadVec3("velocity")
	if !groundVec3ApproxEq(gotV, [3]float32{0, 0, -80}, 1e-3) {
		t.Errorf("velocity: got %v want (0,0,-80) (gravity defaults to 1.0)", gotV)
	}
}

// PhysicsStep think dispatch error: surfaced as (false, err).
func TestPhysicsStep_ThinkCallerError(t *testing.T) {
	ent, ev, _ := newGroundEnt(t, true)
	seedGroundBaseline(t, ev)
	if err := ev.WriteVec3("origin", [3]float32{0, 0, 100}); err != nil {
		t.Fatal(err)
	}
	if err := ev.WriteFloat("nextthink", 0.5); err != nil {
		t.Fatal(err)
	}
	if err := ev.WriteInt32("think", 1); err != nil {
		t.Fatal(err)
	}
	want := errors.New("step think boom")
	ctx := PhysicsContext{
		Worldmodel: groundEmptyWorld(),
		Now:        1.0,
		Dt:         0.1,
		ThinkCaller: func(*progs.Edict, int32) error {
			return want
		},
	}
	alive, err := PhysicsStep(ent, ev, 0, server.DefaultPhysParams(), ctx)
	if alive || !errors.Is(err, want) {
		t.Errorf("alive=%v err=%v want false, %v", alive, err, want)
	}
}

// PhysicsStep with a nil ThinkCaller after a clean move surfaces
// ErrNoThinkCaller via RunThink.
func TestPhysicsStep_NilThinkCallerAfterMove(t *testing.T) {
	ent, ev, _ := newGroundEnt(t, true)
	seedGroundBaseline(t, ev)
	if err := ev.WriteVec3("origin", [3]float32{0, 0, 100}); err != nil {
		t.Fatal(err)
	}
	ctx := PhysicsContext{
		Worldmodel: groundEmptyWorld(),
		Now:        1.0,
		Dt:         0.1,
		// ThinkCaller: nil
	}
	alive, err := PhysicsStep(ent, ev, 0, server.DefaultPhysParams(), ctx)
	if alive || !errors.Is(err, server.ErrNoThinkCaller) {
		t.Errorf("alive=%v err=%v want false, ErrNoThinkCaller", alive, err)
	}
}

// --- PhysicsStep: missing-field paths --------------------------------------

func TestPhysicsStep_MissingFlags(t *testing.T) {
	p := groundDropField("flags")
	ent, ev := newGroundEntFromProgs(t, p)
	ctx := PhysicsContext{
		Worldmodel:  groundEmptyWorld(),
		Now:         1.0,
		Dt:          0.1,
		ThinkCaller: groundNoThink(t),
	}
	alive, err := PhysicsStep(ent, ev, 0, server.DefaultPhysParams(), ctx)
	if alive || !errors.Is(err, progs.ErrFieldNotFound) {
		t.Errorf("alive=%v err=%v want false, ErrFieldNotFound", alive, err)
	}
}

func TestPhysicsStep_MissingVelocity(t *testing.T) {
	p := groundDropField("velocity")
	ent, ev := newGroundEntFromProgs(t, p)
	ctx := PhysicsContext{
		Worldmodel:  groundEmptyWorld(),
		Now:         1.0,
		Dt:          0.1,
		ThinkCaller: groundNoThink(t),
	}
	alive, err := PhysicsStep(ent, ev, 0, server.DefaultPhysParams(), ctx)
	if alive || !errors.Is(err, progs.ErrFieldNotFound) {
		t.Errorf("alive=%v err=%v want false, ErrFieldNotFound", alive, err)
	}
}

func TestPhysicsStep_MissingOrigin(t *testing.T) {
	p := groundDropField("origin")
	ent, ev := newGroundEntFromProgs(t, p)
	ctx := PhysicsContext{
		Worldmodel:  groundEmptyWorld(),
		Now:         1.0,
		Dt:          0.1,
		ThinkCaller: groundNoThink(t),
	}
	alive, err := PhysicsStep(ent, ev, 0, server.DefaultPhysParams(), ctx)
	if alive || !errors.Is(err, progs.ErrFieldNotFound) {
		t.Errorf("alive=%v err=%v want false, ErrFieldNotFound", alive, err)
	}
}

func TestPhysicsStep_MissingMins(t *testing.T) {
	p := groundDropField("mins")
	ent, ev := newGroundEntFromProgs(t, p)
	ctx := PhysicsContext{
		Worldmodel:  groundEmptyWorld(),
		Now:         1.0,
		Dt:          0.1,
		ThinkCaller: groundNoThink(t),
	}
	alive, err := PhysicsStep(ent, ev, 0, server.DefaultPhysParams(), ctx)
	if alive || !errors.Is(err, progs.ErrFieldNotFound) {
		t.Errorf("alive=%v err=%v want false, ErrFieldNotFound", alive, err)
	}
}

func TestPhysicsStep_MissingMaxs(t *testing.T) {
	p := groundDropField("maxs")
	ent, ev := newGroundEntFromProgs(t, p)
	ctx := PhysicsContext{
		Worldmodel:  groundEmptyWorld(),
		Now:         1.0,
		Dt:          0.1,
		ThinkCaller: groundNoThink(t),
	}
	alive, err := PhysicsStep(ent, ev, 0, server.DefaultPhysParams(), ctx)
	if alive || !errors.Is(err, progs.ErrFieldNotFound) {
		t.Errorf("alive=%v err=%v want false, ErrFieldNotFound", alive, err)
	}
}

// PhysicsStep gravity field with the WRONG QC type: readStepGravityFactor
// surfaces ErrFieldTypeMismatch (the "corrupt-progs" branch).
func TestPhysicsStep_GravityFieldWrongType(t *testing.T) {
	p := groundReplaceFieldType("gravity", progs.EvVector)
	ent, ev := newGroundEntFromProgs(t, p)
	seedGroundBaseline(t, ev)
	ctx := PhysicsContext{
		Worldmodel:  groundEmptyWorld(),
		Now:         1.0,
		Dt:          0.1,
		ThinkCaller: groundNoThink(t),
	}
	alive, err := PhysicsStep(ent, ev, 0, server.DefaultPhysParams(), ctx)
	if alive || !errors.Is(err, progs.ErrFieldTypeMismatch) {
		t.Errorf("alive=%v err=%v want false, ErrFieldTypeMismatch", alive, err)
	}
}

// --- PhysicsWalk -----------------------------------------------------------

// PhysicsWalk on ground with zero cmd + zero velocity: no movement.
// Friction has nothing to scale (speed=0); CalcWishVel returns zero;
// Accelerate's addspeed gate short-circuits; PushEntity does a zero
// delta. Origin survives.
func TestPhysicsWalk_StationaryOnGroundZeroCmd(t *testing.T) {
	ent, ev, _ := newGroundEnt(t, true)
	seedGroundBaseline(t, ev)
	if err := ev.WriteVec3("origin", [3]float32{50, 50, 10}); err != nil {
		t.Fatal(err)
	}
	if err := ev.WriteFloat("flags", float32(int32(server.FlagOnGround))); err != nil {
		t.Fatal(err)
	}
	ctx := PhysicsContext{
		Worldmodel:  groundFloorWorld(),
		Now:         1.0,
		Dt:          0.1,
		ThinkCaller: groundNoThink(t),
	}
	cmd := server.UserCmd{}
	alive, err := PhysicsWalk(ent, ev, 0, cmd, server.DefaultPhysParams(), ctx)
	if !alive || err != nil {
		t.Fatalf("alive=%v err=%v want true, nil", alive, err)
	}
	gotO, _ := ev.ReadVec3("origin")
	if !groundVec3ApproxEq(gotO, [3]float32{50, 50, 10}, 1e-3) {
		t.Errorf("origin should not move on zero cmd: got %v", gotO)
	}
	gotV, _ := ev.ReadVec3("velocity")
	if !groundVec3ApproxEq(gotV, [3]float32{0, 0, 0}, 1e-3) {
		t.Errorf("velocity should be zero: got %v", gotV)
	}
}

// PhysicsWalk on ground with forward cmd: velocity gains a forward
// component (toward +X when v_angle is zero -- AngleVectors at zero
// rotation gives forward=+X). Origin advances toward +X.
func TestPhysicsWalk_ForwardCmdGainsForwardVelocity(t *testing.T) {
	ent, ev, _ := newGroundEnt(t, true)
	seedGroundBaseline(t, ev)
	if err := ev.WriteVec3("origin", [3]float32{0, 0, 10}); err != nil {
		t.Fatal(err)
	}
	if err := ev.WriteFloat("flags", float32(int32(server.FlagOnGround))); err != nil {
		t.Fatal(err)
	}
	// v_angle = zero -> forward = (1, 0, 0).
	ctx := PhysicsContext{
		Worldmodel:  groundFloorWorld(),
		Now:         1.0,
		Dt:          0.1,
		ThinkCaller: groundNoThink(t),
	}
	cmd := server.UserCmd{ForwardMove: 200}
	alive, err := PhysicsWalk(ent, ev, 0, cmd, server.DefaultPhysParams(), ctx)
	if !alive || err != nil {
		t.Fatalf("alive=%v err=%v want true, nil", alive, err)
	}
	gotV, _ := ev.ReadVec3("velocity")
	if gotV[0] <= 0 {
		t.Errorf("velocity[0] should be positive after forward cmd: got %v", gotV[0])
	}
	gotO, _ := ev.ReadVec3("origin")
	if gotO[0] <= 0 {
		t.Errorf("origin[0] should advance forward: got %v", gotO[0])
	}
}

// PhysicsWalk airborne (no FL_ONGROUND): no friction is applied,
// gravity DOES apply. Start with a horizontal velocity and zero z;
// after the tick z is negative AND the horizontal velocity is
// unchanged (no friction in air).
func TestPhysicsWalk_AirborneAppliesGravityNoFriction(t *testing.T) {
	ent, ev, _ := newGroundEnt(t, true)
	seedGroundBaseline(t, ev)
	if err := ev.WriteVec3("origin", [3]float32{0, 0, 100}); err != nil {
		t.Fatal(err)
	}
	if err := ev.WriteVec3("velocity", [3]float32{100, 0, 0}); err != nil {
		t.Fatal(err)
	}
	// No FL_ONGROUND -- airborne.
	ctx := PhysicsContext{
		Worldmodel:  groundEmptyWorld(),
		Now:         1.0,
		Dt:          0.1,
		ThinkCaller: groundNoThink(t),
	}
	cmd := server.UserCmd{} // no input
	alive, err := PhysicsWalk(ent, ev, 0, cmd, server.DefaultPhysParams(), ctx)
	if !alive || err != nil {
		t.Fatalf("alive=%v err=%v want true, nil", alive, err)
	}
	gotV, _ := ev.ReadVec3("velocity")
	// Gravity applied: z went from 0 to -80.
	if gotV[2] >= 0 {
		t.Errorf("velocity[2] should be negative (gravity in air): got %v", gotV[2])
	}
	// Horizontal velocity preserved (no friction in air).
	if gotV[0] != 100 {
		t.Errorf("velocity[0] should be preserved (no friction in air): got %v want 100", gotV[0])
	}
}

// PhysicsWalk think dispatch error -> (false, err).
func TestPhysicsWalk_ThinkCallerError(t *testing.T) {
	ent, ev, _ := newGroundEnt(t, true)
	seedGroundBaseline(t, ev)
	if err := ev.WriteVec3("origin", [3]float32{0, 0, 10}); err != nil {
		t.Fatal(err)
	}
	if err := ev.WriteFloat("nextthink", 0.5); err != nil {
		t.Fatal(err)
	}
	if err := ev.WriteInt32("think", 1); err != nil {
		t.Fatal(err)
	}
	want := errors.New("walk think boom")
	ctx := PhysicsContext{
		Worldmodel: groundEmptyWorld(),
		Now:        1.0,
		Dt:         0.1,
		ThinkCaller: func(*progs.Edict, int32) error {
			return want
		},
	}
	cmd := server.UserCmd{}
	alive, err := PhysicsWalk(ent, ev, 0, cmd, server.DefaultPhysParams(), ctx)
	if alive || !errors.Is(err, want) {
		t.Errorf("alive=%v err=%v want false, %v", alive, err, want)
	}
}

// PhysicsWalk with PushEntity error: corrupt world surfaces a trace
// error.
func TestPhysicsWalk_PushEntityError(t *testing.T) {
	ent, ev, _ := newGroundEnt(t, true)
	seedGroundBaseline(t, ev)
	if err := ev.WriteVec3("origin", [3]float32{0, 0, 10}); err != nil {
		t.Fatal(err)
	}
	if err := ev.WriteVec3("velocity", [3]float32{100, 0, 0}); err != nil {
		t.Fatal(err)
	}
	ctx := PhysicsContext{
		Worldmodel:  groundCorruptWorld(),
		Now:         1.0,
		Dt:          0.1,
		ThinkCaller: groundNoThink(t),
	}
	cmd := server.UserCmd{}
	alive, err := PhysicsWalk(ent, ev, 0, cmd, server.DefaultPhysParams(), ctx)
	if alive || err == nil {
		t.Errorf("alive=%v err=%v want false + non-nil", alive, err)
	}
}

// PhysicsWalk with the gravity field absent (airborne path): defaults
// to 1.0.
func TestPhysicsWalk_AbsentGravityFieldDefaults(t *testing.T) {
	ent, ev, _ := newGroundEnt(t, false) // no gravity field
	seedGroundBaseline(t, ev)
	if err := ev.WriteVec3("origin", [3]float32{0, 0, 100}); err != nil {
		t.Fatal(err)
	}
	ctx := PhysicsContext{
		Worldmodel:  groundEmptyWorld(),
		Now:         1.0,
		Dt:          0.1,
		ThinkCaller: groundNoThink(t),
	}
	cmd := server.UserCmd{}
	alive, err := PhysicsWalk(ent, ev, 0, cmd, server.DefaultPhysParams(), ctx)
	if !alive || err != nil {
		t.Fatalf("alive=%v err=%v want true, nil", alive, err)
	}
	gotV, _ := ev.ReadVec3("velocity")
	if !groundVec3ApproxEq(gotV, [3]float32{0, 0, -80}, 1e-3) {
		t.Errorf("velocity: got %v want (0,0,-80) (gravity field absent -> 1.0)", gotV)
	}
}

// PhysicsWalk gravity-field wrong type (airborne path triggers the
// gravity lookup): surfaces ErrFieldTypeMismatch.
func TestPhysicsWalk_GravityFieldWrongType(t *testing.T) {
	p := groundReplaceFieldType("gravity", progs.EvVector)
	ent, ev := newGroundEntFromProgs(t, p)
	seedGroundBaseline(t, ev)
	// Airborne so the gravity lookup runs.
	ctx := PhysicsContext{
		Worldmodel:  groundEmptyWorld(),
		Now:         1.0,
		Dt:          0.1,
		ThinkCaller: groundNoThink(t),
	}
	cmd := server.UserCmd{}
	alive, err := PhysicsWalk(ent, ev, 0, cmd, server.DefaultPhysParams(), ctx)
	if alive || !errors.Is(err, progs.ErrFieldTypeMismatch) {
		t.Errorf("alive=%v err=%v want false, ErrFieldTypeMismatch", alive, err)
	}
}

// PhysicsWalk on ground with non-zero horizontal velocity + zero cmd:
// friction reduces the speed (but doesn't zero it in a single tick at
// default sv_friction).
func TestPhysicsWalk_OnGroundFrictionReducesSpeed(t *testing.T) {
	ent, ev, _ := newGroundEnt(t, true)
	seedGroundBaseline(t, ev)
	if err := ev.WriteVec3("origin", [3]float32{0, 0, 10}); err != nil {
		t.Fatal(err)
	}
	if err := ev.WriteVec3("velocity", [3]float32{200, 0, 0}); err != nil {
		t.Fatal(err)
	}
	if err := ev.WriteFloat("flags", float32(int32(server.FlagOnGround))); err != nil {
		t.Fatal(err)
	}
	ctx := PhysicsContext{
		Worldmodel:  groundFloorWorld(),
		Now:         1.0,
		Dt:          0.1,
		ThinkCaller: groundNoThink(t),
	}
	cmd := server.UserCmd{}
	alive, err := PhysicsWalk(ent, ev, 0, cmd, server.DefaultPhysParams(), ctx)
	if !alive || err != nil {
		t.Fatalf("alive=%v err=%v want true, nil", alive, err)
	}
	gotV, _ := ev.ReadVec3("velocity")
	// ApplyFriction with friction=4, stopSpeed=100, speed=200, dt=0.1:
	// control = max(200, 100) = 200; newspeed = 200 - 0.1 * 200 * 4 =
	// 200 - 80 = 120. So v[0] = 120 (positive, less than 200).
	if !(gotV[0] > 0 && gotV[0] < 200) {
		t.Errorf("velocity[0] should be reduced by friction: got %v", gotV[0])
	}
}

// --- PhysicsWalk: missing-field paths --------------------------------------

func TestPhysicsWalk_MissingFlags(t *testing.T) {
	p := groundDropField("flags")
	ent, ev := newGroundEntFromProgs(t, p)
	ctx := PhysicsContext{
		Worldmodel:  groundEmptyWorld(),
		Now:         1.0,
		Dt:          0.1,
		ThinkCaller: groundNoThink(t),
	}
	alive, err := PhysicsWalk(ent, ev, 0, server.UserCmd{}, server.DefaultPhysParams(), ctx)
	if alive || !errors.Is(err, progs.ErrFieldNotFound) {
		t.Errorf("alive=%v err=%v want false, ErrFieldNotFound", alive, err)
	}
}

func TestPhysicsWalk_MissingVelocity(t *testing.T) {
	p := groundDropField("velocity")
	ent, ev := newGroundEntFromProgs(t, p)
	ctx := PhysicsContext{
		Worldmodel:  groundEmptyWorld(),
		Now:         1.0,
		Dt:          0.1,
		ThinkCaller: groundNoThink(t),
	}
	alive, err := PhysicsWalk(ent, ev, 0, server.UserCmd{}, server.DefaultPhysParams(), ctx)
	if alive || !errors.Is(err, progs.ErrFieldNotFound) {
		t.Errorf("alive=%v err=%v want false, ErrFieldNotFound", alive, err)
	}
}

func TestPhysicsWalk_MissingOrigin(t *testing.T) {
	p := groundDropField("origin")
	ent, ev := newGroundEntFromProgs(t, p)
	ctx := PhysicsContext{
		Worldmodel:  groundEmptyWorld(),
		Now:         1.0,
		Dt:          0.1,
		ThinkCaller: groundNoThink(t),
	}
	alive, err := PhysicsWalk(ent, ev, 0, server.UserCmd{}, server.DefaultPhysParams(), ctx)
	if alive || !errors.Is(err, progs.ErrFieldNotFound) {
		t.Errorf("alive=%v err=%v want false, ErrFieldNotFound", alive, err)
	}
}

func TestPhysicsWalk_MissingMins(t *testing.T) {
	p := groundDropField("mins")
	ent, ev := newGroundEntFromProgs(t, p)
	ctx := PhysicsContext{
		Worldmodel:  groundEmptyWorld(),
		Now:         1.0,
		Dt:          0.1,
		ThinkCaller: groundNoThink(t),
	}
	alive, err := PhysicsWalk(ent, ev, 0, server.UserCmd{}, server.DefaultPhysParams(), ctx)
	if alive || !errors.Is(err, progs.ErrFieldNotFound) {
		t.Errorf("alive=%v err=%v want false, ErrFieldNotFound", alive, err)
	}
}

func TestPhysicsWalk_MissingMaxs(t *testing.T) {
	p := groundDropField("maxs")
	ent, ev := newGroundEntFromProgs(t, p)
	ctx := PhysicsContext{
		Worldmodel:  groundEmptyWorld(),
		Now:         1.0,
		Dt:          0.1,
		ThinkCaller: groundNoThink(t),
	}
	alive, err := PhysicsWalk(ent, ev, 0, server.UserCmd{}, server.DefaultPhysParams(), ctx)
	if alive || !errors.Is(err, progs.ErrFieldNotFound) {
		t.Errorf("alive=%v err=%v want false, ErrFieldNotFound", alive, err)
	}
}

func TestPhysicsWalk_MissingVAngle(t *testing.T) {
	p := groundDropField("v_angle")
	ent, ev := newGroundEntFromProgs(t, p)
	ctx := PhysicsContext{
		Worldmodel:  groundEmptyWorld(),
		Now:         1.0,
		Dt:          0.1,
		ThinkCaller: groundNoThink(t),
	}
	alive, err := PhysicsWalk(ent, ev, 0, server.UserCmd{}, server.DefaultPhysParams(), ctx)
	if alive || !errors.Is(err, progs.ErrFieldNotFound) {
		t.Errorf("alive=%v err=%v want false, ErrFieldNotFound", alive, err)
	}
}

// --- jump ------------------------------------------------------------------

// +jump while standing (FL_ONGROUND set, FL_JUMP_RELEASED armed) launches
// the player: velocity_z gains the jump speed and both FL_ONGROUND and
// FL_JUMP_RELEASED clear so gravity takes over and a held key can't re-fire.
func TestPhysicsWalk_JumpLaunchesFromGround(t *testing.T) {
	ent, ev, _ := newGroundEnt(t, true)
	seedGroundBaseline(t, ev)
	if err := ev.WriteVec3("origin", [3]float32{0, 0, 10}); err != nil {
		t.Fatal(err)
	}
	if err := ev.WriteFloat("flags", float32(int32(server.FlagOnGround|server.FlagJumpReleased))); err != nil {
		t.Fatal(err)
	}
	ctx := PhysicsContext{
		Worldmodel:  groundFloorWorld(),
		Now:         1.0,
		Dt:          0.1,
		ThinkCaller: groundNoThink(t),
	}
	cmd := server.UserCmd{Buttons: server.ButtonJump}
	alive, err := PhysicsWalk(ent, ev, 0, cmd, server.DefaultPhysParams(), ctx)
	if !alive || err != nil {
		t.Fatalf("alive=%v err=%v want true, nil", alive, err)
	}
	gotV, _ := ev.ReadVec3("velocity")
	if gotV[2] <= 0 {
		t.Errorf("velocity_z should be positive after jump: got %v", gotV[2])
	}
	gotF, _ := ev.ReadFloat("flags")
	flags := server.EntityFlag(int32(gotF))
	if flags&server.FlagOnGround != 0 {
		t.Errorf("FL_ONGROUND should clear after a jump: flags=%d", int32(flags))
	}
	if flags&server.FlagJumpReleased != 0 {
		t.Errorf("FL_JUMP_RELEASED should clear while jump held: flags=%d", int32(flags))
	}
}

// A held +jump that never released (FL_JUMP_RELEASED not set) does NOT
// re-launch: the debounce keeps the player grounded until the key is let go.
func TestPhysicsWalk_JumpDebouncedWhenHeld(t *testing.T) {
	ent, ev, _ := newGroundEnt(t, true)
	seedGroundBaseline(t, ev)
	if err := ev.WriteVec3("origin", [3]float32{0, 0, 10}); err != nil {
		t.Fatal(err)
	}
	// FL_ONGROUND set, FL_JUMP_RELEASED NOT set -> jump is still held from
	// a prior tic and must not fire again.
	if err := ev.WriteFloat("flags", float32(int32(server.FlagOnGround))); err != nil {
		t.Fatal(err)
	}
	ctx := PhysicsContext{
		Worldmodel:  groundFloorWorld(),
		Now:         1.0,
		Dt:          0.1,
		ThinkCaller: groundNoThink(t),
	}
	cmd := server.UserCmd{Buttons: server.ButtonJump}
	alive, err := PhysicsWalk(ent, ev, 0, cmd, server.DefaultPhysParams(), ctx)
	if !alive || err != nil {
		t.Fatalf("alive=%v err=%v want true, nil", alive, err)
	}
	gotV, _ := ev.ReadVec3("velocity")
	if gotV[2] > 0 {
		t.Errorf("velocity_z must stay grounded when jump is debounced: got %v", gotV[2])
	}
	gotF, _ := ev.ReadFloat("flags")
	flags := server.EntityFlag(int32(gotF))
	if flags&server.FlagOnGround == 0 {
		t.Errorf("FL_ONGROUND should persist when the debounced jump is skipped: flags=%d", int32(flags))
	}
}

// --- drift detector --------------------------------------------------------

// Pin the ground-handler constants so a future "cleanup" can't drift
// them without re-reading the C upstream.
func TestPhysicsGround_TyrquakeConstants(t *testing.T) {
	if stepDefaultGravity != 1.0 {
		t.Errorf("stepDefaultGravity drift: got %v want 1.0", stepDefaultGravity)
	}
	if jumpSpeed != 270 {
		t.Errorf("jumpSpeed drift: got %v want 270", jumpSpeed)
	}
}
