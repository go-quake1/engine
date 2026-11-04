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

func litSampler(t *testing.T, lighting []byte, styles [4]byte, tex string) *bsprender.LightSampler {
	t.Helper()
	data, size, err := synthbsp.BuildLit(lighting, styles, tex)
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
	ls, err := bsprender.NewLightSampler(bm)
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

// Values worked by hand from r_main.c R_LightPoint (line 754) and
// r_light.c R_FillLightPoint (line 139), for the face of synthbsp.BuildLit
// (square 0..40, s = x, t = y, texturemins 0, extents 48, 4 x 4 samples,
// style 0 at d_lightstylevalue 264, scale 264/256 = 1.03125).
func TestLightPointWorked(t *testing.T) {
	ls := litSampler(t, ramp(), [4]byte{0, 255, 255, 255}, "wall")

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
	// Below the plane the line never crosses it: 0.
	if got := ls.LightPoint([3]float32{20, 10, -5}, bsprender.NormalLightStyle); got != 0 {
		t.Errorf("below: %d, want 0", got)
	}
	// s = 0 exactly: ds = 0, dsfrac = 0; t = 0: sample 0 only: 0 * ... = 0;
	// at (16, 16) s = t = 1: sample 5 = 50, times 1.03125 = 51.5625 -> 51.
	if got := ls.LightPoint([3]float32{16, 16, 50}, bsprender.NormalLightStyle); got != 51 {
		t.Errorf("(16,16): %d, want 51", got)
	}
}

// Two light styles add: style 1 at d_lightstylevalue 128 (scale .5) over a
// second map of 100s adds 50 (the weights sum to 1): 38.671875 + 50 -> 88.
func TestLightPointStylesAdd(t *testing.T) {
	lighting := append(ramp(), bytes.Repeat([]byte{100}, 16)...)
	ls := litSampler(t, lighting, [4]byte{0, 1, 255, 255}, "wall")
	styles := func(s byte) int {
		if s == 1 {
			return 128
		}
		return 264
	}
	if got := ls.LightPoint([3]float32{20, 10, 50}, styles); got != 88 {
		t.Errorf("two styles: %d, want 88", got)
	}
	// An unused slot (255) ends the list: the second map is not read.
	ls1 := litSampler(t, lighting, [4]byte{0, 255, 1, 255}, "wall")
	if got := ls1.LightPoint([3]float32{20, 10, 50}, styles); got != 38 {
		t.Errorf("styles stop at 255: %d, want 38", got)
	}
}

// DRAWTILED surfaces (sky, '*' liquids) have no lightmap and are skipped
// (r_light.c line 151); a map without a lighting lump is 255 (r_main.c line
// 759).
func TestLightPointSpecial(t *testing.T) {
	for _, tex := range []string{"sky1", "*water0", "*lava1"} {
		ls := litSampler(t, ramp(), [4]byte{0, 255, 255, 255}, tex)
		if got := ls.LightPoint([3]float32{20, 10, 50}, bsprender.NormalLightStyle); got != 0 {
			t.Errorf("%s: %d, want 0", tex, got)
		}
	}
	ls := litSampler(t, nil, [4]byte{0, 255, 255, 255}, "wall")
	if got := ls.LightPoint([3]float32{20, 10, 50}, bsprender.NormalLightStyle); got != 255 {
		t.Errorf("no lighting lump: %d, want 255", got)
	}
}

func TestNewLightSamplerNil(t *testing.T) {
	if _, err := bsprender.NewLightSampler(nil); err != bsprender.ErrLightNilModel {
		t.Fatalf("err = %v", err)
	}
}
