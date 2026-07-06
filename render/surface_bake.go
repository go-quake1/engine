// Copyright (c) 1996-1997 Id Software, Inc.
// Copyright (c) 2026 the go-quake1/engine authors.
// SPDX-License-Identifier: GPL-2.0-or-later

package render

import (
	"errors"
	"math"
)

// Sentinel errors returned by BakeSurface.
var (
	ErrBakeNilDst    = errors.New("render: nil destination in BakeSurface")
	ErrBakeNilTex    = errors.New("render: nil texture in BakeSurface")
	ErrBakeNilCM     = errors.New("render: nil colormap in BakeSurface")
	ErrBakeTexShape  = errors.New("render: BakeSurface texture W*H != len(Pixels)")
	ErrBakePlaneDims = errors.New("render: BakeSurface lmW/lmH invalid or mismatch len(plane)")
)

// BakeSurface fills dst with a face's SURFACE CACHE: for every baked texel it
// samples the texture (tile-wrapped) and the lightmap plane (bilinear), maps
// the lightmap sample to a colormap row, and stores the final palette index
// cm.LightIndex(row, texel). Afterwards FillPerspectiveCachedPolygon can paint
// the face with a single fetch + store per pixel.
//
// The baked surface is (lmW-1)*16+1 by (lmH-1)*16+1 texels -- texture
// resolution over the face's lit extent -- so baked texel (cx, cy) is the
// face's surface coordinate (LmS*16, LmT*16) == (cx, cy). texMinS / texMinT are
// the face's texturemins (the surface->texture origin), so the texture pixel at
// (cx, cy) is (texMinS+cx, texMinT+cy) wrapped into the texture.
//
// dst.Pixels is grown/reused as needed (0 allocs on a warm dst of the right
// size). The per-texel lightmap bilinear + colormap here mirror
// FillPerspectiveLightmappedPolygon exactly, so the cached image matches the
// per-pixel path.
//
// Returns ErrBakeNilDst / NilTex / NilCM, ErrBakeTexShape, or ErrBakePlaneDims.
func BakeSurface(dst *CachedSurface, tex *Pic, cm *ColorMap, plane []byte, lmW, lmH, texMinS, texMinT int) error {
	if dst == nil {
		return ErrBakeNilDst
	}
	if tex == nil {
		return ErrBakeNilTex
	}
	if cm == nil {
		return ErrBakeNilCM
	}
	if tex.Width <= 0 || tex.Height <= 0 || tex.Width*tex.Height != len(tex.Pixels) {
		return ErrBakeTexShape
	}
	if lmW <= 0 || lmH <= 0 || lmW*lmH != len(plane) {
		return ErrBakePlaneDims
	}

	texW := tex.Width
	texH := tex.Height
	lmMaxS := lmW - 1
	lmMaxT := lmH - 1

	W := lmMaxS*16 + 1
	H := lmMaxT*16 + 1
	dst.W = W
	dst.H = H
	if cap(dst.Pixels) < W*H {
		dst.Pixels = make([]byte, W*H)
	} else {
		dst.Pixels = dst.Pixels[:W*H]
	}

	for cy := 0; cy < H; cy++ {
		// Lightmap T coord for this row + its bilinear neighbours. cy is in
		// [0, lmMaxT*16], so ltf = cy/16 is in [0, lmMaxT] exactly (16 is a
		// power of two) -- no top clamp needed; ti1 handles the last cell.
		ltf := float64(cy) / 16
		ti0 := int(math.Floor(ltf))
		ti1 := ti0 + 1
		if ti1 > lmMaxT {
			ti1 = lmMaxT
		}
		ft := ltf - float64(ti0)

		vTex := ((texMinT+cy)%texH + texH) % texH
		texRow := vTex * texW
		dstRow := cy * W

		for cx := 0; cx < W; cx++ {
			lsf := float64(cx) / 16 // in [0, lmMaxS] exactly; see cy note
			si0 := int(math.Floor(lsf))
			si1 := si0 + 1
			if si1 > lmMaxS {
				si1 = lmMaxS
			}
			fs := lsf - float64(si0)

			l00 := float64(plane[ti0*lmW+si0])
			l10 := float64(plane[ti0*lmW+si1])
			l01 := float64(plane[ti1*lmW+si0])
			l11 := float64(plane[ti1*lmW+si1])
			l0 := l00 + fs*(l10-l00)
			l1 := l01 + fs*(l11-l01)
			sample := l0 + ft*(l1-l0)
			cmRow := (255 - int(sample)) >> 2

			uTex := ((texMinS+cx)%texW + texW) % texW
			texel := tex.Pixels[texRow+uTex]
			dst.Pixels[dstRow+cx] = cm.LightIndex(cmRow, texel)
		}
	}
	return nil
}
