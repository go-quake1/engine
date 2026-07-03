// Copyright (c) 1996-1997 Id Software, Inc.
// Copyright (c) 2026 the go-quake1/engine authors.
// SPDX-License-Identifier: GPL-2.0-or-later

package runloop

import (
	"errors"

	"github.com/go-quake1/engine/backend"
	"github.com/go-quake1/engine/client"
	"github.com/go-quake1/engine/menu"
	"github.com/go-quake1/engine/music"
	"github.com/go-quake1/engine/protocol"
	"github.com/go-quake1/engine/render"
	"github.com/go-quake1/engine/server"
	"github.com/go-quake1/engine/sound"
)

// HostFramer is the minimal contract [Runner] needs from the host
// package: advance the server-side simulation by one tic.
//
// tyrquake: the SV_Frame call inside Host_Frame.
//
// Defined here as a one-method interface (not the full *host.Host
// struct) so tests can stub the per-tic without spinning up a VM /
// World / Progs / Cache stack. The production *host.Host has a
// matching Frame(dt float32) error method and satisfies this
// interface without any wrapper.
type HostFramer interface {
	Frame(dt float32) error
}

// Runner owns one game session's per-frame orchestration. Created once
// at startup with all the long-lived pieces; [Runner.RunFrame] is
// called each tick by the platform's main loop.
//
// tyrquake: the role of Host_Frame in host.c -- collects input,
// advances server + client state, renders the frame, mixes audio,
// hands the output to the backend.
//
// All fields are caller-owned. Runner does not allocate any of them
// (the working buffers RGBA + MixBuffer are pre-sized at startup so
// per-frame allocations stay at zero -- this matches the project's
// bare-metal / TamaGo / GC-pause-free constraint).
type Runner struct {
	Host    HostFramer
	Client  *client.State
	Conn    server.NetConn
	Backend backend.Backend

	// Renderer state (long-lived; reused each frame).
	FrameBuffer *render.FrameBuffer
	Console     *render.Console
	Screen      *render.Screen
	Chars       *render.Pic
	Palette     *render.Palette

	// Audio state.
	SoundPool *sound.Pool

	// Particle pool. Optional (nil = no per-tic particle advance,
	// matches the historical bring-up behaviour from the renderer
	// pre-this-batch). When non-nil [RunFrame] calls Pool.Run between
	// the client tick and the Pre2DDraw hook so the closure can hand
	// the already-advanced pool to DrawParticles/DrawParticleQuads.
	ParticlePool *render.Pool
	// ParticleGravity is the world-gravity scalar fed into
	// Pool.Run -- typically the server's sv_gravity cvar (default
	// 800 in Q1). Zero = no gravity force on ParticleGrav/Slow types.
	ParticleGravity float32

	// Per-frame input bundles (advanced by the input event handler).
	Buttons    client.MovementButtons
	Speeds     client.InputSpeeds
	ViewAngles [3]float32

	// SimStep is the fixed server-tick interval (seconds) the sim advances
	// per host tick, decoupled from the render framerate. Zero selects
	// [DefaultSimStep] (0.05 = 20Hz, matching host.DefaultFrameTime).
	SimStep float32

	// simAccumulator carries the not-yet-simulated wall-clock time (in
	// seconds) between RunFrame calls so the server steps at the FIXED
	// SimStep rate regardless of the render framerate. Without this a slow
	// render frame (the wasm software rasterizer can dip below the physics
	// rate) would run one physics tic with a huge dt, making gravity /
	// jumps / collision framerate-dependent and unstable.
	simAccumulator float32

	// ConsoleOpen tracks whether the developer console drop-down is
	// currently armed (true) or closed (false). Toggled on the down-
	// edge of [backend.KeyTilde] by [RunFrame]. The per-frame
	// AnimateConsole call lerps Screen.ConCurrent toward
	// Screen.ConLines when open / 0 when closed at ScrollSpeed
	// pixels per tick. tyrquake: the boolean Con_ToggleConsole_f
	// flips, surfaced through key_dest = key_console / key_game.
	ConsoleOpen bool

	// Triggers tracks the held state of the on-wire trigger keys
	// (mouse-fire = +attack, Enter = +jump). Translated to the
	// [server.UserCmd.Buttons] bitmask in RunFrame and handed to
	// [client.Tick] as TickInput.ActionButtons. Movement keys
	// (W/A/S/D + arrows + shift) live in Buttons above and do NOT
	// feed this field -- the on-wire `buttons` byte only carries the
	// trigger bits the QC progs read via self.button*.
	Triggers TriggerButtons

	// ViewOrigin is a legacy caller-owned anchor retained for
	// backwards compatibility. RunFrame no longer sources the
	// per-tic camera position from this field -- the viewOrigin
	// handed to [Runner.Pre2DDraw] now comes from the wire-mirrored
	// client.State.Entities[Client.PlayerNum].Origin (the proper
	// client/server split: the renderer reads what the server told
	// the client, not the server edicts directly). When the player
	// entity has not yet been received the fallback is the zero
	// vector. Callers may still set ViewOrigin for diagnostics /
	// out-of-band camera overrides, but the runloop ignores it.
	ViewOrigin [3]float32

	// Working buffers (long-lived; reused per frame).
	RGBA      []byte               // size = fb.Width * fb.Height * 4
	MixBuffer []sound.StereoSample // size = sound.MixBufferStereoFrames

	// Compose configuration.
	BackgroundIdx  byte    // palette fill for Compose2D
	NotifyLifetime float32 // seconds a notify row stays visible
	MaxNotifyRows  int     // upper bound on the notify overlay row count

	// Pre2DDraw is an optional hook the runner invokes between the
	// client tick and the 2D Compose. The closure owns the 3D
	// rasterization (BSP walk, surface emission, FillTexturedPolygon
	// per face); on return fb holds the rendered scene, which
	// Compose2D then overlays its 2D layers on top of.
	//
	// Signature: (fb, viewOrigin, viewAngles) -> error. viewOrigin
	// is the (x, y, z) world-space camera position; viewAngles is
	// (pitch, yaw, roll) in DEGREES, matching render.RefDef.
	//
	// When non-nil the runner also asks Compose2D to skip its
	// background clear (FrameContext.SkipBackgroundFill = true) so
	// the pre-drawn scene isn't overwritten. When nil the previous
	// 2D-only behaviour is preserved verbatim.
	//
	// Errors propagate from RunFrame verbatim (the present /
	// audio steps are skipped for that tic).
	Pre2DDraw func(fb *render.FrameBuffer, viewOrigin [3]float32, viewAngles [3]float32) error

	// Menu is the optional [menu.Menu] state machine the runloop
	// drives ahead of the world pass. When Menu != nil AND
	// Menu.Active() returns true the runloop:
	//
	//   - routes per-frame KeysDown events into Menu.Handle BEFORE
	//     they reach the movement/trigger button mappers, so a key
	//     consumed by the menu does not also drive the player edict;
	//   - SKIPS the Host.Frame tic (the game world stays paused) +
	//     the Pre2DDraw closure (no 3D BSP walk);
	//   - calls Menu.Draw into fb in the Pre2DDraw slot so the menu
	//     overlay is the only scene composed on top of the 2D layer.
	//
	// Esc-while-not-in-menu opens the menu (Menu.Open). The runloop
	// then sets the frozen-world flag on the NEXT tic so the in-game
	// pause is single-frame sharp.
	//
	// nil = previous behaviour (no menu; world pass + input run
	// unconditionally).
	Menu *menu.Menu

	// MenuAssets is the WAD-pic bundle Menu.Draw paints with. nil
	// falls back to the text-only path inside [menu.Menu.Draw].
	MenuAssets *menu.Assets

	// Demo is the optional attract-loop demo-playback state. When
	// non-nil (and the menu is at the title screen) [Runner.RunFrame]
	// substitutes a [demo.PlayTick] call for the live host.Frame so
	// the recorded stream drives the client state per-tic. Any
	// KeyDown event halts the demo (vanilla "any key drops you out
	// of the attract loop"); on io.EOF the optional Restart closure
	// re-opens the source for the next loop. See [Demo] for the
	// full lifecycle.
	//
	// nil = no demo. The runner falls back to the normal host.Frame
	// + client.Tick path verbatim.
	Demo *Demo

	// MusicOpen is the pak-agnostic asset resolver the music driver
	// consults whenever the wire-received [client.State.MusicTrack]
	// changes. The closure should return (blob, true) when
	// "music/track%02d.ogg" exists in the asset source, (nil, false)
	// otherwise.
	//
	// nil disables the music driver entirely; the audio pipeline
	// still runs SFX through [sound.Paint] but no music is decoded.
	// This keeps the runloop usable on builds whose pak omits the
	// .ogg files (the typical shareware bring-up state).
	MusicOpen music.OpenFunc

	// MusicDecoder is the [music.DecoderFactory] the driver uses to
	// parse the OGG/Vorbis blob into a streaming [music.Decoder].
	// Production callers pass [music.NewVorbisDecoder]; tests pass
	// a fake. nil + a non-nil MusicOpen surfaces as a degraded
	// "skip the music driver entirely" path (same as nil MusicOpen);
	// the runloop logs nothing in either case (the embedder owns
	// the "music missing" log via the resolver-side closure if it
	// wants one).
	MusicDecoder music.DecoderFactory

	// MusicVolume is the per-stream mix scale the driver passes to
	// [music.LoadTrack]. Zero defaults to [music.DefaultVolume].
	MusicVolume float32

	// MusicMissingLog is the optional "music track XX missing" sink
	// the driver fires ONCE per (MusicTrack, MusicLoopTrack) pair
	// whose resolver returned (nil, false). Mirrors the
	// vanilla-engine "QUAKE: missing music track XX -- silent"
	// printf so QEMU-serial-stream-driven bring-up surfaces the
	// missing-music state without leaking noise on every signon.
	// nil = no log.
	MusicMissingLog func(track int)

	// musicStreamer is the active [music.Streamer], (re-)opened
	// whenever the wire-broadcast (MusicTrack, MusicLoopTrack) pair
	// changes. nil = no track is playing (either MusicTrack == 0 OR
	// the resolver returned "track missing" + the driver degraded
	// to silence for this signon).
	musicStreamer *music.Streamer

	// musicEpochSeen is the last [client.State.MusicEpoch] value the
	// driver acted on. The per-tic music dispatch ignores any
	// epoch < musicEpochSeen + 1, so a no-change tic is allocation-
	// free.
	musicEpochSeen uint64

	// musicMissingLogged tracks (Track, LoopTrack) pairs the driver
	// has already logged as missing so the log fires once per unique
	// pair rather than every signon. Bounded by the wire byte
	// range, so the worst case is 256*256 entries -- in practice the
	// game uses < 16 distinct pairs across a full playthrough.
	musicMissingLogged map[uint16]struct{}
}

// Sentinel errors returned by [Runner.RunFrame] before any work runs.
var (
	ErrRunnerNilHost    = errors.New("runloop: nil Host")
	ErrRunnerNilClient  = errors.New("runloop: nil Client State")
	ErrRunnerNilConn    = errors.New("runloop: nil NetConn")
	ErrRunnerNilBackend = errors.New("runloop: nil Backend")
	ErrRunnerNilFB      = errors.New("runloop: nil FrameBuffer")
	ErrRunnerRGBASize   = errors.New("runloop: RGBA buffer too small for framebuffer")
)

// RunFrame runs one full game tic. Sequence:
//
//  1. snap := Backend.PollInput()       (collect events)
//  2. apply snap.KeysDown / KeysUp to r.Buttons via
//     [UpdateButtonsFromSnapshot] (the snapshot's mouse deltas are
//     consumed by client.Tick via the TickInput bundle below)
//  3. host.Frame(dt)                    (server-side tick)
//  4. client.Tick(...)                  (client-side: drain inbound,
//     send clc_move; updates r.ViewAngles)
//  5. r.Pre2DDraw(fb, viewOrigin,       (optional 3D pass; skipped
//     viewAngles)                        when nil)
//  6. render.Compose2D(fb, ...)         (2D frame -- console + notify;
//     SkipBackgroundFill when Pre2DDraw is set so the 3D pixels survive)
//  7. fb.Expand(r.RGBA, palette)        (palette -> RGBA)
//  8. Backend.PresentFrame(r.RGBA, ...) (display)
//  9. sound.Paint(pool, r.MixBuffer, n) (mix audio)
//  10. Backend.QueueAudio(r.MixBuffer[:n])
//
// dt is the frame delta in seconds (from Backend.Now() differences;
// caller passes the result). nowSec is the wall-clock-like time the
// notify overlay + client.Tick stamp messages with.
//
// SHORT-CIRCUITS:
//   - client.Tick ALWAYS runs (no Connection-based skip): the wire-
//     driven signon handshake (server.SendSignonHandshake -> client's
//     applySignonNum stage 1) needs the inbound drain to fire even
//     when state.Connection == StateDisconnected; without it the
//     stage-1 byte that transitions the client into StateConnecting
//     would never be read. Tick's OWN guard short-circuits the
//     OUTBOUND clc_move build pre-StateConnected, so a pre-signon
//     Tick is a pure inbound-drain (no spurious clc_move).
//   - if r.SoundPool == nil or len(r.MixBuffer) == 0, the audio steps
//     are SKIPPED (a video-only backend works fine without audio)
//   - if Backend's QueueAudio returns [backend.ErrUnsupported], that
//     specific error is silently ignored (the engine doesn't need to
//     know the backend lacks audio)
//
// All other backend errors propagate verbatim. On error the remaining
// steps are skipped (so a backend PresentFrame failure doesn't
// prevent the host from advancing the server simulation next tick).
// DefaultSimStep is the fixed server-tick interval used when Runner.SimStep
// is unset (0.05 = 20Hz, matching host.DefaultFrameTime -- the NQ server rate).
const DefaultSimStep float32 = 0.05

// maxSimSubsteps caps how many fixed server ticks a single RunFrame will
// run to catch up the accumulated wall-clock time. It bounds the per-frame
// work after a stall so the loop can't spiral (render slower -> more catch-up
// ticks -> render slower). At the default 0.05s SimStep this is a 0.4s
// backlog ceiling; time beyond it is dropped (brief slow-motion, never a hang).
const maxSimSubsteps = 8

func (r *Runner) RunFrame(dt float32, nowSec float32) error {
	if r.Host == nil {
		return ErrRunnerNilHost
	}
	if r.Client == nil {
		return ErrRunnerNilClient
	}
	if r.Conn == nil {
		return ErrRunnerNilConn
	}
	if r.Backend == nil {
		return ErrRunnerNilBackend
	}
	if r.FrameBuffer == nil {
		return ErrRunnerNilFB
	}
	if len(r.RGBA) < r.FrameBuffer.Width*r.FrameBuffer.Height*4 {
		return ErrRunnerRGBASize
	}

	// 1) Collect input.
	snap, err := r.Backend.PollInput()
	if err != nil {
		return err
	}

	// 1a) Attract-loop demo interrupt. Any KeyDown event on the
	//     incoming snapshot halts an in-flight demo playback (the
	//     vanilla "any key drops you out of the attract loop"
	//     behaviour). The check fires BEFORE the menu dispatch so
	//     the same Esc that opens the menu also stops the demo on
	//     the same tic; the menu_state machine then sees an empty
	//     demo slot + behaves normally.
	r.interruptDemoOnInput(snap)

	// 1b) Menu state machine. When the menu is up, every KeyDown
	//     event is routed into Menu.Handle and CONSUMED before the
	//     movement / trigger mappers run, so a key the menu took
	//     does not also drive the player edict (the C upstream
	//     uses key_dest to multiplex; the Go port hardcodes the
	//     menu-first split because the menu is the only mode
	//     above the game today). Esc-pressed-while-not-in-menu
	//     opens the menu; Esc-pressed-in-menu is dispatched into
	//     Menu.Handle by the same loop and pops the screen back.
	//
	//     menuConsumed tracks whether the menu intercepted any
	//     event this tic; downstream the runloop uses it to decide
	//     whether to skip the host tick + the world pass.
	menuConsumed := r.dispatchMenuInput(snap)

	// 2) Translate the raw key events into the persistent button
	//    state. Skipped when the menu consumed the inputs so a
	//    held movement key from BEFORE the menu opened doesn't keep
	//    driving the player. (The button slots already in
	//    r.Buttons stay held until the user releases, matching
	//    upstream behaviour.)
	if !menuConsumed {
		UpdateButtonsFromSnapshot(&r.Buttons, snap)
		UpdateTriggersFromSnapshot(&r.Triggers, snap)
	}

	// 2b) Console toggle: down-edge of KeyTilde flips r.ConsoleOpen.
	//     Up-edges are intentionally ignored (matches tyrquake's
	//     Con_ToggleConsole_f, which is bound to the press half only;
	//     the release half is a no-op so a held key doesn't oscillate).
	for _, k := range snap.KeysDown {
		if k == backend.KeyTilde {
			r.ConsoleOpen = !r.ConsoleOpen
		}
	}

	// 2c) Animate the console drop-down toward its target each tic.
	//     Open target = Screen.ConLines; closed target = 0. Screen +
	//     ConsoleOpen wiring is optional (tests that omit Screen rely
	//     on the nil guard above; the per-tic animation is skipped
	//     when Screen is nil because the renderer code path doesn't
	//     run either).
	if r.Screen != nil {
		target := 0
		if r.ConsoleOpen {
			target = r.Screen.ConLines
		}
		r.Screen.AnimateConsole(target)
	}

	// 3) Advance server simulation. Skipped in two cases:
	//
	//    a) the menu is up (the C upstream sets cl.paused / sv.paused
	//       when the menu is open; the Go port pauses by short-
	//       circuiting the host tic entirely, which keeps sv.time +
	//       all per-tic QC progressions frozen until the player
	//       dismisses the menu);
	//    b) a demo is playing (the attract-loop path: the demo body
	//       IS the server's per-tic broadcast snapshot, so running
	//       the live server on top would race against the recorded
	//       stream + corrupt the playback).
	//
	//    The demo path swaps host.Frame for a [demo.PlayTick] call
	//    that decodes one recorded tic into the client state +
	//    advances r.ViewAngles to the recorded camera; on io.EOF the
	//    optional Restart closure re-opens the stream for the next
	//    loop (vanilla attract-loop behaviour). When BOTH menu and
	//    demo are active (title screen + attract loop) the demo path
	//    wins so the player still sees motion under the overlay.
	menuActive := r.Menu != nil && r.Menu.Active()
	demoActive := r.demoActive()
	switch {
	case demoActive:
		// The demo drives its own recorded tic cadence; don't carry a
		// live-sim backlog across a demo interlude.
		r.simAccumulator = 0
		if err := r.playDemoTick(); err != nil {
			return err
		}
	case !menuActive:
		// Fixed-timestep server sim. Accumulate the render dt and step the
		// host at SimStep as many whole ticks as have elapsed, so the
		// simulation advances at a constant rate no matter how fast or slow
		// the frame rendered. The backlog is capped at maxSimSubsteps*SimStep
		// before stepping so a long stall (or the very first frame after a
		// load) can't trigger a spiral-of-death of ever-growing catch-up work
		// -- excess time is dropped (the game runs slightly slow-motion under
		// sustained overload rather than locking up).
		ft := r.SimStep
		if ft <= 0 {
			ft = DefaultSimStep
		}
		r.simAccumulator += dt
		steps := 0
		for r.simAccumulator >= ft && steps < maxSimSubsteps {
			if err := r.Host.Frame(ft); err != nil {
				return err
			}
			r.simAccumulator -= ft
			steps++
		}
		if steps == maxSimSubsteps {
			// Hit the catch-up ceiling: drop the remaining backlog so a
			// sustained overload can't spiral (brief slow-motion instead).
			r.simAccumulator = 0
		}
	default:
		// Menu up (sim paused): don't bank paused wall-clock, so dismissing
		// the menu doesn't dump a burst of catch-up ticks.
		r.simAccumulator = 0
	}

	// 4) Client tick: drain inbound, send clc_move (post-signon only).
	//    ALWAYS runs: the wire-driven signon handshake needs the
	//    inbound drain to fire even when state.Connection ==
	//    StateDisconnected -- otherwise the server's stage-1 signon
	//    byte (which transitions the client to StateConnecting) is
	//    never read, and the lifecycle deadlocks. The OUTBOUND
	//    clc_move build inside Tick is itself gated on StateConnected
	//    so a pre-signon Tick is a pure inbound-drain (no spurious
	//    clc_move on the wire before the handshake completes).
	in := client.TickInput{
		// Pointer (not a copy) so the per-frame impulse drain inside
		// KeyState lands on the runloop's persistent r.Buttons state.
		// See client.TickInput.Buttons + client.BaseMove docs for the
		// "0.5 forever" bug a stack copy would re-introduce.
		Buttons:       &r.Buttons,
		MouseDX:       snap.MouseDX,
		MouseDY:       snap.MouseDY,
		Sensitivity:   1,
		Speeds:        r.Speeds,
		Dt:            dt,
		NowSec:        nowSec,
		ActionButtons: r.Triggers.ActionButtons(),
	}
	out, err := client.Tick(r.Client, r.Conn, in, r.ViewAngles)
	if err != nil {
		return err
	}
	r.ViewAngles = out.ViewAngles

	// 4b) Particle pool per-tic step. Advances every alive particle
	//     by dt seconds using the world gravity scalar; expired
	//     particles are freed back into the pool. Runs BEFORE
	//     Pre2DDraw so the closure renders the up-to-date state.
	//     A nil pool skips the step (matches the legacy bring-up
	//     where the renderer existed but the per-tic advance was
	//     not yet wired). tyrquake: CL_RunParticles inside
	//     Host_Frame's per-tic block, between the server tick and
	//     the screen update.
	if r.ParticlePool != nil {
		r.ParticlePool.Run(dt, r.ParticleGravity, nowSec)
	}

	// 5) Optional 3D pass. The closure owns the BSP walk +
	//    rasterization; on return r.FrameBuffer holds the rendered
	//    scene that Compose2D overlays its 2D layers on top of.
	//    When nil the previous 2D-only behaviour is preserved.
	//
	//    viewOrigin is sourced from the wire-mirrored client state:
	//    r.Client.Entities[r.Client.PlayerNum].Origin -- the entity
	//    snapshot the server broadcast via svc_update + the client
	//    cached into State.Entities (proper client/server split, the
	//    renderer reads what the server told the client rather than
	//    reaching into the server edict pool directly). A missing
	//    entry (player entity not yet received this signon) falls
	//    back to the zero vector; the renderer's PointInLeaf guard
	//    skips the BSP walk for out-of-map origins.
	//
	//    ViewAngles is the (pitch, yaw, roll) the client tick has
	//    just refreshed from mouse + arrow-key input.
	//    SKIPPED while the menu is up UNLESS a demo is playing
	//    underneath (the attract-loop path: the recorded stream's
	//    camera is visible behind the semi-opaque menu overlay, so
	//    the world pass must run to give the overlay something to
	//    sit on). With no demo + the menu up the world pass would
	//    only add CPU cost the menu is going to overdraw anyway.
	//    The menu's own Draw fires in step 5b below.
	if (!menuActive || demoActive) && r.Pre2DDraw != nil {
		viewOrigin := viewOriginFromState(r.Client)
		if err := r.Pre2DDraw(r.FrameBuffer, viewOrigin, r.ViewAngles); err != nil {
			return err
		}
	}

	// 5b) Menu overlay. When the menu is up, Menu.Draw paints the
	//     full-screen overlay into r.FrameBuffer; Compose2D's
	//     background-fill is then skipped so the menu pixels
	//     survive into the present.
	if menuActive {
		if err := r.Menu.Draw(r.FrameBuffer, r.Chars, r.MenuAssets, nowSec); err != nil {
			return err
		}
	}

	// 6+7) Render the 2D frame + expand to RGBA in one call. When
	//      Pre2DDraw is set we skip Compose2D's background clear so
	//      the 3D pixels under the console/notify overlay survive.
	ctx := render.FrameContext{
		Screen:             r.Screen,
		Console:            r.Console,
		Chars:              r.Chars,
		Palette:            r.Palette,
		Now:                nowSec,
		NotifyLifetime:     r.NotifyLifetime,
		MaxNotifyRows:      r.MaxNotifyRows,
		BackgroundIdx:      r.BackgroundIdx,
		SkipBackgroundFill: r.Pre2DDraw != nil || menuActive,
		CenterPrintText:    r.Client.CenterPrintText,
		CenterPrintExpiry:  r.Client.CenterPrintExpiry,
		Intermission:       r.Client.Intermission,
		IntermissionLines:  intermissionLines(r.Client, nowSec),
	}
	if err := render.ExpandFrame(r.FrameBuffer, r.RGBA, ctx); err != nil {
		return err
	}

	// 7) Present.
	if err := r.Backend.PresentFrame(r.RGBA, r.FrameBuffer.Width, r.FrameBuffer.Height); err != nil {
		return err
	}

	// 8+9) Audio (optional).
	if r.SoundPool != nil && len(r.MixBuffer) > 0 {
		// Zero the accumulator each tic; sound.Paint accumulates.
		for i := range r.MixBuffer {
			r.MixBuffer[i] = sound.StereoSample{}
		}
		n := len(r.MixBuffer)
		if n > sound.MaxMixOutputFrames {
			n = sound.MaxMixOutputFrames
		}
		if err := sound.Paint(r.SoundPool, r.MixBuffer, n); err != nil {
			return err
		}
		// 8b) Music driver: poll the client's MusicEpoch, (re-)open
		//     the streamer when the wire-broadcast track changes, and
		//     mix the decoded PCM on TOP of the SFX accumulator. The
		//     dispatch is a no-op when the embedder did not wire
		//     MusicOpen / MusicDecoder, or when the track is silence
		//     (MusicTrack == 0), or when no track epoch advance has
		//     happened since the last poll.
		r.tickMusic(r.MixBuffer[:n])
		if err := r.Backend.QueueAudio(r.MixBuffer[:n]); err != nil {
			if !errors.Is(err, backend.ErrUnsupported) {
				return err
			}
		}
	}

	return nil
}

// tickMusic is the per-frame music driver. (Re-)opens the
// [music.Streamer] whenever the wire-mirrored
// (client.State.MusicTrack, client.State.MusicLoopTrack) pair changes,
// and mixes the decoded PCM into out by ACCUMULATING into each frame
// alongside the SFX already deposited there by [sound.Paint].
//
// No-ops when:
//
//   - r.MusicOpen is nil (embedder disabled the music driver),
//   - r.MusicDecoder is nil (same),
//   - r.Client is nil (defensive; RunFrame's preconditions already
//     forbid this, but the helper stays safe to call standalone).
//
// On a track change to MusicTrack == 0 the driver releases the
// streamer (silence). On a change to a missing track the driver logs
// once via [Runner.MusicMissingLog] (when wired) and leaves the
// streamer cleared.
func (r *Runner) tickMusic(out []sound.StereoSample) {
	if r.Client == nil {
		return
	}
	if r.MusicOpen == nil || r.MusicDecoder == nil {
		return
	}
	// Detect a track change via the monotonic epoch.
	if r.Client.MusicEpoch != r.musicEpochSeen {
		r.musicEpochSeen = r.Client.MusicEpoch
		r.musicStreamer = nil
		track := r.Client.MusicTrack
		if track > 0 {
			loop := r.Client.MusicLoopTrack
			s, err := music.LoadTrack(r.MusicOpen, r.MusicDecoder, track, loop, r.MusicVolume)
			switch {
			case errors.Is(err, music.ErrTrackMissing):
				r.logMusicMissingOnce(track, loop)
			case err != nil:
				// Decoder/factory failure: log under the same "missing"
				// once-per-pair gate so a buggy blob doesn't spam the
				// console. The embedder can distinguish the two via
				// its own loader telemetry if it cares.
				r.logMusicMissingOnce(track, loop)
			default:
				r.musicStreamer = s
			}
		}
	}
	if r.musicStreamer == nil || r.musicStreamer.Stopped() {
		return
	}
	// Mix on TOP of the existing accumulator. The streamer overwrites
	// its frames (it has no knowledge of the SFX already there); to
	// preserve both, we mix into a scratch slice and add. The runloop
	// allocates the scratch once per call (it's bounded by len(out) =
	// MaxMixOutputFrames = 512 frames; a fixed 4 KB stack alloc).
	var scratch [sound.MaxMixOutputFrames]sound.StereoSample
	n := r.musicStreamer.NextSamples(scratch[:len(out)])
	for i := 0; i < n; i++ {
		out[i].L = saturatedAdd16(out[i].L, scratch[i].L)
		out[i].R = saturatedAdd16(out[i].R, scratch[i].R)
	}
}

// saturatedAdd16 sums two int16 samples without int16 wrap-around.
// The mixer accumulator is int16 so a naive `a+b` overflows when
// loud SFX overlap with loud music; the saturation keeps the worst-
// case sum inside [-32768, 32767] (the audible result is the
// familiar "clip" rather than the wrap-around "click").
func saturatedAdd16(a, b int16) int16 {
	sum := int32(a) + int32(b)
	if sum > 32767 {
		return 32767
	}
	if sum < -32768 {
		return -32768
	}
	return int16(sum)
}

// logMusicMissingOnce dispatches the "music track XX missing" log
// through the embedder's optional sink, but only the first time a
// given (track, loopTrack) pair is observed. Idempotent on repeated
// signon retransmits.
func (r *Runner) logMusicMissingOnce(track, loopTrack int) {
	if r.MusicMissingLog == nil {
		return
	}
	if r.musicMissingLogged == nil {
		r.musicMissingLogged = make(map[uint16]struct{})
	}
	key := uint16(track)<<8 | uint16(loopTrack&0xff)
	if _, seen := r.musicMissingLogged[key]; seen {
		return
	}
	r.musicMissingLogged[key] = struct{}{}
	r.MusicMissingLog(track)
}

// viewOriginFromState returns the camera position the per-tic
// Pre2DDraw hook should rasterize against, sourced from the wire-
// mirrored client state at [client.State.Entities][PlayerNum].Origin.
//
// This is the proper client/server split: the renderer reads the
// entity snapshot the server broadcast via svc_update + the client
// cached into State.Entities, NOT the server edict pool directly.
// On the single-process loopback path the two values are identical
// per-tic (svc_update writes the edict origin onto the wire and
// applyUpdate writes it back into State.Entities), but the indirection
// keeps the data-flow honest for the eventual remote-server path.
//
// Fallback: if cs is nil OR State.Entities[PlayerNum] is absent (the
// player entity has not been received yet -- pre-signon, or the wire
// drain has not yet delivered the first svc_update for this slot), the
// returned origin is the zero vector. The Pre2DDraw closure's
// PointInLeaf guard will then skip the BSP walk for that tic, which
// is the same behaviour as the legacy out-of-map anchor.
func viewOriginFromState(cs *client.State) [3]float32 {
	if cs == nil || cs.Entities == nil {
		return [3]float32{}
	}
	es, ok := cs.Entities[cs.PlayerNum]
	if !ok {
		return [3]float32{}
	}
	return es.Origin
}

// intermissionLines composes the per-frame scoreboard line block for
// the intermission overlay. Sourced from the client's cached
// per-tic state:
//
//   - State.IntermissionText non-empty (svc_finale): one slice
//     entry per '\n'-separated substring (the finale credits text
//     the server pushed verbatim). The renderer draws each line
//     centered.
//
//   - State.IntermissionText empty (svc_intermission, scoreboard
//     mode): three rows computed from the stat bank +
//     (nowSec - IntermissionTime):
//
//     "TIME: M:SS"               (mm:ss since intermission start)
//     "SECRETS: X / Y"           (Stats[StatSecrets]  / Stats[StatTotalSecrets])
//     "MONSTERS: X / Y"          (Stats[StatMonsters] / Stats[StatTotalMonsters])
//
// Returns nil when cs is nil OR cs.Intermission is false (the
// renderer's drawIntermission helper is a no-op on a nil slice too,
// so the guard is also a defensive double-check).
//
// tyrquake: the line-by-line text composition inside SCR_DrawIntermission /
// Sbar_IntermissionOverlay; the C upstream renders each row as a
// WAD pic for the label + DrawNumber for the digits. The Go port
// uses plain conchars throughout (the WAD pics aren't loaded yet),
// which keeps the helper free of any asset dependency.
func intermissionLines(cs *client.State, nowSec float32) []string {
	if cs == nil || !cs.Intermission {
		return nil
	}
	if cs.IntermissionText != "" {
		return splitLines(cs.IntermissionText)
	}
	elapsed := nowSec - cs.IntermissionTime
	if elapsed < 0 {
		elapsed = 0
	}
	mins := int(elapsed) / 60
	secs := int(elapsed) % 60
	return []string{
		formatTimeLine(mins, secs),
		formatStatLine("SECRETS", cs.Stats[protocol.StatSecrets], cs.Stats[protocol.StatTotalSecrets]),
		formatStatLine("MONSTERS", cs.Stats[protocol.StatMonsters], cs.Stats[protocol.StatTotalMonsters]),
	}
}

// splitLines splits s on '\n' boundaries. An empty s yields a
// single-element slice containing "" (matches strings.Split's
// behaviour); the renderer's drawIntermission tolerates empty rows
// by drawing nothing for that row's character loop.
func splitLines(s string) []string {
	if s == "" {
		return []string{""}
	}
	var out []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			out = append(out, s[start:i])
			start = i + 1
		}
	}
	out = append(out, s[start:])
	return out
}

// formatTimeLine formats the "TIME: M:SS" row.
func formatTimeLine(mins, secs int) string {
	return "TIME: " + itoa(mins) + ":" + pad2(secs)
}

// formatStatLine formats a "LABEL: X / Y" row.
func formatStatLine(label string, x, y int32) string {
	return label + ": " + itoa(int(x)) + " / " + itoa(int(y))
}

// itoa is a strconv-free integer-to-string helper. Negative values
// are prefixed with '-'; zero yields "0".
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

// pad2 zero-pads a non-negative int to at least 2 digits.
func pad2(n int) string {
	if n < 0 {
		n = 0
	}
	if n < 10 {
		return "0" + itoa(n)
	}
	return itoa(n)
}

// UpdateButtonsFromSnapshot translates the per-frame raw key events in
// snap into edge transitions on the persistent [client.MovementButtons]
// state. Maps:
//
//	KeyW     -> Buttons.Forward
//	KeyS     -> Buttons.Back
//	KeyA     -> Buttons.MoveLeft   (strafe; +moveleft)
//	KeyD     -> Buttons.MoveRight  (strafe; +moveright)
//	KeyLeft  -> Buttons.Left       (turn arrow; +left)
//	KeyRight -> Buttons.Right      (turn arrow; +right)
//	KeyUp    -> Buttons.Lookup
//	KeyDown  -> Buttons.Lookdown
//	KeySpace -> Buttons.Up         (jump)
//	KeyCtrl  -> Buttons.Down       (crouch)
//	KeyShift -> Buttons.SpeedHeld  (+speed modifier)
//
// Each down event sets the held bit (bit 0) and stamps the down-edge
// bit (bit 1) so [client.KeyState] reports the partial-frame value
// the first tic the key is pressed. Each up event clears the held bit
// and stamps the up-edge bit (bit 2).
//
// The mouse-button slot ([backend.KeyMouse1] / [backend.KeyMouse2])
// and the trigger keys (Enter/Tab/Escape) are NOT mapped here: those
// drive the per-frame ActionButtons / Impulse bits the caller OR-s
// onto the [client.TickInput] (separate from the movement buttons).
func UpdateButtonsFromSnapshot(buttons *client.MovementButtons, snap backend.InputSnapshot) {
	for _, k := range snap.KeysDown {
		if slot := buttonSlot(buttons, k); slot != nil {
			pressButton(slot)
			continue
		}
		if k == backend.KeyShift {
			buttons.SpeedHeld = true
		}
	}
	for _, k := range snap.KeysUp {
		if slot := buttonSlot(buttons, k); slot != nil {
			releaseButton(slot)
			continue
		}
		if k == backend.KeyShift {
			buttons.SpeedHeld = false
		}
	}
}

// buttonSlot resolves k to the matching field of buttons. Returns nil
// for the keys handled out-of-band (Shift -> SpeedHeld bool, and
// every key not in the movement set).
func buttonSlot(buttons *client.MovementButtons, k backend.KeyCode) *client.ButtonState {
	switch k {
	case backend.KeyW:
		return &buttons.Forward
	case backend.KeyS:
		return &buttons.Back
	case backend.KeyA:
		return &buttons.MoveLeft
	case backend.KeyD:
		return &buttons.MoveRight
	case backend.KeyLeft:
		return &buttons.Left
	case backend.KeyRight:
		return &buttons.Right
	case backend.KeyUp:
		return &buttons.Lookup
	case backend.KeyDown:
		return &buttons.Lookdown
	case backend.KeySpace:
		return &buttons.Up
	case backend.KeyCtrl:
		return &buttons.Down
	}
	return nil
}

// pressButton stamps the held bit (bit 0) and the down-edge bit (bit
// 1) onto b. The down-edge bit fires once -- [client.KeyState] clears
// it the next time it samples the button.
func pressButton(b *client.ButtonState) {
	b.Pressed |= 1 | 2
}

// releaseButton clears the held bit (bit 0) and stamps the up-edge
// bit (bit 2). [client.KeyState] clears the up-edge bit on its next
// sample.
func releaseButton(b *client.ButtonState) {
	b.Pressed &^= 1
	b.Pressed |= 4
}

// TriggerButtons tracks the persistent held state of the on-wire
// trigger keys -- the bits the QC progs read via self.button*. The
// movement keys (W/A/S/D, arrows, shift) live in the separate
// [client.MovementButtons] structure and feed [client.BaseMove] /
// [client.AdjustAngles]; they do NOT show up in the on-wire `buttons`
// byte.
//
// Mappings (driven by [UpdateTriggersFromSnapshot]):
//
//	KeyMouse1 -> Attack ([client.ButtonAttack] = 1)
//	KeyEnter  -> Jump   ([client.ButtonJump]   = 2)
//
// The mouse-2 / Escape / Tab keys are intentionally NOT mapped: Q1's
// per-tic clc_move only carries +attack and +jump in vanilla NQ; the
// QC progs do not read additional bits. Engines that need a "use"
// trigger (BUTTON_USE = 4) layer it on via an impulse byte instead.
type TriggerButtons struct {
	Attack bool // KeyMouse1 currently held
	Jump   bool // KeySpace or KeyEnter currently held (+jump)
}

// ActionButtons returns the [server.UserCmd.Buttons] bitmask the
// runloop hands to [client.Tick] each tic. The caller OR-s this
// straight onto the per-tic [client.TickInput.ActionButtons] field,
// which [client.Tick] then writes onto the outbound clc_move
// payload's `buttons` byte. Mapping mirrors tyrquake's
// CL_BaseButtons:
//
//	Attack -> [client.ButtonAttack] (1)
//	Jump   -> [client.ButtonJump]   (2)
func (t TriggerButtons) ActionButtons() uint8 {
	var b uint8
	if t.Attack {
		b |= client.ButtonAttack
	}
	if t.Jump {
		b |= client.ButtonJump
	}
	return b
}

// dispatchMenuInput routes per-frame KeyDown events through the
// optional [menu.Menu] state machine. Returns true when the menu
// is currently active (which makes the caller short-circuit the
// movement / trigger button mappers + the host tick + the world
// pass for the rest of the tic).
//
// When the menu is NOT active, Esc-pressed-this-frame opens it
// (Menu.Open) and the function returns true on that same tic so
// the press isn't double-counted (the same Esc would otherwise hit
// no movement slot but would still flow through to Tick as a
// no-op + then a SECOND frame's Esc would immediately close the
// menu the user just opened).
//
// Up-events are NOT routed into the menu: the menu's contract is
// "fire on key press", matching upstream M_Keydown which is wired
// to the down-half of the binding only. A nil [Runner.Menu] makes
// the function a constant false return.
func (r *Runner) dispatchMenuInput(snap backend.InputSnapshot) bool {
	if r.Menu == nil {
		return false
	}
	if !r.Menu.Active() {
		// Out-of-menu: open on Esc press, but only when an Esc
		// arrived this frame (don't treat the held state as a
		// continuous "open" command).
		for _, k := range snap.KeysDown {
			if k == backend.KeyEscape {
				r.Menu.Open()
				return true
			}
		}
		return false
	}
	// In-menu: every down-edge feeds Handle. We do NOT early-return
	// after the first key so multi-key snapshots (rare in practice)
	// stay deterministic in their dispatch order.
	for _, k := range snap.KeysDown {
		r.Menu.Handle(k)
	}
	// Still active means "menu owns the frame"; transition to
	// StateNone (e.g. Skill confirm) makes the next tic unfreeze
	// the world automatically.
	return r.Menu.Active()
}

// UpdateTriggersFromSnapshot edge-applies snap.KeysDown / snap.KeysUp
// onto triggers. Down events set the held flag; up events clear it.
// Keys not in the trigger set (everything but [backend.KeyMouse1] and
// [backend.KeyEnter]) are ignored here -- the movement set is owned
// by [UpdateButtonsFromSnapshot].
//
// IDEMPOTENCE: applying the same KeysDown sequence twice leaves the
// triggers at the same true value; ditto KeysUp. The held bits are
// stateful (NOT auto-cleared each tic) so a held mouse-fire keeps
// firing every clc_move until the up event arrives, matching how
// upstream's +attack works.
func UpdateTriggersFromSnapshot(triggers *TriggerButtons, snap backend.InputSnapshot) {
	for _, k := range snap.KeysDown {
		switch k {
		case backend.KeyMouse1:
			triggers.Attack = true
		case backend.KeyEnter, backend.KeySpace:
			triggers.Jump = true
		}
	}
	for _, k := range snap.KeysUp {
		switch k {
		case backend.KeyMouse1:
			triggers.Attack = false
		case backend.KeyEnter, backend.KeySpace:
			triggers.Jump = false
		}
	}
}
