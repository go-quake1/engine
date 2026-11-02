// Copyright (c) 2026 the go-quake1/engine authors.
// SPDX-License-Identifier: GPL-2.0-or-later

package worldview_test

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"testing"

	"github.com/go-quake1/engine/assets"
	"github.com/go-quake1/engine/bench"
	"github.com/go-quake1/engine/render"
	"github.com/go-quake1/engine/vfs"
	"github.com/go-quake1/engine/worldview"
)

// pakEnv names the env var that points at the shareware pak0.pak. The
// pak is not in the repository: the tests skip without it.
const pakEnv = "QUAKE_PAK"

func load(t *testing.T) (*worldview.World, *render.ColorMap) {
	t.Helper()
	path := os.Getenv(pakEnv)
	if path == "" {
		t.Skipf("%s not set: no pak0.pak to load e1m1 from", pakEnv)
	}
	fsys, _, err := bench.OpenPak(path)
	if err != nil {
		t.Fatal(err)
	}
	w, err := worldview.LoadMap(fsys, "e1m1")
	if err != nil {
		t.Fatal(err)
	}
	v := vfs.New()
	v.Add(fsys)
	cm, err := assets.LoadColorMapFrom(v)
	if err != nil {
		t.Fatal(err)
	}
	return w, cm
}

func TestLoadMapMissing(t *testing.T) {
	fsys, _, _ := bench.OpenPak("")
	if _, err := worldview.LoadMap(fsys, "e1m1"); err == nil {
		t.Fatal("the procedural pak has no e1m1")
	}
}

// TestReferenceFrames: the fixed e1m1 views draw non-trivial, deterministic
// world-only frames. The digests are logged: they identify the reference
// the GPU world is compared with.
func TestReferenceFrames(t *testing.T) {
	w, cm := load(t)
	views := w.Views()
	if len(views) < 8 {
		t.Fatalf("%d views, want at least 8", len(views))
	}
	drawn := 0
	for i, v := range views {
		a, _ := render.NewFrameBuffer(320, 200)
		b, _ := render.NewFrameBuffer(320, 200)
		if err := w.DrawSoftware(a, cm, v); err != nil {
			t.Fatal(err)
		}
		if err := w.DrawSoftware(b, cm, v); err != nil {
			t.Fatal(err)
		}
		if string(a.Pixels) != string(b.Pixels) {
			t.Fatalf("view %d: two draws differ", i)
		}
		seen := map[byte]bool{}
		for _, p := range a.Pixels {
			seen[p] = true
		}
		sum := sha256.Sum256(a.Pixels)
		t.Logf("view %2d origin %v yaw %3.0f: %3d indices, sha256 %s", i, v.Origin, v.Angles[1], len(seen), hex.EncodeToString(sum[:6]))
		if len(seen) > 8 {
			drawn++
		}
	}
	if drawn < len(views)/2 {
		t.Fatalf("only %d of %d views draw a textured world", drawn, len(views))
	}
}
