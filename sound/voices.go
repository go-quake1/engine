// Copyright (c) 1996-1997 Id Software, Inc.
// Copyright (c) 2026 the go-quake1/engine authors.
// SPDX-License-Identifier: GPL-2.0-or-later

package sound

// Voices is a mixer that runs somewhere else than this process's thread: the
// AudioWorklet of a browser page, reached through go-wasmcontainers/engine's
// voice_* host imports. The pool stays the game's model of the channels (who
// plays, for which entity); the mixer owns the playing positions and paints
// them itself, so a game that stalls does not stop a sound that is playing.
//
// Slot numbers are the pool's channel indices. The mixer applies the commands
// at Commit and reports, through Retired and Remaining, what its last Commit
// saw of each slot.
type Voices interface {
	// Start plays s on slot from its start (a replacement if the slot plays).
	Start(slot int, s *Sample, loop bool, left, right int)
	// SetVolume changes the volumes of a playing slot.
	SetVolume(slot, left, right int)
	// Stop silences slot.
	Stop(slot int)
	// StopAll silences every slot.
	StopAll()
	// Commit sends the commands queued since the last Commit and refreshes the
	// view Retired and Remaining read.
	Commit() error
	// Retired reports that the mixer has finished the sound last started on
	// slot, as of the last Commit.
	Retired(slot int) bool
	// Remaining is the sample frames the sound on slot has left, as of the
	// last Commit.
	Remaining(slot int) int
	// SetMusic plays the 16-bit stereo track (left and right PCM at the mix
	// rate) on the slots the pool keeps for it, from its start; frames from
	// loopStart on repeat when loopStart >= 0. A nil left stops the music.
	SetMusic(left, right []int16, loopStart int) error
}

// MusicSlots is the number of slots at the end of the pool's range that a
// Voices mixer keeps for the music (one for each ear).
const MusicSlots = 2

// AttachVoices hands the pool's channels to v: from now on starting a sound
// queues a command, and what plays is whatever v paints. Channels already
// playing (the level's ambients, seeded before the mixer existed) start now.
func (p *Pool) AttachVoices(v Voices) {
	p.Voices = v // nil detaches: Launch does nothing without a mixer
	for i := range p.Channels {
		if !p.Channels[i].Free() {
			p.Launch(i)
		}
	}
}

// limit is the number of channels the game can use: the music has the last
// MusicSlots when a Voices mixer is attached.
func (p *Pool) limit() int {
	if p.Voices != nil {
		return MaxChannels - MusicSlots
	}
	return MaxChannels
}

// Launch tells the mixer that slot now plays what its Channel says: a sound
// the game just put there. It is a no-op without a Voices mixer.
func (p *Pool) Launch(slot int) {
	if p.Voices == nil || slot < 0 || slot >= MaxChannels {
		return
	}
	c := &p.Channels[slot]
	if c.Free() {
		p.Voices.Stop(slot)
		return
	}
	p.Voices.Start(slot, c.Sfx, c.Looping, c.LeftVol, c.RightVol)
}

// Retune tells the mixer that the volumes of slot changed.
func (p *Pool) Retune(slot int) {
	if p.Voices == nil || slot < 0 || slot >= MaxChannels || p.Channels[slot].Free() {
		return
	}
	c := &p.Channels[slot]
	p.Voices.SetVolume(slot, c.LeftVol, c.RightVol)
}

// Sync reads the mixer's view into the channels: a sound the mixer finished
// frees its channel, and the others learn how much they have left. Alloc calls
// it, so a slot the mixer has finished is free to the game at once.
func (p *Pool) Sync() {
	if p.Voices == nil {
		return
	}
	for i := range p.Channels {
		c := &p.Channels[i]
		if c.Free() {
			continue
		}
		if p.Voices.Retired(i) {
			c.Stop()
			continue
		}
		if rem := p.Voices.Remaining(i); rem >= 0 && rem <= c.EndPos {
			c.Position = c.EndPos - rem
		}
	}
}
