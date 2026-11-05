// Copyright (c) 2026 the go-quake1/engine authors.
// SPDX-License-Identifier: GPL-2.0-or-later

package worldview_test

import (
	"testing"

	"github.com/go-quake1/engine/render"
)

// BenchmarkDrawSoftware is the software world reference over the 16 fixed
// views at 1280 x 800 (one op = one frame; the views cycle). It uses only
// DrawSoftware, so that the same file measures a clean clone of an earlier tag.
func BenchmarkDrawSoftware(b *testing.B) {
	w, cm := load(b)
	views := w.Views()
	fb, _ := render.NewFrameBuffer(1280, 800)
	for _, v := range views { // warm up: one pass over the views
		_ = w.DrawSoftware(fb, cm, v)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = w.DrawSoftware(fb, cm, views[i%len(views)])
	}
}
