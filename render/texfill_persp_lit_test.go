// Copyright (c) 1996-1997 Id Software, Inc.
// Copyright (c) 2026 the go-quake1/engine authors.
// SPDX-License-Identifier: GPL-2.0-or-later

package render

import (
	"errors"
	"testing"
)

func TestFillPerspectiveLitTexturedPolygon_NilFB(t *testing.T) {
	tex := makeTex4x4()
	cm := makeNoopCM()
	verts := []PerspLitTexturedVertex{
		{0, 0, 1, 0, 0, 0}, {4, 0, 1, 4, 0, 0}, {0, 4, 1, 0, 4, 0},
	}
	if err := FillPerspectiveLitTexturedPolygon(nil, tex, cm, verts); !errors.Is(err, ErrPerspLitTexFillNilFB) {
		t.Fatalf("err = %v want ErrPerspLitTexFillNilFB", err)
	}
}

func TestFillPerspectiveLitTexturedPolygon_NilTex(t *testing.T) {
	fb, _ := NewFrameBuffer(16, 16)
	cm := makeNoopCM()
	verts := []PerspLitTexturedVertex{
		{0, 0, 1, 0, 0, 0}, {4, 0, 1, 4, 0, 0}, {0, 4, 1, 0, 4, 0},
	}
	if err := FillPerspectiveLitTexturedPolygon(fb, nil, cm, verts); !errors.Is(err, ErrPerspLitTexFillNilTex) {
		t.Fatalf("err = %v want ErrPerspLitTexFillNilTex", err)
	}
}

func TestFillPerspectiveLitTexturedPolygon_NilCM(t *testing.T) {
	fb, _ := NewFrameBuffer(16, 16)
	tex := makeTex4x4()
	verts := []PerspLitTexturedVertex{
		{0, 0, 1, 0, 0, 0}, {4, 0, 1, 4, 0, 0}, {0, 4, 1, 0, 4, 0},
	}
	if err := FillPerspectiveLitTexturedPolygon(fb, tex, nil, verts); !errors.Is(err, ErrPerspLitTexFillNilCM) {
		t.Fatalf("err = %v want ErrPerspLitTexFillNilCM", err)
	}
}

func TestFillPerspectiveLitTexturedPolygon_TooFewVerts(t *testing.T) {
	fb, _ := NewFrameBuffer(16, 16)
	tex := makeTex4x4()
	cm := makeNoopCM()
	verts := []PerspLitTexturedVertex{{0, 0, 1, 0, 0, 0}, {4, 0, 1, 4, 0, 0}}
	if err := FillPerspectiveLitTexturedPolygon(fb, tex, cm, verts); !errors.Is(err, ErrPerspLitTexFillFewVerts) {
		t.Fatalf("err = %v want ErrPerspLitTexFillFewVerts", err)
	}
}

func TestFillPerspectiveLitTexturedPolygon_TooManyVerts(t *testing.T) {
	fb, _ := NewFrameBuffer(16, 16)
	tex := makeTex4x4()
	cm := makeNoopCM()
	verts := make([]PerspLitTexturedVertex, MaxPolyVerts+1)
	if err := FillPerspectiveLitTexturedPolygon(fb, tex, cm, verts); !errors.Is(err, ErrPerspLitTexFillManyVerts) {
		t.Fatalf("err = %v want ErrPerspLitTexFillManyVerts", err)
	}
}

func TestFillPerspectiveLitTexturedPolygon_BadShape(t *testing.T) {
	fb, _ := NewFrameBuffer(16, 16)
	cm := makeNoopCM()
	bad := &Pic{Width: 4, Height: 4, Pixels: make([]byte, 15)}
	verts := []PerspLitTexturedVertex{
		{0, 0, 1, 0, 0, 0}, {4, 0, 1, 4, 0, 0}, {0, 4, 1, 0, 4, 0},
	}
	if err := FillPerspectiveLitTexturedPolygon(fb, bad, cm, verts); !errors.Is(err, ErrPicShape) {
		t.Fatalf("err = %v want ErrPicShape", err)
	}
}

func TestFillPerspectiveLitTexturedPolygon_ZeroZ(t *testing.T) {
	fb, _ := NewFrameBuffer(16, 16)
	tex := makeTex4x4()
	cm := makeNoopCM()
	verts := []PerspLitTexturedVertex{
		{0, 0, 1, 0, 0, 0}, {4, 0, 0, 4, 0, 0}, {0, 4, 1, 0, 4, 0},
	}
	if err := FillPerspectiveLitTexturedPolygon(fb, tex, cm, verts); !errors.Is(err, ErrPerspLitTexFillZeroZ) {
		t.Fatalf("err = %v want ErrPerspLitTexFillZeroZ", err)
	}
}

func TestFillPerspectiveLitTexturedPolygon_NegativeZ(t *testing.T) {
	fb, _ := NewFrameBuffer(16, 16)
	tex := makeTex4x4()
	cm := makeNoopCM()
	verts := []PerspLitTexturedVertex{
		{0, 0, 1, 0, 0, 0}, {4, 0, -2, 4, 0, 0}, {0, 4, 1, 0, 4, 0},
	}
	if err := FillPerspectiveLitTexturedPolygon(fb, tex, cm, verts); !errors.Is(err, ErrPerspLitTexFillZeroZ) {
		t.Fatalf("err = %v want ErrPerspLitTexFillZeroZ on negative Z", err)
	}
}

// UniformZ + uniformLight: with constant Z and constant Light, the
// L/Z accumulator divides back to the constant Light at every sub-span
// boundary; the U/V path collapses to affine the same way the
// FillPerspectiveTexturedPolygon UniformZMatchesAffine test verifies.
// With Light = 0 and a no-op colormap (cm[*][src] = src), the result
// must byte-match the plain affine FillTexturedPolygon -- the strongest
// reduction the four-quadrant table predicts.
func TestFillPerspectiveLitTexturedPolygon_UniformAllMatchesAffine(t *testing.T) {
	const W, H = 32, 32
	tex := makeTex4x4()
	cm := makeNoopCM()

	// UVs strictly inside [0, texW) so float-drift at the wrap boundary
	// can't make affine and perspective interpolators disagree on the
	// floor(U) sample column.
	plVerts := []PerspLitTexturedVertex{
		{2, 2, 100, 0, 0, 0},
		{28, 4, 100, 3.9, 0, 0},
		{26, 30, 100, 3.9, 3.9, 0},
		{4, 28, 100, 0, 3.9, 0},
	}
	aVerts := make([]TexturedVertex, len(plVerts))
	for i, v := range plVerts {
		aVerts[i] = TexturedVertex{X: v.X, Y: v.Y, U: v.U, V: v.V}
	}

	fbPL, _ := NewFrameBuffer(W, H)
	fbA, _ := NewFrameBuffer(W, H)
	if err := FillPerspectiveLitTexturedPolygon(fbPL, tex, cm, plVerts); err != nil {
		t.Fatalf("persp-lit: %v", err)
	}
	if err := FillTexturedPolygon(fbA, tex, nil, 0, aVerts); err != nil {
		t.Fatalf("affine: %v", err)
	}
	for i, gp := range fbPL.Pixels {
		if gp != fbA.Pixels[i] {
			t.Fatalf("uniform-all-vs-affine mismatch at byte %d: pl=%#02x a=%#02x", i, gp, fbA.Pixels[i])
		}
	}
}

// UniformZ + varyingLight: with constant Z, L/Z divided back yields L
// at every sub-span boundary; the per-pixel L stepping then tracks the
// affine per-pixel L stepping of FillLitTexturedPolygon. Float-step
// drift between the subdiv-8 accumulator and the affine per-pixel
// recompute can disagree by +/- 1 light row on borderline floors, so
// we assert "within 1 row" instead of strict byte equality.
func TestFillPerspectiveLitTexturedPolygon_UniformZMatchesLit(t *testing.T) {
	const W, H = 32, 32
	tex := makeTex4x4()
	cm := makeIdentityCM()

	plVerts := []PerspLitTexturedVertex{
		{2, 2, 50, 0, 0, 0},
		{28, 4, 50, 4, 0, 20},
		{26, 30, 50, 4, 4, ColorMapRows - 1},
		{4, 28, 50, 0, 4, 30},
	}
	lVerts := make([]LitTexturedVertex, len(plVerts))
	for i, v := range plVerts {
		lVerts[i] = LitTexturedVertex{X: v.X, Y: v.Y, U: v.U, V: v.V, Light: v.Light}
	}

	fbPL, _ := NewFrameBuffer(W, H)
	fbL, _ := NewFrameBuffer(W, H)
	if err := FillPerspectiveLitTexturedPolygon(fbPL, tex, cm, plVerts); err != nil {
		t.Fatalf("persp-lit: %v", err)
	}
	if err := FillLitTexturedPolygon(fbL, tex, cm, lVerts); err != nil {
		t.Fatalf("lit: %v", err)
	}
	for i, gp := range fbPL.Pixels {
		gl := fbL.Pixels[i]
		diff := int(gp) - int(gl)
		if diff < -1 || diff > 1 {
			t.Fatalf("uniform-Z varying-Light drift at byte %d: pl=%d l=%d", i, gp, gl)
		}
	}
}

// VaryingZ + uniformLight: with constant Light L, the L/Z accumulator
// divided back yields ~L at every sub-span boundary; the per-pixel
// floor(L) sometimes lands at L-1 due to ULP noise. Compare against
// FillPerspectiveTexturedPolygon at the same Z geometry and same
// lightLevel; allow +/- 1 light row drift.
func TestFillPerspectiveLitTexturedPolygon_VaryingZMatchesPerspective(t *testing.T) {
	const W, H = 64, 8
	tex := makeTex4x4()
	cm := makeIdentityCM()
	const L = 23

	plVerts := []PerspLitTexturedVertex{
		{0, 0, 1, 0, 0, L},
		{float32(W), 0, 8, 4, 0, L},
		{float32(W), float32(H), 8, 4, 4, L},
		{0, float32(H), 1, 0, 4, L},
	}
	pVerts := make([]PerspTexturedVertex, len(plVerts))
	for i, v := range plVerts {
		pVerts[i] = PerspTexturedVertex{X: v.X, Y: v.Y, Z: v.Z, U: v.U, V: v.V}
	}

	fbPL, _ := NewFrameBuffer(W, H)
	fbP, _ := NewFrameBuffer(W, H)
	if err := FillPerspectiveLitTexturedPolygon(fbPL, tex, cm, plVerts); err != nil {
		t.Fatalf("persp-lit: %v", err)
	}
	if err := FillPerspectiveTexturedPolygon(fbP, tex, cm, L, pVerts); err != nil {
		t.Fatalf("persp: %v", err)
	}
	for i, gp := range fbPL.Pixels {
		gp2 := fbP.Pixels[i]
		diff := int(gp) - int(gp2)
		if diff < -1 || diff > 1 {
			t.Fatalf("varying-Z uniform-Light drift at byte %d: pl=%d p=%d", i, gp, gp2)
		}
	}
}

// VaryingZ + varyingLight: distinct pixel value at a chosen position.
// Heavy perspective (z=1 near, z=8 far) plus a light gradient from 0
// to 32 along the same axis. With perspective-correct light, the
// midpoint screen pixel reads a light value BIASED toward the near
// side (z=1, L=0) -- not the affine midpoint of 16.
func TestFillPerspectiveLitTexturedPolygon_VaryingZVaryingLight(t *testing.T) {
	const W = 64
	fb, _ := NewFrameBuffer(W, 4)
	tex := makeTex4x4()
	cm := makeIdentityCM()
	verts := []PerspLitTexturedVertex{
		{0, 0, 1, 0, 0, 0},
		{float32(W), 0, 8, 4, 0, 32},
		{float32(W), 4, 8, 4, 4, 32},
		{0, 4, 1, 0, 4, 0},
	}
	if err := FillPerspectiveLitTexturedPolygon(fb, tex, cm, verts); err != nil {
		t.Fatalf("FillPerspectiveLitTexturedPolygon: %v", err)
	}
	// Perspective-correct midpoint: 1/Z avg = (1 + 1/8)/2 = 0.5625;
	// Z = 1.778; L/Z avg = (0 + 32/8)/2 = 2; L = 2*1.778 = ~3.56 ->
	// floor = 3. Affine midpoint would be 16. So the midpoint MUST
	// NOT be 16 and MUST be small (<= 8).
	mid := fb.Pixels[2*fb.Pitch+W/2]
	if mid == 16 {
		t.Fatalf("midpoint light = 16 (affine), perspective light correction failed")
	}
	if mid > 8 {
		t.Fatalf("midpoint light = %d, want perspective-biased small value (<= 8)", mid)
	}
}

// LightClampNegative: vertex light far below 0 -- per-pixel clamp via
// cm.LightIndex must keep output bytes at light = 0.
func TestFillPerspectiveLitTexturedPolygon_LightClampNegative(t *testing.T) {
	fb, _ := NewFrameBuffer(8, 8)
	tex := makeTex4x4()
	cm := makeIdentityCM()
	verts := []PerspLitTexturedVertex{
		{0, 0, 10, 0, 0, -500}, {4, 0, 10, 4, 0, -500},
		{4, 4, 10, 4, 4, -500}, {0, 4, 10, 0, 4, -500},
	}
	if err := FillPerspectiveLitTexturedPolygon(fb, tex, cm, verts); err != nil {
		t.Fatalf("FillPerspectiveLitTexturedPolygon: %v", err)
	}
	for y := 0; y < 4; y++ {
		for x := 0; x < 4; x++ {
			if got := fb.Pixels[y*fb.Pitch+x]; got != 0 {
				t.Fatalf("neg-clamp (%d,%d) = %d want 0", x, y, got)
			}
		}
	}
}

// LightClampSuperMax: vertex light far above ColorMapRows-1 -- per-pixel
// clamp keeps output at light = ColorMapRows-1.
func TestFillPerspectiveLitTexturedPolygon_LightClampSuperMax(t *testing.T) {
	fb, _ := NewFrameBuffer(8, 8)
	tex := makeTex4x4()
	cm := makeIdentityCM()
	verts := []PerspLitTexturedVertex{
		{0, 0, 10, 0, 0, 500}, {4, 0, 10, 4, 0, 500},
		{4, 4, 10, 4, 4, 500}, {0, 4, 10, 0, 4, 500},
	}
	if err := FillPerspectiveLitTexturedPolygon(fb, tex, cm, verts); err != nil {
		t.Fatalf("FillPerspectiveLitTexturedPolygon: %v", err)
	}
	for y := 0; y < 4; y++ {
		for x := 0; x < 4; x++ {
			if got := fb.Pixels[y*fb.Pitch+x]; got != ColorMapRows-1 {
				t.Fatalf("max-clamp (%d,%d) = %d want %d", x, y, got, ColorMapRows-1)
			}
		}
	}
}

func TestFillPerspectiveLitTexturedPolygon_UVWrapHigh(t *testing.T) {
	// Quake textures TILE; large UV must wrap modulo (W,H).
	fb, _ := NewFrameBuffer(8, 8)
	tex := makeTex4x4()
	cm := makeNoopCM()
	verts := []PerspLitTexturedVertex{
		{0, 0, 10, 0, 0, 0}, {4, 0, 10, 16, 0, 0},
		{4, 4, 10, 16, 16, 0}, {0, 4, 10, 0, 16, 0},
	}
	if err := FillPerspectiveLitTexturedPolygon(fb, tex, cm, verts); err != nil {
		t.Fatalf("FillPerspectiveLitTexturedPolygon: %v", err)
	}
	// (2,2) center -> U=V=10; wrap mod 4 = (2,2); tex[2][2] = 0x22.
	if got := fb.Pixels[2*fb.Pitch+2]; got != 0x22 {
		t.Fatalf("UV-wrap (2,2) = %#02x want 0x22", got)
	}
}

func TestFillPerspectiveLitTexturedPolygon_UVWrapLow(t *testing.T) {
	fb, _ := NewFrameBuffer(8, 8)
	fb.Clear(0x77)
	tex := makeTex4x4()
	cm := makeNoopCM()
	verts := []PerspLitTexturedVertex{
		{0, 0, 10, -10, -10, 0}, {4, 0, 10, -1, -10, 0},
		{4, 4, 10, -1, -1, 0}, {0, 4, 10, -10, -1, 0},
	}
	if err := FillPerspectiveLitTexturedPolygon(fb, tex, cm, verts); err != nil {
		t.Fatalf("FillPerspectiveLitTexturedPolygon: %v", err)
	}
	if got := fb.Pixels[2*fb.Pitch+2]; got == 0x77 {
		t.Fatalf("UV-wrap low (2,2): pixel not written (still sentinel 0x77)")
	}
}

// MultipleSubSpans: scanline wider than PerspSubdivStep (8 px) so the
// per-8-pixel sub-span loop runs more than once. Width is an exact
// multiple of 8 so the final iteration is a clean 8-pixel sub-span.
func TestFillPerspectiveLitTexturedPolygon_MultipleSubSpans(t *testing.T) {
	const W = 24 // 3 sub-spans
	fb, _ := NewFrameBuffer(W, 4)
	tex := makeTex4x4()
	cm := makeNoopCM()
	verts := []PerspLitTexturedVertex{
		{0, 0, 10, 0, 0, 0}, {float32(W), 0, 10, 4, 0, 0},
		{float32(W), 4, 10, 4, 4, 0}, {0, 4, 10, 0, 4, 0},
	}
	if err := FillPerspectiveLitTexturedPolygon(fb, tex, cm, verts); err != nil {
		t.Fatalf("FillPerspectiveLitTexturedPolygon: %v", err)
	}
	if got := fb.Pixels[2*fb.Pitch+W/2] & 0x0F; got < 1 || got > 2 {
		t.Fatalf("multi-subspan midpoint u = %d, want ~2", got)
	}
}

// PartialFinalSubSpan: width 21 = 2*8 + 5 -> the final sub-span has 5
// pixels and exercises the spanLen-1 divide path.
func TestFillPerspectiveLitTexturedPolygon_PartialFinalSubSpan(t *testing.T) {
	const W = 21
	fb, _ := NewFrameBuffer(W, 4)
	tex := makeTex4x4()
	cm := makeNoopCM()
	verts := []PerspLitTexturedVertex{
		{0, 0, 10, 0, 0, 0}, {float32(W), 0, 10, 4, 0, 0},
		{float32(W), 4, 10, 4, 4, 0}, {0, 4, 10, 0, 4, 0},
	}
	if err := FillPerspectiveLitTexturedPolygon(fb, tex, cm, verts); err != nil {
		t.Fatalf("FillPerspectiveLitTexturedPolygon: %v", err)
	}
	if got := fb.Pixels[2*fb.Pitch+(W-1)] & 0x0F; got != 0x03 {
		t.Fatalf("partial-final last-col u = %d want 3", got)
	}
}

// SinglePixelSpan: scanline of exactly 1 pixel exercises the
// spanLen == 1 path (du/dv/dl stay zero; no inv computation).
func TestFillPerspectiveLitTexturedPolygon_SinglePixelSpan(t *testing.T) {
	fb, _ := NewFrameBuffer(16, 16)
	tex := makeTex4x4()
	cm := makeNoopCM()
	verts := []PerspLitTexturedVertex{
		{5, 0, 10, 2, 0, 0},
		{5.5, 2, 10, 2, 2, 0},
		{4.5, 2, 10, 2, 2, 0},
	}
	if err := FillPerspectiveLitTexturedPolygon(fb, tex, cm, verts); err != nil {
		t.Fatalf("FillPerspectiveLitTexturedPolygon: %v", err)
	}
}

func TestFillPerspectiveLitTexturedPolygon_ClippedOffScreen(t *testing.T) {
	fb, _ := NewFrameBuffer(8, 8)
	fb.Clear(0xAB)
	tex := makeTex4x4()
	cm := makeNoopCM()
	verts := []PerspLitTexturedVertex{
		{-4, -4, 10, 0, 0, 0}, {12, -4, 10, 4, 0, 0},
		{12, 12, 10, 4, 4, 0}, {-4, 12, 10, 0, 4, 0},
	}
	if err := FillPerspectiveLitTexturedPolygon(fb, tex, cm, verts); err != nil {
		t.Fatalf("FillPerspectiveLitTexturedPolygon: %v", err)
	}
	for _, b := range fb.Pixels {
		if b == 0xAB {
			t.Fatalf("clipped poly left sentinel pixels untouched")
		}
	}
}

func TestFillPerspectiveLitTexturedPolygon_FullyOffScreenY(t *testing.T) {
	fb, _ := NewFrameBuffer(8, 8)
	fb.Clear(0xCC)
	tex := makeTex4x4()
	cm := makeNoopCM()
	verts := []PerspLitTexturedVertex{
		{0, 100, 10, 0, 0, 0}, {4, 100, 10, 4, 0, 0}, {2, 110, 10, 2, 4, 0},
	}
	if err := FillPerspectiveLitTexturedPolygon(fb, tex, cm, verts); err != nil {
		t.Fatalf("FillPerspectiveLitTexturedPolygon: %v", err)
	}
	for _, b := range fb.Pixels {
		if b != 0xCC {
			t.Fatalf("off-screen poly wrote into fb")
		}
	}
}

func TestFillPerspectiveLitTexturedPolygon_FullyOffScreenLeft(t *testing.T) {
	fb, _ := NewFrameBuffer(8, 8)
	fb.Clear(0xDD)
	tex := makeTex4x4()
	cm := makeNoopCM()
	verts := []PerspLitTexturedVertex{
		{-20, 0, 10, 0, 0, 0}, {-10, 0, 10, 4, 0, 0}, {-15, 8, 10, 2, 4, 0},
	}
	if err := FillPerspectiveLitTexturedPolygon(fb, tex, cm, verts); err != nil {
		t.Fatalf("FillPerspectiveLitTexturedPolygon: %v", err)
	}
	for _, b := range fb.Pixels {
		if b != 0xDD {
			t.Fatalf("off-screen-left poly wrote into fb")
		}
	}
}

func TestFillPerspectiveLitTexturedPolygon_ReverseWinding(t *testing.T) {
	fb, _ := NewFrameBuffer(8, 8)
	tex := makeTex4x4()
	cm := makeNoopCM()
	verts := []PerspLitTexturedVertex{
		{0, 4, 10, 0, 4, 0}, {4, 4, 10, 4, 4, 0},
		{4, 0, 10, 4, 0, 0}, {0, 0, 10, 0, 0, 0},
	}
	if err := FillPerspectiveLitTexturedPolygon(fb, tex, cm, verts); err != nil {
		t.Fatalf("FillPerspectiveLitTexturedPolygon: %v", err)
	}
	for y := 0; y < 4; y++ {
		for x := 0; x < 4; x++ {
			got := fb.Pixels[y*fb.Pitch+x]
			want := byte(y<<4 | x)
			if got != want {
				t.Fatalf("rev-wind (%d,%d) = %#02x want %#02x", x, y, got, want)
			}
		}
	}
}

// DiamondTriggersSortSwap: diamond polygon whose first-found edge is
// the RIGHT crossing on some scanlines, forcing the insertion sort to
// swap xs + oozs + uozs + vozs + lozs.
func TestFillPerspectiveLitTexturedPolygon_DiamondTriggersSortSwap(t *testing.T) {
	fb, _ := NewFrameBuffer(16, 16)
	tex := makeTex4x4()
	cm := makeNoopCM()
	verts := []PerspLitTexturedVertex{
		{8, 0, 10, 2, 0, 0}, {16, 8, 10, 4, 2, 0},
		{8, 16, 10, 2, 4, 0}, {0, 8, 10, 0, 2, 0},
	}
	if err := FillPerspectiveLitTexturedPolygon(fb, tex, cm, verts); err != nil {
		t.Fatalf("FillPerspectiveLitTexturedPolygon: %v", err)
	}
	if got := fb.Pixels[8*fb.Pitch+8]; got != 0x22 {
		t.Fatalf("diamond center = %#02x want 0x22", got)
	}
}

// SubPixelWidthSkip: sliver triangle where ceil(left) > floor(right)
// for some scanlines -> the per-pair continue fires.
func TestFillPerspectiveLitTexturedPolygon_SubPixelWidthSkip(t *testing.T) {
	fb, _ := NewFrameBuffer(16, 16)
	tex := makeTex4x4()
	cm := makeNoopCM()
	verts := []PerspLitTexturedVertex{
		{10.6, 5, 10, 1, 0, 0}, {10.9, 5, 10, 2, 0, 0},
		{10.7, 15, 10, 1.5, 4, 0},
	}
	if err := FillPerspectiveLitTexturedPolygon(fb, tex, cm, verts); err != nil {
		t.Fatalf("FillPerspectiveLitTexturedPolygon: %v", err)
	}
}
