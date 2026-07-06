// Copyright (c) 2026 the go-quake1/engine authors.
// SPDX-License-Identifier: GPL-2.0-or-later

package render

import (
	"errors"
	"testing"
)

// --- BakeSurface -------------------------------------------------------------

func TestBakeSurface_Errors(t *testing.T) {
	tex := makeTex4x4()
	cm := makeSrcEchoCM()
	plane := []byte{255, 255, 255, 255}
	dst := &CachedSurface{}

	if err := BakeSurface(nil, tex, cm, plane, 2, 2, 0, 0); !errors.Is(err, ErrBakeNilDst) {
		t.Fatalf("nil dst: %v", err)
	}
	if err := BakeSurface(dst, nil, cm, plane, 2, 2, 0, 0); !errors.Is(err, ErrBakeNilTex) {
		t.Fatalf("nil tex: %v", err)
	}
	if err := BakeSurface(dst, tex, nil, plane, 2, 2, 0, 0); !errors.Is(err, ErrBakeNilCM) {
		t.Fatalf("nil cm: %v", err)
	}
	bad := &Pic{Width: 4, Height: 4, Pixels: make([]byte, 5)}
	if err := BakeSurface(dst, bad, cm, plane, 2, 2, 0, 0); !errors.Is(err, ErrBakeTexShape) {
		t.Fatalf("tex shape: %v", err)
	}
	if err := BakeSurface(dst, &Pic{Width: 0, Height: 0, Pixels: nil}, cm, plane, 2, 2, 0, 0); !errors.Is(err, ErrBakeTexShape) {
		t.Fatalf("tex zero dims: %v", err)
	}
	if err := BakeSurface(dst, tex, cm, plane, 0, 2, 0, 0); !errors.Is(err, ErrBakePlaneDims) {
		t.Fatalf("lmW=0: %v", err)
	}
	if err := BakeSurface(dst, tex, cm, plane, 2, 0, 0, 0); !errors.Is(err, ErrBakePlaneDims) {
		t.Fatalf("lmH=0: %v", err)
	}
	if err := BakeSurface(dst, tex, cm, []byte{1, 2, 3}, 2, 2, 0, 0); !errors.Is(err, ErrBakePlaneDims) {
		t.Fatalf("plane len mismatch: %v", err)
	}
}

func TestBakeSurface_UniformBrightEchoesTexture(t *testing.T) {
	// Uniform plane == 255 -> cmRow 0. With srcEcho cm (cm[r][s]==s) the
	// baked pixel is the raw texel, so the surface is the texture tiled over
	// the face extent.
	tex := makeTex4x4() // pixel(u,v) = v<<4 | u
	cm := makeSrcEchoCM()
	plane := []byte{255, 255, 255, 255}
	dst := &CachedSurface{}
	if err := BakeSurface(dst, tex, cm, plane, 2, 2, 0, 0); err != nil {
		t.Fatalf("bake: %v", err)
	}
	// lmW=lmH=2 -> W=H=(2-1)*16+1 = 17.
	if dst.W != 17 || dst.H != 17 {
		t.Fatalf("dims = %dx%d, want 17x17", dst.W, dst.H)
	}
	// texel at (cx,cy)=(0,0) -> tex(0,0)=0x00.
	if got := dst.Pixels[0]; got != 0x00 {
		t.Errorf("(0,0)=%#02x want 0x00", got)
	}
	// (cx,cy)=(5,3) -> tex u=5%4=1, v=3%4=3 -> 0x31.
	if got := dst.Pixels[3*17+5]; got != 0x31 {
		t.Errorf("(5,3)=%#02x want 0x31", got)
	}
}

func TestBakeSurface_UniformDarkUsesRow63(t *testing.T) {
	// Uniform plane == 0 -> cmRow 63. With rowEcho cm (cm[r][s]==r<<2) the
	// baked pixel is 63<<2 = 0xfc regardless of texel.
	tex := makeTex4x4()
	cm := makeRowEchoCM()
	plane := []byte{0, 0, 0, 0}
	dst := &CachedSurface{}
	if err := BakeSurface(dst, tex, cm, plane, 2, 2, 5, 7); err != nil {
		t.Fatalf("bake: %v", err)
	}
	for _, p := range dst.Pixels {
		if p != 0xfc {
			t.Fatalf("dark bake pixel=%#02x want 0xfc", p)
		}
	}
}

func TestBakeSurface_ReuseWarmDst(t *testing.T) {
	tex := makeTex4x4()
	cm := makeSrcEchoCM()
	dst := &CachedSurface{}
	if err := BakeSurface(dst, tex, cm, []byte{255, 255, 255, 255}, 2, 2, 0, 0); err != nil {
		t.Fatalf("bake1: %v", err)
	}
	firstCap := cap(dst.Pixels)
	// Second bake, same dims -> reuses the buffer (no growth).
	if err := BakeSurface(dst, tex, cm, []byte{0, 0, 0, 0}, 2, 2, 0, 0); err != nil {
		t.Fatalf("bake2: %v", err)
	}
	if cap(dst.Pixels) != firstCap {
		t.Errorf("reuse grew cap %d -> %d", firstCap, cap(dst.Pixels))
	}
	// srcEcho + dark plane(0) -> cmRow 63 -> cm[63][texel]==texel (srcEcho is
	// row-independent), so still echoes texture. Spot-check (0,0)=0x00.
	if dst.Pixels[0] != 0x00 {
		t.Errorf("reuse (0,0)=%#02x want 0x00", dst.Pixels[0])
	}
}

// --- FillPerspectiveCachedPolygon --------------------------------------------

func makeCachedSurf(w, h int) *CachedSurface {
	s := &CachedSurface{W: w, H: h, Pixels: make([]byte, w*h)}
	for i := range s.Pixels {
		s.Pixels[i] = byte(i)
	}
	return s
}

func TestFillPerspectiveCachedPolygon_Errors(t *testing.T) {
	fb, _ := NewFrameBuffer(16, 16)
	surf := makeCachedSurf(8, 8)
	good := []CachedVertex{{0, 0, 1, 0, 0}, {8, 0, 1, 8, 0}, {0, 8, 1, 0, 8}}

	if err := FillPerspectiveCachedPolygon(nil, surf, good); !errors.Is(err, ErrCachedFillNilFB) {
		t.Fatalf("nil fb: %v", err)
	}
	if err := FillPerspectiveCachedPolygon(fb, nil, good); !errors.Is(err, ErrCachedFillNilSurf) {
		t.Fatalf("nil surf: %v", err)
	}
	if err := FillPerspectiveCachedPolygon(fb, surf, good[:2]); !errors.Is(err, ErrCachedFillFewVerts) {
		t.Fatalf("few verts: %v", err)
	}
	many := make([]CachedVertex, MaxPolyVerts+1)
	for i := range many {
		many[i] = CachedVertex{0, 0, 1, 0, 0}
	}
	if err := FillPerspectiveCachedPolygon(fb, surf, many); !errors.Is(err, ErrCachedFillManyVerts) {
		t.Fatalf("many verts: %v", err)
	}
	if err := FillPerspectiveCachedPolygon(fb, &CachedSurface{W: 0, H: 8, Pixels: nil}, good); !errors.Is(err, ErrCachedFillSurfEmpty) {
		t.Fatalf("surf empty: %v", err)
	}
	if err := FillPerspectiveCachedPolygon(fb, &CachedSurface{W: 8, H: 8, Pixels: make([]byte, 10)}, good); !errors.Is(err, ErrCachedFillSurfShape) {
		t.Fatalf("surf shape: %v", err)
	}
	zeroZ := []CachedVertex{{0, 0, 1, 0, 0}, {8, 0, 0, 8, 0}, {0, 8, 1, 0, 8}}
	if err := FillPerspectiveCachedPolygon(fb, surf, zeroZ); !errors.Is(err, ErrCachedFillZeroZ) {
		t.Fatalf("zero z: %v", err)
	}
}

func TestFillPerspectiveCachedPolygon_SamplesSurface(t *testing.T) {
	fb, _ := NewFrameBuffer(32, 32)
	surf := makeCachedSurf(16, 16) // Pixels[i]=byte(i); pixel(u,v)=v*16+u
	// Uniform-Z quad (affine), screen 0..16 mapped to cache 0..16.
	verts := []CachedVertex{
		{0, 0, 1, 0, 0},
		{16, 0, 1, 16, 0},
		{16, 16, 1, 16, 16},
		{0, 16, 1, 0, 16},
	}
	if err := FillPerspectiveCachedPolygon(fb, surf, verts); err != nil {
		t.Fatalf("fill: %v", err)
	}
	// Interior pixel (5,6): cache coord ~ (5,6) -> surf pixel 6*16+5 = 101.
	if got := fb.Pixels[6*fb.Pitch+5]; got != 101 {
		t.Errorf("(5,6)=%d want 101", got)
	}
}

func TestFillPerspectiveCachedPolygon_ClampsCoords(t *testing.T) {
	fb, _ := NewFrameBuffer(32, 32)
	surf := makeCachedSurf(8, 8) // max index 63 at (7,7)
	// Cache coords intentionally run past the surface (0..40) so the inner
	// clamp pins to the last row/col (index 7).
	verts := []CachedVertex{
		{0, 0, 1, -20, -20},
		{16, 0, 1, 40, -20},
		{16, 16, 1, 40, 40},
		{0, 16, 1, -20, 40},
	}
	if err := FillPerspectiveCachedPolygon(fb, surf, verts); err != nil {
		t.Fatalf("fill: %v", err)
	}
	// Top-left samples clamp to (0,0)=0.
	if got := fb.Pixels[1*fb.Pitch+1]; got != 0 {
		t.Errorf("clamped TL=%d want 0", got)
	}
	// Bottom-right samples clamp to (7,7)=63.
	if got := fb.Pixels[14*fb.Pitch+14]; got != 63 {
		t.Errorf("clamped BR=%d want 63", got)
	}
}

func TestFillPerspectiveCachedPolygon_WideSpanSubdiv(t *testing.T) {
	// A span far wider than PerspSubdivStep exercises both the full-subspan
	// and final-partial-subspan branches, under real perspective (Z varies).
	fb, _ := NewFrameBuffer(128, 64)
	surf := makeCachedSurf(64, 8)
	verts := []CachedVertex{
		{2, 2, 1, 0, 0},
		{120, 2, 4, 64, 0},
		{120, 40, 4, 64, 8},
		{2, 40, 1, 0, 8},
	}
	if err := FillPerspectiveCachedPolygon(fb, surf, verts); err != nil {
		t.Fatalf("fill: %v", err)
	}
	// Something got drawn on a mid scanline.
	drawn := false
	for x := 0; x < fb.Width; x++ {
		if fb.Pixels[20*fb.Pitch+x] != 0 {
			drawn = true
			break
		}
	}
	if !drawn {
		t.Errorf("wide-span fill drew nothing on row 20")
	}
}

func TestFillPerspectiveCachedPolygon_OffScreenClips(t *testing.T) {
	fb, _ := NewFrameBuffer(16, 16)
	surf := makeCachedSurf(8, 8)

	// Entirely above/below the viewport -> yStart>=yEnd, returns nil no-op.
	above := []CachedVertex{{0, -20, 1, 0, 0}, {8, -20, 1, 8, 0}, {4, -12, 1, 4, 8}}
	if err := FillPerspectiveCachedPolygon(fb, surf, above); err != nil {
		t.Fatalf("above: %v", err)
	}

	// Spans off the right edge (x0 > x1 after clip) + past top/bottom (yStart
	// clamps to 0, yEnd clamps to Height). Large screen-space quad straddling
	// all four edges exercises the x/y clamps and the x0>x1 continue.
	big := []CachedVertex{
		{-10, -10, 1, 0, 0},
		{40, -10, 1, 8, 0},
		{40, 40, 1, 8, 8},
		{-10, 40, 1, 0, 8},
	}
	if err := FillPerspectiveCachedPolygon(fb, surf, big); err != nil {
		t.Fatalf("big: %v", err)
	}
	if fb.Pixels[8*fb.Pitch+8] == 0 && fb.Pixels[8*fb.Pitch+7] == 0 {
		// The clamped surface's non-zero region should have painted the
		// interior; a fully-zero interior would mean nothing drew.
		t.Errorf("clipped big quad drew nothing in the interior")
	}
}

func TestFillPerspectiveCachedPolygon_YMinFromLaterVertex(t *testing.T) {
	// verts[0] is NOT the topmost vertex, so the yMin-update branch fires.
	fb, _ := NewFrameBuffer(16, 16)
	surf := makeCachedSurf(8, 8)
	verts := []CachedVertex{
		{5, 10, 1, 4, 4}, // vert0: middle
		{0, 0, 1, 0, 0},  // vert1: topmost -> updates yMin below vert0.Y
		{10, 0, 1, 8, 0},
	}
	if err := FillPerspectiveCachedPolygon(fb, surf, verts); err != nil {
		t.Fatalf("fill: %v", err)
	}
}

func TestFillPerspectiveCachedPolygon_SpanEntirelyRight(t *testing.T) {
	// A triangle wholly to the RIGHT of the 16px-wide viewport: every span's
	// x0 = ceil(xLeft) > x1 = Width-1 after clip, exercising the x0>x1
	// continue. Nothing should be drawn.
	fb, _ := NewFrameBuffer(16, 16)
	surf := makeCachedSurf(8, 8)
	verts := []CachedVertex{
		{20, 2, 1, 0, 0},
		{30, 2, 1, 8, 0},
		{25, 12, 1, 4, 8},
	}
	if err := FillPerspectiveCachedPolygon(fb, surf, verts); err != nil {
		t.Fatalf("fill: %v", err)
	}
	for _, p := range fb.Pixels {
		if p != 0 {
			t.Fatalf("off-right triangle painted a pixel (%d)", p)
		}
	}
}

// --- parity: bake+cached matches the lightmapped path ------------------------

func TestSurfaceCacheMatchesLightmapped(t *testing.T) {
	// Bake a face, then fill it via the cached path, and fill the SAME face
	// via the per-pixel lightmapped path; the two framebuffers must match on
	// the covered interior (both use the same texture, plane bilinear, and
	// colormap-row mapping).
	tex := &Pic{Width: 16, Height: 16, Pixels: make([]byte, 256)}
	for i := range tex.Pixels {
		tex.Pixels[i] = byte(i)
	}
	cm := makeSrcEchoCM() // isolate texture selection (row-independent)
	// 2x2 lightmap, uniform bright -> row 0 everywhere.
	plane := []byte{255, 255, 255, 255}
	lmW, lmH := 2, 2

	dst := &CachedSurface{}
	if err := BakeSurface(dst, tex, cm, plane, lmW, lmH, 0, 0); err != nil {
		t.Fatalf("bake: %v", err)
	}

	// Same screen quad for both paths, uniform Z (affine) to avoid
	// perspective/rounding divergence in the comparison.
	fbA, _ := NewFrameBuffer(40, 40)
	fbB, _ := NewFrameBuffer(40, 40)

	cverts := []CachedVertex{
		{4, 4, 1, 0, 0}, {20, 4, 1, 16, 0}, {20, 20, 1, 16, 16}, {4, 20, 1, 0, 16},
	}
	if err := FillPerspectiveCachedPolygon(fbA, dst, cverts); err != nil {
		t.Fatalf("cached fill: %v", err)
	}
	lverts := []LightmappedVertex{
		{X: 4, Y: 4, Z: 1, U: 0, V: 0, LmS: 0, LmT: 0},
		{X: 20, Y: 4, Z: 1, U: 16, V: 0, LmS: 1, LmT: 0},
		{X: 20, Y: 20, Z: 1, U: 16, V: 16, LmS: 1, LmT: 1},
		{X: 4, Y: 20, Z: 1, U: 0, V: 16, LmS: 0, LmT: 1},
	}
	if err := FillPerspectiveLightmappedPolygon(fbB, tex, cm, lverts, plane, lmW, lmH); err != nil {
		t.Fatalf("lightmapped fill: %v", err)
	}
	diff := 0
	for i := range fbA.Pixels {
		if fbA.Pixels[i] != fbB.Pixels[i] {
			diff++
		}
	}
	if diff != 0 {
		t.Errorf("cached vs lightmapped differ in %d pixels (want 0)", diff)
	}
}
