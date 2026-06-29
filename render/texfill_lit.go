// Copyright (c) 1996-1997 Id Software, Inc.
// Copyright (c) 2026 the go-quake1/engine authors.
// SPDX-License-Identifier: GPL-2.0-or-later

package render

import (
	"errors"
	"math"
)

// LitTexturedVertex extends TexturedVertex with a per-vertex Light
// value (0..ColorMapRows-1; out-of-range clamps). The rasterizer
// linearly interpolates Light along scanlines just like U/V.
type LitTexturedVertex struct {
	X, Y, U, V float32
	Light      float32 // 0..63 (ColorMapRows-1)
}

// Sentinel errors returned by FillLitTexturedPolygon.
var (
	ErrLitTexFillNilFB     = errors.New("render: nil framebuffer in lit textured fill")
	ErrLitTexFillNilTex    = errors.New("render: nil texture in lit textured fill")
	ErrLitTexFillNilCM     = errors.New("render: nil colormap in lit textured fill")
	ErrLitTexFillFewVerts  = errors.New("render: lit textured polygon needs >= 3 vertices")
	ErrLitTexFillManyVerts = errors.New("render: lit textured polygon vertex count exceeds MaxPolyVerts")
)

// FillLitTexturedPolygon is FillTexturedPolygon with per-vertex
// lighting: each scanline interpolates U, V, AND Light linearly
// between edge crossings; per pixel, sample the texture, then
// look up the lit palette index via cm.LightIndex(light, texel).
//
// Unlike FillTexturedPolygon, the colormap is REQUIRED (the
// per-pixel light makes no sense without one). Pass a "no-op"
// colormap (every cell = src) for unlit rendering equivalence.
//
// Per-pixel Light is clamped to [0, ColorMapRows-1] inside
// cm.LightIndex; out-of-range vertex lights are therefore safe.
//
// Caller responsibility: verts must be convex.
//
// Errors:
//
//	ErrLitTexFillNilFB     fb == nil
//	ErrLitTexFillNilTex    tex == nil
//	ErrLitTexFillNilCM     cm == nil
//	ErrLitTexFillFewVerts  len(verts) < 3
//	ErrLitTexFillManyVerts len(verts) > MaxPolyVerts
//	ErrPicShape            tex.Width*tex.Height != len(tex.Pixels)
//
// tyrquake: the per-vertex (gourand) cousin of D_DrawSubdiv in
// d_polyse.c; matches the affine path of FillTexturedPolygon but
// adds a third interpolated scanline channel for light.
func FillLitTexturedPolygon(fb *FrameBuffer, tex *Pic, cm *ColorMap, verts []LitTexturedVertex) error {
	if fb == nil {
		return ErrLitTexFillNilFB
	}
	if tex == nil {
		return ErrLitTexFillNilTex
	}
	if cm == nil {
		return ErrLitTexFillNilCM
	}
	if len(verts) < 3 {
		return ErrLitTexFillFewVerts
	}
	if len(verts) > MaxPolyVerts {
		return ErrLitTexFillManyVerts
	}
	if tex.Width*tex.Height != len(tex.Pixels) {
		return ErrPicShape
	}

	texW := tex.Width
	texH := tex.Height

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
		// Per-edge crossing buffers: x position + interpolated u, v, light.
		var xs, us, vs, ls [MaxPolyVerts]float32
		nXs := 0
		for i := 0; i < len(verts); i++ {
			a := verts[i]
			b := verts[(i+1)%len(verts)]
			y0, y1 := a.Y, b.Y
			if (y0 <= yf && y1 > yf) || (y1 <= yf && y0 > yf) {
				t := (yf - y0) / (y1 - y0)
				xs[nXs] = a.X + t*(b.X-a.X)
				us[nXs] = a.U + t*(b.U-a.U)
				vs[nXs] = a.V + t*(b.V-a.V)
				ls[nXs] = a.Light + t*(b.Light-a.Light)
				nXs++
			}
		}
		// Insertion-sort crossings by x; carry u, v, light with them.
		for i := 1; i < nXs; i++ {
			for j := i; j > 0 && xs[j-1] > xs[j]; j-- {
				xs[j-1], xs[j] = xs[j], xs[j-1]
				us[j-1], us[j] = us[j], us[j-1]
				vs[j-1], vs[j] = vs[j], vs[j-1]
				ls[j-1], ls[j] = ls[j], ls[j-1]
			}
		}
		for pair := 0; pair+1 < nXs; pair += 2 {
			xLeft := xs[pair]
			xRight := xs[pair+1]
			uLeft, uRight := us[pair], us[pair+1]
			vLeft, vRight := vs[pair], vs[pair+1]
			lLeft, lRight := ls[pair], ls[pair+1]

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

			// Linear UV+light step per output pixel; span is the
			// float distance between the edge crossings, NOT the
			// integer pixel count.
			span := xRight - xLeft
			duDx := (uRight - uLeft) / span
			dvDx := (vRight - vLeft) / span
			dlDx := (lRight - lLeft) / span

			row := y * fb.Pitch
			for x := x0; x <= x1; x++ {
				// Sample at pixel center: x + 0.5.
				xf := float32(x) + 0.5
				u := uLeft + (xf-xLeft)*duDx
				v := vLeft + (xf-xLeft)*dvDx
				l := lLeft + (xf-xLeft)*dlDx
				ui := int(math.Floor(float64(u)))
				vi := int(math.Floor(float64(v)))
				// Tile-wrap: Quake textures repeat across surfaces.
				ui = ((ui % texW) + texW) % texW
				vi = ((vi % texH) + texH) % texH
				texel := tex.Pixels[vi*tex.Width+ui]
				fb.Pixels[row+x] = cm.LightIndex(int(math.Floor(float64(l))), texel)
			}
		}
	}
	return nil
}
