// Copyright (c) 2026 the go-quake1/engine authors.
// SPDX-License-Identifier: GPL-2.0-or-later

package worldview_test

import (
	"os"
	"testing"

	"github.com/go-quake1/engine/bench"
	"github.com/go-quake1/engine/render"
)

// BenchmarkAliasSoftware is the engine's software alias pass over the 16 fixed
// alias views of e1m1 at 1280 x 800 (one op = the 16 views), single core.
func BenchmarkAliasSoftware(b *testing.B) {
	if os.Getenv(pakEnv) == "" {
		b.Skip("QUAKE_PAK not set")
	}
	w, cm := load(b)
	fsys, _, err := bench.OpenPak(os.Getenv(pakEnv))
	if err != nil {
		b.Fatal(err)
	}
	ents, err := w.AliasEntities(fsys)
	if err != nil {
		b.Fatal(err)
	}
	views, err := w.AliasViews(ents, nil)
	if err != nil {
		b.Fatal(err)
	}
	fb, _ := render.NewFrameBuffer(1280, 800)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		for _, av := range views {
			if err := w.DrawAliasSoftware(fb, cm, av.View, av.Ents); err != nil {
				b.Fatal(err)
			}
		}
	}
	b.ReportMetric(float64(b.Elapsed().Nanoseconds())/float64(b.N*len(views))/1e6, "ms/view")
}
