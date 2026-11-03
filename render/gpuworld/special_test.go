// Copyright (c) 2026 the go-quake1/engine authors.
// SPDX-License-Identifier: GPL-2.0-or-later

package gpuworld

import (
	"strconv"
	"strings"
	"testing"

	"github.com/go-quake1/engine/render"
)

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
