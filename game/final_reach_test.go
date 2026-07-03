// Copyright (c) 1996-1997 Id Software, Inc.
// Copyright (c) 2026 the go-quake1/engine authors.
// SPDX-License-Identifier: GPL-2.0-or-later

package game

import (
	"testing"

	"github.com/go-quake1/engine/bspfile"
	"github.com/go-quake1/engine/embedpak"
	"github.com/go-quake1/engine/model"
	"github.com/go-quake1/engine/progs"
	"github.com/go-quake1/engine/render"
)

// TestPickCameraSynthCentreInLeaf covers pickInMapCamera's "bbox centre
// already lands in a leaf" early return (the synth BSP centre is in a
// valid leaf, unlike the real start.bsp whose centre is solid).
func TestPickCameraSynthCentreInLeaf(t *testing.T) {
	b, size, _ := loadBSP(nil)
	f, err := bspfile.Open(bytesReaderAt(b), size)
	if err != nil {
		t.Fatalf("bspfile.Open: %v", err)
	}
	bm, err := model.LoadBrush(f, 0)
	if err != nil {
		t.Fatalf("LoadBrush: %v", err)
	}
	cam := pickInMapCamera(bm, f)
	_ = cam
	wps := buildDemoWaypoints(bm, f, cam)
	if len(wps) == 0 {
		t.Fatal("expected anchor waypoint")
	}
}

// TestPre2DDrawViewerOutsideLeaf covers the "viewer not in a valid leaf
// -> render nothing" early-return in the real Pre2DDraw path by handing
// it an origin far outside the map.
func TestPre2DDrawViewerOutsideLeaf(t *testing.T) {
	pakFS, err := embedpak.OpenAsFS()
	if err != nil {
		t.Skipf("no real pak (%v)", err)
	}
	be := &scriptBackend{w: 64, h: 48}
	sess, err := Build(pakFS, be, Options{Map: "start"})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	const dt = float32(1.0 / 20.0)
	for f := 0; f < 4; f++ {
		if err := sess.Runner.RunFrame(dt, float32(f)*dt); err != nil {
			t.Fatalf("warmup %d: %v", f, err)
		}
	}
	fb, err := render.NewFrameBuffer(64, 48)
	if err != nil {
		t.Fatalf("fb: %v", err)
	}
	// An origin far outside any leaf -> PointInLeaf <= 0 -> return nil.
	outside := [3]float32{1e9, 1e9, 1e9}
	if err := sess.Runner.Pre2DDraw(fb, outside, [3]float32{0, 0, 0}); err != nil {
		t.Fatalf("Pre2DDraw outside: %v", err)
	}
}

// TestPre2DDrawSkyTurbSweep sweeps the camera across the map's bbox at
// several yaw angles, rendering each pose, so the sky-texture and
// turbulent-water face dispatch arms in the rasterize loop fire (start.bsp
// ships 1 sky + 4 water textures, but neither is visible from the default
// spawn anchor).
func TestPre2DDrawSkyTurbSweep(t *testing.T) {
	pakFS, err := embedpak.OpenAsFS()
	if err != nil {
		t.Skipf("no real pak (%v)", err)
	}
	be := &scriptBackend{w: 80, h: 60}
	sess, err := Build(pakFS, be, Options{Map: "start"})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	const dt = float32(1.0 / 20.0)
	for f := 0; f < 4; f++ {
		if err := sess.Runner.RunFrame(dt, float32(f)*dt); err != nil {
			t.Fatalf("warmup %d: %v", f, err)
		}
	}

	// Bbox from the world model.
	b, size, _ := loadBSP(pakFS)
	file, err := bspfile.Open(bytesReaderAt(b), size)
	if err != nil {
		t.Fatalf("bspfile.Open: %v", err)
	}
	models, err := file.Models()
	if err != nil || len(models) == 0 {
		t.Skip("no world model bbox")
	}
	mins, maxs := models[0].Mins, models[0].Maxs

	fb, err := render.NewFrameBuffer(80, 60)
	if err != nil {
		t.Fatalf("fb: %v", err)
	}
	const steps = 6
	for ix := 0; ix < steps; ix++ {
		for iy := 0; iy < steps; iy++ {
			for iz := 0; iz < steps; iz++ {
				p := [3]float32{
					mins[0] + (maxs[0]-mins[0])*float32(ix+1)/float32(steps+1),
					mins[1] + (maxs[1]-mins[1])*float32(iy+1)/float32(steps+1),
					mins[2] + (maxs[2]-mins[2])*float32(iz+1)/float32(steps+1),
				}
				for yaw := 0; yaw < 360; yaw += 45 {
					_ = sess.Runner.Pre2DDraw(fb, p, [3]float32{0, float32(yaw), 0})
				}
			}
		}
	}
}

// TestBuiltinTraceLineHitsEntity drives builtinTraceLine along a ray that
// passes through a known solid edict so res.EntIdx > 0 and the arena
// MakePointer branch for trace_ent runs.
func TestBuiltinTraceLineHitsEntity(t *testing.T) {
	h, vm, _ := buildRealHost(t)
	if vm.Arena() == nil {
		t.Skip("no arena")
	}
	fn := builtinTraceLine(h)

	// Find a linked solid edict with a non-zero origin and trace toward
	// it from just outside its bbox so the swept line clips it.
	p := h.Progs()
	var target [3]float32
	found := false
	for i := 1; i < len(h.Server.Edicts) && !found; i++ {
		ed := h.Server.Edicts[i]
		if ed == nil || ed.Free {
			continue
		}
		ev, err := progs.NewEntVars(p, ed)
		if err != nil {
			continue
		}
		solid, _ := ev.ReadFloat("solid")
		if solid == 0 {
			continue
		}
		org, err := ev.ReadVec3("origin")
		if err != nil || (org[0] == 0 && org[1] == 0 && org[2] == 0) {
			continue
		}
		target = org
		found = true
	}
	if !found {
		t.Skip("no solid non-world edict to trace into")
	}
	start := [3]float32{target[0] - 200, target[1], target[2]}
	end := [3]float32{target[0] + 200, target[1], target[2]}
	must(t, vm.SetGlobalVector(progs.OfsParm0, start))
	must(t, vm.SetGlobalVector(progs.OfsParm1, end))
	must(t, vm.SetGlobalFloat(progs.OfsParm2, 0))
	must(t, vm.SetGlobalInt(progs.OfsParm3, 0))
	if err := fn(vm); err != nil {
		t.Fatalf("traceline into entity: %v", err)
	}
}
