// Copyright (c) 1996-1997 Id Software, Inc.
// Copyright (c) 2026 the go-quake1/engine authors.
// SPDX-License-Identifier: GPL-2.0-or-later

//go:build wasip1 && wctraudio

package main

import (
	"errors"
	"fmt"
	"unsafe"

	"github.com/wasmdesk/wasmbox/audioring"

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

// The voice mixer of the same module (docs/audio-design.md): the game loads
// each sound once and sends commands; the mixer runs in the page.

//go:wasmimport wctr_audio_out voice_open
func voiceOpen(rate uint32, out unsafe.Pointer) uint32

//go:wasmimport wctr_audio_out sample_load
func sampleLoad(mixer, id, ptr, length, bits, rate, loopStart, nsamples uint32) uint32

//go:wasmimport wctr_audio_out voice_commit
func voiceCommit(mixer, cmds, n uint32, snap unsafe.Pointer) uint32

//go:wasmimport wctr_audio_out voice_stats
func voiceStats(mixer uint32, out unsafe.Pointer) uint32

//go:wasmimport wctr_audio_out voice_close
func voiceClose(mixer uint32) uint32

func init() { openVoices = openWctrVoices }

func errnoError(op string, e uint32) error {
	msg, ok := audioErrno[e]
	if !ok {
		msg = fmt.Sprintf("errno %d", e)
	}
	return fmt.Errorf("%s: %s", op, msg)
}

func openWctrVoices() (wasisurface.VoiceHost, error) {
	var h uint32
	if e := voiceOpen(audioRate, unsafe.Pointer(&h)); e != 0 {
		return nil, errnoError("voice_open", e)
	}
	return &wctrVoices{h: h}, nil
}

// wctrVoices is one voice_open handle as a wasisurface.VoiceHost.
type wctrVoices struct{ h uint32 }

func (v *wctrVoices) Load(id, bits, rate, loopStart, frames int, pcm []byte) error {
	if len(pcm) == 0 {
		return errors.New("sample_load: empty sound")
	}
	if e := sampleLoad(v.h, uint32(id), uint32(uintptr(unsafe.Pointer(&pcm[0]))), uint32(len(pcm)),
		uint32(bits), uint32(rate), uint32(loopStart), uint32(frames)); e != 0 {
		return errnoError("sample_load", e)
	}
	return nil
}

func (v *wctrVoices) Commit(words []int32, snap *[2 * audioring.VoiceSlots]uint32) error {
	var p uint32
	if len(words) > 0 {
		p = uint32(uintptr(unsafe.Pointer(&words[0])))
	}
	if e := voiceCommit(v.h, p, uint32(len(words)/4), unsafe.Pointer(snap)); e != 0 {
		return errnoError("voice_commit", e)
	}
	return nil
}

func (v *wctrVoices) Stats() (wasisurface.VoiceStats, error) {
	var s wasisurface.VoiceStats
	if e := voiceStats(v.h, unsafe.Pointer(&s)); e != 0 {
		return s, errnoError("voice_stats", e)
	}
	return s, nil
}

func (v *wctrVoices) Close() { voiceClose(v.h) }
