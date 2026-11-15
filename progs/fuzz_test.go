// Copyright (c) 2026 the go-quake1/engine authors.
// SPDX-License-Identifier: GPL-2.0-or-later

package progs

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"testing"

	"github.com/go-quake1/engine/internal/fuzzcheck"
)

// fuzzSeeds are the committed seeds (testdata/fuzz/FuzzLoad, written by
// TestWriteFuzzSeeds): a small valid progs.dat (two functions, one of them
// calling the other), cuts of it and corruptions of its header counts and of its
// function table, all synthetic.
func fuzzSeeds() map[string][]byte {
	valid := func() []byte {
		raw, _ := build(buildSpec{
			stmts: []Statement{
				{},
				{Op: OP_ADD_F, A: 40, B: 41, C: 42},
				{Op: OP_CALL0, A: 20}, // global slot 20 holds the function number, 2
				{Op: OP_DONE},
				{Op: OP_MUL_F, A: 40, B: 41, C: 43},
				{Op: OP_RETURN, A: 43},
			},
			gdefs: []Def{{Type: uint16(EvFloat), Ofs: 40, SName: 1}},
			fdefs: []Def{{Type: uint16(EvFloat), Ofs: 0, SName: 7}},
			funs:  []Function{{}, {FirstStatement: 1, ParmStart: 44, Locals: 4, SName: 1}, {FirstStatement: 4, ParmStart: 50, Locals: 2, SName: 7}},
			strs:  []byte("\x00think\x00frame\x00"),
			gvals: func() []byte {
				g := make([]byte, 4*64)
				g[4*20] = 2 // the function CALL0 calls
				return g
			}(),
			entityFields: 8,
		})
		return raw
	}
	mut := func(f func(b []byte)) []byte {
		b := valid()
		f(b)
		return b
	}
	put := func(b []byte, off int, v uint32) { binary.LittleEndian.PutUint32(b[off:], v) }
	// The header is 15 int32s: version, crc, then (ofs, num) pairs.
	const (
		hStmtNum    = 12
		hGdefNum    = 20
		hFdefNum    = 28
		hFunNum     = 36
		hStrSize    = 44
		hGlobalsNum = 52
		hEntFields  = 56
	)
	fun := func(b []byte, i int) int { return int(binary.LittleEndian.Uint32(b[32:])) + i*36 }
	return map[string][]byte{
		"valid":              valid(),
		"cut-tables":         valid()[:len(valid())-100],
		"cut-header":         valid()[:40],
		"bad-version":        mut(func(b []byte) { put(b, 0, 5) }),
		"statements-huge":    mut(func(b []byte) { put(b, hStmtNum, 0x7fffffff) }),
		"globaldefs-huge":    mut(func(b []byte) { put(b, hGdefNum, 0x7fffffff) }),
		"fielddefs-negative": mut(func(b []byte) { put(b, hFdefNum, 0xfffffff0) }),
		"functions-huge":     mut(func(b []byte) { put(b, hFunNum, 0x7fffffff) }),
		"strings-huge":       mut(func(b []byte) { put(b, hStrSize, 0x7fffffff) }),
		// 0x40000001 slots: 4 G + 4 bytes, which truncated to 4 as an int32.
		"globals-wrap":          mut(func(b []byte) { put(b, hGlobalsNum, 0x40000001) }),
		"globals-huge":          mut(func(b []byte) { put(b, hGlobalsNum, 0x7fffffff) }),
		"entityfields-huge":     mut(func(b []byte) { put(b, hEntFields, 0x7fffffff) }),
		"entityfields-negative": mut(func(b []byte) { put(b, hEntFields, 0xfffffff0) }),
		"locals-huge":           mut(func(b []byte) { put(b, fun(b, 1)+8, 0x7fffffff) }),
		"locals-negative":       mut(func(b []byte) { put(b, fun(b, 1)+8, 0xfffffff0) }),
		"parmstart-past-end":    mut(func(b []byte) { put(b, fun(b, 1)+4, 0x7ffffff0) }),
		"parms-huge":            mut(func(b []byte) { put(b, fun(b, 1)+24, 0x7fffffff) }),
		"firststatement-huge":   mut(func(b []byte) { put(b, fun(b, 1), 0x7fffffff) }),
		"firststatement-negative": mut(func(b []byte) {
			put(b, fun(b, 1), 0xfffffff0)
		}),
	}
}

func TestWriteFuzzSeeds(t *testing.T) {
	if os.Getenv("WRITE_FUZZ_SEEDS") != "1" {
		t.Skip("WRITE_FUZZ_SEEDS=1 writes the seed corpus")
	}
	for name, data := range fuzzSeeds() {
		if err := fuzzcheck.WriteSeed("testdata/fuzz", "FuzzLoad", name, data); err != nil {
			t.Fatal(err)
		}
	}
}

// maxFuzzFunctions is how many of a progs' functions the fuzz walk runs, each
// for at most MaxRunaway statements.
const maxFuzzFunctions = 4

func walk(t testing.TB, data []byte) {
	p, err := Load(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return // a refusal is the right answer to most inputs
	}
	h := p.Header
	if len(p.Statements) != int(h.NumStatements) || len(p.GlobalDefs) != int(h.NumGlobalDefs) ||
		len(p.FieldDefs) != int(h.NumFieldDefs) || len(p.Functions) != int(h.NumFunctions) ||
		len(p.Globals) != int(h.NumGlobals)*4 || len(p.Strings) != int(h.StringsSize) {
		t.Fatalf("tables for header %+v", h)
	}
	for _, d := range append(append([]Def(nil), p.GlobalDefs...), p.FieldDefs...) {
		_ = p.String(d.SName)
	}
	for _, f := range p.Functions {
		_ = p.String(f.SName)
		_ = p.String(f.SFile)
	}
	for fn := int32(1); fn < int32(len(p.Functions)) && fn <= maxFuzzFunctions; fn++ {
		vm := NewVM(p)
		vm.SetArena(NewEdictArena(p, 4))
		_ = vm.Run(fn)
		vm.Reset()
	}
	// The same function again from the same VM: Run leaves it reusable.
	if len(p.Functions) > 1 {
		vm := NewVM(p)
		_ = vm.Run(1)
		vm.Reset()
		_ = vm.Run(1)
	}
}

// FuzzLoad: progs.Load and the VM that runs what it loads, on any bytes: no panic,
// and no allocation above fuzzcheck.Limit however the header's counts and sizes
// and the function table read; the first functions of a progs that loads are run
// for a bounded number of statements. A progs that loads has the tables its
// header counts.
func FuzzLoad(f *testing.F) {
	for _, s := range fuzzSeeds() {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		fuzzcheck.Bounded(t, len(data), func() { walk(t, data) })
	})
}

// A valid seed loads whole and runs: parse then valid output.
func TestFuzzSeedValidRuns(t *testing.T) {
	d := fuzzSeeds()["valid"]
	p, err := Load(bytes.NewReader(d), int64(len(d)))
	if err != nil {
		t.Fatal(err)
	}
	vm := NewVM(p)
	_ = vm.SetGlobalFloat(40, 6)
	_ = vm.SetGlobalFloat(41, 7)
	// Function 1 adds slots 40 and 41 into 42 and calls function 2, which
	// multiplies them into 43.
	if err := vm.Run(1); err != nil {
		t.Fatal(err)
	}
	if v, _ := vm.GlobalFloat(42); v != 13 {
		t.Errorf("42 = %v, want 13", v)
	}
	if v, _ := vm.GlobalFloat(43); v != 42 {
		t.Errorf("43 = %v, want 42", v)
	}
}

// The crashers the fuzz target found, as refusals: a header that counts what the
// file cannot hold, a globals count that wrapped when cut to 32 bits, and an
// entity field count that would size every edict, are errors from Load; a
// function entry whose locals, parm_start or parameter count are outside what the
// VM can save is an error from Run (ErrBadFunction), not a panic or an 8 GiB
// make.
func TestHostileProgsAreRefused(t *testing.T) {
	seeds := fuzzSeeds()
	for _, name := range []string{"cut-tables", "cut-header", "bad-version", "statements-huge", "globaldefs-huge", "fielddefs-negative", "functions-huge", "strings-huge", "globals-wrap", "globals-huge", "entityfields-huge", "entityfields-negative"} {
		d := seeds[name]
		if _, err := Load(bytes.NewReader(d), int64(len(d))); err == nil {
			t.Errorf("%s: loaded", name)
		}
	}
	for _, name := range []string{"locals-huge", "locals-negative", "parmstart-past-end", "parms-huge"} {
		d := seeds[name]
		p, err := Load(bytes.NewReader(d), int64(len(d)))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if err := NewVM(p).Run(1); !errors.Is(err, ErrBadFunction) {
			t.Errorf("%s: Run = %v, want ErrBadFunction", name, err)
		}
	}
	for _, name := range []string{"firststatement-huge", "firststatement-negative"} {
		d := seeds[name]
		p, err := Load(bytes.NewReader(d), int64(len(d)))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if err := NewVM(p).Run(1); err == nil {
			t.Errorf("%s: ran", name)
		}
	}
}

// The crasher the fuzz target found (testdata/fuzz/FuzzLoad/crasher-empty-strings-wild-offset):
// an empty strings section at an offset outside the file passed the section
// check and then sliced the file at that offset, a panic. Load refuses it.
func TestEmptySectionAtAWildOffsetIsRefused(t *testing.T) {
	raw, err := os.ReadFile("testdata/fuzz/FuzzLoad/crasher-empty-strings-wild-offset")
	if err != nil {
		t.Fatal(err)
	}
	var data []byte
	if _, err := fmt.Sscanf(string(raw[bytes.IndexByte(raw, '\n')+1:]), "[]byte(%q)", &data); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(bytes.NewReader(data), int64(len(data))); !errors.Is(err, ErrSectionOutOfRange) {
		t.Errorf("Load = %v, want ErrSectionOutOfRange", err)
	}
}
