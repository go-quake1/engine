// Copyright (c) 1996-1997 Id Software, Inc.
// Copyright (c) 2026 the go-quake1/engine authors.
// SPDX-License-Identifier: GPL-2.0-or-later

package wasisurface

import (
	"github.com/go-quake1/engine/backend"
	"github.com/go-quake1/engine/sound"
	"github.com/go-quake1/engine/sound/voicemix"
)

// VoiceStats is what the page's mixer reports about itself (voice_stats of
// go-wasmcontainers/engine): the counters of the worklet, which keeps playing
// while the game stalls.
type VoiceStats struct {
	Played    uint32 // frames the output has taken
	Underruns uint32 // output blocks the mixer could not fill
	Starved   uint32 // frames of silence those blocks held
	Painted   uint32 // frames the mixer has mixed
	BadCmds   uint32 // commands the mixer refused
	State     uint32 // audioring state word
}

// VoiceHost is the host's voice mixer: voicemix.Host (sample_load and
// voice_commit) plus its statistics and its close. cmd/quake-wctr's wasip1
// build opens it; a native test fakes it.
type VoiceHost interface {
	voicemix.Host
	// Stats reads the mixer's counters (voice_stats).
	Stats() (VoiceStats, error)
	// Close closes the mixer (voice_close).
	Close()
}

var _ backend.VoiceBackend = (*Backend)(nil)

// WithVoices gives the backend a voice mixer in the page: the runloop then
// sends commands instead of painting frames. nil keeps the stream path. It
// returns b.
func (b *Backend) WithVoices(h VoiceHost) *Backend {
	b.vhost = h
	b.voices = nil
	if h != nil {
		b.voices = voicemix.New(h)
	}
	return b
}

// Voices implements backend.VoiceBackend.
func (b *Backend) Voices() sound.Voices {
	if b.voices == nil {
		return nil
	}
	return b.voices
}

// VoiceMixer is the mixer WithVoices made, for its refusal counters (nil
// without one).
func (b *Backend) VoiceMixer() *voicemix.Mixer { return b.voices }

// VoiceStats reads the mixer's counters; ok is false without a voice mixer or
// when the host cannot say.
func (b *Backend) VoiceStats() (st VoiceStats, ok bool) {
	if b.vhost == nil {
		return VoiceStats{}, false
	}
	st, err := b.vhost.Stats()
	return st, err == nil
}

// CloseVoices closes the voice mixer.
func (b *Backend) CloseVoices() {
	if b.vhost != nil {
		b.vhost.Close()
	}
}
