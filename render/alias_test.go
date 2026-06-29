// Copyright (c) 1996-1997 Id Software, Inc.
// Copyright (c) 2026 the go-quake1/engine authors.
// SPDX-License-Identifier: GPL-2.0-or-later

package render

import (
	"bytes"
	"errors"
	"testing"

	"github.com/go-quake1/engine/mdl"
)

// newAliasSkin builds a uniformly-filled palette-indexed skin.
func newAliasSkin(fill byte, w, h int) *Pic {
	pix := make([]byte, w*h)
	for i := range pix {
		pix[i] = fill
	}
	return &Pic{Width: w, Height: h, Pixels: pix}
}

// newAliasCtx returns a 320x200 framebuffer, a camera at the origin
// looking down +X with 90deg fovX, and a 16x16 skin filled with 0x42.
func newAliasCtx(t *testing.T) (*FrameBuffer, *RefDef, *Pic) {
	t.Helper()
	fb, err := NewFrameBuffer(320, 200)
	if err != nil {
		t.Fatalf("NewFrameBuffer: %v", err)
	}
	rd, err := NewRefDef(
		VRect{Width: 320, Height: 200},
		[3]float32{0, 0, 0},
		[3]float32{0, 0, 0},
		90,
	)
	if err != nil {
		t.Fatalf("NewRefDef: %v", err)
	}
	return fb, rd, newAliasSkin(0x42, 16, 16)
}

// makeTriModel builds a 1-triangle .mdl Model whose 3 vertices are
// supplied as RAW byte triples. The model uses Scale=(1,1,1) and
// ScaleOrigin=(0,-10,-10), letting the caller address the
// object-space cube [0..255] x [-10..245] x [-10..245] -- enough
// negative range to wind triangles on either side of the model's
// center. STVerts span a 16x16 skin's corners.
func makeTriModel(verts [3][3]byte, facesFront int32) *mdl.Model {
	var trivs [3]mdl.TriVertx
	for i, v := range verts {
		trivs[i] = mdl.TriVertx{V: v}
	}
	return &mdl.Model{
		Header: mdl.Header{
			Scale:       [3]float32{1, 1, 1},
			ScaleOrigin: [3]float32{0, -10, -10},
			SkinWidth:   16,
			SkinHeight:  16,
			NumVerts:    3,
			NumTris:     1,
			NumFrames:   1,
		},
		STVerts: []mdl.STVert{
			{S: 0, T: 0},
			{S: 15, T: 0},
			{S: 0, T: 15},
		},
		Triangles: []mdl.Triangle{
			{FacesFront: facesFront, VertIndex: [3]int32{0, 1, 2}},
		},
		Frames: []mdl.Frame{
			{Type: mdl.FrameSingle, Single: mdl.SingleFrame{Verts: trivs[:]}},
		},
	}
}

// frontFacingTri returns a triangle placed in front of the camera
// (entity at world (100,0,0)) and wound so the screen-space signed
// area is negative (front-facing per DrawAlias's convention).
//
// With Scale=(1,1,1) + ScaleOrigin=(0,-10,-10) the byte triples
// decode to object-space coordinates:
//
//	(0, 15, 10) -> obj (0,  5, 0) -> world (100,  5, 0) -> screen (152, 100)
//	(0,  5, 10) -> obj (0, -5, 0) -> world (100, -5, 0) -> screen (168, 100)
//	(0, 10, 15) -> obj (0,  0, 5) -> world (100,  0, 5) -> screen (160,  92)
//
// Signed area = (168-152)*(92-100) - (160-152)*(100-100) = -128
// (negative => front-facing in DrawAlias's pixel-coord convention).
func frontFacingTri() *mdl.Model {
	return makeTriModel([3][3]byte{
		{0, 15, 10},
		{0, 5, 10},
		{0, 10, 15},
	}, 1)
}

func TestDrawAlias_NilFB(t *testing.T) {
	_, rd, skin := newAliasCtx(t)
	m := frontFacingTri()
	err := DrawAlias(nil, rd, nil, 0, m, skin, AliasEntity{Origin: [3]float32{100, 0, 0}})
	if !errors.Is(err, ErrAliasNilFB) {
		t.Fatalf("err = %v want ErrAliasNilFB", err)
	}
}

func TestDrawAlias_NilModel(t *testing.T) {
	fb, rd, skin := newAliasCtx(t)
	err := DrawAlias(fb, rd, nil, 0, nil, skin, AliasEntity{})
	if !errors.Is(err, ErrAliasNilModel) {
		t.Fatalf("err = %v want ErrAliasNilModel", err)
	}
}

func TestDrawAlias_NilRefDef(t *testing.T) {
	fb, _, skin := newAliasCtx(t)
	m := frontFacingTri()
	err := DrawAlias(fb, nil, nil, 0, m, skin, AliasEntity{})
	if !errors.Is(err, ErrAliasNilRefDef) {
		t.Fatalf("err = %v want ErrAliasNilRefDef", err)
	}
}

func TestDrawAlias_NilSkin(t *testing.T) {
	fb, rd, _ := newAliasCtx(t)
	m := frontFacingTri()
	err := DrawAlias(fb, rd, nil, 0, m, nil, AliasEntity{})
	if !errors.Is(err, ErrAliasNilSkin) {
		t.Fatalf("err = %v want ErrAliasNilSkin", err)
	}
}

func TestDrawAlias_BadFrameNegative(t *testing.T) {
	fb, rd, skin := newAliasCtx(t)
	m := frontFacingTri()
	err := DrawAlias(fb, rd, nil, 0, m, skin, AliasEntity{FrameIdx: -1})
	if !errors.Is(err, ErrAliasBadFrame) {
		t.Fatalf("err = %v want ErrAliasBadFrame", err)
	}
}

func TestDrawAlias_BadFrameOverflow(t *testing.T) {
	fb, rd, skin := newAliasCtx(t)
	m := frontFacingTri()
	err := DrawAlias(fb, rd, nil, 0, m, skin, AliasEntity{FrameIdx: len(m.Frames)})
	if !errors.Is(err, ErrAliasBadFrame) {
		t.Fatalf("err = %v want ErrAliasBadFrame", err)
	}
}

func TestDrawAlias_HappyDraw(t *testing.T) {
	fb, rd, skin := newAliasCtx(t)
	m := frontFacingTri()
	ent := AliasEntity{Origin: [3]float32{100, 0, 0}}
	if err := DrawAlias(fb, rd, nil, 0, m, skin, ent); err != nil {
		t.Fatalf("DrawAlias: %v", err)
	}
	// Triangle interior approx contains (160, 98). Expect the skin
	// fill (0x42) at that pixel.
	if got := fb.Pixels[98*fb.Pitch+160]; got != 0x42 {
		t.Fatalf("center pixel = %#x want 0x42", got)
	}
}

func TestDrawAlias_WithColormap(t *testing.T) {
	fb, rd, skin := newAliasCtx(t)
	m := frontFacingTri()
	// Synthetic colormap: every (light, src) cell = src ^ 0xff.
	var cm ColorMap
	for l := 0; l < ColorMapRows; l++ {
		for s := 0; s < ColorMapCols; s++ {
			cm[l][s] = byte(s) ^ 0xff
		}
	}
	ent := AliasEntity{Origin: [3]float32{100, 0, 0}}
	if err := DrawAlias(fb, rd, &cm, 0, m, skin, ent); err != nil {
		t.Fatalf("DrawAlias: %v", err)
	}
	want := byte(0x42) ^ 0xff
	if got := fb.Pixels[98*fb.Pitch+160]; got != want {
		t.Fatalf("center pixel = %#x want %#x", got, want)
	}
}

func TestDrawAlias_BackFaceCulled(t *testing.T) {
	fb, rd, skin := newAliasCtx(t)
	// Same vertices, REVERSED winding: signed area flips positive
	// -> culled.
	m := makeTriModel([3][3]byte{
		{0, 15, 10},
		{0, 10, 15},
		{0, 5, 10},
	}, 1)
	ent := AliasEntity{Origin: [3]float32{100, 0, 0}}
	if err := DrawAlias(fb, rd, nil, 0, m, skin, ent); err != nil {
		t.Fatalf("DrawAlias: %v", err)
	}
	for _, b := range fb.Pixels {
		if b != 0 {
			t.Fatalf("back-facing triangle wrote a pixel")
		}
	}
}

func TestDrawAlias_NearClipped(t *testing.T) {
	fb, rd, skin := newAliasCtx(t)
	m := frontFacingTri()
	// Place the entity behind the camera so every vertex's view-z
	// is negative -> all triangles dropped by the near-clip test.
	ent := AliasEntity{Origin: [3]float32{-50, 0, 0}}
	if err := DrawAlias(fb, rd, nil, 0, m, skin, ent); err != nil {
		t.Fatalf("DrawAlias: %v", err)
	}
	for _, b := range fb.Pixels {
		if b != 0 {
			t.Fatalf("near-clipped triangle wrote a pixel")
		}
	}
}

func TestDrawAlias_BadFov(t *testing.T) {
	fb, _, skin := newAliasCtx(t)
	m := frontFacingTri()
	rd := &RefDef{FovX: 0, FovY: 0}
	if err := DrawAlias(fb, rd, nil, 0, m, skin, AliasEntity{Origin: [3]float32{100, 0, 0}}); err != nil {
		t.Fatalf("bad-fov err = %v", err)
	}
	for _, b := range fb.Pixels {
		if b != 0 {
			t.Fatalf("bad-fov draw wrote a pixel")
		}
	}
}

// makeBadSkin returns a *Pic with shape inconsistent with len(Pixels)
// to force FillTexturedPolygon -> ErrPicShape.
func makeBadSkin() *Pic {
	return &Pic{Width: 8, Height: 8, Pixels: []byte{1, 2, 3}}
}

func TestDrawAlias_PropagatesFillError(t *testing.T) {
	fb, rd, _ := newAliasCtx(t)
	m := frontFacingTri()
	err := DrawAlias(fb, rd, nil, 0, m, makeBadSkin(), AliasEntity{Origin: [3]float32{100, 0, 0}})
	if !errors.Is(err, ErrPicShape) {
		t.Fatalf("err = %v want ErrPicShape", err)
	}
}

func TestDrawAlias_BackSkinSeamFixup(t *testing.T) {
	// Vertices spread across screen so the seam fixup measurably
	// shifts the sampled column. A 16-wide skin: front-facing
	// samples col 0; back-facing seam vertex samples col 8.
	verts := [3][3]byte{
		{0, 15, 10},
		{0, 5, 10},
		{0, 10, 15},
	}
	m := makeTriModel(verts, 0) // FacesFront = 0 -> back skin
	// Mark every STVert as on-seam so the +SkinWidth/2 shift fires
	// on every corner.
	for i := range m.STVerts {
		m.STVerts[i].OnSeam = 0x20
	}
	fb, rd, _ := newAliasCtx(t)
	// Build a half-split skin so we can detect which half was sampled:
	// left 8 cols = 0xAA, right 8 cols = 0xBB.
	skin := &Pic{Width: 16, Height: 16, Pixels: make([]byte, 16*16)}
	for y := 0; y < 16; y++ {
		for x := 0; x < 16; x++ {
			if x < 8 {
				skin.Pixels[y*16+x] = 0xAA
			} else {
				skin.Pixels[y*16+x] = 0xBB
			}
		}
	}
	ent := AliasEntity{Origin: [3]float32{100, 0, 0}}
	if err := DrawAlias(fb, rd, nil, 0, m, skin, ent); err != nil {
		t.Fatalf("DrawAlias: %v", err)
	}
	if got := fb.Pixels[98*fb.Pitch+160]; got != 0xBB {
		t.Fatalf("seam-fixup sampled %#x want 0xBB (right-skin half)", got)
	}
}

func TestDrawAlias_FrameGroupUsesFirst(t *testing.T) {
	// Build a model whose only frame is a GROUP wrapping the same
	// front-facing pose -- DrawAlias should treat Group.Frames[0]
	// as the still pose and render normally.
	base := frontFacingTri()
	groupFrame := mdl.Frame{
		Type: mdl.FrameGroup,
		Group: &mdl.GroupFrame{
			Intervals: []float32{0.1},
			Frames:    []mdl.SingleFrame{base.Frames[0].Single},
		},
	}
	base.Frames[0] = groupFrame
	fb, rd, skin := newAliasCtx(t)
	ent := AliasEntity{Origin: [3]float32{100, 0, 0}}
	if err := DrawAlias(fb, rd, nil, 0, base, skin, ent); err != nil {
		t.Fatalf("DrawAlias: %v", err)
	}
	if got := fb.Pixels[98*fb.Pitch+160]; got != 0x42 {
		t.Fatalf("frame-group draw center = %#x want 0x42", got)
	}
}

func TestDrawAlias_FrameGroupEmptyNoOp(t *testing.T) {
	// Empty group -> FramePose returns nil -> the per-vertex pose
	// slice is empty; the triangle loop then index-panics if not
	// short-circuited by an earlier guard. With a 0-triangle model
	// the loop simply iterates zero work and DrawAlias returns nil.
	m := &mdl.Model{
		Header: mdl.Header{Scale: [3]float32{1, 1, 1}, NumFrames: 1},
		Frames: []mdl.Frame{
			{Type: mdl.FrameGroup, Group: &mdl.GroupFrame{}},
		},
	}
	fb, rd, skin := newAliasCtx(t)
	if err := DrawAlias(fb, rd, nil, 0, m, skin, AliasEntity{}); err != nil {
		t.Fatalf("empty-group err = %v", err)
	}
}

func TestDrawAlias_EntityYawRotates(t *testing.T) {
	// Same pose but rotate the entity 90deg yaw -- the triangle's
	// "right" axis swings to point at world -X, which is behind
	// the camera. The triangle should now near-clip away.
	m := frontFacingTri()
	fb, rd, skin := newAliasCtx(t)
	ent := AliasEntity{Origin: [3]float32{100, 0, 0}, AngleYaw: 180}
	if err := DrawAlias(fb, rd, nil, 0, m, skin, ent); err != nil {
		t.Fatalf("yaw-180 err = %v", err)
	}
	// With yaw=180 the model's +X-forward axis now points at -X
	// (behind the camera), so the screen-projected vertices wind
	// the opposite way -> back-face-culled. Either way, no fill.
	for _, b := range fb.Pixels {
		if b != 0 {
			t.Fatalf("yaw-rotated triangle wrote a pixel; expected back-face cull")
		}
	}
}

// --- entityRotation -------------------------------------------------------

func TestEntityRotation_ZeroIsIdentity(t *testing.T) {
	m := entityRotation(0, 0, 0)
	want := Identity()
	for i := 0; i < 3; i++ {
		for j := 0; j < 3; j++ {
			d := m[i][j] - want[i][j]
			if d < -1e-5 || d > 1e-5 {
				t.Fatalf("entityRotation(0,0,0)[%d][%d] = %v want %v", i, j, m[i][j], want[i][j])
			}
		}
	}
}

func TestFramePose_SingleReturnsVerts(t *testing.T) {
	want := []mdl.TriVertx{{V: [3]byte{1, 2, 3}}}
	got := FramePose(mdl.Frame{Type: mdl.FrameSingle, Single: mdl.SingleFrame{Verts: want}})
	if len(got) != 1 || got[0].V != want[0].V {
		t.Fatalf("FramePose single = %v want %v", got, want)
	}
}

func TestFramePose_GroupNilReturnsNil(t *testing.T) {
	if got := FramePose(mdl.Frame{Type: mdl.FrameGroup, Group: nil}); got != nil {
		t.Fatalf("FramePose nil-group = %v want nil", got)
	}
}

// TestDrawAlias_FrameGroupCyclesWithClTime is the autonomous
// regression test for torch/flame animation. A FrameGroup whose two
// sub-frames place the triangle in DIFFERENT screen positions MUST
// produce different framebuffer bytes at t=0 vs t=0.15s. A regression
// that pins sub-frame 0 (the prior FramePose behaviour) makes both
// draws byte-identical, and this test fires.
//
// Mirrors the in-game pathology for progs/flame.mdl whose flame
// sub-frames cycle at 10 Hz: with PoseAt threading off ClTime, the
// renderer picks a different pose every ~100 ms and the flame
// appears to flicker.
func TestDrawAlias_FrameGroupCyclesWithClTime(t *testing.T) {
	// Two sub-frames with a vertex translated along Y by 20 byte-units.
	// Both front-facing (signed area negative under the DrawAlias
	// convention). Cycle = 0.2s; sub-frame 0 at t in [0,0.1), sub-frame
	// 1 at t in [0.1,0.2).
	subA := mdl.SingleFrame{Verts: []mdl.TriVertx{
		{V: [3]byte{0, 15, 10}},
		{V: [3]byte{0, 5, 10}},
		{V: [3]byte{0, 10, 15}},
	}}
	subB := mdl.SingleFrame{Verts: []mdl.TriVertx{
		// Shift the y coords up by 20 so the triangle lands at a
		// different screen Y -> different pixels written.
		{V: [3]byte{0, 35, 10}},
		{V: [3]byte{0, 25, 10}},
		{V: [3]byte{0, 30, 15}},
	}}
	m := &mdl.Model{
		Header: mdl.Header{
			Scale:       [3]float32{1, 1, 1},
			ScaleOrigin: [3]float32{0, -10, -10},
			SkinWidth:   16,
			SkinHeight:  16,
			NumVerts:    3,
			NumTris:     1,
			NumFrames:   1,
		},
		STVerts: []mdl.STVert{{S: 0, T: 0}, {S: 15, T: 0}, {S: 0, T: 15}},
		Triangles: []mdl.Triangle{
			{FacesFront: 1, VertIndex: [3]int32{0, 1, 2}},
		},
		Frames: []mdl.Frame{
			{Type: mdl.FrameGroup, Group: &mdl.GroupFrame{
				Intervals: []float32{0.1, 0.2},
				Frames:    []mdl.SingleFrame{subA, subB},
			}},
		},
	}
	skin := newAliasSkin(0x42, 16, 16)

	drawAt := func(time float32) []byte {
		fb, _ := NewFrameBuffer(320, 200)
		rd, _ := NewRefDef(VRect{Width: 320, Height: 200}, [3]float32{0, 0, 0}, [3]float32{0, 0, 0}, 90)
		ent := AliasEntity{Origin: [3]float32{100, 0, 0}, ClTime: time}
		if err := DrawAlias(fb, rd, nil, 0, m, skin, ent); err != nil {
			t.Fatalf("DrawAlias(t=%v): %v", time, err)
		}
		out := make([]byte, len(fb.Pixels))
		copy(out, fb.Pixels)
		return out
	}

	at0 := drawAt(0)     // sub-frame A
	at15 := drawAt(0.15) // sub-frame B (in [0.1, 0.2))
	if bytes.Equal(at0, at15) {
		t.Fatal("FrameGroup did NOT cycle: ClTime=0 produced same bytes as ClTime=0.15 (PoseAt regression?)")
	}

	// And the cycle MUST wrap: t=0.25 (> total 0.2) modulo 0.2 = 0.05
	// -> back to sub-frame A. Bytes must match the t=0 draw.
	at25 := drawAt(0.25)
	if !bytes.Equal(at0, at25) {
		t.Fatal("FrameGroup cycle did NOT wrap modulo total: t=0 and t=0.25 differ")
	}
}

// TestDrawAliasLit_FrameGroupCyclesWithClTime mirrors the above for
// the lit renderer (the one the runner actually calls via
// DrawAliasInterpLit). Catches a regression where Lit path forgot to
// thread ClTime through PoseAt.
func TestDrawAliasLit_FrameGroupCyclesWithClTime(t *testing.T) {
	subA := mdl.SingleFrame{Verts: []mdl.TriVertx{
		{V: [3]byte{0, 15, 10}},
		{V: [3]byte{0, 5, 10}},
		{V: [3]byte{0, 10, 15}},
	}}
	subB := mdl.SingleFrame{Verts: []mdl.TriVertx{
		{V: [3]byte{0, 35, 10}},
		{V: [3]byte{0, 25, 10}},
		{V: [3]byte{0, 30, 15}},
	}}
	m := &mdl.Model{
		Header: mdl.Header{
			Scale:       [3]float32{1, 1, 1},
			ScaleOrigin: [3]float32{0, -10, -10},
			SkinWidth:   16,
			SkinHeight:  16,
			NumVerts:    3,
			NumTris:     1,
			NumFrames:   1,
		},
		STVerts:   []mdl.STVert{{S: 0, T: 0}, {S: 15, T: 0}, {S: 0, T: 15}},
		Triangles: []mdl.Triangle{{FacesFront: 1, VertIndex: [3]int32{0, 1, 2}}},
		Frames: []mdl.Frame{
			{Type: mdl.FrameGroup, Group: &mdl.GroupFrame{
				Intervals: []float32{0.1, 0.2},
				Frames:    []mdl.SingleFrame{subA, subB},
			}},
		},
	}
	skin := newAliasSkin(0x42, 16, 16)

	drawAt := func(time float32) []byte {
		fb, _ := NewFrameBuffer(320, 200)
		rd, _ := NewRefDef(VRect{Width: 320, Height: 200}, [3]float32{0, 0, 0}, [3]float32{0, 0, 0}, 90)
		ent := AliasEntity{Origin: [3]float32{100, 0, 0}, ClTime: time}
		if err := DrawAliasLit(fb, rd, nil, DefaultAliasShade(), m, skin, ent); err != nil {
			t.Fatalf("DrawAliasLit(t=%v): %v", time, err)
		}
		out := make([]byte, len(fb.Pixels))
		copy(out, fb.Pixels)
		return out
	}
	a, b := drawAt(0), drawAt(0.15)
	if bytes.Equal(a, b) {
		t.Fatal("DrawAliasLit FrameGroup did NOT cycle with ClTime")
	}
}
