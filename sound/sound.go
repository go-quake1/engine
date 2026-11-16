// Copyright (c) 1996-1997 Id Software, Inc.
// Copyright (c) 2026 the go-quake1/engine authors.
// SPDX-License-Identifier: GPL-2.0-or-later

package sound

import "errors"

// Audio format constants. tyrquake: snddma.h + the default values
// inside SNDDMA_Init.
const (
	DefaultSampleRate = 11025 // Hz
	DefaultChannels   = 1     // mono
	DefaultBitsPerSam = 8     // signed byte
)

// MaxChannels is the per-pool channel cap. tyrquake: MAX_CHANNELS in
// snd_dma.c (typically 128: 8 static ambient + 120 dynamic).
const MaxChannels = 128

// FreeChannel is the sentinel returned by Pool.Alloc when the pool
// is full (no slot can be evicted via priority).
const FreeChannel = -1

// Sample is one loaded sound effect's PCM data.
// tyrquake: sfxcache_t in snd_mem.c.
type Sample struct {
	Name       string // canonical name (e.g. "weapons/sshotgun.wav")
	SampleRate int    // Hz (typically 11025)
	BitsPerSam int    // 8 (signed byte) or 16 (signed short)
	LoopStart  int    // -1 if not looped; else sample index to loop back to
	NumSamples int    // total sample count (NOT bytes)
	Data       []byte // raw PCM bytes
}

// Channel is one active mixer voice. tyrquake: channel_t in snd.h.
type Channel struct {
	Sfx        *Sample // playing sample, nil = free slot
	Position   int     // current sample index inside Sfx.Data
	EndPos     int     // sample index at which to stop (loop handles otherwise)
	LeftVol    int     // 0..255, attenuated stereo left
	RightVol   int     // 0..255, attenuated stereo right
	EntNum     int     // owning entity, 0 = world / global
	EntChannel int     // entity-relative channel (0..7); same ent+channel replaces
	Master     bool    // true = locally-played (no spatial attenuation)
	// Looping: at EndPos the channel restarts at Sfx.LoopStart instead of
	// stopping. tyrquake: channel_t.looping, set when the sound starts to
	// (sc->loopstart >= 0) (snd_dma.c, SND_StartSound and S_StaticSound).
	Looping bool
}

// Free reports whether c is an available slot.
func (c *Channel) Free() bool { return c.Sfx == nil }

// Stop empties the channel (marks it free).
func (c *Channel) Stop() {
	c.Sfx = nil
	c.Position = 0
	c.EndPos = 0
	c.LeftVol = 0
	c.RightVol = 0
	c.Looping = false
}

// Pool is the fixed-cap channel bank. Slot 0..(reservedStatic-1)
// are reserved for ambient/looping sounds (the level's static
// ambience track); slot reservedStatic..(MaxChannels-1) are dynamic.
type Pool struct {
	Channels       [MaxChannels]Channel
	ReservedStatic int // 0..MaxChannels; default 8
}

var (
	ErrPoolBadReserve = errors.New("sound: ReservedStatic must be in [0, MaxChannels]")
	ErrPoolNoFreeSlot = errors.New("sound: pool exhausted (no slot can be allocated)")
	ErrChannelSlotBad = errors.New("sound: channel slot index out of range")
)

// NewPool returns a fresh pool with reservedStatic ambient slots.
func NewPool(reservedStatic int) (*Pool, error) {
	if reservedStatic < 0 || reservedStatic > MaxChannels {
		return nil, ErrPoolBadReserve
	}
	return &Pool{ReservedStatic: reservedStatic}, nil
}

// Alloc returns the slot index of a free dynamic channel, or evicts
// the lowest-priority active slot if the pool is full. Returns
// FreeChannel + ErrPoolNoFreeSlot if everything is high-priority
// reserved.
//
// `entNum` + `entChannel`: if a channel is already playing for this
// (entity, channel) pair, that slot is reused (the upstream's
// "same entity/channel replaces previous sound" rule). tyrquake:
// SND_PickChannel.
func (p *Pool) Alloc(entNum, entChannel int) (int, error) {
	if entChannel != 0 {
		for i := p.ReservedStatic; i < MaxChannels; i++ {
			c := &p.Channels[i]
			if c.EntNum == entNum && c.EntChannel == entChannel && !c.Free() {
				return i, nil
			}
		}
	}
	for i := p.ReservedStatic; i < MaxChannels; i++ {
		if p.Channels[i].Free() {
			return i, nil
		}
	}
	// All dynamic slots in use. Lowest position-from-end wins
	// eviction (oldest still-playing channel). Simple heuristic:
	// pick the slot with the smallest EndPos-Position remaining.
	best := FreeChannel
	bestRemaining := -1
	for i := p.ReservedStatic; i < MaxChannels; i++ {
		c := &p.Channels[i]
		rem := c.EndPos - c.Position
		if best == FreeChannel || rem < bestRemaining {
			best = i
			bestRemaining = rem
		}
	}
	if best == FreeChannel {
		return FreeChannel, ErrPoolNoFreeSlot
	}
	p.Channels[best].Stop()
	return best, nil
}

// StopAll stops every channel (both static and dynamic). tyrquake:
// S_StopAllSounds.
func (p *Pool) StopAll() {
	for i := range p.Channels {
		p.Channels[i].Stop()
	}
}

// ActiveCount returns the number of channels currently playing.
func (p *Pool) ActiveCount() int {
	n := 0
	for i := range p.Channels {
		if !p.Channels[i].Free() {
			n++
		}
	}
	return n
}
