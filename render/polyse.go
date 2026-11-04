// Copyright (c) 1996-1997 Id Software, Inc.
// Copyright (c) 2026 the go-quake1/engine authors.
// SPDX-License-Identifier: GPL-2.0-or-later

package render

import (
	"math"
	"sync"
)

// This file ports Quake's software alias-model rasteriser as tyrquake has it
// (d_polyse.c): the fixed-point edge walk of D_DrawNonSubdiv that every alias
// triangle takes when the model is near, D_PolysetSetEdgeTable,
// D_RasterizeAliasPolySmooth, D_PolysetSetUpForLineScan,
// D_PolysetCalcGradients, D_PolysetScanLeftEdge and D_PolysetDrawSpans8; and
// the recursive subdivision (D_DrawSubdiv, D_PolysetRecursiveTriangle,
// D_PolysetDrawFinalVerts) that distant models take.
//
// What is the engine's own, and not Quake's: there is no z buffer (the engine
// draws entities after the world with no depth test), so the span drawers'
// z test is always true and zi is not carried; pixels are clipped to the frame
// rather than relying on r_aclip.c's clipping of partly visible triangles; the
// colormap row of a pixel is clamped to the colormap.

// FinalVert is finalvert_t: V holds u and v (screen pixels), s and t (the skin
// coordinates in 16.16 fixed point), the light (a colormap row times 256) and
// zi (not used here: no z buffer); OnSeam is ALIAS_ONSEAM.
type FinalVert struct {
	V      [6]int32
	OnSeam bool
}

// polyEdgeTable is edgetable: which vertices (indices into r_p0, r_p1, r_p2,
// -1 for none) bound the left and the right side, top part first.
type polyEdgeTable struct {
	flatTop  bool
	numLeft  int
	left     [3]int
	numRight int
	right    [3]int
}

var polyEdgeTables = [12]polyEdgeTable{
	{false, 1, [3]int{0, 2, -1}, 2, [3]int{0, 1, 2}},
	{false, 2, [3]int{1, 0, 2}, 1, [3]int{1, 2, -1}},
	{true, 1, [3]int{0, 2, -1}, 1, [3]int{1, 2, -1}},
	{false, 1, [3]int{1, 0, -1}, 2, [3]int{1, 2, 0}},
	{false, 2, [3]int{0, 2, 1}, 1, [3]int{0, 1, -1}},
	{false, 1, [3]int{2, 1, -1}, 1, [3]int{2, 0, -1}},
	{false, 1, [3]int{2, 1, -1}, 2, [3]int{2, 0, 1}},
	{false, 2, [3]int{2, 1, 0}, 1, [3]int{2, 0, -1}},
	{false, 1, [3]int{1, 0, -1}, 1, [3]int{1, 2, -1}},
	{true, 1, [3]int{2, 1, -1}, 1, [3]int{0, 1, -1}},
	{true, 1, [3]int{1, 0, -1}, 1, [3]int{2, 0, -1}},
	{false, 1, [3]int{0, 2, -1}, 1, [3]int{0, 1, -1}},
}

// PolysetEdgeTableIndex is D_PolysetSetEdgeTable: the index into the twelve
// edge tables for a triangle with the vertices p0, p1, p2 (only their v, the
// screen row, matters).
func PolysetEdgeTableIndex(p0, p1, p2 [6]int32) int {
	index := 0 // assume the vertices are already in top to bottom order
	if p0[1] >= p1[1] {
		if p0[1] == p1[1] {
			if p0[1] < p2[1] {
				return 2
			}
			return 5
		}
		index = 1
	}
	if p0[1] == p2[1] {
		if index != 0 {
			return 8
		}
		return 9
	} else if p1[1] == p2[1] {
		if index != 0 {
			return 10
		}
		return 11
	}
	if p0[1] > p2[1] {
		index += 2
	}
	if p1[1] > p2[1] {
		index += 4
	}
	return index
}

// floorDivMod is FloorDivMod and the adivtab table (adivtab.h): the floor
// quotient and the remainder of numer / denom; a zero denominator gives 0 0,
// as the table's entry does.
func floorDivMod(numer, denom int32) (q, r int32) {
	if denom == 0 {
		return 0, 0
	}
	q = numer / denom
	if (numer%denom != 0) && ((numer < 0) != (denom < 0)) {
		q--
	}
	return q, numer - q*denom
}

// polyEdge is the globals of D_PolysetSetUpForLineScan: errorterm,
// erroradjustup, erroradjustdown and ubasestep.
type polyEdge struct {
	errorterm, up, down, ubase int32
}

// setUpForLineScan is D_PolysetSetUpForLineScan.
func setUpForLineScan(startU, startV, endU, endV int32) polyEdge {
	tm := endU - startU
	tn := endV - startV
	q, r := floorDivMod(tm, tn)
	return polyEdge{errorterm: -1, ubase: q, up: r, down: tn}
}

// polyGradients are what D_PolysetCalcGradients leaves.
type polyGradients struct {
	lstepx, lstepy       int32
	sstepx, sstepy       int32
	tstepx, tstepy       int32
	sstepxfrac, tstepxfr int32 // a_sstepxfrac, a_tstepxfrac
	ststepxwhole         int32 // a_ststepxwhole
}

// polyCalcGradients is D_PolysetCalcGradients for the (already offset)
// vertices r0, r1, r2 and d_xdenom. The float32 products are rounded one by
// one (no fused multiply-add), as the C does on x86.
func polyCalcGradients(r0, r1, r2 *[6]int32, xdenom int32, skinwidth int32) polyGradients {
	p00 := float32(r0[0] - r2[0])
	p01 := float32(r0[1] - r2[1])
	p10 := float32(r1[0] - r2[0])
	p11 := float32(r1[1] - r2[1])
	xinv := float32(1.0 / float64(float32(xdenom)))
	yinv := -xinv
	var g polyGradients
	step := func(i int, ceil bool) (x, y int32) {
		t0 := float32(r0[i] - r2[i])
		t1 := float32(r1[i] - r2[i])
		fx := float32(float32(t1*p01)-float32(t0*p11)) * xinv
		fy := float32(float32(t1*p00)-float32(t0*p10)) * yinv
		if ceil {
			// ceil () for light so positive steps are exaggerated, negative
			// steps diminished
			return int32(math.Ceil(float64(fx))), int32(math.Ceil(float64(fy)))
		}
		return int32(fx), int32(fy)
	}
	g.lstepx, g.lstepy = step(4, true)
	g.sstepx, g.sstepy = step(2, false)
	g.tstepx, g.tstepy = step(3, false)
	g.sstepxfrac = g.sstepx & 0xFFFF
	g.tstepxfr = g.tstepx & 0xFFFF
	g.ststepxwhole = skinwidth*(g.tstepx>>16) + (g.sstepx >> 16)
	return g
}

// polySpan is spanpackage_t: where a row's span starts (x, y), how far the
// walk had got (count), the skin offset and fractions, and the light.
type polySpan struct {
	x, y         int32
	count        int32
	ptex         int32
	sfrac, tfrac int32
	light        int32
}

// polyWalk is the state of one D_RasterizeAliasPolySmooth.
type polyWalk struct {
	fb        *FrameBuffer
	skin      *Pic
	cm        *ColorMap
	g         polyGradients
	skinwidth int32
	spans     []polySpan
	e         polyEdge                      // the errorterm globals
	onPlot    func(x, y, ptex, light int32) // tests: sees every pixel the walk reaches, unclipped

	// the d_* globals of the left scan
	x, y                           int32
	aspancount                     int32
	ptex                           int32
	sfrac, tfrac, light            int32
	ptexextra, sfracextra          int32
	tfracextra, lightextra         int32
	ptexbase, sfracbase, tfracbase int32
	lightbase                      int32
	countextrastep                 int32
}

// scanLeftEdge is D_PolysetScanLeftEdge.
func (w *polyWalk) scanLeftEdge(height int32) {
	for ; height > 0; height-- {
		w.spans = append(w.spans, polySpan{x: w.x, y: w.y, count: w.aspancount, ptex: w.ptex, sfrac: w.sfrac, tfrac: w.tfrac, light: w.light})
		w.e.errorterm += w.e.up
		if w.e.errorterm >= 0 {
			w.x += w.e.ubase + 1
			w.y++
			w.aspancount += w.countextrastep
			w.ptex += w.ptexextra
			w.sfrac += w.sfracextra
			w.ptex += w.sfrac >> 16
			w.sfrac &= 0xFFFF
			w.tfrac += w.tfracextra
			if w.tfrac&0x10000 != 0 {
				w.ptex += w.skinwidth
				w.tfrac &= 0xFFFF
			}
			w.light += w.lightextra
			w.e.errorterm -= w.e.down
		} else {
			w.x += w.e.ubase
			w.y++
			w.aspancount += w.e.ubase
			w.ptex += w.ptexbase
			w.sfrac += w.sfracbase
			w.ptex += w.sfrac >> 16
			w.sfrac &= 0xFFFF
			w.tfrac += w.tfracbase
			if w.tfrac&0x10000 != 0 {
				w.ptex += w.skinwidth
				w.tfrac &= 0xFFFF
			}
			w.light += w.lightbase
		}
	}
}

// setUpLeft computes the per-segment steps of D_RasterizeAliasPolySmooth for
// the left edge segment just set up in w.e.
func (w *polyWalk) setUpLeft() {
	g := &w.g
	workingLstepx := g.lstepx
	if w.e.ubase < 0 {
		workingLstepx-- // for negative steps in x along the left edge, bias toward overflow
	}
	w.countextrastep = w.e.ubase + 1
	w.ptexbase = ((g.sstepy + g.sstepx*w.e.ubase) >> 16) + ((g.tstepy+g.tstepx*w.e.ubase)>>16)*w.skinwidth
	w.sfracbase = (g.sstepy + g.sstepx*w.e.ubase) & 0xFFFF
	w.tfracbase = (g.tstepy + g.tstepx*w.e.ubase) & 0xFFFF
	w.lightbase = g.lstepy + workingLstepx*w.e.ubase
	w.ptexextra = ((g.sstepy + g.sstepx*w.countextrastep) >> 16) + ((g.tstepy+g.tstepx*w.countextrastep)>>16)*w.skinwidth
	w.sfracextra = (g.sstepy + g.sstepx*w.countextrastep) & 0xFFFF
	w.tfracextra = (g.tstepy + g.tstepx*w.countextrastep) & 0xFFFF
	w.lightextra = w.lightbase + workingLstepx
}

// drawSpans is D_PolysetDrawSpans8 from spans[from], up to the package whose
// count is -999999 (the end marker); there is no z test.
func (w *polyWalk) drawSpans(from int) {
	g := &w.g
	for i := from; ; i++ {
		if i > from && w.spans[i].count == -999999 {
			break // do { ... } while (pspanpackage->count != -999999)
		}
		p := &w.spans[i]
		lcount := w.aspancount - p.count
		w.e.errorterm += w.e.up
		if w.e.errorterm >= 0 {
			w.aspancount += w.countextrastep
			w.e.errorterm -= w.e.down
		} else {
			w.aspancount += w.e.ubase
		}
		if lcount > 0 && w.onPlot == nil && p.y >= 0 && int(p.y) < w.fb.Height && p.x >= 0 && int(p.x)+int(lcount) <= w.fb.Width {
			w.fastSpan(p, lcount) // wholly inside the frame: no per-pixel bounds
		} else if lcount != 0 {
			ptex, sfrac, tfrac, light := p.ptex, p.sfrac, p.tfrac, p.light
			x := p.x
			for ; lcount > 0; lcount-- {
				w.plot(x, p.y, ptex, light)
				x++
				light += g.lstepx
				ptex += g.ststepxwhole
				sfrac += g.sstepxfrac
				ptex += sfrac >> 16
				sfrac &= 0xFFFF
				tfrac += g.tstepxfr
				if tfrac&0x10000 != 0 {
					ptex += w.skinwidth
					tfrac &= 0xFFFF
				}
			}
		}
	}
}

// fastSpan is the inner loop of D_PolysetDrawSpans8 for a span that lies
// inside the frame (the skin offset is still kept inside the skin).
func (w *polyWalk) fastSpan(p *polySpan, n int32) {
	g := &w.g
	dst := w.fb.Pixels[int(p.y)*w.fb.Pitch+int(p.x):][:n]
	skin := w.skin.Pixels
	last := int32(len(skin) - 1)
	cm := w.cm
	ptex, sfrac, tfrac, light := p.ptex, p.sfrac, p.tfrac, p.light
	for i := range dst {
		t := ptex
		if uint32(t) > uint32(last) {
			t = max(0, min(t, last))
		}
		row := (light & 0xFF00) >> 8
		if row > ColorMapRows-1 {
			row = ColorMapRows - 1
		}
		dst[i] = cm[row][skin[t]]
		light += g.lstepx
		ptex += g.ststepxwhole
		sfrac += g.sstepxfrac
		ptex += sfrac >> 16
		sfrac &= 0xFFFF
		tfrac += g.tstepxfr
		if tfrac&0x10000 != 0 {
			ptex += w.skinwidth
			tfrac &= 0xFFFF
		}
	}
}

// plot is *lpdest = acolormap[*lptex + (llight & 0xFF00)] with the frame's
// bounds and the skin's applied; the row is clamped to the colormap.
func (w *polyWalk) plot(x, y, ptex, light int32) {
	if w.onPlot != nil {
		w.onPlot(x, y, ptex, light)
		return
	}
	if x < 0 || y < 0 || int(x) >= w.fb.Width || int(y) >= w.fb.Height {
		return
	}
	pix := w.skin.Pixels
	if ptex < 0 {
		ptex = 0
	} else if int(ptex) >= len(pix) {
		ptex = int32(len(pix) - 1)
	}
	w.fb.Pixels[int(y)*w.fb.Pitch+int(x)] = w.cm.LightIndex(int((light&0xFF00)>>8), pix[ptex])
}

// PolysetDrawTriangle is D_DrawNonSubdiv for one triangle: the fixed-point
// edge walk of D_RasterizeAliasPolySmooth over the vertices v0, v1, v2 of an
// entity whose skin is skin. facesFront is mtriangle_t.facesfront; a vertex on
// the seam of a back-facing triangle has seamFixup (skinwidth / 2 in 16.16)
// added to its s. It reports whether the triangle was drawn (a back-facing one
// is not).
func PolysetDrawTriangle(fb *FrameBuffer, skin *Pic, cm *ColorMap, v0, v1, v2 FinalVert, facesFront bool, seamFixup int32) bool {
	xdenom := (v0.V[1]-v1.V[1])*(v0.V[0]-v2.V[0]) - (v0.V[0]-v1.V[0])*(v0.V[1]-v2.V[1])
	if xdenom >= 0 {
		return false
	}
	p := [3][6]int32{v0.V, v1.V, v2.V}
	if !facesFront {
		for i, v := range [3]FinalVert{v0, v1, v2} {
			if v.OnSeam {
				p[i][2] += seamFixup
			}
		}
	}
	if len(skin.Pixels) == 0 {
		return false
	}
	w := polyPool.Get().(*polyWalk)
	*w = polyWalk{fb: fb, skin: skin, cm: cm, skinwidth: int32(skin.Width), spans: w.spans[:0]}
	w.rasterize(&p, xdenom)
	polyPool.Put(w)
	return true
}

// polyPool keeps the walk state (and its span list) between triangles.
var polyPool = sync.Pool{New: func() any { return &polyWalk{spans: make([]polySpan, 0, 256)} }}

// rasterize is D_RasterizeAliasPolySmooth with D_PolysetSetEdgeTable done.
func (w *polyWalk) rasterize(p *[3][6]int32, xdenom int32) {
	et := &polyEdgeTables[PolysetEdgeTableIndex(p[0], p[1], p[2])]
	plefttop, prighttop := &p[et.left[0]], &p[et.right[0]]
	pleftbottom, prightbottom := &p[et.left[1]], &p[et.right[1]]
	initialleftheight := pleftbottom[1] - plefttop[1]
	initialrightheight := prightbottom[1] - prighttop[1]

	w.g = polyCalcGradients(&p[0], &p[1], &p[2], xdenom, w.skinwidth)

	// scan out the top (and possibly only) part of the left edge
	w.e = setUpForLineScan(plefttop[0], plefttop[1], pleftbottom[0], pleftbottom[1])
	w.spans = w.spans[:0]
	w.y = plefttop[1]
	w.x = plefttop[0]
	w.aspancount = plefttop[0] - prighttop[0]
	w.ptex = (plefttop[2] >> 16) + (plefttop[3]>>16)*w.skinwidth
	w.sfrac = plefttop[2] & 0xFFFF
	w.tfrac = plefttop[3] & 0xFFFF
	w.light = plefttop[4]
	w.setUpLeft()
	w.scanLeftEdge(initialleftheight)

	// scan out the bottom part of the left edge, if it exists
	if et.numLeft == 2 {
		plefttop = pleftbottom
		pleftbottom = &p[et.left[2]]
		w.e = setUpForLineScan(plefttop[0], plefttop[1], pleftbottom[0], pleftbottom[1])
		height := pleftbottom[1] - plefttop[1]
		w.y = plefttop[1]
		w.x = plefttop[0]
		w.aspancount = plefttop[0] - prighttop[0]
		w.ptex = (plefttop[2] >> 16) + (plefttop[3]>>16)*w.skinwidth
		w.sfrac = 0 // sic: the fractions of the vertex are dropped here
		w.tfrac = 0
		w.light = plefttop[4]
		w.setUpLeft()
		w.scanLeftEdge(height)
	}

	// the end marker may sit one past the last row (the C array has room)
	w.spans = append(w.spans, polySpan{}, polySpan{})

	// scan out the top (and possibly only) part of the right edge, updating
	// the count field
	w.e = setUpForLineScan(prighttop[0], prighttop[1], prightbottom[0], prightbottom[1])
	w.aspancount = 0
	w.countextrastep = w.e.ubase + 1
	originalcount := w.spans[initialrightheight].count
	w.spans[initialrightheight].count = -999999 // mark end of the spanpackages
	w.drawSpans(0)

	// scan out the bottom part of the right edge, if it exists
	if et.numRight == 2 {
		pstart := int(initialrightheight)
		w.spans[pstart].count = originalcount
		w.aspancount = prightbottom[0] - prighttop[0]
		prighttop = prightbottom
		prightbottom = &p[et.right[2]]
		height := prightbottom[1] - prighttop[1]
		w.e = setUpForLineScan(prighttop[0], prighttop[1], prightbottom[0], prightbottom[1])
		w.countextrastep = w.e.ubase + 1
		w.spans[int(initialrightheight)+int(height)].count = -999999
		w.drawSpans(pstart)
	}
}

// polyRecursive is D_PolysetRecursiveTriangle: the triangle is split until
// every edge is at most a pixel long, and the midpoint of each leading edge
// split is plotted with the colormap row of the first vertex (d_pcolormap).
func (w *polyWalk) polyRecursive(lp1, lp2, lp3 *[6]int32, row int32) {
	far := func(a, b *[6]int32) bool {
		d := b[0] - a[0]
		if d < -1 || d > 1 {
			return true
		}
		d = b[1] - a[1]
		return d < -1 || d > 1
	}
	switch {
	case far(lp1, lp2):
		// split
	case far(lp2, lp3):
		lp1, lp2, lp3 = lp2, lp3, lp1 // split2
	case far(lp3, lp1):
		lp1, lp2, lp3 = lp3, lp1, lp2 // split3: temp = lp1; lp1 = lp3; lp3 = lp2; lp2 = temp
	default:
		return // entire tri is filled
	}
	var nw [6]int32
	nw[0] = (lp1[0] + lp2[0]) >> 1
	nw[1] = (lp1[1] + lp2[1]) >> 1
	nw[2] = (lp1[2] + lp2[2]) >> 1
	nw[3] = (lp1[3] + lp2[3]) >> 1
	nw[5] = (lp1[5] + lp2[5]) >> 1
	// draw the point if splitting a leading edge
	if !(lp2[1] > lp1[1]) && !(lp2[1] == lp1[1] && lp2[0] < lp1[0]) {
		w.plotPoint(nw[0], nw[1], nw[2], nw[3], row)
	}
	w.polyRecursive(lp3, lp1, &nw, row)
	w.polyRecursive(lp3, &nw, lp2, row)
}

// plotPoint is pix = acolormap[skintable[t >> 16][s >> 16] + row] stored at
// (x, y), with the frame's bounds and the skin's applied.
func (w *polyWalk) plotPoint(x, y, s, t, row int32) {
	w.plot(x, y, (t>>16)*w.skinwidth+(s>>16), row)
}

// PolysetDrawSubdivTriangle is the body of D_DrawSubdiv for one triangle:
// D_PolysetRecursiveTriangle with the colormap row of v0's light. It reports
// whether the triangle was drawn (a back-facing one is not).
func PolysetDrawSubdivTriangle(fb *FrameBuffer, skin *Pic, cm *ColorMap, v0, v1, v2 FinalVert, facesFront bool, seamFixup int32) bool {
	if (v0.V[1]-v1.V[1])*(v0.V[0]-v2.V[0])-(v0.V[0]-v1.V[0])*(v0.V[1]-v2.V[1]) >= 0 {
		return false
	}
	p := [3][6]int32{v0.V, v1.V, v2.V}
	if !facesFront {
		for i, v := range [3]FinalVert{v0, v1, v2} {
			if v.OnSeam {
				p[i][2] += seamFixup
			}
		}
	}
	w := &polyWalk{fb: fb, skin: skin, cm: cm, skinwidth: int32(skin.Width)}
	w.polyRecursive(&p[0], &p[1], &p[2], v0.V[4]&0xFF00)
	return true
}

// PolysetDrawFinalVerts is D_PolysetDrawFinalVerts: every vertex of the model
// is plotted as a pixel (the recursion plots only midpoints), with its own
// light; vertices at or beyond the right or bottom edge are skipped.
func PolysetDrawFinalVerts(fb *FrameBuffer, skin *Pic, cm *ColorMap, verts []FinalVert) {
	w := &polyWalk{fb: fb, skin: skin, cm: cm, skinwidth: int32(skin.Width)}
	for _, v := range verts {
		// valid triangle coordinates for filling can include the bottom and
		// right clip edges, due to the fill rule; these shouldn't be drawn
		if int(v.V[0]) < fb.Width && int(v.V[1]) < fb.Height {
			w.plotPoint(v.V[0], v.V[1], v.V[2], v.V[3], v.V[4]&0xFF00)
		}
	}
}
