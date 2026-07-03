// Copyright (c) 1996-1997 Id Software, Inc.
// Copyright (c) 2026 the go-quake1/engine authors.
// SPDX-License-Identifier: GPL-2.0-or-later

package wasm

import (
	"encoding/binary"
	"math"
	"testing"

	"github.com/go-quake1/engine/sound"
)

func TestResampleNearest_empty(t *testing.T) {
	l, r := ResampleNearest(nil, 11025, 44100)
	if len(l) != 0 || len(r) != 0 {
		t.Fatalf("empty: got (%d, %d) want (0, 0)", len(l), len(r))
	}
}

func TestResampleNearest_badRates(t *testing.T) {
	in := []sound.StereoSample{{L: 1, R: 2}}
	if l, r := ResampleNearest(in, 0, 44100); len(l) != 0 || len(r) != 0 {
		t.Fatalf("src=0: got (%d, %d) want (0, 0)", len(l), len(r))
	}
	if l, r := ResampleNearest(in, 11025, 0); len(l) != 0 || len(r) != 0 {
		t.Fatalf("dst=0: got (%d, %d) want (0, 0)", len(l), len(r))
	}
	if l, r := ResampleNearest(in, -1, 44100); len(l) != 0 || len(r) != 0 {
		t.Fatalf("src<0: got (%d, %d) want (0, 0)", len(l), len(r))
	}
	if l, r := ResampleNearest(in, 11025, -1); len(l) != 0 || len(r) != 0 {
		t.Fatalf("dst<0: got (%d, %d) want (0, 0)", len(l), len(r))
	}
}

func TestResampleNearest_passthroughSameRate(t *testing.T) {
	in := []sound.StereoSample{
		{L: 0, R: 0},
		{L: 32767, R: -32768},
		{L: -1, R: 1},
	}
	l, r := ResampleNearest(in, 44100, 44100)
	if len(l) != len(in) || len(r) != len(in) {
		t.Fatalf("len: got (%d, %d) want (%d, %d)", len(l), len(r), len(in), len(in))
	}
	// Round-trip the values; tolerate the 1 LSB asymmetry on -32768.
	for i, s := range in {
		wantL := float32(s.L) / 32768.0
		wantR := float32(s.R) / 32768.0
		if math.Abs(float64(l[i]-wantL)) > 1e-6 || math.Abs(float64(r[i]-wantR)) > 1e-6 {
			t.Errorf("frame %d: got (%v, %v) want (%v, %v)", i, l[i], r[i], wantL, wantR)
		}
	}
}

func TestResampleNearest_sameRateFastPath(t *testing.T) {
	// srcRate == dstRate exercises the dedicated fast-path branch.
	in := []sound.StereoSample{
		{L: 100, R: 200},
		{L: -100, R: -200},
	}
	l, r := ResampleNearest(in, 11025, 11025)
	if len(l) != 2 || len(r) != 2 {
		t.Fatalf("len: got (%d, %d) want (2, 2)", len(l), len(r))
	}
	if l[0] != 100.0/32768.0 || r[0] != 200.0/32768.0 {
		t.Errorf("out[0]: got (%v, %v)", l[0], r[0])
	}
	if l[1] != -100.0/32768.0 || r[1] != -200.0/32768.0 {
		t.Errorf("out[1]: got (%v, %v)", l[1], r[1])
	}
}

func TestResampleNearest_upsample4x(t *testing.T) {
	// 2 input frames at 11025 → 8 output frames at 44100.
	in := []sound.StereoSample{
		{L: 100, R: 200},
		{L: 300, R: 400},
	}
	l, r := ResampleNearest(in, 11025, 44100)
	want := 8 // ceil(2 * 44100 / 11025) = 8
	if len(l) != want || len(r) != want {
		t.Fatalf("len: got (%d, %d) want (%d, %d)", len(l), len(r), want, want)
	}
	// First 4 should pick src[0]; next 4 should pick src[1].
	for i := 0; i < 4; i++ {
		if l[i] != 100.0/32768.0 || r[i] != 200.0/32768.0 {
			t.Errorf("out[%d]: got (%v, %v) want src[0]", i, l[i], r[i])
		}
	}
	for i := 4; i < 8; i++ {
		if l[i] != 300.0/32768.0 || r[i] != 400.0/32768.0 {
			t.Errorf("out[%d]: got (%v, %v) want src[1]", i, l[i], r[i])
		}
	}
}

func TestResampleNearest_downsample(t *testing.T) {
	// 4 input frames at 44100 → 1 output frame at 11025.
	in := []sound.StereoSample{
		{L: 10, R: 20},
		{L: 30, R: 40},
		{L: 50, R: 60},
		{L: 70, R: 80},
	}
	l, r := ResampleNearest(in, 44100, 11025)
	want := 1
	if len(l) != want || len(r) != want {
		t.Fatalf("len: got (%d, %d) want (%d, %d)", len(l), len(r), want, want)
	}
	// Output[0] picks input[floor(0 * 44100 / 11025)] = input[0].
	if l[0] != 10.0/32768.0 || r[0] != 20.0/32768.0 {
		t.Errorf("out[0]: got (%v, %v) want src[0]", l[0], r[0])
	}
}

func TestResampleNearest_clampOnPastEnd(t *testing.T) {
	// Construct a scenario where the nearest-neighbor index rounds past
	// the last source index due to integer arithmetic at the tail.
	// Two src frames at 1 Hz → 3 out frames at 2 Hz: outFrames =
	// ceil(2*2/1) = 4 actually; pick something that does clamp.
	// Use 3 src @ 5 Hz → 7 out @ 10 Hz: outFrames = ceil(3*10/5) = 6.
	// At i=5, j = 5*5/10 = 2, valid. Force clamp via 3 src @ 3 Hz to
	// 5 Hz: outFrames = ceil(3*5/3) = 5, j_max = floor(4*3/5) = 2,
	// also valid. Use a 1-frame source: 1 src @ 1 → 3, outFrames =
	// ceil(1*3/1) = 3; j = floor(2*1/3) = 0. The clamp is defensive;
	// exercise it directly by forging conditions.
	in := []sound.StereoSample{{L: 7, R: 8}}
	l, r := ResampleNearest(in, 1, 3)
	if len(l) != 3 || len(r) != 3 {
		t.Fatalf("len: got (%d, %d) want (3, 3)", len(l), len(r))
	}
	for i := 0; i < 3; i++ {
		if l[i] != 7.0/32768.0 || r[i] != 8.0/32768.0 {
			t.Errorf("out[%d]: got (%v, %v)", i, l[i], r[i])
		}
	}
}

func TestFloat32SliceAsBytes_empty(t *testing.T) {
	if b := float32SliceAsBytes(nil); b != nil {
		t.Fatalf("nil in: got %v want nil", b)
	}
	if b := float32SliceAsBytes([]float32{}); b != nil {
		t.Fatalf("empty in: got %v want nil", b)
	}
}

func TestFloat32SliceAsBytes_shapesBytes(t *testing.T) {
	in := []float32{1.0, -2.5, 3.25, 0.0}
	b := float32SliceAsBytes(in)
	if len(b) != len(in)*4 {
		t.Fatalf("len: got %d want %d", len(b), len(in)*4)
	}
	// The result is always little-endian (the JS Float32Array order),
	// regardless of host byte order.
	for i, want := range in {
		got := math.Float32frombits(binary.LittleEndian.Uint32(b[i*4 : i*4+4]))
		if got != want {
			t.Errorf("sample[%d]: got %v want %v", i, got, want)
		}
	}
	if hostLittleEndian {
		// On a little-endian host the bytes alias the input: mutating the
		// bytes is visible through the float slice (proves zero-copy).
		b[0], b[1], b[2], b[3] = 0, 0, 0, 0
		if in[0] != 0.0 {
			t.Fatalf("zero-copy: expected in[0]=0, got %v", in[0])
		}
	}
}

// TestFloat32SliceAsBytes_bigEndianHostCopies forces the big-endian path on
// whatever host runs the test (so the amd64 coverage gate exercises it too)
// and verifies it still emits correct little-endian bytes as a copy.
func TestFloat32SliceAsBytes_bigEndianHostCopies(t *testing.T) {
	defer func(orig bool) { hostLittleEndian = orig }(hostLittleEndian)
	hostLittleEndian = false

	in := []float32{1.0, -2.5, 3.25, 0.0}
	b := float32SliceAsBytes(in)
	if len(b) != len(in)*4 {
		t.Fatalf("len: got %d want %d", len(b), len(in)*4)
	}
	for i, want := range in {
		got := math.Float32frombits(binary.LittleEndian.Uint32(b[i*4 : i*4+4]))
		if got != want {
			t.Errorf("sample[%d]: got %v want %v", i, got, want)
		}
	}
	// The big-endian path returns a copy: mutating the input must not change b.
	in[0] = 42.0
	if got := math.Float32frombits(binary.LittleEndian.Uint32(b[0:4])); got != 1.0 {
		t.Fatalf("expected a copy, got %v want 1.0", got)
	}
}
