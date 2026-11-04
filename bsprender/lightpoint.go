// Copyright (c) 1996-1997 Id Software, Inc.
// Copyright (c) 2026 the go-quake1/engine authors.
// SPDX-License-Identifier: GPL-2.0-or-later

package bsprender

import (
	"errors"
	"math"
	"strings"

	"github.com/go-quake1/engine/bspfile"
	"github.com/go-quake1/engine/model"
)

// This file ports R_LightPoint (r_main.c:754) with the sampling it calls,
// R_LightSurfPoint, RecursiveLightPoint and R_FillLightPoint (r_light.c:137
// to 252), and the surface setup they read from Mod_LoadFaces
// (CalcSurfaceExtents, Mod_ProcessSurface, model.c:1138 and 1231): the light
// of an entity is the lightmap under it, found by tracing a line straight down
// from its origin to the first lit surface, sampled bilinearly over 2x2
// lightmap samples with the light styles added.

// ErrLightNilModel is returned by [NewLightSampler] for a nil model.
var ErrLightNilModel = errors.New("bsprender: nil BrushModel passed to NewLightSampler")

type lightFace struct {
	tiled        bool  // SURF_DRAWTILED: no lightmap
	minS, minT   int32 // texturemins
	extS, extT   int32 // extents
	samples      int32 // offset into the lighting lump, -1 for none
	styles       [bspfile.MaxLightmaps]byte
	vecs         [2][4]float32
	hasTexinfo   bool
	width, depth int32 // (extents >> 4) + 1
}

// LightSampler holds what R_LightPoint reads of a brush model.
type LightSampler struct {
	bm       *model.BrushModel
	planes   []bspfile.Plane
	faces    []lightFace
	lighting []byte
}

// NewLightSampler prepares a model for [LightSampler.LightPoint].
func NewLightSampler(bm *model.BrushModel) (*LightSampler, error) {
	if bm == nil {
		return nil, ErrLightNilModel
	}
	f := bm.File
	planes, err := f.Planes()
	if err != nil {
		return nil, err
	}
	faces, err := f.Faces()
	if err != nil {
		return nil, err
	}
	texinfos, err := f.TexInfos()
	if err != nil {
		return nil, err
	}
	surfedges, err := f.Surfedges()
	if err != nil {
		return nil, err
	}
	edges, err := f.Edges()
	if err != nil {
		return nil, err
	}
	verts, err := f.Vertexes()
	if err != nil {
		return nil, err
	}
	texs, err := f.Textures()
	if err != nil {
		return nil, err
	}
	ls := &LightSampler{bm: bm, planes: planes, lighting: f.Lighting(), faces: make([]lightFace, len(faces))}
	for i, face := range faces {
		lf := &ls.faces[i]
		lf.samples = face.LightOfs
		lf.styles = face.Styles
		if int(face.TexInfo) < 0 || int(face.TexInfo) >= len(texinfos) || face.NumEdges <= 0 {
			lf.tiled = true
			continue
		}
		ti := texinfos[face.TexInfo]
		lf.vecs = ti.Vecs
		lf.hasTexinfo = true
		// Mod_ProcessSurface: TEX_SPECIAL, sky and '*' textures are DRAWTILED;
		// a missing texture is r_notexture_mip with TEX_SPECIAL.
		if ti.Flags&1 != 0 {
			lf.tiled = true
		}
		if mt, ok, err := texs.MipTex(int(ti.MipTex)); err != nil || !ok {
			lf.tiled = true
		} else if strings.HasPrefix(mt.Name, "sky") || strings.HasPrefix(mt.Name, "*") {
			lf.tiled = true
		}
		if lf.tiled {
			continue
		}
		// CalcSurfaceExtents; the (long double) accumulation becomes float64.
		var mins, maxs [2]float64
		mins = [2]float64{math.MaxFloat32, math.MaxFloat32}
		maxs = [2]float64{-math.MaxFloat32, -math.MaxFloat32}
		for k := 0; k < int(face.NumEdges); k++ {
			se := int32(surfedges[int(face.FirstEdge)+k])
			var v bspfile.Vertex
			if se >= 0 {
				v = verts[edges[se].V0]
			} else {
				v = verts[edges[-se].V1]
			}
			for j := 0; j < 2; j++ {
				val := float64(v.X)*float64(ti.Vecs[j][0]) + float64(v.Y)*float64(ti.Vecs[j][1]) +
					float64(v.Z)*float64(ti.Vecs[j][2]) + float64(ti.Vecs[j][3])
				val = float64(float32(val)) // stored into a float
				if val < mins[j] {
					mins[j] = val
				}
				if val > maxs[j] {
					maxs[j] = val
				}
			}
		}
		var bmin, bmax [2]int32
		for j := 0; j < 2; j++ {
			bmin[j] = int32(math.Floor(float64(float32(mins[j]) / 16)))
			bmax[j] = int32(math.Ceil(float64(float32(maxs[j]) / 16)))
		}
		lf.minS, lf.minT = bmin[0]*16, bmin[1]*16
		lf.extS, lf.extT = (bmax[0]-bmin[0])*16, (bmax[1]-bmin[1])*16
		lf.width, lf.depth = lf.extS>>4+1, lf.extT>>4+1
	}
	return ls, nil
}

// LightStyleFunc gives d_lightstylevalue[style]: 256 is normal light.
type LightStyleFunc func(style byte) int

// NormalLightStyle is d_lightstylevalue for a map whose styles are all 'm'
// (264 = ('m' - 'a') * 22), the value an unanimated style has once the
// server has sent lightstyle 0.
func NormalLightStyle(byte) int { return 264 }

type lightPoint struct {
	face int
	s, t float32
}

// LightPointOr is [LightSampler.LightPoint], or fallback when ls is nil (a
// scene without a real map, which has no lightmaps to sample).
func (ls *LightSampler) LightPointOr(point [3]float32, styles LightStyleFunc, fallback int) int {
	if ls == nil {
		return fallback
	}
	return ls.LightPoint(point, styles)
}

// LightPoint is R_LightPoint: the light (0..255, possibly more with
// overbright styles) at point, 255 when the map has no lighting lump, 0 when
// no lit surface is below. styles is d_lightstylevalue.
func (ls *LightSampler) LightPoint(point [3]float32, styles LightStyleFunc) int {
	if len(ls.lighting) == 0 {
		return 255
	}
	lp, ok := ls.surfPoint(point)
	if !ok {
		return 0
	}
	f := &ls.faces[lp.face]
	surfwidth := int(f.width)
	surfheight := int(f.depth)
	ds := min(int(math.Floor(float64(lp.s))), surfwidth-2)
	dt := min(int(math.Floor(float64(lp.t))), surfheight-2)
	surfbytes := surfwidth * surfheight
	base := int(f.samples) + dt*surfwidth + ds
	var samples [2][2]float32
	for m := 0; m < bspfile.MaxLightmaps && f.styles[m] != 255; m++ {
		scale := float32(styles(f.styles[m])) * (1.0 / 256.0)
		r0 := base + m*surfbytes
		r1 := r0 + surfwidth
		samples[0][0] += float32(ls.sample(r0)) * scale
		samples[0][1] += float32(ls.sample(r0+1)) * scale
		samples[1][0] += float32(ls.sample(r1)) * scale
		samples[1][1] += float32(ls.sample(r1+1)) * scale
	}
	dsfrac := lp.s - float32(ds)
	dtfrac := lp.t - float32(dt)
	w00 := (1.0 - dsfrac) * (1.0 - dtfrac)
	w01 := dsfrac * (1.0 - dtfrac)
	w10 := (1.0 - dsfrac) * dtfrac
	w11 := dsfrac * dtfrac
	// explicit conversions keep each product rounded to float32 (no fusing)
	sum := float32(samples[0][0]*w00) + float32(samples[0][1]*w01)
	sum += float32(samples[1][0] * w10)
	sum += float32(samples[1][1] * w11)
	return int(math.Floor(float64(sum)))
}

// surfPoint is R_LightSurfPoint: the first lit surface below point.
func (ls *LightSampler) surfPoint(point [3]float32) (lightPoint, bool) {
	end := [3]float32{point[0], point[1], point[2] - (8192 + 2)}
	var lp lightPoint
	if ls.bm.NumNodes() == 0 || !ls.recurse(0, point, end, &lp) {
		return lp, false
	}
	return lp, true
}

// SurfPoint is R_LightSurfPoint: the index of the face the line straight
// down from point meets first among the faces with a lightmap, and the
// lightmap coordinates (s, t) of the hit.
func (ls *LightSampler) SurfPoint(point [3]float32) (face int, s, t float32, ok bool) {
	lp, ok := ls.surfPoint(point)
	return lp.face, lp.s, lp.t, ok
}

// sample reads one lighting byte; out of range reads as 0 (C reads whatever
// follows the lump; a valid map never gets there).
func (ls *LightSampler) sample(i int) byte {
	if i < 0 || i >= len(ls.lighting) {
		return 0
	}
	return ls.lighting[i]
}

// recurse is RecursiveLightPoint over node index n.
func (ls *LightSampler) recurse(n int, start, end [3]float32, lp *lightPoint) bool {
	for {
		node := ls.bm.Node(n)
		pn := int(node.PlaneNum)
		if pn < 0 || pn >= len(ls.planes) {
			return false
		}
		plane := &ls.planes[pn]
		var front, back float32
		switch plane.Type {
		case bspfile.PlaneX, bspfile.PlaneX + 1, bspfile.PlaneX + 2:
			a := int(plane.Type)
			front = start[a] - plane.Dist
			back = end[a] - plane.Dist
		default:
			front = float32(start[0]*plane.Normal[0]) + float32(start[1]*plane.Normal[1]) + float32(start[2]*plane.Normal[2]) - plane.Dist
			back = float32(end[0]*plane.Normal[0]) + float32(end[1]*plane.Normal[1]) + float32(end[2]*plane.Normal[2]) - plane.Dist
		}
		side := 0
		if front < 0 {
			side = 1
		}
		backSide := 0
		if back < 0 {
			backSide = 1
		}
		if backSide == side {
			// completely on one side
			c := node.Children[side]
			if c < 0 {
				return false // a leaf: contents < 0
			}
			n = int(c)
			continue
		}
		frac := front / (front - back)
		var sp [3]float32
		for i := 0; i < 3; i++ {
			sp[i] = start[i] + float32((end[i]-start[i])*frac)
		}
		// down the front side
		if c := node.Children[side]; c >= 0 && ls.recurse(int(c), start, sp, lp) {
			return true
		}
		if ls.fill(node, sp, lp) {
			return true
		}
		c := node.Children[1-side]
		if c < 0 {
			return false
		}
		start = sp
		n = int(c)
	}
}

// fill is R_FillLightPoint.
func (ls *LightSampler) fill(node *model.Node, sp [3]float32, lp *lightPoint) bool {
	for i := 0; i < int(node.NumFaces); i++ {
		fi := int(node.FirstFace) + i
		if fi >= len(ls.faces) {
			return false
		}
		f := &ls.faces[fi]
		if f.tiled || !f.hasTexinfo {
			continue
		}
		s := (float32(sp[0]*f.vecs[0][0]) + float32(sp[1]*f.vecs[0][1]) + float32(sp[2]*f.vecs[0][2]) + f.vecs[0][3] - float32(f.minS)) * 0.0625
		t := (float32(sp[0]*f.vecs[1][0]) + float32(sp[1]*f.vecs[1][1]) + float32(sp[2]*f.vecs[1][2]) + f.vecs[1][3] - float32(f.minT)) * 0.0625
		ds := int32(math.Floor(float64(s)))
		dt := int32(math.Floor(float64(t)))
		if ds < 0 || dt < 0 {
			continue
		}
		if ds > f.extS>>4 || dt > f.extT>>4 {
			continue
		}
		if f.samples < 0 {
			return false
		}
		lp.face, lp.s, lp.t = fi, s, t
		return true
	}
	return false
}
