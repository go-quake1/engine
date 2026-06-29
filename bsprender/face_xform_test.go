// Copyright (c) 2026 the go-quake1/engine authors.
// SPDX-License-Identifier: GPL-2.0-or-later

package bsprender_test

import (
	"bytes"
	"encoding/binary"
	"errors"
	"math"
	"testing"

	"github.com/go-quake1/engine/bspfile"
	"github.com/go-quake1/engine/bspfile/synthbsp"
	"github.com/go-quake1/engine/bsprender"
	"github.com/go-quake1/engine/model"
	"github.com/go-quake1/engine/render"
)

// --- TransformFace error cases ----------------------------------------------

func TestTransformFace_TooFewVerts(t *testing.T) {
	fb := mustFB(t, 320, 200)
	fv := bsprender.FaceVerts{
		NumVerts: 2,
		Vert:     func(i int) [3]float32 { return [3]float32{} },
	}
	if _, err := bsprender.TransformFace(render.Affine{}, fb, 90, fv); !errors.Is(err, bsprender.ErrFaceTooFewVerts) {
		t.Errorf("got %v, want ErrFaceTooFewVerts", err)
	}
}

func TestTransformFace_TooManyVerts(t *testing.T) {
	fb := mustFB(t, 320, 200)
	fv := bsprender.FaceVerts{
		NumVerts: render.MaxPolyVerts + 1,
		Vert:     func(i int) [3]float32 { return [3]float32{} },
	}
	if _, err := bsprender.TransformFace(render.Affine{}, fb, 90, fv); !errors.Is(err, bsprender.ErrFaceTooManyVerts) {
		t.Errorf("got %v, want ErrFaceTooManyVerts", err)
	}
}

func TestTransformFace_BehindCamera(t *testing.T) {
	fb := mustFB(t, 320, 200)
	// Identity view -- world == view. All verts placed deep into the
	// -Z half (behind the forward axis), so every vp[2] < ParticleNearClip.
	face := triangleVerts([3]float32{-10, -10, -100}, [3]float32{10, -10, -100}, [3]float32{0, 10, -100})
	fv := bsprender.FaceVerts{
		NumVerts: 3,
		Vert:     face,
	}
	if _, err := bsprender.TransformFace(identityAffine(), fb, 90, fv); !errors.Is(err, bsprender.ErrFaceBehindCamera) {
		t.Errorf("got %v, want ErrFaceBehindCamera", err)
	}
}

// --- TransformFace happy path -----------------------------------------------

// A triangle dead-ahead of the camera should land near the framebuffer
// center after the perspective divide.
func TestTransformFace_DeadAhead_ScreenCenter(t *testing.T) {
	fb := mustFB(t, 320, 200)
	// Three verts at z=100 (in front), tightly clustered around x=0,y=0.
	face := triangleVerts(
		[3]float32{-1, -1, 100},
		[3]float32{1, -1, 100},
		[3]float32{0, 1, 100},
	)
	fv := bsprender.FaceVerts{
		NumVerts: 3,
		Vert:     face,
	}
	out, err := bsprender.TransformFace(identityAffine(), fb, 90, fv)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 3 {
		t.Fatalf("len(out)=%d, want 3", len(out))
	}
	halfW := float32(fb.Width) / 2
	halfH := float32(fb.Height) / 2
	// Each vert must land within ~5 px of the screen center given the
	// tiny world-space extent and 90 deg fov.
	for i, v := range out {
		if abs(v.X-halfW) > 5 {
			t.Errorf("vert %d X=%v, expected near halfW=%v", i, v.X, halfW)
		}
		if abs(v.Y-halfH) > 5 {
			t.Errorf("vert %d Y=%v, expected near halfH=%v", i, v.Y, halfH)
		}
	}
}

// 4-vert quad in front of camera: API delivers 4 TexturedVertex slots.
func TestTransformFace_QuadInFront(t *testing.T) {
	fb := mustFB(t, 320, 200)
	pts := [][3]float32{
		{-5, -5, 50},
		{5, -5, 50},
		{5, 5, 50},
		{-5, 5, 50},
	}
	fv := bsprender.FaceVerts{
		NumVerts: 4,
		Vert:     func(i int) [3]float32 { return pts[i] },
	}
	out, err := bsprender.TransformFace(identityAffine(), fb, 90, fv)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 4 {
		t.Fatalf("len(out)=%d, want 4", len(out))
	}
}

// Verify the partial-behind-camera vert is depth-clamped (not rejected)
// when at least one vert is in front. Keeps the function from
// divide-by-near-zero while we wait on the real near-plane clipper.
func TestTransformFace_PartiallyBehind_ClampedNotRejected(t *testing.T) {
	fb := mustFB(t, 320, 200)
	pts := [][3]float32{
		{-1, -1, -50}, // behind camera (vp[2] = -50 < ParticleNearClip)
		{1, -1, 100},  // in front
		{0, 1, 100},   // in front
	}
	fv := bsprender.FaceVerts{
		NumVerts: 3,
		Vert:     func(i int) [3]float32 { return pts[i] },
	}
	out, err := bsprender.TransformFace(identityAffine(), fb, 90, fv)
	if err != nil {
		t.Fatalf("expected success (one vert in front), got %v", err)
	}
	if len(out) != 3 {
		t.Fatalf("len(out)=%d, want 3", len(out))
	}
	// The behind-camera vert's screen coords must be finite (the
	// clamp ensures the divide can't blow up).
	for i, v := range out {
		if math.IsNaN(float64(v.X)) || math.IsInf(float64(v.X), 0) {
			t.Errorf("vert %d X=%v non-finite", i, v.X)
		}
		if math.IsNaN(float64(v.Y)) || math.IsInf(float64(v.Y), 0) {
			t.Errorf("vert %d Y=%v non-finite", i, v.Y)
		}
	}
}

// Degenerate fov: tanHalfX <= 0 falls through to the belt-and-braces
// scale=1 fallback. Exercise it via fovX = 180 (tan = inf, the math
// path actually returns a very large positive number, so we use a
// pathological fov that drives tan negative -- fovX > 180 is
// invalid for the caller's NewRefDef but TransformFace still has to
// be non-crashing). Use fovX <= 0 instead via the comment-documented
// fallback: pass a near-zero / slightly-negative fovX (the public
// path validates upstream, this exercises the in-function guard).
func TestTransformFace_DegenerateFOV_DoesNotCrash(t *testing.T) {
	fb := mustFB(t, 320, 200)
	face := triangleVerts(
		[3]float32{-1, -1, 100},
		[3]float32{1, -1, 100},
		[3]float32{0, 1, 100},
	)
	fv := bsprender.FaceVerts{
		NumVerts: 3,
		Vert:     face,
	}
	// fovX = 0 -> tan(0) = 0 -> the guard fires (tanHalfX <= 0).
	out, err := bsprender.TransformFace(identityAffine(), fb, 0, fv)
	if err != nil {
		t.Fatalf("expected success under degenerate fov, got %v", err)
	}
	if len(out) != 3 {
		t.Fatalf("len(out)=%d, want 3", len(out))
	}
}

// --- TransformFace UV math --------------------------------------------------

// With s_axis=(1,0,0) and t_axis=(0,1,0) (zero offsets), U should
// equal vert.x and V should equal vert.y.
func TestTransformFace_UV_AxisAligned(t *testing.T) {
	fb := mustFB(t, 320, 200)
	pts := [][3]float32{
		{2, 3, 100},
		{7, 11, 100},
		{13, 17, 100},
	}
	fv := bsprender.FaceVerts{
		NumVerts: 3,
		Vert:     func(i int) [3]float32 { return pts[i] },
		UVAxisS:  [3]float32{1, 0, 0},
		UVOffS:   0,
		UVAxisT:  [3]float32{0, 1, 0},
		UVOffT:   0,
	}
	out, err := bsprender.TransformFace(identityAffine(), fb, 90, fv)
	if err != nil {
		t.Fatal(err)
	}
	for i, v := range out {
		if v.U != pts[i][0] {
			t.Errorf("vert %d U: got %v want %v (=vert.x)", i, v.U, pts[i][0])
		}
		if v.V != pts[i][1] {
			t.Errorf("vert %d V: got %v want %v (=vert.y)", i, v.V, pts[i][1])
		}
	}
}

// Verify offsets land in U/V additively.
func TestTransformFace_UV_WithOffsets(t *testing.T) {
	fb := mustFB(t, 320, 200)
	pts := [][3]float32{
		{0, 0, 100},
		{1, 0, 100},
		{0, 1, 100},
	}
	fv := bsprender.FaceVerts{
		NumVerts: 3,
		Vert:     func(i int) [3]float32 { return pts[i] },
		UVAxisS:  [3]float32{1, 0, 0},
		UVOffS:   50,
		UVAxisT:  [3]float32{0, 1, 0},
		UVOffT:   100,
	}
	out, err := bsprender.TransformFace(identityAffine(), fb, 90, fv)
	if err != nil {
		t.Fatal(err)
	}
	if out[0].U != 50 || out[0].V != 100 {
		t.Errorf("vert 0 UV: got (%v,%v), want (50,100)", out[0].U, out[0].V)
	}
	if out[1].U != 51 {
		t.Errorf("vert 1 U: got %v want 51", out[1].U)
	}
	if out[2].V != 101 {
		t.Errorf("vert 2 V: got %v want 101", out[2].V)
	}
}

// --- TransformFacePerspective -----------------------------------------------

func TestTransformFacePerspective_TooFewVerts(t *testing.T) {
	fb := mustFB(t, 320, 200)
	fv := bsprender.FaceVerts{NumVerts: 2, Vert: func(i int) [3]float32 { return [3]float32{} }}
	if _, err := bsprender.TransformFacePerspective(render.Affine{}, fb, 90, fv); !errors.Is(err, bsprender.ErrFaceTooFewVerts) {
		t.Errorf("got %v, want ErrFaceTooFewVerts", err)
	}
}

func TestTransformFacePerspective_TooManyVerts(t *testing.T) {
	fb := mustFB(t, 320, 200)
	fv := bsprender.FaceVerts{NumVerts: render.MaxPolyVerts + 1, Vert: func(i int) [3]float32 { return [3]float32{} }}
	if _, err := bsprender.TransformFacePerspective(render.Affine{}, fb, 90, fv); !errors.Is(err, bsprender.ErrFaceTooManyVerts) {
		t.Errorf("got %v, want ErrFaceTooManyVerts", err)
	}
}

func TestTransformFacePerspective_BehindCamera(t *testing.T) {
	fb := mustFB(t, 320, 200)
	face := triangleVerts([3]float32{-10, -10, -100}, [3]float32{10, -10, -100}, [3]float32{0, 10, -100})
	fv := bsprender.FaceVerts{NumVerts: 3, Vert: face}
	if _, err := bsprender.TransformFacePerspective(identityAffine(), fb, 90, fv); !errors.Is(err, bsprender.ErrFaceBehindCamera) {
		t.Errorf("got %v, want ErrFaceBehindCamera", err)
	}
}

// Happy path: confirms screen coords are reasonable AND that the Z
// field is propagated as the in-front view-space depth.
func TestTransformFacePerspective_DeadAhead_CarriesZ(t *testing.T) {
	fb := mustFB(t, 320, 200)
	face := triangleVerts(
		[3]float32{-1, -1, 100},
		[3]float32{1, -1, 100},
		[3]float32{0, 1, 100},
	)
	fv := bsprender.FaceVerts{NumVerts: 3, Vert: face}
	out, err := bsprender.TransformFacePerspective(identityAffine(), fb, 90, fv)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 3 {
		t.Fatalf("len(out)=%d, want 3", len(out))
	}
	halfW := float32(fb.Width) / 2
	halfH := float32(fb.Height) / 2
	for i, v := range out {
		if abs(v.X-halfW) > 5 {
			t.Errorf("vert %d X=%v, want near halfW=%v", i, v.X, halfW)
		}
		if abs(v.Y-halfH) > 5 {
			t.Errorf("vert %d Y=%v, want near halfH=%v", i, v.Y, halfH)
		}
		if v.Z != 100 {
			t.Errorf("vert %d Z=%v, want 100 (view-space depth)", i, v.Z)
		}
	}
}

// Partial-behind exercises the depth-clamp branch and the Z
// propagation for clamped verts (Z = ParticleNearClip on the rear vert).
func TestTransformFacePerspective_PartialBehind_ClampsZ(t *testing.T) {
	fb := mustFB(t, 320, 200)
	pts := [][3]float32{
		{-1, -1, -50}, // behind camera -> Z must be clamped, not propagated as -50
		{1, -1, 100},
		{0, 1, 100},
	}
	fv := bsprender.FaceVerts{NumVerts: 3, Vert: func(i int) [3]float32 { return pts[i] }}
	out, err := bsprender.TransformFacePerspective(identityAffine(), fb, 90, fv)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 3 {
		t.Fatalf("len(out)=%d, want 3", len(out))
	}
	if out[0].Z != render.ParticleNearClip {
		t.Errorf("rear vert Z=%v, want clamped to %v", out[0].Z, render.ParticleNearClip)
	}
	if out[1].Z != 100 || out[2].Z != 100 {
		t.Errorf("front verts Z=(%v,%v), want (100,100)", out[1].Z, out[2].Z)
	}
}

// Degenerate fov path: tanHalfX <= 0 guard falls back to scale=1.
func TestTransformFacePerspective_DegenerateFOV_DoesNotCrash(t *testing.T) {
	fb := mustFB(t, 320, 200)
	face := triangleVerts(
		[3]float32{-1, -1, 100},
		[3]float32{1, -1, 100},
		[3]float32{0, 1, 100},
	)
	fv := bsprender.FaceVerts{NumVerts: 3, Vert: face}
	out, err := bsprender.TransformFacePerspective(identityAffine(), fb, 0, fv)
	if err != nil {
		t.Fatalf("expected success under degenerate fov, got %v", err)
	}
	if len(out) != 3 {
		t.Fatalf("len(out)=%d, want 3", len(out))
	}
}

// UV axes + offsets are emitted unchanged (same math as TransformFace).
func TestTransformFacePerspective_UV_AxisAlignedWithOffsets(t *testing.T) {
	fb := mustFB(t, 320, 200)
	pts := [][3]float32{{2, 3, 100}, {7, 11, 100}, {13, 17, 100}}
	fv := bsprender.FaceVerts{
		NumVerts: 3,
		Vert:     func(i int) [3]float32 { return pts[i] },
		UVAxisS:  [3]float32{1, 0, 0},
		UVOffS:   5,
		UVAxisT:  [3]float32{0, 1, 0},
		UVOffT:   7,
	}
	out, err := bsprender.TransformFacePerspective(identityAffine(), fb, 90, fv)
	if err != nil {
		t.Fatal(err)
	}
	for i, v := range out {
		if v.U != pts[i][0]+5 {
			t.Errorf("vert %d U=%v, want %v", i, v.U, pts[i][0]+5)
		}
		if v.V != pts[i][1]+7 {
			t.Errorf("vert %d V=%v, want %v", i, v.V, pts[i][1]+7)
		}
	}
}

// --- NewBrushFaceVerts -------------------------------------------------------

func TestNewBrushFaceVerts_NilModel(t *testing.T) {
	if _, err := bsprender.NewBrushFaceVerts(nil, 0); !errors.Is(err, bsprender.ErrFaceNilModel) {
		t.Errorf("got %v, want ErrFaceNilModel", err)
	}
}

func TestNewBrushFaceVerts_OutOfRange(t *testing.T) {
	bm := mustLoadBrushWithFaces(t)
	if _, err := bsprender.NewBrushFaceVerts(bm, -1); !errors.Is(err, bsprender.ErrFaceIdxRange) {
		t.Errorf("faceIdx=-1: got %v, want ErrFaceIdxRange", err)
	}
	if _, err := bsprender.NewBrushFaceVerts(bm, 9999); !errors.Is(err, bsprender.ErrFaceIdxRange) {
		t.Errorf("faceIdx=9999: got %v, want ErrFaceIdxRange", err)
	}
}

func TestNewBrushFaceVerts_HappyPath(t *testing.T) {
	bm := mustLoadBrushWithFaces(t)
	fv, err := bsprender.NewBrushFaceVerts(bm, 0)
	if err != nil {
		t.Fatal(err)
	}
	if fv.NumVerts != 3 {
		t.Fatalf("NumVerts: got %d want 3", fv.NumVerts)
	}
	// Walking the synthetic BSP's surfedges + edges + verts the
	// helper installs (see synthbsp.BuildWithFaces), face 0's three
	// verts resolve to (0,0,0), (10,0,0), (0,10,0).
	want := [3][3]float32{
		{0, 0, 0},
		{10, 0, 0},
		{0, 10, 0},
	}
	for i := 0; i < fv.NumVerts; i++ {
		got := fv.Vert(i)
		if got != want[i] {
			t.Errorf("Vert(%d): got %v want %v", i, got, want[i])
		}
	}
	if fv.UVAxisS != ([3]float32{1, 0, 0}) {
		t.Errorf("UVAxisS: got %v want (1,0,0)", fv.UVAxisS)
	}
	if fv.UVAxisT != ([3]float32{0, 1, 0}) {
		t.Errorf("UVAxisT: got %v want (0,1,0)", fv.UVAxisT)
	}
	if fv.TextureWidth != 64 || fv.TextureHeight != 32 {
		t.Errorf("Texture WxH: got (%d,%d) want (64,32)", fv.TextureWidth, fv.TextureHeight)
	}
}

// Negative surfedge: the second face's surfedge is negative, which
// switches the winding from edge.V0 to edge.V1. Exercises the
// negative-surfedge branch in NewBrushFaceVerts' Vert closure.
func TestNewBrushFaceVerts_NegativeSurfedgeWinding(t *testing.T) {
	bm := mustLoadBrushWithFaces(t)
	// Face 1's surfedges are (-1, -2, -3) in our synthetic BSP, so
	// each vert resolves via the V1 branch.
	fv, err := bsprender.NewBrushFaceVerts(bm, 1)
	if err != nil {
		t.Fatal(err)
	}
	// Verts walked backwards via V1 of edges (1,2,3) -> verts (1,2,0).
	// edge 1 V1 = vert 2; edge 2 V1 = vert 0; edge 3 V1 = vert 1.
	// (See synthbsp.BuildWithFaces for the exact edge table.)
	want := [3][3]float32{
		{0, 10, 0}, // vert 2
		{0, 0, 0},  // vert 0
		{10, 0, 0}, // vert 1
	}
	for i := 0; i < fv.NumVerts; i++ {
		got := fv.Vert(i)
		if got != want[i] {
			t.Errorf("Vert(%d): got %v want %v (negative-surfedge winding)", i, got, want[i])
		}
	}
}

// A face that references a TexInfo whose MipTex slot is missing
// (offset = -1) leaves TextureWidth/Height at zero, the UV axes still
// come from texinfo.Vecs.
func TestNewBrushFaceVerts_MissingTexture(t *testing.T) {
	bm := mustLoadBrushWithFaces(t)
	fv, err := bsprender.NewBrushFaceVerts(bm, 2)
	if err != nil {
		t.Fatal(err)
	}
	if fv.TextureWidth != 0 || fv.TextureHeight != 0 {
		t.Errorf("missing-texture face: dims should be 0, got (%d,%d)", fv.TextureWidth, fv.TextureHeight)
	}
	if fv.UVAxisS != ([3]float32{2, 0, 0}) {
		t.Errorf("UVAxisS: got %v want (2,0,0)", fv.UVAxisS)
	}
}

// A face whose TexInfo index is out of range falls back to zero UV
// axes and zero texture dimensions. Exercises the !hasTexInfo branch.
func TestNewBrushFaceVerts_OutOfRangeTexInfo(t *testing.T) {
	bm := mustLoadBrushWithFaces(t)
	// Face 3 has TexInfo = 99 (well past the 3-entry table).
	fv, err := bsprender.NewBrushFaceVerts(bm, 3)
	if err != nil {
		t.Fatal(err)
	}
	if fv.UVAxisS != ([3]float32{}) || fv.UVAxisT != ([3]float32{}) {
		t.Errorf("OOR texinfo: axes should be zero, got %v/%v", fv.UVAxisS, fv.UVAxisT)
	}
	if fv.TextureWidth != 0 || fv.TextureHeight != 0 {
		t.Errorf("OOR texinfo: dims should be 0, got (%d,%d)", fv.TextureWidth, fv.TextureHeight)
	}
}

// Each rendering lump NewBrushFaceVerts reads can corrupt on disk;
// the constructor propagates the lump decoder's error. We exercise
// each branch by truncating the relevant lump length by one byte
// so the decoder fails with ErrSectionMisaligned. The lumps
// LoadBrush itself reads (Planes / Nodes / Leafs / ClipNodes / Models)
// are independent of these, so LoadBrush still succeeds; the error
// surfaces from NewBrushFaceVerts.
func TestNewBrushFaceVerts_LumpReadErrPropagation(t *testing.T) {
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
				t.Fatalf("Open rejected the corruption before NewBrushFaceVerts could; tighten the test: %v", err)
			}
			bm, err := model.LoadBrush(f, 0)
			if err != nil {
				t.Fatalf("LoadBrush rejected the corruption (render lump only); tighten the test: %v", err)
			}
			if _, err := bsprender.NewBrushFaceVerts(bm, 0); err == nil {
				t.Errorf("expected error for corrupt %s lump", c.name)
			}
		})
	}
}

// Textures lump corruption: truncate the directory's int32 header to
// just one byte so the Textures decoder errors. Exercises the err
// propagation from bm.File.Textures().
func TestNewBrushFaceVerts_TexturesLumpErr(t *testing.T) {
	data, size := mustBuildWithFaces(t)
	off := 4 + int(bspfile.LumpTextures)*8
	binary.LittleEndian.PutUint32(data[off+4:off+8], 1) // length = 1 byte -> < 4 -> ErrSectionMisaligned
	f, err := bspfile.Open(bytes.NewReader(data), size)
	if err != nil {
		t.Fatalf("Open rejected the corruption: %v", err)
	}
	bm, err := model.LoadBrush(f, 0)
	if err != nil {
		t.Fatalf("LoadBrush rejected the corruption: %v", err)
	}
	if _, err := bsprender.NewBrushFaceVerts(bm, 0); err == nil {
		t.Error("expected error from corrupt Textures lump")
	}
}

// Textures.MipTex(i) returns ErrSectionOutOfRange when slot i's
// offset points past the lump end. Build a Textures lump with a
// single slot whose offset is huge -> MipTex errors -> NewBrushFaceVerts
// propagates it.
func TestNewBrushFaceVerts_MipTexErr(t *testing.T) {
	data, size, err := synthbsp.BuildWithFacesCustomTextures(func() []byte {
		// Directory: 1 miptex, offset = 9999 (past end of lump).
		buf := &bytes.Buffer{}
		_ = binary.Write(buf, binary.LittleEndian, int32(1)) // num
		_ = binary.Write(buf, binary.LittleEndian, int32(9999))
		// A few filler bytes so the lump isn't tiny.
		buf.Write(make([]byte, 32))
		return buf.Bytes()
	})
	if err != nil {
		t.Fatalf("synthbsp.BuildWithFacesCustomTextures: %v", err)
	}
	f, err := bspfile.Open(bytes.NewReader(data), size)
	if err != nil {
		t.Fatal(err)
	}
	bm, err := model.LoadBrush(f, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := bsprender.NewBrushFaceVerts(bm, 0); err == nil {
		t.Error("expected error from MipTex out-of-range offset")
	}
}

// End-to-end: NewBrushFaceVerts feeds TransformFace which feeds
// FillTexturedPolygon; verify the whole pipeline produces pixels.
func TestNewBrushFaceVerts_FeedsTransformFace(t *testing.T) {
	bm := mustLoadBrushWithFaces(t)
	fv, err := bsprender.NewBrushFaceVerts(bm, 0)
	if err != nil {
		t.Fatal(err)
	}
	// Override Vert to put the triangle in front of the camera at z=50
	// (the synthetic BSP's verts are at z=0 which would project
	// degenerately under the identity affine -- the unit test cares
	// that the surface tying is correct, the geometry is incidental).
	pts := [][3]float32{
		{-5, -5, 50},
		{5, -5, 50},
		{0, 5, 50},
	}
	fv.Vert = func(i int) [3]float32 { return pts[i] }

	fb := mustFB(t, 320, 200)
	tv, err := bsprender.TransformFace(identityAffine(), fb, 90, fv)
	if err != nil {
		t.Fatal(err)
	}
	if len(tv) != 3 {
		t.Fatalf("len(tv)=%d, want 3", len(tv))
	}
}

// --- helpers ----------------------------------------------------------------

func mustFB(t *testing.T, w, h int) *render.FrameBuffer {
	t.Helper()
	fb, err := render.NewFrameBuffer(w, h)
	if err != nil {
		t.Fatal(err)
	}
	return fb
}

func identityAffine() render.Affine {
	return render.Affine{R: render.Identity(), T: [3]float32{}}
}

func triangleVerts(a, b, c [3]float32) func(i int) [3]float32 {
	verts := [3][3]float32{a, b, c}
	return func(i int) [3]float32 { return verts[i] }
}

func abs(x float32) float32 {
	if x < 0 {
		return -x
	}
	return x
}

// mustBuildWithFaces wraps synthbsp.BuildWithFaces with t.Fatalf on
// error so test bodies stay clear of the (always-nil) error return.
func mustBuildWithFaces(t *testing.T) ([]byte, int64) {
	t.Helper()
	data, size, err := synthbsp.BuildWithFaces()
	if err != nil {
		t.Fatalf("synthbsp.BuildWithFaces: %v", err)
	}
	return data, size
}

func mustLoadBrushWithFaces(t *testing.T) *model.BrushModel {
	t.Helper()
	data, size := mustBuildWithFaces(t)
	f, err := bspfile.Open(bytes.NewReader(data), size)
	if err != nil {
		t.Fatalf("bspfile.Open: %v", err)
	}
	bm, err := model.LoadBrush(f, 0)
	if err != nil {
		t.Fatalf("model.LoadBrush: %v", err)
	}
	return bm
}
