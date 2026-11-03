// Copyright (c) 2026 the go-quake1/engine authors.
// SPDX-License-Identifier: GPL-2.0-or-later

package worldview

import (
	"os"
	"testing"

	"github.com/go-quake1/engine/bench"
)

func TestSpecialFacesAndViewsE1M1(t *testing.T) {
	path := os.Getenv("QUAKE_PAK")
	if path == "" {
		t.Skip("QUAKE_PAK not set")
	}
	fsys, _, err := bench.OpenPak(path)
	if err != nil {
		t.Fatal(err)
	}
	w, err := LoadMap(fsys, "e1m1")
	if err != nil {
		t.Fatal(err)
	}
	var sky, turb int
	for _, f := range w.Special {
		if f == nil {
			continue
		}
		if w.Faces[f.Idx] != nil {
			t.Errorf("face %d is both lit and special", f.Idx)
		}
		switch f.Kind {
		case KindSky:
			sky++
		case KindTurb:
			turb++
		}
	}
	t.Logf("e1m1: %d sky faces, %d water faces", sky, turb)
	for _, k := range []struct {
		kind SpecialKind
		n    int
	}{{KindSky, sky}, {KindTurb, turb}} {
		if k.n == 0 {
			continue
		}
		views := w.SpecialViews(k.kind, 16)
		seen := 0
		for _, v := range views {
			if _, err := w.Visible(v, 320, 200); err != nil {
				t.Fatal(err)
			}
			for _, f := range w.VisibleSpecial() {
				if f.Kind == k.kind {
					seen++
					break
				}
			}
		}
		t.Logf("kind %d: %d views, %d see a face of it", k.kind, len(views), seen)
		if len(views) == 0 || seen < len(views)/2 {
			t.Errorf("kind %d: %d views, only %d see their kind", k.kind, len(views), seen)
		}
	}
}
