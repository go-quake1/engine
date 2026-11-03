// Copyright (c) 2026 the go-quake1/engine authors.
// SPDX-License-Identifier: GPL-2.0-or-later

package gpuworld

import (
	"strconv"
	"strings"
	"testing"

	"github.com/go-quake1/engine/render"
)

// TestTurbShaderTableIsTheEngines parses the table back out of the shader
// text: it must be the engine's, bit for bit, with every literal a float.
func TestTurbShaderTableIsTheEngines(t *testing.T) {
	src := TurbShaderWGSL()
	i := strings.Index(src, "array<f32, 256>(")
	if i < 0 {
		t.Fatal("no table in the shader")
	}
	rest := src[i+len("array<f32, 256>("):]
	body, _, ok := strings.Cut(rest, ");")
	if !ok {
		t.Fatal("table not closed")
	}
	got := strings.Split(body, ", ")
	want := render.TurbSinTable()
	if len(got) != len(want) {
		t.Fatalf("%d entries, want %d", len(got), len(want))
	}
	for k, s := range got {
		if !strings.ContainsAny(s, ".e") {
			t.Fatalf("entry %d %q is not a float literal", k, s)
		}
		f, err := strconv.ParseFloat(s, 32)
		if err != nil || float32(f) != want[k] {
			t.Fatalf("entry %d: %q, want %v", k, s, want[k])
		}
	}
	if !strings.Contains(src, "fsSky") || !strings.Contains(src, "fsTurb") {
		t.Error("the water shader lacks an entry point")
	}
}
