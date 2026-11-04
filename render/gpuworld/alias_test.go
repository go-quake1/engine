// Copyright (c) 2026 the go-quake1/engine authors.
// SPDX-License-Identifier: GPL-2.0-or-later

package gpuworld

import (
	"encoding/binary"
	"math"
	"os"
	"strings"
	"testing"

	"github.com/go-quake1/engine/bench"
	"github.com/go-quake1/engine/render"
	"github.com/go-quake1/engine/worldview"
)

func TestAliasShaderMatchesTheReferenceConstants(t *testing.T) {
	// The shader hard-codes ALIAS_Z_CLIP_PLANE and the colormap size.
	if render.AliasClipPlane != 5 || !strings.Contains(AliasShaderWGSL, "< 5.0") {
		t.Errorf("alias clip plane %v is not the shader's 5.0", render.AliasClipPlane)
	}
	if render.ColorMapRows != 64 || !strings.Contains(AliasShaderWGSL, "0, 63)") {
		t.Errorf("colormap rows %d are not the shader's 64", render.ColorMapRows)
	}
	if EntRecordSize != 8*16 {
		t.Errorf("entity record %d bytes", EntRecordSize)
	}
}

func TestAliasE1M1(t *testing.T) {
	path := os.Getenv("QUAKE_PAK")
	if path == "" {
		t.Skip("QUAKE_PAK not set")
	}
	fsys, _, err := bench.OpenPak(path)
	if err != nil {
		t.Fatal(err)
	}
	w, err := worldview.LoadMap(fsys, "e1m1")
	if err != nil {
		t.Fatal(err)
	}
	ents, err := w.AliasEntities(fsys)
	if err != nil {
		t.Fatal(err)
	}
	var models []*worldview.AliasModel
	for _, e := range ents {
		models = append(models, e.Model)
	}
	set, err := BuildAlias(models)
	if err != nil {
		t.Fatal(err)
	}
	if len(set.Corners)%(3*CornerSize) != 0 || set.Skins.Height > MaxAtlasHeight {
		t.Fatalf("corner bytes %d, skin atlas %dx%d", len(set.Corners), set.Skins.Width, set.Skins.Height)
	}
	views, err := w.AliasViews(ents, func(v worldview.View) (worldview.AliasEnt, error) {
		return worldview.ViewWeapon(fsys, v)
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(views) != 16 {
		t.Fatalf("%d alias views, want 16", len(views))
	}
	for i, v := range views {
		target := ents[v.Target]
		found := false
		for _, e := range v.Ents {
			found = found || e.Origin == target.Origin && e.Class == target.Class
		}
		if !found || !v.Ents[len(v.Ents)-1].IsViewWeapon {
			t.Errorf("view %d: target %s kept %v, weapon last %v", i, target.Class, found, v.Ents[len(v.Ents)-1].IsViewWeapon)
		}
		// Models of the views are in the set built from every entity,
		// except the weapon, which has its own.
		all := append(append([]*worldview.AliasModel(nil), models...), v.Ents[len(v.Ents)-1].Model)
		s2, err := BuildAlias(all)
		if err != nil {
			t.Fatal(err)
		}
		recs, draws, err := s2.Entities(v.Ents, nil, nil)
		if err != nil {
			t.Fatalf("view %d: %v", i, err)
		}
		if len(recs) != EntRecordSize*len(v.Ents) || len(draws) != len(v.Ents) {
			t.Fatalf("view %d: %d bytes, %d draws for %d entities", i, len(recs), len(draws), len(v.Ents))
		}
		for k, d := range draws {
			if d.Count == 0 || d.Count%3 != 0 || d.First%3 != 0 || int(d.Instance) != k {
				t.Errorf("view %d draw %d: %+v", i, k, d)
			}
			if int(d.First+d.Count)*CornerSize > len(s2.Corners) {
				t.Errorf("view %d draw %d past the corners", i, k)
			}
			// The rotation row 0 of a record is the entity's forward axis, a unit vector.
			r := recs[k*EntRecordSize:]
			f := func(o int) float64 { return float64(math.Float32frombits(binary.LittleEndian.Uint32(r[o:]))) }
			if n := f(0)*f(0) + f(4)*f(4) + f(8)*f(8); math.Abs(n-1) > 1e-4 {
				t.Errorf("view %d entity %d: rotation row length^2 %v", i, k, n)
			}
		}
	}
}

func TestEntitiesRejectsAModelOutsideTheSet(t *testing.T) {
	set, err := BuildAlias(nil)
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = set.Entities([]worldview.AliasEnt{{Model: &worldview.AliasModel{Path: "x"}}}, nil, nil)
	if err == nil {
		t.Fatal("a model outside the set was accepted")
	}
}
