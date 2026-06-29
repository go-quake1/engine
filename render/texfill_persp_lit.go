// Copyright (c) 1996-1997 Id Software, Inc.
// Copyright (c) 2026 the go-quake1/engine authors.
// SPDX-License-Identifier: GPL-2.0-or-later

package render

import (
	"errors"
	"math"
)

// PerspLitTexturedVertex extends PerspTexturedVertex with a per-vertex
// Light value (0..ColorMapRows-1; out-of-range clamps inside the
// colormap lookup). X/Y are screen-space pixels; Z is view-space depth;
// U/V are texture-space pixels.
type PerspLitTexturedVertex struct {
	X, Y, Z float32
	U, V    float32
	Light   float32 // 0..63 (ColorMapRows-1)
}

// Sentinel errors returned by FillPerspectiveLitTexturedPolygon.
var (
	ErrPerspLitTexFillNilFB     = errors.New("render: nil framebuffer in perspective lit textured fill")
	ErrPerspLitTexFillNilTex    = errors.New("render: nil texture in perspective lit textured fill")
	ErrPerspLitTexFillNilCM     = errors.New("render: nil colormap in perspective lit textured fill")
	ErrPerspLitTexFillFewVerts  = errors.New("render: perspective lit textured polygon needs >= 3 vertices")
	ErrPerspLitTexFillManyVerts = errors.New("render: perspective lit textured polygon vertex count exceeds MaxPolyVerts")
	ErrPerspLitTexFillZeroZ     = errors.New("render: perspective lit textured polygon vertex has Z <= 0")
)

// FillPerspectiveLitTexturedPolygon paints the convex 2D polygon with
// perspective-correct U/V AND perspective-correct per-vertex (gourand)
// lighting. It is the combined cousin of FillPerspectiveTexturedPolygon
// and FillLitTexturedPolygon.
//
// Why perspective-correct light too: at oblique angles, affine light
// interpolation produces the same swim that affine UV does -- a fixed
// step in screen-space corresponds to a varying step in world-space.
// tyrquake's d_polyse.c routes the per-vertex light through the same
// 1/Z divide as UV; we mirror that here by carrying (1/Z, U/Z, V/Z,
// L/Z) across edges and sub-spans, dividing back at every sub-span
// boundary.
//
// Algorithm: same edge-walk + subdiv-8 cadence as
// FillPerspectiveTexturedPolygon, with a fourth interpolated channel
// (L/Z) joining U/Z and V/Z. The colormap is REQUIRED -- per-pixel
// light makes no sense without one (pass a no-op colormap where
// cm[light][src] = src for unlit equivalence).
//
// Caller responsibility: convex polygon; all Z > 0 (frustum-clipped).
//
// Errors:
//
//	ErrPerspLitTexFillNilFB     fb == nil
//	ErrPerspLitTexFillNilTex    tex == nil
//	ErrPerspLitTexFillNilCM     cm == nil
//	ErrPerspLitTexFillFewVerts  len(verts) < 3
//	ErrPerspLitTexFillManyVerts len(verts) > MaxPolyVerts
//	ErrPerspLitTexFillZeroZ     any vertex with Z <= 0
//	ErrPicShape                 tex.Width*tex.Height != len(tex.Pixels)
func FillPerspectiveLitTexturedPolygon(fb *FrameBuffer, tex *Pic, cm *ColorMap, verts []PerspLitTexturedVertex) error {
	if fb == nil {
		return ErrPerspLitTexFillNilFB
	}
	if tex == nil {
		return ErrPerspLitTexFillNilTex
	}
	if cm == nil {
		return ErrPerspLitTexFillNilCM
	}
	if len(verts) < 3 {
		return ErrPerspLitTexFillFewVerts
	}
	if len(verts) > MaxPolyVerts {
		return ErrPerspLitTexFillManyVerts
	}
	if tex.Width*tex.Height != len(tex.Pixels) {
		return ErrPicShape
	}
	for _, v := range verts {
		if v.Z <= 0 {
			return ErrPerspLitTexFillZeroZ
		}
	}

	texW := tex.Width
	texH := tex.Height

	// Pre-compute homogeneous coords per vertex.
	var hOoz, hUoz, hVoz, hLoz [MaxPolyVerts]float32
	for i, v := range verts {
		inv := 1.0 / v.Z
		hOoz[i] = inv
		hUoz[i] = v.U * inv
		hVoz[i] = v.V * inv
		hLoz[i] = v.Light * inv
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
		yf := float32(y) + 0.5 // scanline center
		// Per-edge crossing buffers: x position + homogeneous
		// (1/z, u/z, v/z, l/z) at that crossing.
		var xs, oozs, uozs, vozs, lozs [MaxPolyVerts]float32
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
				lozs[nXs] = fma32(t, hLoz[j]-hLoz[i], hLoz[i])
				nXs++
			}
		}
		// Insertion-sort crossings by x; carry homogeneous values.
		for i := 1; i < nXs; i++ {
			for j := i; j > 0 && xs[j-1] > xs[j]; j-- {
				xs[j-1], xs[j] = xs[j], xs[j-1]
				oozs[j-1], oozs[j] = oozs[j], oozs[j-1]
				uozs[j-1], uozs[j] = uozs[j], uozs[j-1]
				vozs[j-1], vozs[j] = vozs[j], vozs[j-1]
				lozs[j-1], lozs[j] = lozs[j], lozs[j-1]
			}
		}
		for pair := 0; pair+1 < nXs; pair += 2 {
			xLeft, xRight := xs[pair], xs[pair+1]
			oozL, oozR := oozs[pair], oozs[pair+1]
			uozL, uozR := uozs[pair], uozs[pair+1]
			vozL, vozR := vozs[pair], vozs[pair+1]
			lozL, lozR := lozs[pair], lozs[pair+1]

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

			// Per-pixel step of the homogeneous values across the span.
			span := xRight - xLeft
			dOoz := (oozR - oozL) / span
			dUoz := (uozR - uozL) / span
			dVoz := (vozR - vozL) / span
			dLoz := (lozR - lozL) / span

			// Initial homogeneous values at the FIRST pixel center
			// (x0 + 0.5), offset from xLeft.
			xf := float32(x0) + 0.5
			ooz := fma32(xf-xLeft, dOoz, oozL)
			uoz := fma32(xf-xLeft, dUoz, uozL)
			voz := fma32(xf-xLeft, dVoz, vozL)
			loz := fma32(xf-xLeft, dLoz, lozL)

			// First divide: real (u, v, l) at the first pixel.
			z := 1.0 / ooz
			u := uoz * z
			v := voz * z
			l := loz * z

			row := y * fb.Pitch
			count := x1 - x0 + 1
			pix := x0

			// Sub-span loop, D_DrawSpans8 pattern: full 8-pixel
			// sub-spans first (one divide at the far end + linear
			// step over 8 pixels), then a final partial sub-span.
			for count > 0 {
				var spanLen int
				var uNext, vNext, lNext float32
				if count > PerspSubdivStep {
					spanLen = PerspSubdivStep
					// Advance the homogeneous accumulators to the
					// start of the NEXT sub-span (8 pixels along).
					oozEnd := fma32(dOoz, float32(PerspSubdivStep), ooz)
					uozEnd := fma32(dUoz, float32(PerspSubdivStep), uoz)
					vozEnd := fma32(dVoz, float32(PerspSubdivStep), voz)
					lozEnd := fma32(dLoz, float32(PerspSubdivStep), loz)
					zEnd := 1.0 / oozEnd
					uNext = uozEnd * zEnd
					vNext = vozEnd * zEnd
					lNext = lozEnd * zEnd
				} else {
					// Final sub-span: <= PerspSubdivStep pixels.
					// One divide at the LAST pixel (count-1 along)
					// to pin the endpoint without overshooting.
					spanLen = count
					steps := float32(spanLen - 1)
					oozEnd := fma32(dOoz, steps, ooz)
					uozEnd := fma32(dUoz, steps, uoz)
					vozEnd := fma32(dVoz, steps, voz)
					lozEnd := fma32(dLoz, steps, loz)
					zEnd := 1.0 / oozEnd
					uNext = uozEnd * zEnd
					vNext = vozEnd * zEnd
					lNext = lozEnd * zEnd
				}

				// Per-pixel linear (u, v, l) step within the sub-span.
				var du, dv, dl float32
				if spanLen > 1 {
					inv := 1.0 / float32(spanLen)
					if count <= PerspSubdivStep {
						// Final partial sub-span: divide by
						// (spanLen-1) so we land exactly on the
						// pinned endpoint without overshoot.
						inv = 1.0 / float32(spanLen-1)
					}
					du = (uNext - u) * inv
					dv = (vNext - v) * inv
					dl = (lNext - l) * inv
				}

				cu, cv, cl := u, v, l
				for k := 0; k < spanLen; k++ {
					ui := int(math.Floor(float64(cu)))
					vi := int(math.Floor(float64(cv)))
					// Tile-wrap: Quake textures repeat across surfaces.
					ui = ((ui % texW) + texW) % texW
					vi = ((vi % texH) + texH) % texH
					texel := tex.Pixels[vi*tex.Width+ui]
					fb.Pixels[row+pix+k] = cm.LightIndex(int(math.Floor(float64(cl))), texel)
					cu += du
					cv += dv
					cl += dl
				}

				// Advance to the next sub-span: homogeneous
				// accumulators step by spanLen pixels; (u, v, l)
				// resume from the divided-exact endpoints.
				ooz = fma32(dOoz, float32(spanLen), ooz)
				uoz = fma32(dUoz, float32(spanLen), uoz)
				voz = fma32(dVoz, float32(spanLen), voz)
				loz = fma32(dLoz, float32(spanLen), loz)
				u = uNext
				v = vNext
				l = lNext
				pix += spanLen
				count -= spanLen
			}
		}
	}
	return nil
}
