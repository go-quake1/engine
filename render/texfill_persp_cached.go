// Copyright (c) 1996-1997 Id Software, Inc.
// Copyright (c) 2026 the go-quake1/engine authors.
// SPDX-License-Identifier: GPL-2.0-or-later

package render

import (
	"errors"
	"math"
)

// CachedSurface is a per-face BAKED surface: the face's texture already
// modulated by its lightmap through the colormap, stored as final palette
// indices. It is Quake's "surface cache" (D_CacheSurface): built once when a
// face is first drawn (or its lightmap changes) and reused across frames, so
// the per-pixel span loop collapses to a single fetch + store -- no per-pixel
// lightmap bilinear, no colormap lookup, no depth divide for shading.
//
// The surface covers the face's texture EXTENT at lightmap resolution*16
// (i.e. texture resolution over the lit region). Cache texel (cx, cy) maps to
// the face's (LmS*16, LmT*16) surface coordinate, so the rasterizer indexes it
// with the same LmS/LmT the lightmapped path carries, scaled by 16.
type CachedSurface struct {
	W, H   int
	Pixels []byte // W*H final palette indices
}

// CachedVertex is a screen vertex carrying cache-surface coords (CU, CV) in
// baked-texel units (= the face's LmS*16 / LmT*16).
type CachedVertex struct {
	X, Y, Z float32
	CU, CV  float32
}

// Sentinel errors returned by FillPerspectiveCachedPolygon.
var (
	ErrCachedFillNilFB     = errors.New("render: nil framebuffer in cached fill")
	ErrCachedFillNilSurf   = errors.New("render: nil surface in cached fill")
	ErrCachedFillFewVerts  = errors.New("render: cached polygon needs >= 3 vertices")
	ErrCachedFillManyVerts = errors.New("render: cached polygon vertex count exceeds MaxPolyVerts")
	ErrCachedFillZeroZ     = errors.New("render: cached polygon vertex has Z <= 0")
	ErrCachedFillSurfShape = errors.New("render: cached surface W*H != len(Pixels)")
	ErrCachedFillSurfEmpty = errors.New("render: cached surface has zero width or height")
)

// FillPerspectiveCachedPolygon paints a convex polygon by perspective-correct
// sampling of a pre-baked [CachedSurface] -- the fast path that replaces
// FillPerspectiveLightmappedPolygon once a face's surface is cached. Same
// homogeneous 1/z subdivision as the other perspective fills; the inner loop
// is a clamped fetch + store (the surface already holds lit palette indices),
// and cache coords are CLAMPED (not tile-wrapped) since the surface is exactly
// the face's lit extent.
func FillPerspectiveCachedPolygon(fb *FrameBuffer, surf *CachedSurface, verts []CachedVertex) error {
	if fb == nil {
		return ErrCachedFillNilFB
	}
	if surf == nil {
		return ErrCachedFillNilSurf
	}
	if len(verts) < 3 {
		return ErrCachedFillFewVerts
	}
	if len(verts) > MaxPolyVerts {
		return ErrCachedFillManyVerts
	}
	if surf.W <= 0 || surf.H <= 0 {
		return ErrCachedFillSurfEmpty
	}
	if surf.W*surf.H != len(surf.Pixels) {
		return ErrCachedFillSurfShape
	}
	for _, v := range verts {
		if v.Z <= 0 {
			return ErrCachedFillZeroZ
		}
	}

	sW := surf.W
	maxX := sW - 1
	maxY := surf.H - 1

	var hOoz, hUoz, hVoz [MaxPolyVerts]float32
	for i, v := range verts {
		inv := 1.0 / v.Z
		hOoz[i] = inv
		hUoz[i] = v.CU * inv
		hVoz[i] = v.CV * inv
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
		var xs, oozs, uozs, vozs [MaxPolyVerts]float32
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
				nXs++
			}
		}
		for i := 1; i < nXs; i++ {
			for j := i; j > 0 && xs[j-1] > xs[j]; j-- {
				xs[j-1], xs[j] = xs[j], xs[j-1]
				oozs[j-1], oozs[j] = oozs[j], oozs[j-1]
				uozs[j-1], uozs[j] = uozs[j], uozs[j-1]
				vozs[j-1], vozs[j] = vozs[j], vozs[j-1]
			}
		}
		for pair := 0; pair+1 < nXs; pair += 2 {
			xLeft, xRight := xs[pair], xs[pair+1]
			oozL, oozR := oozs[pair], oozs[pair+1]
			uozL, uozR := uozs[pair], uozs[pair+1]
			vozL, vozR := vozs[pair], vozs[pair+1]

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

			xf := float32(x0) + 0.5
			ooz := fma32(xf-xLeft, dOoz, oozL)
			uoz := fma32(xf-xLeft, dUoz, uozL)
			voz := fma32(xf-xLeft, dVoz, vozL)

			z := 1.0 / ooz
			u := uoz * z
			v := voz * z

			row := y * fb.Pitch
			count := x1 - x0 + 1
			pix := x0

			for count > 0 {
				var spanLen int
				var uNext, vNext float32
				if count > PerspSubdivStep {
					spanLen = PerspSubdivStep
					oozEnd := fma32(dOoz, float32(PerspSubdivStep), ooz)
					uozEnd := fma32(dUoz, float32(PerspSubdivStep), uoz)
					vozEnd := fma32(dVoz, float32(PerspSubdivStep), voz)
					zEnd := 1.0 / oozEnd
					uNext = uozEnd * zEnd
					vNext = vozEnd * zEnd
				} else {
					spanLen = count
					steps := float32(spanLen - 1)
					oozEnd := fma32(dOoz, steps, ooz)
					uozEnd := fma32(dUoz, steps, uoz)
					vozEnd := fma32(dVoz, steps, voz)
					zEnd := 1.0 / oozEnd
					uNext = uozEnd * zEnd
					vNext = vozEnd * zEnd
				}

				var du, dv float32
				if spanLen > 1 {
					inv := 1.0 / float32(spanLen)
					if count <= PerspSubdivStep {
						inv = 1.0 / float32(spanLen-1)
					}
					du = (uNext - u) * inv
					dv = (vNext - v) * inv
				}

				cu, cv := u, v
				for k := 0; k < spanLen; k++ {
					ui := int(math.Floor(float64(cu)))
					vi := int(math.Floor(float64(cv)))
					if ui < 0 {
						ui = 0
					} else if ui > maxX {
						ui = maxX
					}
					if vi < 0 {
						vi = 0
					} else if vi > maxY {
						vi = maxY
					}
					fb.Pixels[row+pix+k] = surf.Pixels[vi*sW+ui]
					cu += du
					cv += dv
				}

				ooz = fma32(dOoz, float32(spanLen), ooz)
				uoz = fma32(dUoz, float32(spanLen), uoz)
				voz = fma32(dVoz, float32(spanLen), voz)
				u = uNext
				v = vNext
				pix += spanLen
				count -= spanLen
			}
		}
	}
	return nil
}
