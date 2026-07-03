// Copyright (c) 1996-1997 Id Software, Inc.
// Copyright (c) 2026 the go-quake1/engine authors.
// SPDX-License-Identifier: GPL-2.0-or-later

package runloop

import (
	"errors"
	"testing"

	"github.com/go-quake1/engine/backend"
	"github.com/go-quake1/engine/client"
	"github.com/go-quake1/engine/render"
	"github.com/go-quake1/engine/server"
)

// loopFakeHost satisfies HostFramer for the RunUntilQuit tests.
type loopFakeHost struct {
	frames int
	err    error
}

func (h *loopFakeHost) Frame(_ float32) error {
	h.frames++
	return h.err
}

// quitAfterBackend wraps backend.Recorder + flips QuitRequested
// after `framesUntilQuit` PollInput calls.
type quitAfterBackend struct {
	*backend.Recorder
	framesUntilQuit int
	pollErr         error
}

func (q *quitAfterBackend) PollInput() (backend.InputSnapshot, error) {
	q.Recorder.PollCount++
	if q.pollErr != nil {
		return backend.InputSnapshot{}, q.pollErr
	}
	if q.Recorder.PollCount > q.framesUntilQuit {
		return backend.InputSnapshot{QuitRequested: true}, nil
	}
	return backend.InputSnapshot{}, nil
}

func newLoopRunner(t *testing.T, fb *backend.Recorder) *Runner {
	t.Helper()
	pic := &render.Pic{Width: 128, Height: 128, Pixels: make([]byte, 128*128)}
	con, _ := render.NewConsole(render.MinConsoleWidth, render.MinConsoleLines)
	screen, _ := render.NewScreen(320, 200)
	rfb, _ := render.NewFrameBuffer(320, 200)
	var pal render.Palette
	cli, _ := server.NewLoopbackConn()
	state := client.NewState()

	return &Runner{
		Host:        &loopFakeHost{},
		Client:      state,
		Conn:        cli,
		Backend:     fb,
		FrameBuffer: rfb,
		Console:     con,
		Screen:      screen,
		Chars:       pic,
		Palette:     &pal,
		RGBA:        make([]byte, 320*200*4),
	}
}

func TestRunUntilQuit_QuitsAfterNFrames(t *testing.T) {
	rec := backend.NewRecorder(320, 200)
	rec.NowVal = 0 // constant clock -> dt clamps to MinFrameTime each frame
	q := &quitAfterBackend{Recorder: rec, framesUntilQuit: 3}
	r := newLoopRunner(t, rec)
	r.Backend = q
	// One fixed sim tick per frame so the (time-driven) host-tick count
	// matches the render-frame count for this loop-termination test.
	r.SimStep = MinFrameTime
	if err := r.RunUntilQuit(); err != nil {
		t.Fatalf("RunUntilQuit: %v", err)
	}
	// framesUntilQuit=3 means PollInput #4 raises QuitRequested.
	// The loop's quit-check happens AFTER RunFrame, so the 4th
	// RunFrame runs to completion before exit (current-tic-completes
	// game-loop semantics). Expect 4 host.Frame calls.
	if h := r.Host.(*loopFakeHost); h.frames != 4 {
		t.Fatalf("host frames = %d want 4", h.frames)
	}
	if len(rec.Frames) != 4 {
		t.Fatalf("rec frames = %d want 4", len(rec.Frames))
	}
}

func TestRunFrame_FixedTimestepAccumulator(t *testing.T) {
	rec := backend.NewRecorder(320, 200)
	r := newLoopRunner(t, rec)
	r.SimStep = 0.05
	h := r.Host.(*loopFakeHost)

	// dt = 0.16 -> floor(0.16/0.05) = 3 fixed ticks, 0.01 remainder banked.
	if err := r.RunFrame(0.16, 1); err != nil {
		t.Fatalf("RunFrame: %v", err)
	}
	if h.frames != 3 {
		t.Fatalf("first frame ticks = %d want 3", h.frames)
	}
	// dt = 0.045 -> 0.01 banked + 0.045 = 0.055 -> 1 tick (remainder carried).
	if err := r.RunFrame(0.045, 2); err != nil {
		t.Fatalf("RunFrame: %v", err)
	}
	if h.frames != 4 {
		t.Fatalf("second frame ticks = %d want 4 (remainder carried)", h.frames)
	}
}

func TestRunFrame_SubstepCapPreventsSpiral(t *testing.T) {
	rec := backend.NewRecorder(320, 200)
	r := newLoopRunner(t, rec)
	r.SimStep = 0.05
	h := r.Host.(*loopFakeHost)
	// A 10s stall would be 200 ticks; the cap bounds it to maxSimSubsteps.
	if err := r.RunFrame(10, 1); err != nil {
		t.Fatalf("RunFrame: %v", err)
	}
	if h.frames != maxSimSubsteps {
		t.Fatalf("capped ticks = %d want %d", h.frames, maxSimSubsteps)
	}
}

func TestRunFrame_ZeroSimStepUsesDefault(t *testing.T) {
	rec := backend.NewRecorder(320, 200)
	r := newLoopRunner(t, rec) // SimStep left 0 -> DefaultSimStep (0.05)
	h := r.Host.(*loopFakeHost)
	if err := r.RunFrame(DefaultSimStep, 1); err != nil {
		t.Fatalf("RunFrame: %v", err)
	}
	if h.frames != 1 {
		t.Fatalf("default-step ticks = %d want 1", h.frames)
	}
}

func TestRunUntilQuit_PollErrorPropagates(t *testing.T) {
	rec := backend.NewRecorder(320, 200)
	myErr := errors.New("poll boom")
	q := &quitAfterBackend{Recorder: rec, pollErr: myErr}
	r := newLoopRunner(t, rec)
	r.Backend = q
	err := r.RunUntilQuit()
	if !errors.Is(err, myErr) {
		t.Fatalf("err = %v want poll boom", err)
	}
}

func TestRunUntilQuit_RunFrameErrorPropagates(t *testing.T) {
	rec := backend.NewRecorder(320, 200)
	q := &quitAfterBackend{Recorder: rec, framesUntilQuit: 100}
	r := newLoopRunner(t, rec)
	r.Backend = q
	hostErr := errors.New("host frame boom")
	r.Host = &loopFakeHost{err: hostErr}
	err := r.RunUntilQuit()
	if !errors.Is(err, hostErr) {
		t.Fatalf("err = %v want host frame boom", err)
	}
}

func TestRunUntilQuit_NilHost(t *testing.T) {
	rec := backend.NewRecorder(320, 200)
	r := newLoopRunner(t, rec)
	r.Host = nil
	if err := r.RunUntilQuit(); !errors.Is(err, ErrRunnerNilHost) {
		t.Fatalf("err = %v want ErrRunnerNilHost", err)
	}
}

func TestRunUntilQuit_NilClient(t *testing.T) {
	rec := backend.NewRecorder(320, 200)
	r := newLoopRunner(t, rec)
	r.Client = nil
	if err := r.RunUntilQuit(); !errors.Is(err, ErrRunnerNilClient) {
		t.Fatalf("err = %v want ErrRunnerNilClient", err)
	}
}

func TestRunUntilQuit_NilConn(t *testing.T) {
	rec := backend.NewRecorder(320, 200)
	r := newLoopRunner(t, rec)
	r.Conn = nil
	if err := r.RunUntilQuit(); !errors.Is(err, ErrRunnerNilConn) {
		t.Fatalf("err = %v want ErrRunnerNilConn", err)
	}
}

func TestRunUntilQuit_NilBackend(t *testing.T) {
	rec := backend.NewRecorder(320, 200)
	r := newLoopRunner(t, rec)
	r.Backend = nil
	if err := r.RunUntilQuit(); !errors.Is(err, ErrRunnerNilBackend) {
		t.Fatalf("err = %v want ErrRunnerNilBackend", err)
	}
}

func TestRunUntilQuit_NilFB(t *testing.T) {
	rec := backend.NewRecorder(320, 200)
	r := newLoopRunner(t, rec)
	r.FrameBuffer = nil
	if err := r.RunUntilQuit(); !errors.Is(err, ErrRunnerNilFB) {
		t.Fatalf("err = %v want ErrRunnerNilFB", err)
	}
}

func TestRunUntilQuit_DtClampedToMin(t *testing.T) {
	// Backend reports Now == 0 every call; dt becomes 0 and should
	// be clamped to MinFrameTime. Verify by checking RunFrame was
	// invoked (no nil-arg / panic) and the fixed-timestep sim advanced.
	rec := backend.NewRecorder(320, 200)
	rec.NowVal = 0
	q := &quitAfterBackend{Recorder: rec, framesUntilQuit: 1}
	r := newLoopRunner(t, rec)
	r.Backend = q
	// Match the fixed sim step to the clamped render dt so each frame
	// advances the sim exactly once (the default 0.05 SimStep would need
	// ~12 MinFrameTime frames to accumulate a single tick).
	r.SimStep = MinFrameTime
	if err := r.RunUntilQuit(); err != nil {
		t.Fatalf("RunUntilQuit: %v", err)
	}
	h := r.Host.(*loopFakeHost)
	// framesUntilQuit=1 -> PollInput #2 raises Quit; current frame
	// completes -> 2 host.Frame calls (one fixed tick each).
	if h.frames != 2 {
		t.Fatalf("host frames = %d want 2", h.frames)
	}
}
