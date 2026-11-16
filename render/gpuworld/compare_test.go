// Copyright (c) 2026 the go-quake1/engine authors.
// SPDX-License-Identifier: GPL-2.0-or-later

package gpuworld

import "testing"

// TestCompositeLayer: only the texels an entity drew (not EntityClear) go over
// the frame, in a frame whose rows are wider than the layer's.
func TestCompositeLayer(t *testing.T) {
	dst := []byte{1, 1, 1, 9, 1, 1, 1, 9}
	layer := []byte{EntityClear, 7, EntityClear, 0, EntityClear, EntityClear}
	CompositeLayer(dst, 4, layer, 3, 2)
	if want := []byte{1, 7, 1, 9, 0, 1, 1, 9}; string(dst) != string(want) {
		t.Errorf("%v, want %v", dst, want)
	}
}

func TestCompare(t *testing.T) {
	const w, h, pitch = 8, 4, 10
	ref := make([]byte, pitch*h)
	got := make([]byte, w*h)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			ref[y*pitch+x] = byte(10 * x) // distinct columns
			got[y*w+x] = byte(10 * x)
		}
	}
	if d, u := Compare(ref, pitch, got, w, h); d != 0 || u != 0 {
		t.Errorf("equal frames: %d differ, %d unexplained", d, u)
	}
	got[1*w+3] = byte(10 * 4) // the right neighbour's value: a shift
	if d, u := Compare(ref, pitch, got, w, h); d != 1 || u != 0 {
		t.Errorf("one-pixel shift: %d differ, %d unexplained, want 1, 0", d, u)
	}
	got[2*w+5] = 7 // a value no neighbour has
	if d, u := Compare(ref, pitch, got, w, h); d != 2 || u != 1 {
		t.Errorf("wrong texel: %d differ, %d unexplained, want 2, 1", d, u)
	}
	got[0] = byte(10 * 2) // at the corner, a neighbour two columns away: not a shift
	if _, u := Compare(ref, pitch, got, w, h); u != 2 {
		t.Errorf("a value two pixels away counted as a shift: %d unexplained", u)
	}
}
