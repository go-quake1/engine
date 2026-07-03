// Copyright (c) 1996-1997 Id Software, Inc.
// Copyright (c) 2026 the go-quake1/engine authors.
// SPDX-License-Identifier: GPL-2.0-or-later

package wasm

import (
	"errors"

	"github.com/go-quake1/engine/backend"
	"github.com/go-quake1/engine/sound"
)

// Framebuffer is the subset of the JS-backed canvas-presenter the
// wasm Backend uses. Kept as an interface so unit tests on host
// platforms can plug in an in-memory mock without dragging in
// syscall/js.
type Framebuffer interface {
	// PresentRGBA copies one RGBA8 frame to the underlying canvas
	// (no R↔B swap; browser ImageData wants RGBA). The slice must
	// be exactly Width()*Height()*4 bytes.
	PresentRGBA(rgba []byte) error
	// Width returns the canvas width in CSS-independent pixels.
	Width() int
	// Height returns the canvas height in CSS-independent pixels.
	Height() int
}

// InputDevice is the subset of the JS-backed event listener the wasm
// Backend uses.
type InputDevice interface {
	// PollEvents returns the events queued since the last call. The
	// implementation may return an empty slice + nil error if no
	// events are queued.
	PollEvents() ([]InputEvent, error)
}

// InputEvent is the abstracted event type the backend translates
// into backend.InputSnapshot.
type InputEvent struct {
	Kind  EventKind
	Code  string // DOM KeyboardEvent.code (e.g. "KeyW", "ArrowUp") or "" for non-key
	Value int32  // EventKey: 1 = down, 0 = up. EventRel*: signed delta in pixels.
}

// EventKind tags the event type.
type EventKind int

// EventKind values.
const (
	// EventKey is a key press (Value=1) or release (Value=0).
	EventKey EventKind = 1
	// EventRelX is a relative mouse X delta (Value is the signed delta).
	EventRelX EventKind = 2
	// EventRelY is a relative mouse Y delta (Value is the signed delta).
	EventRelY EventKind = 3
	// EventMouseDown is mouse button press (Code = "Mouse1" / "Mouse2").
	EventMouseDown EventKind = 5
	// EventMouseUp is mouse button release (Code = "Mouse1" / "Mouse2").
	EventMouseUp EventKind = 6
	// EventQuit is the synthetic "tab closed / beforeunload" event.
	EventQuit EventKind = 4
)

// AudioDevice is the subset of the JS-backed WebAudio sink the wasm
// Backend uses.
type AudioDevice interface {
	// WritePCM submits one frame's worth of stereo 16-bit PCM samples
	// at the engine's mixer rate (11025 Hz). The implementation is
	// responsible for resampling to the AudioContext rate + scheduling
	// the buffer.
	WritePCM(samples []sound.StereoSample) error
	// SampleRate returns the AudioContext's preferred output rate in
	// Hz (typically 44100 or 48000 depending on the browser/OS).
	SampleRate() int
}

// ClockFn returns wall-clock seconds. Injected so tests can supply a
// deterministic clock instead of performance.now().
type ClockFn func() float64

// defaultClockStep is the per-Now() tick advance used when no ClockFn
// is injected. 1/60 ≈ 16.7 ms matches a typical 60 Hz display.
const defaultClockStep = 1.0 / 60.0

// Backend implements backend.Backend over the three injected
// devices. Construct via New; the public fields are exposed so
// callers can inspect or swap dependencies after construction (e.g.
// unit tests).
type Backend struct {
	FB    Framebuffer
	Input InputDevice
	Audio AudioDevice
	Clock ClockFn

	// Per-tic state — accumulated between PollInput calls.
	pendingDown []backend.KeyCode
	pendingUp   []backend.KeyCode
	pendingDX   float32
	pendingDY   float32
	quitFlag    bool

	// monotonic tick counter, advanced once per Now() call when no
	// ClockFn is injected.
	tick uint64
}

// Sentinel errors. Exported so callers can errors.Is-check them.
var (
	ErrWASMNilFB         = errors.New("wasm: nil Framebuffer in backend")
	ErrWASMRGBASize      = errors.New("wasm: RGBA buffer size doesn't match framebuffer dimensions")
	ErrUnsupportedHost   = errors.New("wasm: JS-backed adapter requires GOOS=js GOARCH=wasm")
	ErrAudioNoContext    = errors.New("wasm: AudioContext not initialised")
	ErrFBSelectorMissing = errors.New("wasm: canvas selector did not match a DOM element")
)

// New returns a Backend wrapping the supplied devices. fb is
// required; in / au / clock are optional:
//
//   - nil in    → PollInput returns an empty snapshot.
//   - nil au    → QueueAudio drops every frame, SampleRate returns 44100.
//   - nil clock → Now() advances a synthetic 60 Hz monotonic tick.
func New(fb Framebuffer, in InputDevice, au AudioDevice, clock ClockFn) (*Backend, error) {
	if fb == nil {
		return nil, ErrWASMNilFB
	}
	return &Backend{
		FB:    fb,
		Input: in,
		Audio: au,
		Clock: clock,
	}, nil
}

// PresentFrame hands one RGBA frame to the canvas-backed framebuffer.
// The engine's renderer already emits RGBA byte order (R,G,B,A) and
// the browser's ImageData expects the same layout, so the call is a
// straight passthrough with a size check.
func (b *Backend) PresentFrame(rgba []byte, width, height int) error {
	if width != b.FB.Width() || height != b.FB.Height() {
		return ErrWASMRGBASize
	}
	if len(rgba) != width*height*4 {
		return ErrWASMRGBASize
	}
	return b.FB.PresentRGBA(rgba)
}

// Size returns the framebuffer dimensions.
func (b *Backend) Size() (int, int) {
	return b.FB.Width(), b.FB.Height()
}

// QueueAudio forwards samples to the audio device. A nil Audio is a
// silent drop. backend.ErrUnsupported (e.g. an AudioContext that has
// not been resumed yet — browsers gate audio on a user gesture) is
// swallowed; any other error is returned.
func (b *Backend) QueueAudio(samples []sound.StereoSample) error {
	if b.Audio == nil {
		return nil
	}
	if err := b.Audio.WritePCM(samples); err != nil {
		if errors.Is(err, backend.ErrUnsupported) {
			return nil
		}
		return err
	}
	return nil
}

// SampleRate returns the audio device's preferred rate. Defaults to
// 44100 Hz when no Audio device is wired up (matches the most common
// WebAudio default).
func (b *Backend) SampleRate() int {
	if b.Audio == nil {
		return 44100
	}
	return b.Audio.SampleRate()
}

// PollInput drains the input device, translates DOM events to
// backend.KeyCode + accumulated mouse delta, and returns the
// snapshot. Unmapped KeyboardEvent.code values are silently dropped.
// The pending state is cleared on every call.
func (b *Backend) PollInput() (backend.InputSnapshot, error) {
	if b.Input == nil {
		return backend.InputSnapshot{}, nil
	}
	events, err := b.Input.PollEvents()
	if err != nil {
		return backend.InputSnapshot{}, err
	}
	for _, ev := range events {
		switch ev.Kind {
		case EventKey:
			kc, ok := MapDOMKey(ev.Code)
			if !ok {
				continue
			}
			switch ev.Value {
			case 1:
				b.pendingDown = append(b.pendingDown, kc)
			case 0:
				b.pendingUp = append(b.pendingUp, kc)
			}
		case EventMouseDown:
			kc, ok := MapDOMKey(ev.Code)
			if !ok {
				continue
			}
			b.pendingDown = append(b.pendingDown, kc)
		case EventMouseUp:
			kc, ok := MapDOMKey(ev.Code)
			if !ok {
				continue
			}
			b.pendingUp = append(b.pendingUp, kc)
		case EventRelX:
			b.pendingDX += float32(ev.Value)
		case EventRelY:
			b.pendingDY += float32(ev.Value)
		case EventQuit:
			b.quitFlag = true
		}
	}
	snap := backend.InputSnapshot{
		KeysDown:      b.pendingDown,
		KeysUp:        b.pendingUp,
		MouseDX:       b.pendingDX,
		MouseDY:       b.pendingDY,
		QuitRequested: b.quitFlag,
	}
	b.pendingDown = nil
	b.pendingUp = nil
	b.pendingDX = 0
	b.pendingDY = 0
	b.quitFlag = false
	return snap, nil
}

// Now returns wall-clock seconds. With an injected Clock the value
// is whatever the caller's closure returns; without one we hand back
// a monotonic 60 Hz tick (deterministic — handy for headless
// reproducible runs and host-side tests).
func (b *Backend) Now() float64 {
	if b.Clock != nil {
		return b.Clock()
	}
	t := b.tick
	b.tick++
	return float64(t) * defaultClockStep
}
