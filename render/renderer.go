// Copyright (c) 2026 the go-quake1/engine authors.
// SPDX-License-Identifier: GPL-2.0-or-later

package render

import (
	"errors"
	"fmt"
)

// Kind names a renderer implementation. It is the value of the
// renderer setting (`soft` or `gpu`).
type Kind string

const (
	// KindSoft is the software rasterizer: an 8-bit palette-indexed
	// FrameBuffer expanded to RGBA on the CPU. Always available.
	KindSoft Kind = "soft"
	// KindGPU is the hardware renderer (WebGPU, GLQuake-style). Not
	// implemented yet: selecting it falls back to [KindSoft].
	KindGPU Kind = "gpu"
)

var (
	// ErrUnknownKind is returned by [ParseKind] for a value that names
	// no renderer.
	ErrUnknownKind = errors.New("render: unknown renderer kind")
	// ErrGPUNotImplemented is the fallback reason [Choose] reports when
	// the GPU renderer is requested but no implementation is wired.
	ErrGPUNotImplemented = errors.New("render: gpu renderer not implemented yet")
	// ErrNilSoftRenderer is returned by [Choose] when the mandatory
	// software fallback is missing.
	ErrNilSoftRenderer = errors.New("render: nil software renderer")
)

// ParseKind maps the renderer setting to a [Kind]. The empty string
// selects the default, [KindSoft].
func ParseKind(s string) (Kind, error) {
	switch Kind(s) {
	case "", KindSoft:
		return KindSoft, nil
	case KindGPU:
		return KindGPU, nil
	}
	return "", fmt.Errorf("%w: %q (want %q or %q)", ErrUnknownKind, s, KindSoft, KindGPU)
}

// Renderer is the seam between the per-frame game loop
// (runloop.Runner.RunFrame) and a rasterizer. One frame is:
//
//  1. DrawScene -- the 3D view (world, then models, particles,
//     sprites) for the given camera. Skipped while the menu freezes
//     the world.
//  2. Compose -- the 2D layers (console, notify lines, centre print,
//     intermission) over the scene.
//  3. Resolve -- produce the RGBA8 frame the backend presents.
//
// Everything above the seam stays shared between implementations:
// input, the server and client ticks, the particle simulation, the
// PVS / BSP walk helpers (package bsprender), lightstyles and the
// menu and status-bar state.
type Renderer interface {
	// Kind reports which implementation this is.
	Kind() Kind
	// HasScene reports whether DrawScene draws anything. When false
	// the frame is 2D only and Compose clears the background first.
	HasScene() bool
	// DrawScene renders the 3D view. viewAngles are (pitch, yaw, roll)
	// in degrees, matching [RefDef].
	DrawScene(fb *FrameBuffer, viewOrigin, viewAngles [3]float32) error
	// Compose draws the 2D layers described by ctx.
	Compose(fb *FrameBuffer, ctx FrameContext) error
	// Resolve writes the finished frame to dst as RGBA8
	// (len(dst) >= fb.Width*fb.Height*4).
	Resolve(fb *FrameBuffer, dst []byte, pal *Palette) error
}

// Software is the software [Renderer]. Scene is the 3D pass (the
// closure the runner builds over the BSP walk and the span fillers);
// nil means a 2D-only frame.
type Software struct {
	Scene func(fb *FrameBuffer, viewOrigin, viewAngles [3]float32) error
}

// Kind returns [KindSoft].
func (s *Software) Kind() Kind { return KindSoft }

// HasScene reports whether a 3D pass is wired.
func (s *Software) HasScene() bool { return s.Scene != nil }

// DrawScene runs the 3D pass, if any.
func (s *Software) DrawScene(fb *FrameBuffer, viewOrigin, viewAngles [3]float32) error {
	if s.Scene == nil {
		return nil
	}
	return s.Scene(fb, viewOrigin, viewAngles)
}

// Compose runs [Compose2D].
func (s *Software) Compose(fb *FrameBuffer, ctx FrameContext) error {
	return Compose2D(fb, ctx)
}

// Resolve expands the palette-indexed framebuffer to RGBA8. Together
// with Compose it is exactly [ExpandFrame].
func (s *Software) Resolve(fb *FrameBuffer, dst []byte, pal *Palette) error {
	if pal == nil {
		return ErrFBPaletteShape
	}
	return fb.Expand(dst, pal)
}

// Choose returns the renderer to use for want. soft is the software
// renderer, which is always the fallback; newGPU builds the GPU
// renderer and may be nil while none is implemented.
//
// The second result is the reason a fallback happened (nil when want
// was honoured). It is not a failure: the returned renderer is always
// usable. Only a nil soft renderer is an error, reported as the reason
// with a nil renderer.
func Choose(want Kind, soft Renderer, newGPU func() (Renderer, error)) (Renderer, error) {
	if soft == nil {
		return nil, ErrNilSoftRenderer
	}
	if want != KindGPU {
		return soft, nil
	}
	if newGPU == nil {
		return soft, ErrGPUNotImplemented
	}
	gpu, err := newGPU()
	if err != nil {
		return soft, fmt.Errorf("render: gpu renderer unavailable: %w", err)
	}
	if gpu == nil {
		return soft, ErrGPUNotImplemented
	}
	return gpu, nil
}
