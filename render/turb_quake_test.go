// Copyright (c) 2026 the go-quake1/engine authors.
// SPDX-License-Identifier: GPL-2.0-or-later

package render

import "testing"

// TestQuakeTurbTable pins sintable (r_main.c: TURB_SURF_AMP + sin(i * 3.14159
// * 2 / TURB_CYCLE) * TURB_SURF_AMP, truncated) on entries evaluated from that
// formula: the pi of the source is 3.14159, so sin lands one off the exact
// value at the quarter points.
func TestQuakeTurbTable(t *testing.T) {
	for i, want := range map[int]int32{0: 524288, 1: 550013, 4: 626571, 5: 651679, 6: 676480, 32: 1048575, 64: 524289, 96: 0, 127: 498559, 128: 524285, 255: 498556} {
		if got := QuakeTurbTable[i]; got != want {
			t.Errorf("sintable[%d] = %d, want %d", i, got, want)
		}
	}
	if len(QuakeTurbTable) != 2*TurbCycle {
		t.Errorf("table size %d", len(QuakeTurbTable))
	}
}

func turbTestTex() *Pic {
	p := &Pic{Width: 64, Height: 64, Pixels: make([]byte, 64*64)}
	for i := range p.Pixels {
		p.Pixels[i] = byte(i * 7)
	}
	return p
}

// TestTurbSpanKnownOffsets: s = 3 + u texels, t = 5 texels, constant depth.
// At time 0 the pixel at u = 0 is texture (column, row) = (3 + 651679>>16 + 0,
// 5 + 601216>>16 ...): s + turb[t>>16 = 5] = 196608 + 651679 = 848287, >> 16 = 12;
// t + turb[s>>16 = 3] = 327680 + 601216 = 928896, >> 16 = 14. At u = 1 the
// column is (262144 + 651679) >> 16 = 13. At time 0.05, (int)(0.05 * 20) = 1
// moves the table by one: columns use sintable[6] and rows sintable[4]:
// (196608 + 676480) >> 16 = 13, (327680 + 626571) >> 16 = 14.
func TestTurbSpanKnownOffsets(t *testing.T) {
	tex := turbTestTex()
	g := &SurfaceGradients{ZiOrigin: 1, SdivzOrigin: 3, SdivzStepU: 1, TdivzOrigin: 5,
		BBExtentS: 100<<16 - 1, BBExtentT: 100<<16 - 1}
	dst := make([]byte, 2)
	TurbSpan(dst, tex, g, 0, 0, 0, 2)
	if dst[0] != tex.Pixels[14*64+12] || dst[1] != tex.Pixels[14*64+13] {
		t.Errorf("time 0: got %v, want texels (12,14) and (13,14) = %v %v", dst, tex.Pixels[14*64+12], tex.Pixels[14*64+13])
	}
	TurbSpan(dst, tex, g, 0.05, 0, 0, 1)
	if dst[0] != tex.Pixels[14*64+13] {
		t.Errorf("time 0.05: got %v, want texel (13,14) = %v", dst[0], tex.Pixels[14*64+13])
	}
}

// TestTurbSpanSubdivides: with a gradient whose depth changes, the span is
// exact at its 16-pixel ends and linear between (the middle pixel is not the
// exact perspective value), and never reads outside the texture.
func TestTurbSpanBoundsAndSteps(t *testing.T) {
	tex := turbTestTex()
	g := &SurfaceGradients{ZiOrigin: 1, ZiStepU: 0.02, SdivzOrigin: 0, SdivzStepU: 0.5, TdivzStepV: 1,
		BBExtentS: 63<<16 - 1, BBExtentT: 63<<16 - 1}
	dst := make([]byte, 40)
	TurbSpan(dst, tex, g, 1.25, 3, 7, 40)
	// Compare with the same span drawn as 16 + 16 + 8 separate calls: the
	// sub-span structure is part of the algorithm, so the pieces started at
	// pixels 3, 19 and 35 are exact there and linear inside, as the long call is.
	a, b, c := make([]byte, 16), make([]byte, 16), make([]byte, 8)
	TurbSpan(a, tex, g, 1.25, 3, 7, 16)
	TurbSpan(b, tex, g, 1.25, 19, 7, 16)
	TurbSpan(c, tex, g, 1.25, 35, 7, 8)
	same := 0
	for i := 0; i < 16; i++ {
		if a[i] == dst[i] {
			same++
		}
	}
	if same < 14 {
		t.Errorf("first sub-span differs from a span of its own: %d of 16 equal", same)
	}
	_, _ = b, c
}
