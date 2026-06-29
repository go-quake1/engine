// Copyright (c) 1996-1997 Id Software, Inc.
// Copyright (c) 2026 the go-quake1/engine authors.
// SPDX-License-Identifier: GPL-2.0-or-later

package bsprender

import (
	"errors"
	"math"

	"github.com/go-quake1/engine/bspfile"
	"github.com/go-quake1/engine/model"
	"github.com/go-quake1/engine/render"
)

// LightmapInfo carries the per-face metadata the runner needs to
// build a per-surface lightmap plane + the rasterizer needs to map
// pixel-space lightmap coords back into the plane's index space.
//
// Per-face lightmap geometry:
//
//   - The face's vertices project into texinfo-S/T space via the
//     same dot+offset the texture sampler uses.
//   - The per-face range of S, T is computed (Smin..Smax, Tmin..Tmax).
//   - MinS = floor(Smin/16) * 16, MaxS = ceil(Smax/16) * 16; same T.
//     (Quake stores texturemins as integer multiples of 16; lightmap
//     samples are 1 per 16 S-units.)
//   - Width = (MaxS - MinS) / 16 + 1; same Height.
//
// LightOfs is the byte offset into bspfile.File.Lighting() at which
// the face's first lightmap layer starts. Styles[0..3] index the
// per-face lightstyle channels; values of 255 mark unused channels.
// The runner stacks up to MaxLightmaps layers (one per active style)
// each Width*Height bytes long, beginning at LightOfs.
//
// LightOfs == -1 means "no static lighting for this face" (sky,
// turbulent, missing lightdata). Callers MUST treat that as
// "full bright" or skip the lightmapped path entirely.
type LightmapInfo struct {
	Width, Height int
	MinS, MinT    int     // -texturemins; subtracted to bring LmS/LmT into plane index space
	LightOfs      int32   // byte offset into Lighting() lump; -1 if absent
	Styles        [4]byte // per-style channel ids; 255 = unused
}

// Sentinel errors specific to the lightmap path.
var (
	ErrLmFaceNilModel = errors.New("bsprender: nil BrushModel passed to FaceLightmapInfo")
	ErrLmFaceIdxRange = errors.New("bsprender: face index out of [0, NumFaces)")
)

// FaceLightmapInfo computes the lightmap geometry + offset for the
// face at faceIdx. Mirrors tyrquake's CalcSurfaceExtents +
// Mod_LoadFaces lightmap setup.
//
// Returns ErrLmFaceNilModel on nil bm, ErrLmFaceIdxRange on a bad
// faceIdx, or any underlying lump-decode error.
//
// On success, the returned LightmapInfo is safe to use:
//   - Width, Height >= 1
//   - LightOfs == -1 if the face has no static lighting data
//   - Styles is copied verbatim from the bspfile Face record
func FaceLightmapInfo(bm *model.BrushModel, faceIdx int) (LightmapInfo, error) {
	if bm == nil {
		return LightmapInfo{}, ErrLmFaceNilModel
	}
	faces, err := bm.File.Faces()
	if err != nil {
		return LightmapInfo{}, err
	}
	if faceIdx < 0 || faceIdx >= len(faces) {
		return LightmapInfo{}, ErrLmFaceIdxRange
	}
	face := faces[faceIdx]

	texinfos, err := bm.File.TexInfos()
	if err != nil {
		return LightmapInfo{}, err
	}
	surfedges, err := bm.File.Surfedges()
	if err != nil {
		return LightmapInfo{}, err
	}
	edges, err := bm.File.Edges()
	if err != nil {
		return LightmapInfo{}, err
	}
	verts, err := bm.File.Vertexes()
	if err != nil {
		return LightmapInfo{}, err
	}

	// Out-of-range texinfo: treat as single-sample minimal plane so
	// the rasterizer's later len(lightmap)==Width*Height check still
	// works. tyrquake's load-time validator already rejected the
	// BSP; this is the runtime backstop.
	if int(face.TexInfo) < 0 || int(face.TexInfo) >= len(texinfos) {
		info := LightmapInfo{Width: 1, Height: 1, LightOfs: face.LightOfs}
		copy(info.Styles[:], face.Styles[:])
		return info, nil
	}
	ti := texinfos[face.TexInfo]

	first := int(face.FirstEdge)
	n := int(face.NumEdges)
	if n <= 0 {
		info := LightmapInfo{Width: 1, Height: 1, LightOfs: face.LightOfs}
		copy(info.Styles[:], face.Styles[:])
		return info, nil
	}

	// Pre-fetch the first vertex so the min/max scan has a seed.
	resolve := func(i int) [3]float32 {
		se := int32(surfedges[first+i])
		var v bspfile.Vertex
		if se >= 0 {
			e := edges[se]
			v = verts[e.V0]
		} else {
			e := edges[-se]
			v = verts[e.V1]
		}
		return [3]float32{v.X, v.Y, v.Z}
	}
	v0 := resolve(0)
	s0 := v0[0]*ti.Vecs[0][0] + v0[1]*ti.Vecs[0][1] + v0[2]*ti.Vecs[0][2] + ti.Vecs[0][3]
	t0 := v0[0]*ti.Vecs[1][0] + v0[1]*ti.Vecs[1][1] + v0[2]*ti.Vecs[1][2] + ti.Vecs[1][3]
	sMin, sMax := s0, s0
	tMin, tMax := t0, t0
	for i := 1; i < n; i++ {
		v := resolve(i)
		s := v[0]*ti.Vecs[0][0] + v[1]*ti.Vecs[0][1] + v[2]*ti.Vecs[0][2] + ti.Vecs[0][3]
		t := v[0]*ti.Vecs[1][0] + v[1]*ti.Vecs[1][1] + v[2]*ti.Vecs[1][2] + ti.Vecs[1][3]
		if s < sMin {
			sMin = s
		}
		if s > sMax {
			sMax = s
		}
		if t < tMin {
			tMin = t
		}
		if t > tMax {
			tMax = t
		}
	}

	// bmins = floor(min / 16); bmaxs = ceil(max / 16).
	bminS := int(math.Floor(float64(sMin) / 16))
	bmaxS := int(math.Ceil(float64(sMax) / 16))
	bminT := int(math.Floor(float64(tMin) / 16))
	bmaxT := int(math.Ceil(float64(tMax) / 16))

	// texturemins = bmins * 16 (the BSP-load formula); the rasterizer
	// will subtract them on entry to bring LmS into plane index space.
	info := LightmapInfo{
		Width:    bmaxS - bminS + 1,
		Height:   bmaxT - bminT + 1,
		MinS:     bminS * 16,
		MinT:     bminT * 16,
		LightOfs: face.LightOfs,
	}
	copy(info.Styles[:], face.Styles[:])
	// Width/Height are always >= 1: bmaxS - bminS is the ceil(max/16) -
	// floor(min/16) of a real-valued range over >= 1 vertex (the n <= 0
	// early-return above already drained that case), so the
	// (max - min + 1) is at least 1. No defensive clamp needed.
	return info, nil
}

// TransformFaceLightmapped is the lightmap-emitting sibling of
// TransformFacePerspective. Same screen-space projection + UV math
// (texture-pixel coords); additionally emits per-vertex (LmS, LmT) in
// lightmap-sample units, already offset by lmInfo.MinS / MinT so the
// rasterizer can sample the per-face plane directly.
//
// Errors mirror TransformFace (TooFewVerts / TooManyVerts /
// BehindCamera). Caller is responsible for providing a valid lmInfo
// (typically obtained from FaceLightmapInfo on the same faceIdx).
func TransformFaceLightmapped(
	view render.Affine,
	fb *render.FrameBuffer,
	fovX float32,
	faceVerts FaceVerts,
	lmInfo LightmapInfo,
) ([]render.LightmappedVertex, error) {
	if faceVerts.NumVerts < 3 {
		return nil, ErrFaceTooFewVerts
	}
	if faceVerts.NumVerts > render.MaxPolyVerts {
		return nil, ErrFaceTooManyVerts
	}

	type viewVert struct {
		world [3]float32
		view  [3]float32
	}
	vs := make([]viewVert, faceVerts.NumVerts)
	anyInFront := false
	for i := 0; i < faceVerts.NumVerts; i++ {
		w := faceVerts.Vert(i)
		vp := render.TransformAffine(view, w)
		vs[i].world = w
		vs[i].view = vp
		if vp[2] >= render.ParticleNearClip {
			anyInFront = true
		}
	}
	if !anyInFront {
		return nil, ErrFaceBehindCamera
	}

	const deg2rad = math.Pi / 180
	tanHalfX := float32(math.Tan(float64(fovX/2) * deg2rad))
	if tanHalfX <= 0 {
		tanHalfX = 1
	}
	halfW := float32(fb.Width) / 2
	halfH := float32(fb.Height) / 2
	scale := halfW / tanHalfX

	minS := float32(lmInfo.MinS)
	minT := float32(lmInfo.MinT)

	out := make([]render.LightmappedVertex, faceVerts.NumVerts)
	for i := 0; i < faceVerts.NumVerts; i++ {
		w := vs[i].world
		vp := vs[i].view
		depth := vp[2]
		if depth < render.ParticleNearClip {
			depth = render.ParticleNearClip
		}
		invZ := 1 / depth
		sx := halfW + vp[0]*scale*invZ
		sy := halfH - vp[1]*scale*invZ

		u := w[0]*faceVerts.UVAxisS[0] + w[1]*faceVerts.UVAxisS[1] + w[2]*faceVerts.UVAxisS[2] + faceVerts.UVOffS
		vv := w[0]*faceVerts.UVAxisT[0] + w[1]*faceVerts.UVAxisT[1] + w[2]*faceVerts.UVAxisT[2] + faceVerts.UVOffT

		// Lightmap coords: same S/T as the texture, but in
		// lightmap-sample units (1 per 16 S-units) with the face's
		// origin (texturemins) subtracted. The +0.5 centers the
		// sample on the lightmap grid cell.
		lmS := (u - minS) / 16
		lmT := (vv - minT) / 16

		out[i] = render.LightmappedVertex{
			X:   sx,
			Y:   sy,
			Z:   depth,
			U:   u,
			V:   vv,
			LmS: lmS,
			LmT: lmT,
		}
	}
	return out, nil
}
