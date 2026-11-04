// Copyright (c) 1996-1997 Id Software, Inc.
// Copyright (c) 2026 the go-quake1/engine authors.
// SPDX-License-Identifier: GPL-2.0-or-later

// Package worldview is the BSP world of one map as the renderers draw it:
// the faces the PVS and the BSP walk select for a view, each with its
// base texture and its static lightmap, and the software reference
// renderer for exactly that content (world only: no models, particles,
// sprites or HUD; sky and water faces are skipped, they are a later
// step). The GPU renderer draws the same faces and its output is
// checked against [World.DrawSoftware].
//
// Lighting is the one GLQuake and the software engine share: each face's
// lightmap (style 0, one sample per 16 texture units) is sampled
// bilinearly, turned into a colormap row, and the texel index goes
// through that row of the colormap (render.FillPerspectiveLightmappedPolygon).
package worldview

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"strings"

	"github.com/go-quake1/engine/bspfile"
	"github.com/go-quake1/engine/bsprender"
	"github.com/go-quake1/engine/model"
	"github.com/go-quake1/engine/render"
)

// ClearIndex is the palette index the frame is cleared to before the
// world is drawn (the engine's sky-like clear).
const ClearIndex = 0x10

// DefaultFOV is the horizontal field of view of the views, in degrees.
const DefaultFOV = 90

// View is a camera: origin and (pitch, yaw, roll) in degrees.
type View struct {
	Origin [3]float32
	Angles [3]float32
}

// Face is one drawable BSP face with its texture and lightmap.
type Face struct {
	Idx   int
	Tex   *render.Pic
	Verts bsprender.FaceVerts
	Info  bsprender.LightmapInfo
	// Light is the style-0 lightmap plane, Info.Width x Info.Height
	// bytes; a face without static lighting has the 1x1 plane {255}
	// (full bright).
	Light []byte
}

// ErrNoMap is returned by [LoadMap] when the pak has no such map.
var ErrNoMap = errors.New("worldview: map not in the pak")

// World is a loaded map.
type World struct {
	BM    *model.BrushModel
	File  *bspfile.File
	Faces []*Face // by face index; nil for faces that are not drawn
	FOV   float32

	// Lights is R_LightPoint's sampler over the map's lightmaps.
	Lights *bsprender.LightSampler

	// Special is the sky and water faces by face index (nil elsewhere).
	// They are not in Faces and not in [World.Visible]'s result; the ones
	// the last Visible call selected are in [World.VisibleSpecial].
	Special    []*SpecialFace
	visSpecial []int
	sky        render.SkyCache

	walk     bsprender.WalkContext
	mark     bsprender.MarkContext
	surfaces bsprender.SurfaceList
	frame    int32
	rd       render.RefDef
}

type readerAt []byte

func (r readerAt) ReadAt(p []byte, off int64) (int, error) {
	if off < 0 || off >= int64(len(r)) {
		return 0, io.EOF
	}
	n := copy(p, r[off:])
	if n < len(p) {
		return n, io.EOF
	}
	return n, nil
}

// LoadMap loads maps/<slug>.bsp from the pak.
func LoadMap(pakFS fs.FS, slug string) (*World, error) {
	f, err := pakFS.Open("maps/" + slug + ".bsp")
	if err != nil {
		return nil, fmt.Errorf("%w: %s: %v", ErrNoMap, slug, err)
	}
	defer f.Close()
	b, err := io.ReadAll(f)
	if err != nil {
		return nil, err
	}
	return New(b)
}

// New loads a BSP29 file.
func New(bsp []byte) (*World, error) {
	file, err := bspfile.Open(readerAt(bsp), int64(len(bsp)))
	if err != nil {
		return nil, fmt.Errorf("worldview: %w", err)
	}
	bm, err := model.LoadBrush(file, 0)
	if err != nil {
		return nil, fmt.Errorf("worldview: %w", err)
	}
	nf, err := file.Faces()
	if err != nil {
		return nil, fmt.Errorf("worldview: %w", err)
	}
	mtl, err := file.Textures()
	if err != nil {
		return nil, fmt.Errorf("worldview: %w", err)
	}
	pics := make([]*render.Pic, mtl.NumMipTex)
	names := make([]string, mtl.NumMipTex)
	for i := range pics {
		mt, ok, err := mtl.MipTex(i)
		if err != nil || !ok || mt == nil {
			continue
		}
		px, err := mt.Pixels(0)
		if err != nil {
			continue
		}
		pics[i] = &render.Pic{Width: int(mt.Width), Height: int(mt.Height), Pixels: append([]byte(nil), px...)}
		names[i] = mt.Name
	}
	light := file.Lighting()
	special := make([]*SpecialFace, len(nf))
	w := &World{BM: bm, File: file, FOV: DefaultFOV, Faces: make([]*Face, len(nf)), Special: special}
	for i := range nf {
		mi, ok, _ := bm.FaceMipTexIdx(i)
		if !ok || mi < 0 || mi >= len(pics) || pics[mi] == nil {
			continue
		}
		if sky, turb := strings.HasPrefix(names[mi], "sky"), strings.HasPrefix(names[mi], "*"); sky || turb {
			// Sky and water have no lightmap and their own fill: they are
			// kept apart from the lit faces (see [SpecialFace]).
			if fv, err := bsprender.NewBrushFaceVerts(bm, i); err == nil {
				kind := KindSky
				if turb {
					kind = KindTurb
				}
				sf := &SpecialFace{Idx: i, Kind: kind, Tex: pics[mi], Verts: fv, Name: names[mi]}
				if info, err := bsprender.FaceLightmapInfo(bm, i); err == nil {
					sf.Plane = surfacePlane(fv, info)
				}
				special[i] = sf
			}
			continue
		}
		fv, err := bsprender.NewBrushFaceVerts(bm, i)
		if err != nil {
			continue
		}
		info, err := bsprender.FaceLightmapInfo(bm, i)
		if err != nil {
			continue
		}
		plane := []byte{255}
		if n := info.Width * info.Height; info.LightOfs >= 0 && int(info.LightOfs)+n <= len(light) {
			plane = append([]byte(nil), light[info.LightOfs:int(info.LightOfs)+n]...)
		} else {
			info.Width, info.Height = 1, 1
		}
		w.Faces[i] = &Face{Idx: i, Tex: pics[mi], Verts: fv, Info: info, Light: plane}
	}
	w.walk = bsprender.NewWalkContext(bm)
	w.mark = bsprender.NewMarkContext(bm)
	if w.Lights, err = bsprender.NewLightSampler(bm); err != nil {
		return nil, fmt.Errorf("worldview: %w", err)
	}
	return w, nil
}

// Views returns the fixed views the acceptance frames use: four in-map
// origins (the map's centre or the first in-leaf lattice point, then
// the lattice at its height) times four yaws, in a fixed order.
func (w *World) Views() []View {
	models, err := w.File.Models()
	if err != nil || len(models) == 0 {
		return nil
	}
	m := &models[0]
	var anchor [3]float32
	found := false
	centre := [3]float32{(m.Mins[0] + m.Maxs[0]) / 2, (m.Mins[1] + m.Maxs[1]) / 2, (m.Mins[2] + m.Maxs[2]) / 2}
	if w.BM.PointInLeaf(centre) > 0 {
		anchor, found = centre, true
	}
	const steps = 9
	for ix := 0; ix < steps && !found; ix++ {
		for iy := 0; iy < steps && !found; iy++ {
			for iz := 0; iz < steps && !found; iz++ {
				p := [3]float32{
					m.Mins[0] + (m.Maxs[0]-m.Mins[0])*float32(ix+1)/(steps+1),
					m.Mins[1] + (m.Maxs[1]-m.Mins[1])*float32(iy+1)/(steps+1),
					m.Mins[2] + (m.Maxs[2]-m.Mins[2])*float32(iz+1)/(steps+1),
				}
				if w.BM.PointInLeaf(p) > 0 {
					anchor, found = p, true
				}
			}
		}
	}
	if !found {
		return nil
	}
	origins := [][3]float32{anchor}
	const n = 4
	for ix := 0; ix < n && len(origins) < 4; ix++ {
		for iy := 0; iy < n && len(origins) < 4; iy++ {
			p := [3]float32{
				m.Mins[0] + (m.Maxs[0]-m.Mins[0])*float32(ix+1)/(n+1),
				m.Mins[1] + (m.Maxs[1]-m.Mins[1])*float32(iy+1)/(n+1),
				anchor[2],
			}
			if w.BM.PointInLeaf(p) > 0 {
				origins = append(origins, p)
			}
		}
	}
	var out []View
	for _, o := range origins {
		for _, yaw := range []float32{0, 90, 180, 270} {
			out = append(out, View{Origin: o, Angles: [3]float32{0, yaw, 0}})
		}
	}
	return out
}

// Visible returns the indices of the drawn faces the PVS and the BSP
// walk select for v on a width x height frame, in the walk's order
// (front to back). A view outside the map (leaf 0) sees none.
func (w *World) Visible(v View, width, height int) ([]int, error) {
	w.rd = render.RefDef{
		VRect: render.VRect{Width: width, Height: height}, ViewAngles: v.Angles, ViewOrigin: v.Origin,
		FovX: w.FOV, FovY: w.FOV,
	}
	w.frame++
	w.visSpecial = w.visSpecial[:0]
	leaf := w.BM.PointInLeaf(v.Origin)
	if leaf <= 0 {
		return nil, nil
	}
	if err := bsprender.MarkVisibleLeaves(w.mark, bsprender.VisLeafIdx(leaf), bsprender.FrameMarkSequence(w.frame)); err != nil {
		return nil, err
	}
	w.surfaces.Reset()
	if err := bsprender.WalkWorld(w.walk, 0, v.Origin, w.rd.BuildFrustum(), w.frame, &w.surfaces); err != nil {
		return nil, err
	}
	var out []int
	for i := 0; i < w.surfaces.Len(); i++ {
		idx := w.surfaces.Refs[i].FaceIdx
		if w.Faces[idx] != nil {
			out = append(out, idx)
		} else if w.Special[idx] != nil {
			w.visSpecial = append(w.visSpecial, idx)
		}
	}
	return out, nil
}

// DrawSoftware is the reference: it clears fb to [ClearIndex] and draws
// the visible faces in the walk's order, as the engine does, with lightmapped perspective texturing through cm.
func (w *World) DrawSoftware(fb *render.FrameBuffer, cm *render.ColorMap, v View) error {
	for i := range fb.Pixels {
		fb.Pixels[i] = ClearIndex
	}
	faces, err := w.Visible(v, fb.Width, fb.Height)
	if err != nil {
		return err
	}
	view := w.rd.SetupView()
	for _, idx := range faces {
		f := w.Faces[idx]
		verts, err := bsprender.TransformFaceLightmapped(view, fb, w.FOV, f.Verts, f.Info)
		if err != nil {
			continue // behind the camera, or not a polygon the filler takes
		}
		_ = render.FillPerspectiveLightmappedPolygon(fb, f.Tex, cm, verts, f.Light, f.Info.Width, f.Info.Height)
	}
	return nil
}

// identityColorMap is the colormap the engine's world loop (game.setupRenderer)
// draws with: every light level maps a texel to itself, so the world is
// unlit.
var identityColorMap = func() *render.ColorMap {
	var cm render.ColorMap
	for r := range cm {
		for c := range cm[r] {
			cm[r][c] = byte(c)
		}
	}
	return &cm
}()

// DrawEngine is the engine's own software world path, as game.setupRenderer
// runs it for the faces this package draws: the same visible faces, each
// through TransformFace and the affine render.FillTexturedPolygon with the
// identity colormap at light level 0 (no lightmap). It exists to measure
// the cost of what the engine ships next to the lightmapped reference
// ([World.DrawSoftware]) and the GPU world; its pixels are not compared
// with the GPU's.
func (w *World) DrawEngine(fb *render.FrameBuffer, v View) error {
	for i := range fb.Pixels {
		fb.Pixels[i] = ClearIndex
	}
	faces, err := w.Visible(v, fb.Width, fb.Height)
	if err != nil {
		return err
	}
	view := w.rd.SetupView()
	for _, idx := range faces {
		f := w.Faces[idx]
		verts, err := bsprender.TransformFace(view, fb, w.FOV, f.Verts)
		if err != nil {
			continue
		}
		_ = render.FillTexturedPolygon(fb, f.Tex, identityColorMap, 0, verts)
	}
	return nil
}
