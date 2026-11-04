// Copyright (c) 2026 the go-quake1/engine authors.
// SPDX-License-Identifier: GPL-2.0-or-later

package gpuworld

import (
	"strconv"
	"strings"
	"testing"

	"github.com/go-quake1/engine/render"
)

// TestSpecialShadersHoldTheSourcesSubSpans: the sub-span lengths, steps and
// masks of the shaders are those of the sources they port: Turbulent8's 16
// pixels (shift 4, coordinates masked to TURB_CYCLE << 16) in d_scan.c, and
// D_DrawSkyScans8's 32 (shift 5) in d_sky.c.
func TestSpecialShadersHoldTheSourcesSubSpans(t *testing.T) {
	turb := TurbShaderWGSL()
	for _, want := range []string{
		"/ 16;",                                // sub-span index
		"clamp(remain, 1, 16)",                 // the sub-span's length
		"vec2u(4u)",                            // (snext - s) >> 4
		strconv.Itoa(render.TurbCycle<<16 - 1), // s &= (TURB_CYCLE << 16) - 1
		"clamp(v.x, 16,",                       // a sub-span end is at least 16
	} {
		if !strings.Contains(turb, want) {
			t.Errorf("the water shader lacks %q", want)
		}
	}
	if render.TurbSubSpan != 16 {
		t.Errorf("render.TurbSubSpan = %d, the shader's is 16", render.TurbSubSpan)
	}
	for _, want := range []string{"/ 32;", "clamp(remain, 1, 32)", "vec2u(5u)"} {
		if !strings.Contains(SpecialShaderWGSL, want) {
			t.Errorf("the sky shader lacks %q", want)
		}
	}
	if FaceTabStride != 5*16 {
		t.Errorf("FaceTabStride = %d, the shaders read 5 vec4f", FaceTabStride)
	}
}

// TestTurbShaderTableIsQuakes parses sintable back out of the shader text:
// it must be the port's table, entry for entry.
func TestTurbShaderTableIsQuakes(t *testing.T) {
	src := TurbShaderWGSL()
	i := strings.Index(src, "array<i32, 256>(")
	if i < 0 {
		t.Fatal("no table in the shader")
	}
	body, _, ok := strings.Cut(src[i+len("array<i32, 256>("):], ");")
	if !ok {
		t.Fatal("table not closed")
	}
	got := strings.Split(body, ", ")
	if len(got) != len(render.QuakeTurbTable) {
		t.Fatalf("%d entries, want %d", len(got), len(render.QuakeTurbTable))
	}
	for k, s := range got {
		n, err := strconv.Atoi(s)
		if err != nil || int32(n) != render.QuakeTurbTable[k] {
			t.Fatalf("entry %d: %q, want %d", k, s, render.QuakeTurbTable[k])
		}
	}
	if !strings.Contains(src, "fsSky") || !strings.Contains(src, "fsTurb") {
		t.Error("the water shader lacks an entry point")
	}
}
