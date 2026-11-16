// Copyright (c) 1996-1997 Id Software, Inc.
// Copyright (c) 2026 the go-quake1/engine authors.
// SPDX-License-Identifier: GPL-2.0-or-later

//go:build js && wasm

package wasm

import (
	"syscall/js"

	"github.com/go-quake1/engine/sound"
)

// engineMixerRate is the sample rate of the engine's internal mixer
// output (sound.Paint emits stereo frames at this rate). Matches
// sound.DefaultSampleRate (11025 Hz).
const engineMixerRate = 11025

// WebAudio is an AudioDevice that streams the engine's mixer output
// through a WebAudio AudioContext. Each WritePCM call:
//
//  1. Resamples the 11025 Hz stereo frames to ctx.sampleRate via
//     nearest-neighbor (see ResampleNearest).
//  2. Allocates a fresh AudioBuffer of the resampled length.
//  3. Copies the L / R float32 channel data into the buffer with
//     copyToChannel.
//  4. Creates an AudioBufferSourceNode, connects it to ctx.destination,
//     and starts it at max(playhead, ctx.currentTime).
//  5. Advances playhead by the new buffer's duration.
//
// AudioContext creation in browsers requires a user gesture; the
// caller is responsible for resuming the context inside a click /
// keydown handler. SampleRate() returns whatever ctx.sampleRate
// reports (typically 44100 or 48000 depending on the host OS).
type WebAudio struct {
	ctx        js.Value
	sampleRate int
	playhead   float64 // seconds, monotonic; ctx.currentTime baseline
}

// NewWebAudio constructs a WebAudio sink, instantiating an
// AudioContext. Returns ErrAudioNoContext if the browser does not
// expose AudioContext or webkitAudioContext.
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
	return &WebAudio{
		ctx:        ctx,
		sampleRate: rate,
	}, nil
}

// WritePCM resamples + schedules one chunk of stereo PCM. Empty
// inputs are a no-op (return nil).
func (a *WebAudio) WritePCM(samples []sound.StereoSample) error {
	if len(samples) == 0 {
		return nil
	}
	left, right := ResampleNearest(samples, engineMixerRate, a.sampleRate)
	if len(left) == 0 {
		return nil
	}
	buf := a.ctx.Call("createBuffer", 2, len(left), a.sampleRate)
	// Float32Array views over the resampled L / R buffers. We allocate
	// fresh JS Float32Arrays + js.CopyBytesToJS isn't usable for
	// float32 — instead build a typed array via the Float32Array
	// constructor and Set() per index. To stay efficient with JS round-
	// trips we round-trip an []byte view of the float32 slice through
	// js.CopyBytesToJS into a Uint8Array, then construct a Float32Array
	// over the same underlying buffer.
	jsLeft := makeFloat32Array(left)
	jsRight := makeFloat32Array(right)
	buf.Call("copyToChannel", jsLeft, 0)
	buf.Call("copyToChannel", jsRight, 1)
	src := a.ctx.Call("createBufferSource")
	src.Set("buffer", buf)
	src.Call("connect", a.ctx.Get("destination"))
	currentTime := a.ctx.Get("currentTime").Float()
	if a.playhead < currentTime {
		// We've fallen behind (page was hidden, engine stalled). Snap
		// the playhead forward so the next buffer plays immediately
		// instead of trying to fill the gap with silence.
		a.playhead = currentTime
	}
	src.Call("start", a.playhead)
	a.playhead += float64(len(left)) / float64(a.sampleRate)
	return nil
}

// QueuedFrames is the audio still scheduled and not yet played, in mix frames
// (the engine's 11025 Hz): the playhead's lead over the context's clock. A
// suspended context's clock stands still, so what is queued before the user's
// gesture stays queued, and the runloop stops painting at its mix-ahead.
func (a *WebAudio) QueuedFrames() int {
	lead := a.playhead - a.ctx.Get("currentTime").Float()
	if lead <= 0 {
		return 0
	}
	return int(lead * engineMixerRate)
}

// SampleRate returns the AudioContext's negotiated output rate.
func (a *WebAudio) SampleRate() int { return a.sampleRate }

// makeFloat32Array builds a JS Float32Array containing the supplied
// samples. The implementation reinterprets the []float32 backing
// memory as a []byte (4 bytes per sample, host-endian — wasm is
// little-endian, which matches the Float32Array spec) and copies it
// into a Uint8Array, then wraps a Float32Array over the same
// ArrayBuffer.
func makeFloat32Array(samples []float32) js.Value {
	bytesLen := len(samples) * 4
	u8 := js.Global().Get("Uint8Array").New(bytesLen)
	// Reinterpret the float32 slice as a byte slice without copying.
	bytes := float32SliceAsBytes(samples)
	js.CopyBytesToJS(u8, bytes)
	return js.Global().Get("Float32Array").New(u8.Get("buffer"), u8.Get("byteOffset"), len(samples))
}
