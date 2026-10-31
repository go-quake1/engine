// Copyright (c) 1996-1997 Id Software, Inc.
// Copyright (c) 2026 the go-quake1/engine authors.
// SPDX-License-Identifier: GPL-2.0-or-later

//go:build js && wasm

package wasmbox

import (
	"syscall/js"

	"github.com/wasmdesk/wasmbox/audioring"

	"github.com/go-quake1/engine/sound"
)

// ringCapacityFrames is the relay ring size in frames at the engine mixer
// rate: 1024 frames = 93 ms at 11025 Hz, a few game tics of headroom. The
// compositor page plays the ring through an AudioWorklet and resamples it.
const ringCapacityFrames = 1024

// RingAudio is an AudioDevice that relays the engine's mixer output to the
// wasmbox compositor page. A worker has no AudioContext, so instead of
// scheduling AudioBuffers itself (WebAudio) it writes interleaved float32 PCM
// into a SharedArrayBuffer ring (github.com/wasmdesk/wasmbox/audioring) and
// announces it with an audio_open message; the page plays it from an
// AudioWorklet. The engine's 11025 Hz mixer rate goes into the ring as is: the
// worklet resamples, so nothing here allocates per sample or resamples twice.
type RingAudio struct {
	w       *audioring.Writer
	buf     []float32 // reused interleaved scratch
	noWrite bool      // planted-negative hook for the e2e: globalThis.__wasmboxNoAudioWrite
}

// NewRingAudio opens the relay ring. windowID labels the session (the
// compositor binds the ring to this worker's port regardless).
func NewRingAudio(windowID int) (*RingAudio, error) {
	w, _, err := audioring.Open(audioring.Options{
		SampleRate:     engineMixerRate,
		Channels:       2,
		CapacityFrames: ringCapacityFrames,
		WindowID:       windowID,
	})
	if err != nil {
		return nil, err
	}
	return &RingAudio{w: w, noWrite: js.Global().Get("__wasmboxNoAudioWrite").Truthy()}, nil
}

// WritePCM converts one chunk of int16 stereo to float32 and queues it. Frames
// that do not fit are dropped by the ring (and counted), never blocking the
// game loop.
func (a *RingAudio) WritePCM(samples []sound.StereoSample) error {
	if len(samples) == 0 || a.noWrite {
		return nil
	}
	if cap(a.buf) < 2*len(samples) {
		a.buf = make([]float32, 2*len(samples))
	}
	buf := a.buf[:2*len(samples)]
	for i, s := range samples {
		buf[2*i] = float32(s.L) / 32768
		buf[2*i+1] = float32(s.R) / 32768
	}
	a.w.Write(buf)
	return nil
}

// SampleRate is the rate the engine mixes at (the ring carries it unchanged).
func (a *RingAudio) SampleRate() int { return engineMixerRate }

// Stats returns the consumer's counters (frames played, underruns) and the
// producer's overruns, for the runtime probe.
func (a *RingAudio) Stats() audioring.Stats { return a.w.Stats() }

// Close tells the page to stop playing this ring.
func (a *RingAudio) Close() { a.w.Close() }
