// Copyright (c) 2026 the go-quake1/engine authors.
// SPDX-License-Identifier: GPL-2.0-or-later

package render

import "testing"

// benchTurbInputs builds a large water quad (~250k px) matching the other
// fill benchmarks' screen coverage, on a stock 64x64 liquid texture.
func benchTurbInputs() (*FrameBuffer, *Pic, *ColorMap, []TexturedVertex) {
	fb, _ := NewFrameBuffer(640, 480)
	const tw, th = 64, 64
	tex := &Pic{Width: tw, Height: th, Pixels: make([]byte, tw*th)}
	for i := range tex.Pixels {
		tex.Pixels[i] = byte(i * 7)
	}
	cm := makeSrcEchoCM()
	verts := []TexturedVertex{
		{X: 20, Y: 20, U: 0, V: 0},
		{X: 620, Y: 40, U: 512, V: 0},
		{X: 620, Y: 440, U: 512, V: 512},
		{X: 20, Y: 460, U: 0, V: 512},
	}
	return fb, tex, cm, verts
}

// With the per-pixel colormap (cm != nil), as the world renderer calls it today.
func BenchmarkFillTurbulentPolygon_WithColormap(b *testing.B) {
	fb, tex, cm, verts := benchTurbInputs()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := FillTurbulentPolygon(fb, tex, cm, 0, verts, 1.25); err != nil {
			b.Fatal(err)
		}
	}
}

// With a pre-lit texture (cm == nil): the fill skips the per-pixel colormap.
func BenchmarkFillTurbulentPolygon_PreLit(b *testing.B) {
	fb, tex, _, verts := benchTurbInputs()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := FillTurbulentPolygon(fb, tex, nil, 0, verts, 1.25); err != nil {
			b.Fatal(err)
		}
	}
}
