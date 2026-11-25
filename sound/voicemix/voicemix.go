// Copyright (c) 1996-1997 Id Software, Inc.
// Copyright (c) 2026 the go-quake1/engine authors.
// SPDX-License-Identifier: GPL-2.0-or-later

// Package voicemix is the game's side of the voice mixer of
// go-wasmcontainers/engine (docs/audio-design.md section 10): it implements
// sound.Voices over the host's sample_load and voice_commit calls. The mixing
// is done in the page's AudioWorklet (wasmdesk/wasmbox audioring), so the game
// only loads each sound once and says when to start and stop it.
package voicemix

import (
	"encoding/binary"
	"errors"
	"fmt"

	"github.com/wasmdesk/wasmbox/audioring"

	"github.com/go-quake1/engine/sound"
)

// Host is the host's voice calls, for one mixer.
type Host interface {
	// Load copies a sound into the bank under id (sample_load).
	Load(id, bits, rate, loopStart, frames int, pcm []byte) error
	// Commit appends the commands (4 words each) and stores the slots' snapshot
	// (voice_commit): per slot, generation | active << 16 and frames left.
	Commit(words []int32, snap *[2 * audioring.VoiceSlots]uint32) error
}

// First bank id of the music: the sounds take 0.. as they load, the music the
// ids from here up (a loaded id cannot be reloaded, so each track takes two).
const musicBase = audioring.MaxSamples - 24

// Slots of the music's two ears: the last sound.MusicSlots slots.
const musicSlotL = audioring.VoiceSlots - sound.MusicSlots

// Mixer implements sound.Voices.
type Mixer struct {
	host  Host
	gen   [audioring.VoiceSlots]uint16
	live  [audioring.VoiceSlots]bool // started and not yet seen retired
	ids   map[*sound.Sample]int      // bank id, -1 when the host refused it
	next  int
	music int // next music bank id
	words []int32
	snap  [2 * audioring.VoiceSlots]uint32
	seen  bool // a Commit has filled snap

	musicOn bool // the music slots play

	// Refused counts the commands and loads the host or the bank refused.
	Refused int
	// LastErr is the last such refusal.
	LastErr error
}

var _ sound.Voices = (*Mixer)(nil)

// New returns a Mixer over host.
func New(host Host) *Mixer {
	return &Mixer{host: host, ids: map[*sound.Sample]int{}, music: musicBase}
}

func (m *Mixer) refuse(err error) {
	m.Refused++
	m.LastErr = err
}

// load returns s's bank id, loading it on first use; -1 when it cannot play.
func (m *Mixer) load(s *sound.Sample) int {
	if id, ok := m.ids[s]; ok {
		return id
	}
	id := -1
	if m.next < musicBase && s.NumSamples > 0 && (s.BitsPerSam == 8 || s.BitsPerSam == 16) &&
		len(s.Data) == s.NumSamples*s.BitsPerSam/8 {
		loop := s.LoopStart
		if loop < 0 || loop >= s.NumSamples {
			loop = 0
		}
		// Sample.SampleRate is not used: sound.Paint plays every sample at the
		// mix rate, and so does the mixer.
		if err := m.host.Load(m.next, s.BitsPerSam, sound.DefaultSampleRate, loop, s.NumSamples, s.Data); err != nil {
			m.refuse(fmt.Errorf("voicemix: load %q: %w", s.Name, err))
		} else {
			id = m.next
			m.next++
		}
	}
	m.ids[s] = id
	return id
}

func (m *Mixer) push(c audioring.Command) {
	w, err := c.Encode()
	if err != nil {
		m.refuse(err)
		return
	}
	m.words = append(m.words, w[0], w[1], w[2], w[3])
}

// Start implements sound.Voices.
func (m *Mixer) Start(slot int, s *sound.Sample, loop bool, left, right int) {
	if slot < 0 || slot >= musicSlotL {
		return
	}
	id := m.load(s)
	if id < 0 {
		m.live[slot] = false
		return
	}
	m.start(slot, id, loop && s.LoopStart >= 0 && s.LoopStart < s.NumSamples, left, right)
}

func (m *Mixer) start(slot, id int, loop bool, left, right int) {
	m.gen[slot]++
	m.live[slot] = true
	m.push(audioring.Command{Op: audioring.OpStart, Slot: slot, Gen: m.gen[slot], Sample: id,
		Loop: loop, Left: clampVol(left), Right: clampVol(right)})
}

func clampVol(v int) int {
	if v < 0 {
		return 0
	}
	if v > audioring.MaxVolume {
		return audioring.MaxVolume
	}
	return v
}

// SetVolume implements sound.Voices.
func (m *Mixer) SetVolume(slot, left, right int) {
	if slot < 0 || slot >= musicSlotL || !m.live[slot] {
		return
	}
	m.push(audioring.Command{Op: audioring.OpSetVol, Slot: slot, Gen: m.gen[slot], Left: clampVol(left), Right: clampVol(right)})
}

// Stop implements sound.Voices.
func (m *Mixer) Stop(slot int) {
	if slot < 0 || slot >= musicSlotL || !m.live[slot] {
		return
	}
	m.live[slot] = false
	m.push(audioring.Command{Op: audioring.OpStop, Slot: slot, Gen: m.gen[slot]})
}

// StopAll implements sound.Voices. The music is not a sound the game stops
// with the others (S_StopAllSounds leaves the CD playing).
func (m *Mixer) StopAll() {
	for i := 0; i < musicSlotL; i++ {
		m.Stop(i)
	}
}

// Retired implements sound.Voices: the last start on slot has been applied
// and the slot is no longer active.
func (m *Mixer) Retired(slot int) bool {
	if slot < 0 || slot >= musicSlotL || !m.live[slot] || !m.seen {
		return false
	}
	w := m.snap[2*slot]
	return uint16(w) == m.gen[slot] && w>>16&1 == 0
}

// Remaining implements sound.Voices.
func (m *Mixer) Remaining(slot int) int {
	if slot < 0 || slot >= musicSlotL || !m.seen {
		return -1
	}
	w := m.snap[2*slot]
	if uint16(w) != m.gen[slot] || w>>16&1 == 0 {
		return -1
	}
	return int(m.snap[2*slot+1])
}

// Commit implements sound.Voices. A batch the host refuses is dropped and
// counted: a sound lost is better than a game that stops.
func (m *Mixer) Commit() error {
	words := m.words
	m.words = m.words[:0]
	var err error
	// At least one call a tic, even with nothing to say: it refreshes the snapshot.
	for {
		n := len(words) / audioring.CmdWords
		if n > audioring.CmdEntries {
			n = audioring.CmdEntries
		}
		if e := m.host.Commit(words[:n*audioring.CmdWords], &m.snap); e != nil {
			m.refuse(e)
			err = e
			break
		}
		m.seen = true
		words = words[n*audioring.CmdWords:]
		if len(words) == 0 {
			break
		}
	}
	return err
}

// SetMusic implements sound.Voices.
func (m *Mixer) SetMusic(left, right []int16, loopStart int) error {
	if m.musicOn {
		m.musicOn = false
		for i := musicSlotL; i < audioring.VoiceSlots; i++ {
			m.push(audioring.Command{Op: audioring.OpStop, Slot: i, Gen: m.gen[i]})
		}
	}
	if len(left) == 0 || len(left) != len(right) {
		return nil
	}
	if m.music+2 > audioring.MaxSamples {
		return errors.New("voicemix: no bank id left for another track")
	}
	loop := loopStart >= 0 && loopStart < len(left)
	ls := 0
	if loop {
		ls = loopStart
	}
	for ear, pcm := range [][]int16{left, right} {
		b := make([]byte, 2*len(pcm))
		for i, v := range pcm {
			binary.LittleEndian.PutUint16(b[2*i:], uint16(v))
		}
		if err := m.host.Load(m.music+ear, 16, sound.DefaultSampleRate, ls, len(pcm), b); err != nil {
			m.refuse(fmt.Errorf("voicemix: load music: %w", err))
			return err
		}
	}
	id := m.music
	m.music += 2
	m.musicOn = true
	vl, vr := audioring.MaxVolume, 0
	for ear := 0; ear < sound.MusicSlots; ear++ {
		slot := musicSlotL + ear
		m.gen[slot]++
		m.push(audioring.Command{Op: audioring.OpStart, Slot: slot, Gen: m.gen[slot], Sample: id + ear, Loop: loop, Left: vl, Right: vr})
		vl, vr = vr, audioring.MaxVolume
	}
	return nil
}
