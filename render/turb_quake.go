// Copyright (c) 1996-1997 Id Software, Inc.
// Copyright (c) 2026 the go-quake1/engine authors.
// SPDX-License-Identifier: GPL-2.0-or-later

package render

import "math"

// This file ports Quake's software turbulent (water, slime, lava,
// teleporter) span drawer as tyrquake has it: Turbulent8 and
// D_DrawTurbulent8Span (d_scan.c), the sintable of R_Init (r_main.c) and the
// gradients of D_CalcGradients (d_edge.c). What stays the engine's own is
// the choice of which pixels a polygon covers (the engine fills convex
// polygons by scanline; Quake walks edges and builds spans), so a span here
// starts at the first covered pixel of a scanline run exactly as in Turbulent8.
//
// What differs from [FillTurbulentPolygon], which is not the original:
//
//   - the coordinates are relative to the face's texturemins, in 16.16 fixed
//     point, and clamped to [0, bbextents] like Quake's;
//   - they are perspective-correct at the start of every 16-pixel sub-span
//     and stepped linearly between (not affine over the whole polygon);
//   - the warp table is sintable: 8 + 8 sin(2 pi i / 128) texels in 16.16,
//     indexed by the other coordinate's integer part modulo 128 plus
//     (int)(time * 20) modulo 128;
//   - the pixel is texture[((t + turb[s >> 16]) >> 16) & 63][((s + turb[t >> 16])
//     >> 16) & 63], no colormap: water is not lit.

// Constants of d_iface.h, r_local.h and r_shared.h.
const (
	TurbCycle     = 128 // TURB_CYCLE
	TurbTableSize = 2 * TurbCycle
	TurbSpeed     = 20          // TURB_SPEED
	turbSurfAmp   = 8 * 0x10000 // TURB_SURF_AMP
)

// TurbSubSpan is the length of a sub-span (Turbulent8: 16). It is a variable
// only so that an ablation can set it to 1 and make the reference exact per
// pixel; the step sizes use Quake's shift by 4 and are only right for 16.
var TurbSubSpan = 16

// QuakeTurbTable is sintable (r_main.c): TURB_SURF_AMP + sin(i * 3.14159 * 2 /
// TURB_CYCLE) * TURB_SURF_AMP for i in [0, TURB_TABLE_SIZE), truncated to int.
// The constant pi is 3.14159, as in the source.
var QuakeTurbTable = func() [TurbTableSize]int32 {
	var t [TurbTableSize]int32
	for i := range t {
		t[i] = int32(float64(turbSurfAmp) + math.Sin(float64(i)*3.14159*2/TurbCycle)*float64(turbSurfAmp))
	}
	return t
}()

// SurfaceGradients is what D_CalcGradients computes for a surface, at mip
// level 0: the screen-space gradients of s/z, t/z and 1/z, and the extents
// that bound s and t. A pixel is addressed by its integer position (du, dv),
// as Quake does; the gradients are evaluated at the pixel's centre.
type SurfaceGradients struct {
	SdivzOrigin, SdivzStepU, SdivzStepV float32
	TdivzOrigin, TdivzStepU, TdivzStepV float32
	ZiOrigin, ZiStepU, ZiStepV          float32
	BBExtentS, BBExtentT                int32
}

// SurfacePlane describes a planar face: its plane (n . p = dist), its texture
// axes and offsets (texinfo vecs), its texturemins, and its extents in
// texture units (a multiple of 16).
type SurfacePlane struct {
	Normal       [3]float32
	Dist         float32
	AxisS, AxisT [3]float32
	OffS, OffT   float32
	MinS, MinT   float32
	ExtS, ExtT   int
}

// NewSurfaceGradients is D_CalcGradients for a view of a width x height
// frame with horizontal field of view fovX (degrees). The setup is done in
// float64 and rounded to float32; s and t are relative to the surface's
// texturemins (Quake's sadjust), without the 0.5 fixed-point rounding of
// the original.
func NewSurfaceGradients(view Affine, fovX float32, width, height int, p SurfacePlane) (SurfaceGradients, bool) {
	tanHalf := math.Tan(float64(fovX) / 2 * math.Pi / 180)
	if tanHalf <= 0 {
		return SurfaceGradients{}, false
	}
	halfW, halfH := float64(width)/2, float64(height)/2
	scale := halfW / tanHalf
	// Viewer origin: view maps p to R p + T, R orthonormal.
	var o [3]float64
	for i := 0; i < 3; i++ {
		for k := 0; k < 3; k++ {
			o[i] -= float64(view.R[k][i]) * float64(view.T[k])
		}
	}
	dot := func(a [3]float32, b [3]float64) float64 {
		return float64(a[0])*b[0] + float64(a[1])*b[1] + float64(a[2])*b[2]
	}
	n := p.Normal
	dd := float64(p.Dist) - dot(n, o)
	if math.Abs(dd) < 1e-9 {
		return SurfaceGradients{}, false
	}
	// D(X, Y) = fwd + (X - halfW)/scale * right + (halfH - Y)/scale * up, with
	// the continuous screen position (X, Y) of the pixel centre (du + 0.5).
	right, up, fwd := rowF64(view.R[0]), rowF64(view.R[1]), rowF64(view.R[2])
	// affine pieces of D: D = D0 + X*DX + Y*DY
	var d0, dx, dy [3]float64
	for i := 0; i < 3; i++ {
		dx[i] = right[i] / scale
		dy[i] = -up[i] / scale
		d0[i] = fwd[i] - halfW/scale*right[i] + halfH/scale*up[i]
	}
	dotv := func(a [3]float32, b [3]float64) float64 { return dot(a, b) }
	// zi = (n . D) / dd; sdivz = (A.o + off - min) * zi + A.D
	build := func(axis [3]float32, off, min float32) (org, su, sv float64) {
		c := dotv(axis, o) + float64(off) - float64(min)
		zi0, zix, ziy := dotv(n, d0)/dd, dotv(n, dx)/dd, dotv(n, dy)/dd
		a0, ax, ay := dotv(axis, d0), dotv(axis, dx), dotv(axis, dy)
		// value at the pixel (du, dv): X = du + 0.5, Y = dv + 0.5
		org = c*(zi0+0.5*zix+0.5*ziy) + a0 + 0.5*ax + 0.5*ay
		su = c*zix + ax
		sv = c*ziy + ay
		return
	}
	var g SurfaceGradients
	zi0, zix, ziy := dotv(n, d0)/dd, dotv(n, dx)/dd, dotv(n, dy)/dd
	g.ZiOrigin = float32(zi0 + 0.5*zix + 0.5*ziy)
	g.ZiStepU, g.ZiStepV = float32(zix), float32(ziy)
	a, b, c := build(p.AxisS, p.OffS, p.MinS)
	g.SdivzOrigin, g.SdivzStepU, g.SdivzStepV = float32(a), float32(b), float32(c)
	a, b, c = build(p.AxisT, p.OffT, p.MinT)
	g.TdivzOrigin, g.TdivzStepU, g.TdivzStepV = float32(a), float32(b), float32(c)
	g.BBExtentS = int32(p.ExtS<<16) - 1
	g.BBExtentT = int32(p.ExtT<<16) - 1
	return g, true
}

func rowF64(r [3]float32) [3]float64 { return [3]float64{float64(r[0]), float64(r[1]), float64(r[2])} }

// TurbSpan draws count pixels of one span starting at pixel (u, v), as
// Turbulent8 does for an espan_t: sub-spans of 16 pixels, s and t
// perspective-correct at their ends and stepped linearly. timeSec is cl.time.
// tex is the 64x64 (or other size) base texture; dst is the span's first pixel.
func TurbSpan(dst []byte, tex *Pic, g *SurfaceGradients, timeSec float64, u, v, count int) {
	turb := QuakeTurbTable[int(timeSec*TurbSpeed)&(TurbCycle-1):]
	du, dv := float32(u), float32(v)
	sdivz := g.SdivzOrigin + dv*g.SdivzStepV + du*g.SdivzStepU
	tdivz := g.TdivzOrigin + dv*g.TdivzStepV + du*g.TdivzStepU
	zi := g.ZiOrigin + dv*g.ZiStepV + du*g.ZiStepU
	z := float32(0x10000) / zi
	s := int32(sdivz * z)
	if s > g.BBExtentS {
		s = g.BBExtentS
	} else if s < 0 {
		s = 0
	}
	t := int32(tdivz * z)
	if t > g.BBExtentT {
		t = g.BBExtentT
	} else if t < 0 {
		t = 0
	}
	std := tex.Width == 64 && tex.Height == 64
	var sstep, tstep int32
	pos := 0
	for count > 0 {
		sc := count
		if sc > TurbSubSpan {
			sc = TurbSubSpan
		}
		count -= sc
		var snext, tnext int32
		if count > 0 {
			sdivz += g.SdivzStepU * float32(sc)
			tdivz += g.TdivzStepU * float32(sc)
			zi += g.ZiStepU * float32(sc)
			z = float32(0x10000) / zi
			snext, tnext = int32(sdivz*z), int32(tdivz*z)
			snext, tnext = clampNext(snext, g.BBExtentS), clampNext(tnext, g.BBExtentT)
			sstep, tstep = (snext-s)>>4, (tnext-t)>>4
		} else {
			m1 := float32(sc - 1)
			sdivz += g.SdivzStepU * m1
			tdivz += g.TdivzStepU * m1
			zi += g.ZiStepU * m1
			z = float32(0x10000) / zi
			snext, tnext = int32(sdivz*z), int32(tdivz*z)
			snext, tnext = clampNext(snext, g.BBExtentS), clampNext(tnext, g.BBExtentT)
			if sc > 1 {
				sstep, tstep = (snext-s)/int32(sc-1), (tnext-t)/int32(sc-1)
			}
		}
		s &= (TurbCycle << 16) - 1
		t &= (TurbCycle << 16) - 1
		for i := 0; i < sc; i++ {
			sturb := s + turb[(t>>16)&(TurbCycle-1)]
			tturb := t + turb[(s>>16)&(TurbCycle-1)]
			if std {
				dst[pos] = tex.Pixels[(int(tturb>>16)&63)<<6+int(sturb>>16)&63]
			} else {
				dst[pos] = tex.Pixels[int(uint32(tturb)>>16)%tex.Height*tex.Width+int(uint32(sturb)>>16)%tex.Width]
			}
			pos++
			s += sstep
			t += tstep
		}
		s, t = snext, tnext
	}
}

func clampNext(v, ext int32) int32 {
	if v > ext {
		return ext
	}
	if v < 16 {
		return 16
	}
	return v
}

// FillTurbulentQuake paints the convex polygon verts (screen positions only:
// X, Y) with Quake's turbulent spans: for every scanline run of covered
// pixels, one [TurbSpan] starting at the run's first pixel. The coverage is
// the engine's ([FillTurbulentPolygon]'s: a pixel is covered when its left
// edge is inside the polygon at the scanline centre).
func FillTurbulentQuake(fb *FrameBuffer, tex *Pic, g *SurfaceGradients, timeSec float64, verts []TexturedVertex) error {
	if fb == nil {
		return ErrTurbFillNilFB
	}
	if tex == nil {
		return ErrTurbFillNilTex
	}
	if len(verts) < 3 {
		return ErrTurbFillFewVerts
	}
	if len(verts) > MaxPolyVerts {
		return ErrTurbFillManyVerts
	}
	if tex.Width*tex.Height != len(tex.Pixels) {
		return ErrPicShape
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
			TurbSpan(fb.Pixels[y*fb.Pitch+x0:], tex, g, timeSec, x0, y, x1-x0+1)
		}
	}
	return nil
}
