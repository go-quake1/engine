// Copyright (c) 1996-1997 Id Software, Inc.
// Copyright (c) 2026 the go-quake1/engine authors.
// SPDX-License-Identifier: GPL-2.0-or-later

//go:build js && wasm

package wasmbox

import (
	"syscall/js"

	"github.com/go-quake1/engine/sound"
)

// engineMixerRate is the sample rate of the engine's internal mixer
// output (sound.Paint emits stereo frames at this rate). Matches
// sound.DefaultSampleRate (11025 Hz).
const engineMixerRate = 11025

// WebAudio is an AudioDevice that streams the engine's mixer output
// through a per-Worker WebAudio AudioContext. Same algorithm as
// backend/wasm.WebAudio: resample to ctx.sampleRate (nearest-neighbor),
// allocate a fresh AudioBuffer per WritePCM, schedule it on
// AudioBufferSourceNode.start(playhead), advance the playhead.
//
// AudioContext is supported in Web Workers in current Chromium
// (chrome 105+). A freshly-constructed AudioContext starts in the
// "suspended" state under the browser autoplay policy and stays there
// -- emitting NO sound even while WritePCM keeps scheduling buffers --
// until resume() is called. resume() state does NOT propagate across
// separate AudioContext instances (each is gated independently), so the
// worker's own context has to be resumed explicitly; without this
// Quake-in-wasmbox is silent. We call resume() best-effort at
// construction and again from WritePCM whenever the context is found
// suspended (the compositor's unmuting user-gesture can land after this
// worker boots, so a one-shot resume at construction is not enough).
// If the constructor isn't available we return ErrAudioNoContext and the
// Backend degrades to silent (NewClient swallows the error).
type WebAudio struct {
	ctx        js.Value
	sampleRate int
	playhead   float64
	scheduled  int // count of buffers handed to the context (observability)
}

// NewWebAudio constructs a WebAudio sink. Returns ErrAudioNoContext if
// the worker scope does not expose AudioContext or webkitAudioContext.
func NewWebAudio() (*WebAudio, error) {
	ctxCtor := js.Global().Get("AudioContext")
	if !ctxCtor.Truthy() {
		ctxCtor = js.Global().Get("webkitAudioContext")
	}
	if !ctxCtor.Truthy() {
		return nil, ErrAudioNoContext
	}
	ctx := ctxCtor.New()
	rate := ctx.Get("sampleRate").Int()
	a := &WebAudio{
		ctx:        ctx,
		sampleRate: rate,
	}
	// Kick the autoplay-gated context toward "running" immediately; if
	// the gesture has not happened yet this is a harmless no-op and
	// WritePCM re-tries on every frame it finds the context suspended.
	a.resume()
	return a, nil
}

// resume nudges the AudioContext out of the autoplay-gated "suspended"
// state. Best-effort: the returned Promise is ignored and a context
// that does not expose resume() (or is already running/closed) is left
// untouched. Guarded so a stub context in tests -- or an exotic
// browser -- cannot panic the mixer thread.
func (a *WebAudio) resume() {
	if r := a.ctx.Get("resume"); r.Type() == js.TypeFunction {
		a.ctx.Call("resume")
	}
}

// State returns the AudioContext's lifecycle state ("suspended",
// "running", "closed", ...) or "" when the field is absent. Exposed so
// a runtime probe can confirm audio actually reached "running".
func (a *WebAudio) State() string {
	if s := a.ctx.Get("state"); s.Type() == js.TypeString {
		return s.String()
	}
	return ""
}

// Scheduled returns the number of PCM buffers handed to the context so
// far -- a monotonic counter a runtime probe can sample to prove the
// mixer -> WebAudio path is actually live (vs. wired but never driven).
func (a *WebAudio) Scheduled() int { return a.scheduled }

// WritePCM resamples + schedules one chunk of stereo PCM. Empty inputs
// are a no-op.
func (a *WebAudio) WritePCM(samples []sound.StereoSample) error {
	if len(samples) == 0 {
		return nil
	}
	// Self-heal: if the context slipped back to suspended (or the
	// unmuting gesture only just arrived) resume before scheduling, so
	// the buffers we queue below become audible rather than silently
	// backing up in a suspended graph.
	if a.State() == "suspended" {
		a.resume()
	}
	left, right := ResampleNearest(samples, engineMixerRate, a.sampleRate)
	if len(left) == 0 {
		return nil
	}
	buf := a.ctx.Call("createBuffer", 2, len(left), a.sampleRate)
	jsLeft := makeFloat32Array(left)
	jsRight := makeFloat32Array(right)
	buf.Call("copyToChannel", jsLeft, 0)
	buf.Call("copyToChannel", jsRight, 1)
	src := a.ctx.Call("createBufferSource")
	src.Set("buffer", buf)
	src.Call("connect", a.ctx.Get("destination"))
	currentTime := a.ctx.Get("currentTime").Float()
	if a.playhead < currentTime {
		a.playhead = currentTime
	}
	src.Call("start", a.playhead)
	a.playhead += float64(len(left)) / float64(a.sampleRate)
	a.scheduled++
	return nil
}

// SampleRate returns the AudioContext's negotiated output rate.
func (a *WebAudio) SampleRate() int { return a.sampleRate }

// makeFloat32Array builds a JS Float32Array carrying the supplied
// samples. Reinterprets the float32 backing memory as bytes (wasm is
// little-endian + matches the Float32Array spec) and copies them into
// a Uint8Array, then wraps a Float32Array over the same ArrayBuffer.
func makeFloat32Array(samples []float32) js.Value {
	bytesLen := len(samples) * 4
	u8 := js.Global().Get("Uint8Array").New(bytesLen)
	bytes := float32SliceAsBytes(samples)
	js.CopyBytesToJS(u8, bytes)
	return js.Global().Get("Float32Array").New(u8.Get("buffer"), u8.Get("byteOffset"), len(samples))
}
