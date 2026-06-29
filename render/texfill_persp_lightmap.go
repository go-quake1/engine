// Copyright (c) 1996-1997 Id Software, Inc.
// Copyright (c) 2026 the go-quake1/engine authors.
// SPDX-License-Identifier: GPL-2.0-or-later

package render

import (
	"errors"
	"math"
)

// LightmappedVertex extends PerspTexturedVertex with per-vertex
// lightmap-grid coordinates. LmS / LmT are in lightmap-sample units
// (1 sample per 16 texture-pixel units in S/T space), with the face's
// origin already subtracted -- i.e. LmS == 0 is the FIRST sample in
// the face's per-surface lightmap plane, LmS == lmW-1 is the LAST.
//
// The bsprender package computes these via FaceLightmapInfo +
// TransformFaceLightmapped, so the rasterizer can stay agnostic of
// the BSP-level (texturemins / extents) bookkeeping.
type LightmappedVertex struct {
	X, Y, Z  float32 // screen pixel + view-space depth
	U, V     float32 // texture-pixel UV
	LmS, LmT float32 // lightmap-sample coords (0..lmW-1 / 0..lmH-1)
}

// Sentinel errors returned by FillPerspectiveLightmappedPolygon.
var (
	ErrLmFillNilFB      = errors.New("render: nil framebuffer in lightmapped fill")
	ErrLmFillNilTex     = errors.New("render: nil texture in lightmapped fill")
	ErrLmFillNilCM      = errors.New("render: nil colormap in lightmapped fill (lighting requires the per-light-row LUT)")
	ErrLmFillFewVerts   = errors.New("render: lightmapped polygon needs >= 3 vertices")
	ErrLmFillManyVerts  = errors.New("render: lightmapped polygon vertex count exceeds MaxPolyVerts")
	ErrLmFillZeroZ      = errors.New("render: lightmapped polygon vertex has Z <= 0")
	ErrLmFillBadPlane   = errors.New("render: lightmap plane dims do not match len(lightmap)")
	ErrLmFillPlaneEmpty = errors.New("render: lightmap plane has zero width or height")
)

// FillPerspectiveLightmappedPolygon paints a convex polygon with
// BOTH perspective-correct texture sampling AND per-pixel lighting
// driven by a per-face lightmap plane.
//
// Algorithm: same homogeneous-coord pipeline as
// FillPerspectiveTexturedPolygon for (U/Z, V/Z, 1/Z), extended to
// also carry (LmS/Z, LmT/Z). Every PerspSubdivStep pixels we divide
// 1/Z back into real (U, V, LmS, LmT) so the lightmap also samples
// perspective-correct rather than affinely. Inside a sub-span we
// linearly step all four. Per pixel:
//
//  1. Wrap U/V into [0, texW) / [0, texH); sample tex.Pixels.
//  2. Bilinearly sample the lightmap plane at (LmS, LmT) -> 0..255.
//  3. Convert sample to colormap row: row = (255 - sample) >> 2
//     so sample==255 maps to row 0 (brightest) and sample==0 maps
//     to row 63 (darkest) -- the convention cm.LightIndex expects.
//  4. Write cm.LightIndex(row, texel) to the framebuffer.
//
// The lightmap plane is the CALLER's responsibility to pre-build
// from the BSP per-face lightmap layers + per-style brightness;
// this keeps the rasterizer indifferent to lump layout and
// lightstyle animation. Pass lmW * lmH bytes.
//
// Returns:
//
//	ErrLmFillNilFB / NilTex / NilCM
//	ErrLmFillFewVerts / ManyVerts
//	ErrLmFillZeroZ        any vertex has Z <= 0 (frustum-clip first)
//	ErrLmFillPlaneEmpty   lmW <= 0 or lmH <= 0
//	ErrLmFillBadPlane     lmW * lmH != len(lightmap)
//	ErrPicShape           tex shape mismatch
func FillPerspectiveLightmappedPolygon(
	fb *FrameBuffer,
	tex *Pic,
	cm *ColorMap,
	verts []LightmappedVertex,
	lightmap []byte,
	lmW, lmH int,
) error {
	if fb == nil {
		return ErrLmFillNilFB
	}
	if tex == nil {
		return ErrLmFillNilTex
	}
	if cm == nil {
		return ErrLmFillNilCM
	}
	if len(verts) < 3 {
		return ErrLmFillFewVerts
	}
	if len(verts) > MaxPolyVerts {
		return ErrLmFillManyVerts
	}
	if tex.Width*tex.Height != len(tex.Pixels) {
		return ErrPicShape
	}
	if lmW <= 0 || lmH <= 0 {
		return ErrLmFillPlaneEmpty
	}
	if lmW*lmH != len(lightmap) {
		return ErrLmFillBadPlane
	}
	for _, v := range verts {
		if v.Z <= 0 {
			return ErrLmFillZeroZ
		}
	}

	texW := tex.Width
	texH := tex.Height
	lmMaxS := lmW - 1
	lmMaxT := lmH - 1

	// Pre-compute homogeneous coords per vertex.
	var hOoz, hUoz, hVoz, hLsOz, hLtOz [MaxPolyVerts]float32
	for i, v := range verts {
		inv := 1.0 / v.Z
		hOoz[i] = inv
		hUoz[i] = v.U * inv
		hVoz[i] = v.V * inv
		hLsOz[i] = v.LmS * inv
		hLtOz[i] = v.LmT * inv
	}

	yMin, yMax := verts[0].Y, verts[0].Y
	for _, v := range verts[1:] {
		if v.Y < yMin {
			yMin = v.Y
		}
		if v.Y > yMax {
			yMax = v.Y
		}
	}

	yStart := int(math.Floor(float64(yMin)))
	yEnd := int(math.Ceil(float64(yMax)))
	if yStart < 0 {
		yStart = 0
	}
	if yEnd > fb.Height {
		yEnd = fb.Height
	}
	if yStart >= yEnd {
		return nil
	}

	for y := yStart; y < yEnd; y++ {
		yf := float32(y) + 0.5
		var xs, oozs, uozs, vozs, lsozs, ltozs [MaxPolyVerts]float32
		nXs := 0
		for i := 0; i < len(verts); i++ {
			j := (i + 1) % len(verts)
			y0, y1 := verts[i].Y, verts[j].Y
			if (y0 <= yf && y1 > yf) || (y1 <= yf && y0 > yf) {
				t := (yf - y0) / (y1 - y0)
				xs[nXs] = fma32(t, verts[j].X-verts[i].X, verts[i].X)
				oozs[nXs] = fma32(t, hOoz[j]-hOoz[i], hOoz[i])
				uozs[nXs] = fma32(t, hUoz[j]-hUoz[i], hUoz[i])
				vozs[nXs] = fma32(t, hVoz[j]-hVoz[i], hVoz[i])
				lsozs[nXs] = fma32(t, hLsOz[j]-hLsOz[i], hLsOz[i])
				ltozs[nXs] = fma32(t, hLtOz[j]-hLtOz[i], hLtOz[i])
				nXs++
			}
		}
		for i := 1; i < nXs; i++ {
			for j := i; j > 0 && xs[j-1] > xs[j]; j-- {
				xs[j-1], xs[j] = xs[j], xs[j-1]
				oozs[j-1], oozs[j] = oozs[j], oozs[j-1]
				uozs[j-1], uozs[j] = uozs[j], uozs[j-1]
				vozs[j-1], vozs[j] = vozs[j], vozs[j-1]
				lsozs[j-1], lsozs[j] = lsozs[j], lsozs[j-1]
				ltozs[j-1], ltozs[j] = ltozs[j], ltozs[j-1]
			}
		}
		for pair := 0; pair+1 < nXs; pair += 2 {
			xLeft, xRight := xs[pair], xs[pair+1]
			oozL, oozR := oozs[pair], oozs[pair+1]
			uozL, uozR := uozs[pair], uozs[pair+1]
			vozL, vozR := vozs[pair], vozs[pair+1]
			lsozL, lsozR := lsozs[pair], lsozs[pair+1]
			ltozL, ltozR := ltozs[pair], ltozs[pair+1]

			x0 := int(math.Ceil(float64(xLeft)))
			x1 := int(math.Floor(float64(xRight)))
			if x0 < 0 {
				x0 = 0
			}
			if x1 >= fb.Width {
				x1 = fb.Width - 1
			}
			if x0 > x1 {
				continue
			}

			span := xRight - xLeft
			dOoz := (oozR - oozL) / span
			dUoz := (uozR - uozL) / span
			dVoz := (vozR - vozL) / span
			dLsOz := (lsozR - lsozL) / span
			dLtOz := (ltozR - ltozL) / span

			xf := float32(x0) + 0.5
			ooz := fma32(xf-xLeft, dOoz, oozL)
			uoz := fma32(xf-xLeft, dUoz, uozL)
			voz := fma32(xf-xLeft, dVoz, vozL)
			lsoz := fma32(xf-xLeft, dLsOz, lsozL)
			ltoz := fma32(xf-xLeft, dLtOz, ltozL)

			z := 1.0 / ooz
			u := uoz * z
			v := voz * z
			ls := lsoz * z
			lt := ltoz * z

			row := y * fb.Pitch
			count := x1 - x0 + 1
			pix := x0

			for count > 0 {
				var spanLen int
				var uNext, vNext, lsNext, ltNext float32
				if count > PerspSubdivStep {
					spanLen = PerspSubdivStep
					oozEnd := fma32(dOoz, float32(PerspSubdivStep), ooz)
					uozEnd := fma32(dUoz, float32(PerspSubdivStep), uoz)
					vozEnd := fma32(dVoz, float32(PerspSubdivStep), voz)
					lsozEnd := fma32(dLsOz, float32(PerspSubdivStep), lsoz)
					ltozEnd := fma32(dLtOz, float32(PerspSubdivStep), ltoz)
					zEnd := 1.0 / oozEnd
					uNext = uozEnd * zEnd
					vNext = vozEnd * zEnd
					lsNext = lsozEnd * zEnd
					ltNext = ltozEnd * zEnd
				} else {
					spanLen = count
					steps := float32(spanLen - 1)
					oozEnd := fma32(dOoz, steps, ooz)
					uozEnd := fma32(dUoz, steps, uoz)
					vozEnd := fma32(dVoz, steps, voz)
					lsozEnd := fma32(dLsOz, steps, lsoz)
					ltozEnd := fma32(dLtOz, steps, ltoz)
					zEnd := 1.0 / oozEnd
					uNext = uozEnd * zEnd
					vNext = vozEnd * zEnd
					lsNext = lsozEnd * zEnd
					ltNext = ltozEnd * zEnd
				}

				var du, dv, dLs, dLt float32
				if spanLen > 1 {
					inv := 1.0 / float32(spanLen)
					if count <= PerspSubdivStep {
						inv = 1.0 / float32(spanLen-1)
					}
					du = (uNext - u) * inv
					dv = (vNext - v) * inv
					dLs = (lsNext - ls) * inv
					dLt = (ltNext - lt) * inv
				}

				cu, cv := u, v
				cls, clt := ls, lt
				for k := 0; k < spanLen; k++ {
					// Texture: tile-wrap.
					ui := int(math.Floor(float64(cu)))
					vi := int(math.Floor(float64(cv)))
					ui = ((ui % texW) + texW) % texW
					vi = ((vi % texH) + texH) % texH
					texel := tex.Pixels[vi*tex.Width+ui]

					// Lightmap: clamp + bilinear. Lightmap coords
					// are in sample units, not texture pixels, so
					// floor() gives the top-left of a 2x2 cell.
					lsf := float64(cls)
					ltf := float64(clt)
					if lsf < 0 {
						lsf = 0
					} else if lsf > float64(lmMaxS) {
						lsf = float64(lmMaxS)
					}
					if ltf < 0 {
						ltf = 0
					} else if ltf > float64(lmMaxT) {
						ltf = float64(lmMaxT)
					}
					si0 := int(math.Floor(lsf))
					ti0 := int(math.Floor(ltf))
					si1 := si0 + 1
					ti1 := ti0 + 1
					if si1 > lmMaxS {
						si1 = lmMaxS
					}
					if ti1 > lmMaxT {
						ti1 = lmMaxT
					}
					fs := lsf - float64(si0)
					ft := ltf - float64(ti0)
					l00 := float64(lightmap[ti0*lmW+si0])
					l10 := float64(lightmap[ti0*lmW+si1])
					l01 := float64(lightmap[ti1*lmW+si0])
					l11 := float64(lightmap[ti1*lmW+si1])
					l0 := l00 + fs*(l10-l00)
					l1 := l01 + fs*(l11-l01)
					// Bilinear of 4 bytes in [0,255] with weights in
					// [0,1] is itself in [0,255]; no further clamp.
					sample := l0 + ft*(l1-l0)
					// Colormap row: 0 = brightest, 63 = darkest.
					// sample==255 -> row 0, sample==0 -> row 63.
					cmRow := (255 - int(sample)) >> 2

					fb.Pixels[row+pix+k] = cm.LightIndex(cmRow, texel)

					cu += du
					cv += dv
					cls += dLs
					clt += dLt
				}

				ooz = fma32(dOoz, float32(spanLen), ooz)
				uoz = fma32(dUoz, float32(spanLen), uoz)
				voz = fma32(dVoz, float32(spanLen), voz)
				lsoz = fma32(dLsOz, float32(spanLen), lsoz)
				ltoz = fma32(dLtOz, float32(spanLen), ltoz)
				u = uNext
				v = vNext
				ls = lsNext
				lt = ltNext
				pix += spanLen
				count -= spanLen
			}
		}
	}
	return nil
}
