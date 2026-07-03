// Copyright (c) 1996-1997 Id Software, Inc.
// Copyright (c) 2026 the go-quake1/engine authors.
// SPDX-License-Identifier: GPL-2.0-or-later

package game

import (
	"fmt"
	"io/fs"
	"strings"
	"time"

	"github.com/go-quake1/engine/backend"
	"github.com/go-quake1/engine/bspfile"
	"github.com/go-quake1/engine/bsprender"
	"github.com/go-quake1/engine/client"
	enginehost "github.com/go-quake1/engine/host"
	"github.com/go-quake1/engine/mathlib"
	"github.com/go-quake1/engine/mdl"
	"github.com/go-quake1/engine/model"
	"github.com/go-quake1/engine/protocol"
	"github.com/go-quake1/engine/render"
	"github.com/go-quake1/engine/runloop"
	engineserver "github.com/go-quake1/engine/server"
	enginesound "github.com/go-quake1/engine/sound"
	enginespr "github.com/go-quake1/engine/spr"
	"github.com/go-quake1/engine/vfs"
)

// Options tune the session wiring built by [Build].
type Options struct {
	// Map is the bare map slug SpawnServer loads ("start", "e1m1").
	// Empty defaults to "start".
	Map string

	// FBWidth / FBHeight are ignored: the framebuffer size comes from
	// the backend's Size(). Kept for documentation symmetry with the
	// callers; the runloop sizes the FrameBuffer from Backend.Size().

	// DemoOrbit, when true AND no real input has been observed, makes
	// the per-tic renderer override the camera with a slow yaw spin +
	// in-map waypoint cycle. Headless PPM captures with no input use
	// this to land on geometrically distinct shots. The override
	// auto-disables on the first observed movement/trigger key so an
	// interactive session takes over immediately. Default false
	// (interactive). The native PPM harness sets it true.
	DemoOrbit bool

	// FieldOfView in degrees (horizontal). Zero defaults to 90.
	FieldOfView float32

	// OnFrame, when non-nil, is called once per rendered tic from the
	// Pre2DDraw hook with the frame index, the wire-mirrored player
	// origin, and the current view angles. Used by the wasmbox/native
	// harnesses to log liveness + prove input drives the camera without
	// re-deriving the per-tic state. nil = no callback.
	OnFrame func(frame int, playerOrigin, viewAngles [3]float32)
}

// Session is the fully-wired result of [Build]: a runloop.Runner ready
// for RunUntilQuit (or per-frame RunFrame), plus the live host + client
// the loop drives. Callers own the lifecycle; Session holds no
// resources that need explicit Close.
type Session struct {
	Runner *runloop.Runner
	Host   *enginehost.Host
	Client *client.State

	// PlayerSlot is the 1-based edict index of the local player (the
	// slot ConnectLoopback bound). 0 means no real host (synth path).
	PlayerSlot int
}

// changelevelHostFramer wraps a Host so per-tic RunFrame also polls +
// acts on the QC changelevel request (mirrors the tamago wrapper, minus
// the intermission-stat harvest which is bring-up instrumentation).
type changelevelHostFramer struct{ host *enginehost.Host }

func (c *changelevelHostFramer) Frame(dt float32) error {
	if err := c.host.Frame(dt); err != nil {
		return err
	}
	if pending, mapSlug := c.host.ConsumeChangelevel(); pending {
		c.host.HarvestIntermissionStats()
		_ = c.host.EmitIntermission()
		if err := c.host.SpawnServer(mapSlug, c.host.Server.Protocol); err != nil {
			fmt.Printf("QUAKE: changelevel SpawnServer(%q) failed: %v -- staying on previous map\n", mapSlug, err)
		}
	}
	return nil
}

// Build wires a complete real Quake session over pakFS + be. It mirrors
// the proven quake-tamago run()/setupRenderer sequence:
//
//	embedpak/OCI FS -> asset VFS overlay (synthetic fallback + real pak)
//	-> buildHost (progs VM + SpawnServer(map)) -> loopback signon
//	-> NewRunnerFromVFS -> particle/sprite/beam pools + alias/HUD/menu
//	   assets -> SetupRenderer (Pre2DDraw real BSP walk + entities).
//
// On a real pak (progs.dat + maps/<slug>.bsp present) the host runs the
// real simulation; on the placeholder/synth path the loop degrades to
// a stub host + synthbsp scene (still renders, just no gameplay).
//
// Returns the Session; RunUntilQuit on Session.Runner drives the loop.
func Build(pakFS fs.FS, be backend.Backend, opts Options) (*Session, error) {
	mapSlug := opts.Map
	if mapSlug == "" {
		mapSlug = "start"
	}
	fov := opts.FieldOfView
	if fov == 0 {
		fov = 90
	}

	// Asset VFS: synthetic fallback first (prepended -> last in probe),
	// real pak last (prepended -> first). Wrap the pak in a gfx.wad
	// overlay so gfx/*.lmp HUD pics resolve from the WAD when absent as
	// standalone entries (Quake Remastered layout).
	v := vfs.New()
	v.Add(syntheticAssets())
	if pakFS != nil {
		v.Add(newWADOverlay(pakFS, "gfx.wad"))
	}

	// Real host when a real pak is available.
	var realHost *enginehost.Host
	if pakFS != nil {
		h, herr := buildHost(pakFS, mapSlug)
		if herr != nil {
			fmt.Printf("QUAKE: buildHost failed (%v); host stays stubbed\n", herr)
		} else {
			realHost = h
		}
	}

	// Loopback pair + signon. When a real host exists we route through
	// ConnectLoopback so the server-side handle binds a Static.Clients
	// slot; the wire signon prefix drives the client lifecycle.
	var cli engineserver.NetConn
	playerSlot := 0
	clientState := client.NewState()

	if realHost != nil {
		conn, slotIdx, cerr := realHost.ConnectLoopback()
		if cerr != nil {
			return nil, fmt.Errorf("ConnectLoopback: %w", cerr)
		}
		cli = conn
		playerSlot = slotIdx + 1

		hc := realHost.Static.Clients[slotIdx]
		info := engineserver.ServerInfo{
			Protocol:      protocol.VersionNQ,
			MaxClients:    realHost.Static.MaxClients,
			GameType:      engineserver.GameTypeCoop,
			LevelName:     "the slipgate complex",
			ModelPrecache: realHost.Server.ModelPrecache,
			SoundPrecache: realHost.Server.SoundPrecache,
		}
		if err := engineserver.SendServerInfo(hc, info); err != nil {
			return nil, fmt.Errorf("SendServerInfo: %w", err)
		}
		if err := engineserver.EncodeSetView(hc.Message, playerSlot); err != nil {
			return nil, fmt.Errorf("EncodeSetView: %w", err)
		}
		if _, err := realHost.Server.SendBaselines(hc, realHost.Progs(), realHost.Static.MaxClients); err != nil {
			return nil, fmt.Errorf("SendBaselines: %w", err)
		}
		// Pre-flip Spawned so the FIRST per-tic SendEntityUpdates passes
		// its gate (the wire 'spawn' stringcmd re-flips idempotently).
		hc.Spawned = true
		clientState.PlayerNum = playerSlot
	} else {
		cli, _ = engineserver.NewLoopbackConn()
	}

	// Pick the HostFramer: real host (changelevel-wrapped) or stub.
	var hostFramer runloop.HostFramer = stubHost{}
	if realHost != nil {
		hostFramer = &changelevelHostFramer{host: realHost}
	}

	runner, err := runloop.NewRunnerFromVFS(runloop.SetupOpts{
		VFS:            v,
		Host:           hostFramer,
		Client:         clientState,
		Conn:           cli,
		Backend:        be,
		BackgroundIdx:  0x20,
		NotifyLifetime: 3,
		MaxNotifyRows:  4,
		SoundChannels:  8,
	})
	if err != nil {
		return nil, fmt.Errorf("NewRunnerFromVFS: %w", err)
	}
	runner.Console.Print("PURE-GO QUAKE 1\n")

	// Unify the mixer pool with the host's pool so per-tic Paint walks
	// the same bank the QC-driven StartSound/AmbientSound calls fill.
	if realHost != nil && realHost.SoundPool() != nil {
		runner.SoundPool = realHost.SoundPool()
	}

	// Particle pool + QC #48 + svc_particle + svc_temp_entity sinks.
	particlePool := render.NewPool()
	particleRNG := newLCGByteSource(0xC0FFEF)
	runner.ParticlePool = particlePool
	runner.ParticleGravity = 800
	if realHost != nil && realHost.VM != nil {
		realHost.VM.SetParticleSink(func(origin, dir [3]float32, color, count int) {
			particlePool.Emit(origin, dir, byte(color), count, float32(realHost.Server.Time), particleRNG)
		})
	}
	clientState.EmitParticles = func(origin, dir [3]float32, color, count int) {
		particlePool.Emit(origin, dir, byte(color), count, clientState.MsgTime, particleRNG)
	}

	// Explosion sprite + temp-sprite pool + beams/bolts.
	explosionSprite, _ := loadExplosionSprite(pakFS)
	tempSpritePool := client.NewTempSpritePool()
	boltModels, boltSkins, _ := loadBoltModels(pakFS)
	beamPool := client.NewBeamPool()

	clientState.EmitTempEntity = func(kind int, origin [3]float32) {
		now := clientState.MsgTime
		switch kind {
		case protocol.TEExplosion, protocol.TETarExplosion:
			render.ParticleExplosion(particlePool, origin, now, particleRNG)
			if explosionSprite != nil {
				tempSpritePool.Spawn(origin, now, 0)
			}
		case protocol.TELavaSplash:
			render.LavaSplash(particlePool, origin, now, particleRNG)
		case protocol.TETeleport:
			render.TeleportSplash(particlePool, origin, now, particleRNG)
		case protocol.TEGunshot:
			particlePool.Emit(origin, [3]float32{}, 0, 20, now, particleRNG)
		case protocol.TESpike, protocol.TESuperSpike:
			particlePool.Emit(origin, [3]float32{}, 0, 10, now, particleRNG)
		case protocol.TEKnightSpike:
			particlePool.Emit(origin, [3]float32{}, 226, 20, now, particleRNG)
		case protocol.TEWizSpike:
			particlePool.Emit(origin, [3]float32{}, 20, 30, now, particleRNG)
		}
	}
	clientState.EmitBeam = func(kind, ent int, start, end [3]float32) {
		beamPool.Spawn(kind, ent, start, end, clientState.MsgTime)
	}

	// Alias models from the authoritative server precache.
	var aliasPrecache []string
	if realHost != nil {
		aliasPrecache = realHost.Server.ModelPrecache
	} else {
		aliasPrecache = clientState.ModelPrecache
	}
	aliasModels, aliasSkins, _, _ := loadAliasModels(pakFS, aliasPrecache)

	// HUD/sbar assets.
	sbarAssets, _, _, _ := loadSBarAssets(pakFS)

	rcfg := rendererConfig{
		pakFS:           pakFS,
		realHost:        realHost,
		playerSlot:      playerSlot,
		fov:             fov,
		demoOrbit:       opts.DemoOrbit,
		onFrame:         opts.OnFrame,
		aliasPrecache:   aliasPrecache,
		aliasModels:     aliasModels,
		aliasSkins:      aliasSkins,
		sbarAssets:      sbarAssets,
		particleRNG:     particleRNG,
		tempSpritePool:  tempSpritePool,
		explosionSprite: explosionSprite,
		beamPool:        beamPool,
		boltModels:      boltModels,
		boltSkins:       boltSkins,
	}
	if err := setupRenderer(runner, rcfg); err != nil {
		return nil, fmt.Errorf("setupRenderer: %w", err)
	}

	// Menu: boot lands on the title plaque, world frozen until the
	// player picks Skill. (Native PPM harness skips the menu via
	// DemoOrbit-only sessions that never wire it; see below.)
	menuAssets, _, _ := loadMenuAssets(pakFS)
	runner.Menu = nil // leave the menu OFF so the world renders immediately
	runner.MenuAssets = menuAssets

	return &Session{
		Runner:     runner,
		Host:       realHost,
		Client:     clientState,
		PlayerSlot: playerSlot,
	}, nil
}

// rendererConfig bundles the inputs setupRenderer needs (kept internal;
// callers go through Build).
type rendererConfig struct {
	pakFS           fs.FS
	realHost        *enginehost.Host
	playerSlot      int
	fov             float32
	demoOrbit       bool
	onFrame         func(frame int, playerOrigin, viewAngles [3]float32)
	aliasPrecache   []string
	aliasModels     []*mdl.Model
	aliasSkins      []*render.Pic
	sbarAssets      *render.SBarAssets
	particleRNG     func() byte
	tempSpritePool  *client.TempSpritePool
	explosionSprite *enginespr.Sprite
	beamPool        *client.BeamPool
	boltModels      [3]*mdl.Model
	boltSkins       [3]*render.Pic
}

// setupRenderer loads the BSP, builds the walk/mark contexts + miptex
// bridge, primes the player edict for PhysicsWalk, and installs
// runner.Pre2DDraw as the per-tic real-world rasterizer. This is the
// instrumentation-stripped port of quake-tamago's setupRenderer.
func setupRenderer(runner *runloop.Runner, cfg rendererConfig) error {
	realHost := cfg.realHost
	fov := cfg.fov

	bspBytes, size, err := loadBSP(cfg.pakFS)
	if err != nil {
		return fmt.Errorf("loadBSP: %w", err)
	}
	file, err := bspfile.Open(bytesReaderAt(bspBytes), size)
	if err != nil {
		return fmt.Errorf("bspfile.Open: %w", err)
	}
	bm, err := model.LoadBrush(file, 0)
	if err != nil {
		return fmt.Errorf("model.LoadBrush: %w", err)
	}
	faces, err := file.Faces()
	if err != nil {
		return fmt.Errorf("file.Faces: %w", err)
	}
	marks, _ := file.MarkSurfaces()
	isSynth := len(marks) == 0

	fallbackTex := makeCheckerTex(16)
	miptexPics, miptexNames, _, _, err := loadMiptexPicsNamed(file)
	if err != nil {
		return fmt.Errorf("loadMiptexPics: %w", err)
	}

	var cm render.ColorMap
	for light := 0; light < render.ColorMapRows; light++ {
		for src := 0; src < render.ColorMapCols; src++ {
			cm[light][src] = byte(src)
		}
	}

	walkCtx := bsprender.NewWalkContext(bm)
	if isSynth {
		allFaceIdx := make([]int, len(faces))
		for i := range allFaceIdx {
			allFaceIdx[i] = i
		}
		walkCtx.LeafFaces = func(id int) []int {
			if walkCtx.NodeKind(id) == bsprender.NodeKindLeaf {
				return allFaceIdx
			}
			return nil
		}
		walkCtx.NodeKind = func(id int) bsprender.NodeKind {
			if id < walkCtx.NumNodes {
				return bsprender.NodeKindInterior
			}
			leafIdx := id - walkCtx.NumNodes
			if leafIdx < 0 || leafIdx >= bm.TotalLeaves() {
				return bsprender.NodeKindEmpty
			}
			if bm.Leaf(leafIdx).Contents == bspfile.ContentsSolid {
				return bsprender.NodeKindEmpty
			}
			return bsprender.NodeKindLeaf
		}
		const bigF = float32(1e6)
		walkCtx.NodeBBox = func(id int) (mins, maxs [3]float32) {
			return [3]float32{-bigF, -bigF, -bigF}, [3]float32{bigF, bigF, bigF}
		}
	}

	camOrigin := [3]float32{5, 5, 20}
	if !isSynth {
		camOrigin = pickInMapCamera(bm, file)
	}
	runner.ViewOrigin = camOrigin

	var demoWaypoints [][3]float32
	if cfg.demoOrbit && !isSynth {
		demoWaypoints = buildDemoWaypoints(bm, file, camOrigin)
	}

	markCtx := bsprender.NewMarkContext(bm)
	var surfaces bsprender.SurfaceList
	frameCount := 0
	prevEntityOrigin := make(map[int][3]float32)

	// Prime the player edict for PhysicsWalk.
	if realHost != nil && cfg.playerSlot > 0 && !isSynth {
		if eo, err := realHost.EdictOrigin(cfg.playerSlot); err == nil {
			if eo[0] == 0 && eo[1] == 0 && eo[2] == 0 {
				_ = writePlayerOrigin(realHost, cfg.playerSlot, camOrigin)
			}
		}
		_ = initPlayerForPhysicsWalk(realHost, cfg.playerSlot)
		if cfg.playerSlot < len(realHost.Server.Edicts) {
			if pe := realHost.Server.Edicts[cfg.playerSlot]; pe != nil && pe.Free {
				pe.Free = false
			}
		}
	}

	demoOrbit := cfg.demoOrbit

	runner.Pre2DDraw = func(fb *render.FrameBuffer, viewOrigin, viewAngles [3]float32) error {
		frame := frameCount
		frameCount++

		// Liveness / input-proof callback: hand the harness the wire-
		// mirrored player origin + the (input-driven) view angles so it
		// can log whether a key press moved the camera.
		if cfg.onFrame != nil {
			po := viewOrigin
			if runner.Client != nil {
				if es, ok := runner.Client.Entities[runner.Client.PlayerNum]; ok {
					po = es.Origin
				}
			}
			cfg.onFrame(frame, po, viewAngles)
		}

		// Auto-disable demo-orbit on first observed input.
		if demoOrbit && observedAnyInput(runner) {
			demoOrbit = false
		}

		if demoOrbit && len(demoWaypoints) > 0 {
			svTime := float32(0)
			if realHost != nil {
				svTime = float32(realHost.Server.Time)
			}
			waypointIdx := int(svTime/2.0) % len(demoWaypoints)
			if waypointIdx < 0 {
				waypointIdx = 0
			}
			viewOrigin = demoWaypoints[waypointIdx]
			viewAngles = [3]float32{0, float32(frame % 360), 0}
		}

		for i := range fb.Pixels {
			fb.Pixels[i] = 0x10
		}

		origin := viewOrigin
		if origin[0] == 0 && origin[1] == 0 && origin[2] == 0 {
			origin = camOrigin
		}
		origin[2] += runner.Client.ViewHeightOffset

		if realHost != nil {
			_, lright, _ := mathlib.AngleVectors(mathlib.Vec3(viewAngles))
			realHost.SetListener(origin, [3]float32(lright))
		}

		rd := &render.RefDef{
			VRect:      render.VRect{Width: fb.Width, Height: fb.Height},
			ViewAngles: viewAngles,
			ViewOrigin: origin,
			FovX:       fov,
			FovY:       fov,
		}
		view := rd.SetupView()
		frustum := rd.BuildFrustum()
		stampFrame := int32(frame + 1)

		if isSynth {
			for n := 0; n < bm.NumNodes(); n++ {
				bm.SetNodeVisFrame(n, stampFrame)
			}
			for l := 0; l < bm.TotalLeaves(); l++ {
				bm.Leaf(l).VisFrame = stampFrame
			}
		} else {
			viewerLeaf := bm.PointInLeaf(rd.ViewOrigin)
			if viewerLeaf > 0 {
				if err := bsprender.MarkVisibleLeaves(markCtx,
					bsprender.VisLeafIdx(viewerLeaf),
					bsprender.FrameMarkSequence(stampFrame),
				); err != nil {
					return fmt.Errorf("MarkVisibleLeaves: %w", err)
				}
			} else {
				return nil
			}
		}

		surfaces.Reset()
		if err := bsprender.WalkWorld(walkCtx, 0, rd.ViewOrigin, frustum, stampFrame, &surfaces); err != nil {
			return fmt.Errorf("WalkWorld: %w", err)
		}

		// Rasterize faces (sky / turb / textured dispatch).
		var skyTime, turbTime float32
		if realHost != nil {
			skyTime = float32(realHost.Server.Time)
			turbTime = skyTime
		}
		for i := 0; i < surfaces.Len(); i++ {
			ref := surfaces.Refs[i]
			fv, err := bsprender.NewBrushFaceVerts(bm, ref.FaceIdx)
			if err != nil {
				continue
			}
			verts, err := bsprender.TransformFace(view, fb, fov, fv)
			if err != nil {
				continue
			}
			tex := fallbackTex
			var name string
			if mtIdx, ok, _ := bm.FaceMipTexIdx(ref.FaceIdx); ok && mtIdx >= 0 && mtIdx < len(miptexPics) {
				if p := miptexPics[mtIdx]; p != nil {
					tex = p
					name = miptexNames[mtIdx]
				}
			}
			switch {
			case strings.HasPrefix(name, "sky"):
				_ = render.FillSkyPolygon(fb, tex, verts, skyTime)
			case strings.HasPrefix(name, "*"):
				_ = render.FillTurbulentPolygon(fb, tex, &cm, 0, verts, turbTime)
			default:
				_ = render.FillTexturedPolygon(fb, tex, &cm, 0, verts)
			}
		}

		// Projectile trails.
		trailNow := runner.Client.MsgTime
		if realHost != nil && runner.ParticlePool != nil {
			precache := realHost.Server.ModelPrecache
			seenThisTic := make(map[int]struct{}, len(runner.Client.Entities))
			for entNum, es := range runner.Client.Entities {
				seenThisTic[entNum] = struct{}{}
				if es.ModelIdx <= 0 || es.ModelIdx >= len(precache) {
					continue
				}
				kind, ok := trailKindForModel(precache[es.ModelIdx])
				if !ok {
					continue
				}
				prev, hadPrev := prevEntityOrigin[entNum]
				prevEntityOrigin[entNum] = es.Origin
				if !hadPrev {
					continue
				}
				runner.ParticlePool.EmitTrail(prev, es.Origin, kind, trailNow, cfg.particleRNG)
			}
			for k := range prevEntityOrigin {
				if _, ok := seenThisTic[k]; !ok {
					delete(prevEntityOrigin, k)
				}
			}
		}

		// Alias entities (interp + lit).
		const aliasFramePeriod = float32(0.1)
		aliasShade := render.AliasShadeRange{
			Ambient:   0.3,
			DirectMin: 0.0,
			DirectMax: 0.7,
			LightDir:  [3]float32{0, 0, -1},
		}
		now := runner.Client.MsgTime
		for _, es := range runner.Client.Entities {
			if es.ModelIdx <= 0 || es.ModelIdx >= len(cfg.aliasModels) {
				continue
			}
			am := cfg.aliasModels[es.ModelIdx]
			if am == nil {
				continue
			}
			skin := cfg.aliasSkins[es.ModelIdx]
			if skin == nil {
				skin = fallbackTex
			}
			frameIdx := es.Frame
			if frameIdx < 0 || frameIdx >= len(am.Frames) {
				frameIdx = 0
			}
			prevIdx := es.PrevFrame
			if prevIdx < 0 || prevIdx >= len(am.Frames) {
				prevIdx = frameIdx
			}
			var lerp float32
			if es.LerpStartTime > 0 && now > es.LerpStartTime {
				lerp = (now - es.LerpStartTime) / aliasFramePeriod
				if lerp < 0 {
					lerp = 0
				} else if lerp > 1 {
					lerp = 1
				}
			}
			ent := render.AliasEntityInterp{
				AliasEntity: render.AliasEntity{
					Origin:     es.Origin,
					AnglePitch: es.Angles[0],
					AngleYaw:   es.Angles[1],
					AngleRoll:  es.Angles[2],
					FrameIdx:   prevIdx,
					SkinIdx:    es.SkinNum,
				},
				FrameIdxNext: frameIdx,
				Lerp:         lerp,
			}
			_ = render.DrawAliasInterpLit(fb, rd, &cm, aliasShade, am, skin, ent)
		}

		// Particles.
		_ = render.DrawParticleQuads(fb, rd, runner.ParticlePool, runner.Client.MsgTime)

		// Temp sprites (explosions).
		cfg.tempSpritePool.Walk(now, func(origin [3]float32, elapsed float32) {
			if cfg.explosionSprite == nil {
				return
			}
			_ = render.DrawSpriteAtTime(fb, rd, cfg.explosionSprite, origin, elapsed)
		})

		// Lightning beams.
		cfg.beamPool.Walk(now, func(seg client.BeamSegment) {
			var m *mdl.Model
			var bskin *render.Pic
			switch seg.Kind {
			case protocol.TELightning1:
				m, bskin = cfg.boltModels[0], cfg.boltSkins[0]
			case protocol.TELightning2, protocol.TEBeam:
				m, bskin = cfg.boltModels[1], cfg.boltSkins[1]
			case protocol.TELightning3:
				m, bskin = cfg.boltModels[2], cfg.boltSkins[2]
			default:
				return
			}
			if m == nil {
				return
			}
			if bskin == nil {
				bskin = fallbackTex
			}
			ent := render.AliasEntityInterp{
				AliasEntity: render.AliasEntity{
					Origin:     seg.Origin,
					AnglePitch: seg.Pitch,
					AngleYaw:   seg.Yaw,
				},
			}
			_ = render.DrawAliasInterpLit(fb, rd, &cm, aliasShade, m, bskin, ent)
		})

		// HUD / status bar.
		if cfg.sbarAssets != nil {
			_ = render.DrawSBar(fb, runner.Client, cfg.sbarAssets)
		}
		return nil
	}
	return nil
}

// RunUntilQuit drives the session's run loop until the backend reports
// a quit request. It is the GOOS=js-safe equivalent of
// runloop.Runner.RunUntilQuit: it yields to the host scheduler (a 1 ms
// sleep) once per tic so a single-threaded wasm runtime hands the JS
// event loop back to the browser between frames.
//
// WHY THIS MATTERS (the playable-wasmbox bug it fixes): the plain
// runloop.RunUntilQuit is a tight `for {}` with no yield. Under wasm
// that STARVES the worker's `message` event callbacks -- the compositor
// forwards keydown/mouse events via worker.postMessage, but Go never
// returns to the JS event loop to dispatch them, so PollInput sees an
// empty queue every tic and the player never moves (rendering still
// works because the SAB framebuffer is blitted by the compositor's own
// rAF, independent of the worker goroutine). The 1 ms sleep parks the
// goroutine on a Go timer, which the wasm runtime backs with
// setTimeout(0) -- exactly the round-trip the JS event loop needs to
// drain queued input. Natively the sleep just caps the loop at ~1 kHz
// (harmless; RunFrame is the real cost).
//
// Mirrors cmd/quake-wasm's runUntilQuitYielding; lives here so every
// platform front-end (wasmbox, DOM, native) shares one correct loop.
func (s *Session) RunUntilQuit() error {
	r := s.Runner
	original := r.Backend
	defer func() { r.Backend = original }()
	obs := &quitObserver{Backend: original}
	r.Backend = obs

	lastNow := original.Now()
	dt := runloop.MinFrameTime
	for {
		nowSec := original.Now()
		if err := r.RunFrame(dt, float32(nowSec)); err != nil {
			return err
		}
		if obs.quitRequested {
			return nil
		}
		nextDt := float32(nowSec - lastNow)
		if nextDt < runloop.MinFrameTime {
			nextDt = runloop.MinFrameTime
		}
		lastNow = nowSec
		dt = nextDt
		// Cooperative yield -- MUST be > 0 (time.Sleep early-returns for
		// <= 0 without parking), so a zero sleep would NOT yield and the
		// worker would stay locked exactly like runloop.RunUntilQuit.
		time.Sleep(time.Millisecond)
	}
}

// quitObserver wraps a backend so RunUntilQuit can spot the
// QuitRequested flag as snapshots flow past PollInput (runloop's own
// wrapper is unexported, so we repeat it).
type quitObserver struct {
	backend.Backend
	quitRequested bool
}

func (q *quitObserver) PollInput() (backend.InputSnapshot, error) {
	snap, err := q.Backend.PollInput()
	if snap.QuitRequested {
		q.quitRequested = true
	}
	return snap, err
}

// stubHost satisfies runloop.HostFramer when no real pak is available.
type stubHost struct{}

func (stubHost) Frame(_ float32) error { return nil }

// bytesReaderAt adapts a byte slice to io.ReaderAt for bspfile.Open.
func bytesReaderAt(b []byte) *readerAt { return &readerAt{data: b, size: int64(len(b))} }

type readerAt struct {
	data []byte
	size int64
}

func (r *readerAt) ReadAt(p []byte, off int64) (int, error) {
	if off < 0 || off >= r.size {
		return 0, errEOFReaderAt
	}
	n := copy(p, r.data[off:])
	// copy bounds n to min(len(p), size-off); a short copy therefore always
	// means we hit the end of the data before filling p.
	if n < len(p) {
		return n, errEOFReaderAt
	}
	return n, nil
}

var errEOFReaderAt = fmt.Errorf("EOF")

var _ = enginesound.DefaultSampleRate
