// Copyright (c) 2026 the go-quake1/engine authors.
// SPDX-License-Identifier: GPL-2.0-or-later

package render

import "testing"

// benchLmInputs builds a large, perspective-foreshortened, lightmapped
// quad approximating a real world wall face: a 256x256 texture, a
// 16x16 lightmap plane with a gradient, and a quad that covers most of
// a 640x480 framebuffer with a near/far depth split (so the perspective
// subdivision + 1/z divides run for real). This is the pixel-bound hot
// path the software renderer spends its frame in.
func benchLmInputs() (*FrameBuffer, *Pic, *ColorMap, []LightmappedVertex, []byte, int, int) {
	fb, _ := NewFrameBuffer(640, 480)

	const tw, th = 256, 256
	tex := &Pic{Width: tw, Height: th, Pixels: make([]byte, tw*th)}
	for i := range tex.Pixels {
		tex.Pixels[i] = byte(i * 7)
	}

	const lmW, lmH = 16, 16
	lm := make([]byte, lmW*lmH)
	for t := 0; t < lmH; t++ {
		for s := 0; s < lmW; s++ {
			lm[t*lmW+s] = byte((s + t) * 8)
		}
	}

	cm := makeSrcEchoCM()

	// A quad tilted in depth: left edge near (z=1), right edge far
	// (z=6), spanning almost the whole viewport.
	verts := []LightmappedVertex{
		{X: 20, Y: 20, Z: 1, U: 0, V: 0, LmS: 0, LmT: 0},
		{X: 620, Y: 40, Z: 6, U: 256, V: 0, LmS: float32(lmW - 1), LmT: 0},
		{X: 620, Y: 440, Z: 6, U: 256, V: 256, LmS: float32(lmW - 1), LmT: float32(lmH - 1)},
		{X: 20, Y: 460, Z: 1, U: 0, V: 256, LmS: 0, LmT: float32(lmH - 1)},
	}
	return fb, tex, cm, verts, lm, lmW, lmH
}

func BenchmarkFillPerspectiveLightmappedPolygon(b *testing.B) {
	fb, tex, cm, verts, lm, lmW, lmH := benchLmInputs()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := FillPerspectiveLightmappedPolygon(fb, tex, cm, verts, lm, lmW, lmH); err != nil {
			b.Fatal(err)
		}
	}
}
