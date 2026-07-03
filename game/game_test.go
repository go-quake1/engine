// Copyright (c) 1996-1997 Id Software, Inc.
// Copyright (c) 2026 the go-quake1/engine authors.
// SPDX-License-Identifier: GPL-2.0-or-later

package game

import (
	"testing"

	"github.com/go-quake1/engine/backend"
	"github.com/go-quake1/engine/embedpak"
	"github.com/go-quake1/engine/sound"
)

// scriptBackend is a Recorder-like backend that lets a test inject a
// per-tic InputSnapshot + observe presented frames. It implements the
// full backend.Backend contract.
type scriptBackend struct {
	w, h   int
	snap   backend.InputSnapshot
	frames [][]byte
	tick   int
}

func (b *scriptBackend) PresentFrame(rgba []byte, w, h int) error {
	cp := make([]byte, len(rgba))
	copy(cp, rgba)
	b.frames = append(b.frames, cp)
	return nil
}
func (b *scriptBackend) Size() (int, int)                        { return b.w, b.h }
func (b *scriptBackend) QueueAudio(_ []sound.StereoSample) error { return nil }
func (b *scriptBackend) SampleRate() int                         { return 22050 }
func (b *scriptBackend) PollInput() (backend.InputSnapshot, error) {
	return b.snap, nil
}
func (b *scriptBackend) Now() float64 {
	t := float64(b.tick) * (1.0 / 20.0)
	b.tick++
	return t
}

// distinctColors counts the unique RGB triples in an RGBA frame. A
// real rendered Quake scene yields hundreds; a flat clear yields ~1.
func distinctColors(rgba []byte) int {
	seen := map[uint32]struct{}{}
	for i := 0; i+3 < len(rgba); i += 4 {
		key := uint32(rgba[i])<<16 | uint32(rgba[i+1])<<8 | uint32(rgba[i+2])
		seen[key] = struct{}{}
	}
	return len(seen)
}

// TestBuildRendersRealLevel proves Build wires the real loop: a real
// start.bsp renders from the player viewpoint (a richly-coloured frame,
// not a flat clear) and injected forward input moves the player origin.
//
// Skips when the embedded pak is the 12-byte placeholder (CI without
// the real id1 archive) -- the wiring is still exercised by the
// placeholder/synth path in TestBuildSynthFallback below.
func TestBuildRendersRealLevel(t *testing.T) {
	pakFS, err := embedpak.OpenAsFS()
	if err != nil {
		t.Skipf("no real pak embedded (%v); skipping real-level render test", err)
	}

	be := &scriptBackend{w: 320, h: 240}
	sess, err := Build(pakFS, be, Options{Map: "start"})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if sess.Host == nil {
		t.Fatal("expected a real host with the real pak")
	}
	if sess.PlayerSlot != 1 {
		t.Fatalf("PlayerSlot = %d, want 1", sess.PlayerSlot)
	}

	const dt = float32(1.0 / 20.0)
	// Warm up: signon + first svc_updates so the player entity lands in
	// State.Entities and the renderer has a real camera anchor.
	for f := 0; f < 10; f++ {
		if err := sess.Runner.RunFrame(dt, float32(f)*dt); err != nil {
			t.Fatalf("RunFrame(%d): %v", f, err)
		}
	}

	// The most recent frame must be a real rendered scene.
	last := be.frames[len(be.frames)-1]
	if dc := distinctColors(last); dc < 32 {
		t.Fatalf("rendered frame has only %d distinct colours; expected a real textured scene (>=32)", dc)
	}

	// Player entity must have been received over the wire.
	es, ok := sess.Client.Entities[sess.Client.PlayerNum]
	if !ok {
		t.Fatal("player entity not received in State.Entities after signon")
	}
	startOrigin := es.Origin

	// Inject forward (W) for a run of ticks; the player must move.
	for f := 10; f < 40; f++ {
		be.snap = backend.InputSnapshot{}
		if f == 10 {
			be.snap.KeysDown = []backend.KeyCode{backend.KeyW}
		}
		if err := sess.Runner.RunFrame(dt, float32(f)*dt); err != nil {
			t.Fatalf("RunFrame(%d): %v", f, err)
		}
	}
	endOrigin := sess.Client.Entities[sess.Client.PlayerNum].Origin

	moved := dist2(startOrigin, endOrigin)
	if moved < 16 {
		t.Fatalf("player did not move under forward input: start=%v end=%v (moved %.1f units)", startOrigin, endOrigin, moved)
	}
	t.Logf("player moved %.1f units under forward input: %v -> %v", moved, startOrigin, endOrigin)
}

// TestBuildSynthFallback exercises the no-pak path: Build must still
// produce a usable runner that renders the synthbsp scene without a
// real host.
func TestBuildSynthFallback(t *testing.T) {
	be := &scriptBackend{w: 320, h: 240}
	sess, err := Build(nil, be, Options{})
	if err != nil {
		t.Fatalf("Build(nil pak): %v", err)
	}
	if sess.Host != nil {
		t.Fatal("expected stub host on the nil-pak path")
	}
	const dt = float32(1.0 / 20.0)
	for f := 0; f < 4; f++ {
		if err := sess.Runner.RunFrame(dt, float32(f)*dt); err != nil {
			t.Fatalf("RunFrame(%d): %v", f, err)
		}
	}
	if len(be.frames) == 0 {
		t.Fatal("no frames presented on the synth path")
	}
}

func dist2(a, b [3]float32) float32 {
	dx := a[0] - b[0]
	dy := a[1] - b[1]
	dz := a[2] - b[2]
	d := dx*dx + dy*dy + dz*dz
	// integer-ish sqrt via Newton (avoid importing math for a test
	// helper that only needs a rough magnitude).
	if d == 0 {
		return 0
	}
	x := d
	for i := 0; i < 20; i++ {
		x = 0.5 * (x + d/x)
	}
	return x
}
