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
// device side is in gpuworld_js.go.
//
// Per fragment the shader does what render.FillPerspectiveLightmappedPolygon
// does per pixel: it wraps the texture coordinate into the face's texture
// (a rectangle of the texture atlas), samples the face's lightmap plane (a
// rectangle of the lightmap atlas) bilinearly, turns the sample into a
// colormap row ((255 - sample) >> 2), and looks the texel index up in that
// row of the colormap texture. The result is a palette index written to an
// r8uint target. Faces are drawn in the BSP walk's order, with no depth
// test, as the software renderer does.
package gpuworld

import (
	"encoding/binary"
	"errors"
	"math"

	"github.com/go-gfx/webgpu/atlas"

	"github.com/go-quake1/engine/render"
	"github.com/go-quake1/engine/worldview"
)

// VertexStride is the size of one vertex in bytes: position (3 floats),
// texture uv (2), lightmap uv (2), texture rect (4 uint32: x y w h) and
// lightmap rect (4 uint32), padded to 64.
const VertexStride = 64

// AtlasWidth is the width of the texture and lightmap atlases.
const AtlasWidth = 2048

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

func newShelf(maxH int) *packer {
	return &packer{sh: atlas.NewShelf(AtlasWidth, maxH), a: &Atlas{Width: AtlasWidth}, placed: map[any]Rect{}}
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
	Tex, Lm  *Atlas
	Faces    int // drawn faces
}

// Build packs the world's textures and lightmaps and the faces' triangle
// fans. A face is a convex polygon, fanned from its first vertex.
func Build(w *worldview.World) (*Geometry, error) {
	g := &Geometry{First: make([]uint32, len(w.Faces)), Count: make([]uint32, len(w.Faces))}
	tex, lm := newShelf(MaxAtlasHeight), newShelf(MaxAtlasHeight)
	for _, f := range w.Faces {
		if f == nil {
			continue
		}
		tr, err := tex.place(f.Tex, f.Tex.Width, f.Tex.Height, func(a *Atlas, r Rect) { blitRows(a, r, f.Tex.Pixels, f.Tex.Width) })
		if err != nil {
			return nil, err
		}
		lr, err := lm.place(&f.Light[0], f.Info.Width, f.Info.Height, func(a *Atlas, r Rect) { blitRows(a, r, f.Light, f.Info.Width) })
		if err != nil {
			return nil, err
		}
		n := f.Verts.NumVerts
		base := uint32(len(g.Vertices) / VertexStride)
		for i := 0; i < n; i++ {
			p := f.Verts.Vert(i)
			u := p[0]*f.Verts.UVAxisS[0] + p[1]*f.Verts.UVAxisS[1] + p[2]*f.Verts.UVAxisS[2] + f.Verts.UVOffS
			v := p[0]*f.Verts.UVAxisT[0] + p[1]*f.Verts.UVAxisT[1] + p[2]*f.Verts.UVAxisT[2] + f.Verts.UVOffT
			var vb [VertexStride]byte
			put := func(o int, x float32) { binary.LittleEndian.PutUint32(vb[o:], math.Float32bits(x)) }
			putu := func(o int, x uint32) { binary.LittleEndian.PutUint32(vb[o:], x) }
			put(0, p[0])
			put(4, p[1])
			put(8, p[2])
			put(12, u)
			put(16, v)
			put(20, (u-float32(f.Info.MinS))/16)
			put(24, (v-float32(f.Info.MinT))/16)
			putu(28, tr.X)
			putu(32, tr.Y)
			putu(36, tr.W)
			putu(40, tr.H)
			putu(44, lr.X)
			putu(48, lr.Y)
			putu(52, lr.W)
			putu(56, lr.H)
			g.Vertices = append(g.Vertices, vb[:]...)
		}
		g.First[f.Idx] = uint32(len(g.Indices))
		for i := 1; i+1 < n; i++ {
			g.Indices = append(g.Indices, base, base+uint32(i), base+uint32(i+1))
		}
		g.Count[f.Idx] = uint32(len(g.Indices)) - g.First[f.Idx]
		g.Faces++
	}
	g.Tex, g.Lm = tex.finish(), lm.finish()
	return g, nil
}

// Draw is one face's index range in a frame's draw list.
type Draw struct{ First, Count uint32 }

// Draws is the frame's draw list: the faces the PVS and the BSP walk
// select for v, in the walk's order, minus those the software renderer
// also refuses (every vertex behind the near plane).
func (g *Geometry) Draws(w *worldview.World, v worldview.View, width, height int, out []Draw) ([]Draw, error) {
	faces, err := w.Visible(v, width, height)
	if err != nil {
		return nil, err
	}
	view := (&render.RefDef{ViewAngles: v.Angles, ViewOrigin: v.Origin}).SetupView()
	out = out[:0]
	for _, idx := range faces {
		if g.Count[idx] == 0 {
			continue
		}
		fv := w.Faces[idx].Verts
		front := false
		for i := 0; i < fv.NumVerts && !front; i++ {
			front = render.TransformAffine(view, fv.Vert(i))[2] >= render.ParticleNearClip
		}
		if front {
			out = append(out, Draw{g.First[idx], g.Count[idx]})
		}
	}
	return out, nil
}

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
struct U { rows: array<vec4f, 3>, p: vec4f };
@group(0) @binding(0) var<uniform> u: U;
@group(0) @binding(1) var tex: texture_2d<u32>;
@group(0) @binding(2) var lm: texture_2d<u32>;
@group(0) @binding(3) var cmap: texture_2d<u32>;

struct VIn {
  @location(0) pos: vec3f, @location(1) uv: vec2f, @location(2) lmuv: vec2f,
  @location(3) tr: vec4u, @location(4) lr: vec4u,
};
struct VOut {
  @builtin(position) p: vec4f,
  @location(0) uv: vec2f, @location(1) lmuv: vec2f,
  @location(2) @interpolate(flat) tr: vec4u, @location(3) @interpolate(flat) lr: vec4u,
};

@vertex fn vs(v: VIn) -> VOut {
  let vp = vec3f(dot(u.rows[0].xyz, v.pos) + u.rows[0].w,
                 dot(u.rows[1].xyz, v.pos) + u.rows[1].w,
                 dot(u.rows[2].xyz, v.pos) + u.rows[2].w);
  let d = max(vp.z, u.p.y);
  var o: VOut;
  o.p = vec4f(vp.x / u.p.x, vp.y * u.p.z, 0.5 * d, d);
  o.uv = v.uv; o.lmuv = v.lmuv; o.tr = v.tr; o.lr = v.lr;
  return o;
}

fn wrap(i: i32, n: i32) -> i32 { return ((i % n) + n) % n; }

@fragment fn fs(v: VOut) -> @location(0) vec4u {
  let tw = i32(v.tr.z); let th = i32(v.tr.w);
  let tx = wrap(i32(floor(v.uv.x)), tw);
  let ty = wrap(i32(floor(v.uv.y)), th);
  let texel = textureLoad(tex, vec2u(v.tr.x + u32(tx), v.tr.y + u32(ty)), 0).r;

  let lw = f32(v.lr.z - 1u); let lh = f32(v.lr.w - 1u);
  let ls = clamp(v.lmuv.x, 0.0, lw); let lt = clamp(v.lmuv.y, 0.0, lh);
  let s0 = u32(floor(ls)); let t0 = u32(floor(lt));
  let s1 = min(s0 + 1u, v.lr.z - 1u); let t1 = min(t0 + 1u, v.lr.w - 1u);
  let fs_ = ls - f32(s0); let ft = lt - f32(t0);
  let l00 = f32(textureLoad(lm, vec2u(v.lr.x + s0, v.lr.y + t0), 0).r);
  let l10 = f32(textureLoad(lm, vec2u(v.lr.x + s1, v.lr.y + t0), 0).r);
  let l01 = f32(textureLoad(lm, vec2u(v.lr.x + s0, v.lr.y + t1), 0).r);
  let l11 = f32(textureLoad(lm, vec2u(v.lr.x + s1, v.lr.y + t1), 0).r);
  let l0 = l00 + fs_ * (l10 - l00);
  let l1 = l01 + fs_ * (l11 - l01);
  let sample = l0 + ft * (l1 - l0);
  let row = clamp((255 - i32(sample)) >> 2u, 0, 63);
  return vec4u(textureLoad(cmap, vec2u(texel, u32(row)), 0).r, 0u, 0u, 0u);
}
`
