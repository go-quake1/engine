// Copyright (c) 2026 the go-quake1/engine authors.
// SPDX-License-Identifier: GPL-2.0-or-later

package render

import (
	"testing"

	"github.com/go-quake1/engine/mdl"
)

// R_AliasCheckBBox (r_alias.c line 205) on a 320 x 200 view (tan(fov / 2) =
// 1: xscale 160, xcenter 159.5, ycenter 99.5), worked by hand.
func TestAliasCheckBBox(t *testing.T) {
	av := newAliasView(320, 200, 1)
	if av.scale != 160 || av.xcenter != 159.5 || av.ycenter != 99.5 {
		t.Fatalf("view %+v", av)
	}
	// res_scale = sqrt(320 * 200 / (320 * 152)) = 1.1471; transition 200 *
	// res_scale = 229.4, fudge 114.7: a size 0 model is far beyond z 230.
	if av.transition < 229.4 || av.transition > 229.5 || av.fudge < 114.7 || av.fudge > 114.8 {
		t.Fatalf("transition %v fudge %v", av.transition, av.fudge)
	}
	box := func(half float32) (mins, maxs [3]float32) {
		return [3]float32{-half, -half, -half}, [3]float32{half, half, half}
	}
	cases := []struct {
		name       string
		half, z    float32
		size       float32
		draw       bool
		accept     int
		offX, offY float32
	}{
		{"near, whole inside", 10, 100, 0, true, AliasUnclipped, 0, 0},
		{"far, whole inside", 1, 300, 0, true, AliasFarSubdiv, 0, 0},
		{"far but a large size keeps it near", 1, 300, 1.5, true, AliasUnclipped, 0, 0}, // 229.4 + 1.5 * 114.7 = 401
		{"partly off the left", 40, 100, 0, true, AliasClipped, -150, 0},
		{"entirely off the left", 5, 100, 0, false, 0, -400, 0},
		{"entirely above", 5, 100, 0, false, 0, 0, 400},
		{"entirely behind", 5, -20, 0, false, 0, 0, 0},
		{"across the near plane", 10, 3, 0, true, AliasClipped, 0, 0},
		{"across the near plane, off to the right", 10, 3, 0, false, 0, 600, 0},
	}
	for _, c := range cases {
		mins, maxs := box(c.half)
		corner := func(p [3]float32) [3]float32 { return [3]float32{p[0] + c.offX, p[1] + c.offY, p[2] + c.z} }
		draw, accept := aliasCheckBBox(av, mins, maxs, c.size, corner)
		if draw != c.draw || accept != c.accept {
			t.Errorf("%s: draw %v accept %d, want %v %d", c.name, draw, accept, c.draw, c.accept)
		}
	}
	// the bottom and right edges, and a point on the screen's far side
	mins, maxs := box(5)
	if d, _ := aliasCheckBBox(av, mins, maxs, 0, func(p [3]float32) [3]float32 { return [3]float32{p[0], p[1] - 400, p[2] + 100} }); d {
		t.Error("a box below the screen was drawn")
	}
	if d, _ := aliasCheckBBox(av, mins, maxs, 0, func(p [3]float32) [3]float32 { return [3]float32{p[0] + 400, p[1], p[2] + 100} }); d {
		t.Error("a box right of the screen was drawn")
	}
}

// R_AliasGetBBox: the box of the frame, of a group frame, or the union of two.
func TestAliasBBox(t *testing.T) {
	tv := func(a, b, c byte) mdl.TriVertx { return mdl.TriVertx{V: [3]byte{a, b, c}} }
	m := &mdl.Model{Frames: []mdl.Frame{
		{Type: mdl.FrameSingle, Single: mdl.SingleFrame{BBoxMin: tv(1, 2, 3), BBoxMax: tv(10, 20, 30)}},
		{Type: mdl.FrameSingle, Single: mdl.SingleFrame{BBoxMin: tv(0, 5, 4), BBoxMax: tv(8, 25, 29)}},
		{Type: mdl.FrameGroup, Group: &mdl.GroupFrame{BBoxMin: tv(7, 7, 7), BBoxMax: tv(9, 9, 9)}},
	}}
	if mn, mx := AliasBBox(m, 0, 0); mn != [3]float32{1, 2, 3} || mx != [3]float32{10, 20, 30} {
		t.Errorf("single: %v %v", mn, mx)
	}
	if mn, mx := AliasBBox(m, 0, 1); mn != [3]float32{0, 2, 3} || mx != [3]float32{10, 25, 30} {
		t.Errorf("union: %v %v", mn, mx)
	}
	if mn, mx := AliasBBox(m, 2, 2); mn != [3]float32{7, 7, 7} || mx != [3]float32{9, 9, 9} {
		t.Errorf("group: %v %v", mn, mx)
	}
}
