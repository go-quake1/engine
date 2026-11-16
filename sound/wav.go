// Copyright (c) 1996-1997 Id Software, Inc.
// Copyright (c) 2026 the go-quake1/engine authors.
// SPDX-License-Identifier: GPL-2.0-or-later

package sound

import (
	"encoding/binary"
	"errors"
)

// WAV header magic + chunk identifiers.
var (
	wavMagicRIFF = [4]byte{'R', 'I', 'F', 'F'}
	wavMagicWAVE = [4]byte{'W', 'A', 'V', 'E'}
	wavMagicFmt  = [4]byte{'f', 'm', 't', ' '}
	wavMagicData = [4]byte{'d', 'a', 't', 'a'}
	wavMagicCue  = [4]byte{'c', 'u', 'e', ' '}
	wavMagicList = [4]byte{'L', 'I', 'S', 'T'}
)

// WAV format codes (a tiny subset; Quake assets are always PCM).
const (
	WavFormatPCM = 1
)

var (
	ErrWavShort       = errors.New("sound: wav lump too short for RIFF header")
	ErrWavNotRIFF     = errors.New("sound: wav lump missing RIFF magic")
	ErrWavNotWAVE     = errors.New("sound: wav lump missing WAVE magic")
	ErrWavMissingFmt  = errors.New("sound: wav lump missing fmt chunk")
	ErrWavMissingData = errors.New("sound: wav lump missing data chunk")
	ErrWavBadFormat   = errors.New("sound: wav lump non-PCM (Quake assets are PCM only)")
	ErrWavBadBits     = errors.New("sound: wav lump bits-per-sample must be 8 or 16")
	ErrWavBadChannels = errors.New("sound: wav lump channels must be 1 (Quake assets are mono)")
)

// LoadWav parses a WAV file blob into a *Sample. Supports:
//   - PCM only (audio_format == 1)
//   - mono only (channels == 1; Quake's hardcoded restriction)
//   - 8-bit (unsigned, converted to signed by subtracting 128) or
//     16-bit (signed LE)
//   - optional cue chunk for loop start (the "cue " chunk's first
//     entry's sample offset)
//
// `name` is recorded into Sample.Name so callers don't have to thread
// the asset name separately.
//
// Returns:
//
//	ErrWavShort        len(blob) < 12 (RIFF header alone)
//	ErrWavNotRIFF      bytes 0..3 != "RIFF"
//	ErrWavNotWAVE      bytes 8..11 != "WAVE"
//	ErrWavMissingFmt   no fmt chunk before EOF
//	ErrWavMissingData  no data chunk before EOF
//	ErrWavBadFormat    non-PCM format code
//	ErrWavBadChannels  channels != 1
//	ErrWavBadBits      bitsPerSam not in {8, 16}
//
// Any extra chunks (LIST, INFO, JUNK, ...) are skipped.
func LoadWav(name string, blob []byte) (*Sample, error) {
	if len(blob) < 12 {
		return nil, ErrWavShort
	}
	if blob[0] != 'R' || blob[1] != 'I' || blob[2] != 'F' || blob[3] != 'F' {
		return nil, ErrWavNotRIFF
	}
	if blob[8] != 'W' || blob[9] != 'A' || blob[10] != 'V' || blob[11] != 'E' {
		return nil, ErrWavNotWAVE
	}

	// Walk chunks starting at offset 12 (just past "WAVE").
	pos := 12
	var (
		fmtSeen    bool
		channels   int
		sampleRate int
		bitsPerSam int
		dataSeen   bool
		dataOffset int
		dataLength int
		loopStart  = -1
		markEnd    = 0 // loopstart + the loop length of a "mark" in a LIST chunk, 0: none
	)
	for pos+8 <= len(blob) {
		var id [4]byte
		copy(id[:], blob[pos:pos+4])
		size := int(binary.LittleEndian.Uint32(blob[pos+4 : pos+8]))
		bodyStart := pos + 8
		bodyEnd := bodyStart + size
		if bodyEnd > len(blob) {
			bodyEnd = len(blob) // truncated chunk; tolerate
		}
		switch id {
		case wavMagicFmt:
			if bodyEnd-bodyStart < 16 {
				return nil, ErrWavMissingFmt
			}
			format := int(binary.LittleEndian.Uint16(blob[bodyStart : bodyStart+2]))
			if format != WavFormatPCM {
				return nil, ErrWavBadFormat
			}
			channels = int(binary.LittleEndian.Uint16(blob[bodyStart+2 : bodyStart+4]))
			sampleRate = int(binary.LittleEndian.Uint32(blob[bodyStart+4 : bodyStart+8]))
			bitsPerSam = int(binary.LittleEndian.Uint16(blob[bodyStart+14 : bodyStart+16]))
			fmtSeen = true
		case wavMagicData:
			dataOffset = bodyStart
			dataLength = bodyEnd - bodyStart
			dataSeen = true
		case wavMagicCue:
			// Cue chunk has a uint32 count then per-cue 24-byte
			// records; field at offset +20 of each record is the
			// sample-frame offset. We only read the first.
			if bodyEnd-bodyStart >= 4+24 {
				loopStart = int(binary.LittleEndian.Uint32(blob[bodyStart+24 : bodyStart+28]))
			}
		case wavMagicList:
			// A LIST chunk after the cue chunk may carry a loop-length
			// marker; tyrquake's GetWavinfo ("this is not a proper
			// parse, but it works with cooledit"): "mark" at +20 of the
			// body, the samples in the loop at +16. The sound then ends
			// at loopstart + that length.
			if loopStart >= 0 && bodyEnd-bodyStart >= 24 && string(blob[bodyStart+20:bodyStart+24]) == "mark" {
				markEnd = loopStart + int(binary.LittleEndian.Uint32(blob[bodyStart+16:bodyStart+20]))
			}
		}
		// Chunks pad to even-byte boundaries on the wire.
		pos = bodyEnd
		if pos%2 != 0 {
			pos++
		}
	}

	if !fmtSeen {
		return nil, ErrWavMissingFmt
	}
	if !dataSeen {
		return nil, ErrWavMissingData
	}
	if channels != 1 {
		return nil, ErrWavBadChannels
	}
	if bitsPerSam != 8 && bitsPerSam != 16 {
		return nil, ErrWavBadBits
	}

	numSamples := dataLength / (bitsPerSam / 8)
	// The loop marker ends the sound early (info.samples in GetWavinfo; a
	// marker past the data is a bad loop length there, ignored here).
	if markEnd > 0 && markEnd < numSamples {
		numSamples = markEnd
	}
	pcm := make([]byte, dataLength)
	copy(pcm, blob[dataOffset:dataOffset+dataLength])
	if bitsPerSam == 8 {
		// WAV 8-bit is unsigned (silence = 128); the mixer expects
		// signed (silence = 0). Bias each byte.
		for i := range pcm {
			pcm[i] = byte(int(pcm[i]) - 128)
		}
	}

	return &Sample{
		Name:       name,
		SampleRate: sampleRate,
		BitsPerSam: bitsPerSam,
		LoopStart:  loopStart,
		NumSamples: numSamples,
		Data:       pcm,
	}, nil
}
