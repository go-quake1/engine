// Copyright (c) 1996-1997 Id Software, Inc.
// Copyright (c) 2026 the go-quake1/engine authors.
// SPDX-License-Identifier: GPL-2.0-or-later

package main

import (
	"fmt"
	"io"
	"time"

	"github.com/go-quake1/engine/backend"
	"github.com/go-quake1/engine/backend/wasisurface"
	"github.com/go-quake1/engine/runloop"
	"github.com/go-quake1/engine/sound"
)

// openAudio opens the container's sound stream: 11025 Hz stereo, the mixer's
// rate, through the host module wctr_audio_out of go-wasmcontainers/engine
// (docs/audio-design.md). audio_wasip1.go sets it, in a build with the tag
// wctraudio; it is nil in any other build (nothing imports the module, so a
// host without it still runs the game, silent), and tests set it to a fake.
// An error is the grant's absence (Denied: the container was not run with
// --audio; Unsupported: this host has no audio output): the game then plays
// silent and raises nothing.
var openAudio func() (wasisurface.AudioOut, error)

// fixedMixer paints a fixed chunk of the mix every tic, as the runloop did
// before it paced the mixer (--fixed-mixer, hidden: a measuring aid).
var fixedMixer bool

// attachAudio gives be the sound output when this build and this container
// have one, says which on errOut, and returns the function that closes it.
func attachAudio(be *wasisurface.Backend, errOut io.Writer) (closeAudio func()) {
	if openAudio == nil {
		fmt.Fprintln(errOut, "QUAKE: sound off (this build has no wctr_audio_out)")
		return func() {}
	}
	out, err := openAudio()
	if err != nil {
		fmt.Fprintf(errOut, "QUAKE: sound off: %v\n", err)
		return func() {}
	}
	be.WithAudio(out)
	fmt.Fprintln(errOut, "QUAKE: sound on (wctr_audio_out, 11025 Hz stereo)")
	return out.Close
}

// audioStatser is what writeAudioStats reads: the backend, or a wrapper that
// forwards it.
type audioStatser interface{ AudioStats() wasisurface.AudioStats }

// mixerBackend is the backend the game loop drives: be itself, or with
// --fixed-mixer a wrapper that hides its queue (a struct embedding the Backend
// interface promotes only Backend's methods), so the runloop paints the fixed
// chunk.
func mixerBackend(be *wasisurface.Backend) backend.Backend {
	if fixedMixer {
		return fixedChunk{Backend: be, stats: be.AudioStats}
	}
	return be
}

type fixedChunk struct {
	backend.Backend
	stats func() wasisurface.AudioStats
}

func (f fixedChunk) AudioStats() wasisurface.AudioStats { return f.stats() }

// writeAudioStats prints what the sound output did over a --frames run, and
// the real-time ratio: mix frames offered per second of game time over the
// 11025 a second an output plays. 1.00 is real time (a little above it is the
// mix-ahead the first tic prefills); the fixed chunk is 2.8 at 60 tics a second.
//
// The line goes on with what explains a ratio under 1: the game's frame rate,
// and the mix frames of the time the stalled tics (the ones that outlasted the
// queue the update before left) had the output with nothing to play
// (runloop.Runner.MixDropped). Offered plus dropped is the whole of the game's
// time, so "with them" is 1.00 (a little above: the prefill) whatever the speed
// of the machine: a ratio under 1 with "with them" at 1.00 is a game that
// outlasted its queue, not a mixer that under-produces. The line ends with the
// tic lengths the ratio is read against: the tics over 0.1, 0.2, 0.35 and 0.5 s,
// and the longest.
func writeAudioStats(out io.Writer, be backend.Backend, el time.Duration, frames int, r *runloop.Runner) {
	s, ok := be.(audioStatser)
	if !ok || el <= 0 {
		return
	}
	st := s.AudioStats()
	total := el.Seconds() * sound.DefaultSampleRate
	state := "playing"
	if st.Silent {
		state = "silent"
	}
	fmt.Fprintf(out, "audio: %s, %d mix frames offered, %d accepted, in %.2f s: real-time ratio %.3f; %.1f frames a second, %d mix frames dropped in %d stalled tics, ratio with them %.3f; tics over 0.1 0.2 0.35 0.5 s: %d %d %d %d, longest %.3f s\n",
		state, st.Offered, st.Accepted, el.Seconds(), float64(st.Offered)/total, float64(frames)/el.Seconds(), r.MixDropped, r.MixStalls,
		float64(st.Offered+r.MixDropped)/total, r.MixTicsOver[0], r.MixTicsOver[1], r.MixTicsOver[2], r.MixTicsOver[3], r.MixLongest)
}
