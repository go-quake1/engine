// Copyright (c) 1996-1997 Id Software, Inc.
// Copyright (c) 2026 the go-quake1/engine authors.
// SPDX-License-Identifier: GPL-2.0-or-later

package wasisurface

import (
	"time"

	"github.com/go-quake1/engine/backend"
	"github.com/go-quake1/engine/sound"
)

// AudioOut is a sound output that takes interleaved float32 stereo frames at
// the mixer's rate (11025 Hz): the host module wctr_audio_out of
// go-wasmcontainers/engine, behind one stream. cmd/quake-wctr's wasip1 build
// opens it; a native test fakes it.
type AudioOut interface {
	// Write queues up to len(pcm)/2 frames. It returns how many it accepted and
	// how many frames are queued after them. It never blocks. Until the page
	// plays, the host accepts and discards (queued stays 0).
	Write(pcm []float32) (accepted, queued int, err error)
	// Close closes the stream.
	Close()
}

// AudioStats is what the sound output did, for the --frames summary.
type AudioStats struct {
	Offered  int // mix frames the runloop handed to QueueAudio
	Accepted int // of those, frames the host accepted
	Silent   bool
}

// WithAudio gives the backend a sound output (nil is silence: the game runs
// the same, the mix just goes nowhere). It returns b.
func (b *Backend) WithAudio(out AudioOut) *Backend {
	b.out = out
	return b
}

// Stats returns what the sound output did.
func (b *Backend) AudioStats() AudioStats {
	return AudioStats{Offered: b.offered, Accepted: b.accepted, Silent: b.out == nil}
}

var (
	_ backend.Backend     = (*Backend)(nil)
	_ backend.QueuedAudio = (*Backend)(nil)
)

// QueueAudio converts the mix (int16 stereo at 11025 Hz) to float32 and writes it
// to the output in one call: the call's answer is the queue the pacing reads, so
// a tic costs one host round trip. Without an output the mix is dropped and
// counted. A failing output is switched off (the game keeps running, silent): a
// grant is optional and a stream that cannot be written is not worth a crash.
func (b *Backend) QueueAudio(samples []sound.StereoSample) error {
	now := b.now()
	b.offered += len(samples)
	if len(samples) == 0 {
		return nil
	}
	if b.out == nil {
		// No output: model one that plays in real time, so the pacing is the
		// same as with sound and the sounds are consumed at the same rate.
		b.setQueued(b.queuedAt(now)+float64(len(samples)), now)
		return nil
	}
	if cap(b.pcm) < 2*len(samples) {
		b.pcm = make([]float32, 2*len(samples))
	}
	pcm := b.pcm[:2*len(samples)]
	for i, s := range samples {
		pcm[2*i] = float32(s.L) / 32768
		pcm[2*i+1] = float32(s.R) / 32768
	}
	accepted, queued, err := b.out.Write(pcm)
	if err != nil {
		b.out.Close()
		b.out = nil
		b.setQueued(0, now)
		return nil
	}
	b.accepted += accepted
	b.setQueued(float64(queued), now)
	return nil
}

// QueuedFrames is the mix frames the output still has to play: what the last
// write reported, minus what has played since (the mixer's rate is 11025 frames
// a second). One host call a tic gives both.
func (b *Backend) QueuedFrames() int { return int(b.queuedAt(b.now())) }

// SampleRate is the mixer's rate: the stream carries it unchanged and the page
// resamples. Without an output it is what NoAudio said.
func (b *Backend) SampleRate() int {
	if b.out == nil {
		return b.NoAudio.SampleRate()
	}
	return sound.DefaultSampleRate
}

func (b *Backend) setQueued(q float64, now time.Time) { b.queued, b.queuedAt0 = q, now }

func (b *Backend) queuedAt(now time.Time) float64 {
	q := b.queued - now.Sub(b.queuedAt0).Seconds()*sound.DefaultSampleRate
	if q < 0 {
		return 0
	}
	return q
}
