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
	// FaceTab is 64 bytes per drawn face: the plane (normal, dist), the
	// texture axes with offsets, texturemins and extents, as the water
	// shader needs to rebuild D_CalcGradients each frame.
	FaceTab []byte
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
			binary.LittleEndian.PutUint32(vb[36:], uint32(s.Faces)) // index in FaceTab
			s.Vertices = append(s.Vertices, vb[:]...)
		}
		s.First[f.Idx] = uint32(len(s.Indices))
		for i := 1; i+1 < n; i++ {
			s.Indices = append(s.Indices, base, base+uint32(i), base+uint32(i+1))
		}
		s.Count[f.Idx] = uint32(len(s.Indices)) - s.First[f.Idx]
		pl := f.Plane
		for _, x := range [16]float32{
			pl.Normal[0], pl.Normal[1], pl.Normal[2], pl.Dist,
			pl.AxisS[0], pl.AxisS[1], pl.AxisS[2], pl.OffS,
			pl.AxisT[0], pl.AxisT[1], pl.AxisT[2], pl.OffT,
			pl.MinS, pl.MinT, float32(pl.ExtS), float32(pl.ExtT),
		} {
			s.FaceTab = binary.LittleEndian.AppendUint32(s.FaceTab, math.Float32bits(x))
		}
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

// TurbShaderWGSL is [SpecialShaderWGSL] plus fsTurb: Quake's Turbulent8 per
// fragment (render.TurbSpan). The gradients of D_CalcGradients are rebuilt
// per fragment from the face's plane and the view (the 16-pixel sub-spans are
// not reproduced: the coordinates are perspective-exact at every pixel);
// s and t are 16.16 fixed point relative to the face's texturemins, clamped to
// [0, extent]; the warp is render.QuakeTurbTable (sintable), written into the
// shader, indexed by the other coordinate's integer part modulo 128 plus
// (int)(time * 20) modulo 128; the texel is written as is (water is not lit).
func TurbShaderWGSL() string {
	var b strings.Builder
	b.WriteString(SpecialShaderWGSL)
	b.WriteString("\nvar<private> sintable = array<i32, 256>(")
	for i, v := range render.QuakeTurbTable {
		if i > 0 {
			b.WriteString(", ")
		}
		b.WriteString(strconv.Itoa(int(v)))
	}
	b.WriteString(`);

@fragment fn fsTurb(v: VOut) -> @location(0) vec4u {
  let f0 = faces[v.fi * 4u]; let f1 = faces[v.fi * 4u + 1u];
  let f2 = faces[v.fi * 4u + 2u]; let f3 = faces[v.fi * 4u + 3u];
  let W = u.q.y; let H = u.q.z;
  let halfW = W * 0.5; let halfH = H * 0.5;
  let X = floor(v.p.x) + 0.5; let Y = floor(v.p.y) + 0.5;
  let right = u.rows[0].xyz; let up = u.rows[1].xyz; let fwd = u.rows[2].xyz;
  let D = fwd + (X - halfW) * u.p.x / halfW * right + (halfH - Y) * u.p.x / halfW * up;
  let org = u.o.xyz;
  let nd = dot(f0.xyz, D);
  let z = (f0.w - dot(f0.xyz, org)) / nd;
  let cs = dot(f1.xyz, org) + f1.w - f3.x;
  let ct = dot(f2.xyz, org) + f2.w - f3.y;
  var s = i32((cs + z * dot(f1.xyz, D)) * 65536.0);
  var t = i32((ct + z * dot(f2.xyz, D)) * 65536.0);
  s = clamp(s, 0, i32(f3.z) * 65536 - 1);
  t = clamp(t, 0, i32(f3.w) * 65536 - 1);
  let off = i32(u.q.x * 20.0) & 127;
  let sturb = s + sintable[off + ((t >> 16u) & 127)];
  let tturb = t + sintable[off + ((s >> 16u) & 127)];
  let tw = i32(v.tr.z); let th = i32(v.tr.w);
  let col = i32((u32(sturb) >> 16u) % u32(tw));
  let row = i32((u32(tturb) >> 16u) % u32(th));
  return vec4u(at(v.tr, col, row), 0u, 0u, 0u);
}
`)
	return b.String()
}

// SpecialShaderWGSL draws sky faces (fsSky) and, with [TurbShaderWGSL],
// water faces into the r8uint target.
const SpecialShaderWGSL = `
struct U { rows: array<vec4f, 3>, p: vec4f, q: vec4f, o: vec4f };
@group(0) @binding(0) var<uniform> u: U;
@group(0) @binding(1) var tex: texture_2d<u32>;
@group(0) @binding(2) var cmap: texture_2d<u32>;
@group(0) @binding(3) var<storage, read> faces: array<vec4f>;

struct VIn { @location(0) pos: vec3f, @location(1) uv: vec2f, @location(2) tr: vec4u, @location(3) fi: u32 };
struct VOut {
  @builtin(position) p: vec4f,
  @location(0) @interpolate(linear) uv: vec2f,
  @location(1) @interpolate(flat) tr: vec4u,
  @location(2) @interpolate(flat) fi: u32,
};

@vertex fn vs(v: VIn) -> VOut {
  let vp = vec3f(dot(u.rows[0].xyz, v.pos) + u.rows[0].w,
                 dot(u.rows[1].xyz, v.pos) + u.rows[1].w,
                 dot(u.rows[2].xyz, v.pos) + u.rows[2].w);
  let d = max(vp.z, u.p.y);
  var o: VOut;
  // Half a pixel to the right (u.p.w is 1 / width, in clip units 1 / width * w).
  o.p = vec4f(vp.x / u.p.x + u.p.w * d, vp.y * u.p.z, 0.5 * d, d);
  o.uv = v.uv; o.tr = v.tr; o.fi = v.fi;
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
