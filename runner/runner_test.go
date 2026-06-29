// Copyright (c) 2026 the go-quake1/engine authors.
// SPDX-License-Identifier: BSD-3-Clause

package runner

import "testing"

// TestHUDScaleFor pins the integer HUDScale picker against the
// reference grid: vanilla 320x240 = 1, doubled 640x480 = 2, the
// 900x700 wasmbox saved-window default = 2, and the upper-bound
// 1920x1080 = 4 (height-bound: 1080/240 = 4.5 floored to 4).
func TestHUDScaleFor(t *testing.T) {
	cases := []struct {
		name string
		w, h int
		want int
	}{
		{"vanilla 320x240", 320, 240, 1},
		{"doubled 640x480", 640, 480, 2},
		{"saved-window 900x700", 900, 700, 2},
		{"sub-1 280x200", 280, 200, 1},
		{"width-bound 1280x960", 1280, 960, 4},
		{"height-bound 1920x1080", 1920, 1080, 4},
		{"zero width", 0, 240, 1},
		{"zero height", 320, 0, 1},
		{"negative width", -1, 240, 1},
		{"negative height", 320, -1, 1},
		{"both zero", 0, 0, 1},
		{"tiny 100x100", 100, 100, 1},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := HUDScaleFor(c.w, c.h); got != c.want {
				t.Fatalf("HUDScaleFor(%d, %d) = %d, want %d",
					c.w, c.h, got, c.want)
			}
		})
	}
}
