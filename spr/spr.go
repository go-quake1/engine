// Copyright (c) 1996-1997 Id Software, Inc.
// Copyright (c) 2026 the go-quake1/engine authors.
// SPDX-License-Identifier: GPL-2.0-or-later

package spr

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
)

// On-wire constants from tyrquake/include/spritegn.h.
const (
	// IDSP -- little-endian "IDSP" magic.
	IDSpriteHeader = uint32('I') | uint32('D')<<8 | uint32('S')<<16 | uint32('P')<<24
	Version        = 1

	headerSize        = 9 * 4 // 9 int32 fields
	frameHeaderSize   = 16    // origin[2] + width + height
	frameTypeTagSize  = 4     // one int32 type tag
	groupHeaderSize   = 4     // one int32 numframes
	groupIntervalSize = 4     // one float32 interval per group entry
)

// SPR_* orientation tags. tyrquake: same names.
const (
	VPParallelUpright  = 0
	FacingUpright      = 1
	VPParallel         = 2
	Oriented           = 3
	VPParallelOriented = 4
)

// Frame-type tag values written before each per-frame record.
const (
	FrameSingle = 0 // SPR_SINGLE
	FrameGroup  = 1 // SPR_GROUP
)

// Sync type from synctype_t. ST_SYNC = all sprites step in lockstep;
// ST_RAND = each instance offsets its frame index by a random seed.
const (
	SyncSync = 0
	SyncRand = 1
)

// Sentinel errors.
var (
	ErrBadMagic          = errors.New("spr: not a sprite file (bad IDSP magic)")
	ErrBadVersion        = errors.New("spr: unsupported version (need 1)")
	ErrShortRead         = errors.New("spr: short read")
	ErrSectionOutOfRange = errors.New("spr: frame payload extends past EOF")
	ErrBadFrameType      = errors.New("spr: unknown frame-type tag")
)

// Header mirrors dsprite_t.
type Header struct {
	Ident          uint32
	Version        int32
	Type           int32
	BoundingRadius float32
	Width          int32
	Height         int32
	NumFrames      int32
	BeamLength     float32
	SyncType       int32
}

// FrameHeader mirrors dspriteframe_t. Width and Height may differ
// from the file-level Header.Width/Height (the per-frame slot wins).
type FrameHeader struct {
	OriginX int32
	OriginY int32
	Width   int32
	Height  int32
}

// Frame is one decoded per-frame record. For SPR_SINGLE entries
// Group is nil and Bitmap holds the bytes. For SPR_GROUP entries
// Group is populated and Bitmap is nil.
type Frame struct {
	Type   int32       // FrameSingle or FrameGroup
	Single SingleFrame // valid when Type == FrameSingle
	Group  *GroupFrame // non-nil when Type == FrameGroup
}

// SingleFrame is one (header + bitmap) pair.
type SingleFrame struct {
	FrameHeader
	Bitmap []byte // length = Width * Height
}

// GroupFrame is a collection of single frames with per-frame timing.
// tyrquake: dspritegroup_t + intervals[] + numframes (single frame
// records back-to-back).
type GroupFrame struct {
	Intervals []float32     // monotonically increasing tic boundaries
	Frames    []SingleFrame // len(Frames) == len(Intervals)
}

// Sprite is a fully-decoded .spr file.
type Sprite struct {
	Header Header
	Frames []Frame
}

// Load reads src in full and decodes the sprite. src is held only
// for the duration of Load; the returned Sprite owns its bitmaps.
// tyrquake: Mod_LoadSpriteModel.
func Load(src io.ReaderAt, size int64) (*Sprite, error) {
	if src == nil {
		return nil, errors.New("spr: nil src")
	}
	if size < headerSize {
		return nil, ErrShortRead
	}
	raw := make([]byte, size)
	n, err := src.ReadAt(raw, 0)
	if err != nil && err != io.EOF {
		return nil, fmt.Errorf("spr: read: %w", err)
	}
	if int64(n) < size {
		return nil, ErrShortRead
	}

	h := Header{
		Ident:          binary.LittleEndian.Uint32(raw[0:4]),
		Version:        int32(binary.LittleEndian.Uint32(raw[4:8])),
		Type:           int32(binary.LittleEndian.Uint32(raw[8:12])),
		BoundingRadius: math.Float32frombits(binary.LittleEndian.Uint32(raw[12:16])),
		Width:          int32(binary.LittleEndian.Uint32(raw[16:20])),
		Height:         int32(binary.LittleEndian.Uint32(raw[20:24])),
		NumFrames:      int32(binary.LittleEndian.Uint32(raw[24:28])),
		BeamLength:     math.Float32frombits(binary.LittleEndian.Uint32(raw[28:32])),
		SyncType:       int32(binary.LittleEndian.Uint32(raw[32:36])),
	}
	if h.Ident != IDSpriteHeader {
		return nil, ErrBadMagic
	}
	if h.Version != Version {
		return nil, fmt.Errorf("%w: got %d", ErrBadVersion, h.Version)
	}

	if h.NumFrames < 0 {
		// A negative count used to load as a sprite of no frames whose header said
		// otherwise (found by FuzzLoad).
		return nil, fmt.Errorf("%w: %d frames", ErrSectionOutOfRange, h.NumFrames)
	}
	sp := &Sprite{Header: h}
	pos := int64(headerSize)
	for i := int32(0); i < h.NumFrames; i++ {
		fr, np, err := decodeFrame(raw, pos)
		if err != nil {
			return nil, fmt.Errorf("spr: frame %d: %w", i, err)
		}
		sp.Frames = append(sp.Frames, fr)
		pos = np
	}
	return sp, nil
}

// decodeFrame consumes one per-frame record starting at pos and
// returns the parsed Frame + the next byte position to continue from.
func decodeFrame(raw []byte, pos int64) (Frame, int64, error) {
	if pos+frameTypeTagSize > int64(len(raw)) {
		return Frame{}, 0, ErrSectionOutOfRange
	}
	typ := int32(binary.LittleEndian.Uint32(raw[pos : pos+4]))
	pos += frameTypeTagSize
	switch typ {
	case FrameSingle:
		sf, np, err := decodeSingleFrame(raw, pos)
		if err != nil {
			return Frame{}, 0, err
		}
		return Frame{Type: FrameSingle, Single: sf}, np, nil
	case FrameGroup:
		gf, np, err := decodeGroupFrame(raw, pos)
		if err != nil {
			return Frame{}, 0, err
		}
		return Frame{Type: FrameGroup, Group: gf}, np, nil
	default:
		return Frame{}, 0, fmt.Errorf("%w: %d", ErrBadFrameType, typ)
	}
}

func decodeSingleFrame(raw []byte, pos int64) (SingleFrame, int64, error) {
	if pos+frameHeaderSize > int64(len(raw)) {
		return SingleFrame{}, 0, ErrSectionOutOfRange
	}
	sf := SingleFrame{FrameHeader: FrameHeader{
		OriginX: int32(binary.LittleEndian.Uint32(raw[pos : pos+4])),
		OriginY: int32(binary.LittleEndian.Uint32(raw[pos+4 : pos+8])),
		Width:   int32(binary.LittleEndian.Uint32(raw[pos+8 : pos+12])),
		Height:  int32(binary.LittleEndian.Uint32(raw[pos+12 : pos+16])),
	}}
	pos += frameHeaderSize
	if sf.Width < 0 || sf.Height < 0 {
		return SingleFrame{}, 0, ErrSectionOutOfRange
	}
	bmpSize := int64(sf.Width) * int64(sf.Height)
	if pos+bmpSize > int64(len(raw)) {
		return SingleFrame{}, 0, ErrSectionOutOfRange
	}
	sf.Bitmap = append([]byte(nil), raw[pos:pos+bmpSize]...)
	return sf, pos + bmpSize, nil
}

func decodeGroupFrame(raw []byte, pos int64) (*GroupFrame, int64, error) {
	if pos+groupHeaderSize > int64(len(raw)) {
		return nil, 0, ErrSectionOutOfRange
	}
	num := int32(binary.LittleEndian.Uint32(raw[pos : pos+4]))
	pos += groupHeaderSize
	if num < 0 {
		return nil, 0, ErrSectionOutOfRange
	}
	intervalsBytes := int64(num) * int64(groupIntervalSize)
	if pos+intervalsBytes > int64(len(raw)) {
		return nil, 0, ErrSectionOutOfRange
	}
	gf := &GroupFrame{
		Intervals: make([]float32, num),
		Frames:    make([]SingleFrame, 0, num),
	}
	for i := int32(0); i < num; i++ {
		off := pos + int64(i)*int64(groupIntervalSize)
		gf.Intervals[i] = math.Float32frombits(binary.LittleEndian.Uint32(raw[off : off+4]))
	}
	pos += intervalsBytes
	for i := int32(0); i < num; i++ {
		sf, np, err := decodeSingleFrame(raw, pos)
		if err != nil {
			return nil, 0, err
		}
		gf.Frames = append(gf.Frames, sf)
		pos = np
	}
	return gf, pos, nil
}
