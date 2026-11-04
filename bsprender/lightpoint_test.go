// Copyright (c) 2026 the go-quake1/engine authors.
// SPDX-License-Identifier: GPL-2.0-or-later

package bsprender_test

import (
	"bytes"
	"testing"

	"github.com/go-quake1/engine/bspfile"
	"github.com/go-quake1/engine/bspfile/synthbsp"
	"github.com/go-quake1/engine/bsprender"
	"github.com/go-quake1/engine/model"
)

func litModel(t *testing.T, spec synthbsp.LitSpec) *model.BrushModel {
	t.Helper()
	data, size, err := synthbsp.BuildLit(spec)
	if err != nil {
		t.Fatal(err)
	}
	f, err := bspfile.Open(bytes.NewReader(data), size)
	if err != nil {
		t.Fatal(err)
	}
	bm, err := model.LoadBrush(f, 0)
	if err != nil {
		t.Fatal(err)
	}
	return bm
}

func litSampler(t *testing.T, spec synthbsp.LitSpec) *bsprender.LightSampler {
	t.Helper()
	ls, err := bsprender.NewLightSampler(litModel(t, spec))
	if err != nil {
		t.Fatal(err)
	}
	return ls
}

// ramp is a 4 x 4 lightmap with sample i = 10 * i.
func ramp() []byte {
	b := make([]byte, 16)
	for i := range b {
		b[i] = byte(10 * i)
	}
	return b
}

func wall() synthbsp.LitSpec {
	return synthbsp.LitSpec{Lighting: ramp(), Styles: [4]byte{0, 255, 255, 255}, TexName: "wall"}
}

// Values worked by hand from r_main.c R_LightPoint (line 754) and
// r_light.c R_FillLightPoint (line 139), for the face of synthbsp.BuildLit
// (square 0..40, s = x, t = y, texturemins 0, extents 48, 4 x 4 samples,
// style 0 at d_lightstylevalue 264, scale 264/256 = 1.03125).
func TestLightPointWorked(t *testing.T) {
	ls := litSampler(t, wall())

	// (20, 10): s = 1.25, t = 0.625, ds = 1, dt = 0. Samples 10 20 / 50 60,
	// times 1.03125, weights .28125 .09375 .46875 .15625:
	// 2.900390625 + 1.93359375 + 24.169921875 + 9.66796875 = 38.671875.
	if got := ls.LightPoint([3]float32{20, 10, 50}, bsprender.NormalLightStyle); got != 38 {
		t.Errorf("(20,10): %d, want 38", got)
	}
	// (56, 10): s = 3.5, t = 0.625. ds = floor(3.5) = 3 is inside extents[0]>>4
	// = 3, then min(3, surfwidth - 2 = 2) = 2, so dsfrac = 1.5 (as in the C).
	// Samples 20 30 / 60 70 times 1.03125, weights -.1875 .5625 -.3125 .9375:
	// -3.8671875 + 17.40234375 - 19.3359375 + 67.67578125 = 61.875.
	if got := ls.LightPoint([3]float32{56, 10, 50}, bsprender.NormalLightStyle); got != 61 {
		t.Errorf("(56,10): %d, want 61", got)
	}
	// s = 4.5 is past extents[0]>>4 = 3: R_FillLightPoint skips the face and
	// nothing else lies below: 0.
	if got := ls.LightPoint([3]float32{72, 10, 50}, bsprender.NormalLightStyle); got != 0 {
		t.Errorf("off the lightmap: %d, want 0", got)
	}
	// s = -0.3125: ds = -1 is rejected (line 160): 0.
	if got := ls.LightPoint([3]float32{-5, 10, 50}, bsprender.NormalLightStyle); got != 0 {
		t.Errorf("s < 0: %d, want 0", got)
	}
	// Below the plane the line never crosses it: 0.
	if got := ls.LightPoint([3]float32{20, 10, -5}, bsprender.NormalLightStyle); got != 0 {
		t.Errorf("below: %d, want 0", got)
	}
	// (16, 16): s = t = 1, ds = dt = 1, no fraction: sample 5 = 50, times
	// 1.03125 = 51.5625 -> 51.
	if got := ls.LightPoint([3]float32{16, 16, 50}, bsprender.NormalLightStyle); got != 51 {
		t.Errorf("(16,16): %d, want 51", got)
	}
	// The hit itself: face 0 at s = 1.25, t = 0.625.
	if f, s, tt, ok := ls.SurfPoint([3]float32{20, 10, 50}); !ok || f != 0 || s != 1.25 || tt != 0.625 {
		t.Errorf("SurfPoint = %d %v %v %v", f, s, tt, ok)
	}
	// LightPointOr: the sampler when there is one, the fallback when nil.
	if got := ls.LightPointOr([3]float32{20, 10, 50}, bsprender.NormalLightStyle, 7); got != 38 {
		t.Errorf("LightPointOr = %d", got)
	}
	var none *bsprender.LightSampler
	if got := none.LightPointOr([3]float32{20, 10, 50}, bsprender.NormalLightStyle, 7); got != 7 {
		t.Errorf("nil LightPointOr = %d, want the fallback 7", got)
	}
}

// Two light styles add: style 1 at d_lightstylevalue 128 (scale .5) over a
// second map of 100s adds 50 (the weights sum to 1): 38.671875 + 50 -> 88.
func TestLightPointStylesAdd(t *testing.T) {
	spec := wall()
	spec.Lighting = append(ramp(), bytes.Repeat([]byte{100}, 16)...)
	spec.Styles = [4]byte{0, 1, 255, 255}
	styles := func(s byte) int {
		if s == 1 {
			return 128
		}
		return 264
	}
	if got := litSampler(t, spec).LightPoint([3]float32{20, 10, 50}, styles); got != 88 {
		t.Errorf("two styles: %d, want 88", got)
	}
	// An unused slot (255) ends the list: the second map is not read.
	spec.Styles = [4]byte{0, 255, 1, 255}
	if got := litSampler(t, spec).LightPoint([3]float32{20, 10, 50}, styles); got != 38 {
		t.Errorf("styles stop at 255: %d, want 38", got)
	}
}

// DRAWTILED surfaces (sky, '*' liquids, TEX_SPECIAL, no texture) have no
// lightmap and are skipped (r_light.c line 151); a map without a lighting
// lump is 255 (r_main.c line 759); a face without samples ends the search at
// that node (line 164) and so does one the map gives no geometry for.
func TestLightPointSkipsAndFallbacks(t *testing.T) {
	at := [3]float32{20, 10, 50}
	for _, tex := range []string{"sky1", "*water0", "*lava1"} {
		spec := wall()
		spec.TexName = tex
		if got := litSampler(t, spec).LightPoint(at, bsprender.NormalLightStyle); got != 0 {
			t.Errorf("%s: %d, want 0", tex, got)
		}
	}
	for name, mod := range map[string]func(*synthbsp.LitSpec){
		"TEX_SPECIAL":     func(s *synthbsp.LitSpec) { s.SpecialTexInfo = true },
		"missing texture": func(s *synthbsp.LitSpec) { s.MissingTex = true },
		"miptex range":    func(s *synthbsp.LitSpec) { s.MipTexIndex = 7 },
		"bad texinfo":     func(s *synthbsp.LitSpec) { s.BadTexInfo = true },
		"no edges":        func(s *synthbsp.LitSpec) { s.NoEdges = true },
		"no samples":      func(s *synthbsp.LitSpec) { s.NoSamples = true },
		"bad plane":       func(s *synthbsp.LitSpec) { s.BadPlane = true },
		"more faces":      func(s *synthbsp.LitSpec) { s.ExtraFaces = 3; s.SpecialTexInfo = true },
	} {
		spec := wall()
		mod(&spec)
		if got := litSampler(t, spec).LightPoint(at, bsprender.NormalLightStyle); got != 0 {
			t.Errorf("%s: %d, want 0", name, got)
		}
	}
	if got := litSampler(t, synthbsp.LitSpec{TexName: "wall"}).LightPoint(at, bsprender.NormalLightStyle); got != 255 {
		t.Errorf("no lighting lump: %d, want 255", got)
	}
}

// The tree shapes: a node below or above the plane's node is searched too,
// and the general plane test (a slanted plane type) gives the same answer.
func TestLightPointTreeShapes(t *testing.T) {
	at := [3]float32{20, 10, 50}
	for name, mod := range map[string]func(*synthbsp.LitSpec){
		"front node": func(s *synthbsp.LitSpec) { s.Tree = synthbsp.TreeFrontNode },
		"slant type": func(s *synthbsp.LitSpec) { s.SlantType = true },
	} {
		spec := wall()
		mod(&spec)
		if got := litSampler(t, spec).LightPoint(at, bsprender.NormalLightStyle); got != 38 {
			t.Errorf("%s: %d, want 38", name, got)
		}
	}
	// Off the face's lightmap, with a node on the back side to go down into:
	// nothing is found there either.
	spec := wall()
	spec.Tree = synthbsp.TreeBackNode
	if got := litSampler(t, spec).LightPoint([3]float32{72, 10, 50}, bsprender.NormalLightStyle); got != 0 {
		t.Errorf("back node: %d, want 0", got)
	}
	// A point below the plane in the back node's half: the line never crosses.
	if got := litSampler(t, spec).LightPoint([3]float32{20, 10, -5}, bsprender.NormalLightStyle); got != 0 {
		t.Errorf("below with a back node: %d, want 0", got)
	}
	// A front node: a point below the plane never reaches it.
	spec.Tree = synthbsp.TreeFrontNode
	if got := litSampler(t, spec).LightPoint([3]float32{20, 10, -5}, bsprender.NormalLightStyle); got != 0 {
		t.Errorf("below with a front node: %d, want 0", got)
	}
}

// The edges taken backwards give the same square; a lightmap offset that
// runs off the lump reads zeros (the C reads whatever follows).
func TestLightPointReadsPastTheLump(t *testing.T) {
	spec := wall()
	spec.ReverseEdges = true
	if got := litSampler(t, spec).LightPoint([3]float32{20, 10, 50}, bsprender.NormalLightStyle); got != 38 {
		t.Errorf("reversed edges: %d, want 38", got)
	}
	spec = wall()
	// Offset 12: row 0 reads samples 13 and 14 (130, 140), row 1 is past the
	// lump (0): 134.0625 * .28125 + 144.375 * .09375 = 51.23 -> 51.
	spec.LightOfsDelta = 12
	if got := litSampler(t, spec).LightPoint([3]float32{20, 10, 50}, bsprender.NormalLightStyle); got != 51 {
		t.Errorf("%d, want 51", got)
	}
	spec.LightOfsDelta = -100
	if got := litSampler(t, spec).LightPoint([3]float32{20, 10, 50}, bsprender.NormalLightStyle); got != 0 {
		t.Errorf("negative offset: %d, want 0", got)
	}
}

func TestNewLightSamplerErrors(t *testing.T) {
	if _, err := bsprender.NewLightSampler(nil); err != bsprender.ErrLightNilModel {
		t.Fatalf("err = %v", err)
	}
	for _, k := range []bspfile.LumpKind{bspfile.LumpFaces, bspfile.LumpTexInfo, bspfile.LumpSurfedges,
		bspfile.LumpEdges, bspfile.LumpVertexes, bspfile.LumpTextures} {
		spec := wall()
		data, size, _ := synthbsp.BuildLit(spec)
		// the model loads from the intact map; the sampler from the damaged one
		f, _ := bspfile.Open(bytes.NewReader(data), size)
		bm, err := model.LoadBrush(f, 0)
		if err != nil {
			t.Fatal(err)
		}
		spec.Corrupt = []bspfile.LumpKind{k}
		bad, bsize, _ := synthbsp.BuildLit(spec)
		bf, err := bspfile.Open(bytes.NewReader(bad), bsize)
		if err != nil {
			t.Fatalf("lump %d: %v", k, err)
		}
		bbm, err := model.LoadBrush(bf, 0)
		if err != nil {
			t.Fatalf("lump %d: %v", k, err)
		}
		_ = bm
		if _, err := bsprender.NewLightSampler(bbm); err == nil {
			t.Errorf("lump %d corrupt: no error", k)
		}
	}
}
