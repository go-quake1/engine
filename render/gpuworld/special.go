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
	// FaceTab is FaceTabStride bytes per drawn face: the plane (normal,
	// dist), the texture axes with offsets, texturemins and extents, as the
	// water shader needs to rebuild D_CalcGradients each frame, and where the
	// face's polygon is in FaceVerts (first vertex, count).
	FaceTab []byte
	// FaceVerts is the faces' world-space polygons, 16 bytes per vertex: the
	// shaders project them to find the scanline run a pixel belongs to, from
	// whose first pixel the reference's sub-spans start.
	FaceVerts []byte
}

// FaceTabStride is the size of one face's record in [Special.FaceTab].
const FaceTabStride = 80

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
		first := uint32(len(s.FaceVerts) / 16)
		for i := 0; i < n; i++ {
			p := f.Verts.Vert(i)
			for _, x := range [4]float32{p[0], p[1], p[2], 0} {
				s.FaceVerts = binary.LittleEndian.AppendUint32(s.FaceVerts, math.Float32bits(x))
			}
		}
		for _, x := range [20]float32{
			pl.Normal[0], pl.Normal[1], pl.Normal[2], pl.Dist,
			pl.AxisS[0], pl.AxisS[1], pl.AxisS[2], pl.OffS,
			pl.AxisT[0], pl.AxisT[1], pl.AxisT[2], pl.OffT,
			pl.MinS, pl.MinT, float32(pl.ExtS), float32(pl.ExtT),
			float32(first), float32(n), 0, 0,
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

// s and t of D_CalcGradients at the pixel (xp, yp), 16.16, clamped to the
// face's extents, exact (what Turbulent8 computes with a divide).
fn turbAt(f0: vec4f, f1: vec4f, f2: vec4f, f3: vec4f, xp: i32, yp: i32) -> vec2i {
  let W = u.q.y; let H = u.q.z;
  let halfW = W * 0.5; let halfH = H * 0.5;
  let X = f32(xp) + 0.5; let Y = f32(yp) + 0.5;
  let right = u.rows[0].xyz; let up = u.rows[1].xyz; let fwd = u.rows[2].xyz;
  let D = fwd + (X - halfW) * u.p.x / halfW * right + (halfH - Y) * u.p.x / halfW * up;
  let org = u.o.xyz;
  let nd = dot(f0.xyz, D);
  let z = (f0.w - dot(f0.xyz, org)) / nd;
  let cs = dot(f1.xyz, org) + f1.w - f3.x;
  let ct = dot(f2.xyz, org) + f2.w - f3.y;
  let s = i32((cs + z * dot(f1.xyz, D)) * 65536.0);
  let t = i32((ct + z * dot(f2.xyz, D)) * 65536.0);
  return vec2i(clamp(s, 0, i32(f3.z) * 65536 - 1), clamp(t, 0, i32(f3.w) * 65536 - 1));
}

// A sub-span's end value as Turbulent8 limits it: at most the extent, at
// least 16.
fn clampNext(v: vec2i, f3: vec4f) -> vec2i {
  return vec2i(clamp(v.x, 16, i32(f3.z) * 65536 - 1), clamp(v.y, 16, i32(f3.w) * 65536 - 1));
}

// Turbulent8 (d_scan.c): the face's run on this scanline is cut into
// sub-spans of 16 pixels from its first pixel; s and t are exact at the start
// of each and at its end, stepped linearly between (a shift by 4 for a full
// sub-span, a division by (count - 1) for the last), the start of every
// sub-span after the first limited as its end was.
@fragment fn fsTurb(v: VOut) -> @location(0) vec4u {
  let f0 = faces[v.fi * 5u]; let f1 = faces[v.fi * 5u + 1u];
  let f2 = faces[v.fi * 5u + 2u]; let f3 = faces[v.fi * 5u + 3u];
  let f4 = faces[v.fi * 5u + 4u];
  let px = i32(floor(v.p.x)); let py = i32(floor(v.p.y));
  var run = runOf(u32(f4.x), u32(f4.y), py);
  if (run.x > run.y) { run = vec2i(px, px); }
  let k = max(px - run.x, 0) / 16;
  let xa = run.x + 16 * k;
  let remain = run.y - xa + 1;
  let sc = clamp(remain, 1, 16);
  var start = turbAt(f0, f1, f2, f3, xa, py);
  if (k > 0) { start = clampNext(start, f3); }
  var stp = vec2i(0);
  if (remain > 16) {
    let nx = clampNext(turbAt(f0, f1, f2, f3, xa + 16, py), f3);
    stp = (nx - start) >> vec2u(4u);
  } else if (sc > 1) {
    let nx = clampNext(turbAt(f0, f1, f2, f3, xa + sc - 1, py), f3);
    stp = vec2i((nx.x - start.x) / (sc - 1), (nx.y - start.y) / (sc - 1));
  }
  let st = (start & vec2i(8388607)) + vec2i(px - xa) * stp; // 8388607 = (TURB_CYCLE << 16) - 1
  let s = st.x; let t = st.y;
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
@group(0) @binding(4) var<storage, read> faceVerts: array<vec4f>;

// The screen position of a world point as bsprender.TransformFace gives it:
// depth clamped to the near plane.
fn proj(w: vec3f) -> vec2f {
  let vp = vec3f(dot(u.rows[0].xyz, w) + u.rows[0].w,
                 dot(u.rows[1].xyz, w) + u.rows[1].w,
                 dot(u.rows[2].xyz, w) + u.rows[2].w);
  let d = max(vp.z, u.p.y);
  let halfW = u.q.y * 0.5;
  let scale = halfW / u.p.x;
  return vec2f(halfW + vp.x * scale / d, u.q.z * 0.5 - vp.y * scale / d);
}

// The scanline run of a face the reference fills at pixel row py, as
// (first pixel, last pixel): from the polygon's edges at the row centre, a
// pixel taken when its left edge is inside; first > last when there is none.
fn runOf(first: u32, n: u32, py: i32) -> vec2i {
  let yf = f32(py) + 0.5;
  var xl = 1.0e30; var xr = -1.0e30;
  var prev = proj(faceVerts[first + n - 1u].xyz);
  for (var i = 0u; i < 64u; i++) { // render.MaxPolyVerts, broken at n (see ShaderWGSL)
    if (i >= n) { break; }
    let cur = proj(faceVerts[first + i].xyz);
    if ((prev.y <= yf && cur.y > yf) || (cur.y <= yf && prev.y > yf)) {
      let x = prev.x + (yf - prev.y) / (cur.y - prev.y) * (cur.x - prev.x);
      xl = min(xl, x); xr = max(xr, x);
    }
    prev = cur;
  }
  if (xl > xr) { return vec2i(1, 0); }
  return vec2i(max(i32(ceil(xl)), 0), min(i32(floor(xr)), i32(u.q.y) - 1));
}

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
// D_Sky_uv_To_st: the 16.16 sky coordinates of pixel (xp, yp).
fn skyST(xp: i32, yp: i32) -> vec2i {
  let W = u.q.y; let H = u.q.z;
  let temp = max(W, H);
  let wu = 8192.0 * f32(xp - i32(floor(W * 0.5))) / temp;
  let wv = 8192.0 * f32(i32(floor(H * 0.5)) - yp) / temp;
  var e = 4096.0 * u.rows[2].xyz + wu * u.rows[0].xyz + wv * u.rows[1].xyz;
  e.z = e.z * 3.0;
  e = normalize(e);
  let off = u.q.w * 8.0;
  return vec2i(i32((off + 378.0 * e.x) * 65536.0), i32((off + 378.0 * e.y) * 65536.0));
}

// D_DrawSkyScans8: the face's run on this scanline is cut into sub-spans of
// 32 pixels from its first pixel; s and t are exact at the start of each and
// at its end, stepped linearly between (a shift by 5, a division by (count -
// 1) for the last).
@fragment fn fsSky(v: VOut) -> @location(0) vec4u {
  let f4 = faces[v.fi * 5u + 4u];
  let px = i32(floor(v.p.x)); let py = i32(floor(v.p.y));
  var run = runOf(u32(f4.x), u32(f4.y), py);
  if (run.x > run.y) { run = vec2i(px, px); }
  let k = max(px - run.x, 0) / 32;
  let xa = run.x + 32 * k;
  let remain = run.y - xa + 1;
  let sc = clamp(remain, 1, 32);
  let start = skyST(xa, py);
  var stp = vec2i(0);
  if (remain > 32) {
    stp = (skyST(xa + 32, py) - start) >> vec2u(5u);
  } else if (sc > 1) {
    let nx = skyST(xa + sc - 1, py);
    stp = vec2i((nx.x - start.x) / (sc - 1), (nx.y - start.y) / (sc - 1));
  }
  let st = start + vec2i(px - xa) * stp;
  let off = u.q.w * 8.0;
  let s = st.x; let t = st.y;
  let col = (s >> 16u) & 127;
  let row = (t >> 16u) & 127;
  let shift = i32(off);
  let f = at(v.tr, (col + shift) & 127, (row + shift) & 127);
  if (f != 0u) { return vec4u(f, 0u, 0u, 0u); }
  return vec4u(at(v.tr, 128 + col, row), 0u, 0u, 0u);
}
`
