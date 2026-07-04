// Copyright (c) 1996-1997 Id Software, Inc.
// Copyright (c) 2026 the go-quake1/engine authors.
// SPDX-License-Identifier: GPL-2.0-or-later

package host

import (
	"errors"
	"fmt"
	"time"

	"github.com/go-quake1/engine/model"
	"github.com/go-quake1/engine/progs"
	"github.com/go-quake1/engine/server"
	"github.com/go-quake1/engine/sound"
	"github.com/go-quake1/engine/world"
)

// readClientMoves is the package-level seam Frame() walks to drain
// each active client's inbox before RunPhysics dispatches PhysicsWalk.
// The default is [server.ReadClientMoves]; tests swap it via the
// runClientCmds path (see Frame's hookable form below) so the cmd-
// drain step can be exercised without a real NetConn.
var readClientMoves = server.ReadClientMoves

// Host is the main game-loop coordinator. Owns the Server + Static
// + VM + World + per-frame timing + the ThinkCaller bridge.
// tyrquake: the global host_* variables in NQ/host.c (host_client,
// host_server, host_static, host_frametime, host_time, etc.).
//
// A zero Host is not usable; build one via [NewHost] which validates
// the required dependencies and pre-allocates the Server / Static
// / World pools at the configured client count.
type Host struct {
	Server   *server.Server
	Static   *server.Static
	VM       *progs.VM
	World    *world.World
	Cache    *model.Cache
	Resolver server.FileResolver

	// FrameTime is sv.frametime per tic (seconds; 0.05 = 20Hz).
	// Carried so [Host.Frame] callers can pass it as the dt value
	// when they have no per-frame wall-clock measurement of their
	// own. tyrquake: host_frametime.
	FrameTime float32

	// NowFn is the wall-clock the per-tic loop reads to advance
	// sv.time. nil falls back to a time.Now-based default.
	// tyrquake: Sys_FloatTime.
	NowFn func() float64

	// progsRef is the Progs pointer the bridge consults. Set via
	// [Host.SetProgs] -- the VM does not expose its bound Progs so
	// the host has to be told. nil means the bridge skips the
	// named-global hand-off (the per-think dispatch still runs).
	progsRef *progs.Progs

	// interner is the StringInterner SpawnServer hands to the
	// entity-spawn pass. Defaults to a noop returning offset 0
	// (the empty-string sentinel); production embedders override
	// via [Host.SetInterner] once a real progs.StringInterner exists.
	interner progs.StringInterner

	// spawnFn is the per-entity QC spawn-dispatch hook SpawnServer
	// hands to the entity-spawn pass. nil = entities still get their
	// fields populated from the BSP entity lump but no per-classname
	// QC initialiser runs (no monster health, no model, no solid).
	// Wired by the embedder via [Host.SetSpawnFn] once the VM has
	// a builtin table large enough for the spawn-time builtins
	// (precache_*, setmodel, setorigin, ...) the QC code calls.
	spawnFn func(ent *progs.Edict, classname string)

	// onArenaReady is the per-SpawnServer arena-publication hook.
	// SpawnServer calls it once, AFTER the EdictArena is allocated
	// + BEFORE the entity-spawn pass runs. Wired by the embedder
	// via [Host.SetOnArenaReady]; the production callback is
	// vm.SetArena so the per-entity SpawnFn dispatches see live
	// entity-pointer opcodes. nil = arena is still published on
	// Server.Arena but not handed to the VM mid-SpawnServer.
	onArenaReady func(arena *progs.EdictArena)

	// LastEntityUpdatesSent is the cumulative count of svc_update
	// messages the most recent [Host.Frame] call appended to client
	// Message buffers, summed across every active+spawned client.
	// Exposed for bring-up instrumentation (the quake-tamago main
	// reads it per 60 frames to log update flow); not used by the
	// per-tic loop itself.
	LastEntityUpdatesSent int

	// LastThinksDispatched is the number of QC think functions the
	// most recent [Host.Frame] call fired through the top-level
	// [Host.runThink] walker (the SV_RunThink-equivalent pass that
	// runs BEFORE RunPhysics so MOVETYPE_PUSH / WALK / STEP / NONE+
	// SolidNot edicts -- which the per-MOVETYPE physics dispatcher
	// silent-skips -- still get their per-tic think drive). Counts
	// only the walker's own dispatches; the per-handler RunThink
	// calls inside the physics dispatch path are not counted here.
	// Exposed for bring-up instrumentation (quake-tamago logs it per
	// 60 frames so the serial output proves monster animation is
	// wired); not used by the per-tic loop itself.
	LastThinksDispatched int

	// LastThinkErrors is the count of think-dispatch errors the most
	// recent [Host.Frame] call's runThink walker swallowed (logged +
	// continued rather than aborting the frame). A QC function that
	// calls a builtin the host has not stubbed yet (W_FireRifle,
	// ai_walk, ...) surfaces as one of these. Exposed for bring-up
	// instrumentation alongside LastThinksDispatched.
	LastThinkErrors int

	// LastPlayerPostThinks is the number of PlayerPostThink dispatches the
	// most recent Frame ran (0 = PlayerPostThink absent or no active client).
	LastPlayerPostThinks int

	// LastThinkErrorMsgs accumulates the first 8 unique error strings
	// the most recent [Host.Frame] call's runThink walker swallowed,
	// in arrival order. Reset (re-allocated) at the top of every Frame
	// alongside LastThinksDispatched / LastThinkErrors. Exposed for
	// bring-up instrumentation so the per-tic log can name the missing
	// builtin (or other failure) instead of just a count. The 8-entry
	// cap bounds the cost of de-dup + log emission per tic.
	LastThinkErrorMsgs []string

	// Sounds is the parallel-to-Server.SoundPrecache slice of parsed
	// *sound.Sample blobs. Populated by [Host.PrecacheSound]; consumed
	// by [Host.StartSound] / [Host.AmbientSound] to feed the mixer
	// without re-parsing the WAV body. Index 0 is the empty-string
	// sentinel slot (always nil); index >= 1 maps onto Server.SoundPrecache.
	Sounds []*sound.Sample

	// LastSoundsStarted is the cumulative count of successful
	// [Host.StartSound] calls; bumped per per-tic sound-firing builtin.
	// Exposed for bring-up instrumentation (quake-tamago logs the delta
	// per 60-tic cadence so the serial output proves the QC builtin
	// reached the mixer).
	LastSoundsStarted int

	// LastAmbientsStarted is the cumulative count of successful
	// [Host.AmbientSound] calls. Separate from LastSoundsStarted so the
	// instrumentation can distinguish "looped ambient tracks parked at
	// spawn time" from "per-tic gameplay sound triggers".
	LastAmbientsStarted int

	// LastTriggerTouches is the count of QC `.touch` functions the
	// most recent [Host.Frame] call dispatched via [Host.TouchTriggers]
	// (the SV_TouchLinks-equivalent walk that runs once per client
	// edict after RunPhysics). Reset to zero at the top of every Frame.
	// Exposed for bring-up instrumentation -- a non-zero value the
	// moment the demo camera grazes an item proves the QC `.touch`
	// dispatch chain (= item pickup) is wired end-to-end.
	LastTriggerTouches int

	// LastTouchErrors is the count of touch dispatches the most recent
	// [Host.Frame] call's TouchTriggers walk swallowed (logged +
	// continued rather than aborting the frame). A trigger whose QC
	// touch function calls an unstubbed builtin surfaces here.
	LastTouchErrors int

	// LastTouchErrorMsgs accumulates the first 8 unique error strings
	// the most recent [Host.Frame] call's TouchTriggers walk swallowed,
	// in arrival order. Same shape as LastThinkErrorMsgs.
	LastTouchErrorMsgs []string

	// PendingChangelevel is set by the QC `changelevel(mapname)` builtin
	// (= [BuiltinChangeLevel]) and polled by the embedder's main loop
	// to know "the QC asked us to re-spawn into NextMap". The host
	// itself does NOT re-spawn -- it has no way to know the embedder's
	// post-SpawnServer wiring (sound pool, sound loader, OnArenaReady
	// hook). The embedder reads + clears the flag, then drives the
	// re-spawn via Host.SpawnServer + whatever per-map state it needs
	// to refresh. tyrquake: pr_global_struct->nextmap + the
	// SV_SaveSpawnparms + Host_Reconnect_f path that re-enters the
	// new map at the top of host_frame.
	//
	// Reset semantics: the host never clears this on its own. The
	// embedder MUST clear it after acting; otherwise the next Frame()
	// will see it still set and the level-reload print log emits
	// every tic. Frame() itself ignores the flag (= no behavioural
	// change inside the host); the embedder polls it post-Frame.
	PendingChangelevel bool

	// NextMap is the bare map-slug the most recent `changelevel` QC
	// call requested (the embedder's main loop combines it with the
	// "maps/<slug>.bsp" expansion that SpawnServer does internally).
	// Stable while PendingChangelevel is true; undefined otherwise.
	NextMap string

	// LastIntermissionStats is the cumulative per-map progression
	// tally [Host.EmitIntermission] pushes to every client as
	// svc_updatestat slots ahead of the svc_intermission marker.
	// Embedders populate it (typically from named-global QC reads of
	// `total_secrets` / `found_secrets` / `total_monsters` /
	// `killed_monsters`) before calling EmitIntermission; the
	// zero value is a tolerated fallback (the scoreboard renders
	// "SECRETS: 0 / 0" / "MONSTERS: 0 / 0" instead of garbage).
	LastIntermissionStats IntermissionStats

	// soundPool is the mixer pool installed via [Host.SetSoundPool].
	// nil = audio path silent-no-ops (the runloop owns its own pool;
	// the host needs a reference so the QC-driven StartSound builtin
	// can park samples on the SAME pool the runloop's Paint walks).
	soundPool *sound.Pool

	// soundLoader is the WAV blob lookup installed via
	// [Host.SetSoundLoader]. nil = PrecacheSound surfaces the no-loader
	// path (ErrSoundLoadFailed). Decoupled from the host so the package
	// stays free of any `pak` / `vfs` dependency.
	soundLoader SoundLoader

	// listenerSet records whether a per-tic listener context has been
	// wired via [Host.SetListener]. Without one, the spatialize-then-
	// allocate path in [Host.StartSoundAt] / [Host.AmbientSoundAt]
	// degrades to the existing "full master volume on both ears"
	// behaviour (= no stereo balance, no distance falloff). Tests +
	// pre-signon embedders that never call SetListener get the
	// unspatialized path verbatim; production quake-tamago calls
	// SetListener every tic with the camera origin + right axis.
	listenerSet    bool
	listenerOrigin [3]float32
	listenerRight  [3]float32

	// SaveSkill is the active difficulty rung the most recent
	// menu-driven skill confirm wrote. SaveSlot persists it into
	// the save header; LoadSlot restores it. Defaults to 1 (Normal)
	// to match the upstream `skill` cvar default.
	//
	// Exposed for the runloop's menu hook (it copies the post-confirm
	// menu.SkillLevel into here via [Host.SetSkill]).
	SaveSkill int
}

// ErrNilDep fires on a missing required NewHost dependency.
var ErrNilDep = errors.New("host: NewHost requires non-nil VM, Cache, Resolver")

// DefaultFrameTime is the per-tic interval the Go port defaults to
// when the caller doesn't override Host.FrameTime. 0.05 = 20Hz,
// matching tyrquake's sys_ticrate default.
const DefaultFrameTime float32 = 0.05

// defaultNowFn returns wall-clock seconds since the Unix epoch as a
// float64, the same shape the upstream's Sys_FloatTime exposes.
func defaultNowFn() float64 {
	return float64(time.Now().UnixNano()) / 1e9
}

// NewHost wires up a fresh Host with the given dependencies.
// maxClients controls the [server.Static] pool size; values <= 0
// fall back to 1 (a single local-client loop is the minimum
// useful configuration).
//
// Returns ErrNilDep on missing required deps.
func NewHost(vm *progs.VM, cache *model.Cache, resolver server.FileResolver, maxClients int) (*Host, error) {
	if vm == nil || cache == nil || resolver == nil {
		return nil, ErrNilDep
	}
	if maxClients <= 0 {
		maxClients = 1
	}
	return &Host{
		Server:    server.NewServer(),
		Static:    server.NewStatic(maxClients),
		VM:        vm,
		World:     world.New(),
		Cache:     cache,
		Resolver:  resolver,
		FrameTime: DefaultFrameTime,
		NowFn:     defaultNowFn,
		interner:  func(string) int32 { return 0 },
	}, nil
}

// SetInterner overrides the [progs.StringInterner] the host hands
// to [server.SpawnServer]'s entity-spawn pass. The default is a
// noop that interns every string to offset 0; callers wire a real
// interner once their progs's string table is appendable.
func (h *Host) SetInterner(intern progs.StringInterner) {
	h.interner = intern
}

// SetSpawnFn installs the per-entity QC spawn-dispatch hook that
// [server.SpawnServer] hands to the entity-spawn pass. The hook is
// invoked once per parsed entity (after AssignFields has populated
// the edict's entvars) with the edict + its "classname" field value;
// the production implementation typically resolves classname via
// [progs.Progs.FindFunction], sets the QC "self" global to point at
// ent, and calls [progs.VM.Run] on the function index.
//
// nil disables the dispatch -- entities still parse + assign but no
// QC initialiser runs. The default for a fresh Host is nil since the
// dispatch needs a builtin table the embedder owns (host.go can't
// pre-wire it without growing a dependency on every progs builtin
// the spawn-time QC code calls).
func (h *Host) SetSpawnFn(fn func(ent *progs.Edict, classname string)) {
	h.spawnFn = fn
}

// SetOnArenaReady installs the arena-publication hook
// [server.SpawnServer] fires once per map load, right after the
// per-map [progs.EdictArena] is allocated + reset and BEFORE the
// entity-spawn pass dispatches the first SpawnFn. Production
// embedders pass [progs.VM.SetArena] (or a closure wrapping it)
// so the VM has the arena handle the entity-pointer opcodes
// (OP_ADDRESS / OP_LOAD_ENT / OP_STORE_P_*) need to resolve
// self.field = X writes inside spawn-time QC.
//
// nil disables the hook; the arena is still stashed on
// Server.Arena so callers can pick it up post-SpawnServer.
func (h *Host) SetOnArenaReady(fn func(arena *progs.EdictArena)) {
	h.onArenaReady = fn
}

// edictAt is the [world.PhysicsEdictResolver] the per-tic RunPhysics
// dispatcher walks. Returns nil for any out-of-range index (the
// dispatcher treats nil as "free slot, skip").
func (h *Host) edictAt(i int) *progs.Edict {
	if i < 0 || i >= len(h.Server.Edicts) {
		return nil
	}
	return h.Server.Edicts[i]
}

// cmdAt is the [world.PhysicsCmdResolver] the per-tic dispatcher
// passes through to the (future) PhysicsWalk handler. Client slots
// are 1..MaxClients (slot 0 is the world); any other index returns
// a zero UserCmd, matching the C upstream's per-non-client
// MOVETYPE_WALK guard (which simply doesn't dispatch Walk for
// non-client entities).
func (h *Host) cmdAt(i int) server.UserCmd {
	if i < 1 || i > h.Static.MaxClients {
		return server.UserCmd{}
	}
	c := h.Static.Clients[i-1]
	if c == nil {
		return server.UserCmd{}
	}
	return c.Cmd
}

// hostKeyAt is the [world.PhysicsKeyResolver] the dispatcher uses:
// edict slot index == area-tree Key (the canonical identity
// mapping; future maps that re-key clients vs monsters can swap
// this for a non-identity function).
func hostKeyAt(i int) world.Key { return world.Key(i) }

// runClientCmds drains every active client's inbox via ReadClientMoves
// then copies the resulting Cmd.ViewAngles into the bound edict's
// `v_angle` entvars field plus the per-tic trigger-button bits
// (button0 = +attack, button2 = +jump) and the +impulse byte.
// tyrquake: SV_RunClients in NQ/host.c -- the per-tic
// SV_ReadClientMessage + SV_ClientThink call pair that runs just
// before SV_Physics so PhysicsWalk sees the freshest UserCmd + view
// angles, and so the per-tic PlayerPostThink QC dispatch sees a
// fresh self.button0 / self.button2 / self.impulse the W_WeaponFrame
// + ImpulseCommands chain reads.
//
// The v_angle copy is the minimal SV_RunCmd equivalent for movement:
// PhysicsWalk's CalcWishVel reads v_angle (NOT the cmd's ViewAngles)
// for the forward/right basis, so without this propagation the
// player's wishvel would always lock to v_angle's last value (zero
// at spawn).
//
// The button0 / button2 / impulse propagation is the minimal
// SV_RunCmd equivalent for triggers: PlayerPostThink reads
// self.button0 inside W_WeaponFrame ("if (self.button0) W_Attack();")
// and self.impulse inside ImpulseCommands ("ImpulseCommands();" at
// the top of PlayerPostThink). Without it, +attack on the client
// flips the on-wire `buttons` bit but the QC dispatch sees zero on
// the edict's field, so W_Attack never runs and no shot is fired.
//
// Slots without an Edict yet (ConnectClient ran but the edict pool
// wasn't ready) and slots without a given field (test stubs with
// stripped progs) are skipped silently per field. Other errors -- a
// transport failure, a bad clc opcode -- are surfaced verbatim so
// callers can log + drop.
func (h *Host) runClientCmds() error {
	p := h.findProgs()
	for _, c := range h.Static.Clients {
		if c == nil || !c.Active || c.NetConnection == nil {
			continue
		}
		if _, err := readClientMoves(c); err != nil {
			return err
		}
		if p == nil || c.Edict == nil {
			continue
		}
		// NewEntVars's nil-arg guard is unreachable here -- the p and
		// c.Edict checks above ensure both inputs are non-nil -- so its
		// error is dropped (bsptrace-style).
		ev, _ := progs.NewEntVars(p, c.Edict)
		// v_angle absent (stripped test progs) -> silent skip. A real
		// Q1 progs.dat always declares it.
		_ = ev.WriteVec3("v_angle", c.Cmd.ViewAngles)
		// button0 (=ButtonAttack), button2 (=ButtonJump), impulse:
		// per-tic trigger bits the QC reads via self.button0 /
		// self.button2 / self.impulse. Stripped test progs without
		// these fields silent-skip; real Q1 progs.dat always
		// declares them as EvFloat.
		_ = ev.WriteFloat("button0", boolToFloat(c.Cmd.Buttons&server.ButtonAttack != 0))
		_ = ev.WriteFloat("button2", boolToFloat(c.Cmd.Buttons&server.ButtonJump != 0))
		_ = ev.WriteFloat("impulse", float32(c.Cmd.Impulse))
	}
	return nil
}

// boolToFloat is the inline "0 or 1" helper the button-bit
// propagation in [Host.runClientCmds] uses to flatten a bitmask test
// into the EvFloat value QC reads. tyrquake encodes per-tic buttons
// as floats on the entvars (button0 / button1 / button2) even though
// the on-wire shape is a single byte bitmask -- the QC compiler has
// no integer type, so the bit-to-float widen happens at the server-
// to-VM boundary.
func boolToFloat(b bool) float32 {
	if b {
		return 1
	}
	return 0
}

// runThink is the per-tic SV_RunThink-equivalent walker. Iterates
// every edict in [1, NumEdicts) (slot 0 is the world; the per-tic
// SV_Physics loop also skips it). For each non-free edict whose
// .nextthink is > 0 AND <= sv.time, clears .nextthink to 0, sets the
// QC self / other globals via the existing thinkCaller bridge, and
// invokes the QC function indexed by .think. tyrquake: the per-edict
// SV_RunThink calls inside the per-MOVETYPE SV_Physics_* handlers,
// hoisted to the top of the per-tic loop so MOVETYPEs the Go port
// has no handler for yet (PUSH / WALK / STEP) and the
// None+SolidNot free-entity case still get their think drive.
//
// Errors from thinkCaller (typically: the QC function called a
// builtin the host has not stubbed yet) are tallied into
// LastThinkErrors and swallowed -- one monster's missing-builtin
// crash does NOT take down the rest of the frame. Successful
// dispatches are tallied into LastThinksDispatched.
//
// Counters reset to zero at the top of every Frame call so the
// per-60 instrumentation log in quake-tamago shows per-tic activity
// rather than cumulative-since-boot totals.
//
// Pre-conditions silently handled (no error surfaced):
//
//   - h.progsRef nil          -> the bridge skips named-global writes
//     but vm.Run still dispatches; the field
//     lookups below need progs.NewEntVars so
//     a nil progsRef short-circuits the
//     whole walk (no fields to read).
//   - ent nil OR ent.Free     -> per-slot skip (matches the C
//     upstream's `if (ent->free) continue`).
//   - nextthink field absent  -> per-slot skip (test stubs with
//     stripped progs; real Q1 progs.dat
//     always declares it).
//   - think field absent      -> per-slot skip (same rationale).
//   - nextthink <= 0          -> per-slot skip (no think scheduled).
//   - nextthink > sv.time     -> per-slot skip (think is in the
//     future); matches RunThink's
//     deadline check.
func (h *Host) runThink() {
	h.LastThinksDispatched = 0
	h.LastThinkErrors = 0
	h.LastThinkErrorMsgs = h.LastThinkErrorMsgs[:0]
	p := h.findProgs()
	if p == nil {
		return
	}
	// Walk slots [1, NumEdicts); slot 0 is the world entity. Bound
	// the upper limit by len(h.Server.Edicts) defensively so a
	// NumEdicts past the allocated pool can't index out of range.
	n := h.Server.NumEdicts
	if n > len(h.Server.Edicts) {
		n = len(h.Server.Edicts)
	}
	now := float32(h.Server.Time)
	for i := 1; i < n; i++ {
		ent := h.Server.Edicts[i]
		if ent == nil || ent.Free {
			continue
		}
		// NewEntVars only errors on nil-arg; both guards above
		// (ent != nil + the p != nil short-circuit at the head)
		// ensure both inputs are non-nil so the error is dropped
		// bsptrace-style.
		ev, _ := progs.NewEntVars(p, ent)
		thinktime, err := ev.ReadFloat("nextthink")
		if err != nil {
			continue
		}
		if thinktime <= 0 || thinktime > now {
			continue
		}
		funcID, err := ev.ReadInt32("think")
		if err != nil {
			continue
		}
		// Clear nextthink BEFORE dispatch so a think that re-arms
		// itself (writes ent.nextthink = now + delay) survives the
		// clear. The preceding ReadFloat proved the field exists as
		// EvFloat, so WriteFloat's error branch is unreachable here
		// and is dropped bsptrace-style.
		_ = ev.WriteFloat("nextthink", 0)
		if err := h.thinkCaller(ent, funcID); err != nil {
			h.LastThinkErrors++
			// Capture up to 8 unique error strings per Frame so the
			// caller's per-tic log can name the failure (typically:
			// missing-builtin index) instead of just counting. Prefix
			// the funcID so two distinct think functions that fail on
			// the same underlying err (e.g. both miss the same builtin
			// slot) still show up as separate diagnostic lines.
			// De-dup + cap bounds the cost.
			msg := fmt.Sprintf("think funcID=%d: %v", funcID, err)
			if len(h.LastThinkErrorMsgs) < 8 {
				seen := false
				for _, prev := range h.LastThinkErrorMsgs {
					if prev == msg {
						seen = true
						break
					}
				}
				if !seen {
					h.LastThinkErrorMsgs = append(h.LastThinkErrorMsgs, msg)
				}
			}
			continue
		}
		h.LastThinksDispatched++
	}
}

// Frame runs one game tic: per-tic SV_Physics + SendClientFrames +
// CleanupEnts + ClearDatagram + ClearReliableDatagram. tyrquake:
// Host_Frame's server-side portion (the SV_Physics + SV_SendClient-
// Messages + SV_CleanupEnts + SV_ClearDatagram block in NQ/host.c).
//
// Caller passes the current frame-time delta in seconds (typically
// [Host.FrameTime]). The host advances sv.time by dt before the
// physics pass so per-tic think deadlines align with the new tic.
//
// On a server that has not yet been SpawnServer'd (s.Active == false),
// Frame is a no-op -- there is no per-tic work without a map loaded.
//
// Returns the first error from any step; nil on a clean frame.
func (h *Host) Frame(dt float32) error {
	if !h.Server.Active {
		return nil
	}

	// Advance sv.time. The C upstream does this at the bottom of
	// Host_Frame after SV_Physics; the Go port does it up-front so
	// per-tic think deadlines (RunThink's now+dt window) align with
	// the tic the dispatched code sees. The semantic difference is
	// nil for a single-tic test: every think scheduled this frame
	// fires this frame either way.
	h.Server.Time += float64(dt)

	// SV_RunClients-equivalent: drain each active client's inbox into
	// its Client.Cmd, then mirror Cmd.ViewAngles into the bound edict's
	// v_angle field so PhysicsWalk's wishvel basis is fresh this tic.
	// Runs BEFORE RunPhysics so PhysicsWalk picks up the post-drain
	// cmd via cmdAt.
	if err := h.runClientCmds(); err != nil {
		return err
	}

	// SV_RunThink-equivalent top-level pass. The per-MOVETYPE physics
	// handlers each call server.RunThink internally for the slots they
	// own, but the dispatcher silent-skips MOVETYPE_PUSH / WALK / STEP
	// (no handler yet) plus the (None && SolidNot) free-entity case.
	// Without a top-level walker those slots' .nextthink deadlines
	// would never fire -- which kills monster animation (the QC
	// monster_*_stand / walk1 chain re-arms ent.nextthink + writes
	// ent.frame on every think). The walker fires think for ANY edict
	// with a scheduled nextthink; double-walking a slot the physics
	// dispatcher will also RunThink is safe because the first call
	// clears nextthink to 0 and the second sees the cleared field as
	// "nothing scheduled".
	h.runThink()

	ctx := world.PhysicsContext{
		Worldmodel:  h.Server.WorldModel,
		Candidates:  nil,
		Now:         float32(h.Server.Time),
		Dt:          dt,
		ThinkCaller: h.thinkCaller,
	}
	if err := world.RunPhysics(
		h.Server.NumEdicts,
		server.DefaultPhysParams(),
		ctx,
		h.edictAt,
		h.cmdAt,
		hostKeyAt,
		h.findProgs(),
	); err != nil {
		return err
	}

	// PlayerPostThink (weapon fire): the post-move half of
	// SV_Physics_Client. Runs after the physics move so W_Attack's
	// traceline sees the player's final position this tic, and before the
	// trigger/broadcast phase so any damage it deals is reflected in the
	// outgoing entity updates.
	h.runPlayerPostThink()

	// SV_TouchLinks-equivalent: per-client per-tic walk the area
	// tree for SOLID_TRIGGER edicts whose absbounds overlap the
	// player edict, dispatching each trigger's QC `.touch` function
	// with self=trigger, other=player. This is the half of the
	// upstream SV_Physics_Client that delivers item pickup (the
	// item_*_touch QC functions write self.ammo_* / self.health onto
	// the player, then setmodel("") + setorigin(item, -8000) to
	// remove the item).
	//
	// Runs AFTER RunPhysics so the player edict's post-walk position
	// is what the trigger query sees (matches the upstream's
	// SV_LinkEdict (which calls SV_TouchLinks internally) firing
	// per per-MOVETYPE handler at the end of its move integration).
	//
	// Walks only client slots [1, MaxClients]: NPCs / monsters /
	// movers don't fire trigger touches in vanilla Quake -- only
	// the player does (the upstream wires SV_LinkEdict's
	// SV_TouchLinks call for every MOVETYPE_WALK move, and only
	// clients use MOVETYPE_WALK). The Go port preserves the same
	// shape by iterating client slots directly.
	h.ResetTouchCounters()
	for slot := 1; slot <= h.Static.MaxClients && slot < len(h.Server.Edicts); slot++ {
		c := h.Static.Clients[slot-1]
		if c == nil || !c.Active || c.Edict == nil {
			continue
		}
		h.TouchTriggers(slot, hostKeyAt(slot))
	}

	// Per-client svc_clientdata compose+encode. Runs BEFORE
	// SendClientFrames so the per-tic player snapshot lands at the
	// head of client.Message, the broadcast datagrams append after,
	// and FlushClientMessage drains the whole thing through the
	// loopback NetConn in one shot.
	//
	// tyrquake: SV_WriteClientdataToMessage inside SV_SendClientDatagram
	// -- per-client, just before the entity/datagram append phase.
	//
	// A nil Progs (test stubs that boot without a bytecode binding)
	// short-circuits WriteClientData internally; the existing
	// SendClientFrames + flush still run for the broadcast datagrams.
	p := h.findProgs()
	for _, c := range h.Static.Clients {
		if err := h.Server.WriteClientData(c, p); err != nil {
			return err
		}
	}

	// Per-client svc_update broadcast. Runs AFTER WriteClientData
	// (so the player-snapshot bytes lead the message) but BEFORE
	// SendClientFrames so the per-entity updates land BEFORE the
	// generic Datagram/ReliableDatagram bytes -- matching the
	// SV_WriteEntitiesToClient -> reliable_datagram/datagram append
	// order inside SV_SendClientDatagram.
	//
	// Bring-up shape: emits a full origin+angles update for every
	// non-free entity past slot 0 -- no PVS culling, no delta
	// bit encoding. The client's apply arm caches the updates in
	// State.Entities. Bandwidth optimization (PVS + delta bits) is
	// a follow-up batch.
	//
	// A nil Progs short-circuits ComposeBaselineFromEdict internally;
	// every read degrades to the QC zero default. The emit path still
	// runs (the wire format doesn't care about which fields were
	// "real" QC data vs zeroes).
	h.LastEntityUpdatesSent = 0
	for _, c := range h.Static.Clients {
		stat, err := h.Server.SendEntityUpdates(c, p, h.Static.MaxClients)
		if err != nil {
			return err
		}
		h.LastEntityUpdatesSent += stat.Emitted
	}

	// SendClientFrames is best-effort per-client; its PerClientErrs
	// surface here as the first non-nil entry (so the caller can
	// log + decide whether to DropClient). The slice itself is
	// dropped after the scan -- the host loop has no per-client
	// retry policy yet.
	res := h.Server.SendClientFrames(h.Static)
	for _, perr := range res.PerClientErrs {
		if perr != nil {
			return perr
		}
	}

	// FlushClientMessage drains each client.Message buffer through
	// its bound NetConnection via SendReliable, then clears it. This
	// is the server-to-client back-channel: without it, the per-tic
	// svc_clientdata + broadcast datagrams the loop above wrote into
	// client.Message never reach the loopback peer + client.Tick's
	// drain finds an empty inbox.
	for _, c := range h.Static.Clients {
		if err := h.Server.FlushClientMessage(c); err != nil {
			return err
		}
	}

	// End-of-frame: clear muzzleflash effect bits, then drop the
	// per-tic unreliable + reliable buffers so the next tic starts
	// with a fresh canvas.
	h.Server.CleanupEnts(nil)
	h.Server.ClearDatagram()
	h.Server.ClearReliableDatagram()
	return nil
}

// SpawnServer wraps server.Server.SpawnServer with the host's
// configured deps (Cache, Resolver, the VM's bound Progs, the
// World, and the Static client pool). tyrquake: the SV_SpawnServer
// call inside Host_Cmd's "map" command handler.
//
// The bound Progs comes from h.VM via [findProgs]; the host owns
// the VM, which owns the Progs, so the caller doesn't need to
// re-supply it. Returns SpawnServer's error verbatim.
func (h *Host) SpawnServer(mapName string, protocol int) error {
	deps := server.SpawnDeps{
		Cache:    h.Cache,
		Resolver: h.Resolver,
		Progs:    h.findProgs(),
		Static:   h.Static,
		World:    h.World,
		// SpawnFn is whatever [Host.SetSpawnFn] last installed; nil
		// = entities still get their fields populated from the BSP
		// entity lump but no per-classname QC initialiser runs. The
		// embedder wires the real dispatch once the VM has a builtin
		// table large enough for the spawn-time builtins the QC code
		// calls (precache_*, setmodel, setorigin, ...).
		SpawnFn: h.spawnFn,
		// Interner is a noop-zero by default: every string field
		// interns to offset 0 (the empty-string sentinel). The map
		// still parses; only the human-readable string payload is
		// dropped. Production embedders override via [Host.SetInterner]
		// once a real progs.StringInterner exists.
		Interner: h.interner,
		// OnArenaReady is the per-SpawnServer arena-publication
		// hook; nil = arena is still stashed on Server.Arena post-
		// SpawnServer, just not handed to the VM mid-spawn.
		OnArenaReady: h.onArenaReady,
	}
	return h.Server.SpawnServer(mapName, protocol, deps)
}

// ConnectLoopback creates a loopback NetConn pair, binds the
// server-side half to a free client slot, and returns the client-
// side half for the local client to read/write.
//
// tyrquake: the implicit "local client" connection that
// Host_InitLocal sets up before the first PR_ExecuteProgram dispatch.
//
// Returns:
//
//	(clientSide, slotIdx, nil)            -- happy path
//	(nil, -1, ErrNoFreeClientSlot)        -- pool is full
//
// The server-side half is held by Static.Clients[slotIdx]
// .NetConnection (as an `any`); callers that need to talk to it
// type-assert to *server.LoopbackConn.
func (h *Host) ConnectLoopback() (server.NetConn, int, error) {
	clientSide, serverSide := server.NewLoopbackConn()
	now := h.NowFn()
	// Pre-compute the slot index the upcoming ConnectClient call
	// will pick (the first non-active slot). makeEdict needs the
	// index *before* ConnectClient flips the Active flag, since
	// ConnectClient calls makeEdict only after setting Active = true
	// -- so a post-hoc scan inside makeEdict would find the *next*
	// free slot, not the one being bound.
	freeSlot := -1
	for i, c := range h.Static.Clients {
		if !c.Active {
			freeSlot = i
			break
		}
	}
	makeEdict := func() *progs.Edict {
		// SpawnServer reserves Server.Edicts[1..MaxClients] for
		// clients (slot 0 is the world); if SpawnServer hasn't run
		// yet, the pool is empty + this returns nil. The upstream
		// lifecycle crashes on the first per-client progs dispatch
		// in that case; the Go port surfaces it as a nil-edict
		// client the per-tic physics guards against.
		if freeSlot < 0 || freeSlot+1 >= len(h.Server.Edicts) {
			return nil
		}
		return h.Server.Edicts[freeSlot+1]
	}
	idx, err := server.ConnectClient(h.Static, serverSide, now, makeEdict)
	if err != nil {
		return nil, -1, err
	}
	return clientSide, idx, nil
}

// thinkCaller is the [server.ThinkCaller] hook the host installs on
// every per-tic Physics call. Translates a QC function index into a
// [progs.VM.Run] invocation, after setting the QC self global to
// point at the thinking edict and the time global to the current
// sv.time.
//
// Wiring approach: the Go port's VM has SetGlobalInt /
// SetGlobalFloat keyed by global-pool slot offsets, but no named
// "self" / "other" / "time" accessor -- the upstream uses a C
// pr_global_struct overlaid on the start of pr_globals at fixed
// offsets, which is a layout convention this Go port leaves to the
// embedder. The bridge looks up the named globals via Progs.FindGlobal;
// progs.dat files that don't declare them (test stubs, custom
// minimalist QC) silently skip the write -- the per-think dispatch
// still happens, just without the global hand-off. Real Q1 progs.dat
// always declares all three.
//
// Returns the propagated VM.Run error; nil on success.
// runPlayerPostThink dispatches the QC PlayerPostThink for each active
// client's edict once per tic, AFTER the physics move (the post-move half of
// SV_Physics_Client). PlayerPostThink runs W_WeaponFrame, which reads
// self.button0 (the +attack bit runClientCmds wrote onto the edict) and calls
// W_Attack to fire the current weapon -- traceline -> T_Damage on whatever it
// hits. Without this the player can hold +attack forever and never shoot.
//
// PlayerPreThink is intentionally NOT dispatched: id1's PlayerPreThink also
// drives +jump, which this port already applies engine-side in PhysicsWalk;
// running both would double the jump impulse.
//
// A missing PlayerPostThink (stripped test progs) is a no-op. A QC error
// (W_Attack hit a builtin the host hasn't wired) is tallied + swallowed so
// one bad shot can't abort the whole frame -- same policy as runThink.
func (h *Host) runPlayerPostThink() {
	h.LastPlayerPostThinks = 0
	p := h.findProgs()
	if p == nil {
		return
	}
	_, idx := p.FindFunction("PlayerPostThink")
	if idx < 1 {
		return
	}
	for _, c := range h.Static.Clients {
		if c == nil || !c.Active || c.Edict == nil {
			continue
		}
		// A QC error (W_Attack reaching a builtin the host hasn't wired) is
		// swallowed so one bad shot can't abort the frame -- same tolerance
		// as runThink's monster dispatch.
		_ = h.thinkCaller(c.Edict, int32(idx))
		h.LastPlayerPostThinks++
	}
}

func (h *Host) thinkCaller(ent *progs.Edict, funcID int32) error {
	p := h.findProgs()
	if p != nil {
		if def := p.FindGlobal("self"); def != nil {
			// Self is an ev_entity, stored as the byte-offset
			// pointer the arena's MakePointer produces. The VM
			// owns the same arena via SetArena; if the arena is
			// not wired, the per-edict pointer arithmetic is
			// undefined -- but FindGlobal returning non-nil means
			// the embedder declared the field, so we trust the
			// embedder also wired the arena.
			_ = h.VM.SetGlobalInt(int(def.Ofs), int32(h.entityPointer(ent)))
		}
		if def := p.FindGlobal("other"); def != nil {
			// Default other to the world edict (index 0), matching
			// the C upstream's PR_ExecuteProgram entry convention.
			_ = h.VM.SetGlobalInt(int(def.Ofs), int32(h.entityPointer(h.worldEdict())))
		}
		if def := p.FindGlobal("time"); def != nil {
			_ = h.VM.SetGlobalFloat(int(def.Ofs), float32(h.Server.Time))
		}
	}
	return h.VM.Run(funcID)
}

// entityPointer returns the QC entity-pointer (a byte offset into
// the edict arena) for ent. Falls back to 0 (the world pointer)
// when ent is nil OR when no arena is attached, matching the
// upstream's "every nil ent is the world" convention inside
// PR_ExecuteProgram.
//
// With an arena present (the production path post-SpawnServer), the
// per-edict byte offset is the value the arena's MakePointer encodes
// for ent. This is what OP_STATE consumes via vm.selfEdict() to
// resolve which edict's nextthink/frame/think fields to write -- so
// returning 0 here for a non-world ent would silently divert every
// post-spawn OP_STATE write onto the world edict (root cause of the
// monster-think-chain breaking after the first dispatch: re-arms
// land on the world, the monster's own .nextthink stays cleared,
// and the walker never re-fires the chain).
func (h *Host) entityPointer(ent *progs.Edict) int {
	if ent == nil {
		return 0
	}
	if h.Server == nil || h.Server.Arena == nil {
		// Pre-SpawnServer (test stubs that skip the map load). The
		// QC bridge still dispatches vm.Run(funcID); only the per-
		// edict pointer hand-off degrades to the world sentinel.
		return 0
	}
	return int(h.Server.Arena.PointerForEdict(ent))
}

// worldEdict returns Server.Edicts[0] -- the world entity the C
// upstream pins as pr_global_struct->other's default before each
// think dispatch. nil when SpawnServer has not run.
func (h *Host) worldEdict() *progs.Edict {
	if len(h.Server.Edicts) == 0 {
		return nil
	}
	return h.Server.Edicts[0]
}

// findProgs returns the Progs the VM is bound to. The VM exposes no
// direct accessor for its bound progs (it owns a writable copy of
// the globals, but the Functions / FieldDefs / GlobalDefs come from
// the original Progs the embedder loaded). The Host stashes the
// reference via [Host.SetProgs]; production callers call it once
// after NewVM, passing the same *Progs they fed into NewVM.
func (h *Host) findProgs() *progs.Progs {
	return h.progsRef
}

// SetProgs records the Progs the bridge should reach for when
// resolving the self / other / time globals + the per-tic physics
// dispatch's entvars binds. Callers pass the same *Progs they used
// to build [Host.VM]. Safe to call before or after [Host.SpawnServer].
// nil is accepted -- the bridge falls back to the no-named-global
// path + RunPhysics surfaces the nil-progs guard verbatim.
func (h *Host) SetProgs(p *progs.Progs) {
	h.progsRef = p
}

// Progs returns the [progs.Progs] last installed via [Host.SetProgs]
// (nil if none). Exposed so embedders that need to issue ad-hoc
// EntVars reads / writes against the host's edict pool can construct
// the [progs.EntVars] view without re-loading the bytecode. The host
// itself uses the unexported alias [Host.findProgs] internally so the
// accessor surface stays narrow + opt-in.
func (h *Host) Progs() *progs.Progs {
	return h.progsRef
}

// ErrNoEdict fires when [Host.EdictOrigin] is asked for a slot that
// has no live edict -- either SpawnServer has not run (the pool is
// empty) or the requested slot is past the end of the allocated pool.
var ErrNoEdict = errors.New("host: edict slot out of range")

// ErrNoProgs fires when [Host.EdictOrigin] is called without a Progs
// bound -- the field-name -> offset lookup needs the FieldDefs table,
// which only the embedder-supplied Progs carries.
var ErrNoProgs = errors.New("host: EdictOrigin needs a bound Progs")

// EdictOrigin returns the value of the per-edict QC "origin" vector
// at slot in the active server's edict pool. tyrquake: ent->v.origin
// for ent = sv.edicts[slot] (the world entity is slot 0; clients
// occupy slots 1..MaxClients).
//
// Bring-up use case: per-frame camera-follow reads slot 1 (the local
// player) and uses the returned origin as the [render.RefDef]
// ViewOrigin. Until the QC SpawnFn hook is wired the player edict's
// origin field stays at the bytecode default (typically {0,0,0});
// callers detect that case and fall back to a fixed anchor.
//
// Returns ErrNoEdict when slot is out of range, ErrNoProgs when no
// Progs is bound, and ErrFieldNotFound / ErrFieldTypeMismatch from
// the underlying [progs.EntVars] read.
//
// NewEntVars's nil-arg guard is unreachable here -- the slot + progs
// guards above ensure both inputs are non-nil -- so its error is
// dropped (bsptrace-style) rather than threaded through.
func (h *Host) EdictOrigin(slot int) ([3]float32, error) {
	if slot < 0 || slot >= len(h.Server.Edicts) {
		return [3]float32{}, ErrNoEdict
	}
	ent := h.Server.Edicts[slot]
	if ent == nil {
		return [3]float32{}, ErrNoEdict
	}
	p := h.findProgs()
	if p == nil {
		return [3]float32{}, ErrNoProgs
	}
	v, _ := progs.NewEntVars(p, ent)
	return v.ReadVec3("origin")
}
