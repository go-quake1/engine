// Copyright (c) 1996-1997 Id Software, Inc.
// Copyright (c) 2026 the go-quake1/engine authors.
// SPDX-License-Identifier: GPL-2.0-or-later

package runner

import (
	"github.com/go-quake1/engine/bspfile"
	"github.com/go-quake1/engine/bsprender"
	"github.com/go-quake1/engine/render"
	"github.com/go-quake1/engine/worldview"
)

// worldSurfaces draws the lit world faces of the runner's frame from Quake's
// surface cache with mip levels, as worldview.DrawSoftware does (reference
// common/d_surf.c D_CacheSurface, r_surf.c R_DrawSurface, d_edge.c
// D_DrawSurface): the mip level of a face comes from its nearest 1/z
// (worldview.MipView), the lit surface from render.SurfaceCache, and the span
// drawer reads that surface with the face's own gradients.
//
// What the runner adds to the reference's fixed views: light styles at their
// real value (d_lightstylevalue, R_AnimateLight) and animated '+' textures,
// which are a changed texture for D_CacheSurface (the surface is rebuilt in
// place, its block kept).
type worldSurfaces struct {
	tex   []*render.SurfaceTexture // by miptex slot; nil for a slot that did not decode
	light []byte                   // the lighting lump
	faces map[int]*surfFace

	cache  *render.SurfaceCache
	cw, ch int
	mv     worldview.MipView
	env    render.SurfaceEnv

	cv [render.MaxPolyVerts]render.CachedVertex

	// Builds and Hits count what the cache did since the map was loaded.
	Builds, Hits, ThrashFrames int
}

// surfFace is the msurface_t of one world face: its cache spots and the
// geometry the frame needs.
type surfFace struct {
	sf   render.SurfaceFace
	info bsprender.LightmapInfo
	wf   worldview.Face // Verts and MipAdjust, for the mip choice
}

// newWorldSurfaces decodes the four mip levels of every miptex of the map.
func newWorldSurfaces(file *bspfile.File) (*worldSurfaces, error) {
	mtl, err := file.Textures()
	if err != nil {
		return nil, err
	}
	ws := &worldSurfaces{
		tex:   make([]*render.SurfaceTexture, mtl.NumMipTex),
		light: file.Lighting(),
		faces: make(map[int]*surfFace),
	}
	for i := range ws.tex {
		if mt, ok, err := mtl.MipTex(i); err == nil && ok {
			ws.tex[i], _ = worldview.NewSurfaceTexture(mt)
		}
	}
	ws.env.HaveLightData = len(ws.light) > 0
	return ws, nil
}

// beginFrame is the per-frame state of D_SetupFrame: the cache (made for the
// frame size) with its rover reset, and the mip view.
func (ws *worldSurfaces) beginFrame(width, height int, fovX float32) {
	if ws.cache == nil || ws.cw != width || ws.ch != height {
		ws.cache = render.NewSurfaceCache(render.SurfaceCacheForRes(width, height))
		ws.cw, ws.ch = width, height
		for _, f := range ws.faces {
			f.sf = render.SurfaceFace{Desc: f.sf.Desc} // the old blocks belong to the old arena
		}
		ws.mv = worldview.NewMipView(width, height, fovX)
	}
	ws.cache.SetupFrame()
	ws.cache.Thrash = false
}

// endFrame counts a frame in which the cache thrashed (r_cache_thrash).
func (ws *worldSurfaces) endFrame() {
	if ws.cache != nil && ws.cache.Thrash {
		ws.ThrashFrames++
	}
}

// face returns the surface of face idx, built on first use; nil when the face
// has no lightmap geometry or its miptex did not decode.
func (ws *worldSurfaces) face(bmFaceInfo bsprender.LightmapInfo, fv bsprender.FaceVerts, idx, mtIdx int) *surfFace {
	if f, ok := ws.faces[idx]; ok {
		return f
	}
	var st *render.SurfaceTexture
	if mtIdx >= 0 && mtIdx < len(ws.tex) {
		st = ws.tex[mtIdx]
	}
	if st == nil {
		ws.faces[idx] = nil
		return nil
	}
	desc, info, _ := worldview.NewSurfaceDesc(bmFaceInfo, ws.light, st)
	f := &surfFace{
		sf:   render.SurfaceFace{Desc: desc},
		info: info,
		wf:   worldview.Face{Verts: fv, MipAdjust: render.MipAdjust(fv.UVAxisS, fv.UVAxisT)},
	}
	ws.faces[idx] = f
	return f
}

// draw paints one lit face. info is the face's lightmap geometry
// (bsprender.FaceLightmapInfo), mtIdx its miptex slot and animIdx the slot its
// texture animation shows now (R_TextureAnimation); style maps a light style
// to d_lightstylevalue. It reports false when the face is not a surface the
// cache can draw (no decodable texture, a surface the cache cannot hold), so
// the caller can fall back.
func (ws *worldSurfaces) draw(fb *render.FrameBuffer, cm *render.ColorMap, view render.Affine, fovX float32,
	info bsprender.LightmapInfo, fv bsprender.FaceVerts, idx, mtIdx, animIdx int, style func(byte) int) (drawn bool) {

	f := ws.face(info, fv, idx, mtIdx)
	if f == nil {
		return false
	}
	st := ws.tex[mtIdx]
	if animIdx >= 0 && animIdx < len(ws.tex) && ws.tex[animIdx] != nil {
		st = ws.tex[animIdx]
	}
	verts, err := bsprender.TransformFaceLightmapped(view, fb, fovX, fv, f.info)
	if err != nil {
		return true // behind the camera, or not a polygon the filler takes: nothing to draw
	}
	mip := ws.mv.Level(view, &f.wf)
	var adj [4]int
	for i, s := range f.sf.Desc.Styles {
		adj[i] = style(s)
	}
	f.sf.Desc.Texture = st // an animated texture is a changed texture to D_CacheSurface
	ws.env.Colormap = cm
	had := f.sf.Spot(mip)
	hit := had != nil && !had.Dlight && had.Texture == st && had.LightAdj == adj // D_CacheSurface's reuse test
	blk, err := ws.cache.Cache(&f.sf, mip, adj, &ws.env)
	if err != nil {
		return false
	}
	if hit {
		ws.Hits++
	} else {
		ws.Builds++
	}
	sw, sh := f.sf.Desc.Extents[0]>>mip, f.sf.Desc.Extents[1]>>mip
	if sw <= 0 || sh <= 0 {
		return true
	}
	scale := 1 / float32(int(1)<<mip)
	c := ws.cv[:len(verts)]
	for i, lv := range verts {
		c[i] = render.CachedVertex{X: lv.X, Y: lv.Y, Z: lv.Z, CU: lv.LmS * 16 * scale, CV: lv.LmT * 16 * scale}
	}
	_ = render.FillPerspectiveCachedPolygon(fb, &render.CachedSurface{W: sw, H: sh, Pixels: blk.Data}, c)
	return true
}
