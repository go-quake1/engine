// Copyright (c) 2026 the go-quake1/engine authors.
// SPDX-License-Identifier: GPL-2.0-or-later

package render

import "testing"

func TestFillSkyQuakeAndCache(t *testing.T) {
	tex := &Pic{Width: 256, Height: 128, Pixels: make([]byte, 256*128)}
	for i := range tex.Pixels {
		tex.Pixels[i] = 7
	}
	var c SkyCache
	tab := c.Table(tex, 0)
	if tab == nil {
		t.Fatal("a 256 x 128 sky has no table")
	}
	if c.Table(tex, 0.05) != tab { // shift (int)(0.05 * 8) = 0: the table is kept
		t.Error("the table was rebuilt without a new shift")
	}
	if c.Table(tex, 3) == tab { // shift 24
		t.Error("the table was kept across a new shift")
	}
	if c.Table(&Pic{Width: 4, Height: 4, Pixels: make([]byte, 16)}, 0) != nil {
		t.Error("a 4 x 4 texture has a sky table")
	}
	fb := filledFB(t)
	if err := FillSkyQuake(fb, tab, skyTestView(), 0, square()); err != nil {
		t.Fatal(err)
	}
	if n := covered(fb); n != 25*24 {
		t.Errorf("%d pixels covered, want %d", n, 25*24)
	}
	_ = FillSkyQuake(fb, tab, skyTestView(), 0, []TexturedVertex{{X: -10, Y: -10}, {X: 50, Y: 10}, {X: 20, Y: 60}})
	_ = FillSkyQuake(fb, tab, skyTestView(), 0, []TexturedVertex{{X: 5.2, Y: 5}, {X: 5.4, Y: 5}, {X: 5.3, Y: 9}})
	if FillSkyQuake(nil, tab, skyTestView(), 0, square()) != ErrSkyFillNilFB ||
		FillSkyQuake(fb, tab, skyTestView(), 0, square()[:2]) != ErrSkyFillFewVerts ||
		FillSkyQuake(fb, tab, skyTestView(), 0, make([]TexturedVertex, MaxPolyVerts+1)) != ErrSkyFillManyVerts {
		t.Error("a bad argument was not refused")
	}
	// a portrait frame takes its height as the scale (D_Sky_uv_To_st)
	if s, _ := SkyST(skyTestView(), 100, 200, 0, 50, 100); s != 378*65536 {
		t.Errorf("portrait centre s = %d", s)
	}
}
