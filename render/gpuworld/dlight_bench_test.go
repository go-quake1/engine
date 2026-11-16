// Copyright (c) 2026 the go-quake1/engine authors.
// SPDX-License-Identifier: GPL-2.0-or-later

package gpuworld

import (
	"testing"

	"github.com/go-quake1/engine/render"
)

// BenchmarkDLightFrame is the CPU side of a frame of the dynamic-light case of
// the browser measurement (a muzzle flash, a rocket and an explosion around each
// of the 16 views at 1280x800): the draw list and the re-bake of the surfaces the
// lights reach, and the coalescing of their upload (needs QUAKE_PAK). The
// surfaces re-baked and their bytes are reported per frame.
func BenchmarkDLightFrame(b *testing.B) {
	w, cm := loadE1M1(b)
	g, err := Build(w, cm)
	if err != nil {
		b.Fatal(err)
	}
	views := w.Views()
	var draws []FaceDraw
	var out []Write
	var buf []byte
	frame := func(i int) {
		v := views[i%len(views)]
		w.DLights = [render.MaxDLights]render.DLight{}
		w.DLights[0] = render.DLight{Origin: [3]float32{v.Origin[0] + 18, v.Origin[1], v.Origin[2] + 16}, Radius: 220, MinLight: 32, Die: 0.1}
		w.DLights[1] = render.DLight{Origin: [3]float32{v.Origin[0] + 160, v.Origin[1], v.Origin[2]}, Radius: 200, Die: 0.01}
		w.DLights[2] = render.DLight{Origin: [3]float32{v.Origin[0] + 320, v.Origin[1], v.Origin[2] + 40}, Radius: 350, Die: 0.5, Decay: 300}
		var err error
		if draws, err = g.Draws(w, v, 1280, 800, draws, Chosen); err != nil {
			b.Fatal(err)
		}
		out, buf = g.Coalesce(g.Updates, DefaultCoalesceSlack, out, buf)
	}
	for i := 0; i < len(views); i++ { // warm up: every view once
		frame(i)
	}
	b.ReportAllocs()
	b.ResetTimer()
	surfaces, bytes := 0, 0
	for i := 0; i < b.N; i++ {
		frame(i)
		surfaces += len(g.Updates)
		bytes += g.UpdateBytes
	}
	b.ReportMetric(float64(surfaces)/float64(b.N), "surfaces/frame")
	b.ReportMetric(float64(bytes)/float64(b.N), "bytes/frame")
}
