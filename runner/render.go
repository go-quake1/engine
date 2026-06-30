// Copyright (c) 1996-1997 Id Software, Inc.
// Copyright (c) 2026 the go-quake1/engine authors.
// SPDX-License-Identifier: BSD-3-Clause

package runner

import (
	"bytes"
	"fmt"
	"io/fs"
	"strings"

	"github.com/go-quake1/engine/assets"
	"github.com/go-quake1/engine/bspfile"
	"github.com/go-quake1/engine/bsprender"
	"github.com/go-quake1/engine/client"
	enginehost "github.com/go-quake1/engine/host"
	"github.com/go-quake1/engine/mathlib"
	"github.com/go-quake1/engine/mdl"
	"github.com/go-quake1/engine/model"
	"github.com/go-quake1/engine/progs"
	"github.com/go-quake1/engine/protocol"
	"github.com/go-quake1/engine/render"
	"github.com/go-quake1/engine/runloop"
	enginesound "github.com/go-quake1/engine/sound"
	enginespr "github.com/go-quake1/engine/spr"
	"github.com/go-quake1/engine/vfs"
)

// setupRendererOpts bundles the (many) parameters [setupRenderer] consumes.
type setupRendererOpts struct {
	runner          *runloop.Runner
	pakFS           fs.FS
	searchPath      *vfs.SearchPath
	realHost        *enginehost.Host
	playerSlot      int
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
	state           *runtimeState
	logf            func(string, ...any)
}

// setupRenderer loads the BSP, builds the mark/walk contexts + synthetic
// texture + identity colormap, anchors the camera origin, and installs
// runner.Pre2DDraw as a closure that rasterizes one frame per call.
func setupRenderer(opts setupRendererOpts) error {
	runner := opts.runner
	pakFS := opts.pakFS
	realHost := opts.realHost
	playerSlot := opts.playerSlot
	aliasPrecache := opts.aliasPrecache
	aliasModels := opts.aliasModels
	aliasSkins := opts.aliasSkins
	sbarAssets := opts.sbarAssets
	particleRNG := opts.particleRNG
	tempSpritePool := opts.tempSpritePool
	explosionSprite := opts.explosionSprite
	beamPool := opts.beamPool
	boltModels := opts.boltModels
	boltSkins := opts.boltSkins
	state := opts.state
	logf := opts.logf

	bspBytes, size, err := loadBSP(pakFS, logf)
	if err != nil {
		return fmt.Errorf("loadBSP: %w", err)
	}
	file, err := bspfile.Open(bytes.NewReader(bspBytes), size)
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
	logf("BSP loaded -- %d nodes, %d leaves (PVS), %d faces, %d marksurfaces (synth=%v)",
		bm.NumNodes(), bm.NumLeaves(), len(faces), len(marks), isSynth)

	fallbackTex := makeCheckerTex(16)

	miptexPics, miptexNames, loaded, total, err := loadMiptexPicsNamed(file)
	if err != nil {
		return fmt.Errorf("loadMiptexPics: %w", err)
	}
	logf("loaded %d miptexes from BSP (total slots: %d, loaded: %d, null: %d)",
		loaded, total, loaded, total-loaded)
	var nSky, nTurb, nAnim int
	for _, n := range miptexNames {
		switch {
		case strings.HasPrefix(n, "sky"):
			nSky++
		case strings.HasPrefix(n, "*"):
			nTurb++
		case strings.HasPrefix(n, "+"):
			nAnim++
		}
	}
	miptexChains := BuildMiptexChains(miptexNames)
	logf("miptex specials -- sky=%d liquid=%d anim=%d (chains=%d)",
		nSky, nTurb, nAnim, miptexChains.NumChains())

	cm := loadColorMapOrFallback(opts.searchPath, logf)

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

	const fovX = 90.0
	camOrigin := [3]float32{5, 5, 20}
	if !isSynth {
		camOrigin = pickInMapCamera(bm, file)
		logf("camera origin %v", camOrigin)
	}
	runner.ViewOrigin = camOrigin

	var demoWaypoints [][3]float32
	if state.demoOrbit && !isSynth {
		demoWaypoints = buildDemoWaypoints(bm, file, camOrigin)
		logf("demoOrbit ON -- %d waypoints + 1-degree-per-frame yaw spin", len(demoWaypoints))
		for i, wp := range demoWaypoints {
			logf("  waypoint[%d] = %v", i, wp)
		}
	}

	markCtx := bsprender.NewMarkContext(bm)
	var surfaces bsprender.SurfaceList
	frameCount := 0
	prevEntityOrigin := make(map[int][3]float32)
	loggedWireSpawn := false

	// Seed the player edict for PhysicsWalk.
	if realHost != nil && playerSlot > 0 && !isSynth {
		if eo, err := realHost.EdictOrigin(playerSlot); err == nil {
			if eo[0] == 0 && eo[1] == 0 && eo[2] == 0 {
				_ = writePlayerOrigin(realHost, playerSlot, camOrigin)
				logf("seeded player edict %d origin = %v (was zero)", playerSlot, camOrigin)
			}
		}
		if err := initPlayerForPhysicsWalk(realHost, playerSlot); err != nil {
			logf("initPlayerForPhysicsWalk(%d) failed: %v -- PhysicsWalk may not dispatch",
				playerSlot, err)
		} else {
			logf("player edict %d primed for PhysicsWalk (movetype=Walk solid=SlideBox hull1 mins/maxs)",
				playerSlot)
		}
		if playerSlot < len(realHost.Server.Edicts) {
			if pe := realHost.Server.Edicts[playerSlot]; pe != nil && pe.Free {
				pe.Free = false
				logf("claimed player edict %d (Free=true -> false) so per-tic svc_update emits it",
					playerSlot)
			}
		}
	}

	runner.Pre2DDraw = func(fb *render.FrameBuffer, viewOrigin, viewAngles [3]float32) error {
		frame := frameCount
		frameCount++

		if state.demoOrbit && state.demoOrbitAutoDisableOnInput && observedAnyInput(runner) {
			state.demoOrbit = false
			logf("demo-orbit auto-disabled at tic %d (input observed -- player takes over)", frame)
		}

		waypointIdx := -1
		if state.demoOrbit && len(demoWaypoints) > 0 {
			svTime := float32(0)
			if realHost != nil {
				svTime = float32(realHost.Server.Time)
			}
			waypointIdx = int(svTime/DemoWaypointPeriodSeconds) % len(demoWaypoints)
			if waypointIdx < 0 {
				waypointIdx = 0
			}
			viewOrigin = demoWaypoints[waypointIdx]
			viewAngles = [3]float32{
				0,
				float32(frame % DemoYawPeriodFrames),
				0,
			}
			if frame%60 == 0 {
				logf("demo-orbit tic %d -- waypoint[%d]=%v yaw=%v",
					frame, waypointIdx, viewOrigin, viewAngles[1])
			}
		}

		for i := range fb.Pixels {
			fb.Pixels[i] = 0x10
		}

		origin := viewOrigin
		fromEntities := true
		if origin[0] == 0 && origin[1] == 0 && origin[2] == 0 {
			origin = camOrigin
			fromEntities = false
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
			FovX:       fovX,
			FovY:       fovX,
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

		if frame < 6 {
			logf("signon trace tic %d -- clientConn=%d Spawned=%v viewh=%v health=%d baselines=%d",
				frame, int(runner.Client.Connection),
				runner.Client.Spawned,
				runner.Client.ViewHeightOffset, runner.Client.Health,
				len(runner.Client.Baselines))
		}

		if !loggedWireSpawn && realHost != nil && playerSlot > 0 {
			if c := realHost.Static.Clients[playerSlot-1]; c != nil && c.Spawned {
				logf("server-side Spawned observed true (tic %d, slot %d) -- per-tic svc_update broadcast enabled",
					frame, playerSlot-1)
				loggedWireSpawn = true
			}
		}

		if frame%60 == 0 {
			active := 0
			if runner.SoundPool != nil {
				active = runner.SoundPool.ActiveCount()
			}
			cmdFwd, cmdSide := float32(0), float32(0)
			if realHost != nil && playerSlot > 0 {
				if c := realHost.Static.Clients[playerSlot-1]; c != nil {
					cmdFwd = c.Cmd.ForwardMove
					cmdSide = c.Cmd.SideMove
				}
			}
			viewSrc := "state.Entities"
			if !fromEntities {
				viewSrc = "fallback(pickInMapCamera)"
			}
			entOrigin := [3]float32{}
			entPresent := false
			if es, ok := runner.Client.Entities[runner.Client.PlayerNum]; ok {
				entOrigin = es.Origin
				entPresent = true
			}
			soundsStarted := 0
			ambientsStarted := 0
			if realHost != nil {
				soundsStarted = realHost.LastSoundsStarted
				ambientsStarted = realHost.LastAmbientsStarted
			}
			activeParticles := 0
			if runner.ParticlePool != nil {
				activeParticles = runner.ParticlePool.NumAlive
			}
			logf("tic %d -- viewOrigin=%v src=%s entOrigin=%v entPresent=%v (PlayerNum=%d, %d entities cached) viewAngles=%v cmd.fwd=%v cmd.side=%v clientConn=%d cl.vel=%v cl.viewh=%v cl.health=%d; %d surfaces; audio: %d active, %d mixed; sounds_started=%d ambients_started=%d; particles: %d active",
				frame, origin, viewSrc, entOrigin, entPresent,
				runner.Client.PlayerNum, len(runner.Client.Entities),
				viewAngles, cmdFwd, cmdSide,
				int(runner.Client.Connection),
				runner.Client.Velocity, runner.Client.ViewHeightOffset, runner.Client.Health,
				surfaces.Len(),
				active, enginesound.MixBufferStereoFrames,
				soundsStarted, ambientsStarted,
				activeParticles)

			if realHost != nil && realHost.SoundPool() != nil {
				pool := realHost.SoundPool()
				for i := range pool.Channels {
					if pool.Channels[i].Sfx == nil {
						continue
					}
					ch := &pool.Channels[i]
					logf("spatialize sample tic %d -- ch[%d] ent=%d L=%d R=%d master=%v",
						frame, i, ch.EntNum, ch.LeftVol, ch.RightVol, ch.Master)
					break
				}
			}

			if frame == 60 && len(runner.Client.Entities) > 0 {
				minK, maxK := -1, -1
				hasPlayer := false
				for k := range runner.Client.Entities {
					if minK == -1 || k < minK {
						minK = k
					}
					if k > maxK {
						maxK = k
					}
					if k == runner.Client.PlayerNum {
						hasPlayer = true
					}
				}
				logf("Entities-map census tic 60 -- count=%d minKey=%d maxKey=%d hasPlayerKey(PlayerNum=%d)=%v",
					len(runner.Client.Entities), minK, maxK,
					runner.Client.PlayerNum, hasPlayer)
			}

			if realHost != nil {
				logf("updates tic %d -- %d entities sent / %d entities received in state.Entities",
					frame, realHost.LastEntityUpdatesSent, len(runner.Client.Entities))
			}
		}

		if realHost != nil && (frame < 12 || frame%30 == 0) {
			logf("thinks tic %d -- %d dispatched, %d errored (missing builtins are non-fatal)",
				frame, realHost.LastThinksDispatched, realHost.LastThinkErrors)
			for _, msg := range realHost.LastThinkErrorMsgs {
				logf("think error -- %s", msg)
			}
			logf("touches tic %d -- %d dispatched, %d errored",
				frame, realHost.LastTriggerTouches, realHost.LastTouchErrors)
			for _, msg := range realHost.LastTouchErrorMsgs {
				logf("touch error -- %s", msg)
			}
			if p := realHost.Progs(); p != nil {
				base := realHost.Static.MaxClients + 1
				scheduled := 0
				framesAdvanced := 0
				maxFrame := float32(0)
				minNext := float32(0)
				sample := ""
				for i := base; i < realHost.Server.NumEdicts; i++ {
					e := realHost.Server.Edicts[i]
					if e == nil || e.Free {
						continue
					}
					ev, evErr := progs.NewEntVars(p, e)
					if evErr != nil {
						continue
					}
					nt, _ := ev.ReadFloat("nextthink")
					f, _ := ev.ReadFloat("frame")
					if nt > 0 {
						scheduled++
						if minNext == 0 || nt < minNext {
							minNext = nt
						}
					}
					if f > 0 {
						framesAdvanced++
						if f > maxFrame {
							maxFrame = f
						}
						if sample == "" {
							th, _ := ev.ReadInt32("think")
							sample = fmt.Sprintf(" first-with-frame=[slot=%d frame=%.0f nextthink=%.3f think=%d]",
								i, f, nt, th)
						}
					}
				}
				logf("think-census tic %d sv.time=%.3f -- %d edicts with future nextthink (soonest=%.3f), %d edicts with frame>0 (max=%.0f)%s",
					frame, realHost.Server.Time, scheduled, minNext, framesAdvanced, maxFrame, sample)
			}
		}

		skyTimeSec := float32(0)
		turbTimeSec := float32(0)
		if realHost != nil {
			skyTimeSec = float32(realHost.Server.Time)
			turbTimeSec = float32(realHost.Server.Time)
		}
		// Cache the Lighting() lump once per frame; FaceLightmapInfo
		// hands back byte offsets into this slice.
		lightingLump := bm.File.Lighting()
		// Painter's algorithm: WalkWorld emits faces FRONT-to-BACK
		// (designed for a span buffer where the first-touch wins). The
		// lightmapped rasterizer below has no span/Z buffer -- it
		// overwrites every pixel of the polygon unconditionally -- so
		// drawing in that order makes FAR faces paint over the NEAR
		// ones (visible failure mode: stairs with the higher steps
		// drawing over the lower steps that should occlude them). We
		// iterate in REVERSE to flip the order to BACK-to-FRONT, so
		// the near surface is the LAST writer and wins. BSP guarantees
		// this is correct for opaque world surfaces.
		for i := surfaces.Len() - 1; i >= 0; i-- {
			ref := surfaces.Refs[i]
			fv, err := bsprender.NewBrushFaceVerts(bm, ref.FaceIdx)
			if err != nil {
				continue
			}
			tex := fallbackTex
			var name string
			if mtIdx, ok, _ := bm.FaceMipTexIdx(ref.FaceIdx); ok && mtIdx >= 0 && mtIdx < len(miptexPics) {
				// Resolve animated chains via cl.time at 5 Hz
				// (tyrquake R_TextureAnimation). Non-animated
				// slots resolve to themselves.
				effIdx := miptexChains.Pic(mtIdx, turbTimeSec)
				if effIdx < 0 || effIdx >= len(miptexPics) {
					effIdx = mtIdx
				}
				if p := miptexPics[effIdx]; p != nil {
					tex = p
					name = miptexNames[effIdx]
				} else if p := miptexPics[mtIdx]; p != nil {
					tex = p
					name = miptexNames[mtIdx]
				}
			}
			switch {
			case strings.HasPrefix(name, "sky"):
				verts, err := bsprender.TransformFace(view, fb, fovX, fv)
				if err != nil {
					continue
				}
				_ = render.FillSkyPolygon(fb, tex, verts, skyTimeSec)
			case strings.HasPrefix(name, "*"):
				verts, err := bsprender.TransformFace(view, fb, fovX, fv)
				if err != nil {
					continue
				}
				_ = render.FillTurbulentPolygon(fb, tex, &cm, 0, verts, turbTimeSec)
			default:
				// World surface: per-face lightmap + perspective-correct
				// UV (tyrquake R_DrawSurface). Lightmap data lives in
				// the BSP's Lighting() lump, indexed by Face.LightOfs;
				// up to 4 lightstyle layers stacked W*H bytes each.
				lmInfo, lmErr := bsprender.FaceLightmapInfo(bm, ref.FaceIdx)
				if lmErr != nil {
					continue
				}
				lverts, err := bsprender.TransformFaceLightmapped(view, fb, fovX, fv, lmInfo)
				if err != nil {
					continue
				}
				plane := buildLightmapPlane(lmInfo, lightingLump, turbTimeSec)
				_ = render.FillPerspectiveLightmappedPolygon(fb, tex, &cm, lverts, plane, lmInfo.Width, lmInfo.Height)
			}
		}

		// Per-tic projectile trail emission.
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
				runner.ParticlePool.EmitTrail(prev, es.Origin, kind, trailNow, particleRNG)
			}
			for k := range prevEntityOrigin {
				if _, ok := seenThisTic[k]; !ok {
					delete(prevEntityOrigin, k)
				}
			}
		}

		// Alias-model pass.
		const aliasFramePeriod = float32(0.1)
		aliasShade := render.AliasShadeRange{
			Ambient:   0.3,
			DirectMin: 0.0,
			DirectMax: 0.7,
			LightDir:  [3]float32{0, 0, -1},
		}
		now := runner.Client.MsgTime
		aliasRendered := 0
		var (
			sampleES   client.EntityState
			sampleAM   *mdl.Model
			haveSample bool
		)
		for _, es := range runner.Client.Entities {
			if es.ModelIdx <= 0 || es.ModelIdx >= len(aliasModels) {
				continue
			}
			// PVS cull: skip entities whose origin falls in a leaf
			// the world walk did NOT mark visible this frame. Without
			// this check, torches / monsters / items in non-visible
			// leaves (e.g. a different floor of the labyrinth) draw
			// on top of the wall surfaces in front of them -- "flames
			// from the floor below" pathology. Mirrors tyrquake's
			// R_DrawEntitiesOnList leaf-vis test in r_main.c. synth
			// scenes (which mark every leaf visible above) trivially
			// pass.
			if !aliasEntityVisible(bm, es.Origin, stampFrame) {
				continue
			}
			am := aliasModels[es.ModelIdx]
			if am == nil {
				continue
			}
			skin := aliasSkins[es.ModelIdx]
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
					ClTime:     turbTimeSec, // mdl FrameGroup sub-frame cycler (torches, flames)
				},
				FrameIdxNext: frameIdx,
				Lerp:         lerp,
			}
			if err := render.DrawAliasInterpLit(fb, rd, &cm, aliasShade, am, skin, ent); err != nil {
				if frame%60 == 0 {
					logf("DrawAliasInterpLit modelIdx=%d from=%d to=%d lerp=%v err: %v",
						es.ModelIdx, prevIdx, frameIdx, lerp, err)
				}
				continue
			}
			if !haveSample {
				sampleES = es
				sampleAM = am
				haveSample = true
			}
			aliasRendered++
		}
		if frame%60 == 0 {
			logf("tic %d rendered %d alias entities (interp+lit)", frame, aliasRendered)
			if haveSample {
				var sampleLerp float32
				if sampleES.LerpStartTime > 0 && now > sampleES.LerpStartTime {
					sampleLerp = (now - sampleES.LerpStartTime) / aliasFramePeriod
					if sampleLerp > 1 {
						sampleLerp = 1
					}
				}
				logf("alias interp sample modelIdx=%d frames=%d prev=%d cur=%d lerpStart=%v now=%v lerp=%v",
					sampleES.ModelIdx, len(sampleAM.Frames),
					sampleES.PrevFrame, sampleES.Frame,
					sampleES.LerpStartTime, now, sampleLerp)
				fIdx := sampleES.Frame
				if fIdx < 0 || fIdx >= len(sampleAM.Frames) {
					fIdx = 0
				}
				verts := render.FramePose(sampleAM.Frames[fIdx])
				if lights, err := render.ComputeAliasVertexLights(verts, aliasShade); err == nil && len(lights) > 0 {
					lmin, lmax := lights[0], lights[0]
					seen := make(map[int]struct{}, len(lights))
					for _, v := range lights {
						if v < lmin {
							lmin = v
						}
						if v > lmax {
							lmax = v
						}
						seen[v] = struct{}{}
					}
					logf("alias shade sample modelIdx=%d verts=%d distinct=%d min=%d max=%d",
						sampleES.ModelIdx, len(lights), len(seen), lmin, lmax)
				}
			}
		}

		if err := render.DrawParticleQuads(fb, rd, runner.ParticlePool, runner.Client.MsgTime); err != nil {
			if frame%60 == 0 {
				logf("DrawParticleQuads err: %v", err)
			}
		}

		spritesDrawn := 0
		tempSpritePool.Walk(now, func(origin [3]float32, elapsed float32) {
			if explosionSprite == nil {
				return
			}
			if err := render.DrawSpriteAtTime(fb, rd, explosionSprite, origin, elapsed); err != nil {
				if frame%60 == 0 {
					logf("DrawSpriteAtTime err: %v", err)
				}
				return
			}
			spritesDrawn++
		})

		beamsDrawn := 0
		beamSegmentsDrawn := 0
		beamPool.Walk(now, func(seg client.BeamSegment) {
			var bm *mdl.Model
			var bskin *render.Pic
			switch seg.Kind {
			case protocol.TELightning1:
				bm, bskin = boltModels[0], boltSkins[0]
			case protocol.TELightning2, protocol.TEBeam:
				bm, bskin = boltModels[1], boltSkins[1]
			case protocol.TELightning3:
				bm, bskin = boltModels[2], boltSkins[2]
			default:
				return
			}
			if bm == nil {
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
					AngleRoll:  0,
					FrameIdx:   0,
					SkinIdx:    0,
				},
				FrameIdxNext: 0,
				Lerp:         0,
			}
			if err := render.DrawAliasInterpLit(fb, rd, &cm, aliasShade, bm, bskin, ent); err != nil {
				if frame%60 == 0 {
					logf("DrawAliasInterpLit(bolt kind=%d) err: %v", seg.Kind, err)
				}
				return
			}
			beamSegmentsDrawn++
			if seg.Index == 0 {
				beamsDrawn++
			}
		})
		if frame%60 == 0 {
			logf("lightning beams active=%d segments=%d", beamsDrawn, beamSegmentsDrawn)
		}

		if frame%60 == 0 {
			missiles := 0
			for _, es := range runner.Client.Entities {
				if es.ModelIdx <= 0 || es.ModelIdx >= len(aliasPrecache) {
					continue
				}
				if _, ok := trailKindForModel(aliasPrecache[es.ModelIdx]); ok {
					missiles++
				}
			}
			logf("tic %d missiles in flight: %d, explosion sprites: %d/%d (drawn/alive)",
				frame, missiles, spritesDrawn, tempSpritePool.NumAlive(now))
		}

		if sbarAssets != nil {
			if err := render.DrawSBar(fb, runner.Client, sbarAssets); err != nil {
				if frame%60 == 0 {
					logf("DrawSBar err: %v", err)
				}
			}
		}
		return nil
	}
	return nil
}

// aliasEntityVisible reports whether the entity at `origin` falls in
// a leaf the world walk marked visible this frame (Leaf.VisFrame ==
// stampFrame). tyrquake equivalent: the leaf-vis test bracketed
// inside R_DrawEntitiesOnList in r_main.c.
//
// Without this guard, every alias entity in cl.Entities (~47 on
// start.bsp -- torches, monsters, items in EVERY room) renders every
// frame, blasting through the world surfaces drawn in front of them.
// The visible failure mode is "flames from a floor below show through
// the ceiling": the torch entity sits in a non-visible leaf, the wall
// occluding it was already drawn, but the alias loop paints over it
// anyway because it has no Z buffer and no PVS check.
//
// Behaviour:
//   - bm nil               -> visible (no model to cull against; degenerate
//                            case shouldn't crash, mirrors the synth-scene
//                            fall-through above).
//   - point in solid (-1)  -> NOT visible (entity origin inside geometry
//                            is almost certainly stale state; safer to
//                            cull than to draw over walls).
//   - leaf VisFrame == stampFrame -> visible.
//   - otherwise            -> NOT visible.
//
// This is a 1-leaf-per-entity test (no bbox sweep). Real Quake uses
// the entity's full bbox over the leaf tree to handle entities
// straddling leaf boundaries; the single-point test is the cheap
// fallback that correctly culls 100% of torches (which sit at a
// single fixed origin) and 99% of moving entities (whose origin
// reasonably represents their leaf membership).
func aliasEntityVisible(bm *model.BrushModel, origin [3]float32, stampFrame int32) bool {
	if bm == nil {
		return true
	}
	leafIdx := bm.PointInLeaf(origin)
	if leafIdx < 0 || leafIdx >= bm.TotalLeaves() {
		return false
	}
	return bm.Leaf(leafIdx).VisFrame == stampFrame
}

// loadColorMapOrFallback resolves the runtime colormap. Real Quake
// renders correctly ONLY with the gfx.wad COLORMAP lump (a 64x256
// LUT whose row 0 is identity and row 63 is heavily darkened); the
// previous in-place synthesis of cm[r][s] = byte(s) made every row
// identity, which silently broke per-pixel lighting (lightmap row had
// zero effect because the LUT didn't darken). Pulled into its own
// function so the runtime contract -- "this returns a NON-IDENTITY
// colormap whenever the search path supplies one" -- is unit-testable
// in isolation, without driving the whole setupRenderer.
//
// Fallback to synthetic identity only when no SearchPath is supplied
// (e.g. synth-only test runs that don't ship a real pak) OR when the
// load fails. The fallback is loud (logged) so the surface is visibly
// flat -- the bug is then immediately obvious instead of subtle.
func loadColorMapOrFallback(searchPath *vfs.SearchPath, logf func(string, ...any)) render.ColorMap {
	var cm render.ColorMap
	if searchPath != nil {
		if loaded, err := assets.LoadColorMapFrom(searchPath); err == nil && loaded != nil {
			return *loaded
		} else if err != nil && logf != nil {
			logf("LoadColorMapFrom failed, falling back to identity (lighting will look flat): %v", err)
		}
	}
	for light := 0; light < render.ColorMapRows; light++ {
		for src := 0; src < render.ColorMapCols; src++ {
			cm[light][src] = byte(src)
		}
	}
	return cm
}

// defaultLightStyles is tyrquake's R_AnimateLight() built-in table:
// 11 standard Quake lightstyles indexed by Face.Styles[i]. Each char
// 'a'..'z' encodes a brightness 0..25; the engine samples it at 10 Hz
// from cl.time and multiplies by 22 to yield a 0..550 light scale
// (256 = ~='m', the resting level used by static lights).
//
// Style 0 is hard-set "m" (full-bright steady, NOT in this table) by
// engine convention; this slice holds 1..11.
//
// Source: tyrquake's r_light.c R_AnimateLight + the lightstyle string
// table in NQ/cl_main.c CL_ClearState / S_INIT.
var defaultLightStyles = [...]string{
	"m",                                                  // 0 - normal (steady)
	"mmnmmommommnonmmonqnmmo",                            // 1 - FLICKER (torches)
	"abcdefghijklmnopqrstuvwxyzyxwvutsrqponmlkjihgfedcba", // 2 - SLOW PULSE
	"mmmmmaaaaammmmmaaaaaabcdefgabcdefg",                 // 3 - CANDLE 1
	"mamamamamama",                                       // 4 - FAST STROBE
	"jklmnopqrstuvwxyzyxwvutsrqponmlkj",                  // 5 - GENTLE PULSE
	"nmonqnmomnmomomno",                                  // 6 - FLICKER 2
	"mmmaaaabcdefgmmmmaaaammmaamm",                       // 7 - CANDLE 2
	"mmmaaammmaaammmabcdefaaaammmmabcdefmmmaaaa",         // 8 - CANDLE 3
	"aaaaaaaazzzzzzzz",                                   // 9 - SLOW STROBE
	"mmamammmmammamamaaamammma",                          // 10 - FLUORESCENT
	"abcdefghijklmnopqrrqponmlkjihgfedcba",               // 11 - SLOW PULSE 2
}

// lightStyleBrightness returns the per-style brightness (0..550) at
// the given time, sampling the style's Anim string at 10 Hz. style 0
// is the constant full-bright. Unknown / out-of-range styles fall
// back to the static-style brightness 256 so a face with a style not
// in the default table still renders.
func lightStyleBrightness(style byte, t float32) int {
	if style == 0 {
		return 264 // 'm' = 12, * 22 = 264
	}
	if int(style) >= len(defaultLightStyles) {
		return 256
	}
	anim := defaultLightStyles[style]
	if anim == "" {
		return 256
	}
	idx := int(t*10) % len(anim)
	if idx < 0 {
		idx += len(anim)
	}
	ch := anim[idx]
	if ch < 'a' || ch > 'z' {
		return 256
	}
	return int(ch-'a') * 22
}

// buildLightmapPlane assembles the per-face W*H lightmap byte plane
// the lightmapped rasterizer samples from. tyrquake equivalent:
// R_BuildLightMap in r_surf.c.
//
// The BSP packs up to 4 lightstyle layers back-to-back at
// info.LightOfs in the Lighting() lump; each layer is W*H bytes.
// For each active layer (Styles[i] != 255) we accumulate
// `lightmap[j] * lightStyleBrightness(Styles[i], t)` into a per-pixel
// int sum, then convert that sum back into a byte (clamped at 255).
// `t` is the client time in seconds (cl.time); it drives the 10 Hz
// sampling of each style's Anim string so torches flicker and
// fluorescents pulse over time.
//
// LightOfs == -1 OR an out-of-range offset means "no static lighting"
// (sky / turbulent / faces past the lump end); we return a fully-lit
// plane (255 everywhere) so the rasterizer renders full-bright.
func buildLightmapPlane(info bsprender.LightmapInfo, lighting []byte, t float32) []byte {
	pix := info.Width * info.Height
	plane := make([]byte, pix)
	if info.LightOfs < 0 {
		for i := range plane {
			plane[i] = 255
		}
		return plane
	}
	base := int(info.LightOfs)
	if base >= len(lighting) {
		for i := range plane {
			plane[i] = 255
		}
		return plane
	}
	// Per-style accumulator: int so we can sum past 255 before
	// clamping at the end.
	accum := make([]int, pix)
	for s := 0; s < len(info.Styles); s++ {
		if info.Styles[s] == 255 {
			continue
		}
		layerOff := base + s*pix
		if layerOff+pix > len(lighting) {
			break
		}
		scale := lightStyleBrightness(info.Styles[s], t)
		for j := 0; j < pix; j++ {
			accum[j] += int(lighting[layerOff+j]) * scale >> 8
		}
	}
	// Floor at the engine "ambient" of 0 (caller can light-up via
	// colormap row 0 when all styles are absent). Clamp at 255.
	for j := 0; j < pix; j++ {
		v := accum[j]
		if v > 255 {
			v = 255
		}
		plane[j] = byte(v)
	}
	return plane
}

// observedAnyInput returns true iff the runloop has seen any movement
// key or trigger this frame.
func observedAnyInput(r *runloop.Runner) bool {
	if r == nil {
		return false
	}
	b := r.Buttons
	if b.Forward.Pressed != 0 || b.Back.Pressed != 0 ||
		b.MoveLeft.Pressed != 0 || b.MoveRight.Pressed != 0 ||
		b.Left.Pressed != 0 || b.Right.Pressed != 0 ||
		b.Up.Pressed != 0 || b.Down.Pressed != 0 ||
		b.Lookup.Pressed != 0 || b.Lookdown.Pressed != 0 ||
		b.SpeedHeld {
		return true
	}
	if r.Triggers.Attack || r.Triggers.Jump {
		return true
	}
	return false
}
