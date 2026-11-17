// Copyright (c) 1996-1997 Id Software, Inc.
// Copyright (c) 2026 the go-quake1/engine authors.
// SPDX-License-Identifier: GPL-2.0-or-later

//go:build wasip1 && wctraudio

package main

import (
	"errors"
	"fmt"
	"unsafe"

	"github.com/go-quake1/engine/backend/wasisurface"
)

// The host module wctr_audio_out (go-wasmcontainers/engine, package audio):
// not a wasi_ module, because no WASI proposal covers audio. A module that
// imports it needs a host that provides it, so this file is built only with
// the tag wctraudio (the playground's image); without the tag nothing imports
// it and the game runs silent anywhere.

//go:wasmimport wctr_audio_out stream_open
func streamOpen(rate, channels, capacity uint32, out unsafe.Pointer) uint32

//go:wasmimport wctr_audio_out stream_write
func streamWrite(stream, ptr, frames uint32, out unsafe.Pointer) uint32

//go:wasmimport wctr_audio_out stream_close
func streamClose(stream uint32) uint32

// The stream: 11025 Hz stereo (the mixer's rate; the page resamples), 8192
// frames of ring (743 ms). The ring must hold what the mixer runs ahead: the
// mix-ahead is 0.1 s (1102 frames, runloop.MixAhead, WinQuake's _snd_mixahead),
// refilled at every update. At 1024 frames (93 ms), less than the mix-ahead, the
// host refused the end of every write (playground#63: 6 % of the mix lost, 204
// underruns in 66 s at 14 frames a second).
const (
	audioRate     = 11025
	audioChannels = 2
	audioCapacity = 8192
)

var audioErrno = map[uint32]string{1: "invalid argument", 2: "denied: run the container with --audio", 3: "unsupported: this host has no audio output",
	4: "too large", 5: "quota exceeded", 9: "fault"}

func init() { openAudio = openWctrAudio }

func openWctrAudio() (wasisurface.AudioOut, error) {
	var h uint32
	if e := streamOpen(audioRate, audioChannels, audioCapacity, unsafe.Pointer(&h)); e != 0 {
		msg, ok := audioErrno[e]
		if !ok {
			msg = fmt.Sprintf("errno %d", e)
		}
		return nil, errors.New(msg)
	}
	return &wctrStream{h: h}, nil
}

// wctrStream is one stream_open handle as a wasisurface.AudioOut.
type wctrStream struct {
	h   uint32
	res [2]uint32 // {accepted, queued}, the host writes it
}

// Write is one stream_write: the host copies the frames during the call.
func (s *wctrStream) Write(pcm []float32) (accepted, queued int, err error) {
	frames := len(pcm) / audioChannels
	if frames == 0 {
		return 0, 0, nil
	}
	// The mixer paints at most MixBufferStereoFrames (512) a batch; the ring
	// holds audioCapacity, and a bigger write is TooLarge.
	if frames > audioCapacity {
		frames = audioCapacity
	}
	e := streamWrite(s.h, uint32(uintptr(unsafe.Pointer(&pcm[0]))), uint32(frames), unsafe.Pointer(&s.res))
	if e != 0 {
		return 0, 0, fmt.Errorf("stream_write: errno %d", e)
	}
	return int(s.res[0]), int(s.res[1]), nil
}

func (s *wctrStream) Close() { streamClose(s.h) }
