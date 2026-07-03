// Copyright (c) 1996-1997 Id Software, Inc.
// Copyright (c) 2026 the go-quake1/engine authors.
// SPDX-License-Identifier: GPL-2.0-or-later

package game

import (
	"encoding/binary"
	"testing"

	"github.com/go-quake1/engine/bspfile"
	"github.com/go-quake1/engine/embedpak"
	"github.com/go-quake1/engine/model"
)

// loadBrushFromFile decodes the world brush model (submodel 0) from f.
func loadBrushFromFile(t *testing.T, f *bspfile.File) (*model.BrushModel, error) {
	t.Helper()
	return model.LoadBrush(f, 0)
}

// realStartBSP returns a mutable copy of the real start.bsp bytes (or
// skips). The header lump table starts at byte 4 (15 lump_t entries of 8
// bytes: int32 offset + int32 length); LUMP_TEXTURES is lump index 2, so
// its lump_t sits at byte 20.
func realStartBSP(t *testing.T) []byte {
	t.Helper()
	pakFS, err := embedpak.OpenAsFS()
	if err != nil {
		t.Skipf("no real pak (%v)", err)
	}
	b, ok := tryReadPakFile(pakFS, "maps/start.bsp")
	if !ok {
		t.Skip("no maps/start.bsp")
	}
	cp := make([]byte, len(b))
	copy(cp, b)
	return cp
}

func texturesLumpOfs(b []byte) uint32 { return binary.LittleEndian.Uint32(b[20:24]) }

// loadMiptexFromBytes opens b and runs loadMiptexPicsNamed, returning the
// loaded count, total, and error.
func loadMiptexFromBytes(t *testing.T, b []byte) (int, int, error) {
	t.Helper()
	f, err := bspfile.Open(bytesReaderAt(b), int64(len(b)))
	if err != nil {
		// Open itself rejected the corruption; surface so the caller can
		// assert it took the intended branch instead.
		return 0, 0, err
	}
	_, _, loaded, total, terr := loadMiptexPicsNamed(f)
	return loaded, total, terr
}

// TestLoadMiptexCorruptBranches covers loadMiptexPicsNamed's four
// defensive branches (Textures() error, per-miptex MipTex error, the
// null/!ok slot, and the per-miptex Pixels error) by surgically
// corrupting a real start.bsp's textures lump.
func TestLoadMiptexCorruptBranches(t *testing.T) {
	base := realStartBSP(t)
	tofs := texturesLumpOfs(base)

	// (1) Textures() error: a NumMipTex that makes the directory length
	// inconsistent with the lump size.
	cTex := append([]byte(nil), base...)
	binary.LittleEndian.PutUint32(cTex[tofs:tofs+4], 0x7FFFFFFF)
	if _, _, err := loadMiptexFromBytes(t, cTex); err == nil {
		t.Fatal("corrupt NumMipTex must surface a Textures() error")
	}

	// The first miptex data offset lives at tofs+4.
	mt0 := tofs + 4

	// (2) MipTex(i) error: an in-range-but-bogus data offset.
	cBad := append([]byte(nil), base...)
	binary.LittleEndian.PutUint32(cBad[mt0:mt0+4], 0x7FFFFFF0)
	if loaded, total, err := loadMiptexFromBytes(t, cBad); err != nil || total == 0 || loaded >= total {
		t.Fatalf("bad miptex offset: loaded=%d total=%d err=%v", loaded, total, err)
	}

	// (3) null/!ok slot: the -1 sentinel offset.
	cNull := append([]byte(nil), base...)
	binary.LittleEndian.PutUint32(cNull[mt0:mt0+4], 0xFFFFFFFF)
	if loaded, total, err := loadMiptexFromBytes(t, cNull); err != nil || loaded >= total {
		t.Fatalf("null miptex slot: loaded=%d total=%d err=%v", loaded, total, err)
	}

	// (4) Pixels(0) error: a valid miptex header whose mip0 pixel offset
	// points outside the lump. The miptex record begins at tofs+off0;
	// its layout is name[16] + width[4] + height[4] + 4 mip offsets, so
	// the mip0 offset is at +24.
	off0 := binary.LittleEndian.Uint32(base[mt0 : mt0+4])
	recBase := tofs + off0
	cPix := append([]byte(nil), base...)
	binary.LittleEndian.PutUint32(cPix[recBase+24:recBase+28], 0x7FFFFFF0)
	if loaded, total, err := loadMiptexFromBytes(t, cPix); err != nil || loaded >= total {
		t.Fatalf("bad pixel offset: loaded=%d total=%d err=%v", loaded, total, err)
	}
}

// TestPickCameraZeroModels covers pickInMapCamera's + buildDemoWaypoints'
// "no models in the file" early returns by zeroing the BSP models lump
// (lump index 14; its lump_t sits at byte 4+8*14 = 116).
func TestPickCameraZeroModels(t *testing.T) {
	// A valid brush model from the pristine BSP (LoadBrush needs >=1
	// submodel)...
	good := realStartBSP(t)
	gf, err := bspfile.Open(bytesReaderAt(good), int64(len(good)))
	if err != nil {
		t.Fatalf("bspfile.Open good: %v", err)
	}
	bm, err := loadBrushFromFile(t, gf)
	if err != nil {
		t.Fatalf("LoadBrush: %v", err)
	}

	// ...but a separate file view whose models lump is empty so
	// file.Models() returns zero entries (pickInMapCamera + waypoints
	// only consult Models() off this file).
	base := realStartBSP(t)
	binary.LittleEndian.PutUint32(base[116+4:116+8], 0) // models lump length -> 0
	f, err := bspfile.Open(bytesReaderAt(base), int64(len(base)))
	if err != nil {
		t.Fatalf("bspfile.Open zero: %v", err)
	}
	cam := pickInMapCamera(bm, f)
	if cam != [3]float32{0, 0, 0} {
		t.Fatalf("zero-models pickInMapCamera = %v, want origin", cam)
	}
	if wps := buildDemoWaypoints(bm, f, cam); len(wps) != 1 {
		t.Fatalf("zero-models buildDemoWaypoints len = %d, want 1 (anchor only)", len(wps))
	}
}

// TestResolveModelBBoxModelsErr covers resolveModelBBox's
// "WorldModel.File.Models() returns an error" branch by swapping the
// host worldmodel's File for one whose models lump length is corrupt.
func TestResolveModelBBoxModelsErr(t *testing.T) {
	h, _, _ := buildRealHost(t)
	if h.Server.WorldModel == nil {
		t.Skip("no worldmodel")
	}
	// A file view whose models lump length is not a multiple of the
	// model element size -> Models() errors.
	base := realStartBSP(t)
	lp := 4 + 8*14
	ln := binary.LittleEndian.Uint32(base[lp+4 : lp+8])
	binary.LittleEndian.PutUint32(base[lp+4:lp+8], ln-1)
	bad, err := bspfile.Open(bytesReaderAt(base), int64(len(base)))
	if err != nil {
		t.Fatalf("bspfile.Open: %v", err)
	}
	if _, mErr := bad.Models(); mErr == nil {
		t.Fatal("expected a Models() error from the corrupt file")
	}

	orig := h.Server.WorldModel.File
	defer func() { h.Server.WorldModel.File = orig }()
	h.Server.WorldModel.File = bad

	cache := &setModelCache{mdlBBox: map[int][2][3]float32{}}
	if _, _, ok := resolveModelBBox(h, cache, "maps/start.bsp", 1); ok {
		t.Fatal("Models() error must make resolveModelBBox !ok")
	}
}
