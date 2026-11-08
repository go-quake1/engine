// Copyright (c) 1996-1997 Id Software, Inc.
// Copyright (c) 2026 the go-quake1/engine authors.
// SPDX-License-Identifier: GPL-2.0-or-later

// Package wasisurface is the engine's backend for a wasip1 guest that
// shows its frames through wasi-gfx:surface (package wasigpu of
// github.com/go-wasmcontainers/gpu): the surface is a pane of the
// playground's dashboard.
//
// Video: PresentFrame is a no-op, as in cmd/quake-wasm. The frame reaches
// the surface through a render.Presenter (package render/wasipresent) that
// the renderer calls from Resolve, so the RGBA buffer the runner passes
// carries nothing. Input: the surface's key-down and key-up streams, read
// without blocking once per frame, through MapKey. Audio: backend.NoAudio;
// the playground has no audio path for a wasip1 guest.
//
// The package itself needs no wasip1 build tag: it talks to the streams
// through the small KeyPoller interface, which a native test fakes.
package wasisurface

import (
	"time"

	"github.com/go-quake1/engine/backend"
	"github.com/go-wasmcontainers/gpu/wasigpu"
)

// KeyPoller is one key stream of a surface; *wasigpu.KeyEvents is one.
type KeyPoller interface {
	// Poll returns the events since the last call, without blocking.
	Poll() ([]wasigpu.KeyEvent, error)
	// Closed reports whether the stream ended (the surface is gone).
	Closed() bool
}

// Backend is the engine backend over a surface.
type Backend struct {
	backend.NoAudio

	w, h     int
	down, up KeyPoller
	start    time.Time
	now      func() time.Time
}

var _ backend.Backend = (*Backend)(nil)

// New returns a backend for a w x h frame whose keys come from down and
// up (either may be nil for no keys of that kind).
func New(w, h int, down, up KeyPoller) *Backend {
	return &Backend{w: w, h: h, down: down, up: up, start: time.Now(), now: time.Now}
}

// Size returns the frame size the engine renders at.
func (b *Backend) Size() (int, int) { return b.w, b.h }

// PresentFrame does nothing: the presenter has already put the frame on the
// surface.
func (b *Backend) PresentFrame([]byte, int, int) error { return nil }

// Now returns the seconds since the backend was made, monotonic.
func (b *Backend) Now() float64 { return b.now().Sub(b.start).Seconds() }

// PollInput returns the keys pressed and released since the last call.
// QuitRequested is set once the key-down stream has ended (the surface was
// dropped).
func (b *Backend) PollInput() (backend.InputSnapshot, error) {
	var snap backend.InputSnapshot
	var err error
	if snap.KeysDown, err = poll(b.down); err != nil {
		return snap, err
	}
	if snap.KeysUp, err = poll(b.up); err != nil {
		return snap, err
	}
	snap.QuitRequested = b.down != nil && b.down.Closed()
	return snap, nil
}

func poll(p KeyPoller) ([]backend.KeyCode, error) {
	if p == nil {
		return nil, nil
	}
	evs, err := p.Poll()
	var out []backend.KeyCode
	for _, e := range evs {
		if k, ok := MapKey(e.Key); ok {
			out = append(out, k)
		}
	}
	return out, err
}
