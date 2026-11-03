// Copyright (c) 2026 the go-quake1/engine authors.
// SPDX-License-Identifier: GPL-2.0-or-later

package render

import "testing"

// TestSkyFrameTime: R_SetSkyFrame, g = gcd(8, 2) = 2, s1 = 4, s2 = 1, so the
// period is SKYSIZE * 4 * 1 = 512.
func TestSkyFrameTime(t *testing.T) {
	for in, want := range map[float64]float64{0: 0, 511.5: 511.5, 512: 0, 600: 88, 1024.25: 0.25} {
		if got := SkyFrameTime(in); got != want {
			t.Errorf("SkyFrameTime(%v) = %v, want %v", in, got, want)
		}
	}
}

// TestSkySTKnownValues: at the screen centre end = (1, 0, 0) exactly, so
// s = (skytime * 8 + 6 * 63 * 1) * 65536 and t = skytime * 8 * 65536.
func TestSkySTKnownValues(t *testing.T) {
	for _, c := range []struct {
		skytime float64
		s, t    int32
	}{{0, 378 * 65536, 0}, {10, (80 + 378) * 65536, 80 * 65536}} {
		s, tt := SkyST(skyTestView(), 320, 200, c.skytime, 160, 100)
		if s != c.s || tt != c.t {
			t.Errorf("skytime %v: (%d, %d), want (%d, %d)", c.skytime, s, tt, c.s, c.t)
		}
	}
	// One pixel to the right of the centre turns the ray towards -y (right
	// is -y): wu = 8192 / 320 = 25.6, end = (4096, -25.6, 0) / |..|, so t is
	// negative, 6 * 63 * (-25.6 / 4096.04) * 65536 about -154,800.
	_, tt := SkyST(skyTestView(), 320, 200, 0, 161, 100)
	if tt > -154000 || tt < -155500 {
		t.Errorf("t one pixel right of the centre: %d", tt)
	}
}

// TestQuakeSkyTable: the masked left half scrolls by (int)(skytime * 8) in
// both directions where it is not clear; the right half does not move.
func TestQuakeSkyTable(t *testing.T) {
	tex := &Pic{Width: 256, Height: 128, Pixels: make([]byte, 256*128)}
	for y := 0; y < 128; y++ {
		for x := 128; x < 256; x++ {
			tex.Pixels[y*256+x] = 3
		}
	}
	tex.Pixels[7*256+5] = 99
	q, ok := NewQuakeSky(tex)
	if !ok {
		t.Fatal("a 256 x 128 sky was refused")
	}
	if tab := q.Table(0); tab[7*128+5] != 99 || tab[0] != 3 {
		t.Errorf("shift 0: %d at (5, 7), %d at (0, 0)", tab[7*128+5], tab[0])
	}
	// skytime 1: shift 8, so the 99 shows at (5 - 8, 7 - 8) mod 128 = (125, 127).
	tab := q.Table(1)
	if tab[127*128+125] != 99 || tab[7*128+5] != 3 {
		t.Errorf("shift 8: %d at (125, 127), %d at (5, 7)", tab[127*128+125], tab[7*128+5])
	}
	if _, ok := NewQuakeSky(&Pic{Width: 128, Height: 128, Pixels: make([]byte, 128*128)}); ok {
		t.Error("a 128 x 128 texture was taken for a sky")
	}
}

// TestSkySpanIsExactAtTheSubSpanStarts: sub-spans are 32 pixels, so pixels 0
// and 32 of a span are the table at exactly SkyST of those pixels.
func TestSkySpanIsExactAtTheSubSpanStarts(t *testing.T) {
	var tab [SkySize * SkySize]byte
	for i := range tab {
		tab[i] = byte(i*13 + i>>7)
	}
	view := skyTestView()
	dst := make([]byte, 40)
	SkySpan(dst, &tab, view, 320, 200, 2.5, 100, 60, 40)
	for _, px := range []int{0, 32} {
		s, tt := SkyST(view, 320, 200, 2.5, 100+px, 60)
		want := tab[((int(tt)>>16)&SkyMask)*SkySize+((int(s)>>16)&SkyMask)]
		if dst[px] != want {
			t.Errorf("pixel %d: %d, want %d", px, dst[px], want)
		}
	}
}
