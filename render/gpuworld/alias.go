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
// translated and viewed, then perspective-divided and truncated to integer
// screen coordinates (R_AliasTransformAndProjectFinalVerts); a triangle is
// dropped when any vertex is nearer than render.AliasClipPlane or when
// d_xdenom is not negative. Each vertex gets R_AliasTransformFinalVert's
// light from its light normal (the anorms table, written into the shader),
// the entity's r_ambientlight, r_shadelight and light vector
// (render.NewAliasLighting). The pixels are those of d_polyse.c's fixed-point
// edge walk (render.PolysetDrawTriangle): the shader works out per pixel, in
// integers, which pixels the walk's error-term steps reach, and the skin
// offset and the light it has stepped to there, and the colormap row is
// (light & 0xFF00) >> 8. No depth test: entities are drawn over the world in
// list order. Not reproduced: the recursive drawer that Quake uses for a far
// model (render.PolysetDrawSubdivTriangle).
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
		half := float32(int(m.Header.SkinWidth) >> 1) // seamfixupX16 is (skinwidth >> 1) << 16
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
		l := render.NewAliasLighting(e.Light, e.IsViewWeapon, nil, rot)
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
struct U { rows: array<vec4f, 3>, p: vec4f, q: vec4f, o: vec4f };
struct Ent { r0: vec4f, r1: vec4f, r2: vec4f, scale: vec4f, so: vec4f, info: vec4u, skin: vec4u, lv: vec4f };
@group(0) @binding(0) var<uniform> u: U;
@group(0) @binding(1) var skins: texture_2d<u32>;
@group(0) @binding(2) var cmap: texture_2d<u32>;
@group(0) @binding(3) var<storage, read> poses: array<u32>;
@group(0) @binding(4) var<storage, read> corners: array<vec4u>;
@group(0) @binding(5) var<storage, read> ents: array<Ent>;

// The triangle's three final vertices (d_polyse.c finalvert_t), flat: u, v,
// s, t (16.16) of each, and their lights.
struct VOut {
  @builtin(position) p: vec4f,
  @location(0) @interpolate(flat) a: vec4i,
  @location(1) @interpolate(flat) b: vec4i,
  @location(2) @interpolate(flat) c: vec4i,
  @location(3) @interpolate(flat) l: vec4i,
  @location(4) @interpolate(flat) skin: vec4u,
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

// R_AliasTransformAndProjectFinalVerts: u and v are (x * xscale * zi) +
// xcenter and the same for y (down), truncated to integers; xscale is
// halfW / tan(fov / 2) and the centres are half the frame less half a pixel.
fn project(vp: vec3f) -> vec2i {
  let scale = u.q.y * 0.5 / u.p.x;
  let zi = 1.0 / vp.z;
  let xf = (vp.x * scale) * zi + (u.q.y * 0.5 - 0.5);
  let yf = (-vp.y * scale) * zi + (u.q.z * 0.5 - 0.5);
  return vec2i(i32(xf), i32(yf));
}

// One of the three final vertices of the triangle: u, v, s, t (16.16, the
// skin x and y of the corner, with the seam fixup already in s).
fn finalVert(c: vec4u, q: vec2i) -> vec4i {
  return vec4i(q.x, q.y, i32(bitcast<f32>(c.y)) << 16u, i32(bitcast<f32>(c.z)) << 16u);
}

// A triangle is one non-indexed run of three vertices; each invocation builds
// the whole triangle's final vertices (the model's pose lerped, transformed
// and projected, as R_AliasTransformAndProjectFinalVerts does), drops it when
// a vertex is nearer than ALIAS_Z_CLIP_PLANE or d_xdenom >= 0, and emits the
// corner of a right triangle that covers the bounding box of the pixels the
// edge walk can reach. The fragment shader decides which of them it takes.
@vertex fn vs(@builtin(vertex_index) vidx: u32, @builtin(instance_index) inst: u32) -> VOut {
  let e = ents[inst];
  let base = (vidx / 3u) * 3u;
  let c0 = corners[base]; let c1 = corners[base + 1u]; let c2 = corners[base + 2u];
  let l0 = lerped(e, c0.x); let l1 = lerped(e, c1.x); let l2 = lerped(e, c2.x);
  let v0 = view(e, l0.xyz); let v1 = view(e, l1.xyz); let v2 = view(e, l2.xyz);
  var o: VOut;
  o.p = vec4f(0.0, 0.0, 0.0, 1.0);
  o.a = vec4i(0); o.b = vec4i(0); o.c = vec4i(0); o.l = vec4i(0); o.skin = e.skin;
  if (v0.z < 5.0 || v1.z < 5.0 || v2.z < 5.0) { return o; }
  let q0 = project(v0); let q1 = project(v1); let q2 = project(v2);
  let xdenom = (q0.y - q1.y) * (q0.x - q2.x) - (q0.x - q1.x) * (q0.y - q2.y);
  if (xdenom >= 0) { return o; }
  o.a = finalVert(c0, q0); o.b = finalVert(c1, q1); o.c = finalVert(c2, q2);
  o.l = vec4i(i32(vlight(e, u32(l0.w))), i32(vlight(e, u32(l1.w))), i32(vlight(e, u32(l2.w))), 0);
  let mn = min(q0, min(q1, q2)); let mx = max(q0, max(q1, q2));
  let w = f32(mx.x + 1 - mn.x); let h = f32(mx.y + 1 - mn.y);
  var px = f32(mn.x); var py = f32(mn.y);
  let k = vidx - base;
  // legs of 2w and 2h cover the box: the last pixel centre, (w - 0.5, h - 0.5),
  // is inside the hypotenuse (it lies at x/2w + y/2h = 1 - 1/(4w) - 1/(4h)).
  if (k == 1u) { px += 2.0 * w; }
  if (k == 2u) { py += 2.0 * h; }
  o.p = vec4f(px / (u.q.y * 0.5) - 1.0, 1.0 - py / (u.q.z * 0.5), 0.0, 1.0);
  return o;
}

// D_PolysetSetEdgeTable and the twelve edge tables of d_polyse.c: which
// vertices bound the left and the right side of the triangle, top part first.
var<private> leftTab = array<vec3i, 12>(vec3i(0, 2, -1), vec3i(1, 0, 2), vec3i(0, 2, -1), vec3i(1, 0, -1), vec3i(0, 2, 1), vec3i(2, 1, -1),
  vec3i(2, 1, -1), vec3i(2, 1, 0), vec3i(1, 0, -1), vec3i(2, 1, -1), vec3i(1, 0, -1), vec3i(0, 2, -1));
var<private> rightTab = array<vec3i, 12>(vec3i(0, 1, 2), vec3i(1, 2, -1), vec3i(1, 2, -1), vec3i(1, 2, 0), vec3i(0, 1, -1), vec3i(2, 0, -1),
  vec3i(2, 0, 1), vec3i(2, 0, -1), vec3i(1, 2, -1), vec3i(0, 1, -1), vec3i(2, 0, -1), vec3i(0, 1, -1));
var<private> numLeft = array<i32, 12>(1, 2, 1, 1, 2, 1, 1, 2, 1, 1, 1, 1);
var<private> numRight = array<i32, 12>(2, 1, 1, 2, 1, 1, 2, 1, 1, 1, 1, 1);

fn edgeTable(y0: i32, y1: i32, y2: i32) -> i32 {
  var index = 0;
  if (y0 >= y1) {
    if (y0 == y1) {
      if (y0 < y2) { return 2; }
      return 5;
    }
    index = 1;
  }
  if (y0 == y2) {
    if (index != 0) { return 8; }
    return 9;
  } else if (y1 == y2) {
    if (index != 0) { return 10; }
    return 11;
  }
  if (y0 > y2) { index += 2; }
  if (y1 > y2) { index += 4; }
  return index;
}

fn floorDiv(a: i32, b: i32) -> i32 {
  var q = a / b;
  if ((a % b != 0) && ((a < 0) != (b < 0))) { q -= 1; }
  return q;
}

// ceil(a / b) for b > 0: the column of a row j below the top of an edge is
// top + ceil(j dx / dy), which is what D_PolysetScanLeftEdge's error term steps
// out (errorterm starts at -1 and takes the remainder of dx / dy each row).
fn ceilDiv(a: i32, b: i32) -> i32 { return -floorDiv(-a, max(b, 1)); }

// D_RasterizeAliasPolySmooth per pixel: the walk's column, skin position and
// light are linear in the row and the column moved (the integer steps of the
// walk add up exactly), so each fragment works out its own row's span from
// the three vertices, takes the pixel if it is inside [left, right), and
// reads the skin offset (s >> 16) + (t >> 16) * skinwidth with the light
// stepped to it: acolormap[texel + (light & 0xFF00)].
@fragment fn fs(v: VOut) -> @location(0) vec4u {
  let px = i32(floor(v.p.x)); let py = i32(floor(v.p.y));
  var pv = array<vec4i, 3>(v.a, v.b, v.c);
  var pl = array<i32, 3>(v.l.x, v.l.y, v.l.z);
  let r0 = pv[0]; let r1 = pv[1]; let r2 = pv[2];
  let ti = edgeTable(r0.y, r1.y, r2.y);
  let lt = leftTab[ti]; let rt = rightTab[ti];
  let topy = pv[lt.x].y;
  let h1 = pv[lt.y].y - topy;
  var h2 = 0;
  if (numLeft[ti] == 2) { h2 = pv[lt.z].y - pv[lt.y].y; }
  let i = py - topy;
  if (i < 0 || i >= h1 + h2) { discard; }
  // the left edge's segment of this row
  var seg = 0; var j = i; var tvi = lt.x; var bvi = lt.y;
  if (i >= h1) { seg = 1; j = i - h1; tvi = lt.y; bvi = lt.z; }
  let tv = pv[tvi]; let bv = pv[bvi];
  let dx = bv.x - tv.x; let dy = bv.y - tv.y;
  let xl = tv.x + ceilDiv(j * dx, dy);
  // the right edge's
  let hr1 = pv[rt.y].y - pv[rt.x].y;
  var rti = rt.x; var rbi = rt.y; var jr = i;
  if (numRight[ti] == 2 && i >= hr1) { rti = rt.y; rbi = rt.z; jr = i - hr1; }
  let rtv = pv[rti]; let rbv = pv[rbi];
  let xr = rtv.x + ceilDiv(jr * (rbv.x - rtv.x), rbv.y - rtv.y);
  if (px < xl || px >= xr) { discard; }
  // D_PolysetCalcGradients (float32; the ceil for the light only)
  let xdenom = (r0.y - r1.y) * (r0.x - r2.x) - (r0.x - r1.x) * (r0.y - r2.y);
  let p00 = f32(r0.x - r2.x); let p01 = f32(r0.y - r2.y);
  let p10 = f32(r1.x - r2.x); let p11 = f32(r1.y - r2.y);
  let xinv = 1.0 / f32(xdenom); let yinv = -xinv;
  let ds0 = f32(r0.z - r2.z); let ds1 = f32(r1.z - r2.z);
  let dt0 = f32(r0.w - r2.w); let dt1 = f32(r1.w - r2.w);
  let dl0 = f32(pl[0] - pl[2]); let dl1 = f32(pl[1] - pl[2]);
  let sstepx = i32((ds1 * p01 - ds0 * p11) * xinv); let sstepy = i32((ds1 * p00 - ds0 * p10) * yinv);
  let tstepx = i32((dt1 * p01 - dt0 * p11) * xinv); let tstepy = i32((dt1 * p00 - dt0 * p10) * yinv);
  let lstepx = i32(ceil((dl1 * p01 - dl0 * p11) * xinv)); let lstepy = i32(ceil((dl1 * p00 - dl0 * p10) * yinv));
  // the walk's position at the start of this row and in the row. The second
  // left segment starts with the vertex's fractions dropped.
  var s0 = tv.z; var t0 = tv.w;
  if (seg == 1) { s0 = (tv.z >> 16u) << 16u; t0 = (tv.w >> 16u) << 16u; }
  var wl = lstepx;
  if (floorDiv(dx, max(dy, 1)) < 0) { wl -= 1; } // bias toward overflow along a leftward edge
  let off = xl - tv.x; let k = px - xl;
  let ps = s0 + j * sstepy + off * sstepx + k * sstepx;
  let pt = t0 + j * tstepy + off * tstepx + k * tstepx;
  let light = pl[tvi] + j * lstepy + wl * off + k * lstepx;
  let sw = i32(v.skin.z); let sh = i32(v.skin.w);
  let idx = clamp((ps >> 16u) + (pt >> 16u) * sw, 0, sw * sh - 1);
  let texel = textureLoad(skins, vec2u(v.skin.x + u32(idx % sw), v.skin.y + u32(idx / sw)), 0).r;
  let row = u32(clamp((light & 0xFF00) >> 8u, 0, 63));
  return vec4u(textureLoad(cmap, vec2u(texel, row), 0).r, 0u, 0u, 0u);
}
`
