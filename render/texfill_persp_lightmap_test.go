// Copyright (c) 2026 the go-quake1/engine authors.
// SPDX-License-Identifier: GPL-2.0-or-later

package render

import (
	"errors"
	"testing"
)

// makeSrcEchoCM returns a colormap where cm[r][s] == s for all r --
// every row is the identity over the source byte. With this LUT
// the framebuffer pixel equals the raw texel, so tests can assert
// "the right texel was sampled" without lightmap interference.
// (Distinct from makeIdentityCM in texfill_lit_test.go which encodes
// the LIGHT row, not the source byte.)
func makeSrcEchoCM() *ColorMap {
	cm := &ColorMap{}
	for r := 0; r < ColorMapRows; r++ {
		for s := 0; s < ColorMapCols; s++ {
			cm[r][s] = byte(s)
		}
	}
	return cm
}

// makeRowEchoCM returns a colormap whose every cell encodes the
// row index (cm[r][s] == byte(r << 2)) regardless of source. Tests
// can read a framebuffer pixel and deduce the colormap row the
// rasterizer chose, which directly probes the lightmap pipeline.
func makeRowEchoCM() *ColorMap {
	cm := &ColorMap{}
	for r := 0; r < ColorMapRows; r++ {
		v := byte(r << 2)
		for s := 0; s < ColorMapCols; s++ {
			cm[r][s] = v
		}
	}
	return cm
}

// --- error-path tests ----------------------------------------------------

func TestFillPerspectiveLightmappedPolygon_NilFB(t *testing.T) {
	tex := makeTex4x4()
	cm := makeSrcEchoCM()
	verts := []LightmappedVertex{
		{0, 0, 1, 0, 0, 0, 0}, {4, 0, 1, 4, 0, 1, 0}, {0, 4, 1, 0, 4, 0, 1},
	}
	if err := FillPerspectiveLightmappedPolygon(nil, tex, cm, verts, []byte{255, 255, 255, 255}, 2, 2); !errors.Is(err, ErrLmFillNilFB) {
		t.Fatalf("err=%v want ErrLmFillNilFB", err)
	}
}

func TestFillPerspectiveLightmappedPolygon_NilTex(t *testing.T) {
	fb, _ := NewFrameBuffer(8, 8)
	cm := makeSrcEchoCM()
	verts := []LightmappedVertex{{0, 0, 1, 0, 0, 0, 0}, {4, 0, 1, 4, 0, 1, 0}, {0, 4, 1, 0, 4, 0, 1}}
	if err := FillPerspectiveLightmappedPolygon(fb, nil, cm, verts, []byte{255, 255, 255, 255}, 2, 2); !errors.Is(err, ErrLmFillNilTex) {
		t.Fatalf("err=%v want ErrLmFillNilTex", err)
	}
}

func TestFillPerspectiveLightmappedPolygon_NilCM(t *testing.T) {
	fb, _ := NewFrameBuffer(8, 8)
	tex := makeTex4x4()
	verts := []LightmappedVertex{{0, 0, 1, 0, 0, 0, 0}, {4, 0, 1, 4, 0, 1, 0}, {0, 4, 1, 0, 4, 0, 1}}
	if err := FillPerspectiveLightmappedPolygon(fb, tex, nil, verts, []byte{255, 255, 255, 255}, 2, 2); !errors.Is(err, ErrLmFillNilCM) {
		t.Fatalf("err=%v want ErrLmFillNilCM", err)
	}
}

func TestFillPerspectiveLightmappedPolygon_FewVerts(t *testing.T) {
	fb, _ := NewFrameBuffer(8, 8)
	tex := makeTex4x4()
	cm := makeSrcEchoCM()
	verts := []LightmappedVertex{{0, 0, 1, 0, 0, 0, 0}, {4, 0, 1, 4, 0, 1, 0}}
	if err := FillPerspectiveLightmappedPolygon(fb, tex, cm, verts, []byte{255, 255, 255, 255}, 2, 2); !errors.Is(err, ErrLmFillFewVerts) {
		t.Fatalf("err=%v want ErrLmFillFewVerts", err)
	}
}

func TestFillPerspectiveLightmappedPolygon_ManyVerts(t *testing.T) {
	fb, _ := NewFrameBuffer(8, 8)
	tex := makeTex4x4()
	cm := makeSrcEchoCM()
	verts := make([]LightmappedVertex, MaxPolyVerts+1)
	for i := range verts {
		verts[i] = LightmappedVertex{X: 0, Y: 0, Z: 1, LmS: 0, LmT: 0}
	}
	if err := FillPerspectiveLightmappedPolygon(fb, tex, cm, verts, []byte{255, 255, 255, 255}, 2, 2); !errors.Is(err, ErrLmFillManyVerts) {
		t.Fatalf("err=%v want ErrLmFillManyVerts", err)
	}
}

func TestFillPerspectiveLightmappedPolygon_BadTexShape(t *testing.T) {
	fb, _ := NewFrameBuffer(8, 8)
	bad := &Pic{Width: 4, Height: 4, Pixels: make([]byte, 7)}
	cm := makeSrcEchoCM()
	verts := []LightmappedVertex{{0, 0, 1, 0, 0, 0, 0}, {4, 0, 1, 4, 0, 1, 0}, {0, 4, 1, 0, 4, 0, 1}}
	if err := FillPerspectiveLightmappedPolygon(fb, bad, cm, verts, []byte{255, 255, 255, 255}, 2, 2); !errors.Is(err, ErrPicShape) {
		t.Fatalf("err=%v want ErrPicShape", err)
	}
}

func TestFillPerspectiveLightmappedPolygon_PlaneEmpty(t *testing.T) {
	fb, _ := NewFrameBuffer(8, 8)
	tex := makeTex4x4()
	cm := makeSrcEchoCM()
	verts := []LightmappedVertex{{0, 0, 1, 0, 0, 0, 0}, {4, 0, 1, 4, 0, 1, 0}, {0, 4, 1, 0, 4, 0, 1}}
	if err := FillPerspectiveLightmappedPolygon(fb, tex, cm, verts, nil, 0, 1); !errors.Is(err, ErrLmFillPlaneEmpty) {
		t.Fatalf("err=%v want ErrLmFillPlaneEmpty", err)
	}
	if err := FillPerspectiveLightmappedPolygon(fb, tex, cm, verts, nil, 1, 0); !errors.Is(err, ErrLmFillPlaneEmpty) {
		t.Fatalf("err=%v want ErrLmFillPlaneEmpty (h=0)", err)
	}
}

func TestFillPerspectiveLightmappedPolygon_BadPlaneSize(t *testing.T) {
	fb, _ := NewFrameBuffer(8, 8)
	tex := makeTex4x4()
	cm := makeSrcEchoCM()
	verts := []LightmappedVertex{{0, 0, 1, 0, 0, 0, 0}, {4, 0, 1, 4, 0, 1, 0}, {0, 4, 1, 0, 4, 0, 1}}
	if err := FillPerspectiveLightmappedPolygon(fb, tex, cm, verts, []byte{255, 255, 255}, 2, 2); !errors.Is(err, ErrLmFillBadPlane) {
		t.Fatalf("err=%v want ErrLmFillBadPlane", err)
	}
}

func TestFillPerspectiveLightmappedPolygon_ZeroZ(t *testing.T) {
	fb, _ := NewFrameBuffer(8, 8)
	tex := makeTex4x4()
	cm := makeSrcEchoCM()
	verts := []LightmappedVertex{{0, 0, 0, 0, 0, 0, 0}, {4, 0, 1, 4, 0, 1, 0}, {0, 4, 1, 0, 4, 0, 1}}
	if err := FillPerspectiveLightmappedPolygon(fb, tex, cm, verts, []byte{255, 255, 255, 255}, 2, 2); !errors.Is(err, ErrLmFillZeroZ) {
		t.Fatalf("err=%v want ErrLmFillZeroZ", err)
	}
}

// --- happy-path / pixel-output tests -----------------------------------

// Uniform fully-lit plane (255 everywhere) + identity CM == raw texel
// for every pixel. Verifies the wrap + sampling pipeline routes
// texels through to the framebuffer unchanged when the lightmap is
// effectively a no-op.
func TestFillPerspectiveLightmappedPolygon_UniformLitNoopMatchesTex(t *testing.T) {
	const W, H = 8, 8
	fb, _ := NewFrameBuffer(W, H)
	tex := makeTex4x4()
	cm := makeSrcEchoCM()
	// 4x4 quad mapped 1:1 to a 4x4 texture, uniform Z, lightmap
	// quad with LmS/LmT in [0,1] (a 2x2 plane).
	verts := []LightmappedVertex{
		{X: 0, Y: 0, Z: 10, U: 0, V: 0, LmS: 0, LmT: 0},
		{X: 4, Y: 0, Z: 10, U: 4, V: 0, LmS: 1, LmT: 0},
		{X: 4, Y: 4, Z: 10, U: 4, V: 4, LmS: 1, LmT: 1},
		{X: 0, Y: 4, Z: 10, U: 0, V: 4, LmS: 0, LmT: 1},
	}
	plane := []byte{255, 255, 255, 255}
	if err := FillPerspectiveLightmappedPolygon(fb, tex, cm, verts, plane, 2, 2); err != nil {
		t.Fatalf("FillPerspectiveLightmappedPolygon: %v", err)
	}
	// Pixel (2,2) center -> U=V=2.5; floor -> (2,2); tex[2][2] = 0x22.
	if got := fb.Pixels[2*fb.Pitch+2]; got != 0x22 {
		t.Fatalf("uniformly-lit (2,2)=%#02x want 0x22", got)
	}
}

// Uniform DARK plane (0 everywhere) + row-echo CM. Every pixel should
// land on colormap row 63 (darkest) -> CM emits byte(63<<2)==0xfc.
func TestFillPerspectiveLightmappedPolygon_UniformDarkSelectsDarkestRow(t *testing.T) {
	const W, H = 8, 8
	fb, _ := NewFrameBuffer(W, H)
	tex := makeTex4x4()
	cm := makeRowEchoCM()
	verts := []LightmappedVertex{
		{X: 0, Y: 0, Z: 10, U: 0, V: 0, LmS: 0, LmT: 0},
		{X: 4, Y: 0, Z: 10, U: 4, V: 0, LmS: 1, LmT: 0},
		{X: 4, Y: 4, Z: 10, U: 4, V: 4, LmS: 1, LmT: 1},
		{X: 0, Y: 4, Z: 10, U: 0, V: 4, LmS: 0, LmT: 1},
	}
	plane := []byte{0, 0, 0, 0}
	if err := FillPerspectiveLightmappedPolygon(fb, tex, cm, verts, plane, 2, 2); err != nil {
		t.Fatalf("FillPerspectiveLightmappedPolygon: %v", err)
	}
	// All pixels: lightmap sample = 0 -> row = (255-0)>>2 = 63 ->
	// cm[63][anything] = byte(63<<2) = 0xfc.
	if got := fb.Pixels[2*fb.Pitch+2]; got != 0xfc {
		t.Fatalf("uniformly-dark (2,2)=%#02x want 0xfc (row 63)", got)
	}
}

// Lightmap gradient: 0 left, 255 right (2x1 plane). The right edge
// should land brighter (lower row) than the left edge.
func TestFillPerspectiveLightmappedPolygon_GradientProducesGradient(t *testing.T) {
	const W, H = 16, 16
	fb, _ := NewFrameBuffer(W, H)
	tex := makeTex4x4()
	cm := makeRowEchoCM()
	verts := []LightmappedVertex{
		{X: 0, Y: 0, Z: 10, U: 0, V: 0, LmS: 0, LmT: 0},
		{X: 12, Y: 0, Z: 10, U: 4, V: 0, LmS: 1, LmT: 0},
		{X: 12, Y: 12, Z: 10, U: 4, V: 4, LmS: 1, LmT: 1},
		{X: 0, Y: 12, Z: 10, U: 0, V: 4, LmS: 0, LmT: 1},
	}
	plane := []byte{0, 255, 0, 255}
	if err := FillPerspectiveLightmappedPolygon(fb, tex, cm, verts, plane, 2, 2); err != nil {
		t.Fatalf("FillPerspectiveLightmappedPolygon: %v", err)
	}
	rowMid := 6 * fb.Pitch
	left := fb.Pixels[rowMid+1]   // near LmS=0 -> dark -> high row
	right := fb.Pixels[rowMid+10] // near LmS=1 -> light -> low row
	if left <= right {
		t.Fatalf("gradient inverted: left=%#02x right=%#02x (expect left > right)", left, right)
	}
}

// Negative LmS clamps to the first sample column; positive past lmW-1
// clamps to the last. Verifies the per-pixel clamp path.
func TestFillPerspectiveLightmappedPolygon_LmCoordClamp(t *testing.T) {
	const W, H = 8, 8
	fb, _ := NewFrameBuffer(W, H)
	tex := makeTex4x4()
	cm := makeRowEchoCM()
	// LmS span from -2 to +3 across an 8-pixel quad on a 2x1 plane:
	// left half wants negative (clamps to col 0 = dark = high row),
	// right half wants > 1 (clamps to col 1 = light = low row).
	verts := []LightmappedVertex{
		{X: 0, Y: 0, Z: 10, U: 0, V: 0, LmS: -2, LmT: 0},
		{X: 8, Y: 0, Z: 10, U: 4, V: 0, LmS: 3, LmT: 0},
		{X: 8, Y: 8, Z: 10, U: 4, V: 4, LmS: 3, LmT: 1},
		{X: 0, Y: 8, Z: 10, U: 0, V: 4, LmS: -2, LmT: 1},
	}
	plane := []byte{0, 255, 0, 255}
	if err := FillPerspectiveLightmappedPolygon(fb, tex, cm, verts, plane, 2, 2); err != nil {
		t.Fatalf("FillPerspectiveLightmappedPolygon: %v", err)
	}
	left := fb.Pixels[3*fb.Pitch+0]
	right := fb.Pixels[3*fb.Pitch+7]
	if left != 0xfc {
		t.Fatalf("clamp-low (0,3) row=%#02x want 0xfc (dark)", left)
	}
	if right != 0x00 {
		t.Fatalf("clamp-high (7,3) row=%#02x want 0x00 (bright)", right)
	}
}

// Off-screen polygon (yEnd <= yStart after clamp): returns nil
// without touching the framebuffer.
func TestFillPerspectiveLightmappedPolygon_OffScreenNoOp(t *testing.T) {
	const W, H = 8, 8
	fb, _ := NewFrameBuffer(W, H)
	for i := range fb.Pixels {
		fb.Pixels[i] = 0x77
	}
	tex := makeTex4x4()
	cm := makeSrcEchoCM()
	verts := []LightmappedVertex{
		{X: 0, Y: -10, Z: 10, U: 0, V: 0, LmS: 0, LmT: 0},
		{X: 4, Y: -10, Z: 10, U: 4, V: 0, LmS: 1, LmT: 0},
		{X: 4, Y: -2, Z: 10, U: 4, V: 4, LmS: 1, LmT: 1},
		{X: 0, Y: -2, Z: 10, U: 0, V: 4, LmS: 0, LmT: 1},
	}
	if err := FillPerspectiveLightmappedPolygon(fb, tex, cm, verts, []byte{255, 255, 255, 255}, 2, 2); err != nil {
		t.Fatalf("FillPerspectiveLightmappedPolygon: %v", err)
	}
	if fb.Pixels[0] != 0x77 {
		t.Fatalf("off-screen polygon wrote to fb: %#02x", fb.Pixels[0])
	}
}

// Negative LmT exercises the < 0 branch; large LmT exercises the
// > lmMaxT branch + the ti1 == lmMaxT clamp.
func TestFillPerspectiveLightmappedPolygon_LmTClamp(t *testing.T) {
	const W, H = 8, 8
	fb, _ := NewFrameBuffer(W, H)
	tex := makeTex4x4()
	cm := makeRowEchoCM()
	// LmT span from -1 (top) to +3 (bottom) across an 8-pixel quad
	// on a 2x2 plane; top half clamps to row T=0 (dark), bottom
	// clamps to row T=1 (light). Plane:
	//   T=0: 0, 0   (dark)
	//   T=1: 255, 255 (light)
	verts := []LightmappedVertex{
		{X: 0, Y: 0, Z: 10, U: 0, V: 0, LmS: 0, LmT: -1},
		{X: 8, Y: 0, Z: 10, U: 4, V: 0, LmS: 1, LmT: -1},
		{X: 8, Y: 8, Z: 10, U: 4, V: 4, LmS: 1, LmT: 3},
		{X: 0, Y: 8, Z: 10, U: 0, V: 4, LmS: 0, LmT: 3},
	}
	plane := []byte{0, 0, 255, 255}
	if err := FillPerspectiveLightmappedPolygon(fb, tex, cm, verts, plane, 2, 2); err != nil {
		t.Fatalf("FillPerspectiveLightmappedPolygon: %v", err)
	}
	top := fb.Pixels[0*fb.Pitch+4]
	bot := fb.Pixels[7*fb.Pitch+4]
	if top != 0xfc {
		t.Fatalf("LmT clamp-low (4,0)=%#02x want 0xfc (dark)", top)
	}
	if bot != 0x00 {
		t.Fatalf("LmT clamp-high (4,7)=%#02x want 0x00 (bright)", bot)
	}
}

// Polygon with verts[2] / [3] at smaller Y than verts[0] exercises
// the yMin update branch + a vert past fb.Height exercises the
// yEnd clamp; a left-of-screen X exercises the x0<0 clamp.
func TestFillPerspectiveLightmappedPolygon_BoundsClamps(t *testing.T) {
	const W, H = 8, 8
	fb, _ := NewFrameBuffer(W, H)
	tex := makeTex4x4()
	cm := makeSrcEchoCM()
	// Quad straddles all three: yMin update (verts[2].Y < verts[0].Y),
	// yEnd clamp (Y > H), x0 clamp (X < 0).
	verts := []LightmappedVertex{
		{X: 4, Y: 4, Z: 10, U: 0, V: 0, LmS: 0, LmT: 0},
		{X: 10, Y: 12, Z: 10, U: 4, V: 0, LmS: 1, LmT: 0},
		{X: -4, Y: 0, Z: 10, U: 0, V: 4, LmS: 0, LmT: 1},
		{X: -2, Y: 2, Z: 10, U: 0, V: 4, LmS: 0, LmT: 1},
	}
	if err := FillPerspectiveLightmappedPolygon(fb, tex, cm, verts, []byte{255, 255, 255, 255}, 2, 2); err != nil {
		t.Fatalf("FillPerspectiveLightmappedPolygon: %v", err)
	}
}

// A scanline whose ceil(xLeft) > floor(xRight) (sub-pixel-wide span)
// hits the `continue` branch without writing any pixel.
func TestFillPerspectiveLightmappedPolygon_SubpixelSpanContinue(t *testing.T) {
	const W, H = 8, 8
	fb, _ := NewFrameBuffer(W, H)
	for i := range fb.Pixels {
		fb.Pixels[i] = 0x77
	}
	tex := makeTex4x4()
	cm := makeSrcEchoCM()
	// Very narrow polygon: width < 1 px so ceil(xLeft) > floor(xRight)
	// at every scanline -> every span hits the continue path.
	verts := []LightmappedVertex{
		{X: 1.2, Y: 0, Z: 10, U: 0, V: 0, LmS: 0, LmT: 0},
		{X: 1.4, Y: 0, Z: 10, U: 4, V: 0, LmS: 1, LmT: 0},
		{X: 1.4, Y: 4, Z: 10, U: 4, V: 4, LmS: 1, LmT: 1},
		{X: 1.2, Y: 4, Z: 10, U: 0, V: 4, LmS: 0, LmT: 1},
	}
	if err := FillPerspectiveLightmappedPolygon(fb, tex, cm, verts, []byte{255, 255, 255, 255}, 2, 2); err != nil {
		t.Fatalf("FillPerspectiveLightmappedPolygon: %v", err)
	}
	// Sentinel preserved -> no pixel was written.
	if fb.Pixels[2*fb.Pitch+2] != 0x77 {
		t.Fatalf("subpixel-narrow polygon wrote a pixel: %#02x", fb.Pixels[2*fb.Pitch+2])
	}
}

// Wider polygon (> PerspSubdivStep) exercises the multi-sub-span
// branch of the inner loop.
func TestFillPerspectiveLightmappedPolygon_MultiSubSpan(t *testing.T) {
	const W, H = 24, 4
	fb, _ := NewFrameBuffer(W, H)
	tex := makeTex4x4()
	cm := makeSrcEchoCM()
	verts := []LightmappedVertex{
		{X: 0, Y: 0, Z: 5, U: 0, V: 0, LmS: 0, LmT: 0},
		{X: 24, Y: 0, Z: 5, U: 4, V: 0, LmS: 1, LmT: 0},
		{X: 24, Y: 4, Z: 5, U: 4, V: 4, LmS: 1, LmT: 1},
		{X: 0, Y: 4, Z: 5, U: 0, V: 4, LmS: 0, LmT: 1},
	}
	if err := FillPerspectiveLightmappedPolygon(fb, tex, cm, verts, []byte{255, 255, 255, 255}, 2, 2); err != nil {
		t.Fatalf("FillPerspectiveLightmappedPolygon: %v", err)
	}
	// Spot-check: pixel (12, 2) center maps to U=2, V=2 (4x4 tex
	// wraps cleanly), texel = vi<<4|ui = 0x22.
	if got := fb.Pixels[2*fb.Pitch+12]; got != 0x22 {
		t.Fatalf("multi-subspan mid pixel=%#02x want 0x22", got)
	}
}
