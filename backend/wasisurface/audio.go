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

// LastWrite is when the last sound write returned and how many frames the host
// said were queued after it (ok false before the first write): with the writes
// before it, the gap the output had to bridge on what it held.
func (b *Backend) LastWrite() (at time.Time, queued int, ok bool) {
	return b.lastAt, b.lastQ, !b.lastAt.IsZero()
}

// WriteRecord is one sound write: when the call began and returned (wall
// clock), the frames it carried, the frames the host said were queued after it,
// and where in which frame the game made it (Tag, see SetTag).
type WriteRecord struct {
	Start, End time.Time
	Frames     int
	Queued     int
	Site       string
	Frame      int
}

// maxWriteLog bounds the log: a window holds a few hundred writes, and a game
// nobody drains drops the oldest half rather than growing.
const maxWriteLog = 8192

// SetTag says where in the game a write is made: f returns a site name and a
// frame number, read at each write.
func (b *Backend) SetTag(f func() (site string, frame int)) { b.tag = f }

// TakeWrites returns every sound write since the last call, and empties the log.
func (b *Backend) TakeWrites() []WriteRecord {
	w := b.writes
	b.writes = nil
	return w
}

// TakeWriteTime returns the host time the sound writes (stream_write) took since
// the last call, and resets it: the per-frame diagnostic of a slow tic.
func (b *Backend) TakeWriteTime() time.Duration {
	d := b.writeTime
	b.writeTime = 0
	return d
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
	t0 := time.Now()
	accepted, queued, err := b.out.Write(pcm)
	b.writeTime += time.Since(t0)
	if err == nil {
		rec := WriteRecord{Start: t0, End: time.Now(), Frames: len(samples), Queued: queued}
		if b.tag != nil {
			rec.Site, rec.Frame = b.tag()
		}
		if len(b.writes) >= maxWriteLog {
			b.writes = append(b.writes[:0], b.writes[maxWriteLog/2:]...)
		}
		b.writes = append(b.writes, rec)
	}
	if err != nil {
		b.out.Close()
		b.out = nil
		b.setQueued(0, now)
		return nil
	}
	b.accepted += accepted
	b.lastAt, b.lastQ = time.Now(), queued
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
