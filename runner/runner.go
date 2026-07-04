// Copyright (c) 1996-1997 Id Software, Inc.
// Copyright (c) 2026 the go-quake1/engine authors.
// SPDX-License-Identifier: BSD-3-Clause

// Package runner is the shared real-pak Quake bring-up: given a constructed
// [backend.Backend] and an [fs.FS] rooted at a real id1 pak0, [Setup] wires
// the full per-tic pipeline (host + client + loopback + sound pool + music
// + particles + sprites + beams + alias preload + sbar + menu + attract
// demo + Pre2DDraw closure) and returns a *runloop.Runner the caller can
// drive via RunUntilQuit. The shared code lives here so both the bare-metal
// tamago entry (quake-tamago) AND the wasmbox entry (cmd/quake-wasmbox) get
// the same bring-up path without each duplicating ~1500 LOC.
//
// The caller is responsible for: (1) constructing the [backend.Backend]
// (virtio-gpu / wasmbox / DOM canvas / etc.), (2) opening the pak FS from
// wherever (embedpak / OCI / disk / etc.), (3) calling RunUntilQuit on
// the returned Runner.
//
// On any setup failure, Setup returns (nil, err). The caller chooses
// whether to surface the error or fall back to a synth-only path.
package runner

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"

	"github.com/go-quake1/engine/backend"
	"github.com/go-quake1/engine/client"
	"github.com/go-quake1/engine/demo"
	"github.com/go-quake1/engine/embedmusic"
	enginehost "github.com/go-quake1/engine/host"
	"github.com/go-quake1/engine/menu"
	enginemusic "github.com/go-quake1/engine/music"
	"github.com/go-quake1/engine/protocol"
	"github.com/go-quake1/engine/render"
	"github.com/go-quake1/engine/runloop"
	engineserver "github.com/go-quake1/engine/server"
	"github.com/go-quake1/engine/vfs"
)

// hudVirtualWidth + hudVirtualHeight are the canonical "virtual"
// 2D-layout dims the engine's HUD / menu / console / centerprint were
// designed against (vanilla DOS Quake 320x240). HUDScaleFor folds an
// arbitrary physical (FBWidth, FBHeight) into the largest integer
// upscale that keeps the 2D layer inside the virtual envelope.
const (
	hudVirtualWidth  = 320
	hudVirtualHeight = 240
)

// HUDScaleFor returns the integer HUDScale (>=1) the 2D layer should
// render at for a framebuffer of (fbWidth, fbHeight) physical pixels.
//
// Rule: pick the largest integer s such that s * hudVirtualWidth <=
// fbWidth AND s * hudVirtualHeight <= fbHeight. Examples:
//
//	320x240   -> 1 (vanilla)
//	640x480   -> 2 (canonical "high-res" tyrquake mode)
//	900x700   -> 2 (900/320=2.8 floored to 2; 700/240=2.9 floored to 2)
//	1280x960  -> 4
//	1920x1080 -> 4 (1920/320=6 but 1080/240=4.5 floored to 4)
//
// Non-positive inputs collapse to 1 so a degenerate framebuffer cannot
// trigger a zero-scale upscale (which would divide by zero in the
// virtual-coordinate math).
func HUDScaleFor(fbWidth, fbHeight int) int {
	if fbWidth <= 0 || fbHeight <= 0 {
		return 1
	}
	sx := fbWidth / hudVirtualWidth
	sy := fbHeight / hudVirtualHeight
	s := sx
	if sy < s {
		s = sy
	}
	if s < 1 {
		return 1
	}
	return s
}

// DemoYawPeriodFrames is the frame count over which the demo-orbit Yaw
// winds from 0 to 360. One degree per frame at 60 Hz gives a 6-second
// panorama -- fast enough for headless capture runs to catch many angles,
// slow enough that any single screendump catches a clean instantaneous
// shot.
const DemoYawPeriodFrames = 360

// DemoWaypointPeriodSeconds is how long (in sv.time seconds) each
// demo-orbit waypoint holds before the next one takes over.
const DemoWaypointPeriodSeconds = 2.0

// SetupOpts bundles the per-call wiring parameters for [Setup].
// Backend + PakFS + FBWidth + FBHeight are required; the rest carry
// sensible defaults.
type SetupOpts struct {
	// Backend is the framebuffer / input / audio sink the runloop
	// drives. Required.
	Backend backend.Backend

	// PakFS is the pak-rooted [fs.FS] real assets come from. Required.
	PakFS fs.FS

	// FBWidth / FBHeight are the framebuffer dimensions. Required
	// (non-zero positive values).
	FBWidth  int
	FBHeight int

	// MapSlug is the bare map name SpawnServer loads. Default "start".
	MapSlug string

	// Logf is the engine's bring-up log sink. nil = fmt.Printf with a
	// "QUAKE: " prefix.
	Logf func(format string, args ...any)

	// ConCharsLump is an optional 16384-byte 128x128 conchars sheet.
	// When non-nil it overrides the synthetic (all-zero) glyph sheet
	// in the asset VFS fallback. Tamago passes the concharsfont.Build
	// result so menu/console/centerprint text is readable.
	ConCharsLump []byte

	// DemoOrbit enables the programmatic camera walk for headless
	// validation. Default false.
	DemoOrbit bool

	// DemoOrbitAutoDisableOnInput, when true, flips DemoOrbit to false
	// on the first observed user input event. Default false.
	DemoOrbitAutoDisableOnInput bool

	// DisableAttractDemo skips wiring the attract-loop demo (demo1.dem).
	// The recorded vanilla demos carry svc_* opcodes this SvcReader does
	// not yet decode; playing them under SkipUnknownSvc desyncs the reader
	// (a stray byte surfaces as `unknown TE_* kind`), which on the wasmbox
	// client manifests as the world flashing in + out at the menu as the
	// demo repeatedly desyncs, drops, and re-arms. Set true until the
	// demo-stream opcode coverage is complete. Default false.
	DisableAttractDemo bool

	// MusicOverlayFS is an optional secondary [fs.FS] the music loader
	// probes BEFORE the embedded fallback. Useful when PakFS lives
	// inside a .pak archive (which doesn't carry music) but the music
	// tracks are available alongside in a separate overlay (e.g. the
	// OCI image's music/track*.ogg layers). The probe path is the
	// canonical "music/trackNN.ogg" form (PathPrefix+%02d+PathSuffix).
	// nil = no overlay; the existing PakFS-then-embedmusic chain is
	// preserved verbatim.
	MusicOverlayFS fs.FS
}

// runtimeState carries the per-call mutable knobs the Pre2DDraw closure
// needs.
type runtimeState struct {
	demoOrbit                   bool
	demoOrbitAutoDisableOnInput bool
}

// Setup wires the full real-pak Quake bring-up onto runner-side state.
// Returns a configured *runloop.Runner ready for RunUntilQuit().
//
// The function is tolerant: a partial pak (missing music, missing alias
// models, missing sbar pics) degrades gracefully -- only progs.dat +
// maps/<slug>.bsp matter for the real-host path, and even those have a
// synthbsp fallback via loadBSP.
func Setup(opts SetupOpts) (*runloop.Runner, error) {
	if opts.Backend == nil {
		return nil, errors.New("runner.Setup: nil Backend")
	}
	if opts.PakFS == nil {
		return nil, errors.New("runner.Setup: nil PakFS")
	}
	if opts.FBWidth <= 0 || opts.FBHeight <= 0 {
		return nil, fmt.Errorf("runner.Setup: bad framebuffer size %dx%d",
			opts.FBWidth, opts.FBHeight)
	}
	logf := opts.Logf
	if logf == nil {
		logf = func(format string, args ...any) {
			fmt.Printf("QUAKE: "+format+"\n", args...)
		}
	}
	mapSlug := opts.MapSlug
	if mapSlug == "" {
		mapSlug = "start"
	}

	state := &runtimeState{
		demoOrbit:                   opts.DemoOrbit,
		demoOrbitAutoDisableOnInput: opts.DemoOrbitAutoDisableOnInput,
	}

	pakFS := opts.PakFS

	// 6. Build the asset VFS as an ordered overlay.
	v := vfs.New()
	syn := syntheticAssets(opts.ConCharsLump)
	v.Add(syn)
	v.Add(newWADOverlay(pakFS, "gfx.wad"))
	reportLumpSources(v, pakFS, syn, []string{
		"gfx/palette.lmp",
		"gfx/colormap.lmp",
		"gfx/conchars.lmp",
	}, logf)

	// 7. Build the real Host.
	realHost, herr := buildHost(pakFS, mapSlug, logf)
	if herr != nil {
		return nil, fmt.Errorf("buildHost: %w", herr)
	}
	logf("real host live -- sv.active=%v numEdicts=%d maxEdicts=%d",
		realHost.Server.Active, realHost.Server.NumEdicts, realHost.Server.MaxEdicts)
	logf("spawn census -- %d edicts populated past the world+client reserve (MaxClients=%d)",
		realHost.Server.NumEdicts-realHost.Static.MaxClients-1, realHost.Static.MaxClients)
	total, mdlCount := 0, 0
	for _, n := range realHost.Server.ModelPrecache {
		if n == "" {
			break
		}
		total++
		if len(n) >= 4 && n[len(n)-4:] == ".mdl" {
			mdlCount++
		}
	}
	logf("ModelPrecache populated -- %d entries (%d .mdl alias models)",
		total, mdlCount)

	// 8. Loopback NetConn pair via the host.
	conn, slotIdx, cerr := realHost.ConnectLoopback()
	if cerr != nil {
		return nil, fmt.Errorf("ConnectLoopback: %w", cerr)
	}
	cli := conn
	playerSlot := slotIdx + 1
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
	blStat, err := realHost.Server.SendBaselines(hc, realHost.Progs(), realHost.Static.MaxClients)
	if err != nil {
		return nil, fmt.Errorf("SendBaselines: %w", err)
	}
	logf("svc_spawnbaseline broadcast -- emitted=%d skipped_free=%d skipped_nomodel=%d (out of %d edicts; total queued bytes=%d)",
		blStat.Emitted, blStat.SkippedFree, blStat.SkippedNoModel,
		realHost.Server.NumEdicts, hc.Message.Len())
	hc.Spawned = true
	logf("loopback bound -- client slot %d -> edict %d (active+Spawned, wire 'spawn' will re-flip idempotently); wire signon prefix queued (%d bytes)",
		slotIdx, playerSlot, hc.Message.Len())

	// 9. Client state.
	clientState := client.NewState()
	clientState.PlayerNum = playerSlot
	logf("client state initialised StateDisconnected -- wire signon drives the lifecycle (PlayerNum=%d)",
		playerSlot)

	// 9b. Changelevel wrapper.
	var hostFramer runloop.HostFramer = &changelevelHostFramer{host: realHost, logf: logf}

	// 10. Runner construction. HUDScale picks an integer upscale
	// for the 2D layer (HUD, menu, console, centerprint) so on a
	// 900x700 fb the bottom bar reads at the same apparent size as
	// the canonical 320x240 surface. The 3D world pass continues
	// to render at native fb.Width x fb.Height.
	runner, err := runloop.NewRunnerFromVFS(runloop.SetupOpts{
		VFS:            v,
		Host:           hostFramer,
		Client:         clientState,
		Conn:           cli,
		Backend:        opts.Backend,
		BackgroundIdx:  0x20,
		NotifyLifetime: 3,
		MaxNotifyRows:  4,
		SoundChannels:  8,
		HUDScale:       HUDScaleFor(opts.FBWidth, opts.FBHeight),
	})
	if err != nil {
		return nil, fmt.Errorf("NewRunnerFromVFS: %w", err)
	}

	// 11. Console.
	runner.Console.Print("PURE-GO QUAKE 1 -- shared bring-up via runner.Setup\n")
	runner.Console.Print("runloop wired: input -> client.Tick -> host.Frame -> Pre2DDraw\n")

	// 11b. Seed the sound pool.
	if runner.SoundPool != nil {
		seeded := seedSoundPool(runner.SoundPool, pakFS, []string{
			"sound/ambience/water1.wav",
			"sound/nav_editor/changed_edict.wav",
		}, logf)
		logf("sound pool seeded -- %d sample(s) playing on reserved-static slots", seeded)
	}

	// 11c. Unify the mixer pool.
	if realHost.SoundPool() != nil {
		runner.SoundPool = realHost.SoundPool()
		precached := 0
		for i := 1; i < len(realHost.Server.SoundPrecache); i++ {
			if realHost.Server.SoundPrecache[i] != "" {
				precached++
			}
		}
		mixerSamples := 0
		for _, s := range realHost.Sounds {
			if s != nil {
				mixerSamples++
			}
		}
		active := realHost.SoundPool().ActiveCount()
		logf("mixer pool unified host<->runner -- %d sounds precached (wire), %d samples loaded (mixer), %d channels already active (ambient tracks parked at spawn time)",
			precached, mixerSamples, active)
	}

	// 11c-bis. Music driver wiring. Probe order: PakFS first (the user
	// can override embedded tracks by shipping music/trackXX.ogg INSIDE
	// pak0), then MusicOverlayFS if any (the OCI-streaming wasmbox build
	// ships music as separate /v2/blobs/* layers, NOT inside pak0), then
	// embedmusic as the static fallback.
	musicOverlay := opts.MusicOverlayFS
	runner.MusicOpen = func(track int) ([]byte, bool) {
		if track <= 0 {
			return nil, false
		}
		path := fmt.Sprintf("%s%02d%s", enginemusic.PathPrefix, track, enginemusic.PathSuffix)
		if pakFS != nil {
			if blob, ok := tryReadPakFile(pakFS, path); ok && len(blob) > 64 {
				return blob, true
			}
		}
		if musicOverlay != nil {
			if blob, ok := tryReadPakFile(musicOverlay, path); ok && len(blob) > 64 {
				return blob, true
			}
		}
		blob, err := embedmusic.MusicFS.ReadFile(fmt.Sprintf("track%02d.ogg", track))
		if err != nil || len(blob) < 64 {
			return nil, false
		}
		return blob, true
	}
	runner.MusicDecoder = enginemusic.NewVorbisDecoder
	runner.MusicVolume = enginemusic.DefaultVolume
	runner.MusicMissingLog = func(track int) {
		logf("music track%02d.ogg missing -- silent", track)
	}

	// 11d. Particle pool.
	particlePool := render.NewPool()
	particleRNG := newLCGByteSource(0xC0FFEF)
	runner.ParticlePool = particlePool
	runner.ParticleGravity = 800

	if realHost.VM != nil {
		realHost.VM.SetParticleSink(func(origin, dir [3]float32, color, count int) {
			particlePool.Emit(origin, dir, byte(color), count, float32(realHost.Server.Time), particleRNG)
		})
	}

	clientState.EmitParticles = func(origin, dir [3]float32, color, count int) {
		now := clientState.MsgTime
		particlePool.Emit(origin, dir, byte(color), count, now, particleRNG)
	}

	// Sprites + beams.
	explosionSprite, explosionPath := loadExplosionSprite(pakFS, logf)
	spritesLoaded := 0
	if explosionSprite != nil {
		spritesLoaded = 1
	}
	logf("sprites loaded %d (s_explod path=%q)", spritesLoaded, explosionPath)

	tempSpritePool := client.NewTempSpritePool()

	boltModels, boltSkins, boltLoaded := loadBoltModels(pakFS, logf)
	logf("bolt models loaded %d/3 (TE_LIGHTNING1/2/3 + TE_BEAM)", boltLoaded)

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
	logf("particle pool wired -- cap=%d gravity=%v (QC #48 + svc_particle + svc_temp_entity)",
		render.MaxParticles, runner.ParticleGravity)

	clientState.EmitBeam = func(kind, ent int, start, end [3]float32) {
		beamPool.Spawn(kind, ent, start, end, clientState.MsgTime)
	}
	logf("beam pool wired -- cap=%d lifetime=%vs segment=%v (TE_LIGHTNING1/2/3 + TE_BEAM)",
		client.MaxBeams, client.BeamLifetime, client.BeamSegmentLength)

	// 12. Alias preload.
	aliasPrecache := realHost.Server.ModelPrecache
	aliasModels, aliasSkins, aliasLoaded, aliasNames := loadAliasModels(pakFS, aliasPrecache, logf)
	logf("alias models loaded: %d / %d names", aliasLoaded, aliasNames)

	// 12b. SBar.
	sbarAssets, sbarLoaded, sbarTotal, sbarMissing := loadSBarAssets(pakFS, logf)
	sample := sbarMissing
	if len(sample) > 8 {
		sample = sample[:8]
	}
	logf("sbar -- loaded %d/%d assets, missing: %v", sbarLoaded, sbarTotal, sample)

	if err := setupRenderer(setupRendererOpts{
		runner:          runner,
		pakFS:           pakFS,
		searchPath:      v,
		realHost:        realHost,
		playerSlot:      playerSlot,
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
		state:           state,
		logf:            logf,
		mapFile:         renderMapFile(opts.MapSlug),
	}); err != nil {
		return nil, fmt.Errorf("setupRenderer: %w", err)
	}

	// 12c. Menu.
	menuAssets, menuLoaded, menuTotal := loadMenuAssets(pakFS, logf)
	logf("menu -- loaded %d/%d assets, dots=%d",
		menuLoaded, menuTotal, len(menuAssets.MenuDots))
	runner.Menu = menu.New()
	runner.MenuAssets = menuAssets
	logf("menu state=%s cursor=%d (boot lands on the main menu, world pass frozen until player picks Skill)",
		runner.Menu.State, runner.Menu.CursorIndex)

	// 12d. Attract demo (opt-out: the demo stream desyncs this SvcReader).
	if opts.DisableAttractDemo {
		logf("attract-loop demo disabled (DisableAttractDemo)")
	} else if d := loadAttractDemo(pakFS, "demo1.dem"); d != nil {
		d.PlayerOpts = demo.PlayerOpts{
			Protocol:       protocol.VersionNQ,
			TickDelta:      1.0 / 20.0,
			SkipUnknownSvc: true,
		}
		d.OnFrame = func(frame int, angles [3]float32) {
			if frame%60 == 0 {
				logf("demo playback frame %d viewAngles=%v", frame, angles)
			}
		}
		runner.Demo = d
		logf("attract-loop demo wired -- source=%q", "demo1.dem")
	} else {
		logf("attract-loop demo skipped -- demo1.dem not in pak")
	}

	logf("entering RunUntilQuit (realHost=%v)", realHost != nil)
	return runner, nil
}

// changelevelHostFramer wraps an [enginehost.Host] so the per-tic
// [runloop.Runner.RunFrame] step also polls
// [enginehost.Host.ConsumeChangelevel] post-Frame.
type changelevelHostFramer struct {
	host *enginehost.Host
	logf func(format string, args ...any)
}

// Frame runs one host tic, then polls + acts on any pending changelevel.
func (c *changelevelHostFramer) Frame(dt float32) error {
	if err := c.host.Frame(dt); err != nil {
		return err
	}
	if pending, mapSlug := c.host.ConsumeChangelevel(); pending {
		c.logf("level change requested -- nextmap=%q", mapSlug)
		c.host.HarvestIntermissionStats()
		if err := c.host.EmitIntermission(); err != nil {
			c.logf("EmitIntermission failed: %v", err)
		} else {
			s := c.host.LastIntermissionStats
			c.logf("intermission active=1 (secrets=%d/%d monsters=%d/%d)",
				s.FoundSecrets, s.TotalSecrets, s.KilledMonsters, s.TotalMonsters)
		}
		if err := c.host.SpawnServer(mapSlug, c.host.Server.Protocol); err != nil {
			c.logf("changelevel SpawnServer(%q) failed: %v -- staying on previous map",
				mapSlug, err)
		} else {
			c.logf("changelevel SpawnServer(%q) ok -- now on new map", mapSlug)
		}
	}
	return nil
}

// loadAttractDemo opens name inside pakFS + builds a [runloop.Demo] with
// a Restart closure that re-opens the same entry on io.EOF.
func loadAttractDemo(pakFS fs.FS, name string) *runloop.Demo {
	open := func() (*demo.Reader, error) {
		data, ok := tryReadPakFile(pakFS, name)
		if !ok {
			return nil, fmt.Errorf("attract demo %q not found in pak", name)
		}
		return demo.NewReader(bytes.NewReader(data))
	}
	rd, err := open()
	if err != nil {
		return nil
	}
	return &runloop.Demo{
		Reader:  rd,
		Restart: open,
	}
}
