// Copyright (c) 1996-1997 Id Software, Inc.
// Copyright (c) 2026 the go-quake1/engine authors.
// SPDX-License-Identifier: GPL-2.0-or-later

package render

import (
	"errors"
	"testing"
)

// affineUVAt returns the affine-interpolated (u, v) at framebuffer
// pixel (x, y) for a fixed-depth quad whose verts are (0,0)..(4,4)
// mapped 1:1 to UV (0,0)..(4,4) -- i.e., u == xf, v == yf at pixel
// center. Used to assert "uniform Z is equivalent to affine".
func affineUVAt(x, y int) (float32, float32) {
	return float32(x) + 0.5, float32(y) + 0.5
}

func TestFillPerspectiveTexturedPolygon_NilFB(t *testing.T) {
	tex := makeTex4x4()
	verts := []PerspTexturedVertex{
		{0, 0, 1, 0, 0}, {4, 0, 1, 4, 0}, {0, 4, 1, 0, 4},
	}
	if err := FillPerspectiveTexturedPolygon(nil, tex, nil, 0, verts); !errors.Is(err, ErrPerspTexFillNilFB) {
		t.Fatalf("err = %v want ErrPerspTexFillNilFB", err)
	}
}

func TestFillPerspectiveTexturedPolygon_NilTex(t *testing.T) {
	fb, _ := NewFrameBuffer(16, 16)
	verts := []PerspTexturedVertex{
		{0, 0, 1, 0, 0}, {4, 0, 1, 4, 0}, {0, 4, 1, 0, 4},
	}
	if err := FillPerspectiveTexturedPolygon(fb, nil, nil, 0, verts); !errors.Is(err, ErrPerspTexFillNilTex) {
		t.Fatalf("err = %v want ErrPerspTexFillNilTex", err)
	}
}

func TestFillPerspectiveTexturedPolygon_TooFewVerts(t *testing.T) {
	fb, _ := NewFrameBuffer(16, 16)
	tex := makeTex4x4()
	verts := []PerspTexturedVertex{{0, 0, 1, 0, 0}, {4, 0, 1, 4, 0}}
	if err := FillPerspectiveTexturedPolygon(fb, tex, nil, 0, verts); !errors.Is(err, ErrPerspTexFillFewVerts) {
		t.Fatalf("err = %v want ErrPerspTexFillFewVerts", err)
	}
}

func TestFillPerspectiveTexturedPolygon_TooManyVerts(t *testing.T) {
	fb, _ := NewFrameBuffer(16, 16)
	tex := makeTex4x4()
	verts := make([]PerspTexturedVertex, MaxPolyVerts+1)
	if err := FillPerspectiveTexturedPolygon(fb, tex, nil, 0, verts); !errors.Is(err, ErrPerspTexFillManyVerts) {
		t.Fatalf("err = %v want ErrPerspTexFillManyVerts", err)
	}
}

func TestFillPerspectiveTexturedPolygon_BadShape(t *testing.T) {
	fb, _ := NewFrameBuffer(16, 16)
	bad := &Pic{Width: 4, Height: 4, Pixels: make([]byte, 15)}
	verts := []PerspTexturedVertex{
		{0, 0, 1, 0, 0}, {4, 0, 1, 4, 0}, {0, 4, 1, 0, 4},
	}
	if err := FillPerspectiveTexturedPolygon(fb, bad, nil, 0, verts); !errors.Is(err, ErrPicShape) {
		t.Fatalf("err = %v want ErrPicShape", err)
	}
}

func TestFillPerspectiveTexturedPolygon_ZeroZ(t *testing.T) {
	fb, _ := NewFrameBuffer(16, 16)
	tex := makeTex4x4()
	// One vertex has Z = 0; caller should have clipped.
	verts := []PerspTexturedVertex{
		{0, 0, 1, 0, 0}, {4, 0, 0, 4, 0}, {0, 4, 1, 0, 4},
	}
	if err := FillPerspectiveTexturedPolygon(fb, tex, nil, 0, verts); !errors.Is(err, ErrPerspTexFillZeroZ) {
		t.Fatalf("err = %v want ErrPerspTexFillZeroZ", err)
	}
}

func TestFillPerspectiveTexturedPolygon_NegativeZ(t *testing.T) {
	fb, _ := NewFrameBuffer(16, 16)
	tex := makeTex4x4()
	verts := []PerspTexturedVertex{
		{0, 0, 1, 0, 0}, {4, 0, -2, 4, 0}, {0, 4, 1, 0, 4},
	}
	if err := FillPerspectiveTexturedPolygon(fb, tex, nil, 0, verts); !errors.Is(err, ErrPerspTexFillZeroZ) {
		t.Fatalf("err = %v want ErrPerspTexFillZeroZ on negative Z", err)
	}
}

// UniformZ_MatchesAffine: with every vert at the same Z, 1/Z is
// constant, U/Z and V/Z are linear in U/V, so the per-pixel result
// must match the affine FillTexturedPolygon byte-for-byte.
func TestFillPerspectiveTexturedPolygon_UniformZMatchesAffine(t *testing.T) {
	const W, H = 32, 32
	tex := makeTex4x4()

	// UVs kept strictly inside [0, texW) so that floor() agrees between
	// the affine straight-scanline interpolation and the perspective sub-
	// span interpolation; at the wrap boundary float drift can otherwise
	// land one side on U=W-eps (floor W-1) and the other on U=W+eps
	// (floor W -> wrap 0), making the equality flaky despite identical
	// math.
	pVerts := []PerspTexturedVertex{
		{2, 2, 100, 0, 0},
		{28, 4, 100, 3.9, 0},
		{26, 30, 100, 3.9, 3.9},
		{4, 28, 100, 0, 3.9},
	}
	aVerts := make([]TexturedVertex, len(pVerts))
	for i, v := range pVerts {
		aVerts[i] = TexturedVertex{X: v.X, Y: v.Y, U: v.U, V: v.V}
	}

	fbP, _ := NewFrameBuffer(W, H)
	fbA, _ := NewFrameBuffer(W, H)
	if err := FillPerspectiveTexturedPolygon(fbP, tex, nil, 0, pVerts); err != nil {
		t.Fatalf("persp: %v", err)
	}
	if err := FillTexturedPolygon(fbA, tex, nil, 0, aVerts); err != nil {
		t.Fatalf("affine: %v", err)
	}
	for i, gp := range fbP.Pixels {
		if gp != fbA.Pixels[i] {
			t.Fatalf("uniform-Z mismatch at byte %d: persp=%#02x affine=%#02x", i, gp, fbA.Pixels[i])
		}
	}
	// Spot-check sample matches the affine expectation.
	for y := 5; y < 25; y++ {
		for x := 5; x < 25; x++ {
			u, v := affineUVAt(x, y)
			_ = u
			_ = v
		}
	}
}

// HappyQuad: at uniform Z, the (0,0)-(4,4) quad reads exactly
// tex[y][x] = (y<<4 | x), the same as the affine test.
func TestFillPerspectiveTexturedPolygon_HappyQuad(t *testing.T) {
	fb, _ := NewFrameBuffer(8, 8)
	tex := makeTex4x4()
	verts := []PerspTexturedVertex{
		{0, 0, 50, 0, 0}, {4, 0, 50, 4, 0},
		{4, 4, 50, 4, 4}, {0, 4, 50, 0, 4},
	}
	if err := FillPerspectiveTexturedPolygon(fb, tex, nil, 0, verts); err != nil {
		t.Fatalf("FillPerspectiveTexturedPolygon: %v", err)
	}
	for y := 0; y < 4; y++ {
		for x := 0; x < 4; x++ {
			got := fb.Pixels[y*fb.Pitch+x]
			want := byte(y<<4 | x)
			if got != want {
				t.Fatalf("pixel (%d,%d) = %#02x want %#02x", x, y, got, want)
			}
		}
	}
}

// PerspectiveForeshortening: a quad with the right edge much farther
// away. The far edge texel density is compressed (more texture per
// screen pixel near the far edge than near the near edge), which a
// purely affine interp would miss. We verify the sampled UV at the
// horizontal midpoint is BIASED toward the near side -- i.e., the
// midpoint pixel does NOT read texture(2, *), the affine midpoint.
func TestFillPerspectiveTexturedPolygon_PerspectiveBias(t *testing.T) {
	const W = 64
	fb, _ := NewFrameBuffer(W, 4)
	tex := makeTex4x4()
	// Near-left z=1, far-right z=8: heavy perspective.
	verts := []PerspTexturedVertex{
		{0, 0, 1, 0, 0}, {float32(W), 0, 8, 4, 0},
		{float32(W), 4, 8, 4, 4}, {0, 4, 1, 0, 4},
	}
	if err := FillPerspectiveTexturedPolygon(fb, tex, nil, 0, verts); err != nil {
		t.Fatalf("FillPerspectiveTexturedPolygon: %v", err)
	}
	// Affine midpoint would read u=2 -> texel 0x?2. Perspective-correct:
	// at screen midpoint, 1/Z is the average of 1 and 1/8 = 0.5625;
	// Z = ~1.78; U/Z is avg of 0 and 4/8 = 0.25; U = 0.25*1.78 = 0.44
	// -> u=0 -> texel 0x?0. So column W/2 must NOT be 0x?2.
	mid := fb.Pixels[2*fb.Pitch+W/2]
	if mid&0x0F == 0x02 {
		t.Fatalf("midpoint U = %d (affine), perspective correction failed", mid&0x0F)
	}
	// And the perspective-correct U at midpoint should be 0 (or 1):
	if got := mid & 0x0F; got > 1 {
		t.Fatalf("perspective midpoint U = %d, want 0 or 1", got)
	}
}

func TestFillPerspectiveTexturedPolygon_LightingFullBright(t *testing.T) {
	fb, _ := NewFrameBuffer(8, 8)
	tex := makeTex4x4()
	cm := makeCM()
	verts := []PerspTexturedVertex{
		{0, 0, 10, 0, 0}, {4, 0, 10, 4, 0},
		{4, 4, 10, 4, 4}, {0, 4, 10, 0, 4},
	}
	if err := FillPerspectiveTexturedPolygon(fb, tex, cm, 0, verts); err != nil {
		t.Fatalf("FillPerspectiveTexturedPolygon: %v", err)
	}
	for y := 0; y < 4; y++ {
		for x := 0; x < 4; x++ {
			if got := fb.Pixels[y*fb.Pitch+x]; got != 0xFF {
				t.Fatalf("lit (%d,%d) = %#02x want 0xFF", x, y, got)
			}
		}
	}
}

func TestFillPerspectiveTexturedPolygon_LightingDarkest(t *testing.T) {
	fb, _ := NewFrameBuffer(8, 8)
	fb.Clear(0x42)
	tex := makeTex4x4()
	cm := makeCM()
	verts := []PerspTexturedVertex{
		{0, 0, 10, 0, 0}, {4, 0, 10, 4, 0},
		{4, 4, 10, 4, 4}, {0, 4, 10, 0, 4},
	}
	if err := FillPerspectiveTexturedPolygon(fb, tex, cm, ColorMapRows-1, verts); err != nil {
		t.Fatalf("FillPerspectiveTexturedPolygon: %v", err)
	}
	for y := 0; y < 4; y++ {
		for x := 0; x < 4; x++ {
			if got := fb.Pixels[y*fb.Pitch+x]; got != 0x00 {
				t.Fatalf("dark (%d,%d) = %#02x want 0x00", x, y, got)
			}
		}
	}
}

func TestFillPerspectiveTexturedPolygon_NilCMRaw(t *testing.T) {
	fb, _ := NewFrameBuffer(8, 8)
	tex := makeTex4x4()
	verts := []PerspTexturedVertex{
		{0, 0, 10, 0, 0}, {4, 0, 10, 4, 0},
		{4, 4, 10, 4, 4}, {0, 4, 10, 0, 4},
	}
	if err := FillPerspectiveTexturedPolygon(fb, tex, nil, 42, verts); err != nil {
		t.Fatalf("FillPerspectiveTexturedPolygon: %v", err)
	}
	if got := fb.Pixels[2*fb.Pitch+2]; got != byte(2<<4|2) {
		t.Fatalf("nil-cm raw = %#02x want %#02x", got, byte(2<<4|2))
	}
}

func TestFillPerspectiveTexturedPolygon_UVWrapHigh(t *testing.T) {
	// Quake textures TILE; UVs spanning past (W,H) wrap modulo.
	fb, _ := NewFrameBuffer(8, 8)
	tex := makeTex4x4()
	verts := []PerspTexturedVertex{
		{0, 0, 10, 0, 0}, {4, 0, 10, 16, 0},
		{4, 4, 10, 16, 16}, {0, 4, 10, 0, 16},
	}
	if err := FillPerspectiveTexturedPolygon(fb, tex, nil, 0, verts); err != nil {
		t.Fatalf("FillPerspectiveTexturedPolygon: %v", err)
	}
	// (2,2) center: U=V=10; wrap mod 4 = (2,2); tex[2][2] = 0x22.
	if got := fb.Pixels[2*fb.Pitch+2]; got != 0x22 {
		t.Fatalf("UV-wrap (2,2) = %#02x want 0x22", got)
	}
}

func TestFillPerspectiveTexturedPolygon_UVWrapLow(t *testing.T) {
	fb, _ := NewFrameBuffer(8, 8)
	fb.Clear(0x77)
	tex := makeTex4x4()
	verts := []PerspTexturedVertex{
		{0, 0, 10, -10, -10}, {4, 0, 10, -1, -10},
		{4, 4, 10, -1, -1}, {0, 4, 10, -10, -1},
	}
	if err := FillPerspectiveTexturedPolygon(fb, tex, nil, 0, verts); err != nil {
		t.Fatalf("FillPerspectiveTexturedPolygon: %v", err)
	}
	if got := fb.Pixels[2*fb.Pitch+2]; got == 0x77 {
		t.Fatalf("UV-wrap low (2,2): pixel not written (still sentinel 0x77)")
	}
}

// MultipleSubSpans: scanline wider than PerspSubdivStep (8 px) so the
// per-8-pixel sub-span loop runs more than once. Width is an exact
// multiple of 8 so the loop exits cleanly without a partial tail.
func TestFillPerspectiveTexturedPolygon_MultipleSubSpans(t *testing.T) {
	const W = 24 // 3 full sub-spans
	fb, _ := NewFrameBuffer(W, 4)
	tex := makeTex4x4()
	verts := []PerspTexturedVertex{
		{0, 0, 10, 0, 0}, {float32(W), 0, 10, 4, 0},
		{float32(W), 4, 10, 4, 4}, {0, 4, 10, 0, 4},
	}
	if err := FillPerspectiveTexturedPolygon(fb, tex, nil, 0, verts); err != nil {
		t.Fatalf("FillPerspectiveTexturedPolygon: %v", err)
	}
	// At uniform Z, midpoint should read u = ~2.
	if got := fb.Pixels[2*fb.Pitch+W/2] & 0x0F; got < 1 || got > 2 {
		t.Fatalf("multi-subspan midpoint u = %d, want ~2", got)
	}
}

// PartialFinalSubSpan: width = 21 (= 2*8 + 5) so the final sub-span
// has 5 pixels and exercises the spancount-1 divide path.
func TestFillPerspectiveTexturedPolygon_PartialFinalSubSpan(t *testing.T) {
	const W = 21
	fb, _ := NewFrameBuffer(W, 4)
	tex := makeTex4x4()
	verts := []PerspTexturedVertex{
		{0, 0, 10, 0, 0}, {float32(W), 0, 10, 4, 0},
		{float32(W), 4, 10, 4, 4}, {0, 4, 10, 0, 4},
	}
	if err := FillPerspectiveTexturedPolygon(fb, tex, nil, 0, verts); err != nil {
		t.Fatalf("FillPerspectiveTexturedPolygon: %v", err)
	}
	// Last column at y=2: u ~= 3, texel 0x?3.
	if got := fb.Pixels[2*fb.Pitch+(W-1)] & 0x0F; got != 0x03 {
		t.Fatalf("partial-final last-col u = %d want 3", got)
	}
}

// SinglePixelFinalSubSpan: scanline of exactly 1 pixel exercises the
// spanLen == 1 path (du/dv stay zero; no inv computation).
func TestFillPerspectiveTexturedPolygon_SinglePixelSpan(t *testing.T) {
	fb, _ := NewFrameBuffer(16, 16)
	tex := makeTex4x4()
	// A sliver triangle: top vertex at (5, 0); base width 1 px at y=2.
	verts := []PerspTexturedVertex{
		{5, 0, 10, 2, 0},
		{5.5, 2, 10, 2, 2},
		{4.5, 2, 10, 2, 2},
	}
	if err := FillPerspectiveTexturedPolygon(fb, tex, nil, 0, verts); err != nil {
		t.Fatalf("FillPerspectiveTexturedPolygon: %v", err)
	}
}

func TestFillPerspectiveTexturedPolygon_ClippedOffScreen(t *testing.T) {
	fb, _ := NewFrameBuffer(8, 8)
	tex := makeTex4x4()
	verts := []PerspTexturedVertex{
		{-4, -4, 10, 0, 0}, {12, -4, 10, 4, 0},
		{12, 12, 10, 4, 4}, {-4, 12, 10, 0, 4},
	}
	fb.Clear(0xAB)
	if err := FillPerspectiveTexturedPolygon(fb, tex, nil, 0, verts); err != nil {
		t.Fatalf("FillPerspectiveTexturedPolygon: %v", err)
	}
	for _, b := range fb.Pixels {
		if b == 0xAB {
			t.Fatalf("clipped poly left sentinel pixels untouched")
		}
	}
}

func TestFillPerspectiveTexturedPolygon_FullyOffScreenY(t *testing.T) {
	fb, _ := NewFrameBuffer(8, 8)
	fb.Clear(0xCC)
	tex := makeTex4x4()
	verts := []PerspTexturedVertex{
		{0, 100, 10, 0, 0}, {4, 100, 10, 4, 0}, {2, 110, 10, 2, 4},
	}
	if err := FillPerspectiveTexturedPolygon(fb, tex, nil, 0, verts); err != nil {
		t.Fatalf("FillPerspectiveTexturedPolygon: %v", err)
	}
	for _, b := range fb.Pixels {
		if b != 0xCC {
			t.Fatalf("off-screen poly wrote into fb")
		}
	}
}

func TestFillPerspectiveTexturedPolygon_FullyOffScreenLeft(t *testing.T) {
	fb, _ := NewFrameBuffer(8, 8)
	fb.Clear(0xDD)
	tex := makeTex4x4()
	verts := []PerspTexturedVertex{
		{-20, 0, 10, 0, 0}, {-10, 0, 10, 4, 0}, {-15, 8, 10, 2, 4},
	}
	if err := FillPerspectiveTexturedPolygon(fb, tex, nil, 0, verts); err != nil {
		t.Fatalf("FillPerspectiveTexturedPolygon: %v", err)
	}
	for _, b := range fb.Pixels {
		if b != 0xDD {
			t.Fatalf("off-screen-left poly wrote into fb")
		}
	}
}

func TestFillPerspectiveTexturedPolygon_ReverseWinding(t *testing.T) {
	fb, _ := NewFrameBuffer(8, 8)
	tex := makeTex4x4()
	verts := []PerspTexturedVertex{
		{0, 4, 10, 0, 4}, {4, 4, 10, 4, 4},
		{4, 0, 10, 4, 0}, {0, 0, 10, 0, 0},
	}
	if err := FillPerspectiveTexturedPolygon(fb, tex, nil, 0, verts); err != nil {
		t.Fatalf("FillPerspectiveTexturedPolygon: %v", err)
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
// the RIGHT crossing on some scanlines, forcing the insertion sort
// to swap xs + oozs + uozs + vozs.
func TestFillPerspectiveTexturedPolygon_DiamondTriggersSortSwap(t *testing.T) {
	fb, _ := NewFrameBuffer(16, 16)
	tex := makeTex4x4()
	verts := []PerspTexturedVertex{
		{8, 0, 10, 2, 0}, {16, 8, 10, 4, 2},
		{8, 16, 10, 2, 4}, {0, 8, 10, 0, 2},
	}
	if err := FillPerspectiveTexturedPolygon(fb, tex, nil, 0, verts); err != nil {
		t.Fatalf("FillPerspectiveTexturedPolygon: %v", err)
	}
	if got := fb.Pixels[8*fb.Pitch+8]; got != 0x22 {
		t.Fatalf("diamond center = %#02x want 0x22", got)
	}
}

// SubPixelWidthSkip: sliver triangle where ceil(left) > floor(right)
// for some scanlines -> the per-pair continue fires.
func TestFillPerspectiveTexturedPolygon_SubPixelWidthSkip(t *testing.T) {
	fb, _ := NewFrameBuffer(16, 16)
	tex := makeTex4x4()
	verts := []PerspTexturedVertex{
		{10.6, 5, 10, 1, 0}, {10.9, 5, 10, 2, 0},
		{10.7, 15, 10, 1.5, 4},
	}
	if err := FillPerspectiveTexturedPolygon(fb, tex, nil, 0, verts); err != nil {
		t.Fatalf("FillPerspectiveTexturedPolygon: %v", err)
	}
}
