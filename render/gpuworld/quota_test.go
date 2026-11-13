// Copyright (c) 2026 the go-quake1/engine authors.
// SPDX-License-Identifier: GPL-2.0-or-later

package gpuworld

import (
	"errors"
	"os"
	"testing"

	"github.com/go-quake1/engine/assets"
	"github.com/go-quake1/engine/bench"
	"github.com/go-quake1/engine/vfs"
	"github.com/go-quake1/engine/worldview"
)

// TestDrawQuotaHoldsForEveryMap: the batch host refuses a draw above
// DefaultLimits.MaxDrawVertices (1 Mi) and a stream above MaxStreamVertices (16 Mi)
// (vertex invocations: the index count times the instance count, one instance
// here). A frame draws at most every face of the map, once, so the whole index
// buffer bounds a frame's stream and the largest face's index range bounds a
// draw. Needs the freepak pak0.pak (QUAKE_PAK); it checks every e1 map the pak has
// and logs the peaks.
func TestDrawQuotaHoldsForEveryMap(t *testing.T) {
	path := os.Getenv("QUAKE_PAK")
	if path == "" {
		t.Skip("QUAKE_PAK not set")
	}
	fsys, _, err := bench.OpenPak(path)
	if err != nil {
		t.Fatal(err)
	}
	v := vfs.New()
	v.Add(fsys)
	cm, err := assets.LoadColorMapFrom(v)
	if err != nil {
		t.Fatal(err)
	}
	// go-wasmcontainers/gpu v0.13.0 gpubatch.DefaultLimits (not imported: it
	// would pull the whole WASI host into this module).
	lim := struct{ MaxDrawVertices, MaxStreamVertices uint64 }{1 << 20, 1 << 24}
	found := 0
	for _, slug := range []string{"e1m1", "e1m2", "e1m3", "e1m4", "e1m5", "e1m6", "e1m7", "e1m8"} {
		w, err := worldview.LoadMap(fsys, slug)
		if err != nil {
			t.Logf("%s: not in the pak (%v)", slug, err)
			continue
		}
		found++
		g, err := Build(w, cm)
		if errors.Is(err, ErrAtlasFull) {
			t.Logf("%s: the surface atlas is full: no GPU world for this map (the game falls back to the CPU)", slug)
			continue
		}
		if err != nil {
			t.Fatalf("%s: %v", slug, err)
		}
		var largest uint32
		for _, c := range g.Count {
			largest = max(largest, c)
		}
		stream := uint64(len(g.Indices))
		t.Logf("%s: %d faces, whole index buffer %d (stream quota %d), largest draw %d (quota %d)",
			slug, g.Faces, stream, lim.MaxStreamVertices, largest, lim.MaxDrawVertices)
		if stream > lim.MaxStreamVertices || uint64(largest) > lim.MaxDrawVertices {
			t.Errorf("%s: stream %d / draw %d above the host's %d / %d", slug, stream, largest, lim.MaxStreamVertices, lim.MaxDrawVertices)
		}
	}
	if found == 0 {
		t.Fatal("no e1 map in the pak")
	}
}
