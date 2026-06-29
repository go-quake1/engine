// Copyright (c) 1996-1997 Id Software, Inc.
// Copyright (c) 2026 the go-quake1/engine authors.
// SPDX-License-Identifier: GPL-2.0-or-later

package render

import (
	"errors"
	"math"
)

// TexturedVertex is one polygon corner with UV texture coordinates.
// X, Y in screen-space pixels; U, V in texture-space pixels (0..tex.Width,
// 0..tex.Height; wrap is the caller's job if needed).
type TexturedVertex struct {
	X, Y float32 // screen pixel
	U, V float32 // texture pixel
}

// Sentinel errors returned by FillTexturedPolygon.
var (
	ErrTexFillNilFB     = errors.New("render: nil framebuffer in textured fill")
	ErrTexFillNilTex    = errors.New("render: nil texture in textured fill")
	ErrTexFillFewVerts  = errors.New("render: textured polygon needs >= 3 vertices")
	ErrTexFillManyVerts = errors.New("render: textured polygon vertex count exceeds MaxPolyVerts")
)

// FillTexturedPolygon paints the convex 2D polygon defined by verts
// with the texture sampled per pixel from `tex`. UV is linearly
// interpolated along each scanline (affine; NOT perspective-correct).
//
// `lightLevel` selects the colormap row used to attenuate the
// sampled texel (0 = full bright, 63 = darkest). If `cm` is nil,
// the raw texel is written without lighting.
//
// Sampling: for each output pixel (x, y), compute (u, v) from the
// scanline endpoints; clamp to [0, tex.Width-1] / [0, tex.Height-1]
// (no wrapping; caller wraps if needed); read tex.Pixels[v*Width+u];
// optionally light via cm.LightIndex(lightLevel, texel); write to
// fb.Pixels.
//
// Algorithm: mirrors FillPolygon's edge-walking scanline fill, but
// each edge crossing also carries the interpolated (u, v) at that
// point; the per-span inner loop linearly interpolates (u, v) from
// left to right and per-pixel samples the texture.
//
// Sample positions: scanlines are sampled at y+0.5 (scanline center),
// per-pixel x is sampled at x+0.5 (pixel center). Integer u, v are
// floor() of the float result; final clamp pins to texture bounds.
//
// Caller responsibilities:
//   - verts must be convex (same as FillPolygon)
//   - tex.Width * tex.Height == len(tex.Pixels) (validated)
//
// tyrquake: simplified affine cousin of D_DrawSubdiv in d_polyse.c
// (which subdivides every 16 horizontal pixels for perspective).
// This version skips the subdivide -- straight per-pixel linear UV.
//
// Errors:
//
//	ErrTexFillNilFB      fb == nil
//	ErrTexFillNilTex     tex == nil
//	ErrTexFillFewVerts   len(verts) < 3
//	ErrTexFillManyVerts  len(verts) > MaxPolyVerts
//	ErrPicShape          tex.Width*tex.Height != len(tex.Pixels)
func FillTexturedPolygon(fb *FrameBuffer, tex *Pic, cm *ColorMap, lightLevel int, verts []TexturedVertex) error {
	if fb == nil {
		return ErrTexFillNilFB
	}
	if tex == nil {
		return ErrTexFillNilTex
	}
	if len(verts) < 3 {
		return ErrTexFillFewVerts
	}
	if len(verts) > MaxPolyVerts {
		return ErrTexFillManyVerts
	}
	if tex.Width*tex.Height != len(tex.Pixels) {
		return ErrPicShape
	}

	// Tile-wrap dims. Quake textures repeat across surfaces -- world-space
	// U/V here is in texture-pixel units (often thousands), so the
	// rasterizer must modulo into [0, W) / [0, H). Clamping (the previous
	// behaviour) sampled the rightmost column of the miptex on every
	// face = each surface filled with one palette byte = flat-shaded
	// polygons. tyrquake's R_DrawSpans uses & (W-1) since miptex dims
	// are power-of-two; we use a positive-modulo so test fixtures with
	// non-pow2 dims also wrap cleanly.
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
		// Per-edge crossing buffers: x position + interpolated u, v.
		var xs, us, vs [MaxPolyVerts]float32
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
				nXs++
			}
		}
		// Insertion-sort crossings by x; carry u, v with them.
		for i := 1; i < nXs; i++ {
			for j := i; j > 0 && xs[j-1] > xs[j]; j-- {
				xs[j-1], xs[j] = xs[j], xs[j-1]
				us[j-1], us[j] = us[j], us[j-1]
				vs[j-1], vs[j] = vs[j], vs[j-1]
			}
		}
		for pair := 0; pair+1 < nXs; pair += 2 {
			xLeft := xs[pair]
			xRight := xs[pair+1]
			uLeft, uRight := us[pair], us[pair+1]
			vLeft, vRight := vs[pair], vs[pair+1]

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

			// Linear UV step per output pixel. Span width is the
			// float distance between the edge crossings, NOT the
			// integer pixel count (so partial-pixel slivers don't
			// inflate the step).
			span := xRight - xLeft
			duDx := (uRight - uLeft) / span
			dvDx := (vRight - vLeft) / span

			row := y * fb.Pitch
			for x := x0; x <= x1; x++ {
				// Sample at pixel center: x + 0.5.
				xf := float32(x) + 0.5
				u := uLeft + (xf-xLeft)*duDx
				v := vLeft + (xf-xLeft)*dvDx
				ui := int(math.Floor(float64(u)))
				vi := int(math.Floor(float64(v)))
				// Quake textures TILE -- the world-space U/V here is in
				// texture-pixel units (often thousands), so clamping
				// would sample the rightmost column of the miptex on
				// every face = each surface fills with one palette byte
				// = flat-shaded polygons. Quake miptex dims are always
				// powers of two, so the cheap & (W-1) / & (H-1) wrap
				// matches tyrquake's R_DrawSpans behaviour byte-for-byte.
				ui = ((ui % texW) + texW) % texW
				vi = ((vi % texH) + texH) % texH
				texel := tex.Pixels[vi*tex.Width+ui]
				if cm != nil {
					texel = cm.LightIndex(lightLevel, texel)
				}
				fb.Pixels[row+x] = texel
			}
		}
	}
	return nil
}
