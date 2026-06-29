// Copyright (c) 1996-1997 Id Software, Inc.
// Copyright (c) 2026 the go-quake1/engine authors.
// SPDX-License-Identifier: GPL-2.0-or-later

package render

import (
	"errors"
	"testing"
)

// makeIdentityCM returns a colormap where cm[light][src] = byte(light).
// Lets a test recover the per-pixel light value at any framebuffer
// pixel just by reading the byte back, regardless of source texel.
func makeIdentityCM() *ColorMap {
	cm := new(ColorMap)
	for light := 0; light < ColorMapRows; light++ {
		for src := 0; src < ColorMapCols; src++ {
			cm[light][src] = byte(light)
		}
	}
	return cm
}

// makeNoopCM returns a colormap where cm[light][src] = src for every
// row. Renders FillLitTexturedPolygon equivalent to the unlit affine
// FillTexturedPolygon.
func makeNoopCM() *ColorMap {
	cm := new(ColorMap)
	for light := 0; light < ColorMapRows; light++ {
		for src := 0; src < ColorMapCols; src++ {
			cm[light][src] = byte(src)
		}
	}
	return cm
}

func TestFillLitTexturedPolygon_NilFB(t *testing.T) {
	tex := makeTex4x4()
	cm := makeNoopCM()
	verts := []LitTexturedVertex{
		{0, 0, 0, 0, 0}, {4, 0, 4, 0, 0}, {0, 4, 0, 4, 0},
	}
	if err := FillLitTexturedPolygon(nil, tex, cm, verts); !errors.Is(err, ErrLitTexFillNilFB) {
		t.Fatalf("err = %v want ErrLitTexFillNilFB", err)
	}
}

func TestFillLitTexturedPolygon_NilTex(t *testing.T) {
	fb, _ := NewFrameBuffer(16, 16)
	cm := makeNoopCM()
	verts := []LitTexturedVertex{
		{0, 0, 0, 0, 0}, {4, 0, 4, 0, 0}, {0, 4, 0, 4, 0},
	}
	if err := FillLitTexturedPolygon(fb, nil, cm, verts); !errors.Is(err, ErrLitTexFillNilTex) {
		t.Fatalf("err = %v want ErrLitTexFillNilTex", err)
	}
}

func TestFillLitTexturedPolygon_NilCM(t *testing.T) {
	fb, _ := NewFrameBuffer(16, 16)
	tex := makeTex4x4()
	verts := []LitTexturedVertex{
		{0, 0, 0, 0, 0}, {4, 0, 4, 0, 0}, {0, 4, 0, 4, 0},
	}
	if err := FillLitTexturedPolygon(fb, tex, nil, verts); !errors.Is(err, ErrLitTexFillNilCM) {
		t.Fatalf("err = %v want ErrLitTexFillNilCM", err)
	}
}

func TestFillLitTexturedPolygon_TooFewVerts(t *testing.T) {
	fb, _ := NewFrameBuffer(16, 16)
	tex := makeTex4x4()
	cm := makeNoopCM()
	verts := []LitTexturedVertex{{0, 0, 0, 0, 0}, {4, 0, 4, 0, 0}}
	if err := FillLitTexturedPolygon(fb, tex, cm, verts); !errors.Is(err, ErrLitTexFillFewVerts) {
		t.Fatalf("err = %v want ErrLitTexFillFewVerts", err)
	}
}

func TestFillLitTexturedPolygon_TooManyVerts(t *testing.T) {
	fb, _ := NewFrameBuffer(16, 16)
	tex := makeTex4x4()
	cm := makeNoopCM()
	verts := make([]LitTexturedVertex, MaxPolyVerts+1)
	if err := FillLitTexturedPolygon(fb, tex, cm, verts); !errors.Is(err, ErrLitTexFillManyVerts) {
		t.Fatalf("err = %v want ErrLitTexFillManyVerts", err)
	}
}

func TestFillLitTexturedPolygon_BadShape(t *testing.T) {
	fb, _ := NewFrameBuffer(16, 16)
	cm := makeNoopCM()
	bad := &Pic{Width: 4, Height: 4, Pixels: make([]byte, 15)}
	verts := []LitTexturedVertex{
		{0, 0, 0, 0, 0}, {4, 0, 4, 0, 0}, {0, 4, 0, 4, 0},
	}
	if err := FillLitTexturedPolygon(fb, bad, cm, verts); !errors.Is(err, ErrPicShape) {
		t.Fatalf("err = %v want ErrPicShape", err)
	}
}

func TestFillLitTexturedPolygon_UniformLightZeroMatchesUnlit(t *testing.T) {
	// A no-op colormap + Light = 0 everywhere must match the affine
	// FillTexturedPolygon's unlit (cm == nil) output pixel-for-pixel.
	fbA, _ := NewFrameBuffer(8, 8)
	fbB, _ := NewFrameBuffer(8, 8)
	tex := makeTex4x4()
	cm := makeNoopCM()
	plain := []TexturedVertex{
		{0, 0, 0, 0}, {4, 0, 4, 0}, {4, 4, 4, 4}, {0, 4, 0, 4},
	}
	lit := []LitTexturedVertex{
		{0, 0, 0, 0, 0}, {4, 0, 4, 0, 0}, {4, 4, 4, 4, 0}, {0, 4, 0, 4, 0},
	}
	if err := FillTexturedPolygon(fbA, tex, nil, 0, plain); err != nil {
		t.Fatalf("FillTexturedPolygon: %v", err)
	}
	if err := FillLitTexturedPolygon(fbB, tex, cm, lit); err != nil {
		t.Fatalf("FillLitTexturedPolygon: %v", err)
	}
	for i := range fbA.Pixels {
		if fbA.Pixels[i] != fbB.Pixels[i] {
			t.Fatalf("pixel %d: lit=%#02x unlit=%#02x", i, fbB.Pixels[i], fbA.Pixels[i])
		}
	}
}

func TestFillLitTexturedPolygon_UniformLightDarkMatchesUnlitWithLight(t *testing.T) {
	// Light = 63 everywhere + identity-light colormap must equal
	// FillTexturedPolygon with the same colormap + lightLevel = 63.
	fbA, _ := NewFrameBuffer(8, 8)
	fbB, _ := NewFrameBuffer(8, 8)
	tex := makeTex4x4()
	cm := makeIdentityCM()
	plain := []TexturedVertex{
		{0, 0, 0, 0}, {4, 0, 4, 0}, {4, 4, 4, 4}, {0, 4, 0, 4},
	}
	lit := []LitTexturedVertex{
		{0, 0, 0, 0, ColorMapRows - 1}, {4, 0, 4, 0, ColorMapRows - 1},
		{4, 4, 4, 4, ColorMapRows - 1}, {0, 4, 0, 4, ColorMapRows - 1},
	}
	if err := FillTexturedPolygon(fbA, tex, cm, ColorMapRows-1, plain); err != nil {
		t.Fatalf("FillTexturedPolygon: %v", err)
	}
	if err := FillLitTexturedPolygon(fbB, tex, cm, lit); err != nil {
		t.Fatalf("FillLitTexturedPolygon: %v", err)
	}
	for i := range fbA.Pixels {
		if fbA.Pixels[i] != fbB.Pixels[i] {
			t.Fatalf("pixel %d: lit=%#02x ref=%#02x", i, fbB.Pixels[i], fbA.Pixels[i])
		}
	}
}

func TestFillLitTexturedPolygon_VaryingLightProducesMidValue(t *testing.T) {
	// Triangle with lights 0, 32, 63 at the three corners + identity
	// colormap (cm[light][src] = byte(light)) -> output pixels carry
	// the per-pixel interpolated light. Clear to a sentinel so we can
	// distinguish "light = 0 written" from "untouched"; expect at
	// least one DRAWN pixel in each band (dark / mid / bright).
	fb, _ := NewFrameBuffer(32, 32)
	fb.Clear(0xAB)
	tex := makeTex4x4()
	cm := makeIdentityCM()
	verts := []LitTexturedVertex{
		{0, 0, 0, 0, 0},
		{30, 0, 4, 0, 32},
		{15, 30, 2, 4, ColorMapRows - 1},
	}
	if err := FillLitTexturedPolygon(fb, tex, cm, verts); err != nil {
		t.Fatalf("FillLitTexturedPolygon: %v", err)
	}
	sawLow, sawMid, sawHigh := false, false, false
	for _, b := range fb.Pixels {
		switch {
		case b == 0xAB:
			// untouched sentinel; skip.
		case b < 8:
			sawLow = true
		case b >= ColorMapRows-8:
			sawHigh = true
		default:
			sawMid = true
		}
	}
	if !sawLow || !sawMid || !sawHigh {
		t.Fatalf("gradient missing: low=%v mid=%v high=%v", sawLow, sawMid, sawHigh)
	}
}

func TestFillLitTexturedPolygon_UVWrapHigh(t *testing.T) {
	// Quake textures TILE; large UV must wrap modulo (W,H), not clamp.
	fb, _ := NewFrameBuffer(8, 8)
	tex := makeTex4x4()
	cm := makeNoopCM()
	verts := []LitTexturedVertex{
		{0, 0, 0, 0, 0}, {4, 0, 16, 0, 0}, {4, 4, 16, 16, 0}, {0, 4, 0, 16, 0},
	}
	if err := FillLitTexturedPolygon(fb, tex, cm, verts); err != nil {
		t.Fatalf("FillLitTexturedPolygon: %v", err)
	}
	// (2,2) center -> U=V=10; wrap mod 4 = (2,2); tex[2][2] = 0x22.
	if got := fb.Pixels[2*fb.Pitch+2]; got != 0x22 {
		t.Fatalf("UV-wrap (2,2) = %#02x want 0x22", got)
	}
}

func TestFillLitTexturedPolygon_UVWrapLow(t *testing.T) {
	fb, _ := NewFrameBuffer(8, 8)
	fb.Clear(0x77)
	tex := makeTex4x4()
	cm := makeNoopCM()
	verts := []LitTexturedVertex{
		{0, 0, -10, -10, 0}, {4, 0, -1, -10, 0}, {4, 4, -1, -1, 0}, {0, 4, -10, -1, 0},
	}
	if err := FillLitTexturedPolygon(fb, tex, cm, verts); err != nil {
		t.Fatalf("FillLitTexturedPolygon: %v", err)
	}
	// Just assert a sample was written (not the sentinel) -- exact byte
	// depends on floor-then-wrap of negative span endpoints.
	if got := fb.Pixels[2*fb.Pitch+2]; got == 0x77 {
		t.Fatalf("UV-wrap low (2,2): pixel not written (still sentinel 0x77)")
	}
}

func TestFillLitTexturedPolygon_LightClampPerPixel(t *testing.T) {
	// Vertex lights span well outside [0, 63]; per-pixel clamp via
	// cm.LightIndex must keep output bytes inside [0, 63] when paired
	// with the identity colormap (cm[light][src] = byte(light)).
	fb, _ := NewFrameBuffer(8, 8)
	tex := makeTex4x4()
	cm := makeIdentityCM()
	verts := []LitTexturedVertex{
		{0, 0, 0, 0, -500},
		{4, 0, 4, 0, -500},
		{4, 4, 4, 4, 500},
		{0, 4, 0, 4, 500},
	}
	if err := FillLitTexturedPolygon(fb, tex, cm, verts); err != nil {
		t.Fatalf("FillLitTexturedPolygon: %v", err)
	}
	sawLow, sawHigh := false, false
	for y := 0; y < 4; y++ {
		for x := 0; x < 4; x++ {
			b := fb.Pixels[y*fb.Pitch+x]
			if b > ColorMapRows-1 {
				t.Fatalf("pixel (%d,%d)=%d exceeds clamp max", x, y, b)
			}
			if b == 0 {
				sawLow = true
			}
			if b == ColorMapRows-1 {
				sawHigh = true
			}
		}
	}
	if !sawLow || !sawHigh {
		t.Fatalf("light clamp not exercised: low=%v high=%v", sawLow, sawHigh)
	}
}

func TestFillLitTexturedPolygon_ReverseWinding(t *testing.T) {
	fb, _ := NewFrameBuffer(8, 8)
	tex := makeTex4x4()
	cm := makeNoopCM()
	verts := []LitTexturedVertex{
		{0, 4, 0, 4, 0}, {4, 4, 4, 4, 0}, {4, 0, 4, 0, 0}, {0, 0, 0, 0, 0},
	}
	if err := FillLitTexturedPolygon(fb, tex, cm, verts); err != nil {
		t.Fatalf("FillLitTexturedPolygon: %v", err)
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

func TestFillLitTexturedPolygon_DiamondTriggersSortSwap(t *testing.T) {
	fb, _ := NewFrameBuffer(16, 16)
	tex := makeTex4x4()
	cm := makeNoopCM()
	verts := []LitTexturedVertex{
		{8, 0, 2, 0, 0}, {16, 8, 4, 2, 0}, {8, 16, 2, 4, 0}, {0, 8, 0, 2, 0},
	}
	if err := FillLitTexturedPolygon(fb, tex, cm, verts); err != nil {
		t.Fatalf("FillLitTexturedPolygon: %v", err)
	}
	if got := fb.Pixels[8*fb.Pitch+8]; got != 0x22 {
		t.Fatalf("diamond center = %#02x want 0x22", got)
	}
}

func TestFillLitTexturedPolygon_ClippedOffScreen(t *testing.T) {
	// Polygon partially off all four sides -> exercises both x and y
	// clamp branches.
	fb, _ := NewFrameBuffer(8, 8)
	fb.Clear(0xAB)
	tex := makeTex4x4()
	cm := makeNoopCM()
	verts := []LitTexturedVertex{
		{-4, -4, 0, 0, 0}, {12, -4, 4, 0, 0}, {12, 12, 4, 4, 0}, {-4, 12, 0, 4, 0},
	}
	if err := FillLitTexturedPolygon(fb, tex, cm, verts); err != nil {
		t.Fatalf("FillLitTexturedPolygon: %v", err)
	}
	for _, b := range fb.Pixels {
		if b == 0xAB {
			t.Fatalf("clipped poly left sentinel pixels untouched")
		}
	}
}

func TestFillLitTexturedPolygon_FullyOffScreenY(t *testing.T) {
	fb, _ := NewFrameBuffer(8, 8)
	fb.Clear(0xCC)
	tex := makeTex4x4()
	cm := makeNoopCM()
	verts := []LitTexturedVertex{
		{0, 100, 0, 0, 0}, {4, 100, 4, 0, 0}, {2, 110, 2, 4, 0},
	}
	if err := FillLitTexturedPolygon(fb, tex, cm, verts); err != nil {
		t.Fatalf("FillLitTexturedPolygon: %v", err)
	}
	for _, b := range fb.Pixels {
		if b != 0xCC {
			t.Fatalf("off-screen poly wrote into fb")
		}
	}
}

func TestFillLitTexturedPolygon_FullyOffScreenLeft(t *testing.T) {
	fb, _ := NewFrameBuffer(8, 8)
	fb.Clear(0xDD)
	tex := makeTex4x4()
	cm := makeNoopCM()
	verts := []LitTexturedVertex{
		{-20, 0, 0, 0, 0}, {-10, 0, 4, 0, 0}, {-15, 8, 2, 4, 0},
	}
	if err := FillLitTexturedPolygon(fb, tex, cm, verts); err != nil {
		t.Fatalf("FillLitTexturedPolygon: %v", err)
	}
	for _, b := range fb.Pixels {
		if b != 0xDD {
			t.Fatalf("off-screen-left poly wrote into fb")
		}
	}
}

func TestFillLitTexturedPolygon_SubPixelWidthSkip(t *testing.T) {
	// Sliver triangle: ceil(left) > floor(right) -> per-pair continue.
	fb, _ := NewFrameBuffer(16, 16)
	tex := makeTex4x4()
	cm := makeNoopCM()
	verts := []LitTexturedVertex{
		{10.6, 5, 1, 0, 0}, {10.9, 5, 2, 0, 0}, {10.7, 15, 1.5, 4, 0},
	}
	if err := FillLitTexturedPolygon(fb, tex, cm, verts); err != nil {
		t.Fatalf("FillLitTexturedPolygon: %v", err)
	}
	_ = fb
}
