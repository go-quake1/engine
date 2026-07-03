// Copyright (c) 1996-1997 Id Software, Inc.
// Copyright (c) 2026 the go-quake1/engine authors.
// SPDX-License-Identifier: GPL-2.0-or-later

package game

import (
	"strings"
	"testing"

	"github.com/go-quake1/engine/client"
	"github.com/go-quake1/engine/embedpak"
	"github.com/go-quake1/engine/protocol"
	"github.com/go-quake1/engine/render"
)

// newFB allocates a render.FrameBuffer for direct Pre2DDraw calls.
func newFB(t *testing.T, w, h int) (*render.FrameBuffer, error) {
	t.Helper()
	return render.NewFrameBuffer(w, h)
}

// TestEmitClosures drives the EmitTempEntity / EmitParticles / EmitBeam
// client sinks Build installs, covering every temp-entity kind branch.
func TestEmitClosures(t *testing.T) {
	be := &scriptBackend{w: 64, h: 48}
	sess, err := Build(nil, be, Options{})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	c := sess.Client
	origin := [3]float32{10, 20, 30}

	// EmitParticles.
	if c.EmitParticles != nil {
		c.EmitParticles(origin, [3]float32{1, 0, 0}, 5, 10)
	}
	// Every temp-entity kind.
	for _, kind := range []int{
		protocol.TEExplosion, protocol.TETarExplosion,
		protocol.TELavaSplash, protocol.TETeleport,
		protocol.TEGunshot, protocol.TESpike, protocol.TESuperSpike,
		protocol.TEKnightSpike, protocol.TEWizSpike,
		protocol.TESpike + 999, // default: no-op
	} {
		c.EmitTempEntity(kind, origin)
	}
	// Beams of every recognised kind.
	for _, kind := range []int{
		protocol.TELightning1, protocol.TELightning2,
		protocol.TEBeam, protocol.TELightning3, protocol.TESpike + 999,
	} {
		c.EmitBeam(kind, 1, [3]float32{0, 0, 0}, [3]float32{64, 0, 0})
	}

	// Run extra synth frames so the synthetic walkCtx LeafFaces /
	// NodeKind closures traverse leaves during WalkWorld.
	const dt = float32(1.0 / 20.0)
	for f := 0; f < 12; f++ {
		if err := sess.Runner.RunFrame(dt, float32(f)*dt); err != nil {
			t.Fatalf("synth frame %d: %v", f, err)
		}
	}
}

// TestRenderBranchesReal populates the client entity cache + beam/sprite
// pools then calls runner.Pre2DDraw DIRECTLY (bypassing the wire decode
// that rewrites Entities each tic) so the projectile-trail, alias-entity
// interp/clamp, temp-sprite, and beam render arms all execute under
// controlled state.
func TestRenderBranchesReal(t *testing.T) {
	pakFS, err := embedpak.OpenAsFS()
	if err != nil {
		t.Skipf("no real pak (%v)", err)
	}
	be := &scriptBackend{w: 160, h: 120}
	sess, err := Build(pakFS, be, Options{Map: "start"})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	const dt = float32(1.0 / 20.0)
	// Warm up so the player entity + camera anchor land.
	for f := 0; f < 8; f++ {
		if err := sess.Runner.RunFrame(dt, float32(f)*dt); err != nil {
			t.Fatalf("warmup %d: %v", f, err)
		}
	}

	precache := sess.Host.Server.ModelPrecache
	aliasIdx, missileIdx := -1, -1
	for i, n := range precache {
		if i == 0 {
			continue // the alias loop requires ModelIdx > 0
		}
		if missileIdx < 0 && n == "progs/missile.mdl" {
			missileIdx = i
		}
		if aliasIdx < 0 && strings.HasSuffix(n, ".mdl") {
			aliasIdx = i
		}
	}

	r := sess.Runner
	cam := r.ViewOrigin
	near := [3]float32{cam[0] + 40, cam[1], cam[2]}

	// Build a FrameBuffer the size the backend reports.
	fb, err := newFB(t, be.w, be.h)
	if err != nil {
		t.Fatalf("fb: %v", err)
	}

	setEntities := func(missileX float32) {
		ents := map[int]client.EntityState{}
		if aliasIdx > 0 {
			// Lerp in progress (0<lerp<1).
			ents[500] = client.EntityState{ModelIdx: aliasIdx, Frame: 1, PrevFrame: 0, Origin: near, Angles: [3]float32{0, 45, 0}, LerpStartTime: r.Client.MsgTime - 0.02}
			// Out-of-range frame/prevframe -> clamp-to-0.
			ents[501] = client.EntityState{ModelIdx: aliasIdx, Frame: 1 << 20, PrevFrame: -5, Origin: [3]float32{cam[0] + 50, cam[1] + 10, cam[2]}}
			// lerp >> 1 -> high clamp.
			ents[502] = client.EntityState{ModelIdx: aliasIdx, Frame: 2, PrevFrame: 0, Origin: [3]float32{cam[0] + 45, cam[1] - 10, cam[2]}, LerpStartTime: 0.01}
		}
		// ModelIdx 1 = worldmodel slot -> aliasModels[1] == nil -> am==nil skip.
		ents[503] = client.EntityState{ModelIdx: 1, Origin: [3]float32{cam[0] + 30, cam[1], cam[2]}}
		if missileIdx > 0 {
			ents[600] = client.EntityState{ModelIdx: missileIdx, Origin: [3]float32{missileX, near[1], near[2]}}
		}
		r.Client.Entities = ents
	}

	// Spawn an explosion temp-sprite + lightning beams of every bolt kind
	// so the sprite + beam Walk callbacks render every branch.
	r.Client.EmitTempEntity(protocol.TEExplosion, near)
	r.Client.EmitBeam(protocol.TELightning1, 1, cam, near)
	r.Client.EmitBeam(protocol.TELightning2, 1, cam, near)
	r.Client.EmitBeam(protocol.TEBeam, 1, cam, near)
	r.Client.EmitBeam(protocol.TELightning3, 1, cam, near)

	// Call Pre2DDraw directly across several ticks (the missile moves so
	// the trail hadPrev branch fires on the second call).
	missileX := near[0]
	for i := 0; i < 6; i++ {
		missileX += 8
		setEntities(missileX)
		if i == 5 {
			// Drop the missile on the final tic so the projectile-trail
			// stale-entity delete branch (entity no longer seen) runs.
			delete(r.Client.Entities, 600)
		}
		if err := r.Pre2DDraw(fb, cam, [3]float32{0, float32(i * 30), 0}); err != nil {
			t.Fatalf("Pre2DDraw %d: %v", i, err)
		}
	}
}

// TestRenderNilSkinFallbacks installs a custom rendererConfig whose alias
// + bolt models are loaded but carry NIL skins, so the per-entity alias
// skin==nil fallback and the per-beam bskin==nil fallback both fire (the
// real pak's models all ship skins, so these defensive fallbacks are
// otherwise unreachable).
func TestRenderNilSkinFallbacks(t *testing.T) {
	pakFS, err := embedpak.OpenAsFS()
	if err != nil {
		t.Skipf("no real pak (%v)", err)
	}
	be := &scriptBackend{w: 96, h: 72}
	sess, err := Build(pakFS, be, Options{Map: "start"})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	const dt = float32(1.0 / 20.0)
	for f := 0; f < 6; f++ {
		if err := sess.Runner.RunFrame(dt, float32(f)*dt); err != nil {
			t.Fatalf("warmup %d: %v", f, err)
		}
	}
	r := sess.Runner

	precache := sess.Host.Server.ModelPrecache
	aliasModels, aliasSkins, _, _ := loadAliasModels(pakFS, precache)
	// Find a loaded alias slot (>0) and force its skin to nil.
	aliasIdx := -1
	for i := 1; i < len(aliasModels); i++ {
		if aliasModels[i] != nil {
			aliasIdx = i
			aliasSkins[i] = nil
			break
		}
	}
	if aliasIdx < 0 {
		t.Skip("no loaded alias model")
	}

	boltModels, boltSkins, _ := loadBoltModels(pakFS)
	boltSkins[1] = nil // bolt2 / TE_BEAM skin -> nil-skin fallback

	tsp := client.NewTempSpritePool()
	cfg := rendererConfig{
		pakFS:          pakFS,
		realHost:       sess.Host,
		playerSlot:     sess.PlayerSlot,
		fov:            90,
		aliasPrecache:  precache,
		aliasModels:    aliasModels,
		aliasSkins:     aliasSkins,
		particleRNG:    newLCGByteSource(1),
		tempSpritePool: tsp,
		beamPool:       client.NewBeamPool(),
		boltModels:     boltModels,
		boltSkins:      boltSkins,
		// explosionSprite intentionally nil so the temp-sprite Walk's
		// "no sprite -> skip" branch fires.
		explosionSprite: nil,
	}
	if err := setupRenderer(r, cfg); err != nil {
		t.Fatalf("setupRenderer: %v", err)
	}

	cam := r.ViewOrigin
	r.Client.Entities = map[int]client.EntityState{
		aliasIdx: {ModelIdx: aliasIdx, Frame: 0, Origin: [3]float32{cam[0] + 40, cam[1], cam[2]}},
	}
	// Spawn a TE_BEAM lightning bolt so the nil-skin beam fallback fires,
	// plus a temp sprite so the nil-explosionSprite skip branch runs.
	cfg.beamPool.Spawn(protocol.TEBeam, 1, cam, [3]float32{cam[0] + 60, cam[1], cam[2]}, r.Client.MsgTime)
	tsp.Spawn(cam, r.Client.MsgTime, 0)

	fb, err := newFB(t, be.w, be.h)
	if err != nil {
		t.Fatalf("fb: %v", err)
	}
	if err := r.Pre2DDraw(fb, cam, [3]float32{0, 0, 0}); err != nil {
		t.Fatalf("Pre2DDraw: %v", err)
	}
}
