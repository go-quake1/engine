// Copyright (c) 1996-1997 Id Software, Inc.
// Copyright (c) 2026 the go-quake1/engine authors.
// SPDX-License-Identifier: GPL-2.0-or-later

package progs

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
)

// On-wire constants from tyrquake/include/pr_comp.h.
const (
	headerSize     = 15 * 4 // 15 int32 fields in dprograms_t = 60 bytes
	statementSize  = 8      // u16 op + 3x i16 operands
	defSize        = 8      // u16 type + u16 ofs + i32 s_name
	functionSize   = 36     // see Function struct
	globalSlotSize = 4      // every global is a 4-byte int / float / pointer
)

// Statement is one dstatement_t: a single bytecode instruction.
// tyrquake: dstatement_t.
type Statement struct {
	Op      Op
	A, B, C int16
}

// Def is one ddef_t: a global or field definition. The Type field
// may have the DefSaveGlobal bit set; strip it with `t & ^uint16(
// DefSaveGlobal)` before switching on Etype.
// tyrquake: ddef_t.
type Def struct {
	Type  uint16
	Ofs   uint16
	SName int32 // offset into the string table
}

// Function is one dfunction_t: a callable QuakeC function.
// FirstStatement < 0 means the function is a builtin -- its negative
// value is the (1-based) index into the C-side builtin table.
// tyrquake: dfunction_t.
type Function struct {
	FirstStatement int32
	ParmStart      int32
	Locals         int32 // total ints of parms + locals
	Profile        int32 // runtime accumulator
	SName          int32 // offset into string table -- function name
	SFile          int32 // offset into string table -- source file
	NumParms       int32
	ParmSize       [MaxParms]byte
}

// Header mirrors dprograms_t verbatim. Useful for diagnostic dumps
// that compare two progs.dat layouts side by side.
type Header struct {
	Version       int32
	CRC           int32
	OfsStatements int32
	NumStatements int32
	OfsGlobalDefs int32
	NumGlobalDefs int32
	OfsFieldDefs  int32
	NumFieldDefs  int32
	OfsFunctions  int32
	NumFunctions  int32
	OfsStrings    int32
	StringsSize   int32
	OfsGlobals    int32
	NumGlobals    int32
	EntityFields  int32
}

// Progs is a parsed progs.dat ready for the interpreter to execute.
// All slices are owned by this value; mutating them is undefined.
type Progs struct {
	Header     Header
	Statements []Statement
	GlobalDefs []Def
	FieldDefs  []Def
	Functions  []Function
	Strings    []byte // NUL-separated; a string_t is a byte offset into this
	Globals    []byte // raw bytes; each global is a 4-byte slot; reinterpret as float/int/etc
}

// MaxEntityFields is the most 4-byte fields an entity can have in a file Load
// accepts: field offsets are 16 bits (Def.Ofs), and a vector takes three slots.
const MaxEntityFields = 1<<16 + 2

// Sentinel errors surfaced by Load.
var (
	ErrBadVersion        = errors.New("progs: unsupported version (need 6)")
	ErrSectionOutOfRange = errors.New("progs: section offset/length outside file")
	ErrShortRead         = errors.New("progs: short read")
)

// Load parses a progs.dat from src. The caller retains ownership; src
// is read in full into memory (a progs.dat is typically <1 MiB). On
// success returns a Progs whose slices share no storage with src.
// tyrquake: PR_LoadProgs.
func Load(src io.ReaderAt, size int64) (*Progs, error) {
	if src == nil {
		return nil, errors.New("progs: nil src")
	}
	if size < headerSize {
		return nil, ErrShortRead
	}
	raw := make([]byte, size)
	n, err := src.ReadAt(raw, 0)
	if err != nil && err != io.EOF {
		return nil, fmt.Errorf("progs: read: %w", err)
	}
	if int64(n) < size {
		return nil, ErrShortRead
	}
	return parse(raw)
}

func parse(raw []byte) (*Progs, error) {
	r := bytes.NewReader(raw)
	var h Header
	if err := binary.Read(r, binary.LittleEndian, &h); err != nil {
		return nil, fmt.Errorf("progs: header decode: %w", err)
	}
	if h.Version != ProgVersion {
		return nil, fmt.Errorf("%w: got %d", ErrBadVersion, h.Version)
	}
	p := &Progs{Header: h}

	stmts, err := readStatements(raw, h.OfsStatements, h.NumStatements)
	if err != nil {
		return nil, err
	}
	p.Statements = stmts

	gdefs, err := readDefs(raw, h.OfsGlobalDefs, h.NumGlobalDefs)
	if err != nil {
		return nil, err
	}
	p.GlobalDefs = gdefs

	fdefs, err := readDefs(raw, h.OfsFieldDefs, h.NumFieldDefs)
	if err != nil {
		return nil, err
	}
	p.FieldDefs = fdefs

	funs, err := readFunctions(raw, h.OfsFunctions, h.NumFunctions)
	if err != nil {
		return nil, err
	}
	p.Functions = funs

	if err := checkSection(int64(len(raw)), h.OfsStrings, h.StringsSize, 1); err != nil {
		return nil, err
	}
	p.Strings = append([]byte(nil), raw[h.OfsStrings:h.OfsStrings+h.StringsSize]...)

	// The byte count is int64 all the way: it was cut to int32 for the check, so a
	// count of 0x40000001 slots (4 G + 4 bytes) passed as 4 bytes and then sliced
	// the file by the full count (a panic; found by FuzzLoad).
	globalsBytes := int64(h.NumGlobals) * globalSlotSize
	if h.NumGlobals < 0 || globalsBytes > int64(len(raw)) || int64(h.OfsGlobals) < 0 || int64(h.OfsGlobals)+globalsBytes > int64(len(raw)) {
		return nil, ErrSectionOutOfRange
	}
	// EntityFields sizes every edict (NewEdictArena makes EntityFields*4 bytes per
	// slot): a def's offset is 16 bits, so no field lies past 65535 + a vector.
	if h.EntityFields < 0 || h.EntityFields > MaxEntityFields {
		return nil, fmt.Errorf("%w: %d entity fields", ErrSectionOutOfRange, h.EntityFields)
	}
	p.Globals = append([]byte(nil), raw[h.OfsGlobals:int64(h.OfsGlobals)+globalsBytes]...)

	return p, nil
}

func readStatements(raw []byte, ofs, n int32) ([]Statement, error) {
	if err := checkSection(int64(len(raw)), ofs, n, statementSize); err != nil {
		return nil, err
	}
	out := make([]Statement, n)
	for i := int32(0); i < n; i++ {
		off := int(ofs) + int(i)*statementSize
		out[i].Op = Op(binary.LittleEndian.Uint16(raw[off : off+2]))
		out[i].A = int16(binary.LittleEndian.Uint16(raw[off+2 : off+4]))
		out[i].B = int16(binary.LittleEndian.Uint16(raw[off+4 : off+6]))
		out[i].C = int16(binary.LittleEndian.Uint16(raw[off+6 : off+8]))
	}
	return out, nil
}

func readDefs(raw []byte, ofs, n int32) ([]Def, error) {
	if err := checkSection(int64(len(raw)), ofs, n, defSize); err != nil {
		return nil, err
	}
	out := make([]Def, n)
	for i := int32(0); i < n; i++ {
		off := int(ofs) + int(i)*defSize
		out[i].Type = binary.LittleEndian.Uint16(raw[off : off+2])
		out[i].Ofs = binary.LittleEndian.Uint16(raw[off+2 : off+4])
		out[i].SName = int32(binary.LittleEndian.Uint32(raw[off+4 : off+8]))
	}
	return out, nil
}

func readFunctions(raw []byte, ofs, n int32) ([]Function, error) {
	if err := checkSection(int64(len(raw)), ofs, n, functionSize); err != nil {
		return nil, err
	}
	out := make([]Function, n)
	for i := int32(0); i < n; i++ {
		off := int(ofs) + int(i)*functionSize
		fn := &out[i]
		fn.FirstStatement = int32(binary.LittleEndian.Uint32(raw[off : off+4]))
		fn.ParmStart = int32(binary.LittleEndian.Uint32(raw[off+4 : off+8]))
		fn.Locals = int32(binary.LittleEndian.Uint32(raw[off+8 : off+12]))
		fn.Profile = int32(binary.LittleEndian.Uint32(raw[off+12 : off+16]))
		fn.SName = int32(binary.LittleEndian.Uint32(raw[off+16 : off+20]))
		fn.SFile = int32(binary.LittleEndian.Uint32(raw[off+20 : off+24]))
		fn.NumParms = int32(binary.LittleEndian.Uint32(raw[off+24 : off+28]))
		copy(fn.ParmSize[:], raw[off+28:off+28+MaxParms])
	}
	return out, nil
}

// checkSection validates that [ofs, ofs+count*unitSize) lies within
// [0, fileSize). count == 0 is always valid (empty section).
func checkSection(fileSize int64, ofs, count int32, unitSize int) error {
	if count == 0 {
		// An empty section is valid, but its offset still slices the file (the
		// strings section does): one outside [0, fileSize] is refused (it
		// panicked there; found by FuzzLoad).
		if ofs < 0 || int64(ofs) > fileSize {
			return ErrSectionOutOfRange
		}
		return nil
	}
	if ofs < 0 || count < 0 {
		return ErrSectionOutOfRange
	}
	end := int64(ofs) + int64(count)*int64(unitSize)
	if int64(ofs) > fileSize || end > fileSize {
		return ErrSectionOutOfRange
	}
	return nil
}

// String returns the NUL-terminated string at offset off in the
// string table, or "" when off is out of range. tyrquake:
// PR_GetString.
func (p *Progs) String(off int32) string {
	if off < 0 || int64(off) >= int64(len(p.Strings)) {
		return ""
	}
	end := bytes.IndexByte(p.Strings[off:], 0)
	if end < 0 {
		return string(p.Strings[off:])
	}
	return string(p.Strings[off : int64(off)+int64(end)])
}
