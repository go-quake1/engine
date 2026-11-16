// Copyright (c) 1996-1997 Id Software, Inc.
// Copyright (c) 2026 the go-quake1/engine authors.
// SPDX-License-Identifier: GPL-2.0-or-later

package worldview

import (
	"github.com/go-quake1/engine/bspfile"
	"github.com/go-quake1/engine/bsprender"
	"github.com/go-quake1/engine/render"
)

// NewSurfaceTexture is the texture_t that R_DrawSurface reads for a miptex: its
// size and its four mip levels (copied). ok is false when level 0 cannot be
// decoded.
func NewSurfaceTexture(mt *bspfile.MipTex) (st *render.SurfaceTexture, ok bool) {
	if mt == nil {
		return nil, false
	}
	if _, err := mt.Pixels(0); err != nil {
		return nil, false
	}
	st = &render.SurfaceTexture{Width: int(mt.Width), Height: int(mt.Height)}
	for k := 0; k < render.SurfMips; k++ {
		if mp, err := mt.Pixels(k); err == nil {
			st.Mips[k] = append([]byte(nil), mp...)
		}
	}
	return st, true
}

// SetSurfaceGeometry sets what R_AddDynamicLights reads of a face besides its
// lightmap: surf->plane (the face's plane, from its vertices) and
// surf->texinfo->vecs.
func SetSurfaceGeometry(desc *render.SurfaceDesc, fv bsprender.FaceVerts, info bsprender.LightmapInfo) {
	pl := bsprender.SurfacePlaneOf(fv, info)
	desc.Plane = render.Plane{Normal: pl.Normal, Dist: pl.Dist}
	desc.TexVecs = [2][4]float32{
		{pl.AxisS[0], pl.AxisS[1], pl.AxisS[2], pl.OffS},
		{pl.AxisT[0], pl.AxisT[1], pl.AxisT[2], pl.OffT},
	}
}

// NewSurfaceDesc is the msurface_t of a lit face as the surface cache reads it
// (Mod_LoadFaces: extents, texturemins, styles, samples), from the face's
// lightmap geometry and the lighting lump. The returned info is the one to
// transform the face with: a face without a lightmap (or whose samples lie
// outside the lump) has its lightmap shrunk to 1 x 1 and no samples.
// plane is the face's first lightmap layer (255 for no lightmap), the data of
// the per-pixel lightmapped fill.
func NewSurfaceDesc(info bsprender.LightmapInfo, light []byte, tex *render.SurfaceTexture) (desc render.SurfaceDesc, outInfo bsprender.LightmapInfo, plane []byte) {
	plane = []byte{255}
	desc = render.SurfaceDesc{
		Extents:     [2]int{(info.Width - 1) * 16, (info.Height - 1) * 16},
		TextureMins: [2]int{info.MinS, info.MinT},
		Styles:      info.Styles,
		Texture:     tex,
	}
	n := info.Width * info.Height
	if info.LightOfs >= 0 && int(info.LightOfs)+n <= len(light) {
		plane = append([]byte(nil), light[info.LightOfs:int(info.LightOfs)+n]...)
		styles := 0
		for styles < 4 && info.Styles[styles] != 255 {
			styles++
		}
		if int(info.LightOfs)+styles*n <= len(light) {
			desc.Samples = append([]byte(nil), light[info.LightOfs:int(info.LightOfs)+styles*n]...)
		}
	} else {
		info.Width, info.Height = 1, 1
	}
	return desc, info, plane
}
