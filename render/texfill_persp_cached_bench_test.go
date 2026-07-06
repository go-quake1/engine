// Copyright (c) 2026 the go-quake1/engine authors.
// SPDX-License-Identifier: GPL-2.0-or-later

package render

import "testing"

// BenchmarkFillPerspectiveCachedPolygon mirrors the lightmapped-fill
// benchmark's geometry (same ~250k-px depth-tilted quad) but draws from a
// pre-baked surface cache -- the fast path. Comparing the two ns/op shows the
// per-pixel win of removing the lightmap bilinear + colormap from the span
// loop (the whole point of the surface cache).
func BenchmarkFillPerspectiveCachedPolygon(b *testing.B) {
	fb, _ := NewFrameBuffer(640, 480)

	// A baked surface at texture resolution (256x256 lit palette indices).
	const sw, sh = 256, 256
	surf := &CachedSurface{W: sw, H: sh, Pixels: make([]byte, sw*sh)}
	for i := range surf.Pixels {
		surf.Pixels[i] = byte(i * 7)
	}

	// Same screen quad + depth split as benchLmInputs; cache coords span the
	// surface (0..255) matching that quad's LmS*16 / LmT*16 range.
	verts := []CachedVertex{
		{X: 20, Y: 20, Z: 1, CU: 0, CV: 0},
		{X: 620, Y: 40, Z: 6, CU: 255, CV: 0},
		{X: 620, Y: 440, Z: 6, CU: 255, CV: 255},
		{X: 20, Y: 460, Z: 1, CU: 0, CV: 255},
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := FillPerspectiveCachedPolygon(fb, surf, verts); err != nil {
			b.Fatal(err)
		}
	}
}
