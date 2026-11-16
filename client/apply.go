// Copyright (c) 1996-1997 Id Software, Inc.
// Copyright (c) 2026 the go-quake1/engine authors.
// SPDX-License-Identifier: GPL-2.0-or-later

package client

import (
	"errors"
	"fmt"

	"github.com/go-quake1/engine/protocol"
)

// ErrApplyNilState is returned by [Apply] when state == nil.
var ErrApplyNilState = errors.New("client.Apply: nil state")

// ErrApplyNilMessage is returned by [Apply] when msg == nil.
var ErrApplyNilMessage = errors.New("client.Apply: nil message")

// ErrApplyUnknown is returned by [Apply] when the message's runtime
// type is not one of the [Decoded] variants Apply knows about. It is
// a forward-compatibility guard: if a new svc_* decoder ships a new
// Decoded* shape and the matching Apply arm is forgotten, callers
// see this sentinel instead of a silent no-op.
var ErrApplyUnknown = errors.New("client.Apply: unknown decoded message type")

// ErrApplyBadState is returned by [Apply] when a lifecycle message
// (DecodedSignonNum stage 4, DecodedDisconnect) maps to a state
// transition that the [State] helper rejects (e.g. MarkSpawned when
// not in StateConnecting). The underlying [State] sentinel
// ([ErrAlreadyConnected] / [ErrNotConnecting]) is wrapped so callers
// can do both [errors.Is](err, ErrApplyBadState) and
// [errors.Unwrap](err) to recover it.
var ErrApplyBadState = errors.New("client.Apply: state transition rejected")

// applyBadStateErr wraps an underlying State.* sentinel so that
//
//	errors.Is(err, ErrApplyBadState)         == true
//	errors.Is(err, ErrNotConnecting)         == true (when underlying)
//	errors.Unwrap(err)                       == the underlying sentinel
//
// both succeed. The wrapper is purely internal -- callers use the
// errors.Is / errors.Unwrap protocol to interrogate.
type applyBadStateErr struct{ underlying error }

func (e *applyBadStateErr) Error() string {
	return fmt.Sprintf("%s: %v", ErrApplyBadState.Error(), e.underlying)
}

// Is matches ErrApplyBadState (the category sentinel) so the wrapped
// error can be inspected without an Unwrap dance.
func (e *applyBadStateErr) Is(target error) bool { return target == ErrApplyBadState }

// Unwrap returns the underlying State sentinel so errors.Is also
// matches the specific transition error (ErrNotConnecting /
// ErrAlreadyConnected).
func (e *applyBadStateErr) Unwrap() error { return e.underlying }

// Apply mutates state to reflect the decoded message. Each Decoded*
// type drives a specific state mutation (precache fill from
// DecodedServerInfo, player stat write from DecodedClientData,
// lifecycle transition from DecodedSignonNum, ...).
//
// tyrquake: the per-svc_* mutation arms inside CL_ParseServerMessage
// (NQ/cl_parse.c). Apply is the Go-side dispatcher that pairs each
// Decoded* shape with its mutation, factored out of the wire layer
// so the decoder stays pure.
//
// nowSec is the current wall-clock-like server time -- used for
// MsgTime updates on every message (the C upstream's cl.mtime[0]
// update inside CL_ParseServerMessage's main loop). MsgTime is
// updated on EVERY call, including the no-op arms.
//
// Returns:
//
//   - nil                  on success
//   - ErrApplyNilState     if state == nil
//   - ErrApplyNilMessage   if msg == nil
//   - ErrApplyUnknown      if msg's runtime type is not a known
//     Decoded* variant
//   - ErrApplyBadState     wrapping the underlying State.* sentinel
//     when a lifecycle transition is rejected
func Apply(state *State, msg Decoded, nowSec float32) error {
	if state == nil {
		return ErrApplyNilState
	}
	if msg == nil {
		return ErrApplyNilMessage
	}

	// Always-on: the upstream's per-message cl.mtime[0] update.
	state.MsgTime = nowSec

	switch m := msg.(type) {
	case DecodedServerInfo:
		return applyServerInfo(state, m)
	case DecodedSignonNum:
		return applySignonNum(state, m)
	case DecodedDisconnect:
		state.Disconnect()
		return nil
	case DecodedSetView:
		state.PlayerNum = m.EntityNum
		return nil
	case DecodedClientData:
		applyClientData(state, m)
		return nil
	case DecodedUpdateStat:
		applyUpdateStat(state, m)
		return nil
	case DecodedUpdateName:
		// TODO: add Names[16]string once scoreboard rendering lands.
		// For now the message is intentionally acknowledged and
		// discarded; the wire layer has already validated the body.
		return nil
	case DecodedUpdateColors:
		// TODO: add Colors[16]int once scoreboard rendering lands.
		// Upstream cl.scores[i].colors has no [State] field yet;
		// intentional acknowledged-and-discarded.
		return nil
	case DecodedUpdateFrags:
		applyUpdateFrags(state, m)
		return nil
	case DecodedBaseline:
		applyBaseline(state, m)
		return nil
	case DecodedUpdate:
		applyUpdate(state, m, nowSec)
		return nil
	case DecodedParticle:
		applyParticle(state, m)
		return nil
	case DecodedTempEntity:
		applyTempEntity(state, m)
		return nil
	case DecodedCenterPrint:
		applyCenterPrint(state, m, nowSec)
		return nil
	case DecodedIntermission:
		applyIntermission(state, nowSec)
		return nil
	case DecodedFinale:
		applyFinale(state, m, nowSec)
		return nil
	case DecodedCDTrack:
		applyCDTrack(state, m)
		return nil
	case DecodedLightStyle:
		applyLightStyle(state, m)
		return nil
	case DecodedNop,
		DecodedPrint,
		DecodedStuffText,
		DecodedCutscene,
		DecodedSellScreen,
		DecodedKilledMonster,
		DecodedFoundSecret,
		DecodedSound:
		// Documented no-op arms:
		//   - Nop:                       connection-alive heartbeat
		//   - Print / StuffText:         renderer/UI + console concern
		//   - Cutscene / SellScreen:     UI-state transitions
		//   - KilledMonster / FoundSecret: gameplay sound triggers
		//     (the stat-bank tallies still arrive via svc_updatestat)
		//   - Sound:                     sound mixer (separate layer)
		return nil
	}
	return fmt.Errorf("%w: %T", ErrApplyUnknown, msg)
}

// applyServerInfo handles svc_serverinfo: fill precaches + level
// banner, clear local-player slot. The SetConnecting transition
// itself happens in the caller's establish-connection path; Apply
// must NOT touch state.Connection here. tyrquake: CL_ParseServerInfo.
func applyServerInfo(state *State, m DecodedServerInfo) error {
	state.MapName = m.LevelName
	state.LevelName = m.LevelName
	state.ModelPrecache = append([]string(nil), m.ModelPrecache...)
	state.SoundPrecache = append([]string(nil), m.SoundPrecache...)
	state.PlayerNum = 0
	return nil
}

// applySignonNum handles the signon-handshake stage byte. Stage 1
// is the wire-driven equivalent of CL_EstablishConnection: when the
// client sees the first signonnum byte from a server, it knows the
// handshake has started and transitions itself into [StateConnecting]
// (a no-op when already past Disconnected, e.g. when the caller pre-
// drove the transition via [State.SetConnecting]). Stages 2 and 3
// are caller-side handshake markers Apply just acknowledges. Stage 4
// (post-spawn signon) drives the final transition to [StateConnected]
// via [State.MarkSpawned]. tyrquake: CL_SignonReply -- the upstream
// C engine sets cls.state = ca_connected in CL_EstablishConnection
// BEFORE any wire bytes arrive; the Go port collapses that into the
// stage-1 handler so a server emitting svc_signonnum(1) drives the
// client's lifecycle directly, with no caller-side pre-step needed.
func applySignonNum(state *State, m DecodedSignonNum) error {
	switch m.Stage {
	case 1:
		// Wire-driven establish: bring an undriven (StateDisconnected)
		// state into StateConnecting so the upcoming stage-4 byte can
		// MarkSpawned cleanly. Already-connecting / already-connected
		// states are left untouched -- this is idempotent on retransmit.
		if state.Connection == StateDisconnected {
			// SetConnecting's only failure path is "not in
			// StateDisconnected"; the guard above rules it out, so the
			// error return is structurally unreachable here.
			_ = state.SetConnecting()
		}
		return nil
	case 4:
		if err := state.MarkSpawned(); err != nil {
			return &applyBadStateErr{underlying: err}
		}
		return nil
	}
	// Stages 2 + 3 are no-op acknowledgements -- the C upstream uses
	// them as triggers for outbound clc_stringcmd commands (prespawn /
	// spawn), which the Go port doesn't yet emit.
	return nil
}

// applyClientData handles svc_clientdata: copy the per-tic player
// snapshot into the matching State fields. tyrquake:
// CL_ParseClientdata.
func applyClientData(state *State, m DecodedClientData) {
	state.ViewHeightOffset = m.ViewHeightOffset
	state.IdealPitch = m.IdealPitch
	state.PunchAngle = m.PunchAngle
	state.Velocity = m.Velocity
	state.Health = m.Health
	state.Items = m.Items
	state.Ammo = [4]int{m.Ammo[0], m.Ammo[1], m.Ammo[2], m.Ammo[3]}
	state.CurrentAmmo = m.CurrentAmmo
	state.OnGround = m.OnGround
	state.InWater = m.InWater
}

// applyUpdateStat handles svc_updatestat: write into Stats[Stat] if
// in range, silently skip otherwise. The upstream Sys_Error's on
// out-of-range stat ids; the Go port treats unknown stat ids as
// future-extension data and ignores them.
func applyUpdateStat(state *State, m DecodedUpdateStat) {
	if m.Stat < 0 || m.Stat >= len(state.Stats) {
		return
	}
	state.Stats[m.Stat] = m.Value
}

// applyUpdateFrags handles svc_updatefrags: write into Frags[Slot]
// if in range, silently skip otherwise.
func applyUpdateFrags(state *State, m DecodedUpdateFrags) {
	if m.Slot < 0 || m.Slot >= len(state.Frags) {
		return
	}
	state.Frags[m.Slot] = m.Frags
}

// applyBaseline handles svc_spawnbaseline: cache the per-entity
// snapshot into [State.Baselines] keyed by EntityNum so per-tic
// svc_update deltas (a follow-up batch) can resolve their omitted
// fields against the entity's last-known-good state. Allocates the
// map lazily so callers that constructed a State without going through
// [NewState] don't crash on the first arm.
// tyrquake: CL_ParseBaseline -- the per-entity body that copies the
// decoded entity_state_t into cl_entities[entnum].baseline.
func applyBaseline(state *State, m DecodedBaseline) {
	if state.Baselines == nil {
		state.Baselines = make(map[int]EntityBaseline)
	}
	state.Baselines[m.EntityNum] = EntityBaseline{
		ModelIdx: m.ModelIdx,
		Frame:    m.Frame,
		ColorMap: m.ColorMap,
		SkinNum:  m.SkinNum,
		Origin:   m.Origin,
		Angles:   m.Angles,
		Alpha:    m.Alpha,
	}
}

// applyParticle dispatches a svc_particle burst into the embedder's
// optional [State.EmitParticles] sink. nil sink = silent no-op
// (mirrors the historical bring-up behaviour: the particle decoder
// landed before the renderer pool did, and the apply arm was a
// documented "particle pool is a separate layer" no-op). Once the
// embedder wires the sink to render.Pool.Emit the same arm starts
// driving real particles.
//
// tyrquake: the svc_particle case inside CL_ParseServerMessage --
// the C upstream parses origin/dir/color/count off the wire then
// calls R_RunParticleEffect directly (no callback indirection
// because the C engine has a global *r_particles pool); the Go
// port keeps the engine-renderer split clean via the sink.
func applyParticle(state *State, m DecodedParticle) {
	if state.EmitParticles == nil {
		return
	}
	state.EmitParticles(m.Origin, m.Dir, m.Color, m.Count)
}

// applyTempEntity dispatches a svc_temp_entity sub-event into the
// embedder's optional sinks: point-effect kinds (Spike / SuperSpike /
// Gunshot / Explosion / TarExplosion / WizSpike / KnightSpike /
// LavaSplash / Teleport) go through [State.EmitTempEntity] with the
// wire-reported Origin; lightning kinds (TELightning1 / 2 / 3 / TEBeam)
// go through [State.EmitBeam] with the (Start, End) traceline + the
// owning entity index. TEExplosion2 still routes to EmitTempEntity
// (colour-mapped explosion is a future renderer batch -- the embedder
// is free to inspect Kind and dispatch it locally if it grows that
// capability later).
//
// nil sinks = silent no-ops so callers that don't yet wire the pool
// keep the historical "TE arm is a stub" behaviour.
//
// tyrquake: CL_ParseTEnt -- the per-kind switch that calls
// R_ParticleExplosion / R_RunParticleEffect / R_LavaSplash /
// R_TeleportSplash / CL_NewDLight / CL_ParseBeam depending on the
// TE_* byte. Light-emission (CL_NewDLight calls) is out of scope here
// -- the dynamic-lighting pool is wired separately when the bring-up
// gets to it.
func applyTempEntity(state *State, m DecodedTempEntity) {
	switch m.Kind {
	case TELightning1, TELightning2, TELightning3, TEBeam:
		if state.EmitBeam == nil {
			return
		}
		state.EmitBeam(int(m.Kind), m.EntityNum, m.Start, m.End)
		return
	}
	if m.Kind == TEExplosion2 && state.EmitExplosion2 != nil {
		state.EmitExplosion2(m.Origin, m.ColorStart, m.ColorLength)
	}
	if state.EmitTempEntity == nil {
		return
	}
	state.EmitTempEntity(int(m.Kind), m.Origin)
}

// applyCenterPrint handles svc_centerprint: stash the text on State +
// stamp the per-frame expiry [CenterPrintLifetime] seconds in the
// future. Empty text clears the active centerprint immediately (matches
// upstream SCR_CenterPrint, which sets scr_centertime_off = 0 on an
// empty string so the renderer's fade-out runs the next frame).
//
// tyrquake: SCR_CenterPrint in screen.c -- copies the string into
// scr_centerstring + seeds scr_centertime_off = scr_centertime->value
// (default 2.0).
func applyCenterPrint(state *State, m DecodedCenterPrint, nowSec float32) {
	state.CenterPrintText = m.Text
	if m.Text == "" {
		state.CenterPrintExpiry = 0
		return
	}
	state.CenterPrintExpiry = nowSec + CenterPrintLifetime
}

// applyIntermission handles svc_intermission: flip the
// [State.Intermission] flag, clear any [State.IntermissionText] (the
// scoreboard variant has no caller-supplied text -- the renderer
// composes it from the stat bank), and stamp IntermissionTime with
// nowSec. The renderer reads the flag + the cached stat bank
// (StatTotalSecrets / StatSecrets / StatTotalMonsters / StatMonsters
// + Time) to lay out the end-of-level scoreboard.
//
// Active centerprint is suppressed (cleared) on entry so a "you got
// the shotgun" banner doesn't bleed through the scoreboard overlay.
// tyrquake: the scr_centertime_off = 0 reset inside CL_ParseStartSound's
// svc_intermission arm.
//
// tyrquake: the svc_intermission case inside CL_ParseServerMessage
// (NQ/cl_parse.c) that sets cl.intermission = 1 + cl.completed_time
// = cl.time + scr_centertime_off = 0.
func applyIntermission(state *State, nowSec float32) {
	state.Intermission = true
	state.IntermissionText = ""
	state.IntermissionTime = nowSec
	state.CenterPrintText = ""
	state.CenterPrintExpiry = 0
}

// applyFinale handles svc_finale: flip the [State.Intermission] flag
// AND stash the caller-supplied multi-line credits text in
// [State.IntermissionText]. The renderer composes the centered-text
// overlay from IntermissionText (one screen line per '\n'-delimited
// substring); the per-map scoreboard from the stat bank is
// suppressed when IntermissionText is non-empty.
//
// Active centerprint is suppressed on entry (same rationale as
// applyIntermission). tyrquake: the svc_finale case inside
// CL_ParseServerMessage that sets cl.intermission = 2 + copies the
// payload into scr_centerstring.
func applyFinale(state *State, m DecodedFinale, nowSec float32) {
	state.Intermission = true
	state.IntermissionText = m.Text
	state.IntermissionTime = nowSec
	state.CenterPrintText = ""
	state.CenterPrintExpiry = 0
}

// applyCDTrack handles svc_cdtrack: write the (track, loopTrack) byte
// pair onto [State.MusicTrack] + [State.MusicLoopTrack] and bump
// [State.MusicEpoch] so the embedder's per-tic music driver detects
// the change and (re-)opens the streamer.
//
// The epoch is bumped unconditionally, even when the new pair is
// identical to the previous one: the server may legitimately
// retransmit svc_cdtrack to force a music restart (e.g. after a save-
// game load that ought to re-trigger the current track from the top),
// and the embedder's driver is responsible for deciding whether to
// re-arm the streamer or treat the redundant emission as a no-op by
// comparing (MusicTrack, MusicLoopTrack) themselves.
//
// tyrquake: the svc_cdtrack arm of CL_ParseServerMessage (the C
// upstream writes cl.cdtrack + cl.looptrack then immediately calls
// BGM_PlayCDtrack -- the Go port splits the wire write from the
// playback driver via the epoch counter so the music subsystem stays
// out of the wire decoder).
func applyCDTrack(state *State, m DecodedCDTrack) {
	state.MusicTrack = m.Track
	state.MusicLoopTrack = m.LoopTrack
	state.MusicEpoch++
}

// applyUpdate handles svc_update: seed the entity's live state from
// the cached [EntityBaseline] (so fields whose U_* bit is unset keep
// their last-known-good value), then overlay the U_*-bit-flagged
// fields from the message.
//
// Lazily allocates [State.Entities] so callers that constructed a
// State without going through [NewState] don't crash on the first
// arm. Missing baseline = zero EntityState seed (the upstream
// allocates entities lazily on the first parse too; entities the
// server emits an update for without a prior baseline appear as
// "default state + the update's bits").
//
// tyrquake: CL_ParseUpdate -- the "entity_state_t state = ent->baseline;
// <decode delta bits onto state>; ent->state = state;" body.
//
// Animation-interpolation bookkeeping (PrevFrame + LerpStartTime): when
// the message's UFrame bit overlays a NEW Frame value (different from
// the live cache's prior Frame) the arm copies the prior Frame into
// PrevFrame and stamps LerpStartTime = nowSec. The renderer reads
// these to lerp between adjacent poses over the 10 Hz alias-animation
// window. tyrquake: entity_t.previousframe + entity_t.frame_start_time
// in CL_LerpEntities.
func applyUpdate(state *State, m DecodedUpdate, nowSec float32) {
	if state.Entities == nil {
		state.Entities = make(map[int]EntityState)
	}

	// Seed from the last-known live state if present (so successive
	// updates carry forward unchanged fields); fall back to the
	// baseline; finally to zero. The upstream's idiom is
	// "state = ent->baseline" every time -- the Go port prefers the
	// last live state because the bring-up always emits full origins
	// + angles in the update (the delta-encoded fields haven't been
	// implemented yet, so missing fields are genuinely "no change",
	// not "back to baseline").
	es, ok := state.Entities[m.EntityNum]
	if !ok {
		if bl, hadBaseline := state.Baselines[m.EntityNum]; hadBaseline {
			es = EntityState{
				ModelIdx: bl.ModelIdx,
				Frame:    bl.Frame,
				ColorMap: bl.ColorMap,
				SkinNum:  bl.SkinNum,
				Origin:   bl.Origin,
				Angles:   bl.Angles,
			}
		}
	}

	// Overlay each U_*-bit-flagged field from the decoded message.
	// Per-axis origin / angles are individually gated by the upstream
	// wire format -- the encoder emits only the axes whose bit is set.
	if m.Bits&protocol.UOrigin1 != 0 {
		es.Origin[0] = m.Origin[0]
	}
	if m.Bits&protocol.UOrigin2 != 0 {
		es.Origin[1] = m.Origin[1]
	}
	if m.Bits&protocol.UOrigin3 != 0 {
		es.Origin[2] = m.Origin[2]
	}
	if m.Bits&protocol.UAngle1 != 0 {
		es.Angles[0] = m.Angles[0]
	}
	if m.Bits&protocol.UAngle2 != 0 {
		es.Angles[1] = m.Angles[1]
	}
	if m.Bits&protocol.UAngle3 != 0 {
		es.Angles[2] = m.Angles[2]
	}
	if m.Bits&protocol.UModel != 0 {
		es.ModelIdx = m.Model
	}
	if m.Bits&protocol.UFrame != 0 {
		// Animation-interp bookkeeping: only stamp when the frame
		// actually changes. Repeated updates with the same Frame
		// preserve the existing lerp window (otherwise the renderer
		// would see a perpetual lerp == 0 freeze on every tic).
		if m.Frame != es.Frame {
			es.PrevFrame = es.Frame
			es.LerpStartTime = nowSec
		}
		es.Frame = m.Frame
	}
	if m.Bits&protocol.UColorMap != 0 {
		es.ColorMap = m.ColorMap
	}
	if m.Bits&protocol.USkin != 0 {
		es.SkinNum = m.Skin
	}
	if m.Bits&protocol.UEffects != 0 {
		es.Effects = m.Effects
	}

	state.Entities[m.EntityNum] = es
}
