// Copyright (c) 2026 the go-quake1/engine authors.
// SPDX-License-Identifier: GPL-2.0-or-later

package gpuworld

import (
	"encoding/binary"
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/go-quake1/engine/anorms"
	"github.com/go-quake1/engine/render"
	"github.com/go-quake1/engine/worldview"
)

// This file is the alias-model (monsters, items, the view weapon) half of
// the package that does not touch the device: the pose, triangle and skin
// data built once, and the per-frame entity records and draw list.
//
// What the shader reproduces is render.DrawAliasInterpQuake, the engine's
// alias pass with Quake's lighting: the vertices are the model's bytes,
// blended between two frames in byte space and rounded (R_AliasBlendPoseVerts),
// decoded with the model's scale and origin, rotated by render.EntityRotation,
// translated and viewed, then perspective-divided; a triangle is dropped when
// any vertex is nearer than AliasNearClip or when its screen winding is the
// back-facing one. Each vertex gets R_AliasTransformFinalVert's light from its
// light normal (the anorms table, written into the shader), the entity's
// r_ambientlight, r_shadelight and light vector (render.NewAliasLighting);
// the light is interpolated across the triangle (Gouraud, as d_polyse.c) and
// the colormap row is (int(light) & 0xFF00) >> 8. The skin coordinates are
// interpolated linearly in screen space (not perspective-correct, as
// render.FillTexturedPolygon does), wrapped into the skin. No depth test:
// entities are drawn over the world in list order.
//
// The pose is packed as one uint32 per vertex per frame:
// x | y << 8 | z << 16 | light normal index << 24.

// EntRecordSize is the size of one entity record in bytes.
const EntRecordSize = 128

// CornerSize is the size of one triangle corner in bytes.
const CornerSize = 16

type aliasModel struct {
	poseBase   []uint32 // by frame: first vertex of the pose in Poses
	cornerBase uint32   // first corner (3 per triangle)
	ntris      uint32
	skin       []Rect // by skin
}

// AliasSet is the GPU-side content of a set of alias models.
type AliasSet struct {
	Poses   []uint32
	Corners []byte // CornerSize bytes each: vertex index, s, t as float32 bits
	Skins   *Atlas
	models  map[*worldview.AliasModel]*aliasModel
}

// BuildAlias packs the models' poses, triangles and skins.
func BuildAlias(models []*worldview.AliasModel) (*AliasSet, error) {
	s := &AliasSet{models: map[*worldview.AliasModel]*aliasModel{}}
	skins := newShelf(MaxAtlasHeight)
	for _, am := range models {
		if _, dup := s.models[am]; dup {
			continue
		}
		m := am.M
		mm := &aliasModel{cornerBase: uint32(len(s.Corners) / CornerSize), ntris: uint32(len(m.Triangles))}
		for _, f := range m.Frames {
			pose := render.FramePoseAt(f, 0)
			mm.poseBase = append(mm.poseBase, uint32(len(s.Poses)))
			for _, tv := range pose {
				s.Poses = append(s.Poses, uint32(tv.V[0])|uint32(tv.V[1])<<8|uint32(tv.V[2])<<16|uint32(tv.LightNormalIndex)<<24)
			}
		}
		half := float32(m.Header.SkinWidth) / 2
		for _, t := range m.Triangles {
			for k := 0; k < 3; k++ {
				vi := t.VertIndex[k]
				if vi < 0 || int(vi) >= len(m.STVerts) {
					return nil, fmt.Errorf("%s: vertex index %d out of range", am.Path, vi)
				}
				st := m.STVerts[vi]
				sf := float32(st.S)
				if t.FacesFront == 0 && st.OnSeam != 0 {
					sf += half
				}
				var c [CornerSize]byte
				binary.LittleEndian.PutUint32(c[0:], uint32(vi))
				binary.LittleEndian.PutUint32(c[4:], math.Float32bits(sf))
				binary.LittleEndian.PutUint32(c[8:], math.Float32bits(float32(st.T)))
				s.Corners = append(s.Corners, c[:]...)
			}
		}
		for _, sk := range am.Skins {
			sk := sk
			r, err := skins.place(sk, sk.Width, sk.Height, func(a *Atlas, r Rect) { blitRows(a, r, sk.Pixels, sk.Width) })
			if err != nil {
				return nil, err
			}
			mm.skin = append(mm.skin, r)
		}
		s.models[am] = mm
	}
	s.Skins = skins.finish()
	if len(s.Poses) == 0 {
		s.Poses = []uint32{0}
		s.Corners = make([]byte, CornerSize)
	}
	return s, nil
}

// AliasDraw is one entity's non-indexed draw: its triangle corners, and the
// entity record it reads (as the instance).
type AliasDraw struct{ First, Count, Instance uint32 }

// Entities encodes the frame's entity records into out (grown as needed)
// and returns the draw list. An entity whose model is not in the set, or
// whose frame is out of range, is skipped.
func (s *AliasSet) Entities(ents []worldview.AliasEnt, out []byte, draws []AliasDraw) ([]byte, []AliasDraw, error) {
	out, draws = out[:0], draws[:0]
	for _, e := range ents {
		mm := s.models[e.Model]
		if mm == nil {
			return nil, nil, fmt.Errorf("gpuworld: model %s not in the set", e.Model.Path)
		}
		if e.Frame < 0 || e.Frame >= len(mm.poseBase) || e.Next < 0 || e.Next >= len(mm.poseBase) || e.Skin < 0 || e.Skin >= len(mm.skin) {
			return nil, nil, fmt.Errorf("gpuworld: %s: frame %d/%d or skin %d out of range", e.Model.Path, e.Frame, e.Next, e.Skin)
		}
		rot := render.EntityRotation(e.Angles[0], e.Angles[1], e.Angles[2])
		var rec [EntRecordSize]byte
		f := func(o int, x float32) { binary.LittleEndian.PutUint32(rec[o:], math.Float32bits(x)) }
		u := func(o int, x uint32) { binary.LittleEndian.PutUint32(rec[o:], x) }
		for r := 0; r < 3; r++ {
			for c := 0; c < 3; c++ {
				f(16*r+4*c, rot[r][c])
			}
			f(16*r+12, e.Origin[r])
		}
		h := e.Model.M.Header
		for c := 0; c < 3; c++ {
			f(48+4*c, h.Scale[c])
			f(64+4*c, h.ScaleOrigin[c])
		}
		f(60, e.Lerp)
		l := render.NewAliasLighting(render.AliasStandInLight, e.IsViewWeapon, nil, rot)
		f(76, float32(l.Ambient))
		f(112, l.Vec[0])
		f(116, l.Vec[1])
		f(120, l.Vec[2])
		f(124, l.Shade)
		u(80, mm.poseBase[e.Frame])
		u(84, mm.poseBase[e.Next])
		sr := mm.skin[e.Skin]
		u(96, sr.X)
		u(100, sr.Y)
		u(104, sr.W)
		u(108, sr.H)
		draws = append(draws, AliasDraw{First: mm.cornerBase * 1, Count: mm.ntris * 3, Instance: uint32(len(out) / EntRecordSize)})
		out = append(out, rec[:]...)
	}
	return out, draws, nil
}

// AliasShaderWGSL draws alias entities into the r8uint target; see the
// comment at the top of this file. It carries the anorms table (r_avertexnormals).
var AliasShaderWGSL = aliasShader()

func aliasShader() string {
	var b strings.Builder
	b.WriteString("var<private> anorms = array<vec3f, 162>(")
	for i, n := range anorms.Table {
		if i > 0 {
			b.WriteString(", ")
		}
		fmt.Fprintf(&b, "vec3f(%s, %s, %s)", wgslFloat(n[0]), wgslFloat(n[1]), wgslFloat(n[2]))
	}
	b.WriteString(");\n")
	b.WriteString(aliasShaderBody)
	return b.String()
}

// wgslFloat formats a float32 as a WGSL float literal that parses back to it.
func wgslFloat(v float32) string {
	s := strconv.FormatFloat(float64(v), 'g', -1, 32)
	if !strings.ContainsAny(s, ".e") {
		s += ".0"
	}
	return s
}

const aliasShaderBody = `
struct U { rows: array<vec4f, 3>, p: vec4f };
struct Ent { r0: vec4f, r1: vec4f, r2: vec4f, scale: vec4f, so: vec4f, info: vec4u, skin: vec4u, lv: vec4f };
@group(0) @binding(0) var<uniform> u: U;
@group(0) @binding(1) var skins: texture_2d<u32>;
@group(0) @binding(2) var cmap: texture_2d<u32>;
@group(0) @binding(3) var<storage, read> poses: array<u32>;
@group(0) @binding(4) var<storage, read> corners: array<vec4u>;
@group(0) @binding(5) var<storage, read> ents: array<Ent>;

struct VOut {
  @builtin(position) p: vec4f,
  @location(0) @interpolate(linear) uv: vec2f,
  @location(1) @interpolate(linear) light: f32,
  @location(2) @interpolate(flat) skin: vec4u,
};

// R_AliasTransformFinalVert's light: r_ambientlight, less (int)(r_shadelight *
// lightcos) when the normal faces the light, not below zero.
fn vlight(e: Ent, idx: u32) -> f32 {
  let cosv = dot(anorms[min(idx, 161u)], e.lv.xyz);
  var temp = i32(e.so.w);
  if (cosv < 0.0) { temp = max(temp + i32(e.lv.w * cosv), 0); }
  return f32(temp);
}

fn lerped(e: Ent, vi: u32) -> vec4f {
  let pa = poses[e.info.x + vi];
  let pb = poses[e.info.y + vi];
  let t = e.scale.w;
  let a = vec3f(f32(pa & 255u), f32((pa >> 8u) & 255u), f32((pa >> 16u) & 255u));
  let b = vec3f(f32(pb & 255u), f32((pb >> 8u) & 255u), f32((pb >> 16u) & 255u));
  let m = floor(a * (1.0 - t) + b * t + vec3f(0.5));
  var normal = pa >> 24u;
  if (t >= 0.5) { normal = pb >> 24u; }
  return vec4f(m, f32(normal));
}

fn view(e: Ent, m: vec3f) -> vec3f {
  let obj = m * e.scale.xyz + e.so.xyz;
  let w = vec3f(dot(e.r0.xyz, obj) + e.r0.w, dot(e.r1.xyz, obj) + e.r1.w, dot(e.r2.xyz, obj) + e.r2.w);
  return vec3f(dot(u.rows[0].xyz, w) + u.rows[0].w, dot(u.rows[1].xyz, w) + u.rows[1].w, dot(u.rows[2].xyz, w) + u.rows[2].w);
}

fn ndc(vp: vec3f) -> vec2f { return vec2f(vp.x / (u.p.x * vp.z), -vp.y * u.p.z / vp.z); }

@vertex fn vs(@builtin(vertex_index) vidx: u32, @builtin(instance_index) inst: u32) -> VOut {
  let e = ents[inst];
  let base = (vidx / 3u) * 3u;
  let c0 = corners[base]; let c1 = corners[base + 1u]; let c2 = corners[base + 2u];
  let l0 = lerped(e, c0.x); let l1 = lerped(e, c1.x); let l2 = lerped(e, c2.x);
  let v0 = view(e, l0.xyz); let v1 = view(e, l1.xyz); let v2 = view(e, l2.xyz);
  var o: VOut;
  o.p = vec4f(0.0, 0.0, 0.0, 1.0);
  o.uv = vec2f(0.0); o.light = 0.0; o.skin = e.skin;
  if (v0.z < 8.0 || v1.z < 8.0 || v2.z < 8.0) { return o; }
  let s0 = ndc(v0); let s1 = ndc(v1); let s2 = ndc(v2);
  // Screen y is flipped from the clip space y; the winding test is the
  // software one, on screen coordinates (x right, y down).
  let area = (s1.x - s0.x) * (s2.y - s0.y) - (s2.x - s0.x) * (s1.y - s0.y);
  if (area > 0.0) { return o; }
  let k = vidx - base;
  var vp = v0; var c = c0; var nrm = l0.w;
  if (k == 1u) { vp = v1; c = c1; nrm = l1.w; }
  if (k == 2u) { vp = v2; c = c2; nrm = l2.w; }
  o.light = vlight(e, u32(nrm));
  // render.FillTexturedPolygon takes pixel x when its left edge, not its
  // centre, is inside the span, and samples the texture at the centre; so
  // the polygon is moved half a pixel right (u.p.w is 1 / width: half a
  // pixel is 1 / width in clip units) and the fragment samples half a pixel
  // further along.
  o.p = vec4f(vp.x / u.p.x + u.p.w * vp.z, vp.y * u.p.z, 0.5 * vp.z, vp.z);
  o.uv = vec2f(bitcast<f32>(c.y), bitcast<f32>(c.z));
  return o;
}

fn wrap(i: i32, n: i32) -> i32 { return ((i % n) + n) % n; }

@fragment fn fs(v: VOut) -> @location(0) vec4u {
  let uv = v.uv + 0.5 * dpdxFine(v.uv);
  let tx = wrap(i32(floor(uv.x)), i32(v.skin.z));
  let ty = wrap(i32(floor(uv.y)), i32(v.skin.w));
  let texel = textureLoad(skins, vec2u(v.skin.x + u32(tx), v.skin.y + u32(ty)), 0).r;
  // d_polyse.c: acolormap[texel + (light & 0xFF00)], the row clamped here.
  let light = v.light + 0.5 * dpdxFine(v.light);
  let row = u32(clamp((i32(light) & 0xFF00) >> 8u, 0, 63));
  return vec4u(textureLoad(cmap, vec2u(texel, row), 0).r, 0u, 0u, 0u);
}
`
