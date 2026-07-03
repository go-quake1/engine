// Copyright (c) 2026 the go-quake1/engine authors.
// SPDX-License-Identifier: GPL-2.0-or-later

package bsprender_test

import (
	"bytes"
	"encoding/binary"
	"errors"
	"testing"

	"github.com/go-quake1/engine/bspfile"
	"github.com/go-quake1/engine/bsprender"
	"github.com/go-quake1/engine/model"
	"github.com/go-quake1/engine/render"
)

// --- FaceLightmapInfo error paths -------------------------------------------

func TestFaceLightmapInfo_NilModel(t *testing.T) {
	if _, err := bsprender.FaceLightmapInfo(nil, 0); !errors.Is(err, bsprender.ErrLmFaceNilModel) {
		t.Fatalf("got %v, want ErrLmFaceNilModel", err)
	}
}

func TestFaceLightmapInfo_OutOfRange(t *testing.T) {
	bm := mustLoadBrushWithFaces(t)
	if _, err := bsprender.FaceLightmapInfo(bm, -1); !errors.Is(err, bsprender.ErrLmFaceIdxRange) {
		t.Errorf("faceIdx=-1: got %v, want ErrLmFaceIdxRange", err)
	}
	if _, err := bsprender.FaceLightmapInfo(bm, 1<<30); !errors.Is(err, bsprender.ErrLmFaceIdxRange) {
		t.Errorf("faceIdx=huge: got %v, want ErrLmFaceIdxRange", err)
	}
}

// --- FaceLightmapInfo happy path --------------------------------------------

func TestFaceLightmapInfo_HappyPath(t *testing.T) {
	bm := mustLoadBrushWithFaces(t)
	info, err := bsprender.FaceLightmapInfo(bm, 0)
	if err != nil {
		t.Fatalf("FaceLightmapInfo: %v", err)
	}
	if info.Width < 1 || info.Height < 1 {
		t.Fatalf("non-positive dims: W=%d H=%d", info.Width, info.Height)
	}
	// MinS / MinT must be integer multiples of 16 (texturemins).
	if info.MinS%16 != 0 {
		t.Errorf("MinS=%d not a multiple of 16", info.MinS)
	}
	if info.MinT%16 != 0 {
		t.Errorf("MinT=%d not a multiple of 16", info.MinT)
	}
}

// --- TransformFaceLightmapped errors mirror TransformFace -------------------

func TestTransformFaceLightmapped_TooFewVerts(t *testing.T) {
	fb := mustFB(t, 320, 200)
	fv := bsprender.FaceVerts{NumVerts: 2, Vert: func(i int) [3]float32 { return [3]float32{} }}
	if _, err := bsprender.TransformFaceLightmapped(render.Affine{}, fb, 90, fv, bsprender.LightmapInfo{Width: 1, Height: 1}); !errors.Is(err, bsprender.ErrFaceTooFewVerts) {
		t.Errorf("got %v, want ErrFaceTooFewVerts", err)
	}
}

func TestTransformFaceLightmapped_TooManyVerts(t *testing.T) {
	fb := mustFB(t, 320, 200)
	fv := bsprender.FaceVerts{NumVerts: render.MaxPolyVerts + 1, Vert: func(i int) [3]float32 { return [3]float32{} }}
	if _, err := bsprender.TransformFaceLightmapped(render.Affine{}, fb, 90, fv, bsprender.LightmapInfo{Width: 1, Height: 1}); !errors.Is(err, bsprender.ErrFaceTooManyVerts) {
		t.Errorf("got %v, want ErrFaceTooManyVerts", err)
	}
}

func TestTransformFaceLightmapped_BehindCamera(t *testing.T) {
	fb := mustFB(t, 320, 200)
	face := triangleVerts([3]float32{-10, -10, -100}, [3]float32{10, -10, -100}, [3]float32{0, 10, -100})
	fv := bsprender.FaceVerts{NumVerts: 3, Vert: face}
	if _, err := bsprender.TransformFaceLightmapped(identityAffine(), fb, 90, fv, bsprender.LightmapInfo{Width: 1, Height: 1}); !errors.Is(err, bsprender.ErrFaceBehindCamera) {
		t.Errorf("got %v, want ErrFaceBehindCamera", err)
	}
}

// --- TransformFaceLightmapped happy path + LmS/LmT semantics ----------------

// With axis-aligned UV (S = x, T = y), MinS=MinT=0, the per-vertex
// LmS / LmT must equal vert.x/16 and vert.y/16.
func TestTransformFaceLightmapped_EmitsLmCoords(t *testing.T) {
	fb := mustFB(t, 320, 200)
	pts := [][3]float32{
		{32, 16, 100},
		{48, 16, 100},
		{32, 32, 100},
	}
	fv := bsprender.FaceVerts{
		NumVerts: 3,
		Vert:     func(i int) [3]float32 { return pts[i] },
		UVAxisS:  [3]float32{1, 0, 0},
		UVAxisT:  [3]float32{0, 1, 0},
	}
	info := bsprender.LightmapInfo{Width: 2, Height: 2, MinS: 32, MinT: 16}
	out, err := bsprender.TransformFaceLightmapped(identityAffine(), fb, 90, fv, info)
	if err != nil {
		t.Fatalf("TransformFaceLightmapped: %v", err)
	}
	if len(out) != 3 {
		t.Fatalf("len(out)=%d want 3", len(out))
	}
	// (vert.x - MinS) / 16, (vert.y - MinT) / 16
	cases := []struct {
		i            int
		wantS, wantT float32
	}{
		{0, 0, 0},
		{1, 1, 0},
		{2, 0, 1},
	}
	for _, c := range cases {
		if out[c.i].LmS != c.wantS {
			t.Errorf("vert %d LmS=%v want %v", c.i, out[c.i].LmS, c.wantS)
		}
		if out[c.i].LmT != c.wantT {
			t.Errorf("vert %d LmT=%v want %v", c.i, out[c.i].LmT, c.wantT)
		}
	}
}

// Degenerate fov falls through the scale=1 fallback.
func TestTransformFaceLightmapped_DegenerateFOV(t *testing.T) {
	fb := mustFB(t, 320, 200)
	face := triangleVerts([3]float32{-1, -1, 100}, [3]float32{1, -1, 100}, [3]float32{0, 1, 100})
	fv := bsprender.FaceVerts{NumVerts: 3, Vert: face}
	out, err := bsprender.TransformFaceLightmapped(identityAffine(), fb, 0, fv, bsprender.LightmapInfo{Width: 1, Height: 1})
	if err != nil {
		t.Fatalf("expected success under degenerate fov, got %v", err)
	}
	if len(out) != 3 {
		t.Fatalf("len(out)=%d want 3", len(out))
	}
}

// Lump-decode-error propagation: corrupt the length of each input
// lump in turn and verify FaceLightmapInfo surfaces the bspfile
// decoder error. Mirrors TestNewBrushFaceVerts_LumpReadErrPropagation
// for the lightmap entry point.
func TestFaceLightmapInfo_LumpReadErrPropagation(t *testing.T) {
	cases := []struct {
		name    string
		lumpIdx int
	}{
		{"vertexes", int(bspfile.LumpVertexes)},
		{"texinfo", int(bspfile.LumpTexInfo)},
		{"faces", int(bspfile.LumpFaces)},
		{"edges", int(bspfile.LumpEdges)},
		{"surfedges", int(bspfile.LumpSurfedges)},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			data, size := mustBuildWithFaces(t)
			off := 4 + c.lumpIdx*8
			curLen := int32(binary.LittleEndian.Uint32(data[off+4 : off+8]))
			binary.LittleEndian.PutUint32(data[off+4:off+8], uint32(curLen-1))
			f, err := bspfile.Open(bytes.NewReader(data), size)
			if err != nil {
				t.Fatalf("Open rejected the corruption: %v", err)
			}
			bm, err := model.LoadBrush(f, 0)
			if err != nil {
				t.Fatalf("LoadBrush rejected the corruption: %v", err)
			}
			if _, err := bsprender.FaceLightmapInfo(bm, 0); err == nil {
				t.Errorf("expected error for corrupt %s lump", c.name)
			}
		})
	}
}

// Face with out-of-range TexInfo -> 1x1 minimal plane fallback.
// Synthbsp builds a valid face, so corrupt the face's TexInfo idx by
// rewriting the byte at offset 10 in the first face record.
func TestFaceLightmapInfo_OutOfRangeTexInfo(t *testing.T) {
	data, size := mustBuildWithFaces(t)
	// LumpFaces directory entry: at off+4 is the byte offset.
	off := 4 + int(bspfile.LumpFaces)*8
	faceOff := int(binary.LittleEndian.Uint32(data[off : off+4]))
	// dface_t.TexInfo is int16 at byte offset 10 in the face record;
	// set to a huge value (0x7fff) so it's outside [0, NumTexInfos).
	binary.LittleEndian.PutUint16(data[faceOff+10:faceOff+12], uint16(0x7fff))
	f, err := bspfile.Open(bytes.NewReader(data), size)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	bm, err := model.LoadBrush(f, 0)
	if err != nil {
		t.Fatalf("LoadBrush: %v", err)
	}
	info, err := bsprender.FaceLightmapInfo(bm, 0)
	if err != nil {
		t.Fatalf("FaceLightmapInfo: %v", err)
	}
	if info.Width != 1 || info.Height != 1 {
		t.Fatalf("OOR texinfo: dims=(%d,%d), want (1,1) minimal fallback", info.Width, info.Height)
	}
}

// Face with NumEdges == 0 -> 1x1 minimal plane fallback. Mirrors the
// OutOfRangeTexInfo pattern but zeroes the NumEdges field.
func TestFaceLightmapInfo_ZeroNumEdges(t *testing.T) {
	data, size := mustBuildWithFaces(t)
	off := 4 + int(bspfile.LumpFaces)*8
	faceOff := int(binary.LittleEndian.Uint32(data[off : off+4]))
	// dface_t.NumEdges is int16 at byte offset 8 in the face record.
	binary.LittleEndian.PutUint16(data[faceOff+8:faceOff+10], 0)
	f, err := bspfile.Open(bytes.NewReader(data), size)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	bm, err := model.LoadBrush(f, 0)
	if err != nil {
		t.Fatalf("LoadBrush: %v", err)
	}
	info, err := bsprender.FaceLightmapInfo(bm, 0)
	if err != nil {
		t.Fatalf("FaceLightmapInfo: %v", err)
	}
	if info.Width != 1 || info.Height != 1 {
		t.Fatalf("zero NumEdges: dims=(%d,%d), want (1,1)", info.Width, info.Height)
	}
}

// Synthetic FaceVerts that drives the min/max scan AND the +1 width
// clamp: a 4-vert face whose S/T projections decrease across verts
// so the i>0 `s < sMin` / `t < tMin` branches fire, plus verts in
// negative coords to exercise the negative-surfedge resolve path.
// We test indirectly by reading the BSP that mustBuildWithFaces
// emits — its synthetic face has multiple verts with varying S/T,
// so the existing happy-path test already exercises 134-145 reliably
// when min/max walk runs over all 4 verts.
//
// However the 119-122 negative-surfedge branch and 164/167 width<1
// clamps are tricky: synthbsp's face uses positive surfedges + valid
// extents. Cover them via a Face with synthetically NEGATIVE
// surfedges + degenerate (single-point) extents — corrupt the BSP
// in-memory to flip the sign of one surfedge index.
func TestFaceLightmapInfo_NegativeSurfedge(t *testing.T) {
	data, size := mustBuildWithFaces(t)
	// Flip the first surfedge to its negative.
	off := 4 + int(bspfile.LumpSurfedges)*8
	seOff := int(binary.LittleEndian.Uint32(data[off : off+4]))
	cur := int32(binary.LittleEndian.Uint32(data[seOff : seOff+4]))
	if cur <= 0 {
		t.Skipf("first surfedge is already %d, can't test negative path", cur)
	}
	binary.LittleEndian.PutUint32(data[seOff:seOff+4], uint32(-cur))
	f, err := bspfile.Open(bytes.NewReader(data), size)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	bm, err := model.LoadBrush(f, 0)
	if err != nil {
		t.Fatalf("LoadBrush: %v", err)
	}
	if _, err := bsprender.FaceLightmapInfo(bm, 0); err != nil {
		t.Fatalf("FaceLightmapInfo on negative-surfedge face: %v", err)
	}
}

// Min/max walk: with synthbsp's default verts (S/T are monotonically
// non-decreasing across the face), only the s>sMax and t>tMax
// branches fire. Inflate vertex 0's X + Y so the LATER verts have
// SMALLER S/T values -> the s<sMin / t<tMin branches must fire.
func TestFaceLightmapInfo_MinUpdateBranches(t *testing.T) {
	data, size := mustBuildWithFaces(t)
	off := 4 + int(bspfile.LumpVertexes)*8
	vOff := int(binary.LittleEndian.Uint32(data[off : off+4]))
	// Overwrite vertex 0 (X, Y) = (100, 100); face 0 walks
	// verts (0, 1, 2) so vert 1 (X=10) is < sMin=100 -> branch fires.
	var inflate [4]byte
	binary.LittleEndian.PutUint32(inflate[:], 0x42c80000) // 100.0
	copy(data[vOff:vOff+4], inflate[:])
	copy(data[vOff+4:vOff+8], inflate[:])
	f, err := bspfile.Open(bytes.NewReader(data), size)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	bm, err := model.LoadBrush(f, 0)
	if err != nil {
		t.Fatalf("LoadBrush: %v", err)
	}
	info, err := bsprender.FaceLightmapInfo(bm, 0)
	if err != nil {
		t.Fatalf("FaceLightmapInfo: %v", err)
	}
	if info.Width < 1 || info.Height < 1 {
		t.Fatalf("dims=(%d,%d) want positive", info.Width, info.Height)
	}
}

// Partial-behind vert: Z is clamped to ParticleNearClip and propagated.
func TestTransformFaceLightmapped_PartialBehindClampsZ(t *testing.T) {
	fb := mustFB(t, 320, 200)
	pts := [][3]float32{
		{-1, -1, -50},
		{1, -1, 100},
		{0, 1, 100},
	}
	fv := bsprender.FaceVerts{NumVerts: 3, Vert: func(i int) [3]float32 { return pts[i] }}
	out, err := bsprender.TransformFaceLightmapped(identityAffine(), fb, 90, fv, bsprender.LightmapInfo{Width: 1, Height: 1})
	if err != nil {
		t.Fatalf("expected success, got %v", err)
	}
	if out[0].Z != render.ParticleNearClip {
		t.Errorf("rear vert Z=%v, want clamped to %v", out[0].Z, render.ParticleNearClip)
	}
}

// BenchmarkTransformFaceLightmapped tracks the per-face allocation count of
// the world-surface transform. The view-space scratch is now stack-backed,
// so only the returned vertex slice allocates (2 allocs/op -> 1).
func BenchmarkTransformFaceLightmapped(b *testing.B) {
	fb, err := render.NewFrameBuffer(320, 200)
	if err != nil {
		b.Fatal(err)
	}
	pts := [][3]float32{{32, 16, 100}, {48, 16, 100}, {32, 32, 100}}
	fv := bsprender.FaceVerts{
		NumVerts: 3,
		Vert:     func(i int) [3]float32 { return pts[i] },
		UVAxisS:  [3]float32{1, 0, 0},
		UVAxisT:  [3]float32{0, 1, 0},
	}
	info := bsprender.LightmapInfo{Width: 2, Height: 2, MinS: 32, MinT: 16}
	view := identityAffine()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = bsprender.TransformFaceLightmapped(view, fb, 90, fv, info)
	}
}
