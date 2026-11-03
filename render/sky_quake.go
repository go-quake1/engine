// Copyright (c) 1996-1997 Id Software, Inc.
// Copyright (c) 2026 the go-quake1/engine authors.
// SPDX-License-Identifier: GPL-2.0-or-later

package render

import "math"

// This file ports Quake's software sky as tyrquake has it: R_InitSky,
// R_MakeSky and R_SetSkyFrame (r_sky.c) and D_Sky_uv_To_st and
// D_DrawSkyScans8 (d_sky.c). The sky is a dome: the texture coordinates of a
// pixel come from its view direction, not from the face's texture axes, so it
// does not depend on the polygon that carries it. As for water, only the
// choice of covered pixels stays the engine's (a span starts at the first
// covered pixel of a scanline run).
//
// A sky texture is 256 x 128: the left half is the masked layer (index 0 is
// clear), the right half the solid layer. R_MakeSky builds a 128 x 128 table
// each time the integer shift changes: the masked layer shifted by
// (skytime * skyspeed) in both directions where it is not clear, the solid
// layer, which does not move, elsewhere. The lookup then adds the same offset
// to the direction-derived coordinates.

// Constants of d_iface.h, r_sky.c and d_local.h.
const (
	SkySize    = 128 // SKYSIZE
	SkyMask    = SkySize - 1
	SkySpeed   = 8 // iskyspeed
	SkySpeed2  = 2 // iskyspeed2
	skySpanLen = 32
)

// SkyFrameTime is R_SetSkyFrame's skytime: cl.time modulo
// SKYSIZE * (iskyspeed / g) * (iskyspeed2 / g), g their greatest common
// divisor (here 128 * 4 * 1 = 512).
func SkyFrameTime(clTime float64) float64 {
	const period = SkySize * (SkySpeed / 2) * (SkySpeed2 / 2) // g = 2
	return clTime - float64(int(clTime/period))*period
}

// QuakeSky is a sky texture split as R_InitSky splits it.
type QuakeSky struct {
	front [SkySize * SkySize]byte // the masked layer, left half
	back  [SkySize * SkySize]byte // the solid layer, right half
}

// NewQuakeSky splits a 256 x 128 sky texture. It reports false for any
// other size.
func NewQuakeSky(tex *Pic) (*QuakeSky, bool) {
	if tex == nil || tex.Width != 2*SkySize || tex.Height != SkySize || len(tex.Pixels) != tex.Width*tex.Height {
		return nil, false
	}
	q := &QuakeSky{}
	for y := 0; y < SkySize; y++ {
		copy(q.front[y*SkySize:], tex.Pixels[y*tex.Width:y*tex.Width+SkySize])
		copy(q.back[y*SkySize:], tex.Pixels[y*tex.Width+SkySize:y*tex.Width+2*SkySize])
	}
	return q, true
}

// Table is R_MakeSky: the composed 128 x 128 sky for skytime (see
// [SkyFrameTime]).
func (q *QuakeSky) Table(skytime float64) *[SkySize * SkySize]byte {
	shift := int(skytime * SkySpeed)
	var out [SkySize * SkySize]byte
	for y := 0; y < SkySize; y++ {
		for x := 0; x < SkySize; x++ {
			if f := q.front[((y+shift)&SkyMask)*SkySize+((x+shift)&SkyMask)]; f != 0 {
				out[y*SkySize+x] = f
			} else {
				out[y*SkySize+x] = q.back[y*SkySize+x]
			}
		}
	}
	return &out
}

// SkyST is D_Sky_uv_To_st: the fixed-point (16.16) sky coordinates of the
// screen pixel (u, v) of a screen of width x height, for a view whose rows
// are right, up and forward (vright, vup, vpn), at skytime. The vrect is the
// whole screen.
func SkyST(view Affine, width, height int, skytime float64, u, v int) (s, t int32) {
	temp := float32(width)
	if width < height {
		temp = float32(height)
	}
	wu := 8192.0 * float32(u-(width>>1)) / temp
	wv := 8192.0 * float32((height>>1)-v) / temp
	var end [3]float32
	for i := 0; i < 3; i++ {
		end[i] = 4096*view.R[2][i] + wu*view.R[0][i] + wv*view.R[1][i]
	}
	end[2] *= 3
	l := float32(math.Sqrt(float64(end[0]*end[0] + end[1]*end[1] + end[2]*end[2])))
	for i := range end {
		end[i] /= l
	}
	off := float32(skytime * SkySpeed)
	s = int32((off + 6*(SkySize/2-1)*end[0]) * 0x10000)
	t = int32((off + 6*(SkySize/2-1)*end[1]) * 0x10000)
	return
}

// SkySpan is D_DrawSkyScans8 for one span: count pixels starting at pixel
// (u, v), in sub-spans of 32 with s and t exact at their ends and stepped
// linearly (a shift by 5 for full sub-spans, a division for the last).
func SkySpan(dst []byte, table *[SkySize * SkySize]byte, view Affine, width, height int, skytime float64, u, v, count int) {
	s, t := SkyST(view, width, height, skytime, u, v)
	var snext, tnext, sstep, tstep int32
	pos := 0
	for count > 0 {
		sc := count
		if sc > skySpanLen {
			sc = skySpanLen
		}
		count -= sc
		if count > 0 {
			u += sc
			snext, tnext = SkyST(view, width, height, skytime, u, v)
			sstep, tstep = (snext-s)>>5, (tnext-t)>>5
		} else if m1 := sc - 1; m1 > 0 {
			u += m1
			snext, tnext = SkyST(view, width, height, skytime, u, v)
			sstep, tstep = (snext-s)/int32(m1), (tnext-t)/int32(m1)
		}
		for i := 0; i < sc; i++ {
			dst[pos] = table[((int(t)>>16)&SkyMask)*SkySize+((int(s)>>16)&SkyMask)]
			pos++
			s += sstep
			t += tstep
		}
		s, t = snext, tnext
	}
}

// FillSkyQuake paints the convex polygon verts (screen positions only) with
// the dome sky, one [SkySpan] per scanline run of covered pixels; the
// coverage is the engine's, as in [FillTurbulentQuake].
func FillSkyQuake(fb *FrameBuffer, table *[SkySize * SkySize]byte, view Affine, skytime float64, verts []TexturedVertex) error {
	if fb == nil {
		return ErrSkyFillNilFB
	}
	if len(verts) < 3 {
		return ErrSkyFillFewVerts
	}
	if len(verts) > MaxPolyVerts {
		return ErrSkyFillManyVerts
	}
	yMin, yMax := verts[0].Y, verts[0].Y
	for _, v := range verts[1:] {
		yMin, yMax = min(yMin, v.Y), max(yMax, v.Y)
	}
	yStart, yEnd := max(int(math.Floor(float64(yMin))), 0), min(int(math.Ceil(float64(yMax))), fb.Height)
	for y := yStart; y < yEnd; y++ {
		yf := float32(y) + 0.5
		var xs [MaxPolyVerts]float32
		n := 0
		for i := range verts {
			a, b := verts[i], verts[(i+1)%len(verts)]
			if (a.Y <= yf && b.Y > yf) || (b.Y <= yf && a.Y > yf) {
				xs[n] = a.X + (yf-a.Y)/(b.Y-a.Y)*(b.X-a.X)
				n++
			}
		}
		for i := 1; i < n; i++ {
			for j := i; j > 0 && xs[j-1] > xs[j]; j-- {
				xs[j-1], xs[j] = xs[j], xs[j-1]
			}
		}
		for p := 0; p+1 < n; p += 2 {
			x0, x1 := max(int(math.Ceil(float64(xs[p]))), 0), min(int(math.Floor(float64(xs[p+1]))), fb.Width-1)
			if x0 > x1 {
				continue
			}
			SkySpan(fb.Pixels[y*fb.Pitch+x0:], table, view, fb.Width, fb.Height, skytime, x0, y, x1-x0+1)
		}
	}
	return nil
}

// SkyCache keeps the split sky textures and the composed table of the
// current shift, so that a frame builds R_MakeSky only when the integer shift
// moves (as Quake's xlast / ylast test does). The zero value is ready.
type SkyCache struct {
	m map[*Pic]*skyEntry
}

type skyEntry struct {
	q     *QuakeSky
	tab   *[SkySize * SkySize]byte
	shift int
}

// Table returns the composed sky of tex at cl.time clTime, or nil when tex is
// not a 256 x 128 sky texture.
func (c *SkyCache) Table(tex *Pic, clTime float64) *[SkySize * SkySize]byte {
	if c.m == nil {
		c.m = map[*Pic]*skyEntry{}
	}
	e, ok := c.m[tex]
	if !ok {
		e = &skyEntry{}
		e.q, _ = NewQuakeSky(tex)
		c.m[tex] = e
	}
	if e.q == nil {
		return nil
	}
	st := SkyFrameTime(clTime)
	if shift := int(st * SkySpeed); e.tab == nil || shift != e.shift {
		e.tab, e.shift = e.q.Table(st), shift
	}
	return e.tab
}
