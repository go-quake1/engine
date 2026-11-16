// Copyright (c) 2026 the go-quake1/engine authors.
// SPDX-License-Identifier: GPL-2.0-or-later

package render

import "testing"

// Every expected value below was worked out by hand from
// reference/common/r_aclip.c (R_AliasClipTriangle, R_AliasClip, R_Alias_clip_z
// and _left/_right/_top/_bottom) and r_alias.c (R_AliasPreparePoints), not
// produced by the code under test. The view is 320 x 200 at a 90 degree field:
// xscale 160, centre (159.5, 99.5).

var aclipRect = AliasClipRect{X: 0, Y: 0, Right: 320, Bottom: 200}

func clipProj() aliasProject { return newAliasView(320, 200, 1).project }

func fvert(u, v, s, t, l int32, flags uint8) FinalVert {
	return FinalVert{V: [6]int32{u, v, s, t, l, 0}, Flags: flags}
}

func TestAliasClipFlags(t *testing.T) {
	for _, c := range []struct {
		u, v int32
		want uint8
	}{
		{0, 0, 0},
		{-1, 0, AliasLeftClip},
		{0, -1, AliasTopClip},
		{320, 200, 0}, // aliasvrectright is x + width: the first pixel past is not outside
		{321, 0, AliasRightClip},
		{0, 201, AliasBottomClip},
		{-5, 300, AliasLeftClip | AliasBottomClip},
	} {
		if got := AliasClipFlags(c.u, c.v, aclipRect); got != c.want {
			t.Errorf("flags(%d, %d) = %#x, want %#x", c.u, c.v, got, c.want)
		}
	}
}

// One vertex behind the near plane. A (0, 2, 10) projects to (159, 67), B (2,
// -2, 10) to (191, 131); C (0, 0, 1) is nearer than ALIAS_Z_CLIP_PLANE (5). Both
// edges to C cross z = 5 at scale (5 - 10) / (1 - 10) = 0.5555556 from the
// unclipped end:
//
//	C to A: (0, 2 - 2 * .5555556, 5) = (0, .8888888, 5): u 159, v -.8888888 * 160 * .2 + 99.5 = 71.06 -> 71;
//	        s, t, light 900, 1800, 2700 -> 0 + 900 * .5555556 = 500, 1000, 1500
//	B to C: (2 - 2 * .5555556, -2 + 2 * .5555556, 5) = (.8888888, -.8888888, 5):
//	        u .8888888 * 160 * .2 + 159.5 = 187.94 -> 187, v 28.44 + 99.5 = 127.94 -> 127;
//	        s 1000 - 100 * .5555556 = 944.4 -> 944, t 2000 - 200 * .5555556 -> 1888, light 3000 - 300 * .5555556 -> 2833
//
// and the polygon is [C to A, A, B, B to C].
func TestAliasClipTriangleNearPlane(t *testing.T) {
	tri := [3]FinalVert{
		fvert(159, 67, 0, 0, 0, 0),
		fvert(191, 131, 1000, 2000, 3000, 0),
		fvert(0, 0, 900, 1800, 2700, AliasZClip),
	}
	aux := [3][3]float32{{0, 2, 10}, {2, -2, 10}, {0, 0, 1}}
	var out [8]FinalVert
	k := AliasClipTriangle(tri, aux, true, 0, aclipRect, clipProj(), &out)
	want := [][5]int32{
		{159, 71, 500, 1000, 1500},
		{159, 67, 0, 0, 0},
		{191, 131, 1000, 2000, 3000},
		{187, 127, 944, 1888, 2833},
	}
	if k != len(want) {
		t.Fatalf("%d vertices, want %d: %+v", k, len(want), out[:k])
	}
	for i, w := range want {
		got := [5]int32{out[i].V[0], out[i].V[1], out[i].V[2], out[i].V[3], out[i].V[4]}
		if got != w {
			t.Errorf("vertex %d = %v, want %v", i, got, w)
		}
		if out[i].Flags != 0 {
			t.Errorf("vertex %d keeps flags %#x", i, out[i].Flags)
		}
	}
	// all three behind the plane: nothing is left
	for i := range tri {
		tri[i].Flags = AliasZClip
	}
	if k := AliasClipTriangle(tri, aux, true, 0, aclipRect, clipProj(), &out); k != 0 {
		t.Errorf("a triangle wholly behind the plane left %d vertices", k)
	}
	// two vertices behind: one vertex in front, so a triangle of 3
	tri = [3]FinalVert{
		fvert(159, 67, 0, 0, 0, 0),
		fvert(0, 0, 100, 100, 100, AliasZClip),
		fvert(0, 0, 200, 200, 200, AliasZClip),
	}
	aux = [3][3]float32{{0, 2, 10}, {2, -2, 1}, {-2, -2, 1}}
	if k := AliasClipTriangle(tri, aux, true, 0, aclipRect, clipProj(), &out); k != 3 {
		t.Errorf("two vertices behind the plane left %d vertices, want 3", k)
	}
}

// R_Alias_clip_left on a triangle A (-10, 50), B (30, 20), C (30, 80). The edge
// C to A crosses u = 0 at scale (0 - 30) / (-10 - 30) = .75 from the vertex with
// the larger u: (30 - 40 * .75 + .5, 80 - 30 * .75 + .5) = (0, 58), s 800 - 800
// * .75 + .5 = 200, t 900 - 800 * .75 = 300, light 80 - 80 * .75 = 20. The edge A to B
// crosses it from B: v 20 + 30 * .75 + .5 = 43, s 400 - 400 * .75 = 100, t 500 - 400 *
// .75 = 200, light 40 - 40 * .75 = 10. The polygon is [C to A, A to B, B, C].
func TestAliasClipTriangleLeft(t *testing.T) {
	tri := [3]FinalVert{
		fvert(-10, 50, 0, 100, 0, AliasLeftClip),
		fvert(30, 20, 400, 500, 40, 0),
		fvert(30, 80, 800, 900, 80, 0),
	}
	var out [8]FinalVert
	k := AliasClipTriangle(tri, [3][3]float32{}, true, 0, aclipRect, clipProj(), &out)
	want := [][5]int32{
		{0, 58, 200, 300, 20},
		{0, 43, 100, 200, 10},
		{30, 20, 400, 500, 40},
		{30, 80, 800, 900, 80},
	}
	if k != len(want) {
		t.Fatalf("%d vertices, want %d: %+v", k, len(want), out[:k])
	}
	for i, w := range want {
		got := [5]int32{out[i].V[0], out[i].V[1], out[i].V[2], out[i].V[3], out[i].V[4]}
		if got != w {
			t.Errorf("vertex %d = %v, want %v", i, got, w)
		}
	}
	// the same triangle seen from the other side of the rectangle: right clip
	// at u = 320, A (330, 50), B (290, 20), C (290, 80): the mirror of the above
	tri = [3]FinalVert{
		fvert(330, 50, 0, 100, 0, AliasRightClip),
		fvert(290, 20, 400, 500, 40, 0),
		fvert(290, 80, 800, 900, 80, 0),
	}
	k = AliasClipTriangle(tri, [3][3]float32{}, true, 0, aclipRect, clipProj(), &out)
	// R_Alias_clip_right goes from the vertex with the larger u, A, at scale
	// (320 - 330) / (290 - 330) = .25: u 330 - 40 * .25 + .5 = 320, v 50 + 30 *
	// .25 + .5 = 58 toward C and 50 - 30 * .25 + .5 = 43 toward B.
	if k != 4 || out[0].V[0] != 320 || out[1].V[0] != 320 {
		t.Fatalf("right clip: %d vertices, %+v", k, out[:k])
	}
}

// A triangle larger than the rectangle on every side is clipped to it: at most
// 8 vertices, all inside, the flags cleared.
func TestAliasClipTriangleAllSides(t *testing.T) {
	tri := [3]FinalVert{
		fvert(-50, -50, 0, 0, 0, AliasLeftClip|AliasTopClip),
		fvert(400, -50, 1000, 0, 100, AliasRightClip|AliasTopClip),
		fvert(100, 300, 500, 1000, 200, AliasBottomClip),
	}
	var out [8]FinalVert
	k := AliasClipTriangle(tri, [3][3]float32{}, true, 0, aclipRect, clipProj(), &out)
	if k < 3 || k > 8 {
		t.Fatalf("%d vertices", k)
	}
	for i := 0; i < k; i++ {
		v := out[i]
		if v.V[0] < 0 || v.V[0] > 320 || v.V[1] < 0 || v.V[1] > 200 || v.Flags != 0 {
			t.Errorf("vertex %d = %+v is outside the rectangle or flagged", i, v)
		}
	}
	// completely outside one side: nothing
	for i := range tri {
		tri[i] = fvert(-10-int32(i), 50+int32(i)*10, 0, 0, 0, AliasLeftClip)
	}
	if k := AliasClipTriangle(tri, [3][3]float32{}, true, 0, aclipRect, clipProj(), &out); k != 0 {
		t.Errorf("a triangle wholly left of the rectangle left %d vertices", k)
	}
}

// A back-facing triangle's seam vertices get seamfixupX16 added to s once, while
// the vertices are copied; the polygon's vertices are not on the seam any more.
func TestAliasClipTriangleSeam(t *testing.T) {
	tri := [3]FinalVert{
		{V: [6]int32{-10, 50, 1000, 0, 0, 0}, OnSeam: true, Flags: AliasLeftClip},
		{V: [6]int32{30, 20, 2000, 0, 0, 0}},
		{V: [6]int32{30, 80, 3000, 0, 0, 0}, OnSeam: true},
	}
	var back, front [8]FinalVert
	kb := AliasClipTriangle(tri, [3][3]float32{}, false, 500, aclipRect, clipProj(), &back)
	kf := AliasClipTriangle(tri, [3][3]float32{}, true, 500, aclipRect, clipProj(), &front)
	if kb != 4 || kf != 4 {
		t.Fatalf("%d and %d vertices", kb, kf)
	}
	// vertex 3 is C: 3000, plus the fixup when the triangle is back-facing
	if back[3].V[2] != 3500 || front[3].V[2] != 3000 {
		t.Errorf("C's s: back-facing %d (want 3500), front-facing %d (want 3000)", back[3].V[2], front[3].V[2])
	}
	for i := 0; i < kb; i++ {
		if back[i].OnSeam {
			t.Errorf("vertex %d is still on the seam", i)
		}
	}
}

// A triangle wholly beyond the right, bottom or top side leaves nothing after
// that side's stage (a triangle wholly past the left is the case above).
func TestAliasClipTriangleWhollyOutside(t *testing.T) {
	var out [8]FinalVert
	for _, c := range []struct {
		name string
		flag uint8
		v    [3][2]int32
	}{
		{"right", AliasRightClip, [3][2]int32{{330, 50}, {340, 20}, {350, 80}}},
		{"bottom", AliasBottomClip, [3][2]int32{{50, 210}, {20, 220}, {80, 230}}},
		{"top", AliasTopClip, [3][2]int32{{50, -10}, {20, -20}, {80, -30}}},
	} {
		var tri [3]FinalVert
		for i, p := range c.v {
			tri[i] = fvert(p[0], p[1], 0, 0, 0, c.flag)
		}
		if k := AliasClipTriangle(tri, [3][3]float32{}, true, 0, aclipRect, clipProj(), &out); k != 0 {
			t.Errorf("a triangle wholly past the %s side left %d vertices", c.name, k)
		}
	}
}
