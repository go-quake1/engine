// Copyright (c) 2026 the go-quake1/engine authors.
// SPDX-License-Identifier: GPL-2.0-or-later

// Package gpuworld draws the BSP world on the GPU (go-gfx/webgpu) into an
// 8-bit palettized frame: the same indices the software renderer writes
// (package worldview), so that the existing presenter displays it and the
// look stays the software one.
//
// This file is the platform-neutral part: the geometry and the atlases
// built once per map (every visible-face triangle fan with its texture
// and lightmap rectangles), the per-frame draw list, and the shader. The
// device side is in renderer.go.
//
// The world is drawn from Quake's surface cache with mip levels
// (render.SurfaceCache, reference/common d_surf.c and r_surf.c): per face and
// per mip level the lit surface (render.DrawSurface: the texture's mip level
// lit by the interpolated light grid through the colormap) is built once on the
// CPU, by the code the software path uses, and packed into one r8uint atlas.
// Per frame the CPU gives every visible face its mip level (worldview.MipView,
// D_MipLevelForScale of the face's nearest 1/z); per fragment the shader
// rebuilds the reference's 8-pixel sub-span, takes the surface coordinate
// relative to the face's texturemins, divides it by the level's size and reads
// the surface texel. The result is a palette index written to an r8uint
// target. Faces are drawn back to front (the BSP walk's order reversed), with
// no depth test, as the software renderer does: the walk's first face is the
// one Quake's edge sorter keeps at a pixel (r_edge.c, lowest surf->key wins).
package gpuworld

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"sort"
	"time"

	"github.com/go-gfx/webgpu/atlas"

	"github.com/go-quake1/engine/bsprender"
	"github.com/go-quake1/engine/render"
	"github.com/go-quake1/engine/worldview"
)

// VertexStride is the size of one vertex in bytes: position (3 floats) and the
// face's index in the face tables (a uint32).
const VertexStride = 16

// AtlasWidth is the width of the sky and water texture atlases.
const AtlasWidth = 2048

// SurfAtlasWidth is the width of the surface atlas: e1m1's surfaces are
// 33 MB at the four mip levels (25 MB at level 0), more than a 4096 x 8192
// texture holds.
const SurfAtlasWidth = 8192

// MaxAtlasHeight bounds an atlas: the WebGPU default limit of a 2D texture.
const MaxAtlasHeight = 8192

// ErrAtlasFull is returned by [Build] when the textures or the lightmaps
// do not fit the atlas.
var ErrAtlasFull = errors.New("gpuworld: atlas full")

// Atlas is an r8 image the faces' textures or lightmaps are packed in.
type Atlas struct {
	Width, Height int
	Pixels        []byte // Width*Height
}

// Rect is a region of an atlas.
type Rect = atlas.Rect

// packer places keyed rectangles (one per distinct texture or lightmap
// plane) with atlas.Shelf and copies their pixels once the height is known.
type packer struct {
	sh      *atlas.Shelf
	a       *Atlas
	placed  map[any]Rect
	pending []func()
}

func newShelf(maxH int) *packer { return newShelfW(AtlasWidth, maxH) }

func newShelfW(width, maxH int) *packer {
	return &packer{sh: atlas.NewShelf(width, maxH), a: &Atlas{Width: width}, placed: map[any]Rect{}}
}

func (p *packer) place(key any, w, h int, blit func(a *Atlas, r Rect)) (Rect, error) {
	if r, ok := p.placed[key]; ok {
		return r, nil
	}
	r, err := p.sh.Place(w, h)
	if err != nil {
		return Rect{}, ErrAtlasFull
	}
	p.placed[key] = r
	p.pending = append(p.pending, func() { blit(p.a, r) })
	return r, nil
}

func (p *packer) finish() *Atlas {
	p.a.Height = max(p.sh.Height(), 1)
	p.a.Pixels = make([]byte, p.a.Width*p.a.Height)
	for _, f := range p.pending {
		f()
	}
	return p.a
}

func blitRows(a *Atlas, r Rect, src []byte, srcPitch int) {
	for y := 0; y < int(r.H); y++ {
		copy(a.Pixels[(int(r.Y)+y)*a.Width+int(r.X):][:r.W], src[y*srcPitch:][:r.W])
	}
}

// Geometry is a map's GPU-side content, built once.
type Geometry struct {
	Vertices []byte   // VertexStride bytes per vertex
	Indices  []uint32 // triangle lists, one range per drawn face
	First    []uint32 // by face index: first index; Count 0 = not drawn
	Count    []uint32
	// Surf holds the lit surfaces: every drawn face at each of the four mip
	// levels (render.DrawSurface), as final palette indices.
	//
	// The levels are in two atlases: Surf holds level 0 and SurfLow levels 1
	// to 3, each up to MaxAtlasHeight rows. In one atlas the freepak's e1m2,
	// e1m3 and e1m6 (10634, 8386 and 10048 rows at four levels) did not fit;
	// level 0 alone is at most 8048 rows (e1m2).
	Surf    *Atlas
	SurfLow *Atlas
	Faces   int // drawn faces

	// Ord is by face index the face's ordinal among the drawn faces (the
	// index into the face tables and into a frame's mips), -1 if not drawn.
	Ord []int32

	// FaceTab is 64 bytes per drawn face (in drawing order of Build): the
	// plane (normal, dist), the texture axes with offsets, texturemins, and
	// where the face's world-space polygon is in FaceVerts (16 bytes per
	// vertex). The fragment shader rebuilds the 8-pixel sub-spans of the
	// reference from them.
	FaceTab, FaceVerts []byte
	// SurfTab is 64 bytes per drawn face: the surface's rectangle in Surf
	// (x, y, width, height as uint32) for each of the four mip levels.
	SurfTab []byte
	// SurfBytes is the sum of the surfaces' sizes, the atlas content.
	SurfBytes [render.SurfMips]int

	// The surfaces follow the world's animation (worldview.World.Anim): a
	// surface is re-baked, at the mip level a frame draws it at, when the
	// light styles or the texture frame it was baked with are no longer the
	// frame's (D_CacheSurface's reuse test), and its rectangle is in
	// Updates for the renderer to upload.
	rects [][render.SurfMips]Rect // by ordinal
	baked [][render.SurfMips]surfState
	env   render.SurfaceEnv
	// stamp is the frame (frameNo) a surface was last looked at in.
	stamp   [][render.SurfMips]uint32
	frameNo uint32

	// Updates are the atlas rectangles the last [Geometry.Draws] re-baked
	// (Atlas.Pixels already holds them), UpdateBytes their size and Deferred
	// the surfaces left for a later frame because the frame's UpdateBudget
	// was reached.
	Updates     []Update
	UpdateBytes int
	Deferred    int
	// PlantCoalesceDefect zeroes the texels of every merged write ([Geometry.Coalesce]),
	// the planted defect of the dynamic light check.
	PlantCoalesceDefect bool
	sorted              []Update
	// BakeTime is the time the last Draws spent in render.DrawSurface (the CPU
	// side of the re-bakes, apart from the upload).
	BakeTime time.Duration

	// Idle, if set, is run by Draws between faces, at most once every IdleEvery
	// (DefaultIdleEvery when zero): the frame's BSP-side work and re-bakes are the
	// program's own CPU, where WinQuake calls S_ExtraUpdate (r_edge.c:728, inside
	// the edge scan). Clock is time.Now, replaced by a test.
	Idle      func() error
	IdleEvery time.Duration
	Clock     func() time.Time
	// UpdateBudget is the most surface bytes one frame re-bakes (MaxUpdateBytes
	// from Build).
	UpdateBudget int
	updBuf       []byte
	// IgnoreDLights bakes the surfaces without the world's dynamic lights (the
	// planted defect of the dynamic light check).
	IgnoreDLights bool
}

// MaxUpdateBytes is the default UpdateBudget: the bytes of re-baked surfaces
// one frame uploads. It is a half of MaxBandBytes, far under the host's frame
// (32 MiB); a frame that needs more shows the older surface for a frame or
// more rather than overrunning the frame.
const MaxUpdateBytes = 4 << 20

// surfState is what a baked surface was built with: D_CacheSurface's lightadj
// and texture.
type surfState struct {
	adj  [4]int
	tex  *render.SurfaceTexture
	dlit bool // baked with a dynamic light on it (D_CacheSurface's cache->dlight)
}

// Update is one re-baked surface: its rectangle in the atlas of level 0 (Low
// false) or of levels 1 to 3, and its Rect.W x Rect.H palette indices.
type Update struct {
	Low bool
	R   Rect
	Pix []byte
}

// bakeMip bakes one mip level of face f in the given state with the software
// path's code (render.DrawSurface), refusing the faces the software path does
// not draw (an empty surface, a texture without its mip levels).
func bakeMip(f *worldview.Face, adj [4]int, tex *render.SurfaceTexture, mip int, env *render.SurfaceEnv) ([]byte, bool) {
	sw, sh := f.Surf.Desc.Extents[0]>>mip, f.Surf.Desc.Extents[1]>>mip
	if sw <= 0 || sh <= 0 {
		return nil, false
	}
	px := make([]byte, sw*sh)
	d := f.Surf.Desc
	d.Texture = tex
	if err := render.DrawSurface(px, &d, adj, mip, env); err != nil {
		return nil, false
	}
	return px, true
}

// bakeSurfaces bakes the four mip levels of face f in the world's current
// animation state.
func bakeSurfaces(w *worldview.World, f *worldview.Face, env *render.SurfaceEnv) (s [render.SurfMips][]byte, ok bool) {
	adj, tex := w.SurfaceState(f)
	for mip := range s {
		if s[mip], ok = bakeMip(f, adj, tex, mip, env); !ok {
			return s, false
		}
	}
	return s, true
}

// refresh re-bakes face f (ordinal ord) at mip level mip if its surface was
// baked in another state than the world's animation gives now, within the
// frame's budget.
func (g *Geometry) refresh(w *worldview.World, f *worldview.Face, ord, mip int) {
	// A face is in the frame's list once per leaf it is attached to: one bake
	// serves them all.
	if g.stamp[ord][mip] == g.frameNo {
		return
	}
	g.stamp[ord][mip] = g.frameNo
	adj, tex := w.SurfaceState(f)
	st := &g.baked[ord][mip]
	// The dynamic lights of the frame (R_PushDlights marked the face the way the
	// CPU's surfaces are): a surface they light is baked with them in every
	// frame it is lit, and once more after, to take them off.
	env := g.env
	if !g.IgnoreDLights {
		w.LightEnv(&env)
	}
	dlit := f.Surf.Desc.DLit(&env)
	if st.adj == adj && st.tex == tex && !st.dlit && !dlit {
		return
	}
	r := g.rects[ord][mip]
	n := int(r.W) * int(r.H)
	// A surface larger than the budget is still done, alone in its frame:
	// otherwise it would never be.
	if g.UpdateBytes > 0 && g.UpdateBytes+n > g.UpdateBudget {
		g.Deferred++
		return
	}
	// bake straight into the frame's upload buffer (earlier updates of the frame
	// keep their slices if it has to grow)
	start := len(g.updBuf)
	if start+n > cap(g.updBuf) {
		g.updBuf = make([]byte, 0, max(g.UpdateBudget, 2*cap(g.updBuf), n))
		start = 0
	}
	px := g.updBuf[start : start+n : start+n]
	if sw, sh := f.Surf.Desc.Extents[0]>>mip, f.Surf.Desc.Extents[1]>>mip; sw <= 0 || sh <= 0 {
		return
	}
	d := f.Surf.Desc
	d.Texture = tex
	t0 := time.Now()
	err := render.DrawSurface(px, &d, adj, mip, &env)
	g.BakeTime += time.Since(t0)
	if err != nil {
		return
	}
	g.updBuf = g.updBuf[:start+n]
	*st = surfState{adj, tex, dlit}
	at := g.Surf
	if mip > 0 {
		at = g.SurfLow
	}
	blitRows(at, r, px, int(r.W))
	g.Updates = append(g.Updates, Update{Low: mip > 0, R: r, Pix: px})
	g.UpdateBytes += n
}

// Build bakes and packs the world's lit surfaces (every face at four mip
// levels, through cm) and builds the faces' triangle fans. A face is a convex
// polygon, fanned from its first vertex.
func Build(w *worldview.World, cm *render.ColorMap) (*Geometry, error) {
	g := &Geometry{First: make([]uint32, len(w.Faces)), Count: make([]uint32, len(w.Faces)), Ord: make([]int32, len(w.Faces))}
	for i := range g.Ord {
		g.Ord[i] = -1
	}
	surf := newShelfW(SurfAtlasWidth, MaxAtlasHeight)    // level 0
	surfLow := newShelfW(SurfAtlasWidth, MaxAtlasHeight) // levels 1 to 3
	env := &render.SurfaceEnv{Colormap: cm, HaveLightData: w.HaveLightData}

	// Bake every surface first, then pack them tallest first: the shelf
	// packer's rows are as high as their tallest surface.
	type item struct {
		face   int
		mip    int
		px     []byte
		width  int
		height int
	}
	var items []item
	for _, f := range w.Faces {
		if f == nil {
			continue
		}
		if f.Verts.NumVerts > render.MaxPolyVerts {
			return nil, fmt.Errorf("gpuworld: face %d has %d vertices, above the %d the shader's edge loop is bounded to", f.Idx, f.Verts.NumVerts, render.MaxPolyVerts)
		}
		baked, ok := bakeSurfaces(w, f, env)
		if !ok {
			continue
		}
		for mip, px := range baked {
			sw := f.Surf.Desc.Extents[0] >> mip
			items = append(items, item{f.Idx, mip, px, sw, len(px) / sw})
			g.SurfBytes[mip] += len(px)
		}
	}
	sort.SliceStable(items, func(i, j int) bool {
		if items[i].height != items[j].height {
			return items[i].height > items[j].height
		}
		return items[i].width > items[j].width
	})
	placed := map[[2]int]Rect{}
	for _, it := range items {
		pk := surf
		if it.mip > 0 {
			pk = surfLow
		}
		r, err := pk.place(&it.px[0], it.width, it.height, func(a *Atlas, r Rect) { blitRows(a, r, it.px, it.width) })
		if err != nil {
			return nil, err
		}
		placed[[2]int{it.face, it.mip}] = r
	}

	for _, f := range w.Faces {
		if f == nil {
			continue
		}
		if _, ok := placed[[2]int{f.Idx, 0}]; !ok {
			continue
		}
		var rs [render.SurfMips]Rect
		adj, tex := w.SurfaceState(f)
		for mip := 0; mip < render.SurfMips; mip++ {
			r := placed[[2]int{f.Idx, mip}]
			rs[mip] = r
			for _, x := range [4]uint32{r.X, r.Y, r.W, r.H} {
				g.SurfTab = binary.LittleEndian.AppendUint32(g.SurfTab, x)
			}
		}
		g.rects = append(g.rects, rs)
		g.stamp = append(g.stamp, [render.SurfMips]uint32{})
		g.baked = append(g.baked, [render.SurfMips]surfState{{adj, tex, false}, {adj, tex, false}, {adj, tex, false}, {adj, tex, false}})
		n := f.Verts.NumVerts
		base := uint32(len(g.Vertices) / VertexStride)
		g.Ord[f.Idx] = int32(g.Faces)
		for i := 0; i < n; i++ {
			p := f.Verts.Vert(i)
			var vb [VertexStride]byte
			put := func(o int, x float32) { binary.LittleEndian.PutUint32(vb[o:], math.Float32bits(x)) }
			put(0, p[0])
			put(4, p[1])
			put(8, p[2])
			binary.LittleEndian.PutUint32(vb[12:], uint32(g.Faces)) // index in FaceTab
			g.Vertices = append(g.Vertices, vb[:]...)
		}
		pl := bsprender.SurfacePlaneOf(f.Verts, f.Info)
		for _, x := range [16]float32{
			pl.Normal[0], pl.Normal[1], pl.Normal[2], pl.Dist,
			pl.AxisS[0], pl.AxisS[1], pl.AxisS[2], pl.OffS,
			pl.AxisT[0], pl.AxisT[1], pl.AxisT[2], pl.OffT,
			pl.MinS, pl.MinT, float32(len(g.FaceVerts) / 16), float32(n),
		} {
			g.FaceTab = binary.LittleEndian.AppendUint32(g.FaceTab, math.Float32bits(x))
		}
		for i := 0; i < n; i++ {
			p := f.Verts.Vert(i)
			for _, x := range [4]float32{p[0], p[1], p[2], 0} {
				g.FaceVerts = binary.LittleEndian.AppendUint32(g.FaceVerts, math.Float32bits(x))
			}
		}
		g.First[f.Idx] = uint32(len(g.Indices))
		for i := 1; i+1 < n; i++ {
			g.Indices = append(g.Indices, base, base+uint32(i), base+uint32(i+1))
		}
		g.Count[f.Idx] = uint32(len(g.Indices)) - g.First[f.Idx]
		g.Faces++
	}
	g.Surf, g.SurfLow = surf.finish(), surfLow.finish()
	g.env, g.UpdateBudget = *env, MaxUpdateBytes
	return g, nil
}

// Draw is one face's index range in a frame's draw list.
type Draw struct{ First, Count uint32 }

// MipPolicy says how a frame picks the mip level of a face: the zero value is
// Quake's (the face's nearest 1/z); the others are ablations and the planted
// negative of the check.
type MipPolicy struct {
	// Fixed >= 0 draws every face at that level.
	Fixed int
	// Bias is added to the chosen level (clamped to 0..3).
	Bias int
}

// Chosen is the policy that picks Quake's level.
var Chosen = MipPolicy{Fixed: -1}

// FaceDraw is a world face in a frame's draw list: its index range, the mip
// level of this frame (passed to the shader as the draw's first instance) and
// the face index.
type FaceDraw struct {
	First, Count, Mip uint32
	Face              int
}

// Draws is the frame's draw list: the faces the PVS and the BSP walk
// select for v, in the walk's order reversed (back to front), minus those the software renderer
// also refuses (every vertex behind the near plane), each with its mip level
// for this frame.
func (g *Geometry) Draws(w *worldview.World, v worldview.View, width, height int, out []FaceDraw, pol MipPolicy) ([]FaceDraw, error) {
	clock := g.Clock
	if clock == nil {
		clock = time.Now
	}
	every := g.IdleEvery
	if every <= 0 {
		every = DefaultIdleEvery
	}
	last := clock() // the last time the program fed its sound: this call's start
	var idleErr error
	faces, err := w.Visible(v, width, height)
	if err != nil {
		return nil, err
	}
	view := (&render.RefDef{ViewAngles: v.Angles, ViewOrigin: v.Origin}).SetupView()
	mv := worldview.NewMipView(width, height, w.FOV)
	var ptsBuf [render.MaxPolyVerts][3]float32
	out = out[:0]
	g.Updates, g.UpdateBytes, g.Deferred, g.BakeTime = g.Updates[:0], 0, 0, 0
	g.frameNo++
	g.updBuf = g.updBuf[:0]
	// Back to front: the walk's list is front to back and Quake's edge sorter
	// shows, at every pixel, the face with the smallest key (the first the walk
	// emitted); with no depth test the same result is the list drawn in reverse.
	for n := len(faces) - 1; n >= 0; n-- {
		idx := faces[n]
		if g.Count[idx] == 0 {
			continue
		}
		if g.Idle != nil && n%idleFaces == 0 {
			if now := clock(); now.Sub(last) >= every {
				if err := g.Idle(); err != nil && idleErr == nil {
					idleErr = err
				}
				last = clock()
			}
		}
		f := w.Faces[idx]
		// A face whose bounding sphere is inside the frustum is in front of
		// the near plane and needs no clipping: its level from the z of its
		// vertices alone. Otherwise one transform of the polygon serves the
		// near-plane test and the clipped mip choice.
		level, front := mv.FastLevel(view, f)
		if !front {
			level, front = mv.LevelFront(view, f, &ptsBuf)
		}
		if front {
			mip := pol.Fixed
			if mip < 0 {
				mip = max(0, min(render.SurfMips-1, level+pol.Bias))
			}
			g.refresh(w, f, int(g.Ord[idx]), mip)
			out = append(out, FaceDraw{First: g.First[idx], Count: g.Count[idx], Mip: uint32(mip), Face: idx})
		}
	}
	return out, idleErr
}

// DefaultIdleEvery is the time between two runs of Geometry.Idle in a Draws:
// the slice of the sink's wait for the GPU (batchsink.IdleSlice).
const DefaultIdleEvery = 2 * time.Millisecond

// idleFaces is how many faces Draws goes through between two reads of the clock
// that decides whether Idle is due.
const idleFaces = 16

// UniformSize is the size of the frame uniform in bytes.
const UniformSize = 96

// Uniform packs the frame uniform at time 0; see [UniformAt].
func Uniform(v worldview.View, fov float32, width, height int) []byte {
	return UniformAt(v, fov, width, height, 0)
}

// UniformAt packs the frame uniform: the three rows of the view transform
// (rotation row and translation, as vec4s), (tan(fov/2), near clip,
// halfW/(halfH tan(fov/2)), 1/width) and (time in seconds, 0, 0, 0).
func UniformAt(v worldview.View, fov float32, width, height int, t float32) []byte {
	view := (&render.RefDef{ViewAngles: v.Angles, ViewOrigin: v.Origin}).SetupView()
	b := make([]byte, UniformSize)
	f := func(o int, x float32) { binary.LittleEndian.PutUint32(b[o:], math.Float32bits(x)) }
	for r := 0; r < 3; r++ {
		for c := 0; c < 3; c++ {
			f(16*r+4*c, view.R[r][c])
		}
		f(16*r+12, view.T[r])
	}
	tanHalf := float32(math.Tan(float64(fov/2) * math.Pi / 180))
	f(48, tanHalf)
	f(52, render.ParticleNearClip)
	f(56, float32(width)/2/(float32(height)/2*tanHalf))
	f(60, 1/float32(width))
	f(64, t)
	f(76, float32(render.SkyFrameTime(float64(t))))
	f(68, float32(width))
	f(72, float32(height))
	// the viewer's position: view maps p to R p + T, so it is -R^T T.
	for i := 0; i < 3; i++ {
		var o float32
		for k := 0; k < 3; k++ {
			o -= view.R[k][i] * view.T[k]
		}
		f(80+4*i, o)
	}
	return b
}

// ShaderWGSL draws the faces into an r8uint target; see the package comment.
const ShaderWGSL = `
struct U { rows: array<vec4f, 3>, p: vec4f, q: vec4f, o: vec4f };
@group(0) @binding(0) var<uniform> u: U;
@group(0) @binding(1) var surf: texture_2d<u32>;    // the surfaces' level 0
@group(0) @binding(2) var surfLow: texture_2d<u32>; // their levels 1 to 3
@group(0) @binding(4) var<storage, read> faceTab: array<vec4f>;
@group(0) @binding(5) var<storage, read> faceVerts: array<vec4f>;
@group(0) @binding(6) var<storage, read> surfTab: array<vec4u>;

struct VIn {
  @location(0) pos: vec3f,
  @location(1) fi: u32,
  @builtin(instance_index) mip: u32, // the draw's first instance: the face's mip level
};
struct VOut {
  @builtin(position) p: vec4f,
  @location(4) @interpolate(flat) fi: u32,
  @location(5) @interpolate(flat) mip: u32,
};

@vertex fn vs(v: VIn) -> VOut {
  let vp = vec3f(dot(u.rows[0].xyz, v.pos) + u.rows[0].w,
                 dot(u.rows[1].xyz, v.pos) + u.rows[1].w,
                 dot(u.rows[2].xyz, v.pos) + u.rows[2].w);
  let d = max(vp.z, u.p.y);
  var o: VOut;
  // The reference takes pixel x when its left edge is inside the span
  // (render.FillPerspectiveLightmappedPolygon: ceil(xLeft)): the polygon is
  // moved half a pixel right (u.p.w is 1 / width) so that the GPU takes the
  // same pixels; the fragment then works from its own pixel index.
  o.p = vec4f(vp.x / u.p.x + u.p.w * d, vp.y * u.p.z, 0.5 * d, d);
  o.fi = v.fi; o.mip = v.mip;
  return o;
}

// The screen position of a world vertex as bsprender.TransformFaceLightmapped
// gives it: depth clamped to the near plane.
fn proj(w: vec3f) -> vec2f {
  let vp = vec3f(dot(u.rows[0].xyz, w) + u.rows[0].w,
                 dot(u.rows[1].xyz, w) + u.rows[1].w,
                 dot(u.rows[2].xyz, w) + u.rows[2].w);
  let d = max(vp.z, u.p.y);
  let halfW = u.q.y * 0.5;
  let scale = halfW / u.p.x;
  return vec2f(halfW + vp.x * scale / d, u.q.z * 0.5 - vp.y * scale / d);
}

// A polygon vertex as the reference fills it: its screen position, and
// (1/z, u/z, v/z) with z the depth clamped to the near plane.
struct PV { s: vec2f, w: vec3f };

fn polyVertex(p: vec3f, f1: vec4f, f2: vec4f) -> PV {
  let vp = vec3f(dot(u.rows[0].xyz, p) + u.rows[0].w,
                 dot(u.rows[1].xyz, p) + u.rows[1].w,
                 dot(u.rows[2].xyz, p) + u.rows[2].w);
  let d = max(vp.z, u.p.y);
  let halfW = u.q.y * 0.5;
  let scale = halfW / u.p.x;
  let inv = 1.0 / d;
  return PV(vec2f(halfW + vp.x * scale / d, u.q.z * 0.5 - vp.y * scale / d),
            vec3f(inv, (dot(f1.xyz, p) + f1.w) * inv, (dot(f2.xyz, p) + f2.w) * inv));
}

// (s, t) at screen x xc on the run from xl to xr whose ends carry wl and wr:
// linear in (1/z, u/z, v/z), then divided; relative to the face's texturemins
// (the surface coordinate at mip 0).
fn runAt(xc: f32, xl: f32, wl: vec3f, wr: vec3f, span: f32, minST: vec2f) -> vec2f {
  var dW = vec3f(0.0);
  if (span > 0.0) { dW = (wr - wl) / span; }
  let w = wl + (xc - xl) * dW;
  let z = 1.0 / w.x;
  return vec2f(w.y * z - minST.x, w.z * z - minST.y);
}

@fragment fn fs(v: VOut) -> @location(0) vec4u {
  let f0 = faceTab[v.fi * 4u]; let f1 = faceTab[v.fi * 4u + 1u];
  let f2 = faceTab[v.fi * 4u + 2u]; let f3 = faceTab[v.fi * 4u + 3u];
  let px = i32(floor(v.p.x)); let py = i32(floor(v.p.y));
  let yf = f32(py) + 0.5;
  // The scanline run of the face the reference fills: its first and last
  // pixel, from the polygon's edges at the scanline centre.
  var xl = 1.0e30; var xr = -1.0e30;
  var wl = vec3f(1.0, 0.0, 0.0); var wr = vec3f(1.0, 0.0, 0.0);
  let n = u32(f3.w); let first = u32(f3.z);
  var prev = polyVertex(faceVerts[first + n - 1u].xyz, f1, f2);
  // A constant bound (render.MaxPolyVerts) with a break at the face's count:
  // a host that cannot truncate a fragment stage accepts only loops it can
  // bound (go-gfx/webgpu batch.MaxRenderLoop); Build refuses a larger face.
  for (var i = 0u; i < 64u; i++) {
    if (i >= n) { break; }
    let cur = polyVertex(faceVerts[first + i].xyz, f1, f2);
    if ((prev.s.y <= yf && cur.s.y > yf) || (cur.s.y <= yf && prev.s.y > yf)) {
      let t = (yf - prev.s.y) / (cur.s.y - prev.s.y);
      let x = prev.s.x + t * (cur.s.x - prev.s.x);
      let w = prev.w + t * (cur.w - prev.w);
      if (x < xl) { xl = x; wl = w; }
      if (x > xr) { xr = x; wr = w; }
    }
    prev = cur;
  }
  var x0 = px; var x1 = px;
  if (xl <= xr) { x0 = max(i32(ceil(xl)), 0); x1 = min(i32(floor(xr)), i32(u.q.y) - 1); }
  // Sub-spans of 8 pixels from the run's first pixel, perspective-exact at
  // their ends and linear between (d_scan.c D_DrawSpans8): the last one ends
  // on the run's last pixel.
  let k = max(px - x0, 0) / 8;
  let xa = x0 + 8 * k;
  let count = x1 - xa + 1;
  var xe = xa + 8; var inv = 0.125;
  if (count <= 8) { xe = xa + count - 1; inv = select(0.0, 1.0 / f32(count - 1), count > 1); }
  let span = xr - xl;
  let A = runAt(f32(xa) + 0.5, xl, wl, wr, span, f3.xy);
  let B = runAt(f32(xe) + 0.5, xl, wl, wr, span, f3.xy);
  let c = A + f32(px - xa) * (B - A) * inv;

  // The face's surface at this frame's mip level (D_MipLevelForScale, chosen
  // on the CPU): the surface is Extents >> mip texels, read with the
  // coordinate scaled by 1 / 2^mip and clamped to the surface (D_DrawSpans8's
  // bbextents clamp).
  let mip = v.mip;
  let r = surfTab[v.fi * 4u + mip];
  let sc = 1.0 / f32(1u << mip);
  let su = clamp(i32(floor(c.x * sc)), 0, i32(r.z) - 1);
  let sv = clamp(i32(floor(c.y * sc)), 0, i32(r.w) - 1);
  let at = vec2u(r.x + u32(su), r.y + u32(sv));
  if (mip == 0u) { return vec4u(textureLoad(surf, at, 0).r, 0u, 0u, 0u); }
  return vec4u(textureLoad(surfLow, at, 0).r, 0u, 0u, 0u);
}
`
