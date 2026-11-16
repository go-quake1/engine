// Copyright (c) 1996-1997 Id Software, Inc.
// Copyright (c) 2026 the go-quake1/engine authors.
// SPDX-License-Identifier: GPL-2.0-or-later

package wasmbox

import (
	"errors"

	"github.com/go-quake1/engine/backend"
	"github.com/go-quake1/engine/sound"
)

// Surface is the subset of the SAB-backed window surface the wasmbox
// Backend uses. Pure-Go interface so unit tests on host platforms can
// plug in an in-memory mock without dragging in syscall/js.
type Surface interface {
	// PresentRGBA copies one RGBA8 frame to the SharedArrayBuffer
	// surface + posts a full-damage commit to the compositor. The
	// slice must be exactly Width()*Height()*4 bytes.
	PresentRGBA(rgba []byte) error
	// Width returns the surface width in pixels (the value the
	// compositor granted in its welcome reply).
	Width() int
	// Height returns the surface height in pixels.
	Height() int
}

// InputDevice is the subset of the wasmbox-protocol input source the
// Backend uses. Implementations queue events received from the
// compositor's `{type:"input"}` messages.
type InputDevice interface {
	// PollEvents drains + returns the events queued since the last
	// call. Implementations may return an empty slice + nil error.
	PollEvents() ([]InputEvent, error)
}

// EventKind tags the abstracted event type, matching what backend/wasm
// emits so the Backend.PollInput translation step can share its shape.
type EventKind int

// EventKind values.
const (
	// EventKey is a key press (Value=1) or release (Value=0). Code is
	// a DOM KeyboardEvent.code (e.g. "KeyW", "ArrowUp", "Backquote").
	EventKey EventKind = 1
	// EventRelX is a relative mouse X delta (Value is the signed delta
	// in surface-local pixels).
	EventRelX EventKind = 2
	// EventRelY is a relative mouse Y delta.
	EventRelY EventKind = 3
	// EventQuit is the synthetic "compositor closed our window" event.
	EventQuit EventKind = 4
	// EventMouseDown is a mouse-button press (Code = "Mouse1" /
	// "Mouse2", same convention as backend/wasm).
	EventMouseDown EventKind = 5
	// EventMouseUp is a mouse-button release.
	EventMouseUp EventKind = 6
)

// InputEvent is the abstracted event type the backend translates into
// backend.InputSnapshot.
type InputEvent struct {
	Kind  EventKind
	Code  string
	Value int32
}

// AudioDevice is the subset of the WebAudio sink the wasmbox Backend
// uses. Same shape as backend/wasm.AudioDevice so callers can swap.
type AudioDevice interface {
	// WritePCM submits one frame's worth of stereo PCM at the engine's
	// 11025 Hz mixer rate. Implementations are responsible for
	// resampling to the AudioContext rate + scheduling the buffer.
	WritePCM(samples []sound.StereoSample) error
	// SampleRate returns the AudioContext's preferred output rate.
	SampleRate() int
}

// ClockFn returns wall-clock seconds. Injected so tests can supply a
// deterministic clock instead of performance.now().
type ClockFn func() float64

// defaultClockStep is the per-Now() tick advance used when no ClockFn
// is injected. 1/60 ≈ 16.7 ms matches a typical 60 Hz display.
const defaultClockStep = 1.0 / 60.0

// Backend implements backend.Backend over the wasmbox external-client
// protocol. Construct via New; the public fields are exposed so
// callers can inspect or swap dependencies after construction (e.g.
// unit tests).
type Backend struct {
	Surface Surface
	Input   InputDevice
	Audio   AudioDevice
	Clock   ClockFn

	// Per-tic state — accumulated between PollInput calls.
	pendingDown []backend.KeyCode
	pendingUp   []backend.KeyCode
	pendingDX   float32
	pendingDY   float32
	quitFlag    bool

	// Monotonic tick counter, advanced once per Now() call when no
	// ClockFn is injected.
	tick uint64
}

// Sentinel errors. Exported so callers can errors.Is-check them.
var (
	ErrNilSurface      = errors.New("wasmbox: nil Surface in backend")
	ErrRGBASize        = errors.New("wasmbox: RGBA buffer size doesn't match surface dimensions")
	ErrUnsupportedHost = errors.New("wasmbox: SAB client requires GOOS=js GOARCH=wasm")
	ErrNoSAB           = errors.New("wasmbox: SharedArrayBuffer constructor not available -- page needs COOP/COEP")
	ErrNoWelcome       = errors.New("wasmbox: never received welcome reply from compositor")
	ErrAudioNoContext  = errors.New("wasmbox: AudioContext not initialised")
)

// New returns a Backend wrapping the supplied devices. surface is
// required; the rest are optional:
//
//   - nil in    → PollInput returns an empty snapshot.
//   - nil au    → QueueAudio drops every frame, SampleRate returns 44100.
//   - nil clock → Now() advances a synthetic 60 Hz monotonic tick.
func New(surface Surface, in InputDevice, au AudioDevice, clock ClockFn) (*Backend, error) {
	if surface == nil {
		return nil, ErrNilSurface
	}
	return &Backend{
		Surface: surface,
		Input:   in,
		Audio:   au,
		Clock:   clock,
	}, nil
}

// PresentFrame hands one RGBA frame to the SAB-backed surface + posts a
// commit message. The engine's renderer already emits RGBA byte order
// (R,G,B,A); wasmbox surfaces are RGBA32 row-major top-left so the call
// is a straight passthrough with a size check.
func (b *Backend) PresentFrame(rgba []byte, width, height int) error {
	if width != b.Surface.Width() || height != b.Surface.Height() {
		return ErrRGBASize
	}
	if len(rgba) != width*height*4 {
		return ErrRGBASize
	}
	return b.Surface.PresentRGBA(rgba)
}

// Size returns the surface dimensions.
func (b *Backend) Size() (int, int) {
	return b.Surface.Width(), b.Surface.Height()
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

// queuedFramer is what an AudioDevice may add to report its queue (RingAudio does).
type queuedFramer interface{ QueuedFrames() int }

// QueuedFrames implements backend.QueuedAudio: the mix frames the audio device
// still has to play, so the runloop paces the mixer like tyrquake's S_Update
// instead of painting a fixed chunk a tic. A device that cannot say (and a nil
// one) reports 0.
func (b *Backend) QueuedFrames() int {
	if q, ok := b.Audio.(queuedFramer); ok {
		return q.QueuedFrames()
	}
	return 0
}

// SampleRate returns the audio device's preferred rate. Defaults to
// 44100 Hz when no Audio device is wired up.
func (b *Backend) SampleRate() int {
	if b.Audio == nil {
		return 44100
	}
	return b.Audio.SampleRate()
}

// PollInput drains the input device, translates wasmbox events to
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

// Now returns wall-clock seconds. With an injected Clock the value is
// whatever the caller's closure returns; without one we hand back a
// monotonic 60 Hz tick (deterministic — handy for host-side tests).
func (b *Backend) Now() float64 {
	if b.Clock != nil {
		return b.Clock()
	}
	t := b.tick
	b.tick++
	return float64(t) * defaultClockStep
}
