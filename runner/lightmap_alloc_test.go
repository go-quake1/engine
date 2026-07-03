// Copyright (c) 2026 the go-quake1/engine authors.
// SPDX-License-Identifier: BSD-3-Clause

package runner

import (
	"bytes"
	"testing"

	"github.com/go-quake1/engine/bsprender"
)

// TestBuildLightmapPlane_ScratchReuseMatchesFresh proves the caller-owned
// scratch buffers produce byte-identical output to fresh allocation, and that
// a large prior face leaves no stale pixels in a smaller following face's
// plane (the reuse hazard).
func TestBuildLightmapPlane_ScratchReuseMatchesFresh(t *testing.T) {
	var plane []byte
	var accum []int

	// First face: 8x8 fully-lit (LightOfs < 0 -> all 255), grows the scratch.
	big := bsprender.LightmapInfo{Width: 8, Height: 8, LightOfs: -1}
	p1 := buildLightmapPlane(big, nil, 0, &plane, &accum)
	if len(p1) != 64 {
		t.Fatalf("big plane len=%d want 64", len(p1))
	}
	for i, v := range p1 {
		if v != 255 {
			t.Fatalf("big plane[%d]=%d want 255", i, v)
		}
	}

	// Second face: smaller, real lighting, through the SAME scratch.
	small := bsprender.LightmapInfo{Width: 2, Height: 1, LightOfs: 0, Styles: [4]byte{0, 255, 255, 255}}
	lump := []byte{100, 200}
	reused := buildLightmapPlane(small, lump, 0, &plane, &accum)

	// Same inputs through a fresh scratch must give the same bytes.
	var fp []byte
	var fa []int
	fresh := buildLightmapPlane(small, lump, 0, &fp, &fa)

	if !bytes.Equal(reused, fresh) {
		t.Fatalf("reused-scratch plane %v != fresh-scratch plane %v", reused, fresh)
	}
	if len(reused) != 2 {
		t.Fatalf("small plane len=%d want 2", len(reused))
	}
}

// BenchmarkBuildLightmapPlane confirms the steady-state per-face cost is
// allocation-free (was 2 make() per call before the scratch-reuse change).
func BenchmarkBuildLightmapPlane(b *testing.B) {
	info := bsprender.LightmapInfo{Width: 16, Height: 16, LightOfs: 0, Styles: [4]byte{0, 255, 255, 255}}
	lump := make([]byte, 16*16)
	for i := range lump {
		lump[i] = byte(i)
	}
	var plane []byte
	var accum []int

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = buildLightmapPlane(info, lump, 0, &plane, &accum)
	}
}
