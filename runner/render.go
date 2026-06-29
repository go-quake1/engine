// Copyright (c) 1996-1997 Id Software, Inc.
// Copyright (c) 2026 the go-quake1/engine authors.
// SPDX-License-Identifier: BSD-3-Clause

package runner

import (
	"bytes"
	"fmt"
	"io/fs"
	"strings"

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
)

// setupRendererOpts bundles the (many) parameters [setupRenderer] consumes.
type setupRendererOpts struct {
	runner          *runloop.Runner
	pakFS           fs.FS
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
		for i := 0; i < surfaces.Len(); i++ {
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
				// Perspective-correct UV: world surfaces sampled with
				// 1/z interpolation across 8-pixel sub-spans (tyrquake
				// R_DrawSurface). The affine path made large polygons
				// (floors, big walls) "swim" as the camera moved.
				pverts, err := bsprender.TransformFacePerspective(view, fb, fovX, fv)
				if err != nil {
					continue
				}
				_ = render.FillPerspectiveTexturedPolygon(fb, tex, &cm, 0, pverts)
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
