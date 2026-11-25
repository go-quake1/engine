// Copyright (c) 1996-1997 Id Software, Inc.
// Copyright (c) 2026 the go-quake1/engine authors.
// SPDX-License-Identifier: GPL-2.0-or-later

package runloop

import (
	"github.com/go-quake1/engine/backend"
	"github.com/go-quake1/engine/music"
	"github.com/go-quake1/engine/sound"
)

// Music through a voice mixer: the track is decoded a slice a tic (so a long
// Vorbis stream does not cost one long tic), uploaded once, and the mixer
// loops it on its own.
const (
	musicChunkFrames = 8192                          // source frames decoded per tic
	musicMaxFrames   = 240 * sound.DefaultSampleRate // a track is cut here
)

// musicJob is a track being decoded for the voice mixer.
type musicJob struct {
	s         *music.Streamer
	nextLoop  int // track to append after s ends (0 = none)
	loopStart int // frame the mixer loops from; -1 = no loop
	l, r      []int16
	frac      float64 // position of the next output frame inside the source chunk
	loopSelf  bool    // the track loops onto itself
}

// voiceStep is the audio step of a tic for a backend with a voice mixer: it
// has nothing to paint, so it keeps the channels in step with the mixer, runs
// the music driver and sends the commands.
func (r *Runner) voiceStep(v sound.Voices) {
	if r.SoundPool.Voices != v {
		r.SoundPool.AttachVoices(v)
	}
	r.SoundPool.Sync()
	r.tickMusicVoices(v)
	if err := v.Commit(); err != nil {
		r.VoiceRefused++
	}
}

// tickMusicVoices is tickMusic for a voice mixer.
func (r *Runner) tickMusicVoices(v sound.Voices) {
	if r.Client == nil || r.MusicOpen == nil || r.MusicDecoder == nil {
		return
	}
	if r.Client.MusicEpoch != r.musicEpochSeen {
		r.musicEpochSeen = r.Client.MusicEpoch
		r.musicJob = nil
		v.SetMusic(nil, nil, -1)
		track := r.Client.MusicTrack
		if track > 0 {
			loop := r.Client.MusicLoopTrack
			s, err := music.LoadTrack(r.MusicOpen, r.MusicDecoder, track, 0, r.MusicVolume)
			if err != nil {
				r.logMusicMissingOnce(track, loop)
			} else {
				j := &musicJob{s: s, loopStart: -1}
				switch {
				case loop == track:
					j.loopSelf, j.loopStart = true, 0
				case loop > 0:
					j.nextLoop = loop
				}
				r.musicJob = j
			}
		}
	}
	j := r.musicJob
	if j == nil {
		return
	}
	if len(j.l) < musicMaxFrames {
		j.decode()
	}
	if j.s != nil && !j.s.Stopped() && len(j.l) < musicMaxFrames {
		return // more to decode next tic
	}
	// The source ended (or the cap was reached).
	if j.nextLoop > 0 && len(j.l) < musicMaxFrames {
		s, err := music.LoadTrack(r.MusicOpen, r.MusicDecoder, j.nextLoop, 0, r.MusicVolume)
		j.loopStart = len(j.l)
		j.nextLoop = 0
		if err == nil {
			j.s = s
			return
		}
		r.logMusicMissingOnce(r.Client.MusicTrack, r.Client.MusicLoopTrack)
		j.loopStart = -1
	}
	r.musicJob = nil
	if len(j.l) == 0 {
		return
	}
	if err := v.SetMusic(j.l, j.r, j.loopStart); err != nil {
		r.VoiceRefused++
	}
}

// decode reads one chunk of the stream and appends it at the mix rate (nearest
// frame; the mixer plays everything at 11025 Hz).
func (j *musicJob) decode() {
	var buf [musicChunkFrames]sound.StereoSample
	rate := j.s.SampleRate() // 0 once the stream has stopped, so before reading
	n := j.s.NextSamples(buf[:])
	if rate <= 0 {
		rate = sound.DefaultSampleRate
	}
	step := float64(rate) / sound.DefaultSampleRate
	for ; j.frac < float64(n) && len(j.l) < musicMaxFrames; j.frac += step {
		f := buf[int(j.frac)]
		j.l = append(j.l, f.L)
		j.r = append(j.r, f.R)
	}
	j.frac -= float64(n)
	if j.frac < 0 {
		j.frac = 0
	}
}

// voices returns the backend's voice mixer, nil when it has none.
func (r *Runner) voices() sound.Voices {
	if vb, ok := r.Backend.(backend.VoiceBackend); ok {
		return vb.Voices()
	}
	return nil
}
