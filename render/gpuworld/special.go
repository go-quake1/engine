// Copyright (c) 2026 the go-quake1/engine authors.
// SPDX-License-Identifier: GPL-2.0-or-later

package gpuworld

import (
	"encoding/binary"
	"math"
	"strconv"
	"strings"

	"github.com/go-quake1/engine/render"
	"github.com/go-quake1/engine/worldview"
)

// This file is the sky (and, later, water) half of the package that does
// not touch the device. Sky and water faces are drawn by the engine, after
// the lit faces, with render.FillSkyPolygon and render.FillTurbulentPolygon
// on the polygon bsprender.TransformFace gives: texture coordinates that are
// the texinfo's (texels) and are interpolated linearly in screen space, and
// pixel coverage that takes a pixel when its left edge, not its centre, is in
// the span (the same rule as render.FillTexturedPolygon). The shaders do the
// same: linear interpolation, the polygon moved half a pixel right with the
// fragment sampling half a pixel along, and no lightmap.

// SpecialStride is the size of one sky or water vertex in bytes: position
// (3 floats), texture uv (2) and the face's rectangle in the texture atlas
// (4 uint32: x y w h), padded to 48.
const SpecialStride = 48

// Special is the GPU-side content of the sky or the water faces of a map,
// built once.
type Special struct {
	Kind     worldview.SpecialKind
	Vertices []byte
	Indices  []uint32
	First    []uint32 // by face index
	Count    []uint32 // 0 = not drawn
	Tex      *Atlas
	Faces    int
}

// BuildSpecial packs the textures and the triangle fans of the faces of
// one kind.
func BuildSpecial(w *worldview.World, kind worldview.SpecialKind) (*Special, error) {
	s := &Special{Kind: kind, First: make([]uint32, len(w.Special)), Count: make([]uint32, len(w.Special))}
	tex := newShelf(MaxAtlasHeight)
	for _, f := range w.Special {
		if f == nil || f.Kind != kind {
			continue
		}
		tr, err := tex.place(f.Tex, f.Tex.Width, f.Tex.Height, func(a *Atlas, r Rect) { blitRows(a, r, f.Tex.Pixels, f.Tex.Width) })
		if err != nil {
			return nil, err
		}
		n := f.Verts.NumVerts
		base := uint32(len(s.Vertices) / SpecialStride)
		for i := 0; i < n; i++ {
			p := f.Verts.Vert(i)
			u := p[0]*f.Verts.UVAxisS[0] + p[1]*f.Verts.UVAxisS[1] + p[2]*f.Verts.UVAxisS[2] + f.Verts.UVOffS
			v := p[0]*f.Verts.UVAxisT[0] + p[1]*f.Verts.UVAxisT[1] + p[2]*f.Verts.UVAxisT[2] + f.Verts.UVOffT
			var vb [SpecialStride]byte
			for k, x := range [5]float32{p[0], p[1], p[2], u, v} {
				binary.LittleEndian.PutUint32(vb[4*k:], math.Float32bits(x))
			}
			for k, x := range [4]uint32{tr.X, tr.Y, tr.W, tr.H} {
				binary.LittleEndian.PutUint32(vb[20+4*k:], x)
			}
			s.Vertices = append(s.Vertices, vb[:]...)
		}
		s.First[f.Idx] = uint32(len(s.Indices))
		for i := 1; i+1 < n; i++ {
			s.Indices = append(s.Indices, base, base+uint32(i), base+uint32(i+1))
		}
		s.Count[f.Idx] = uint32(len(s.Indices)) - s.First[f.Idx]
		s.Faces++
	}
	s.Tex = tex.finish()
	return s, nil
}

// Draws is the frame's draw list of this kind: the faces the last
// [worldview.World.Visible] call selected, in the walk's order, minus those
// the software renderer also refuses (every vertex behind the near plane).
func (s *Special) Draws(w *worldview.World, v worldview.View, out []Draw) []Draw {
	view := (&render.RefDef{ViewAngles: v.Angles, ViewOrigin: v.Origin}).SetupView()
	out = out[:0]
	for _, f := range w.VisibleSpecial() {
		if f.Kind != s.Kind || s.Count[f.Idx] == 0 {
			continue
		}
		front := false
		for i := 0; i < f.Verts.NumVerts && !front; i++ {
			front = render.TransformAffine(view, f.Verts.Vert(i))[2] >= render.ParticleNearClip
		}
		if front {
			out = append(out, Draw{s.First[f.Idx], s.Count[f.Idx]})
		}
	}
	return out
}

// TurbShaderWGSL is [SpecialShaderWGSL] plus fsTurb: render.FillTurbulentPolygon
// per fragment. The warp is the engine's: u' = u + turbsin[floor((v + 40 t) /
// 16) & 255] and v' = v + turbsin[floor((u + 40 t) / 16) & 255], with the
// table the engine builds (peak 8 texels), wrapped into the texture and looked
// up through colormap row 0 (light level 0).
func TurbShaderWGSL() string {
	var b strings.Builder
	b.WriteString(SpecialShaderWGSL)
	b.WriteString("\nvar<private> turbsin = array<f32, 256>(")
	for i, v := range render.TurbSinTable() {
		if i > 0 {
			b.WriteString(", ")
		}
		s := strconv.FormatFloat(float64(v), 'g', -1, 32)
		if !strings.ContainsAny(s, ".e") {
			s += ".0" // a literal such as "8" must be a float in WGSL
		}
		b.WriteString(s)
	}
	b.WriteString(`);

fn warp(coord: f32, t: f32) -> f32 {
  return turbsin[u32(i32(floor((coord + t * 40.0) / 16.0)) & 255)];
}

@fragment fn fsTurb(v: VOut) -> @location(0) vec4u {
  let uv = v.uv + 0.5 * dpdxFine(v.uv);
  let t = u.q.x;
  let uw = uv.x + warp(uv.y, t);
  let vw = uv.y + warp(uv.x, t);
  let texel = at(v.tr, wrap(i32(floor(uw)), i32(v.tr.z)), wrap(i32(floor(vw)), i32(v.tr.w)));
  return vec4u(textureLoad(cmap, vec2u(texel, 0u), 0).r, 0u, 0u, 0u);
}
`)
	return b.String()
}

// SpecialShaderWGSL draws sky faces (fsSky) and, with [TurbShaderWGSL],
// water faces into the r8uint target.
const SpecialShaderWGSL = `
struct U { rows: array<vec4f, 3>, p: vec4f, q: vec4f };
@group(0) @binding(0) var<uniform> u: U;
@group(0) @binding(1) var tex: texture_2d<u32>;
@group(0) @binding(2) var cmap: texture_2d<u32>;

struct VIn { @location(0) pos: vec3f, @location(1) uv: vec2f, @location(2) tr: vec4u };
struct VOut {
  @builtin(position) p: vec4f,
  @location(0) @interpolate(linear) uv: vec2f,
  @location(1) @interpolate(flat) tr: vec4u,
};

@vertex fn vs(v: VIn) -> VOut {
  let vp = vec3f(dot(u.rows[0].xyz, v.pos) + u.rows[0].w,
                 dot(u.rows[1].xyz, v.pos) + u.rows[1].w,
                 dot(u.rows[2].xyz, v.pos) + u.rows[2].w);
  let d = max(vp.z, u.p.y);
  var o: VOut;
  // Half a pixel to the right (u.p.w is 1 / width, in clip units 1 / width * w).
  o.p = vec4f(vp.x / u.p.x + u.p.w * d, vp.y * u.p.z, 0.5 * d, d);
  o.uv = v.uv; o.tr = v.tr;
  return o;
}

fn wrap(i: i32, n: i32) -> i32 { return ((i % n) + n) % n; }

fn at(tr: vec4u, x: i32, y: i32) -> u32 { return textureLoad(tex, vec2u(tr.x + u32(x), tr.y + u32(y)), 0).r; }

// render.FillSkyPolygon: the front layer (left half, index 0 is clear)
// scrolls twice as fast as the back layer (right half, at half the scale).
@fragment fn fsSky(v: VOut) -> @location(0) vec4u {
  let uv = v.uv + 0.5 * dpdxFine(v.uv);
  let halfW = i32(v.tr.z) / 2;
  let th = i32(v.tr.w);
  let back = u.q.x * 8.0;
  let front = u.q.x * 8.0 * 2.0;
  let f = at(v.tr, wrap(i32(floor(uv.x + front)), halfW), wrap(i32(floor(uv.y)), th));
  if (f != 0u) { return vec4u(f, 0u, 0u, 0u); }
  let b = at(v.tr, halfW + wrap(i32(floor(uv.x * 0.5 + back)), halfW), wrap(i32(floor(uv.y * 0.5)), th));
  return vec4u(b, 0u, 0u, 0u);
}
`
