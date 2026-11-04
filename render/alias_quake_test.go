// Copyright (c) 2026 the go-quake1/engine authors.
// SPDX-License-Identifier: GPL-2.0-or-later

package render

import (
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/go-quake1/engine/anorms"
	"github.com/go-quake1/engine/mdl"
)

// TestAnormsMatchTheHeader reads reference/common/include/anorms.h (the
// source's table of r_avertexnormals) and compares every entry.
func TestAnormsMatchTheHeader(t *testing.T) {
	b, err := os.ReadFile("../reference/common/include/anorms.h")
	if err != nil {
		t.Skip("the tyrquake reference is not in this checkout")
	}
	var nums []float32
	_, body, _ := strings.Cut(string(b), "*/") // skip the licence comment
	for _, f := range strings.FieldsFunc(body, func(r rune) bool {
		return r == ' ' || r == '\n' || r == '\t' || r == ',' || r == '\r' || r == '{' || r == '}'
	}) {
		if v, err := strconv.ParseFloat(f, 32); err == nil {
			nums = append(nums, float32(v))
		}
	}
	if len(nums) != 3*anorms.Count {
		t.Fatalf("%d numbers in the header, want %d", len(nums), 3*anorms.Count)
	}
	for i := 0; i < anorms.Count; i++ {
		for k := 0; k < 3; k++ {
			if anorms.Table[i][k] != nums[3*i+k] {
				t.Fatalf("normal %d component %d: %v, header %v", i, k, anorms.Table[i][k], nums[3*i+k])
			}
		}
	}
}

// TestNewAliasLighting pins R_AliasSetupLighting on worked cases:
//
//	R_LightPoint 100: ambient 100, shade 100; 100 + 100 > 192 so shade = 92;
//	  r_ambientlight = (255 - 100) << 6 = 9920, r_shadelight = 92 * 64 = 5888.
//	view model with light 10: raised to 24 first: (255 - 24) << 6 = 14784, shade 24 * 64.
//	light 100 plus a dynamic light of 80: ambient 180 -> 128, shade 100 ->
//	  192 - 128 = 64: (255 - 128) << 6 = 8128, shade 4096.
//	light 0: ambient raised to LIGHT_MIN 5: 250 << 6 = 16000, shade 0.
//	a dynamic light of -5 adds nothing.
func TestNewAliasLighting(t *testing.T) {
	id := Mat3{{1, 0, 0}, {0, 1, 0}, {0, 0, 1}}
	for _, c := range []struct {
		name    string
		light   int
		vm      bool
		dl      []int
		ambient int32
		shade   float32
	}{
		{"light 100", 100, false, nil, 9920, 5888},
		{"view model light 10", 10, true, nil, 14784, 24 * 64},
		{"dynamic light", 100, false, []int{80}, 8128, 4096},
		{"dark", 0, false, nil, 16000, 0},
		{"negative light", -10, false, nil, 16000, 0},
		{"negative dynamic light", 100, false, []int{-5}, 9920, 5888},
	} {
		l := NewAliasLighting(c.light, c.vm, c.dl, id)
		if l.Ambient != c.ambient || l.Shade != c.shade {
			t.Errorf("%s: ambient %d shade %v, want %d %v", c.name, l.Ambient, l.Shade, c.ambient, c.shade)
		}
	}
	// the light direction (0, 0, -1) in the model's frame: straight down
	// for an upright model, along +x for one pitched a quarter turn.
	if v := NewAliasLighting(100, false, nil, id).Vec; v != [3]float32{0, 0, -1} {
		t.Errorf("upright light vector %v", v)
	}
}

// TestVertexLight: normal 0 is (-0.525731, 0, 0.850651), so against the
// light (0, 0, -1) its cosine is -0.850651 and with ambient 9920, shade 5888
// the light is 9920 + int(5888 * -0.850651) = 9920 - 5008 = 4912; a normal
// facing away (the opposite of normal 0's z) keeps the ambient; shade larger
// than the ambient clamps at zero.
func TestVertexLight(t *testing.T) {
	l := AliasLighting{Ambient: 9920, Shade: 5888, Vec: [3]float32{0, 0, -1}}
	if got := l.VertexLight(0); got != 4912 {
		t.Errorf("normal 0: %d, want 4912", got)
	}
	away := AliasLighting{Ambient: 9920, Shade: 5888, Vec: [3]float32{0, 0, 1}}
	if got := away.VertexLight(0); got != 9920 {
		t.Errorf("facing away: %d, want 9920", got)
	}
	bright := AliasLighting{Ambient: 100, Shade: 5888, Vec: [3]float32{0, 0, -1}}
	if got := bright.VertexLight(0); got != 0 {
		t.Errorf("underflow: %d, want 0", got)
	}
	if l.VertexLight(255) != l.VertexLight(0) { // out of the table: normal 0
		t.Error("an out-of-range normal is not normal 0")
	}
}

func TestFillGouraudTriangleInterpolatesTheLight(t *testing.T) {
	fb := filledFB(t)
	cm := litColormap()
	skin := newAliasSkin(9, 4, 4)
	// light 0 at the left corners and 40 rows at the right one
	tri := [3]GouraudVertex{{X: 2, Y: 2, L: 0}, {X: 30, Y: 2, L: 40 * 256}, {X: 2, Y: 30, L: 0}}
	if err := FillGouraudTriangle(fb, skin, cm, tri); err != nil {
		t.Fatal(err)
	}
	left, right := fb.Pixels[3*fb.Pitch+3], fb.Pixels[3*fb.Pitch+27]
	if left >= right || right < 30 {
		t.Errorf("rows %d at the left, %d at the right: not increasing", left, right)
	}
	for _, c := range []struct {
		name string
		err  error
		got  error
	}{
		{"nil fb", ErrTexFillNilFB, FillGouraudTriangle(nil, skin, cm, tri)},
		{"nil tex", ErrTexFillNilTex, FillGouraudTriangle(fb, nil, cm, tri)},
		{"shape", ErrPicShape, FillGouraudTriangle(fb, &Pic{Width: 2, Height: 2}, cm, tri)},
	} {
		if c.got != c.err {
			t.Errorf("%s: %v", c.name, c.got)
		}
	}
	// a triangle above the frame, and a flat one, draw nothing and do not fail
	_ = FillGouraudTriangle(fb, skin, cm, [3]GouraudVertex{{X: 5, Y: -9}, {X: 9, Y: -9}, {X: 7, Y: -3}})
	_ = FillGouraudTriangle(fb, skin, cm, [3]GouraudVertex{{X: 5.2, Y: 5}, {X: 5.4, Y: 5}, {X: 5.3, Y: 9}})
	// a scanline that meets no edge (the flat bottom at y = 4.5)
	_ = FillGouraudTriangle(fb, skin, cm, [3]GouraudVertex{{X: 0, Y: 0}, {X: 10, Y: 4.5}, {X: 0, Y: 4.5}})
	// right-to-left winding
	_ = FillGouraudTriangle(fb, skin, cm, [3]GouraudVertex{{X: 30, Y: 4}, {X: 2, Y: 4}, {X: 30, Y: 20}})
}

func TestDrawAliasInterpQuake(t *testing.T) {
	fb, rd, skin := newAliasCtx(t)
	m := quakeFrontTri()
	cm := litColormap()
	ent := AliasEntityInterp{AliasEntity: AliasEntity{Origin: [3]float32{100, 0, 0}}}
	if err := DrawAliasInterpQuake(fb, rd, cm, 100, false, m, skin, ent); err != nil {
		t.Fatal(err)
	}
	// the three vertices have normal 0: light 4912 everywhere, row 4912 >> 8 = 19
	if got := fb.Pixels[98*fb.Pitch+160]; got != 19 {
		t.Errorf("centre pixel row %d, want 19", got)
	}
	// a frame blend takes the nearer pose's normals; a second frame of the same triangle
	m2 := quakeFrontTri()
	m2.Frames = append(m2.Frames, m2.Frames[0])
	for _, lerp := range []float32{0.25, 1} {
		e := ent
		e.FrameIdxNext, e.Lerp = 1, lerp
		fb2, _, _ := newAliasCtx(t)
		if err := DrawAliasInterpQuake(fb2, rd, cm, 100, false, m2, skin, e); err != nil {
			t.Fatal(err)
		}
		if fb2.Pixels[98*fb2.Pitch+160] != 19 {
			t.Errorf("lerp %v: centre pixel %d", lerp, fb2.Pixels[98*fb2.Pitch+160])
		}
	}
	// the view model is at least 24 bright
	fbv, _, _ := newAliasCtx(t)
	if err := DrawAliasInterpQuake(fbv, rd, cm, 0, true, m, skin, ent); err != nil {
		t.Fatal(err)
	}
	if got := fbv.Pixels[98*fbv.Pitch+160]; got == 0 {
		t.Errorf("view model centre row %d", got)
	}
	// culled and clipped triangles, empty frame group, bad field of view
	back := frontFacingTri() // wound the other way: back-facing for d_polyse.c
	fbc, _, _ := newAliasCtx(t)
	_ = DrawAliasInterpQuake(fbc, rd, cm, 100, false, back, skin, ent)
	near := ent
	near.Origin = [3]float32{1, 0, 0}
	_ = DrawAliasInterpQuake(fbc, rd, cm, 100, false, m, skin, near)
	seam := quakeFrontTri()
	seam.Triangles[0].FacesFront = 0
	seam.STVerts[0].OnSeam = 1
	_ = DrawAliasInterpQuake(fbc, rd, cm, 100, false, seam, skin, ent)
	empty := quakeFrontTri()
	empty.Frames[0] = mdl.Frame{Type: mdl.FrameGroup, Group: &mdl.GroupFrame{}}
	if err := DrawAliasInterpQuake(fbc, rd, cm, 100, false, empty, skin, ent); err != nil {
		t.Errorf("empty group: %v", err)
	}
	flat := *rd
	flat.FovX = 0
	if err := DrawAliasInterpQuake(fbc, &flat, cm, 100, false, m, skin, ent); err != nil {
		t.Errorf("zero fov: %v", err)
	}
	bad := ent
	bad.Lerp = 2
	badFrame := ent
	badFrame.FrameIdxNext = 9
	for _, c := range []struct {
		name string
		err  error
		got  error
	}{
		{"nil fb", ErrAliasNilFB, DrawAliasInterpQuake(nil, rd, cm, 0, false, m, skin, ent)},
		{"nil model", ErrAliasNilModel, DrawAliasInterpQuake(fbc, rd, cm, 0, false, nil, skin, ent)},
		{"nil refdef", ErrAliasNilRefDef, DrawAliasInterpQuake(fbc, nil, cm, 0, false, m, skin, ent)},
		{"nil skin", ErrAliasNilSkin, DrawAliasInterpQuake(fbc, rd, cm, 0, false, m, nil, ent)},
		{"lerp", ErrAliasInterpRange, DrawAliasInterpQuake(fbc, rd, cm, 0, false, m, skin, bad)},
		{"frame", ErrAliasBadFrame, DrawAliasInterpQuake(fbc, rd, cm, 0, false, m, skin, badFrame)},
		{"shape", ErrPicShape, DrawAliasInterpQuake(fbc, rd, cm, 0, false, m, &Pic{Width: 2, Height: 2}, ent)},
	} {
		if c.got != c.err {
			t.Errorf("%s: %v, want %v", c.name, c.got, c.err)
		}
	}
}

// quakeFrontTri is frontFacingTri wound the way d_polyse.c draws it: the
// screen-space d_xdenom is negative (clockwise on screen, y down), where
// DrawAlias's own convention is the opposite. A model drawn by the engine
// before the port was inside out: its back-facing triangles were the ones
// drawn.
func quakeFrontTri() *mdl.Model {
	m := frontFacingTri()
	m.Triangles[0].VertIndex = [3]int32{0, 2, 1}
	return m
}

// A far model is drawn by recursive subdivision and vertex points
// (D_DrawSubdiv, D_PolysetDrawFinalVerts); one across the near plane keeps
// only the triangles whose vertices are all in front of it; one wholly behind
// the view is not drawn.
func TestDrawAliasInterpQuakeFarAndClipped(t *testing.T) {
	_, rd, skin := newAliasCtx(t)
	cm := litColormap()
	count := func(fb *FrameBuffer) int {
		n := 0
		for _, p := range fb.Pixels {
			if p != 0 {
				n++
			}
		}
		return n
	}
	// far: three vertices are plotted by DrawFinalVerts, the edges' midpoints
	// by the recursion (the triangle is a few pixels across at z 600)
	far := quakeFrontTri()
	fb, _, _ := newAliasCtx(t)
	if err := DrawAliasInterpQuake(fb, rd, cm, 100, false, far, skin, AliasEntityInterp{AliasEntity: AliasEntity{Origin: [3]float32{600, 0, 0}}}); err != nil {
		t.Fatal(err)
	}
	if n := count(fb); n < 3 {
		t.Errorf("far model: %d pixels, want its vertices at least", n)
	}
	// partly in front of the near plane: bbox from z 0 to 40 (object x), one
	// vertex beyond z 5, two before it: the triangle is dropped
	part := quakeFrontTri()
	part.Frames[0].Single.BBoxMax = mdl.TriVertx{V: [3]byte{40, 20, 20}}
	part.Frames[0].Single.Verts[2].V[0] = 40
	fb2, _, _ := newAliasCtx(t)
	if err := DrawAliasInterpQuake(fb2, rd, cm, 100, false, part, skin, AliasEntityInterp{}); err != nil {
		t.Fatal(err)
	}
	if n := count(fb2); n != 0 {
		t.Errorf("clipped model: %d pixels, want none (r_aclip.c not ported)", n)
	}
	// wholly behind the view
	fb3, _, _ := newAliasCtx(t)
	if err := DrawAliasInterpQuake(fb3, rd, cm, 100, false, quakeFrontTri(), skin, AliasEntityInterp{AliasEntity: AliasEntity{Origin: [3]float32{-100, 0, 0}}}); err != nil {
		t.Fatal(err)
	}
	if n := count(fb3); n != 0 {
		t.Errorf("model behind the view: %d pixels", n)
	}
}

// AliasClassify is R_AliasCheckBBox for an entity: a model in view is drawn,
// unclipped, and far ones take the recursive drawer; unusable input is not drawn.
func TestAliasClassify(t *testing.T) {
	fb, rd, _ := newAliasCtx(t)
	m := quakeFrontTri()
	at := func(x float32) AliasEntityInterp {
		return AliasEntityInterp{AliasEntity: AliasEntity{Origin: [3]float32{x, 0, 0}}}
	}
	if d, a := AliasClassify(fb.Width, fb.Height, rd, m, at(100)); !d || a != AliasUnclipped {
		t.Errorf("near: %v %d", d, a)
	}
	if d, a := AliasClassify(fb.Width, fb.Height, rd, m, at(600)); !d || a != AliasFarSubdiv {
		t.Errorf("far: %v %d", d, a)
	}
	if d, _ := AliasClassify(fb.Width, fb.Height, rd, m, at(-100)); d {
		t.Error("a model behind the view is drawn")
	}
	flat := *rd
	flat.FovX = 0
	bad := at(100)
	bad.FrameIdxNext = 5
	neg := at(100)
	neg.FrameIdx = -1
	for name, f := range map[string]func() bool{
		"nil refdef": func() bool { d, _ := AliasClassify(fb.Width, fb.Height, nil, m, at(100)); return d },
		"nil model":  func() bool { d, _ := AliasClassify(fb.Width, fb.Height, rd, nil, at(100)); return d },
		"zero fov":   func() bool { d, _ := AliasClassify(fb.Width, fb.Height, &flat, m, at(100)); return d },
		"bad frame":  func() bool { d, _ := AliasClassify(fb.Width, fb.Height, rd, m, bad); return d },
		"neg frame":  func() bool { d, _ := AliasClassify(fb.Width, fb.Height, rd, m, neg); return d },
	} {
		if f() {
			t.Errorf("%s: drawn", name)
		}
	}
}
