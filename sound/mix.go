// Copyright (c) 1996-1997 Id Software, Inc.
// Copyright (c) 2026 the go-quake1/engine authors.
// SPDX-License-Identifier: GPL-2.0-or-later

package sound

import (
	"encoding/binary"
	"errors"
)

// MixBufferStereoFrames is the working accumulator's frame count.
// One stereo frame = 2 int16s (L + R). tyrquake uses 512 frames in
// paintbuffer; the Go port matches.
const MixBufferStereoFrames = 512

// MaxMixOutputFrames is the per-call ceiling on Paint's output
// length. Anything longer must be batched.
const MaxMixOutputFrames = MixBufferStereoFrames

var (
	// ErrMixOutputTooLarge is returned when numFrames exceeds
	// MaxMixOutputFrames or len(output), or when ClampToInt8's
	// input/output slices disagree in length.
	ErrMixOutputTooLarge = errors.New("sound: paint output length > MaxMixOutputFrames")
	// ErrMixNilPool is returned when Paint is called with a nil pool.
	ErrMixNilPool = errors.New("sound: nil pool in Paint")
	// ErrMixNilOutput is returned when Paint is called with a nil
	// output buffer.
	ErrMixNilOutput = errors.New("sound: nil output buffer in Paint")
	// ErrMixBadFormat is returned when an active channel's sample
	// has a BitsPerSam other than 8 or 16. tyrquake mirrors:
	// SND_PaintChannelFrom8 + SND_PaintChannelFrom16 are the only
	// two paint paths id Software shipped; anything else is a loader
	// bug and gets rejected here before any state mutation.
	ErrMixBadFormat = errors.New("sound: sample BitsPerSam must be 8 or 16")
)

// StereoSample is one (L, R) int16 frame.
type StereoSample struct {
	L, R int16
}

// Paint mixes `numFrames` stereo samples from every active channel
// into `output`. Each output frame is a (L, R) int16 pair. After
// mixing, each channel's Position is advanced by numFrames. Channels
// whose Position reaches EndPos are stopped (Sfx = nil) and skipped
// for the rest of this call.
//
// tyrquake: S_PaintChannels (plus SND_PaintChannelFrom8 +
// SND_PaintChannelFrom16 for the per-channel inner loops).
//
// Per-channel mix per output frame i (8-bit path):
//
//	left[i]  += sample[ch.Position + i] * ch.LeftVol  / 256
//	right[i] += sample[ch.Position + i] * ch.RightVol / 256
//
// The 8-bit sample bytes are int8 (signed); the multiply lifts them
// to int16 without clipping (max int8 (127) * max vol (255) = 32385,
// < int16 max). The Data byte at index ch.Position+i is the next
// PCM sample (one byte = one sample).
//
// Per-channel mix per output frame i (16-bit path):
//
//	s = int16(LittleEndian(Data[2*(ch.Position+i) : 2*(ch.Position+i)+2]))
//	left[i]  += int16(int32(s) * int32(ch.LeftVol)  >> 8)
//	right[i] += int16(int32(s) * int32(ch.RightVol) >> 8)
//
// The 16-bit samples are signed little-endian int16s packed two
// bytes per sample (LoadWav stores them verbatim from the WAV body).
// The `>> 8` aligns the 16-bit result with the 8-bit path's scale --
// tyrquake's SND_PaintChannelFrom16 does the same (vol >> 8). The
// int32 widen on the multiply is mandatory: int16(32767) *
// int16(255) overflows int16; the widen + arithmetic-shift keeps the
// sign before the narrow back to int16.
//
// Position accounting is in SAMPLE units, NOT byte units, so the
// per-call advance (`ch.Position += numFrames`) is identical for
// both bit-depths; only the byte indexing into Data differs (factor
// of 2 for 16-bit).
//
// Caller is responsible for clamping/converting the int16 accumulator
// to whatever the audio backend wants (int8 / int16 / float32). The
// mixer exposes the 16-bit intermediate so backends with > 8-bit
// hardware retain the full dynamic range. Caller is also responsible
// for zeroing `output` between calls; Paint accumulates into existing
// content (matches tyrquake's paintbuffer-then-music-then-transfer
// pipeline).
//
// Returns:
//
//	ErrMixNilPool         pool == nil
//	ErrMixNilOutput       output == nil
//	ErrMixOutputTooLarge  numFrames > MaxMixOutputFrames OR > len(output)
//	ErrMixBadFormat       any active channel's Sfx.BitsPerSam not in {8, 16}
func Paint(pool *Pool, output []StereoSample, numFrames int) error {
	if pool == nil {
		return ErrMixNilPool
	}
	if output == nil {
		return ErrMixNilOutput
	}
	if numFrames > MaxMixOutputFrames || numFrames > len(output) {
		return ErrMixOutputTooLarge
	}

	// First pass: validate every active channel's sample format
	// before mutating any state. Mismatches at this layer mean the
	// loader handed us a sample type the mixer does not understand
	// (LoadWav only emits 8 or 16); failing fast prevents partial
	// mixes / corrupted position advances.
	for i := range pool.Channels {
		ch := &pool.Channels[i]
		if ch.Free() {
			continue
		}
		if ch.Sfx.BitsPerSam != 8 && ch.Sfx.BitsPerSam != 16 {
			return ErrMixBadFormat
		}
	}

	// WinQuake's paintbuffer is an int array clamped once at the transfer:
	// sum every channel in int32 and clamp at the end, so a loud mix
	// saturates instead of wrapping to the opposite sign.
	var acc [MaxMixOutputFrames * 2]int32
	for i := 0; i < numFrames; i++ {
		acc[2*i] = int32(output[i].L)
		acc[2*i+1] = int32(output[i].R)
	}
	defer func() {
		for i := 0; i < numFrames; i++ {
			output[i].L = clamp16(acc[2*i])
			output[i].R = clamp16(acc[2*i+1])
		}
	}()

	for i := range pool.Channels {
		ch := &pool.Channels[i]
		if ch.Free() {
			continue
		}
		// Paint numFrames frames from this channel. EndPos is the
		// sample index at which playback halts: a channel that gets
		// there is retired, unless it is looping, in which case it
		// restarts at the sample's LoopStart and the rest of the
		// frames come from there (as many wraps as the frames need).
		// tyrquake: S_PaintChannels, "if at end of loop, restart"
		// (snd_mix.c): `if (ch->looping && sc->loopstart >= 0) {
		// ch->pos = sc->loopstart; ch->end = ltime + sc->length - ch->pos;
		// } else { ch->sfx = NULL; break; }`.
		done := 0
		for done < numFrames {
			remaining := ch.EndPos - ch.Position
			count := numFrames - done
			if remaining < count {
				count = remaining
			}
			if count > 0 {
				paintSamples(ch, acc[2*done:2*(done+count)])
				ch.Position += count
				done += count
			}
			if ch.Position >= ch.EndPos {
				// A loop point at or past the end would never advance:
				// treat it as not looped rather than spin.
				if ch.Looping && ch.Sfx.LoopStart >= 0 && ch.Sfx.LoopStart < ch.EndPos {
					ch.Position = ch.Sfx.LoopStart
					continue
				}
				ch.Stop()
				break
			}
		}
	}
	return nil
}

// paintSamples mixes len(out) samples of ch, from ch.Position, into out.
// out holds interleaved L, R int32 accumulators.
func paintSamples(ch *Channel, out []int32) {
	data := ch.Sfx.Data
	pos := ch.Position
	lv := int32(ch.LeftVol)
	rv := int32(ch.RightVol)
	switch ch.Sfx.BitsPerSam {
	case 8:
		// 8-bit signed bytes; one byte per sample.
		for j := 0; j < len(out)/2; j++ {
			s := int32(int8(data[pos+j]))
			out[2*j] += s * lv / 256
			out[2*j+1] += s * rv / 256
		}
	case 16:
		// 16-bit signed little-endian; two bytes per sample.
		// int32 widen on the multiply avoids int16 overflow
		// (int16 * int16 can exceed int16 range); `>> 8`
		// brings the result to the 8-bit path's scale so
		// downstream clamp + transfer logic is bit-depth-
		// agnostic.
		for j := 0; j < len(out)/2; j++ {
			off := (pos + j) * 2
			s := int32(int16(binary.LittleEndian.Uint16(data[off : off+2])))
			out[2*j] += s * lv >> 8
			out[2*j+1] += s * rv >> 8
		}
	}
}

// clamp16 saturates v to the int16 range (WinQuake's transfer clamp).
func clamp16(v int32) int16 {
	if v > 32767 {
		return 32767
	}
	if v < -32768 {
		return -32768
	}
	return int16(v)
}

// ClampToInt8 converts the int16 stereo accumulator to int8 PCM,
// clamping any per-sample value outside [-128, 127] to the boundary.
// Convenience for backends whose hardware output is 8-bit signed PCM
// (Quake's stock DMA path: 8-bit hardware was mono in the late-90s).
//
// Each frame's L and R are averaged into a single int8, so len(out)
// must equal len(in). Returns ErrMixOutputTooLarge on length mismatch.
// tyrquake: the samplebits==8 branch of S_TransferPaintBuffer.
func ClampToInt8(in []StereoSample, out []int8) error {
	if len(in) != len(out) {
		return ErrMixOutputTooLarge
	}
	for i, s := range in {
		// Average L+R for the mono 8-bit path. int32 widen avoids
		// the (L+R) intermediate overflowing int16.
		v := (int32(s.L) + int32(s.R)) / 2
		if v > 127 {
			out[i] = 127
		} else if v < -128 {
			out[i] = -128
		} else {
			out[i] = int8(v)
		}
	}
	return nil
}
