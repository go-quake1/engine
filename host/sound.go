// Copyright (c) 1996-1997 Id Software, Inc.
// Copyright (c) 2026 the go-quake1/engine authors.
// SPDX-License-Identifier: GPL-2.0-or-later

package host

import (
	"errors"
	"fmt"

	"github.com/go-quake1/engine/server"
	"github.com/go-quake1/engine/sound"
)

// ErrNoSoundPool fires when [Host.StartSound] / [Host.AmbientSound]
// is called before [Host.SetSoundPool] has wired a mixer pool. The QC
// builtins that route through here treat it as a tolerated no-op (the
// game continues silently); the error surfaces to embedder paths that
// want a hard fail.
var ErrNoSoundPool = errors.New("host: no sound pool wired (call SetSoundPool first)")

// ErrSoundNotPrecached fires when [Host.StartSound] / [Host.AmbientSound]
// is asked to play a sample name that was never handed to
// [Host.PrecacheSound]. The upstream Sys_Printf's a warning + silently
// drops the sound; the Go port surfaces the error so a buggy caller
// can tell why nothing played.
var ErrSoundNotPrecached = errors.New("host: sample not in precache table")

// SetListener records the per-tic listener context the spatializing
// sound dispatch ([Host.StartSoundAt] / [Host.AmbientSoundAt]) reads
// to drive stereo balance + distance falloff. tyrquake: the per-tic
// AngleVectors(cl.viewangles, forward, right, up) inside S_Update,
// hoisted out so the host doesn't directly depend on the client
// package + so embedders can wire any listener basis (replay viewer,
// fixed camera, ...).
//
//	origin -- viewer world position (typically the wire-mirrored
//	          client.State.Entities[PlayerNum].Origin)
//	right  -- viewer right axis (the second return of
//	          mathlib.AngleVectors(viewangles); points to the
//	          listener's right ear)
//
// Until called for the first time, StartSoundAt / AmbientSoundAt
// degrade to "no spatial separation" (= existing pre-batch behaviour).
// After a SetListener, every subsequent spatializing call uses the
// stored basis until a new SetListener overrides it.
func (h *Host) SetListener(origin, right [3]float32) {
	if h == nil {
		return
	}
	h.listenerOrigin = origin
	h.listenerRight = right
	h.listenerSet = true
}

// HasListener reports whether [Host.SetListener] has been called at
// least once. Exposed so embedders that want to choose between the
// spatializing and the non-spatializing StartSound path can detect
// the wiring state without re-tracking it themselves.
func (h *Host) HasListener() bool {
	if h == nil {
		return false
	}
	return h.listenerSet
}

// SetSoundPool wires the mixer pool the per-tic runloop ALREADY owns
// (runloop.Runner.SoundPool, allocated by NewRunnerFromVFS when
// SoundChannels > 0). The host's StartSound / AmbientSound builtins
// reach into the pool to allocate channels + park samples; without
// this hook the builtins degrade to silent no-ops + return
// [ErrNoSoundPool].
//
// Safe to call before or after [Host.SpawnServer]. Passing nil
// detaches the pool (every subsequent StartSound becomes a no-op);
// useful for tests that want to validate the precache path without
// the mixer.
func (h *Host) SetSoundPool(p *sound.Pool) {
	h.soundPool = p
}

// SoundPool returns the mixer pool last installed via [Host.SetSoundPool]
// (nil if none). Exposed so the per-tic instrumentation in quake-tamago
// can read ActiveCount without re-threading the pool through the loop.
func (h *Host) SoundPool() *sound.Pool {
	return h.soundPool
}

// SoundLoader is the per-name WAV blob lookup the host hands its
// PrecacheSound implementation. The host itself does not know about
// pak archives / VFS layouts; the embedder injects the lookup via
// [Host.SetSoundLoader] so the QC builtin can resolve the sample
// data without the host package growing a dependency on `pak` / `vfs`.
//
// The function returns (blob, true) when the named WAV exists, or
// (nil, false) when it is missing. Lookup keys are the canonical
// upstream form: "sound/<path>.wav" (the QC builtin prefixes "sound/"
// before calling).
type SoundLoader func(name string) (blob []byte, ok bool)

// SetSoundLoader wires the WAV blob lookup [Host.PrecacheSound]
// consults. The embedder typically passes a closure over its pakFS
// (e.g. `func(n string) ([]byte, bool) { return tryReadPakFile(pakFS, n) }`).
// nil disables the lookup (PrecacheSound then surfaces the missing-blob
// path: ErrSoundLoadFailed wrapped around "no loader").
func (h *Host) SetSoundLoader(fn SoundLoader) {
	h.soundLoader = fn
}

// ErrSoundLoadFailed wraps any error the underlying WAV blob lookup
// OR [sound.LoadWav] returns. Includes the sample name for diagnostics.
var ErrSoundLoadFailed = errors.New("host: WAV load failed")

// PrecacheSound resolves `name` (the bare upstream form, e.g.
// "weapons/sshotgun.wav") into a fully-loaded [sound.Sample], records
// it in [Host.Server.SoundPrecache] (the wire-side precache the
// per-client svc_sound dispatch looks up), and parks the parsed
// *Sample in h.Sounds[idx] so [Host.StartSound] can hand it to the
// mixer without re-parsing.
//
// The lookup key prepends "sound/" to match the upstream's "sound/<n>"
// asset path. Empty name returns (0, nil) (the empty-string sentinel
// reserved by the precache table; QC's `precache_sound("")` is a no-op).
//
// Tolerated states:
//
//   - nil host                          (0, server.ErrNilServer)
//   - nil Server                        (0, server.ErrNilServer)
//   - nil SoundLoader                   (0, ErrSoundLoadFailed)
//   - loader returns (_, false)         (0, ErrSoundLoadFailed)
//   - LoadWav fails                     (0, ErrSoundLoadFailed wrapping the parse err)
//   - server.PrecacheSound full         (0, propagated server.ErrPrecacheFull)
//
// On success returns the precache slot index (>=1) the sample landed
// in; the embedder typically writes this back to OFS_RETURN so
// QC's `self.noise = precache_sound(...)` lands a stable handle.
//
// tyrquake: PF_precache_sound (pr_cmds.c) -- which in the upstream just
// adds the name to the table; the Go port also eagerly parses the WAV
// here so the per-tic StartSound dispatch is a pool-Alloc + Channel
// fill rather than a parse-and-fill (the parse is one-time at map
// load; per-tic stays allocation-free).
func (h *Host) PrecacheSound(name string) (int, error) {
	if h == nil || h.Server == nil {
		return 0, server.ErrNilServer
	}
	if name == "" {
		return 0, nil
	}
	idx, err := server.PrecacheSound(h.Server.SoundPrecache, name)
	if err != nil {
		return 0, err
	}
	// Re-precache of the same name: keep the slot, return immediately.
	// h.Sounds may already hold the parsed sample (per-classname spawn
	// QC frequently calls precache_sound for the same WAV from multiple
	// classnames -- e.g. every door variant precaches "doors/medtry.wav").
	if h.ensureSoundsLen(idx) && h.Sounds[idx] != nil {
		return idx, nil
	}
	if h.soundLoader == nil {
		return 0, fmt.Errorf("%w: no loader for %q", ErrSoundLoadFailed, name)
	}
	blob, ok := h.soundLoader("sound/" + name)
	if !ok {
		return 0, fmt.Errorf("%w: %q missing from pak", ErrSoundLoadFailed, name)
	}
	s, err := sound.LoadWav(name, blob)
	if err != nil {
		return 0, fmt.Errorf("%w: %q: %v", ErrSoundLoadFailed, name, err)
	}
	h.Sounds[idx] = s
	return idx, nil
}

// ensureSoundsLen grows h.Sounds to at least len > idx so the slot
// at idx is addressable. Returns true if the slot was already (or
// just became) in range. Capacity policy: grow to exactly idx+1 the
// first time, then double when grown again -- typical Q1 maps
// precache 10..30 sounds so the linear-then-double shape avoids
// O(N^2) without over-allocating.
func (h *Host) ensureSoundsLen(idx int) bool {
	if idx < 0 {
		return false
	}
	if idx < len(h.Sounds) {
		return true
	}
	want := idx + 1
	if cap(h.Sounds) >= want {
		h.Sounds = h.Sounds[:want]
		return true
	}
	// Grow geometrically once we exceed the initial idx+1 (the first
	// PrecacheSound call sets the baseline).
	newCap := want
	if cap(h.Sounds) > 0 && cap(h.Sounds)*2 > want {
		newCap = cap(h.Sounds) * 2
	}
	next := make([]*sound.Sample, want, newCap)
	copy(next, h.Sounds)
	h.Sounds = next
	return true
}

// StartSound dispatches one sound play-event onto the host's mixer
// pool. tyrquake: SV_StartSound (NQ/sv_main.c) -- the C upstream
// also writes svc_sound onto the per-client datagram so remote
// clients hear it; the Go port delegates that to [server.StartSound]
// (which the embedder calls separately if a network-broadcast is
// desired). This method is the LOCAL mixer path that drives audio
// output through the runloop's Paint -> QueueAudio chain.
//
// Parameters:
//
//	entIdx       entity owning the sound (0 = world/global)
//	channel      0..7, entity-relative channel (same ent+channel
//	             replaces -- footsteps don't stack)
//	name         sample name (bare upstream form, looked up in
//	             Server.SoundPrecache + h.Sounds[idx])
//	volume       0..MaxVolume (sound.MaxVolume = 255)
//	leftVol,
//	rightVol     -1 to skip spatialization; >= 0 overrides the
//	             post-spatialize left/right (the spatialize-then-
//	             attenuate path is the future enhancement; for the
//	             bring-up the embedder passes the spatialized values
//	             directly OR -1 to use full-master volume)
//
// On success returns the pool slot index the channel landed in
// (>=0); on a tolerated no-op (no pool wired, empty name, missing
// precache) returns (-1, err) -- the caller can swallow if QC-side
// silence is preferred.
//
// nil host / nil pool -> (-1, ErrNoSoundPool).
// Empty name          -> (-1, nil) (matches QC sound("",...) no-op).
// Missing precache    -> (-1, ErrSoundNotPrecached).
// Sample not loaded   -> (-1, ErrSoundNotPrecached) (precache slot
//
//	exists but Sounds[idx] is nil; usually means
//	the WAV was missing from pak at load time).
//
// Pool exhaustion     -> propagated sound.ErrPoolNoFreeSlot.
func (h *Host) StartSound(entIdx, channel int, name string, volume int, leftVol, rightVol int) (int, error) {
	if h == nil || h.soundPool == nil {
		return -1, ErrNoSoundPool
	}
	if name == "" {
		return -1, nil
	}
	if h.Server == nil {
		return -1, server.ErrNilServer
	}
	idx, err := server.SoundIndex(h.Server.SoundPrecache, name)
	if err != nil {
		return -1, ErrSoundNotPrecached
	}
	if idx < 0 || idx >= len(h.Sounds) || h.Sounds[idx] == nil {
		return -1, ErrSoundNotPrecached
	}
	sample := h.Sounds[idx]
	slot, err := h.soundPool.Alloc(entIdx, channel)
	if err != nil {
		return -1, err
	}
	ch := &h.soundPool.Channels[slot]
	ch.Sfx = sample
	ch.Position = 0
	ch.EndPos = sample.NumSamples
	ch.Looping = sample.LoopStart >= 0 // tyrquake: target_chan->looping = (sc->loopstart >= 0)
	ch.EntNum = entIdx
	ch.EntChannel = channel
	if leftVol < 0 {
		leftVol = volume
	}
	if rightVol < 0 {
		rightVol = volume
	}
	if leftVol > sound.MaxVolume {
		leftVol = sound.MaxVolume
	}
	if rightVol > sound.MaxVolume {
		rightVol = sound.MaxVolume
	}
	if leftVol < 0 {
		leftVol = 0
	}
	if rightVol < 0 {
		rightVol = 0
	}
	ch.LeftVol = leftVol
	ch.RightVol = rightVol
	ch.Master = false
	h.soundPool.Launch(slot) // a command for the mixer when it runs elsewhere
	h.LastSoundsStarted++
	return slot, nil
}

// StartSoundAt is the spatializing variant of [Host.StartSound] that
// computes the per-ear volumes via [sound.Spatialize] from the stored
// listener context ([Host.SetListener]) and the source's world
// position. tyrquake: the SV_StartSound -> SND_Spatialize chain
// inside snd_dma.c (the C upstream spatializes on the client side
// every Paint; the Go port spatializes once at fire-time on the host
// side -- a cheaper shape that gives the same audible result for
// short one-shot effects + matches the "do it once where the source
// origin is known" pattern the embedder's QC builtin already lives in).
//
// Per-arg shape:
//
//	entIdx       -- entity owning the sound (0 = world / global)
//	channel      -- 0..7, entity-relative channel
//	name         -- sample name (canonical bare form, looked up in
//	                Server.SoundPrecache)
//	vol          -- caller-supplied master volume (0..[sound.MaxVolume],
//	                = the QC `volume` arg scaled to byte range)
//	attenuation  -- per-sound distance falloff coefficient (= the QC
//	                `attenuation` arg; typical values:
//	                [sound.AttenuationNone] for global UI sounds,
//	                [sound.AttenuationNormal] for gameplay,
//	                [sound.AttenuationIdle] for short-range monster
//	                idle sounds, [sound.AttenuationStatic] for env_sound)
//	sourceOrigin -- world position of the sound source (typically the
//	                owning entity's `origin` entvars value; callers
//	                pass [3]float32{} for entIdx==0 + no specific
//	                anchor)
//
// Without a listener wired (HasListener()==false) OR with
// attenuation==[sound.AttenuationNone], the dispatch falls through to
// the existing [Host.StartSound] no-spatialize path (full vol on both
// ears). Otherwise sound.Spatialize computes leftVol + rightVol from
// the (listener->source) vector + the listener's right axis, and
// those values are passed verbatim to StartSound's channel-fill.
//
// Errors mirror [Host.StartSound] verbatim.
func (h *Host) StartSoundAt(entIdx, channel int, name string, vol int, attenuation sound.SoundAttenuation, sourceOrigin [3]float32) (int, error) {
	if h == nil || h.soundPool == nil {
		return -1, ErrNoSoundPool
	}
	// AttenuationNone short-circuits the per-ear math (master scale is
	// always 1, balance is irrelevant) -- skip Spatialize and reuse the
	// existing no-spatialize path so the channel-fill stays single-sourced.
	if !h.listenerSet || attenuation == sound.AttenuationNone {
		return h.StartSound(entIdx, channel, name, vol, -1, -1)
	}
	out := sound.Spatialize(sound.SpatializeIn{
		ListenerOrigin: h.listenerOrigin,
		ListenerRight:  h.listenerRight,
		SoundOrigin:    sourceOrigin,
		MasterVolume:   vol,
		Attenuation:    attenuation,
	})
	return h.StartSound(entIdx, channel, name, vol, out.LeftVol, out.RightVol)
}

// AmbientSound parks `name` on one of the pool's reserved-static
// channels (slots 0..ReservedStatic-1) as a looped ambient track.
// tyrquake: PF_ambientsound (pr_cmds.c builtin #74) -- the C upstream
// records the sound on a static channel that ignores entity / channel
// allocation rules + loops forever.
//
// `slot` selects which reserved-static channel to use (0..pool.ReservedStatic-1).
// Callers typically increment a counter so multiple ambient sources at
// different positions in the map each get their own slot; passing the
// same slot twice replaces the previous ambient (the upstream allocates
// from a fixed bank too).
//
// On success returns the pool slot index (= slot). Errors mirror
// StartSound: no pool, missing precache, slot out of range.
//
// The channel loops when the sample has a cue-chunk loop point
// (Sample.LoopStart >= 0): [sound.Paint] restarts it at LoopStart when
// Position reaches EndPos.
func (h *Host) AmbientSound(slot, entIdx int, name string, volume int) (int, error) {
	if h == nil || h.soundPool == nil {
		return -1, ErrNoSoundPool
	}
	if name == "" {
		return -1, nil
	}
	if h.Server == nil {
		return -1, server.ErrNilServer
	}
	if slot < 0 || slot >= h.soundPool.ReservedStatic {
		return -1, fmt.Errorf("host: ambient slot %d out of [0, %d)", slot, h.soundPool.ReservedStatic)
	}
	idx, err := server.SoundIndex(h.Server.SoundPrecache, name)
	if err != nil {
		return -1, ErrSoundNotPrecached
	}
	if idx < 0 || idx >= len(h.Sounds) || h.Sounds[idx] == nil {
		return -1, ErrSoundNotPrecached
	}
	sample := h.Sounds[idx]
	ch := &h.soundPool.Channels[slot]
	ch.Sfx = sample
	ch.Position = 0
	// Ambient sounds loop: a sample with a cue loop point (LoopStart >= 0)
	// restarts at it when its data is consumed ([sound.Paint], tyrquake's
	// S_PaintChannels). One without a loop point plays once and ends;
	// tyrquake's S_StaticSound would refuse it ("Sound %s not looped"), and
	// this port keeps playing it once.
	ch.EndPos = sample.NumSamples
	ch.Looping = sample.LoopStart >= 0
	ch.EntNum = entIdx
	ch.EntChannel = 0
	if volume > sound.MaxVolume {
		volume = sound.MaxVolume
	}
	if volume < 0 {
		volume = 0
	}
	ch.LeftVol = volume
	ch.RightVol = volume
	ch.Master = true
	h.soundPool.Launch(slot)
	h.LastAmbientsStarted++
	return slot, nil
}

// AmbientSoundAt is the spatializing variant of [Host.AmbientSound]
// that drives stereo balance + distance falloff via [sound.Spatialize]
// from the listener context [Host.SetListener] last recorded + the
// passed `position` (the QC `position` arg of PF_ambientsound, the
// world-space anchor of the ambient source). tyrquake: the static-
// channel arm of PF_ambientsound + SND_Spatialize.
//
//	slot        -- reserved-static channel index (0..ReservedStatic-1)
//	entIdx      -- owning entity (typically 0 for env_sound entities)
//	name        -- sample name (canonical bare form)
//	volume      -- caller-supplied master volume (0..[sound.MaxVolume])
//	position    -- world-space anchor (= QC ambientsound's first arg)
//	attenuation -- per-source distance falloff coefficient (the QC
//	               ATTN_STATIC=3 / ATTN_IDLE=2 / ATTN_NORM=1 dispatch)
//
// Without a wired listener OR with attenuation==[sound.AttenuationNone]
// the dispatch falls through to the existing [Host.AmbientSound] path
// (full vol on both ears). Channel.Master stays true (= ambient,
// looping) regardless of which branch wrote the volumes.
//
// Errors mirror [Host.AmbientSound] verbatim.
func (h *Host) AmbientSoundAt(slot, entIdx int, name string, volume int, position [3]float32, attenuation sound.SoundAttenuation) (int, error) {
	if h == nil || h.soundPool == nil {
		return -1, ErrNoSoundPool
	}
	if !h.listenerSet || attenuation == sound.AttenuationNone {
		return h.AmbientSound(slot, entIdx, name, volume)
	}
	out := sound.Spatialize(sound.SpatializeIn{
		ListenerOrigin: h.listenerOrigin,
		ListenerRight:  h.listenerRight,
		SoundOrigin:    position,
		MasterVolume:   volume,
		Attenuation:    attenuation,
	})
	// Park the sample on the slot via AmbientSound first (handles
	// precache lookup + channel-fill + Master flag), then override the
	// left/right volumes with the spatialized values. AmbientSound's
	// own volume clamp is harmless (the Spatialize output is already
	// clamped to [0, MaxVolume]).
	out2, err := h.AmbientSound(slot, entIdx, name, volume)
	if err != nil || out2 < 0 {
		return out2, err
	}
	ch := &h.soundPool.Channels[out2]
	ch.LeftVol = out.LeftVol
	ch.RightVol = out.RightVol
	h.soundPool.Retune(out2)
	return out2, nil
}
