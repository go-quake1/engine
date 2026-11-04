// Copyright (c) 2026 the go-quake1/engine authors.
// SPDX-License-Identifier: GPL-2.0-or-later

package render

import (
	"math/rand"
	"os"
	"regexp"
	"strconv"
	"testing"
)

// polyTestCM maps (row, texel) to row*3 + texel so that a pixel shows both.
func polyTestCM() *ColorMap {
	var cm ColorMap
	for r := range cm {
		for c := range cm[r] {
			cm[r][c] = byte(r*3 + c)
		}
	}
	return &cm
}

// polySkin is a w x h skin with texel i = i.
func polySkin(w, h int) *Pic {
	p := &Pic{Width: w, Height: h, Pixels: make([]byte, w*h)}
	for i := range p.Pixels {
		p.Pixels[i] = byte(i)
	}
	return p
}

// FloorDivMod and adivtab.h (reference/common/include/adivtab.h): the 32 x 32
// table of the quotient and remainder for numerators -15..16 (rows) and
// denominators -15..16 (columns), every entry.
func TestFloorDivModIsAdivtab(t *testing.T) {
	src, err := os.ReadFile("../reference/common/include/adivtab.h")
	if err != nil {
		t.Skip(err)
	}
	re := regexp.MustCompile(`(?m)^\{\s*(-?\d+),\s*(-?\d+)\s*\}`)
	m := re.FindAllStringSubmatch(string(src), -1)
	if len(m) != 32*32 {
		t.Fatalf("%d entries in adivtab.h, want 1024", len(m))
	}
	for i, e := range m {
		tm, tn := int32(i/32-15), int32(i%32-15)
		wq, _ := strconv.Atoi(e[1])
		wr, _ := strconv.Atoi(e[2])
		q, r := floorDivMod(tm, tn)
		if q != int32(wq) || r != int32(wr) {
			t.Fatalf("numerator %d denominator %d: %d %d, adivtab has %d %d", tm, tn, q, r, wq, wr)
		}
	}
}

// D_PolysetSetEdgeTable (d_polyse.c line 1181) for one triangle of each of
// the twelve tables, worked by hand from its tests: (y0, y1, y2) -> index.
func TestPolysetEdgeTableIndex(t *testing.T) {
	for _, c := range []struct {
		y0, y1, y2 int32
		want       int
	}{
		{0, 5, 10, 0}, {5, 0, 10, 1}, {0, 0, 5, 2}, {10, 0, 5, 3}, {0, 10, 5, 4}, {5, 5, 0, 5},
		{5, 10, 0, 6}, {10, 5, 0, 7}, {5, 0, 5, 8}, {0, 5, 0, 9}, {10, 5, 5, 10}, {0, 5, 5, 11},
	} {
		if got := PolysetEdgeTableIndex([6]int32{0, c.y0}, [6]int32{0, c.y1}, [6]int32{0, c.y2}); got != c.want {
			t.Errorf("y = %d %d %d: table %d, want %d", c.y0, c.y1, c.y2, got, c.want)
		}
	}
}

// A flat-top triangle worked by hand. v0 = (0,0), v1 = (8,0), v2 = (4,4);
// s = x, t = y (16.16) and light = 0x1000 + 256 y. d_xdenom = -32 (drawn),
// table 2: left edge v0 -> v2 (dx 4, dy 4: ubasestep 1), right edge v1 -> v2
// (dx -4, dy 4: ubasestep -1). Rows 0..3 cover x = 0..7, 1..6, 2..5, 3..4.
// The gradients (D_PolysetCalcGradients): lstepx 0, lstepy 256, sstepx 65536,
// sstepy 0, tstepx 0, tstepy 65536, a_ststepxwhole 1. So pixel (x, y) shows
// skin texel y * 16 + x through colormap row 16 + y.
func TestPolysetFlatTopTriangle(t *testing.T) {
	v := func(x, y int32) FinalVert {
		return FinalVert{V: [6]int32{x, y, x << 16, y << 16, 0x1000 + 256*y}}
	}
	v0, v1, v2 := v(0, 0), v(8, 0), v(4, 4)
	g := polyCalcGradients(&v0.V, &v1.V, &v2.V, -32, 16)
	if g.lstepx != 0 || g.lstepy != 256 || g.sstepx != 65536 || g.sstepy != 0 || g.tstepx != 0 || g.tstepy != 65536 || g.ststepxwhole != 1 {
		t.Fatalf("gradients %+v", g)
	}
	fb, _ := NewFrameBuffer(10, 6)
	for i := range fb.Pixels {
		fb.Pixels[i] = 0xEE
	}
	cm := polyTestCM()
	if !PolysetDrawTriangle(fb, polySkin(16, 16), cm, v0, v1, v2, true, 0) {
		t.Fatal("the triangle was not drawn")
	}
	first, last := []int{0, 1, 2, 3}, []int{7, 6, 5, 4}
	for y := 0; y < 6; y++ {
		for x := 0; x < 10; x++ {
			want := byte(0xEE)
			if y < 4 && x >= first[y] && x <= last[y] {
				want = cm[16+y][y*16+x]
			}
			if got := fb.Pixels[y*fb.Pitch+x]; got != want {
				t.Errorf("(%d,%d) = %#02x, want %#02x", x, y, got, want)
			}
		}
	}
	// reversed winding: d_xdenom >= 0, not drawn
	if PolysetDrawTriangle(fb, polySkin(16, 16), cm, v0, v2, v1, true, 0) {
		t.Error("the reversed triangle was drawn")
	}
}

// The seam fixup of a back-facing triangle (d_polyse.c line 306): a vertex on
// the seam has seamfixupX16 added to its s, so the skin is read half a skin
// further along; front-facing triangles are not touched.
func TestPolysetSeamFixup(t *testing.T) {
	v := func(x, y int32, seam bool) FinalVert {
		return FinalVert{V: [6]int32{x, y, x << 16, y << 16, 0x1000}, OnSeam: seam}
	}
	draw := func(front bool) byte {
		fb, _ := NewFrameBuffer(10, 6)
		PolysetDrawTriangle(fb, polySkin(16, 16), polyTestCM(), v(0, 0, true), v(8, 0, true), v(4, 4, true), front, 8<<16)
		return fb.Pixels[0] // pixel (0, 0): s = 0 (+ 8 when fixed up), t = 0
	}
	cm := polyTestCM()
	if got, want := draw(true), cm[16][0]; got != want {
		t.Errorf("front: %#02x, want %#02x", got, want)
	}
	if got, want := draw(false), cm[16][8]; got != want {
		t.Errorf("back: %#02x, want %#02x", got, want)
	}
}

// D_PolysetRecursiveTriangle worked by hand for (0,0), (2,0), (0,2): the only
// leading-edge midpoints are (1,0) and (0,1); the vertices themselves are
// plotted by D_PolysetDrawFinalVerts, with their own light and the bottom and
// right edges left out.
func TestPolysetSubdivTriangleAndFinalVerts(t *testing.T) {
	v := func(x, y int32, l int32) FinalVert { return FinalVert{V: [6]int32{x, y, 0, 0, l}} }
	cm := polyTestCM()
	fb, _ := NewFrameBuffer(4, 4)
	for i := range fb.Pixels {
		fb.Pixels[i] = 0xEE
	}
	if !PolysetDrawSubdivTriangle(fb, polySkin(2, 2), cm, v(0, 0, 0x0500), v(2, 0, 0x0900), v(0, 2, 0x0900), true, 0) {
		t.Fatal("not drawn")
	}
	for y := 0; y < 4; y++ {
		for x := 0; x < 4; x++ {
			want := byte(0xEE)
			if (x == 1 && y == 0) || (x == 0 && y == 1) {
				want = cm[5][0] // the row of vertex 0's light
			}
			if got := fb.Pixels[y*fb.Pitch+x]; got != want {
				t.Errorf("(%d,%d) = %#02x, want %#02x", x, y, got, want)
			}
		}
	}
	if PolysetDrawSubdivTriangle(fb, polySkin(2, 2), cm, v(0, 0, 0), v(0, 2, 0), v(2, 0, 0), true, 0) {
		t.Error("a back-facing triangle was drawn")
	}
	// back-facing with a seam vertex: fixed up, drawn
	if !PolysetDrawSubdivTriangle(fb, polySkin(2, 2), cm, FinalVert{V: [6]int32{0, 0, 0, 0, 0x0500}, OnSeam: true}, v(2, 0, 0), v(0, 2, 0), false, 1<<16) {
		t.Error("the back-facing seam triangle was not drawn")
	}
	fb2, _ := NewFrameBuffer(3, 3)
	for i := range fb2.Pixels {
		fb2.Pixels[i] = 0xEE
	}
	PolysetDrawFinalVerts(fb2, polySkin(2, 2), cm, []FinalVert{v(0, 0, 0x0500), v(2, 2, 0x0900), v(3, 0, 0x0900), v(0, 3, 0x0900), v(1, 1, 0x0100)})
	for _, c := range []struct {
		x, y int
		want byte
	}{{0, 0, cm[5][0]}, {2, 2, cm[9][0]}, {1, 1, cm[1][0]}, {1, 0, 0xEE}} {
		if got := fb2.Pixels[c.y*fb2.Pitch+c.x]; got != c.want {
			t.Errorf("final verts (%d,%d) = %#02x, want %#02x", c.x, c.y, got, c.want)
		}
	}
}

// ceilDiv is the ceiling of a / b for b > 0.
func ceilDiv(a, b int32) int32 {
	q, r := floorDivMod(a, b)
	if r != 0 {
		q++
	}
	return q
}

// closedForm is what the walk produces, derived instead of stepped: down a
// segment from (x0, y0) to (x1, y1) the column after j rows is
// x0 + ceil(j (x1 - x0) / (y1 - y0)) (the error term of D_PolysetScanLeftEdge
// is the remainder of that division), and the skin position and light advance
// by the row step plus the column step times the columns moved. Pixels map to
// the skin offset and the light the colormap is read with.
type cfPixel struct{ ptex, light int32 }

func closedForm(p *[3][6]int32, xdenom, w int32) map[[2]int32]cfPixel {
	et := &polyEdgeTables[PolysetEdgeTableIndex(p[0], p[1], p[2])]
	g := polyCalcGradients(&p[0], &p[1], &p[2], xdenom, w)
	column := func(top, bot *[6]int32, j int32) int32 {
		return top[0] + ceilDiv(j*(bot[0]-top[0]), bot[1]-top[1])
	}
	xr := func(i int32) int32 { // right edge column at row i, relative to the top
		top, bot := &p[et.right[0]], &p[et.right[1]]
		if h := bot[1] - top[1]; i >= h && et.numRight == 2 {
			top2, bot2 := bot, &p[et.right[2]]
			return column(top2, bot2, i-h) // starts at the vertex: its column
		}
		return column(top, bot, i)
	}
	out := map[[2]int32]cfPixel{}
	rows := int32(0)
	for si := 0; si < et.numLeft; si++ {
		top, bot := &p[et.left[si]], &p[et.left[si+1]]
		h := bot[1] - top[1]
		s0, t0 := top[2], top[3]
		if si == 1 { // the second segment drops the vertex's fractions
			s0, t0 = (s0>>16)<<16, (t0>>16)<<16
		}
		work := g.lstepx
		q, _ := floorDivMod(bot[0]-top[0], h)
		if q < 0 {
			work--
		}
		for j := int32(0); j < h; j++ {
			x := column(top, bot, j)
			i := rows + j
			count := xr(i) - x
			ps := s0 + j*g.sstepy + (x-top[0])*g.sstepx
			pt := t0 + j*g.tstepy + (x-top[0])*g.tstepx
			l := top[4] + j*g.lstepy + work*(x-top[0])
			for k := int32(0); k < count; k++ {
				out[[2]int32{x + k, top[1] + j}] = cfPixel{
					ptex:  (ps+k*g.sstepx)>>16 + ((pt+k*g.tstepx)>>16)*w,
					light: l + k*g.lstepx,
				}
			}
		}
		rows += h
	}
	return out
}

// The stepped walk and the closed form agree on every pixel of many random
// triangles (screen 64 x 64, skin 32 wide, s, t and light random): the
// coverage, the skin offset and the light of each pixel.
func TestPolysetWalkIsItsClosedForm(t *testing.T) {
	rng := rand.New(rand.NewSource(1))
	cm := polyTestCM()
	skin := &Pic{Width: 32, Height: 32, Pixels: make([]byte, 32*32)}
	drawn, pixels := 0, 0
	for n := 0; n < 3000; n++ {
		var vs [3]FinalVert
		for i := range vs {
			vs[i].V = [6]int32{rng.Int31n(64), rng.Int31n(64), rng.Int31n(28 << 16), rng.Int31n(28 << 16), 0x1000 + rng.Int31n(0x2000)}
		}
		p := [3][6]int32{vs[0].V, vs[1].V, vs[2].V}
		xdenom := (p[0][1]-p[1][1])*(p[0][0]-p[2][0]) - (p[0][0]-p[1][0])*(p[0][1]-p[2][1])
		if xdenom >= 0 {
			continue
		}
		got := map[[2]int32]cfPixel{}
		w := &polyWalk{fb: &FrameBuffer{Width: 64, Height: 64, Pitch: 64}, skin: skin, cm: cm, skinwidth: 32,
			onPlot: func(x, y, ptex, light int32) {
				if _, dup := got[[2]int32{x, y}]; dup {
					t.Fatalf("pixel (%d,%d) reached twice", x, y)
				}
				got[[2]int32{x, y}] = cfPixel{ptex, light}
			}}
		w.rasterize(&p, xdenom)
		want := closedForm(&p, xdenom, 32)
		if len(got) != len(want) {
			t.Fatalf("triangle %v: %d pixels walked, %d in the closed form", p, len(got), len(want))
		}
		for k, wp := range want {
			g, ok := got[k]
			if !ok || g.ptex != wp.ptex || g.light != wp.light {
				t.Fatalf("triangle %v pixel %v: walked %+v (%v), closed form %+v", p, k, g, ok, wp)
			}
		}
		drawn++
		pixels += len(want)
	}
	if drawn < 1000 || pixels < 50000 {
		t.Fatalf("only %d triangles, %d pixels compared", drawn, pixels)
	}
}

// Plotting is clipped to the frame and the skin: nothing is written off the
// frame, and a skin offset outside the skin reads its nearest texel.
func TestPolysetPlotClips(t *testing.T) {
	fb, _ := NewFrameBuffer(4, 4)
	cm := polyTestCM()
	w := &polyWalk{fb: fb, skin: polySkin(2, 2), cm: cm, skinwidth: 2}
	w.plot(-1, 0, 0, 0)
	w.plot(0, -1, 0, 0)
	w.plot(4, 0, 0, 0)
	w.plot(0, 4, 0, 0)
	for _, p := range fb.Pixels {
		if p != 0 {
			t.Fatal("a pixel off the frame was written")
		}
	}
	w.plot(0, 0, -5, 0x0100) // before the skin: texel 0
	w.plot(1, 0, 99, 0x0100) // past the skin: the last texel (3)
	if fb.Pixels[0] != cm[1][0] || fb.Pixels[1] != cm[1][3] {
		t.Errorf("clamped texels: %#02x %#02x, want %#02x %#02x", fb.Pixels[0], fb.Pixels[1], cm[1][0], cm[1][3])
	}
}

// The recursive drawer takes every split case and terminates on random
// triangles, and plots only inside a frame it was given.
func TestPolysetRecursiveRandom(t *testing.T) {
	rng := rand.New(rand.NewSource(2))
	fb, _ := NewFrameBuffer(48, 48)
	cm := polyTestCM()
	skin := polySkin(8, 8)
	drawn := 0
	for n := 0; n < 400; n++ {
		var v [3]FinalVert
		for i := range v {
			v[i].V = [6]int32{rng.Int31n(48), rng.Int31n(48), rng.Int31n(7 << 16), rng.Int31n(7 << 16), 0x1000 + rng.Int31n(0x1000)}
		}
		if PolysetDrawSubdivTriangle(fb, skin, cm, v[0], v[1], v[2], rng.Intn(2) == 0, 4<<16) {
			drawn++
		}
	}
	if drawn < 100 {
		t.Fatalf("only %d triangles drawn", drawn)
	}
}

// A span wholly inside the frame takes the fast loop; it must write what the
// per-pixel plot writes (the skin offset kept inside the skin, the colormap row
// clamped), on triangles whose skin positions and lights run off both ends.
func TestPolysetFastSpanMatchesPlot(t *testing.T) {
	rng := rand.New(rand.NewSource(3))
	cm := polyTestCM()
	skin := polySkin(8, 8)
	for n := 0; n < 300; n++ {
		var vs [3]FinalVert
		for i := range vs {
			// screen positions run off every side of the 64 x 64 frame
			vs[i].V = [6]int32{rng.Int31n(112) - 24, rng.Int31n(112) - 24, rng.Int31n(40<<16) - 8<<16, rng.Int31n(40<<16) - 8<<16, rng.Int31n(0x7000) - 0x800}
		}
		fast, _ := NewFrameBuffer(64, 64)
		slow, _ := NewFrameBuffer(64, 64)
		if !PolysetDrawTriangle(fast, skin, cm, vs[0], vs[1], vs[2], true, 0) {
			continue
		}
		// the same triangle through the per-pixel plot (a hook that writes as plot does)
		p := [3][6]int32{vs[0].V, vs[1].V, vs[2].V}
		xdenom := (p[0][1]-p[1][1])*(p[0][0]-p[2][0]) - (p[0][0]-p[1][0])*(p[0][1]-p[2][1])
		w := &polyWalk{fb: slow, skin: skin, cm: cm, skinwidth: 8}
		w.onPlot = func(x, y, ptex, light int32) {
			w2 := &polyWalk{fb: slow, skin: skin, cm: cm, skinwidth: 8}
			w2.plot(x, y, ptex, light)
		}
		w.rasterize(&p, xdenom)
		for i := range fast.Pixels {
			if fast.Pixels[i] != slow.Pixels[i] {
				t.Fatalf("triangle %v: pixel %d: fast %#02x, plot %#02x", p, i, fast.Pixels[i], slow.Pixels[i])
			}
		}
	}
	// an empty skin draws nothing
	fb, _ := NewFrameBuffer(8, 8)
	v := FinalVert{V: [6]int32{1, 1}}
	if PolysetDrawTriangle(fb, &Pic{}, cm, v, FinalVert{V: [6]int32{5, 1}}, FinalVert{V: [6]int32{1, 5}}, true, 0) {
		t.Error("a triangle was drawn with an empty skin")
	}
}
