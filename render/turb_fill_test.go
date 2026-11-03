// Copyright (c) 2026 the go-quake1/engine authors.
// SPDX-License-Identifier: GPL-2.0-or-later

package render

import (
	"math"
	"testing"
)

// A view looking along +x with right = -y and up = +z (all angles 0).
func skyTestView() Affine {
	return Affine{R: Mat3{{0, -1, 0}, {0, 0, 1}, {1, 0, 0}}}
}

// A wall at x = 100 seen from the origin along +x.
func wallPlane() SurfacePlane {
	return SurfacePlane{Normal: [3]float32{1, 0, 0}, Dist: 100, AxisS: [3]float32{0, 1, 0}, OffS: 5,
		AxisT: [3]float32{0, 0, 1}, ExtS: 64, ExtT: 64}
}

func TestNewSurfaceGradientsOnAWall(t *testing.T) {
	g, ok := NewSurfaceGradients(skyTestView(), 90, 320, 200, wallPlane())
	if !ok {
		t.Fatal("refused")
	}
	// 1/z is 0.01 everywhere on the wall (right and up are perpendicular to its normal).
	at := func(o, su, sv float32) float64 { return float64(o + 160*su + 100*sv) }
	zi := at(g.ZiOrigin, g.ZiStepU, g.ZiStepV)
	if math.Abs(zi-0.01) > 1e-7 {
		t.Errorf("1/z = %v, want 0.01", zi)
	}
	// Pixel (160, 100) has its centre 0.5 px right of the axis, 0.5 px below it:
	// y = -0.5 / 160 * 100 = -0.3125, so s = y + 5 = 4.6875 texels.
	if s := at(g.SdivzOrigin, g.SdivzStepU, g.SdivzStepV) / zi; math.Abs(s-4.6875) > 1e-3 {
		t.Errorf("s = %v, want 4.6875", s)
	}
	if tt := at(g.TdivzOrigin, g.TdivzStepU, g.TdivzStepV) / zi; math.Abs(tt+0.3125) > 1e-3 {
		t.Errorf("t = %v, want -0.3125", tt)
	}
	if g.BBExtentS != 64<<16-1 || g.BBExtentT != 64<<16-1 {
		t.Errorf("extents %d %d", g.BBExtentS, g.BBExtentT)
	}
	if _, ok := NewSurfaceGradients(skyTestView(), 0, 320, 200, wallPlane()); ok {
		t.Error("a zero field of view was accepted")
	}
	through := wallPlane()
	through.Dist = 0 // a plane through the viewer
	if _, ok := NewSurfaceGradients(skyTestView(), 90, 320, 200, through); ok {
		t.Error("a plane through the viewer was accepted")
	}
}

func square() []TexturedVertex {
	return []TexturedVertex{{X: 4, Y: 4}, {X: 28, Y: 4}, {X: 28, Y: 28}, {X: 4, Y: 28}}
}

func filledFB(t *testing.T) *FrameBuffer {
	t.Helper()
	fb, err := NewFrameBuffer(32, 32)
	if err != nil {
		t.Fatal(err)
	}
	for i := range fb.Pixels {
		fb.Pixels[i] = 0xFF
	}
	return fb
}

func covered(fb *FrameBuffer) int {
	n := 0
	for _, p := range fb.Pixels {
		if p != 0xFF {
			n++
		}
	}
	return n
}

func TestFillTurbulentQuakeCoversTheScanlineRuns(t *testing.T) {
	tex := &Pic{Width: 64, Height: 64, Pixels: make([]byte, 64*64)}
	g, _ := NewSurfaceGradients(skyTestView(), 90, 32, 32, wallPlane())
	fb := filledFB(t)
	if err := FillTurbulentQuake(fb, tex, &g, 1, square()); err != nil {
		t.Fatal(err)
	}
	// columns 4..28 (a pixel is taken when its left edge is inside), rows 4..27.
	if n := covered(fb); n != 25*24 {
		t.Errorf("%d pixels covered, want %d", n, 25*24)
	}
	// a triangle partly off the frame, and a sliver whose runs are empty,
	// exercise the clipping.
	_ = FillTurbulentQuake(fb, tex, &g, 1, []TexturedVertex{{X: -10, Y: -10}, {X: 50, Y: 10}, {X: 20, Y: 60}})
	_ = FillTurbulentQuake(fb, tex, &g, 1, []TexturedVertex{{X: 5.2, Y: 5}, {X: 5.4, Y: 5}, {X: 5.3, Y: 9}})
	for _, c := range []struct {
		name string
		err  error
		got  error
	}{
		{"nil fb", ErrTurbFillNilFB, FillTurbulentQuake(nil, tex, &g, 0, square())},
		{"nil tex", ErrTurbFillNilTex, FillTurbulentQuake(fb, nil, &g, 0, square())},
		{"few", ErrTurbFillFewVerts, FillTurbulentQuake(fb, tex, &g, 0, square()[:2])},
		{"many", ErrTurbFillManyVerts, FillTurbulentQuake(fb, tex, &g, 0, make([]TexturedVertex, MaxPolyVerts+1))},
		{"shape", ErrPicShape, FillTurbulentQuake(fb, &Pic{Width: 2, Height: 2}, &g, 0, square())},
	} {
		if c.got != c.err {
			t.Errorf("%s: %v, want %v", c.name, c.got, c.err)
		}
	}
}

func TestTurbSpanNonStandardTextureAndClamps(t *testing.T) {
	tex := &Pic{Width: 32, Height: 16, Pixels: make([]byte, 32*16)}
	for i := range tex.Pixels {
		tex.Pixels[i] = byte(i)
	}
	// s grows past the extent and t is negative: both are clamped, and no read
	// leaves the texture (no panic).
	g := &SurfaceGradients{ZiOrigin: 1, SdivzOrigin: 10, SdivzStepU: 40, TdivzOrigin: -5, BBExtentS: 3<<16 - 1, BBExtentT: 3<<16 - 1}
	TurbSpan(make([]byte, 20), tex, g, 0, 0, 0, 20)
	for in, want := range map[int32]int32{100: 50, 3: 16, 20: 20} {
		if got := clampNext(in, 50); got != want {
			t.Errorf("clampNext(%d, 50) = %d, want %d", in, got, want)
		}
	}
}

// A span of 17 pixels ends in a sub-span of one pixel, which has no step.
func TestTurbSpanEndsInASinglePixel(t *testing.T) {
	tex := &Pic{Width: 64, Height: 64, Pixels: make([]byte, 64*64)}
	g := &SurfaceGradients{ZiOrigin: 1, SdivzOrigin: 3, SdivzStepU: 1, TdivzOrigin: 5, BBExtentS: 100<<16 - 1, BBExtentT: 100<<16 - 1}
	TurbSpan(make([]byte, 17), tex, g, 0, 0, 0, 17)
	g.SdivzOrigin = -3 // s starts below zero and is clamped
	TurbSpan(make([]byte, 17), tex, g, 0, 0, 0, 17)
}

func TestEntityRotationAtRest(t *testing.T) {
	r := EntityRotation(0, 0, 0)
	if r[0][0] != 1 || r[1][1] != 1 || r[2][2] != 1 || r[0][1] != 0 || r[1][0] != 0 {
		t.Errorf("not the identity at zero angles: %v", r)
	}
	// yaw 90 turns the forward column (1, 0, 0) into (0, 1, 0)
	if r := EntityRotation(0, 90, 0); math.Abs(float64(r[1][0])-1) > 1e-6 || math.Abs(float64(r[0][0])) > 1e-6 {
		t.Errorf("yaw 90: %v", r)
	}
}
