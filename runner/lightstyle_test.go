// Copyright (c) 2026 the go-quake1/engine authors.
// SPDX-License-Identifier: GPL-2.0-or-later

package runner

import (
	"os"
	"testing"

	"github.com/go-quake1/engine/bench"
)

// TestWorldspawnSetsTheServersLightStyles: the real progs.dat's worldspawn calls
// lightstyle(style, string); sv.lightstyles holds them once the map is loaded
// and the server is active. (Needs QUAKE_PAK.)
func TestWorldspawnSetsTheServersLightStyles(t *testing.T) {
	path := os.Getenv("QUAKE_PAK")
	if path == "" {
		t.Skip("QUAKE_PAK not set: no pak0.pak")
	}
	fsys, _, err := bench.OpenPak(path)
	if err != nil {
		t.Fatal(err)
	}
	h, err := buildHost(fsys, "e1m1", func(string, ...any) {})
	if err != nil {
		t.Fatal(err)
	}
	ls := h.Server.LightStyles
	if ls[0] != "m" || ls[1] != "mmnmmommommnonmmonqnmmo" || ls[2] != "abcdefghijklmnopqrstuvwxyzyxwvutsrqponmlkjihgfedcba" {
		t.Errorf("styles 0 to 2: %q %q %q", ls[0], ls[1], ls[2])
	}
	if !h.Server.Active {
		t.Error("the server is not active after spawn")
	}
}
