// Copyright (c) 2026 the go-quake1/engine authors.
// SPDX-License-Identifier: GPL-2.0-or-later

package render

import (
	"slices"
	"testing"
)

// TestAliasDLightLevels pins the dynamic-light loop of R_AliasSetupLighting
// (reference/common/r_alias.c:684-691) on worked values:
//
//	lightlevel = dlight->radius - Length(origin - dlight->origin)   (an int: truncated)
//	a light with die < cl.time is skipped; a level > 0 is added.
//
// on the light at the origin of radius 350: 350; at (3, 4, 0) of radius 220:
// distance 5, level 215; radius 220 at distance 18.5: 201.5, 201; radius 200 at
// distance 200: 0, not added; radius 100 at distance 150: -50, not added; a
// light that died at 9.9 is skipped at 10, one that dies at 10 is not.
func TestAliasDLightLevels(t *testing.T) {
	o := [3]float32{}
	for _, c := range []struct {
		name  string
		lamp  DLight
		now   float32
		level int // the value of the C expression
		added bool
	}{
		{"on the entity", DLight{Radius: 350, Die: 11}, 10, 350, true},
		{"3-4-5", DLight{Origin: [3]float32{3, 4, 0}, Radius: 220, Die: 11}, 10, 215, true},
		{"truncated", DLight{Origin: [3]float32{0, 0, 18.5}, Radius: 220, Die: 11}, 10, 201, true},
		{"on the edge", DLight{Origin: [3]float32{200, 0, 0}, Radius: 200, Die: 11}, 10, 0, false},
		{"out of reach", DLight{Origin: [3]float32{0, 150, 0}, Radius: 100, Die: 11}, 10, -50, false},
		{"died", DLight{Radius: 350, Die: 9.9}, 10, 350, false},
		{"dies now", DLight{Radius: 350, Die: 10}, 10, 350, true},
	} {
		if got := AliasDLightLevel(o, c.lamp.Origin, c.lamp.Radius); got != c.level {
			t.Errorf("%s: level %d, want %d", c.name, got, c.level)
		}
		got := AliasDLightLevels(o, []DLight{c.lamp}, c.now)
		if c.added && !slices.Equal(got, []int{c.level}) || !c.added && len(got) != 0 {
			t.Errorf("%s: levels %v, want %v added=%v", c.name, got, c.level, c.added)
		}
	}
	// every slot is visited (id's loop; tyrquake's never advances its pointer):
	// slot 1 and slot 2 are both counted, slot 0 unused.
	lights := []DLight{{}, {Radius: 100, Die: 11}, {Radius: 50, Die: 11}}
	if got := AliasDLightLevels(o, lights, 10); !slices.Equal(got, []int{100, 50}) {
		t.Errorf("slots 1 and 2: %v, want [100 50]", got)
	}
}

// TestAliasDLightsLightTheEntity: R_AliasSetupLighting with R_LightPoint 40 and
// the levels 350 and 201: ambient 40 + 551 = 591 -> 128, shade 40 (128 + 40 <=
// 192): (255 - 128) << 6 = 8128 and 40 * 64 = 2560. And through the draw: the
// quake front triangle at light 100 is colormap row 19 (TestDrawAliasInterpQuake);
// with a dynamic light of level 80 its ambient is 128 and its shade 192 - 128 =
// 64, the vertex light 8128 + int(4096 * -0.850651) = 4644, row 18: brighter.
func TestAliasDLightsLightTheEntity(t *testing.T) {
	id := Mat3{{1, 0, 0}, {0, 1, 0}, {0, 0, 1}}
	l := NewAliasLighting(40, false, AliasDLightLevels([3]float32{}, []DLight{{Radius: 350, Die: 1}, {Radius: 201, Die: 1}}, 0), id)
	if l.Ambient != 8128 || l.Shade != 2560 {
		t.Errorf("ambient %d shade %v, want 8128 2560", l.Ambient, l.Shade)
	}
	fb, rd, skin := newAliasCtx(t)
	cm := litColormap()
	ent := AliasEntityInterp{AliasEntity: AliasEntity{Origin: [3]float32{100, 0, 0}}, DLights: []int{80}}
	if err := DrawAliasInterpQuake(fb, rd, cm, 100, false, quakeFrontTri(), skin, ent); err != nil {
		t.Fatal(err)
	}
	if got := fb.Pixels[98*fb.Pitch+160]; got != 18 {
		t.Errorf("centre pixel row %d with a dynamic light, want 18 (19 without)", got)
	}
}
