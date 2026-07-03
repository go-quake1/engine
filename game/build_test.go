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

// quitBackend is a scriptBackend that flips QuitRequested after a fixed
// number of PollInput calls so RunUntilQuit terminates. It can also
// inject a key/mouse snapshot per tic to drive input-observed branches.
type quitBackend struct {
	w, h      int
	snap      backend.InputSnapshot
	polls     int
	quitAfter int
	frames    int
	tick      int
}

func (b *quitBackend) PresentFrame(rgba []byte, w, h int) error { b.frames++; return nil }
func (b *quitBackend) Size() (int, int)                         { return b.w, b.h }
func (b *quitBackend) QueueAudio(_ []sound.StereoSample) error  { return nil }
func (b *quitBackend) SampleRate() int                          { return 22050 }
func (b *quitBackend) PollInput() (backend.InputSnapshot, error) {
	b.polls++
	s := b.snap
	if b.polls >= b.quitAfter {
		s.QuitRequested = true
	}
	return s, nil
}
func (b *quitBackend) Now() float64 {
	t := float64(b.tick) * (1.0 / 20.0)
	b.tick++
	return t
}

// slowClockBackend reports a near-frozen clock so RunUntilQuit's
// nextDt < MinFrameTime clamp branch runs (consecutive Now() values
// differ by less than one tic).
type slowClockBackend struct {
	w, h      int
	polls     int
	quitAfter int
	now       float64
}

func (b *slowClockBackend) PresentFrame(_ []byte, _, _ int) error { return nil }
func (b *slowClockBackend) Size() (int, int)                      { return b.w, b.h }
func (b *slowClockBackend) QueueAudio(_ []sound.StereoSample) error {
	return nil
}
func (b *slowClockBackend) SampleRate() int { return 22050 }
func (b *slowClockBackend) PollInput() (backend.InputSnapshot, error) {
	b.polls++
	return backend.InputSnapshot{QuitRequested: b.polls >= b.quitAfter}, nil
}
func (b *slowClockBackend) Now() float64 {
	b.now += 1e-6 // << MinFrameTime (1/240 s)
	return b.now
}

// TestRunUntilQuitDtClamp drives RunUntilQuit with a near-frozen clock so
// the per-iteration "nextDt below MinFrameTime -> clamp" branch runs.
func TestRunUntilQuitDtClamp(t *testing.T) {
	be := &slowClockBackend{w: 64, h: 48, quitAfter: 4}
	sess, err := Build(nil, be, Options{})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if err := sess.RunUntilQuit(); err != nil {
		t.Fatalf("RunUntilQuit: %v", err)
	}
}

// TestBuildDemoOrbitAndOnFrame drives Build with DemoOrbit + OnFrame +
// a non-default Map + FieldOfView, then runs enough frames with injected
// input to exercise the demo-orbit waypoint cycle AND the auto-disable
// branch (observedAnyInput flips on a held movement key).
func TestBuildDemoOrbitAndOnFrame(t *testing.T) {
	pakFS, err := embedpak.OpenAsFS()
	if err != nil {
		t.Skipf("no real pak embedded (%v)", err)
	}

	var frameCount int
	be := &scriptBackend{w: 320, h: 240}
	sess, err := Build(pakFS, be, Options{
		Map:         "start",
		DemoOrbit:   true,
		FieldOfView: 100,
		OnFrame: func(frame int, origin, angles [3]float32) {
			frameCount++
		},
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	const dt = float32(1.0 / 20.0)
	// First run several frames with NO input so the demo-orbit waypoint
	// cycle override runs (viewOrigin replaced, frame%360 yaw).
	for f := 0; f < 20; f++ {
		if err := sess.Runner.RunFrame(dt, float32(f)*dt); err != nil {
			t.Fatalf("RunFrame demo %d: %v", f, err)
		}
	}
	if frameCount == 0 {
		t.Fatal("OnFrame callback never fired")
	}

	// Now hold forward so observedAnyInput flips and demo-orbit auto-
	// disables; keep going so the input-driven camera path runs.
	for f := 20; f < 60; f++ {
		be.snap = backend.InputSnapshot{KeysDown: []backend.KeyCode{backend.KeyW}}
		if err := sess.Runner.RunFrame(dt, float32(f)*dt); err != nil {
			t.Fatalf("RunFrame input %d: %v", f, err)
		}
	}
}

// TestRunUntilQuit drives the cooperative RunUntilQuit loop to its quit
// exit on both the synth and the real path.
func TestRunUntilQuitSynth(t *testing.T) {
	be := &quitBackend{w: 320, h: 240, quitAfter: 3}
	sess, err := Build(nil, be, Options{})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if err := sess.RunUntilQuit(); err != nil {
		t.Fatalf("RunUntilQuit: %v", err)
	}
	if be.frames == 0 {
		t.Fatal("no frames presented")
	}
}

func TestRunUntilQuitReal(t *testing.T) {
	pakFS, err := embedpak.OpenAsFS()
	if err != nil {
		t.Skipf("no real pak embedded (%v)", err)
	}
	be := &quitBackend{w: 320, h: 240, quitAfter: 12}
	sess, err := Build(pakFS, be, Options{Map: "start"})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if err := sess.RunUntilQuit(); err != nil {
		t.Fatalf("RunUntilQuit: %v", err)
	}
}

// TestBuildLongRunReal drives many frames with mouse + movement +
// trigger input so the per-tic renderer's projectile-trail, alias-entity,
// particle, sprite, and beam branches get a chance to run as the QC
// simulation spawns entities, and observedAnyInput's trigger arms run.
func TestBuildLongRunReal(t *testing.T) {
	pakFS, err := embedpak.OpenAsFS()
	if err != nil {
		t.Skipf("no real pak embedded (%v)", err)
	}
	be := &scriptBackend{w: 160, h: 120}
	sess, err := Build(pakFS, be, Options{Map: "start"})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	const dt = float32(1.0 / 20.0)
	for f := 0; f < 200; f++ {
		be.snap = backend.InputSnapshot{}
		switch {
		case f%5 == 0:
			be.snap.KeysDown = []backend.KeyCode{backend.KeyW}
			be.snap.MouseDX = 8
		case f%5 == 1:
			be.snap.KeysDown = []backend.KeyCode{backend.KeyS}
			be.snap.MouseDY = -4
		case f%5 == 2:
			be.snap.KeysDown = []backend.KeyCode{backend.KeyA, backend.KeySpace}
		case f%5 == 3:
			be.snap.KeysDown = []backend.KeyCode{backend.KeyD, backend.KeyMouse1}
		}
		if err := sess.Runner.RunFrame(dt, float32(f)*dt); err != nil {
			t.Fatalf("RunFrame %d: %v", f, err)
		}
	}
}
