// Copyright (c) 1996-1997 Id Software, Inc.
// Copyright (c) 2026 the go-quake1/engine authors.
// SPDX-License-Identifier: GPL-2.0-or-later

package client

import (
	"errors"

	"github.com/go-quake1/engine/sizebuf"
)

// ConnectionState tracks the client's high-level lifecycle phase.
// tyrquake: cactive_t in NQ/client.h. The upstream enum has five
// values (ca_dedicated / ca_disconnected / ca_connected /
// ca_firstupdate / ca_active); the Go port collapses
// ca_connected+ca_firstupdate into [StateConnecting] (both phases
// are "signon handshake in progress" from the lifecycle helpers'
// point of view) and drops ca_dedicated (the dedicated-server
// build has no client state at all, so the case is structurally
// unreachable here).
type ConnectionState int

const (
	StateDisconnected ConnectionState = iota
	StateConnecting                   // signon handshake in progress
	StateConnected                    // fully signed on, in-game
)

// MaxLightStyles + MaxDLights + MaxTempEntities + MaxEfrags +
// MaxBeams + NumPingTimes are the per-client static caps.
// tyrquake: NQ/quakedef.h + NQ/client.h defines.
const (
	MaxLightStyles  = 64  // NQ/quakedef.h MAX_LIGHTSTYLES
	MaxDLights      = 32  // NQ/client.h MAX_DLIGHTS
	MaxTempEntities = 64  // NQ/quakedef.h MAX_TEMP_ENTITIES
	MaxEfrags       = 640 // NQ/quakedef.h MAX_EFRAGS (id1 value)
	MaxBeams        = 24  // NQ/client.h MAX_BEAMS
	NumPingTimes    = 16  // mirrors server.NumPingTimes
)

// CenterPrintLifetime is the wall-clock duration (in seconds) a
// centerprint message stays on screen before fading out. tyrquake:
// scr_centertime_off seeded to scr_centertime->value (default 2.0)
// inside SCR_CenterPrint. The Go port hard-codes the same default.
const CenterPrintLifetime float32 = 2.0

// MaxClientMessage is the per-client transport buffer size. Same
// numeric value as server.MaxMsgLen (1 << 18); duplicated here so
// the client package stays decoupled from the server package.
// tyrquake: NQ/quakedef.h MAX_MSGLEN.
const MaxClientMessage = 1 << 18

// EntityBaseline is one per-entity snapshot the server emits during
// the signon handshake (svc_spawnbaseline). The client caches every
// baseline so future svc_update deltas can be resolved against the
// last-known-good state of the entity. tyrquake: entity_state_t (the
// e.baseline field on entity_t in the client-side CL_ParseBaseline
// code path).
//
// The Alpha field is FITZ-only; vanilla NQ baselines always store 0
// (ENTALPHA_DEFAULT). Mirrors the wire-decoded [DecodedBaseline] shape
// minus the EntityNum (which is the map key).
type EntityBaseline struct {
	ModelIdx int
	Frame    int
	ColorMap int
	SkinNum  int
	Origin   [3]float32
	Angles   [3]float32
	Alpha    int
}

// EntityState is the live per-tic snapshot the client maintains for
// every entity the server broadcast a svc_update for. The Apply arm
// for [DecodedUpdate] writes into [State.Entities] keyed by EntityNum;
// the renderer + animation systems read from there to draw the
// current frame.
//
// Fields not present in the update message (the wire-format omits
// fields whose U_* bit is unset) are seeded from the entity's
// last-known [EntityBaseline] -- the upstream's
// "entity_state_t state = ent->baseline; <decode deltas onto state>"
// idiom inside CL_ParseUpdate. The Go port collapses the per-entity
// "last-known state" into this single struct (rather than the
// upstream's baseline+deltabits+lerp_origin/lerp_angles split) so
// the per-tic mutation is a single map write.
type EntityState struct {
	ModelIdx int
	Frame    int
	ColorMap int
	SkinNum  int
	Effects  int
	Origin   [3]float32
	Angles   [3]float32

	// PrevFrame + LerpStartTime drive client-side animation
	// interpolation between adjacent frames. The wire protocol only
	// transmits the CURRENT frame index; the previous one is
	// remembered here. Apply's [DecodedUpdate] arm copies the prior
	// Frame into PrevFrame + stamps LerpStartTime = nowSec whenever
	// the message's U_FRAME bit changes the entity's Frame. The
	// renderer's per-tic alias-draw pass then computes
	//
	//	lerp = clamp((now - LerpStartTime) / animPeriod, 0, 1)
	//
	// and hands (PrevFrame, Frame, lerp) to [render.DrawAliasInterp]
	// for byte-space pose blending. Period == 0.1 s (10 Hz alias
	// animation cadence) matches the upstream R_SetupAliasFrame
	// interpolation window. tyrquake: entity_t.previousframe +
	// entity_t.frame_start_time inside CL_LerpEntities / R_AliasSetupFrame.
	PrevFrame     int
	LerpStartTime float32
}

// LightStyle is one of the 64 named light animation strings.
// Each byte is one frame; 'a' = dim, 'z' = bright. tyrquake:
// lightstyle_t.map in NQ/client.h (the .length field is implicit
// in len(Anim)).
type LightStyle struct {
	Anim []byte
}

// DLight is a per-frame dynamic light (muzzle flash, projectile
// glow, ...). tyrquake: dlight_t in NQ/client.h. Color is stored
// inline (the upstream holds a `const float *` into dl_colors[],
// the Go port copies the three components so the struct owns its
// data and is trivially zero-valued).
type DLight struct {
	Origin   [3]float32
	Radius   float32
	Die      float32 // server time at which the light should expire
	Decay    float32 // radius reduction per second
	MinLight float32
	Key      int
	Color    [3]float32
}

// State is the top-level client state. Everything the renderer +
// UI read to draw a frame lives here. tyrquake: client_state_t in
// NQ/client.h.
//
// The struct is wiped on every map change (see [State.Clear]);
// the [State.Message] sizebuf survives because the upstream's
// cls.message is allocated in client_static_t (which is NOT
// wiped). The Go port collapses both into one struct + the Clear
// method preserves the buffer.
type State struct {
	Connection ConnectionState
	Spawned    bool

	// SentSpawn latches once the client has emitted the canonical
	// "spawn" clc_stringcmd (the wire trigger the server uses to flip
	// its own Client.Spawned + queue svc_signonnum(4)). [client.Tick]
	// reads + sets this so the stringcmd is sent exactly once per
	// signon: on the FIRST post-StateConnecting tick the client sends
	// "spawn", then latches the flag so subsequent ticks don't
	// retransmit. Reset by [State.Disconnect] / [State.Clear] so a
	// reconnect flow re-arms the emission.
	SentSpawn bool

	// Time is the client's view of simulation time, between MsgTime
	// and OldTime so the renderer can lerp other state. OldTime is
	// the previous tick we received from the server. tyrquake:
	// client_state_t.time / oldtime / mtime[0].
	Time    float32
	OldTime float32
	MsgTime float32

	// Player slot the local client is bound to (1..maxclients).
	// tyrquake: client_state_t.viewentity.
	PlayerNum int

	// Server identity. ModelPrecache + SoundPrecache are
	// sentinel-terminated slices the decoder fills as the
	// svc_serverinfo handler walks the wire string list.
	MapName       string
	LevelName     string
	ModelPrecache []string
	SoundPrecache []string
	LightStyles   [MaxLightStyles]LightStyle
	// HaveLightStyles is set by the first svc_lightstyle: until then a renderer
	// that has no server to learn them from (synthetic scenes) keeps its own
	// default table.
	HaveLightStyles bool

	// Per-frame visual state. DLights[i].Die == 0 means the slot
	// is free; NumVisEdicts is the count of entities the decoder
	// has queued for this frame's render walk.
	DLights      [MaxDLights]DLight
	NumVisEdicts int

	// Local player state -- the mirror of the server's clientdata
	// (svc_clientdata wire message). tyrquake: scattered fields in
	// client_state_t (viewheight / idealpitch / punchangle /
	// velocity / stats[STAT_HEALTH] / stats[STAT_ITEMS] /
	// stats[STAT_SHELLS..STAT_CELLS] / stats[STAT_ACTIVEWEAPON]).
	ViewHeightOffset float32
	IdealPitch       float32
	PunchAngle       [3]float32
	Velocity         [3]float32
	Health           int
	Items            int32
	Ammo             [4]int // shells / nails / rockets / cells
	CurrentAmmo      int
	Stats            [32]int32 // MAX_CL_STATS generic stat bank
	OnGround         bool
	InWater          bool

	// Cross-tick latency tracking. PingTimes is a ring buffer
	// indexed by NumPings modulo NumPingTimes. tyrquake: pings
	// recorded by CL_ParseUpdate.
	PingTimes [NumPingTimes]float32
	NumPings  int
	Frags     [16]int

	// Message is the per-client transport buffer (the client's
	// outgoing reliable channel to the server). Allocated by
	// [NewState] and preserved across [State.Clear] /
	// [State.Disconnect]; only its contents are wiped.
	Message *sizebuf.Buffer

	// Baselines is the per-entity snapshot cache the [Apply] arm for
	// [DecodedBaseline] fills as the server's signon-time
	// svc_spawnbaseline broadcast streams in. Keyed by entity index
	// (the same int the wire message carries). The map is allocated
	// by [NewState] so the first DecodedBaseline arm never has to
	// nil-check; [State.Clear] resets it to a fresh empty map so per-
	// map state doesn't leak across SV_SpawnServer.
	//
	// Renderer integration (per-tic svc_update lerping, model dispatch,
	// PVS culling) reads this map -- the current bring-up just proves
	// the broadcast lands by counting Baselines after the handshake
	// drain.
	Baselines map[int]EntityBaseline

	// Entities is the live per-tic state cache the [Apply] arm for
	// [DecodedUpdate] mutates. Keyed by entity index (matches
	// Baselines). On the first DecodedUpdate for an entity the arm
	// seeds the entry from Baselines[entNum] (so omitted fields keep
	// their last-known-good value), then overlays the U_*-bit-flagged
	// fields from the update message. tyrquake: the cl_entities[]
	// table's per-entity origin/angles/frame/skin slots mutated
	// inside CL_ParseUpdate.
	//
	// Like Baselines, the map is allocated by [NewState] + reset by
	// [State.Clear] so per-map state doesn't leak.
	Entities map[int]EntityState

	// EmitParticles is the optional sink that the [Apply] arm for
	// [DecodedParticle] dispatches into, mirroring tyrquake's
	// svc_particle handler that calls R_RunParticleEffect. nil is a
	// silent no-op (the historical bring-up behaviour). The embedder
	// wires this to render.Pool.Emit -- the client package itself
	// MUST stay free of any render-layer dependency so the wire
	// decoder + state machine remain testable in isolation, hence
	// the callback indirection.
	//
	// Signature mirrors PF_particle / R_RunParticleEffect:
	//
	//	origin -- world-space anchor for the burst
	//	dir    -- per-axis velocity bias the renderer adds onto its
	//	          random per-particle jitter
	//	color  -- palette base index (renderer keeps top 5 bits,
	//	          jitters the low 3 per particle)
	//	count  -- number of particles to spawn
	EmitParticles func(origin, dir [3]float32, color, count int)

	// EmitTempEntity is the optional sink the [Apply] arm for the
	// svc_temp_entity point-effect family (TE_Spike, TE_SuperSpike,
	// TE_Gunshot, TE_Explosion, TE_TarExplosion, TE_WizSpike,
	// TE_KnightSpike, TE_LavaSplash, TE_Teleport) dispatches into.
	// nil is a silent no-op so callers that don't yet wire it keep
	// the historical bring-up behaviour (the temp-entity decoder
	// landed before the particle pool did; this arm was previously
	// documented as "particle pool + sound mixer -- separate layers"
	// in the apply.go comment block).
	//
	// Bridges into render.Pool.Emit / Pool.ParticleExplosion /
	// Pool.LavaSplash / Pool.TeleportSplash depending on the Kind:
	// the embedder's closure switches on Kind and picks the right
	// pool method. The client package stays render-agnostic.
	//
	// Signature: (kind = the TE_* sub-type byte, origin = the wire-
	// reported world-space coord).
	EmitTempEntity func(kind int, origin [3]float32)

	// CenterPrintText is the latest svc_centerprint payload the
	// renderer should overlay horizontally-centered near the top of
	// the screen. tyrquake: scr_centerstring + scr_centertime_off in
	// screen.c -- Quake renders it via SCR_CheckDrawCenterString and
	// fades it out after [CenterPrintLifetime] seconds; the Go port
	// stashes the string on State + the renderer reads + clears it
	// when CenterPrintExpiry < now.
	//
	// Empty string = nothing to render. Non-empty + CenterPrintExpiry
	// in the future = draw centered. CenterPrintExpiry <= current
	// MsgTime = stale; renderer treats it as empty.
	CenterPrintText string

	// CenterPrintExpiry is the MsgTime at which the active centerprint
	// stops being drawn. Set by the [Apply] arm for [DecodedCenterPrint]
	// to nowSec + [CenterPrintLifetime]. Zero = no active centerprint
	// (matches a freshly-zeroed State; the renderer's "expiry <= now"
	// guard makes this equivalent to "no draw").
	CenterPrintExpiry float32

	// Intermission flips true on receipt of svc_intermission /
	// svc_finale. The renderer hides the in-game HUD and overlays
	// the end-of-level scoreboard (or the finale credits text)
	// instead. The flag stays set until the next svc_serverinfo
	// (= map change tearing the client back to first-spawn) clears
	// it via [State.Clear].
	//
	// tyrquake: cl.intermission in NQ/client.h. The C upstream
	// stores 1 (svc_intermission), 2 (svc_finale "end of episode"
	// credits), or 3 (cutscene); the Go port collapses the kind
	// distinction into IntermissionText (empty = scoreboard mode,
	// non-empty = finale-style centered text block) since the only
	// observable difference is "show stats" vs "show text".
	Intermission bool

	// IntermissionText is the multi-line credits payload from
	// svc_finale (and the lone-line "end of cutscene" payload from
	// svc_cutscene if that arm is ever wired). Empty means the
	// intermission is the scoreboard-only end-of-level kind
	// (svc_intermission) and the renderer composes its text from
	// the cached stat bank (StatTotalSecrets / StatSecrets /
	// StatTotalMonsters / StatMonsters + Time).
	IntermissionText string

	// IntermissionTime is the nowSec the [Apply] arm stamped when
	// the intermission was triggered (the value passed to Apply).
	// The renderer reads it to compute "time elapsed since
	// intermission started" for the centered scoreboard's "time:
	// MM:SS" line; the embedder's per-button advance-trigger logic
	// reads it to enforce the "any button after IntermissionTime +
	// 2s closes the intermission" rule. tyrquake: cl.completed_time.
	IntermissionTime float32

	// MusicTrack is the currently-selected music track index broadcast
	// by the server via svc_cdtrack. The [Apply] arm for
	// [DecodedCDTrack] writes both this field and MusicLoopTrack; the
	// embedder's per-tic music driver polls them (see
	// [State.MusicEpoch]) and (re-)opens the matching "music/trackXX.ogg"
	// asset whenever the wire-broadcast track changes. 0 = silence;
	// 1..255 = the per-map score index (1 = title music; the per-map
	// indices follow the upstream Q1 retail soundtrack convention,
	// where audio-CD tracks 2..11 corresponded to the 10 in-game
	// score loops).
	//
	// tyrquake: cl.cdtrack in NQ/client.h, written by the svc_cdtrack
	// arm of CL_ParseServerMessage. The Go port keeps the same wire
	// shape and the same field name shape; only the playback backend
	// changes (.ogg streamer instead of CD-DA hardware).
	MusicTrack int

	// MusicLoopTrack is the fallback track the music streamer switches
	// to when MusicTrack reaches EOF. Wired by [DecodedCDTrack]'s
	// Apply arm alongside MusicTrack. 0 = stop at EOF; non-zero =
	// re-open and stream that track (typically == MusicTrack for a
	// self-looping background score).
	//
	// tyrquake: cl.looptrack in NQ/client.h, written alongside cdtrack.
	MusicLoopTrack int

	// MusicEpoch is a monotonically-increasing counter the Apply arm
	// for [DecodedCDTrack] bumps every time it writes a new
	// (MusicTrack, MusicLoopTrack) pair. The embedder's per-tic music
	// driver tracks the last value it observed and triggers a
	// (re-)open whenever the epoch changes, which makes the
	// "same wire byte arrives twice" case (a server retransmit of
	// the same track) correctly idempotent: the epoch advances but
	// the open path's caller can deduplicate by comparing
	// (MusicTrack, MusicLoopTrack) themselves.
	//
	// Bumped even when the new pair is identical to the previous one
	// so a server-side restart-the-music intent is preserved on the
	// wire-driven hand-off; the receiving side decides what
	// "epoch advanced but identical pair" means for its mixer.
	MusicEpoch uint64

	// EmitBeam is the optional sink the [Apply] arm for the
	// svc_temp_entity lightning family (TE_Lightning1 / TE_Lightning2 /
	// TE_Lightning3 / TE_Beam) dispatches into. nil is a silent no-op
	// so callers that don't yet wire it keep the historical "lightning
	// is a stub" behaviour. tyrquake: CL_ParseBeam in cl_tent.c (the
	// per-kind switch that picks bolt1/bolt2/bolt3.mdl + queues the
	// per-tic CL_UpdateBeams pass).
	//
	// Bridges into the client-side [BeamPool] (or the embedder's own
	// per-frame renderer) via:
	//
	//	kind  -- TE_* sub-type byte (TELightning1 / 2 / 3 / TEBeam)
	//	ent   -- owning entity index (the player's or boss's slot)
	//	start -- traceline source (the owner's muzzle)
	//	end   -- traceline endpoint (impact / max range)
	//
	// The client package stays render-agnostic; the embedder's closure
	// switches on kind, looks up the matching bolt mdl, and either
	// spawns a [Beam] in a [BeamPool] (the canonical client-side path)
	// or hands the segments to a custom renderer.
	EmitBeam func(kind, ent int, start, end [3]float32)
}

// ErrAlreadyConnected is returned by [State.SetConnecting] when
// the state is not in [StateDisconnected].
var ErrAlreadyConnected = errors.New("client: not in StateDisconnected")

// ErrNotConnecting is returned by [State.MarkSpawned] when the
// state is not in [StateConnecting].
var ErrNotConnecting = errors.New("client: not in StateConnecting")

// NewState returns a fresh client state with the transport buffer
// pre-allocated to [MaxClientMessage]. Connection is
// [StateDisconnected]; Spawned is false; all numeric fields are
// zero.
func NewState() *State {
	return &State{
		Message:   sizebuf.New(make([]byte, MaxClientMessage)),
		Baselines: make(map[int]EntityBaseline),
		Entities:  make(map[int]EntityState),
	}
}

// Clear resets the per-map state without freeing the transport
// buffer. Equivalent to tyrquake's CL_ClearState (called at map
// change / disconnect): wipes the cl struct but keeps cls.message
// alive.
//
// Connection state is NOT changed -- the upstream's CL_ClearState
// is independent of the cls.state transition (CL_Disconnect calls
// Clear AFTER flipping cls.state to ca_disconnected, but spawning
// a fresh map calls Clear without touching cls.state).
func (s *State) Clear() {
	buf := s.Message
	buf.Clear()
	// Preserve MusicEpoch across the wipe so the embedder's per-tic
	// music driver keeps seeing strictly-increasing values; the
	// per-map MusicTrack / MusicLoopTrack themselves SHOULD reset to
	// 0 (a new map starts silent until the server emits svc_cdtrack),
	// but the monotonic counter the driver compares against MUST NOT
	// regress on a map change or a stale "track 0 already handled"
	// observation would silently lose the next emission.
	epoch := s.MusicEpoch
	*s = State{
		Connection: s.Connection,
		Spawned:    s.Spawned,
		SentSpawn:  s.SentSpawn,
		Message:    buf,
		Baselines:  make(map[int]EntityBaseline),
		Entities:   make(map[int]EntityState),
		MusicEpoch: epoch,
	}
}

// Disconnect transitions to [StateDisconnected] and clears
// per-map state via [State.Clear]. Idempotent (calling on an
// already-disconnected state simply re-clears, mirroring
// tyrquake's CL_Disconnect which always runs the cl-wipe path
// regardless of prior cls.state).
//
// tyrquake: CL_Disconnect in NQ/cl_main.c.
func (s *State) Disconnect() {
	s.Connection = StateDisconnected
	s.Spawned = false
	s.SentSpawn = false
	s.Clear()
}

// SetConnecting transitions from [StateDisconnected] to
// [StateConnecting]. Returns [ErrAlreadyConnected] if the state
// is not currently disconnected.
//
// tyrquake: cls.state = ca_connected at the end of
// CL_EstablishConnection.
func (s *State) SetConnecting() error {
	if s.Connection != StateDisconnected {
		return ErrAlreadyConnected
	}
	s.Connection = StateConnecting
	return nil
}

// MarkSpawned transitions to [StateConnected] and flips Spawned
// true. Returns [ErrNotConnecting] if the state is not in
// [StateConnecting] (i.e. caller skipped the handshake or already
// completed it).
//
// tyrquake: cls.state = ca_active at the end of CL_SignonReply
// stage 4.
func (s *State) MarkSpawned() error {
	if s.Connection != StateConnecting {
		return ErrNotConnecting
	}
	s.Connection = StateConnected
	s.Spawned = true
	return nil
}

// RecordPing appends a latency sample to the rolling window. The
// ring-buffer index is NumPings mod [NumPingTimes]; NumPings is
// incremented unconditionally so callers can use it as a
// monotonic counter (the actual sample slot is computed via the
// modulo).
//
// tyrquake: cl.ping_times[host_client->num_pings & (NumPingTimes
// - 1)] in CL_ParseUpdate.
func (s *State) RecordPing(latency float32) {
	s.PingTimes[s.NumPings%NumPingTimes] = latency
	s.NumPings++
}
