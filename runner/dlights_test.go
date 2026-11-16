// Copyright (c) 2026 the go-quake1/engine authors.
// SPDX-License-Identifier: GPL-2.0-or-later

package runner

import (
	"testing"

	"github.com/go-quake1/engine/bsprender"
	"github.com/go-quake1/engine/client"
	"github.com/go-quake1/engine/render"
)

// The expected values are worked from reference/NQ/cl_main.c (CL_AllocDlight,
// CL_DecayLights, CL_RelinkEntities) and cl_tent.c (TE_EXPLOSION).

func TestAllocDLight(t *testing.T) {
	var l [client.MaxDLights]client.DLight
	// every slot dead (die 0 < now 5): the first
	if d := allocDLight(&l, 0, 5); d != &l[0] {
		t.Fatal("first free slot is not slot 0")
	}
	l[0] = client.DLight{Key: 0, Die: 10, Radius: 100}
	l[1] = client.DLight{Key: 7, Die: 10, Radius: 100, Decay: 3}
	// key 7 finds its own slot and clears it, keeping the key
	d := allocDLight(&l, 7, 5)
	if d != &l[1] || *d != (client.DLight{Key: 7}) {
		t.Fatalf("key match: slot %p %+v", d, *d)
	}
	// key 0, or an unknown key: the first light that died (die < now): slot 1,
	// which the key match just cleared (die 0)
	if d := allocDLight(&l, 0, 5); d != &l[1] {
		t.Fatal("the first dead slot was not taken")
	}
	// all alive: slot 0 is taken over
	for i := range l {
		l[i] = client.DLight{Die: 100, Radius: 50, Key: i + 100}
	}
	if d := allocDLight(&l, 9, 5); d != &l[0] || d.Key != 9 || d.Radius != 0 {
		t.Fatalf("full pool: %+v", *d)
	}
}

func TestDecayDLights(t *testing.T) {
	var l [client.MaxDLights]client.DLight
	l[0] = client.DLight{Radius: 350, Die: 10, Decay: 300}
	l[1] = client.DLight{Radius: 350, Die: 1, Decay: 300}  // dead at now = 2
	l[2] = client.DLight{Radius: 100, Die: 10, Decay: 300} // 0.5 s takes 150: floor 0
	l[3] = client.DLight{Radius: 0, Die: 10, Decay: 300}   // no radius: skipped
	decayDLights(&l, 2, 0.5)
	if l[0].Radius != 200 || l[1].Radius != 350 || l[2].Radius != 0 || l[3].Radius != 0 {
		t.Fatalf("radii %v %v %v %v, want 200 350 0 0", l[0].Radius, l[1].Radius, l[2].Radius, l[3].Radius)
	}
}

func TestEntityDLights(t *testing.T) {
	rnd := func() int { return 7 }
	es := client.EntityState{Origin: [3]float32{100, 200, 50}, Angles: [3]float32{0, 90, 0}}

	var l [client.MaxDLights]client.DLight
	es.Effects = efMuzzleFlash
	entityDLights(&l, 3, es, false, 10, rnd)
	// origin + (0, 0, 16) + 18 * forward; forward at yaw 90 is (0, 1, 0)
	got := l[0]
	if !near(got.Origin[0], 100) || !near(got.Origin[1], 218) || !near(got.Origin[2], 66) || got.Radius != 207 || got.MinLight != 32 || !near(got.Die, 10.1) || got.Key != 3 {
		t.Errorf("muzzle flash %+v, want origin (100, 218, 66), radius 207, minlight 32, die 10.1, key 3", got)
	}
	// the same entity's flash comes back in its own slot
	entityDLights(&l, 3, es, false, 10.05, rnd)
	if l[1] != (client.DLight{}) {
		t.Errorf("a second flash of the same entity took slot 1: %+v", l[1])
	}

	// pitch 90 (looking down): forward is (0, 0, -1)
	l = [client.MaxDLights]client.DLight{}
	es.Angles = [3]float32{90, 0, 0}
	entityDLights(&l, 3, es, false, 10, rnd)
	if got := l[0]; !near(got.Origin[0], 100) || !near(got.Origin[1], 200) || !near(got.Origin[2], 50+16-18) {
		t.Errorf("flash looking down at %v, want (100, 200, 48)", got.Origin)
	}

	for _, c := range []struct {
		name   string
		fx     int
		rocket bool
		radius float32
		z      float32
		die    float32
	}{
		{"bright light", efBrightLight, false, 407, 66, 10.001},
		{"dim light", efDimLight, false, 207, 50, 10.001},
		{"red", efRed, false, 207, 50, 10.001},
		{"blue", efBlue, false, 207, 50, 10.001},
		{"red and blue are one light", efRed | efBlue, false, 207, 50, 10.001},
		{"rocket", 0, true, 200, 50, 10.01},
	} {
		l = [client.MaxDLights]client.DLight{}
		es.Effects, es.Angles = c.fx, [3]float32{}
		entityDLights(&l, 4, es, c.rocket, 10, rnd)
		n := 0
		for _, d := range l {
			if d.Radius != 0 {
				n++
			}
		}
		if got := l[0]; n != 1 || got.Radius != c.radius || !near(got.Origin[2], c.z) || !near(got.Die, c.die) || got.Origin[0] != 100 {
			t.Errorf("%s: %d lights, first %+v; want one of radius %v at z %v dying at %v", c.name, n, got, c.radius, c.z, c.die)
		}
	}
	// no effect, no light
	l = [client.MaxDLights]client.DLight{}
	es.Effects = 0
	entityDLights(&l, 4, es, false, 10, rnd)
	if l[0] != (client.DLight{}) {
		t.Errorf("a plain entity made a light: %+v", l[0])
	}
}

func near(a, b float32) bool {
	d := a - b
	return d > -1e-3 && d < 1e-3
}

func TestExplosionDLight(t *testing.T) {
	var l [client.MaxDLights]client.DLight
	explosionDLight(&l, [3]float32{1, 2, 3}, 20)
	if got := l[0]; got.Origin != [3]float32{1, 2, 3} || got.Radius != 350 || !near(got.Die, 20.5) || got.Decay != 300 || got.Key != 0 {
		t.Fatalf("explosion %+v", got)
	}
	// 0.5 s of decay: 350 - 150 = 200
	decayDLights(&l, 20.1, 0.5)
	if l[0].Radius != 200 {
		t.Fatalf("decayed to %v, want 200", l[0].Radius)
	}
}

// TestWorldSurfacesDynamicLights: with a muzzle flash, a rocket and an explosion
// around the view, the runner's surface-cache world (R_PushDlights through its
// own face marks) draws the same pixels as worldview.DrawSoftware (the same
// lights through the world's marks): 0 differ, and the lights do change the
// frame. Needs QUAKE_PAK.
func TestWorldSurfacesDynamicLights(t *testing.T) {
	w, cm := loadE1M1(t)
	ws, err := newWorldSurfaces(w.File)
	if err != nil {
		t.Fatal(err)
	}
	faces, _ := w.File.Faces()
	ws.setLights(bsprender.NewDLightTree(w.BM), len(faces))
	rest := bsprender.NormalLightStyle
	moved, diff := 0, 0
	for k, v := range w.Views() {
		var cl [client.MaxDLights]client.DLight
		cl[0] = client.DLight{Origin: [3]float32{v.Origin[0], v.Origin[1] + 18, v.Origin[2] + 16}, Radius: 220, MinLight: 32, Die: 0.1}
		cl[1] = client.DLight{Origin: [3]float32{v.Origin[0] + 160, v.Origin[1], v.Origin[2]}, Radius: 200, Die: 0.01}
		cl[2] = client.DLight{Origin: [3]float32{v.Origin[0] - 200, v.Origin[1], v.Origin[2] + 40}, Radius: 350, Die: 0.5, Decay: 300}
		w.Now = 0
		w.DLights = [render.MaxDLights]render.DLight{}
		plain, _ := render.NewFrameBuffer(320, 200)
		if err := w.DrawSoftware(plain, cm, v); err != nil {
			t.Fatal(err)
		}
		for i, l := range cl {
			w.DLights[i] = render.DLight{Origin: l.Origin, Radius: l.Radius, Die: l.Die, Decay: l.Decay, MinLight: l.MinLight}
		}
		ref, _ := render.NewFrameBuffer(320, 200)
		if err := w.DrawSoftware(ref, cm, v); err != nil {
			t.Fatal(err)
		}
		got, _ := render.NewFrameBuffer(320, 200)
		drawRunnerWorld(t, ws, w, cm, v, got, true, rest, &cl)
		if d := countDiff(ref.Pixels, got.Pixels); d != 0 {
			t.Errorf("view %d: %d pixels differ from worldview.DrawSoftware with the same lights", k, d)
			diff += d
		}
		moved += countDiff(plain.Pixels, ref.Pixels)
	}
	if moved == 0 {
		t.Fatal("the lights changed no pixel: the test pins nothing")
	}
	t.Logf("16 views: the lights change %d of %d pixels; %d differ between the two paths", moved, 16*320*200, diff)
}
