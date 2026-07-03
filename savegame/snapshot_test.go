// Copyright (c) 2026 the go-quake1/engine authors.
// SPDX-License-Identifier: GPL-2.0-or-later

package savegame

import (
	"encoding/binary"
	"errors"
	"math"
	"testing"

	"github.com/go-quake1/engine/progs"
)

// addStr appends s+NUL to *strs and returns the offset.
func addStr(strs *[]byte, s string) int32 {
	ofs := int32(len(*strs))
	*strs = append(*strs, []byte(s)...)
	*strs = append(*strs, 0)
	return ofs
}

// buildProgs returns a Progs with every field type Snapshot/Restore
// cares about, plus a one-anonymous-field stub at the end so the
// SName==0 skip branch fires.
func buildProgs() *progs.Progs {
	strs := []byte{0}
	healthName := addStr(&strs, "health")
	originName := addStr(&strs, "origin")
	classnameName := addStr(&strs, "classname")
	enemyName := addStr(&strs, "enemy")
	chainName := addStr(&strs, "chain")
	thinkName := addStr(&strs, "think")
	ptrName := addStr(&strs, "trace_endpos_ptr")
	voidName := addStr(&strs, "voidfield")

	// Globals: one DefSaveGlobal float + one un-marked global.
	serverflagsName := addStr(&strs, "serverflags")
	scratchName := addStr(&strs, "scratch")

	const numGlobals = 64
	globals := make([]byte, numGlobals*4)
	// Place serverflags at global slot 30, value 7.0.
	binary.LittleEndian.PutUint32(globals[30*4:30*4+4], math.Float32bits(7))

	return &progs.Progs{
		Header:  progs.Header{EntityFields: 16}, // 16*4 = 64 bytes per edict
		Strings: strs,
		FieldDefs: []progs.Def{
			{Type: uint16(progs.EvFloat), Ofs: 1, SName: healthName},
			{Type: uint16(progs.EvVector), Ofs: 2, SName: originName}, // slots 2..4
			{Type: uint16(progs.EvString), Ofs: 5, SName: classnameName},
			{Type: uint16(progs.EvEntity), Ofs: 6, SName: enemyName},
			{Type: uint16(progs.EvField), Ofs: 7, SName: chainName},
			{Type: uint16(progs.EvFunction), Ofs: 8, SName: thinkName},
			{Type: uint16(progs.EvPointer), Ofs: 9, SName: ptrName},
			{Type: uint16(progs.EvVoid), Ofs: 10, SName: voidName},
			// Anonymous SName=0 field: skipped by Snapshot.
			{Type: uint16(progs.EvFloat), Ofs: 11, SName: 0},
		},
		GlobalDefs: []progs.Def{
			{Type: uint16(progs.EvFloat) | uint16(progs.DefSaveGlobal), Ofs: 30, SName: serverflagsName},
			// Un-marked global: Skipped by SnapshotGlobals.
			{Type: uint16(progs.EvFloat), Ofs: 31, SName: scratchName},
			// Anonymous SName=0: skipped even though marked.
			{Type: uint16(progs.EvFloat) | uint16(progs.DefSaveGlobal), Ofs: 32, SName: 0},
		},
		Globals: globals,
	}
}

// simpleView wraps a fixed-length []*progs.Edict as both an
// EdictView and a MutableEdictView.
type simpleView struct {
	edicts []*progs.Edict
	free   []bool
}

func newView(p *progs.Progs, n int) *simpleView {
	a := progs.NewEdictArena(p, n)
	v := &simpleView{
		edicts: make([]*progs.Edict, n),
		free:   make([]bool, n),
	}
	for i := 0; i < n; i++ {
		e, _ := a.Get(i)
		v.edicts[i] = e
	}
	return v
}

func (v *simpleView) Len() int                 { return len(v.edicts) }
func (v *simpleView) Free(i int) bool          { return v.free[i] }
func (v *simpleView) Edict(i int) *progs.Edict { return v.edicts[i] }
func (v *simpleView) SetFree(i int, free bool) { v.free[i] = free }

// --- Snapshot -----------------------------------------------------------------

func TestSnapshot_NilProgs(t *testing.T) {
	v := newView(buildProgs(), 1)
	if _, err := Snapshot(nil, v); !errors.Is(err, ErrNilProgs) {
		t.Errorf("got %v want ErrNilProgs", err)
	}
}

func TestSnapshot_HappyPath(t *testing.T) {
	p := buildProgs()
	v := newView(p, 3)
	// Populate edict 1 with values across every type.
	e := v.Edict(1)
	_ = e.FieldSetFloat(1, 100)                             // health
	_ = e.FieldSetVector(2, [3]float32{10, 20, 30})         // origin
	_ = e.FieldSetInt(5, addStr(&p.Strings, "monster_dog")) // classname
	_ = e.FieldSetInt(6, 42)                                // enemy
	_ = e.FieldSetInt(7, 9)                                 // chain
	_ = e.FieldSetInt(8, 11)                                // think
	_ = e.FieldSetInt(9, 12345)                             // ptr

	// Mark slot 2 free.
	v.SetFree(2, true)
	// And test: a nil pointer in the slot reports as free too.
	v.edicts[0] = nil

	snaps, err := Snapshot(p, v)
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if len(snaps) != 3 {
		t.Fatalf("snaps len: got %d want 3", len(snaps))
	}
	if !snaps[0].Free {
		t.Errorf("snap[0] nil edict should report Free")
	}
	if snaps[1].Free {
		t.Errorf("snap[1] should be populated")
	}
	if !snaps[2].Free {
		t.Errorf("snap[2] should be Free (SetFree)")
	}
	// Check each rendered field on snap[1].
	want := map[string]string{
		"health":           "100",
		"origin":           "10 20 30",
		"classname":        "monster_dog",
		"enemy":            "42",
		"chain":            "9",
		"think":            "11",
		"trace_endpos_ptr": "12345",
	}
	got := map[string]string{}
	for _, kv := range snaps[1].FieldKV {
		got[kv.Key] = kv.Value
	}
	for k, w := range want {
		if got[k] != w {
			t.Errorf("snap[1] %q: got %q want %q", k, got[k], w)
		}
	}
	if _, ok := got["voidfield"]; ok {
		t.Errorf("EvVoid field should be skipped, got %q", got["voidfield"])
	}
}

// renderFieldNoCoverage exercises the "field offset out of range"
// silent-skip branch in renderField for each type by passing a def
// with a huge offset.
func TestSnapshot_FieldOffsetOutOfRange(t *testing.T) {
	p := &progs.Progs{
		Header:  progs.Header{EntityFields: 1}, // only 4 bytes per edict
		Strings: []byte{0, 'a', 0, 'b', 0, 'c', 0, 'd', 0, 'e', 0, 'f', 0},
		FieldDefs: []progs.Def{
			{Type: uint16(progs.EvFloat), Ofs: 100, SName: 1},     // OOR
			{Type: uint16(progs.EvVector), Ofs: 100, SName: 3},    // OOR
			{Type: uint16(progs.EvString), Ofs: 100, SName: 5},    // OOR
			{Type: uint16(progs.EvEntity), Ofs: 100, SName: 7},    // OOR
			{Type: uint16(progs.EvField), Ofs: 100, SName: 9},     // OOR (no rendered ofs after rename)
			{Type: uint16(progs.EvFunction), Ofs: 100, SName: 11}, // OOR
		},
	}
	a := progs.NewEdictArena(p, 1)
	e, _ := a.Get(0)
	v := &simpleView{edicts: []*progs.Edict{e}, free: []bool{false}}

	snaps, err := Snapshot(p, v)
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if len(snaps[0].FieldKV) != 0 {
		t.Errorf("expected 0 fields (all OOR), got %d", len(snaps[0].FieldKV))
	}
}

// --- Restore ------------------------------------------------------------------

func TestRestore_NilProgs(t *testing.T) {
	v := newView(buildProgs(), 1)
	if err := Restore(nil, v, nil, nil); !errors.Is(err, ErrNilProgs) {
		t.Errorf("got %v want ErrNilProgs", err)
	}
}

func TestRestore_RoundTrip(t *testing.T) {
	p := buildProgs()
	v := newView(p, 3)
	e := v.Edict(1)
	_ = e.FieldSetFloat(1, 100)
	_ = e.FieldSetVector(2, [3]float32{10, 20, 30})
	dogOff := addStr(&p.Strings, "monster_dog")
	_ = e.FieldSetInt(5, dogOff)
	_ = e.FieldSetInt(6, 42)
	_ = e.FieldSetInt(9, 12345)
	v.SetFree(2, true)

	snaps, err := Snapshot(p, v)
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}

	// Build a fresh view (empty edicts) and restore into it.
	v2 := newView(p, 3)
	// A toy interner that appends to p.Strings (the test heap) and
	// returns the offset.
	intern := func(s string) int32 {
		ofs := int32(len(p.Strings))
		p.Strings = append(p.Strings, []byte(s)...)
		p.Strings = append(p.Strings, 0)
		return ofs
	}
	if err := Restore(p, v2, snaps, intern); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	// Assert: every value in v2[1] matches the original.
	e2 := v2.Edict(1)
	if f, _ := e2.FieldFloat(1); f != 100 {
		t.Errorf("health: got %v want 100", f)
	}
	if vec, _ := e2.FieldVector(2); vec != [3]float32{10, 20, 30} {
		t.Errorf("origin: got %v want [10 20 30]", vec)
	}
	classOff, _ := e2.FieldInt(5)
	if got := p.String(classOff); got != "monster_dog" {
		t.Errorf("classname: got %q want monster_dog", got)
	}
	if n, _ := e2.FieldInt(6); n != 42 {
		t.Errorf("enemy: got %d want 42", n)
	}
	if n, _ := e2.FieldInt(9); n != 12345 {
		t.Errorf("ptr: got %d want 12345", n)
	}
	if !v2.Free(2) {
		t.Errorf("slot 2 should be Free after restore")
	}
}

func TestRestore_NilEdictInSlot(t *testing.T) {
	p := buildProgs()
	v := newView(p, 2)
	v.edicts[1] = nil
	snaps := []EdictSnap{
		{FieldKV: []KV{{Key: "health", Value: "10"}}},
		{FieldKV: []KV{{Key: "health", Value: "20"}}},
	}
	// Nil edict in slot 1 is silently skipped (no error).
	if err := Restore(p, v, snaps, nil); err != nil {
		t.Errorf("Restore: %v", err)
	}
}

func TestRestore_UnknownFieldSkipped(t *testing.T) {
	p := buildProgs()
	v := newView(p, 1)
	snaps := []EdictSnap{
		{FieldKV: []KV{{Key: "no-such-field", Value: "0"}}},
	}
	if err := Restore(p, v, snaps, nil); err != nil {
		t.Errorf("Restore: %v", err)
	}
}

func TestRestore_BadFloat(t *testing.T) {
	p := buildProgs()
	v := newView(p, 1)
	snaps := []EdictSnap{
		{FieldKV: []KV{{Key: "health", Value: "abc"}}},
	}
	if err := Restore(p, v, snaps, nil); !errors.Is(err, ErrBadNumber) {
		t.Errorf("got %v want ErrBadNumber", err)
	}
}

func TestRestore_BadVec3Count(t *testing.T) {
	p := buildProgs()
	v := newView(p, 1)
	snaps := []EdictSnap{
		{FieldKV: []KV{{Key: "origin", Value: "1 2"}}},
	}
	if err := Restore(p, v, snaps, nil); !errors.Is(err, ErrBadNumber) {
		t.Errorf("got %v want ErrBadNumber", err)
	}
}

func TestRestore_BadVec3Float(t *testing.T) {
	p := buildProgs()
	v := newView(p, 1)
	snaps := []EdictSnap{
		{FieldKV: []KV{{Key: "origin", Value: "1 xx 3"}}},
	}
	if err := Restore(p, v, snaps, nil); !errors.Is(err, ErrBadNumber) {
		t.Errorf("got %v want ErrBadNumber", err)
	}
}

func TestRestore_BadEntityInt(t *testing.T) {
	p := buildProgs()
	v := newView(p, 1)
	snaps := []EdictSnap{
		{FieldKV: []KV{{Key: "enemy", Value: "not-an-int"}}},
	}
	if err := Restore(p, v, snaps, nil); !errors.Is(err, ErrBadNumber) {
		t.Errorf("got %v want ErrBadNumber", err)
	}
}

func TestRestore_StringNoInternerSkipped(t *testing.T) {
	p := buildProgs()
	v := newView(p, 1)
	snaps := []EdictSnap{
		{FieldKV: []KV{{Key: "classname", Value: "foo"}}},
	}
	if err := Restore(p, v, snaps, nil); err != nil {
		t.Errorf("Restore: %v", err)
	}
	// The field stayed at its zero value (no write).
	off, _ := v.Edict(0).FieldInt(5)
	if off != 0 {
		t.Errorf("classname offset: got %d want 0 (no interner -> no write)", off)
	}
}

func TestRestore_VoidFieldIgnored(t *testing.T) {
	p := buildProgs()
	v := newView(p, 1)
	snaps := []EdictSnap{
		{FieldKV: []KV{{Key: "voidfield", Value: "anything"}}},
	}
	if err := Restore(p, v, snaps, nil); err != nil {
		t.Errorf("Restore: %v", err)
	}
}

func TestRestore_FieldWriteFailsPropagated(t *testing.T) {
	// Build a progs where a field's Ofs is past the entity field
	// block so FieldSetFloat returns ErrFieldOffset.
	strs := []byte{0}
	healthName := addStr(&strs, "health")
	p := &progs.Progs{
		Header:    progs.Header{EntityFields: 1}, // 4 bytes total
		Strings:   strs,
		FieldDefs: []progs.Def{{Type: uint16(progs.EvFloat), Ofs: 100, SName: healthName}},
	}
	a := progs.NewEdictArena(p, 1)
	e, _ := a.Get(0)
	v := &simpleView{edicts: []*progs.Edict{e}, free: []bool{false}}
	snaps := []EdictSnap{
		{FieldKV: []KV{{Key: "health", Value: "10"}}},
	}
	if err := Restore(p, v, snaps, nil); err == nil {
		t.Errorf("expected ErrFieldOffset to propagate, got nil")
	}
}

func TestRestore_FreeSlot(t *testing.T) {
	p := buildProgs()
	v := newView(p, 2)
	// Pre-mark non-free so the restore SetFree(true) is observable.
	v.SetFree(0, false)
	v.SetFree(1, false)
	snaps := []EdictSnap{
		{Free: true},
		{Free: true},
	}
	if err := Restore(p, v, snaps, nil); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if !v.Free(0) || !v.Free(1) {
		t.Errorf("both slots should be Free after restore")
	}
}

func TestRestore_TruncatedSnapsArrayIgnored(t *testing.T) {
	p := buildProgs()
	v := newView(p, 5)
	snaps := []EdictSnap{{FieldKV: []KV{{Key: "health", Value: "1"}}}} // only 1 snap, 5 slots
	if err := Restore(p, v, snaps, nil); err != nil {
		t.Errorf("Restore: %v", err)
	}
}

// --- SnapshotGlobals ----------------------------------------------------------

func TestSnapshotGlobals_NilProgs(t *testing.T) {
	if _, err := SnapshotGlobals(nil); !errors.Is(err, ErrNilProgs) {
		t.Errorf("got %v want ErrNilProgs", err)
	}
}

func TestSnapshotGlobals_HappyPath(t *testing.T) {
	p := buildProgs()
	got, err := SnapshotGlobals(p)
	if err != nil {
		t.Fatalf("SnapshotGlobals: %v", err)
	}
	// Only "serverflags" carries DefSaveGlobal and a non-zero name.
	if len(got) != 1 {
		t.Fatalf("len: got %d want 1, snaps=%+v", len(got), got)
	}
	if got[0].Key != "serverflags" || got[0].Value != "7" {
		t.Errorf("global[0]: got %+v want {serverflags 7}", got[0])
	}
}

func TestSnapshotGlobals_AllTypes(t *testing.T) {
	strs := []byte{0}
	add := func(s string) int32 { return addStr(&strs, s) }
	flName := add("fl")
	vecName := add("vec")
	strName := add("str")
	entName := add("ent")
	fldName := add("fld")
	fnName := add("fn")
	ptrName := add("ptr")
	voidName := add("vd")
	helloOfs := add("hello")

	const numGlobals = 32
	globals := make([]byte, numGlobals*4)
	binary.LittleEndian.PutUint32(globals[0*4:], math.Float32bits(1.5))
	binary.LittleEndian.PutUint32(globals[1*4:], math.Float32bits(10))
	binary.LittleEndian.PutUint32(globals[2*4:], math.Float32bits(20))
	binary.LittleEndian.PutUint32(globals[3*4:], math.Float32bits(30))
	binary.LittleEndian.PutUint32(globals[4*4:], uint32(helloOfs))
	binary.LittleEndian.PutUint32(globals[5*4:], 99) // entity
	binary.LittleEndian.PutUint32(globals[6*4:], 7)  // field
	binary.LittleEndian.PutUint32(globals[7*4:], 8)  // function
	binary.LittleEndian.PutUint32(globals[8*4:], 9)  // pointer
	binary.LittleEndian.PutUint32(globals[9*4:], 0)  // void slot (skipped)

	save := uint16(progs.DefSaveGlobal)
	p := &progs.Progs{
		Header:  progs.Header{EntityFields: 1},
		Strings: strs,
		Globals: globals,
		GlobalDefs: []progs.Def{
			{Type: uint16(progs.EvFloat) | save, Ofs: 0, SName: flName},
			{Type: uint16(progs.EvVector) | save, Ofs: 1, SName: vecName},
			{Type: uint16(progs.EvString) | save, Ofs: 4, SName: strName},
			{Type: uint16(progs.EvEntity) | save, Ofs: 5, SName: entName},
			{Type: uint16(progs.EvField) | save, Ofs: 6, SName: fldName},
			{Type: uint16(progs.EvFunction) | save, Ofs: 7, SName: fnName},
			{Type: uint16(progs.EvPointer) | save, Ofs: 8, SName: ptrName},
			{Type: uint16(progs.EvVoid) | save, Ofs: 9, SName: voidName},
		},
	}
	got, err := SnapshotGlobals(p)
	if err != nil {
		t.Fatalf("SnapshotGlobals: %v", err)
	}
	want := map[string]string{
		"fl":  "1.5",
		"vec": "10 20 30",
		"str": "hello",
		"ent": "99",
		"fld": "7",
		"fn":  "8",
		"ptr": "9",
	}
	gotM := map[string]string{}
	for _, kv := range got {
		gotM[kv.Key] = kv.Value
	}
	for k, w := range want {
		if gotM[k] != w {
			t.Errorf("global %q: got %q want %q", k, gotM[k], w)
		}
	}
	if _, ok := gotM["vd"]; ok {
		t.Errorf("void global should be skipped")
	}
}

func TestSnapshotGlobals_OffsetOutOfRange(t *testing.T) {
	// Globals slab too small for the declared offsets.
	strs := []byte{0}
	flName := addStr(&strs, "fl")
	vecName := addStr(&strs, "vec")
	save := uint16(progs.DefSaveGlobal)
	p := &progs.Progs{
		Header:  progs.Header{EntityFields: 1},
		Strings: strs,
		Globals: make([]byte, 4), // 1 slot total
		GlobalDefs: []progs.Def{
			{Type: uint16(progs.EvFloat) | save, Ofs: 100, SName: flName},
			{Type: uint16(progs.EvVector) | save, Ofs: 0, SName: vecName}, // needs 12 bytes, have 4
		},
	}
	got, err := SnapshotGlobals(p)
	if err != nil {
		t.Fatalf("SnapshotGlobals: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("expected 0 globals (all OOR), got %+v", got)
	}
}

func TestFloat32frombits_Float32(t *testing.T) {
	// Sanity that float32frombits matches math.Float32frombits.
	for _, want := range []float32{0, 1, -1, 3.14, math.MaxFloat32} {
		b := make([]byte, 4)
		binary.LittleEndian.PutUint32(b, math.Float32bits(want))
		got := float32frombits(b)
		if got != want {
			t.Errorf("float32frombits(%v) = %v", want, got)
		}
	}
}

func TestInt32frombits(t *testing.T) {
	for _, want := range []int32{0, 1, -1, math.MaxInt32, math.MinInt32} {
		b := make([]byte, 4)
		binary.LittleEndian.PutUint32(b, uint32(want))
		got := int32frombits(b)
		if got != want {
			t.Errorf("int32frombits(%v) = %v", want, got)
		}
	}
}
