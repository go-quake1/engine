// Copyright (c) 2026 the go-quake1/engine authors.
// SPDX-License-Identifier: GPL-2.0-or-later

package host

import (
	"encoding/binary"
	"errors"
	"strings"
	"testing"

	"github.com/go-quake1/engine/server"
	"github.com/go-quake1/engine/sound"
)

// synthWAV builds a tiny valid PCM WAV blob: mono, 8-bit, `n`
// silence samples (= 0x80 unsigned = 0 signed). Just enough for
// the loader to succeed.
func synthWAV(n int) []byte {
	body := make([]byte, n)
	for i := range body {
		body[i] = 0x80
	}
	buf := make([]byte, 0, 44+n)
	// RIFF header
	buf = append(buf, 'R', 'I', 'F', 'F')
	buf = appendU32(buf, uint32(36+n))
	buf = append(buf, 'W', 'A', 'V', 'E')
	// fmt chunk (PCM, mono, 11025 Hz, 8-bit)
	buf = append(buf, 'f', 'm', 't', ' ')
	buf = appendU32(buf, 16)
	buf = appendU16(buf, 1)       // PCM
	buf = appendU16(buf, 1)       // mono
	buf = appendU32(buf, 11025)   // sample rate
	buf = appendU32(buf, 11025*1) // byte rate
	buf = appendU16(buf, 1)       // block align
	buf = appendU16(buf, 8)       // bits per sample
	// data chunk
	buf = append(buf, 'd', 'a', 't', 'a')
	buf = appendU32(buf, uint32(n))
	buf = append(buf, body...)
	return buf
}

func appendU32(buf []byte, v uint32) []byte {
	var b [4]byte
	binary.LittleEndian.PutUint32(b[:], v)
	return append(buf, b[:]...)
}

func appendU16(buf []byte, v uint16) []byte {
	var b [2]byte
	binary.LittleEndian.PutUint16(b[:], v)
	return append(buf, b[:]...)
}

// hostForSoundTest returns a bare host with a Server (so SoundPrecache
// exists) but no VM / Cache / Resolver -- the sound path doesn't need
// them.
func hostForSoundTest(t *testing.T) *Host {
	t.Helper()
	h := &Host{Server: server.NewServer()}
	return h
}

// TestPrecacheSound_HappyPath asserts a fresh PrecacheSound call
// resolves the WAV via the injected loader, stores the *Sample at
// h.Sounds[idx], and writes the bare name onto h.Server.SoundPrecache.
func TestPrecacheSound_HappyPath(t *testing.T) {
	h := hostForSoundTest(t)
	blob := synthWAV(128)
	h.SetSoundLoader(func(name string) ([]byte, bool) {
		if name == "sound/foo.wav" {
			return blob, true
		}
		return nil, false
	})

	idx, err := h.PrecacheSound("foo.wav")
	if err != nil {
		t.Fatalf("PrecacheSound: %v", err)
	}
	if idx < 1 {
		t.Fatalf("PrecacheSound idx: got %d, want >=1", idx)
	}
	if h.Server.SoundPrecache[idx] != "foo.wav" {
		t.Fatalf("SoundPrecache[%d]: got %q want %q", idx, h.Server.SoundPrecache[idx], "foo.wav")
	}
	if idx >= len(h.Sounds) || h.Sounds[idx] == nil {
		t.Fatalf("h.Sounds[%d] empty: len=%d", idx, len(h.Sounds))
	}
	if h.Sounds[idx].NumSamples != 128 {
		t.Errorf("Sounds[%d].NumSamples = %d, want 128", idx, h.Sounds[idx].NumSamples)
	}
}

// TestPrecacheSound_Empty asserts an empty name short-circuits.
func TestPrecacheSound_Empty(t *testing.T) {
	h := hostForSoundTest(t)
	idx, err := h.PrecacheSound("")
	if err != nil {
		t.Fatalf("empty name: %v", err)
	}
	if idx != 0 {
		t.Errorf("empty name idx: got %d want 0", idx)
	}
}

// TestPrecacheSound_NilHost surfaces ErrNilServer on nil host
// (defensive guard the embedder's QC builtin closure relies on).
func TestPrecacheSound_NilHost(t *testing.T) {
	var h *Host
	if _, err := h.PrecacheSound("x"); !errors.Is(err, server.ErrNilServer) {
		t.Errorf("nil host: got %v want ErrNilServer", err)
	}
	h2 := &Host{} // nil Server
	if _, err := h2.PrecacheSound("x"); !errors.Is(err, server.ErrNilServer) {
		t.Errorf("nil server: got %v want ErrNilServer", err)
	}
}

// TestPrecacheSound_NoLoader surfaces ErrSoundLoadFailed when the
// embedder didn't wire a loader.
func TestPrecacheSound_NoLoader(t *testing.T) {
	h := hostForSoundTest(t)
	if _, err := h.PrecacheSound("foo.wav"); !errors.Is(err, ErrSoundLoadFailed) {
		t.Errorf("no loader: got %v want ErrSoundLoadFailed", err)
	}
}

// TestPrecacheSound_LoaderMiss surfaces ErrSoundLoadFailed when
// the loader returns (_, false).
func TestPrecacheSound_LoaderMiss(t *testing.T) {
	h := hostForSoundTest(t)
	h.SetSoundLoader(func(string) ([]byte, bool) { return nil, false })
	if _, err := h.PrecacheSound("foo.wav"); !errors.Is(err, ErrSoundLoadFailed) {
		t.Errorf("loader miss: got %v want ErrSoundLoadFailed", err)
	}
}

// TestPrecacheSound_BadWAV surfaces ErrSoundLoadFailed (wrapping the
// parse error) when the blob isn't a valid WAV.
func TestPrecacheSound_BadWAV(t *testing.T) {
	h := hostForSoundTest(t)
	h.SetSoundLoader(func(string) ([]byte, bool) { return []byte("not a wav"), true })
	_, err := h.PrecacheSound("foo.wav")
	if !errors.Is(err, ErrSoundLoadFailed) {
		t.Errorf("bad wav: got %v want ErrSoundLoadFailed", err)
	}
}

// TestPrecacheSound_Idempotent asserts re-precaching the same name
// returns the same slot + does not re-parse the WAV.
func TestPrecacheSound_Idempotent(t *testing.T) {
	h := hostForSoundTest(t)
	blob := synthWAV(64)
	calls := 0
	h.SetSoundLoader(func(name string) ([]byte, bool) {
		calls++
		return blob, true
	})
	idx1, err := h.PrecacheSound("a.wav")
	if err != nil {
		t.Fatalf("first: %v", err)
	}
	idx2, err := h.PrecacheSound("a.wav")
	if err != nil {
		t.Fatalf("second: %v", err)
	}
	if idx1 != idx2 {
		t.Errorf("idx mismatch: %d vs %d", idx1, idx2)
	}
	if calls != 1 {
		t.Errorf("loader called %d times, want 1", calls)
	}
}

// TestPrecacheSound_TableFull surfaces ErrPrecacheFull.
func TestPrecacheSound_TableFull(t *testing.T) {
	h := hostForSoundTest(t)
	// Saturate the precache table (slot 0 reserved; slots 1..N-1 fill).
	for i := 1; i < len(h.Server.SoundPrecache); i++ {
		h.Server.SoundPrecache[i] = "filler"
	}
	h.SetSoundLoader(func(string) ([]byte, bool) { return synthWAV(16), true })
	_, err := h.PrecacheSound("overflow.wav")
	if !errors.Is(err, server.ErrPrecacheFull) {
		t.Errorf("full: got %v want ErrPrecacheFull", err)
	}
}

// poolForSoundTest returns a Pool with `reserved` reserved-static slots.
func poolForSoundTest(t *testing.T, reserved int) *sound.Pool {
	t.Helper()
	p, err := sound.NewPool(reserved)
	if err != nil {
		t.Fatalf("NewPool: %v", err)
	}
	return p
}

// TestStartSound_HappyPath asserts a precached sample lands on a
// dynamic pool channel with the expected volume.
func TestStartSound_HappyPath(t *testing.T) {
	h := hostForSoundTest(t)
	h.SetSoundLoader(func(string) ([]byte, bool) { return synthWAV(100), true })
	idx, err := h.PrecacheSound("zap.wav")
	if err != nil {
		t.Fatalf("precache: %v", err)
	}
	pool := poolForSoundTest(t, 4)
	h.SetSoundPool(pool)

	slot, err := h.StartSound(42, 1, "zap.wav", 200, -1, -1)
	if err != nil {
		t.Fatalf("StartSound: %v", err)
	}
	if slot < pool.ReservedStatic {
		t.Errorf("slot %d in reserved range (< %d)", slot, pool.ReservedStatic)
	}
	ch := &pool.Channels[slot]
	if ch.Sfx != h.Sounds[idx] {
		t.Errorf("channel Sfx mismatch")
	}
	if ch.LeftVol != 200 || ch.RightVol != 200 {
		t.Errorf("volume: got L=%d R=%d want 200/200", ch.LeftVol, ch.RightVol)
	}
	if ch.EntNum != 42 || ch.EntChannel != 1 {
		t.Errorf("ent/chan: got %d/%d want 42/1", ch.EntNum, ch.EntChannel)
	}
	if h.LastSoundsStarted != 1 {
		t.Errorf("LastSoundsStarted: got %d want 1", h.LastSoundsStarted)
	}
	if pool.ActiveCount() != 1 {
		t.Errorf("pool.ActiveCount: got %d want 1", pool.ActiveCount())
	}
}

// TestStartSound_NoPool surfaces ErrNoSoundPool.
func TestStartSound_NoPool(t *testing.T) {
	h := hostForSoundTest(t)
	if _, err := h.StartSound(0, 0, "foo", 255, -1, -1); !errors.Is(err, ErrNoSoundPool) {
		t.Errorf("no pool: got %v want ErrNoSoundPool", err)
	}
	var nilHost *Host
	if _, err := nilHost.StartSound(0, 0, "foo", 255, -1, -1); !errors.Is(err, ErrNoSoundPool) {
		t.Errorf("nil host: got %v want ErrNoSoundPool", err)
	}
}

// TestStartSound_Empty short-circuits on empty name.
func TestStartSound_Empty(t *testing.T) {
	h := hostForSoundTest(t)
	h.SetSoundPool(poolForSoundTest(t, 4))
	slot, err := h.StartSound(0, 0, "", 255, -1, -1)
	if err != nil {
		t.Errorf("empty: got err %v", err)
	}
	if slot != -1 {
		t.Errorf("empty slot: got %d want -1", slot)
	}
}

// TestStartSound_NotPrecached surfaces ErrSoundNotPrecached.
func TestStartSound_NotPrecached(t *testing.T) {
	h := hostForSoundTest(t)
	h.SetSoundPool(poolForSoundTest(t, 4))
	if _, err := h.StartSound(0, 0, "missing.wav", 255, -1, -1); !errors.Is(err, ErrSoundNotPrecached) {
		t.Errorf("missing: got %v want ErrSoundNotPrecached", err)
	}
}

// TestStartSound_PrecacheButNoSample surfaces ErrSoundNotPrecached
// when the name is in the table but Sounds[idx] is nil (= the WAV
// failed to load at precache time, leaving a wire-side slot with
// no local sample).
func TestStartSound_PrecacheButNoSample(t *testing.T) {
	h := hostForSoundTest(t)
	h.Server.SoundPrecache[1] = "halfway.wav"
	h.SetSoundPool(poolForSoundTest(t, 4))
	if _, err := h.StartSound(0, 0, "halfway.wav", 255, -1, -1); !errors.Is(err, ErrSoundNotPrecached) {
		t.Errorf("no sample: got %v want ErrSoundNotPrecached", err)
	}
}

// TestStartSound_NilServer surfaces server.ErrNilServer.
func TestStartSound_NilServer(t *testing.T) {
	h := &Host{}
	h.SetSoundPool(poolForSoundTest(t, 4))
	if _, err := h.StartSound(0, 0, "foo", 255, -1, -1); !errors.Is(err, server.ErrNilServer) {
		t.Errorf("nil server: got %v want ErrNilServer", err)
	}
}

// TestStartSound_VolumeClamp covers the leftVol/rightVol > MaxVolume
// and < 0 paths.
func TestStartSound_VolumeClamp(t *testing.T) {
	h := hostForSoundTest(t)
	h.SetSoundLoader(func(string) ([]byte, bool) { return synthWAV(8), true })
	if _, err := h.PrecacheSound("v.wav"); err != nil {
		t.Fatalf("precache: %v", err)
	}
	h.SetSoundPool(poolForSoundTest(t, 4))

	// > MaxVolume clamps to MaxVolume.
	slot, err := h.StartSound(0, 0, "v.wav", 10, 999, 999)
	if err != nil {
		t.Fatalf("high vol: %v", err)
	}
	ch := &h.SoundPool().Channels[slot]
	if ch.LeftVol != sound.MaxVolume || ch.RightVol != sound.MaxVolume {
		t.Errorf("high vol: got L=%d R=%d want %d/%d", ch.LeftVol, ch.RightVol, sound.MaxVolume, sound.MaxVolume)
	}
	// < 0 clamps to 0 (note: -1 is the sentinel for "use volume", so
	// only -2 or lower triggers the negative clamp).
	slot2, err := h.StartSound(0, 1, "v.wav", -5, -2, -2)
	if err != nil {
		t.Fatalf("neg vol: %v", err)
	}
	ch2 := &h.SoundPool().Channels[slot2]
	if ch2.LeftVol != 0 || ch2.RightVol != 0 {
		t.Errorf("neg vol: got L=%d R=%d want 0/0", ch2.LeftVol, ch2.RightVol)
	}
}

// TestStartSound_PoolExhausted surfaces the pool's ErrPoolNoFreeSlot
// (every dynamic slot reserved -> Alloc has no eligible slot).
func TestStartSound_PoolExhausted(t *testing.T) {
	h := hostForSoundTest(t)
	h.SetSoundLoader(func(string) ([]byte, bool) { return synthWAV(8), true })
	if _, err := h.PrecacheSound("p.wav"); err != nil {
		t.Fatalf("precache: %v", err)
	}
	// ReservedStatic == MaxChannels leaves zero dynamic slots; Alloc
	// surfaces ErrPoolNoFreeSlot for every call.
	pool, err := sound.NewPool(sound.MaxChannels)
	if err != nil {
		t.Fatalf("NewPool: %v", err)
	}
	h.SetSoundPool(pool)
	if _, err := h.StartSound(0, 0, "p.wav", 200, -1, -1); !errors.Is(err, sound.ErrPoolNoFreeSlot) {
		t.Errorf("pool exhausted: got %v want ErrPoolNoFreeSlot", err)
	}
}

// TestAmbientSound_HappyPath parks a precached sample on a reserved-
// static channel.
func TestAmbientSound_HappyPath(t *testing.T) {
	h := hostForSoundTest(t)
	h.SetSoundLoader(func(string) ([]byte, bool) { return synthWAV(200), true })
	if _, err := h.PrecacheSound("amb.wav"); err != nil {
		t.Fatalf("precache: %v", err)
	}
	pool := poolForSoundTest(t, 8)
	h.SetSoundPool(pool)

	slot, err := h.AmbientSound(3, 99, "amb.wav", 180)
	if err != nil {
		t.Fatalf("AmbientSound: %v", err)
	}
	if slot != 3 {
		t.Errorf("slot: got %d want 3", slot)
	}
	ch := &pool.Channels[slot]
	if ch.Sfx == nil || ch.EntNum != 99 || ch.LeftVol != 180 || ch.RightVol != 180 || !ch.Master {
		t.Errorf("channel: %+v", ch)
	}
	if h.LastAmbientsStarted != 1 {
		t.Errorf("LastAmbientsStarted: got %d want 1", h.LastAmbientsStarted)
	}
}

// TestAmbientSound_Guards: nil host, no pool, empty name, nil server,
// bad slot, missing precache.
func TestAmbientSound_Guards(t *testing.T) {
	var nilHost *Host
	if _, err := nilHost.AmbientSound(0, 0, "x", 0); !errors.Is(err, ErrNoSoundPool) {
		t.Errorf("nil host: %v", err)
	}

	h := hostForSoundTest(t)
	if _, err := h.AmbientSound(0, 0, "x", 0); !errors.Is(err, ErrNoSoundPool) {
		t.Errorf("no pool: %v", err)
	}
	h.SetSoundPool(poolForSoundTest(t, 4))

	if slot, err := h.AmbientSound(0, 0, "", 0); err != nil || slot != -1 {
		t.Errorf("empty: got (%d, %v) want (-1, nil)", slot, err)
	}

	if _, err := h.AmbientSound(99, 0, "x", 0); err == nil || !strings.Contains(err.Error(), "ambient slot 99") {
		t.Errorf("bad slot: %v", err)
	}

	if _, err := h.AmbientSound(0, 0, "missing.wav", 0); !errors.Is(err, ErrSoundNotPrecached) {
		t.Errorf("missing: %v", err)
	}

	h.Server.SoundPrecache[1] = "halfway.wav"
	if _, err := h.AmbientSound(0, 0, "halfway.wav", 0); !errors.Is(err, ErrSoundNotPrecached) {
		t.Errorf("halfway: %v", err)
	}

	// Nil server.
	h2 := &Host{}
	h2.SetSoundPool(poolForSoundTest(t, 4))
	if _, err := h2.AmbientSound(0, 0, "x", 0); !errors.Is(err, server.ErrNilServer) {
		t.Errorf("nil server: %v", err)
	}
}

// TestAmbientSound_VolumeClamp covers > MaxVolume and < 0.
func TestAmbientSound_VolumeClamp(t *testing.T) {
	h := hostForSoundTest(t)
	h.SetSoundLoader(func(string) ([]byte, bool) { return synthWAV(8), true })
	if _, err := h.PrecacheSound("vv.wav"); err != nil {
		t.Fatalf("precache: %v", err)
	}
	pool := poolForSoundTest(t, 4)
	h.SetSoundPool(pool)

	slot, err := h.AmbientSound(0, 0, "vv.wav", 9999)
	if err != nil {
		t.Fatalf("high: %v", err)
	}
	if pool.Channels[slot].LeftVol != sound.MaxVolume {
		t.Errorf("high vol clamp: got %d want %d", pool.Channels[slot].LeftVol, sound.MaxVolume)
	}

	slot2, err := h.AmbientSound(1, 0, "vv.wav", -10)
	if err != nil {
		t.Fatalf("low: %v", err)
	}
	if pool.Channels[slot2].LeftVol != 0 {
		t.Errorf("neg vol clamp: got %d want 0", pool.Channels[slot2].LeftVol)
	}
}

// TestSetSoundPool_NilDetach asserts passing nil disables the pool.
func TestSetSoundPool_NilDetach(t *testing.T) {
	h := hostForSoundTest(t)
	h.SetSoundPool(poolForSoundTest(t, 2))
	if h.SoundPool() == nil {
		t.Fatal("pool not installed")
	}
	h.SetSoundPool(nil)
	if h.SoundPool() != nil {
		t.Error("pool not detached")
	}
}

// --- Spatialization -------------------------------------------------------

// TestSetListener_RoundTrip asserts SetListener / HasListener wire +
// the nil-host guard returns false.
func TestSetListener_RoundTrip(t *testing.T) {
	var nilHost *Host
	if nilHost.HasListener() {
		t.Error("nil host should not report HasListener")
	}
	nilHost.SetListener([3]float32{}, [3]float32{}) // must not panic
	h := hostForSoundTest(t)
	if h.HasListener() {
		t.Error("fresh host should not report HasListener")
	}
	h.SetListener([3]float32{1, 2, 3}, [3]float32{0, 1, 0})
	if !h.HasListener() {
		t.Error("HasListener should be true after SetListener")
	}
}

// TestStartSoundAt_NoListenerFallsThrough asserts that without a wired
// listener StartSoundAt degrades to the existing StartSound behaviour
// (= L == R == master volume).
func TestStartSoundAt_NoListenerFallsThrough(t *testing.T) {
	h := hostForSoundTest(t)
	h.SetSoundLoader(func(string) ([]byte, bool) { return synthWAV(64), true })
	if _, err := h.PrecacheSound("zap.wav"); err != nil {
		t.Fatalf("precache: %v", err)
	}
	pool := poolForSoundTest(t, 4)
	h.SetSoundPool(pool)

	slot, err := h.StartSoundAt(0, 0, "zap.wav", 200, sound.AttenuationNormal,
		[3]float32{100, 0, 0})
	if err != nil {
		t.Fatalf("StartSoundAt: %v", err)
	}
	ch := &pool.Channels[slot]
	if ch.LeftVol != 200 || ch.RightVol != 200 {
		t.Errorf("no-listener fall-through: got L=%d R=%d want 200/200", ch.LeftVol, ch.RightVol)
	}
}

// TestStartSoundAt_AttenuationNoneFallsThrough asserts that
// AttenuationNone short-circuits the spatialize path even with a
// wired listener (= UI / global sounds stay loud everywhere).
func TestStartSoundAt_AttenuationNoneFallsThrough(t *testing.T) {
	h := hostForSoundTest(t)
	h.SetSoundLoader(func(string) ([]byte, bool) { return synthWAV(64), true })
	if _, err := h.PrecacheSound("ui.wav"); err != nil {
		t.Fatalf("precache: %v", err)
	}
	pool := poolForSoundTest(t, 4)
	h.SetSoundPool(pool)
	h.SetListener([3]float32{0, 0, 0}, [3]float32{0, 1, 0})

	slot, err := h.StartSoundAt(0, 0, "ui.wav", 180, sound.AttenuationNone,
		[3]float32{500, 0, 0})
	if err != nil {
		t.Fatalf("StartSoundAt: %v", err)
	}
	ch := &pool.Channels[slot]
	if ch.LeftVol != 180 || ch.RightVol != 180 {
		t.Errorf("AttenuationNone: got L=%d R=%d want 180/180", ch.LeftVol, ch.RightVol)
	}
}

// TestStartSoundAt_SpatializeCentered asserts a sound directly in
// front of the listener (along the forward axis -- orthogonal to
// the right axis) gives balanced L/R volumes, both reduced by the
// distance attenuation.
func TestStartSoundAt_SpatializeCentered(t *testing.T) {
	h := hostForSoundTest(t)
	h.SetSoundLoader(func(string) ([]byte, bool) { return synthWAV(64), true })
	if _, err := h.PrecacheSound("fwd.wav"); err != nil {
		t.Fatalf("precache: %v", err)
	}
	pool := poolForSoundTest(t, 4)
	h.SetSoundPool(pool)
	// Listener at origin, right axis = +Y; source at +X (in front of
	// the listener -- the dot(relative, right) projection is 0).
	h.SetListener([3]float32{0, 0, 0}, [3]float32{0, 1, 0})

	slot, err := h.StartSoundAt(0, 0, "fwd.wav", 200, sound.AttenuationNormal,
		[3]float32{100, 0, 0})
	if err != nil {
		t.Fatalf("StartSoundAt: %v", err)
	}
	ch := &pool.Channels[slot]
	// Balance == 0 -> leftScale == rightScale == 0.5 -> L == R.
	if ch.LeftVol != ch.RightVol {
		t.Errorf("centered source: L=%d R=%d want L==R", ch.LeftVol, ch.RightVol)
	}
	// Distance attenuation: master = 1 - 100*0.001*1 = 0.9 -> scaled
	// base = 200*0.9 = 180; each ear gets 180 * 0.5 = 90.
	if ch.LeftVol != 90 {
		t.Errorf("centered source: L=%d want 90", ch.LeftVol)
	}
}

// TestStartSoundAt_SpatializeRight asserts a sound to the right of
// the listener (along the right axis) drives right > left.
func TestStartSoundAt_SpatializeRight(t *testing.T) {
	h := hostForSoundTest(t)
	h.SetSoundLoader(func(string) ([]byte, bool) { return synthWAV(64), true })
	if _, err := h.PrecacheSound("right.wav"); err != nil {
		t.Fatalf("precache: %v", err)
	}
	pool := poolForSoundTest(t, 4)
	h.SetSoundPool(pool)
	h.SetListener([3]float32{0, 0, 0}, [3]float32{0, 1, 0})

	slot, err := h.StartSoundAt(0, 0, "right.wav", 200, sound.AttenuationNormal,
		[3]float32{0, 100, 0})
	if err != nil {
		t.Fatalf("StartSoundAt: %v", err)
	}
	ch := &pool.Channels[slot]
	if ch.RightVol <= ch.LeftVol {
		t.Errorf("right-of-listener: got L=%d R=%d want R>L", ch.LeftVol, ch.RightVol)
	}
}

// TestStartSoundAt_FarSourceAttenuated asserts a sound at the
// SoundFalloffDist threshold collapses to zero (1 - 1000*0.001*1 = 0).
func TestStartSoundAt_FarSourceAttenuated(t *testing.T) {
	h := hostForSoundTest(t)
	h.SetSoundLoader(func(string) ([]byte, bool) { return synthWAV(64), true })
	if _, err := h.PrecacheSound("far.wav"); err != nil {
		t.Fatalf("precache: %v", err)
	}
	pool := poolForSoundTest(t, 4)
	h.SetSoundPool(pool)
	h.SetListener([3]float32{0, 0, 0}, [3]float32{0, 1, 0})

	slot, err := h.StartSoundAt(0, 0, "far.wav", 200, sound.AttenuationNormal,
		[3]float32{2000, 0, 0})
	if err != nil {
		t.Fatalf("StartSoundAt: %v", err)
	}
	ch := &pool.Channels[slot]
	if ch.LeftVol != 0 || ch.RightVol != 0 {
		t.Errorf("far source: got L=%d R=%d want 0/0", ch.LeftVol, ch.RightVol)
	}
}

// TestStartSoundAt_NilPool surfaces ErrNoSoundPool (mirrors the
// StartSound guard).
func TestStartSoundAt_NilPool(t *testing.T) {
	h := hostForSoundTest(t)
	if _, err := h.StartSoundAt(0, 0, "x", 0, sound.AttenuationNormal,
		[3]float32{}); !errors.Is(err, ErrNoSoundPool) {
		t.Errorf("no pool: %v", err)
	}
	var nilHost *Host
	if _, err := nilHost.StartSoundAt(0, 0, "x", 0, sound.AttenuationNormal,
		[3]float32{}); !errors.Is(err, ErrNoSoundPool) {
		t.Errorf("nil host: %v", err)
	}
}

// TestAmbientSoundAt_NoListenerFallsThrough asserts the no-listener
// branch parks the ambient at full master volume.
func TestAmbientSoundAt_NoListenerFallsThrough(t *testing.T) {
	h := hostForSoundTest(t)
	h.SetSoundLoader(func(string) ([]byte, bool) { return synthWAV(64), true })
	if _, err := h.PrecacheSound("amb.wav"); err != nil {
		t.Fatalf("precache: %v", err)
	}
	pool := poolForSoundTest(t, 4)
	h.SetSoundPool(pool)

	slot, err := h.AmbientSoundAt(0, 0, "amb.wav", 150, [3]float32{100, 0, 0}, sound.AttenuationStatic)
	if err != nil {
		t.Fatalf("AmbientSoundAt: %v", err)
	}
	ch := &pool.Channels[slot]
	if ch.LeftVol != 150 || ch.RightVol != 150 {
		t.Errorf("no-listener fall-through: got L=%d R=%d want 150/150", ch.LeftVol, ch.RightVol)
	}
	if !ch.Master {
		t.Error("ambient channel should have Master=true")
	}
}

// TestAmbientSoundAt_Spatializes asserts the right-of-listener anchor
// drives right > left on the ambient channel.
func TestAmbientSoundAt_Spatializes(t *testing.T) {
	h := hostForSoundTest(t)
	h.SetSoundLoader(func(string) ([]byte, bool) { return synthWAV(64), true })
	if _, err := h.PrecacheSound("water.wav"); err != nil {
		t.Fatalf("precache: %v", err)
	}
	pool := poolForSoundTest(t, 4)
	h.SetSoundPool(pool)
	h.SetListener([3]float32{0, 0, 0}, [3]float32{0, 1, 0})

	slot, err := h.AmbientSoundAt(1, 0, "water.wav", 200, [3]float32{0, 100, 0},
		sound.AttenuationNormal)
	if err != nil {
		t.Fatalf("AmbientSoundAt: %v", err)
	}
	ch := &pool.Channels[slot]
	if ch.RightVol <= ch.LeftVol {
		t.Errorf("right-of-listener: got L=%d R=%d want R>L", ch.LeftVol, ch.RightVol)
	}
	if !ch.Master {
		t.Error("ambient channel should retain Master=true")
	}
}

// TestAmbientSoundAt_NilPool surfaces ErrNoSoundPool.
func TestAmbientSoundAt_NilPool(t *testing.T) {
	h := hostForSoundTest(t)
	if _, err := h.AmbientSoundAt(0, 0, "x", 0, [3]float32{}, sound.AttenuationNormal); !errors.Is(err, ErrNoSoundPool) {
		t.Errorf("no pool: %v", err)
	}
	var nilHost *Host
	if _, err := nilHost.AmbientSoundAt(0, 0, "x", 0, [3]float32{}, sound.AttenuationNormal); !errors.Is(err, ErrNoSoundPool) {
		t.Errorf("nil host: %v", err)
	}
}

// TestAmbientSoundAt_PropagatesErrors asserts the spatializing variant
// surfaces errors from the underlying AmbientSound call (e.g. bad
// slot index).
func TestAmbientSoundAt_PropagatesErrors(t *testing.T) {
	h := hostForSoundTest(t)
	h.SetSoundLoader(func(string) ([]byte, bool) { return synthWAV(64), true })
	if _, err := h.PrecacheSound("a.wav"); err != nil {
		t.Fatalf("precache: %v", err)
	}
	pool := poolForSoundTest(t, 4)
	h.SetSoundPool(pool)
	h.SetListener([3]float32{0, 0, 0}, [3]float32{0, 1, 0})

	// Out-of-range slot: AmbientSound returns the formatted error.
	if _, err := h.AmbientSoundAt(99, 0, "a.wav", 200, [3]float32{0, 100, 0},
		sound.AttenuationNormal); err == nil {
		t.Error("bad slot: expected error, got nil")
	}
}

// TestEnsureSoundsLen_Geometry covers the cap-doubling branch and
// the negative-index defence.
func TestEnsureSoundsLen_Geometry(t *testing.T) {
	h := &Host{}
	if h.ensureSoundsLen(-1) {
		t.Error("ensureSoundsLen(-1) should return false")
	}
	// First grow: exact (cap == idx+1).
	if !h.ensureSoundsLen(3) {
		t.Fatal("ensureSoundsLen(3) should succeed")
	}
	if len(h.Sounds) != 4 {
		t.Errorf("len: got %d want 4", len(h.Sounds))
	}
	// Second grow: should double cap.
	c0 := cap(h.Sounds)
	if !h.ensureSoundsLen(10) {
		t.Fatal("ensureSoundsLen(10) should succeed")
	}
	if cap(h.Sounds) < 11 {
		t.Errorf("cap not grown: got %d want >= 11 (prev cap %d)", cap(h.Sounds), c0)
	}
	// Within cap: should be a no-grow slice extend.
	h.Sounds = h.Sounds[:5]
	c1 := cap(h.Sounds)
	if !h.ensureSoundsLen(8) {
		t.Fatal("ensureSoundsLen(8) should succeed")
	}
	if cap(h.Sounds) != c1 {
		t.Errorf("cap changed for in-cap grow: got %d want %d", cap(h.Sounds), c1)
	}

	// Hit the "cap*2 > want" branch: start from a small cap, ask for
	// a size that fits inside cap*2 but exceeds cap (so the geometric-
	// growth newCap = cap*2 path runs instead of the linear newCap = want).
	h2 := &Host{}
	h2.Sounds = make([]*sound.Sample, 4, 4) // cap=4
	if !h2.ensureSoundsLen(5) {             // want=6, cap*2=8 > want → newCap=8
		t.Fatal("ensureSoundsLen(5) should succeed")
	}
	if cap(h2.Sounds) != 8 {
		t.Errorf("geometric grow cap: got %d want 8", cap(h2.Sounds))
	}
}
