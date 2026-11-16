// Copyright (c) 1996-1997 Id Software, Inc.
// Copyright (c) 2026 the go-quake1/engine authors.
// SPDX-License-Identifier: GPL-2.0-or-later

package runloop

import "github.com/go-quake1/engine/backend"

// MinFrameTime is the lower bound on the per-tic dt that
// RunUntilQuit feeds RunFrame. Protects against the first iteration
// (dt=0) and against backends whose clock granularity is coarser
// than one tic.
const MinFrameTime float32 = 1.0 / 240.0 // ~4.17 ms

// RunUntilQuit drives a blocking game loop: each iteration calls
// Runner.RunFrame; returns when:
//   - the backend reports QuitRequested in any InputSnapshot
//     observed during RunFrame's PollInput, OR
//   - PollInput, RunFrame, or any backend call returns a non-nil
//     error (returned verbatim)
//
// RunFrame already calls Backend.PollInput once per tic; RunUntilQuit
// must NOT poll itself (that would double-poll). Instead, the loop
// wraps the runner's backend in a small observer that flags
// QuitRequested when it slides past the wrapper.
//
// First iteration's dt: MinFrameTime (no prior Now() reading; the
// engine wouldn't accept dt=0). Subsequent dt: clamped to >=
// MinFrameTime (handles low-resolution clocks).
//
// tyrquake: the Host_Frame outer while-loop in sys_unix.c / sys_win.c.
func (r *Runner) RunUntilQuit() error {
	if err := r.validate(); err != nil {
		return err
	}

	// Wrap the backend so we observe QuitRequested as it slides
	// past RunFrame's PollInput. Restore the original backend on
	// exit so the runner is reusable after RunUntilQuit returns.
	original := r.Backend
	defer func() { r.Backend = original }()
	obs := &quitObserver{Backend: original}
	r.Backend = backend.ForwardQueue(obs, original)

	lastNow := original.Now()
	dt := MinFrameTime
	for {
		nowSec := original.Now()
		if err := r.RunFrame(dt, float32(nowSec)); err != nil {
			return err
		}
		if obs.quitRequested {
			return nil
		}
		nextDt := float32(nowSec - lastNow)
		if nextDt < MinFrameTime {
			nextDt = MinFrameTime
		}
		lastNow = nowSec
		dt = nextDt
	}
}

// quitObserver wraps a Backend, flagging QuitRequested when an
// InputSnapshot bearing the flag passes through its PollInput.
// Everything else passes through verbatim.
type quitObserver struct {
	backend.Backend
	quitRequested bool
}

func (q *quitObserver) PollInput() (backend.InputSnapshot, error) {
	snap, err := q.Backend.PollInput()
	if snap.QuitRequested {
		q.quitRequested = true
	}
	return snap, err
}

// validate runs the same nil-arg checks RunFrame does, but as a
// standalone pre-flight check. Necessary because RunUntilQuit
// accesses r.Backend.Now BEFORE the first RunFrame, so a nil
// Backend would NPE before RunFrame's validation runs.
func (r *Runner) validate() error {
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
	return nil
}
