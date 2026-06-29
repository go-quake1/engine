// Copyright (c) 1996-1997 Id Software, Inc.
// Copyright (c) 2026 the go-quake1/engine authors.
// SPDX-License-Identifier: GPL-2.0-or-later

package mdl

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
)

// On-wire constants from tyrquake/include/modelgen.h.
const (
	// IDPO little-endian magic.
	IDPolyHeader = uint32('I') | uint32('D')<<8 | uint32('P')<<16 | uint32('O')<<24
	Version      = 6

	headerSize         = 84
	frameTagSize       = 4
	skinTagSize        = 4
	groupCountSize     = 4
	intervalSize       = 4
	triVertxSize       = 4  // byte v[3] + lightnormalindex
	stVertSize         = 12 // 3 int32: onseam, s, t
	triangleSize       = 16 // 4 int32: facesfront + vertindex[3]
	frameNameLen       = 16
	singleFrameHdrSize = triVertxSize + triVertxSize + frameNameLen // bbox + name = 24
)

// Skin record tag (int32).
const (
	SkinSingle = 0
	SkinGroup  = 1
)

// Frame record tag (int32).
const (
	FrameSingle = 0
	FrameGroup  = 1
)

// Sync type (matches spr.Sync* and modelgen.h's synctype_t).
const (
	SyncSync = 0
	SyncRand = 1
)

// Sentinel errors.
var (
	ErrBadMagic          = errors.New("mdl: not an alias-model file (bad IDPO magic)")
	ErrBadVersion        = errors.New("mdl: unsupported version (need 6)")
	ErrShortRead         = errors.New("mdl: short read")
	ErrSectionOutOfRange = errors.New("mdl: section extends past EOF")
	ErrBadSkinType       = errors.New("mdl: unknown skin-type tag")
	ErrBadFrameType      = errors.New("mdl: unknown frame-type tag")
	ErrInvalidCounts     = errors.New("mdl: negative count in header")
)

// Header mirrors mdl_t.
type Header struct {
	Ident          uint32
	Version        int32
	Scale          [3]float32
	ScaleOrigin    [3]float32
	BoundingRadius float32
	EyePosition    [3]float32
	NumSkins       int32
	SkinWidth      int32
	SkinHeight     int32
	NumVerts       int32
	NumTris        int32
	NumFrames      int32
	SyncType       int32
	Flags          int32
	Size           float32
}

// STVert is one stvert_t: a vertex's (s,t) texture coordinate and a
// seam-membership tag. tyrquake: stvert_t.
type STVert struct {
	OnSeam int32 // ALIAS_ONSEAM (0x20) or 0
	S, T   int32
}

// Triangle is one dtriangle_t: a 3-vertex face with a tag for whether
// it faces front (skin uses left half) or back (uses right half).
type Triangle struct {
	FacesFront int32
	VertIndex  [3]int32
}

// TriVertx is one trivertx_t: a byte-packed per-frame vertex
// position + the index into the 162-entry anorms light-normal table
// the renderer dot-products against the world light dir.
type TriVertx struct {
	V                [3]byte
	LightNormalIndex byte
}

// SingleSkin is one (skinwidth * skinheight) bytes of 8-bit indexed
// skin texture.
type SingleSkin struct {
	Pixels []byte
}

// GroupSkin is an animated skin: N skins with per-frame timing.
type GroupSkin struct {
	Intervals []float32    // monotonically increasing
	Skins     []SingleSkin // len(Skins) == len(Intervals)
}

// Skin is one decoded skin record (tagged union).
type Skin struct {
	Type   int32      // SkinSingle or SkinGroup
	Single SingleSkin // valid when Type == SkinSingle
	Group  *GroupSkin // non-nil when Type == SkinGroup
}

// SingleFrame is one daliasframe_t.
type SingleFrame struct {
	BBoxMin TriVertx
	BBoxMax TriVertx
	Name    string     // 16-byte slot, NUL-trimmed
	Verts   []TriVertx // len == header.NumVerts
}

// GroupFrame is an animated frame: N frames with per-frame timing
// and a group-level bbox.
type GroupFrame struct {
	BBoxMin   TriVertx
	BBoxMax   TriVertx
	Intervals []float32
	Frames    []SingleFrame
}

// Frame is one decoded frame record (tagged union).
type Frame struct {
	Type   int32       // FrameSingle or FrameGroup
	Single SingleFrame // valid when Type == FrameSingle
	Group  *GroupFrame // non-nil when Type == FrameGroup
}

// PoseAt returns the SingleFrame the renderer should sample at the
// given client time. tyrquake equivalent: R_AliasSetupFrame's
// "if (frame->type == ALIAS_GROUP)" branch in r_alias.c which picks
// the sub-frame whose cumulative interval > (time mod total).
//
// For FrameSingle Type the time argument is ignored.
//
// For FrameGroup Type with non-empty Frames + matching Intervals,
// time is reduced modulo the group's total duration and the first
// sub-frame whose Intervals[i] > (time mod total) is returned. The
// progs/flame.mdl + progs/torch.mdl shipped with Quake are
// single-element top-level FrameGroups whose sub-frames cycle the
// torch flame at ~10 Hz; without this method (the renderer
// previously pinned sub-frame 0 unconditionally) the flame appears
// frozen even though the model carries the animation data.
//
// Malformed groups (Frames empty, fewer Intervals than Frames, or
// non-positive total) fall back to Frames[0] so the renderer always
// has a defined pose.
func (f Frame) PoseAt(time float32) SingleFrame {
	if f.Type != FrameGroup {
		return f.Single
	}
	if f.Group == nil || len(f.Group.Frames) == 0 {
		return SingleFrame{}
	}
	if len(f.Group.Intervals) == 0 {
		return f.Group.Frames[0]
	}
	total := f.Group.Intervals[len(f.Group.Intervals)-1]
	if total <= 0 {
		return f.Group.Frames[0]
	}
	t := time
	if t < 0 {
		t = 0
	}
	// time mod total (positive-modulo).
	t -= total * float32(int(t/total))
	for i, end := range f.Group.Intervals {
		if t < end && i < len(f.Group.Frames) {
			return f.Group.Frames[i]
		}
	}
	// Floating-point edge: t == total. Last sub-frame.
	return f.Group.Frames[len(f.Group.Frames)-1]
}

// Model is a fully-decoded .mdl file.
type Model struct {
	Header    Header
	Skins     []Skin
	STVerts   []STVert
	Triangles []Triangle
	Frames    []Frame
}

// Load reads src fully and decodes the model. tyrquake:
// Mod_LoadAliasModel.
func Load(src io.ReaderAt, size int64) (*Model, error) {
	if src == nil {
		return nil, errors.New("mdl: nil src")
	}
	if size < headerSize {
		return nil, ErrShortRead
	}
	raw := make([]byte, size)
	n, err := src.ReadAt(raw, 0)
	if err != nil && err != io.EOF {
		return nil, fmt.Errorf("mdl: read: %w", err)
	}
	if int64(n) < size {
		return nil, ErrShortRead
	}

	h, err := decodeHeader(raw)
	if err != nil {
		return nil, err
	}
	if h.NumSkins < 0 || h.NumVerts < 0 || h.NumTris < 0 || h.NumFrames < 0 ||
		h.SkinWidth < 0 || h.SkinHeight < 0 {
		return nil, ErrInvalidCounts
	}

	m := &Model{Header: h}
	pos := int64(headerSize)
	skinPixels := int64(h.SkinWidth) * int64(h.SkinHeight)

	for i := int32(0); i < h.NumSkins; i++ {
		sk, np, err := decodeSkin(raw, pos, skinPixels)
		if err != nil {
			return nil, fmt.Errorf("mdl: skin %d: %w", i, err)
		}
		m.Skins = append(m.Skins, sk)
		pos = np
	}

	stRaw, np, err := sliceN(raw, pos, int64(h.NumVerts), stVertSize)
	if err != nil {
		return nil, fmt.Errorf("mdl: stverts: %w", err)
	}
	m.STVerts = decodeSTVerts(stRaw, h.NumVerts)
	pos = np

	triRaw, np, err := sliceN(raw, pos, int64(h.NumTris), triangleSize)
	if err != nil {
		return nil, fmt.Errorf("mdl: triangles: %w", err)
	}
	m.Triangles = decodeTriangles(triRaw, h.NumTris)
	pos = np

	for i := int32(0); i < h.NumFrames; i++ {
		fr, np, err := decodeFrame(raw, pos, h.NumVerts)
		if err != nil {
			return nil, fmt.Errorf("mdl: frame %d: %w", i, err)
		}
		m.Frames = append(m.Frames, fr)
		pos = np
	}
	return m, nil
}

func decodeHeader(raw []byte) (Header, error) {
	readVec3 := func(at int) [3]float32 {
		return [3]float32{
			math.Float32frombits(binary.LittleEndian.Uint32(raw[at : at+4])),
			math.Float32frombits(binary.LittleEndian.Uint32(raw[at+4 : at+8])),
			math.Float32frombits(binary.LittleEndian.Uint32(raw[at+8 : at+12])),
		}
	}
	h := Header{
		Ident:          binary.LittleEndian.Uint32(raw[0:4]),
		Version:        int32(binary.LittleEndian.Uint32(raw[4:8])),
		Scale:          readVec3(8),
		ScaleOrigin:    readVec3(20),
		BoundingRadius: math.Float32frombits(binary.LittleEndian.Uint32(raw[32:36])),
		EyePosition:    readVec3(36),
		NumSkins:       int32(binary.LittleEndian.Uint32(raw[48:52])),
		SkinWidth:      int32(binary.LittleEndian.Uint32(raw[52:56])),
		SkinHeight:     int32(binary.LittleEndian.Uint32(raw[56:60])),
		NumVerts:       int32(binary.LittleEndian.Uint32(raw[60:64])),
		NumTris:        int32(binary.LittleEndian.Uint32(raw[64:68])),
		NumFrames:      int32(binary.LittleEndian.Uint32(raw[68:72])),
		SyncType:       int32(binary.LittleEndian.Uint32(raw[72:76])),
		Flags:          int32(binary.LittleEndian.Uint32(raw[76:80])),
		Size:           math.Float32frombits(binary.LittleEndian.Uint32(raw[80:84])),
	}
	if h.Ident != IDPolyHeader {
		return Header{}, ErrBadMagic
	}
	if h.Version != Version {
		return Header{}, fmt.Errorf("%w: got %d", ErrBadVersion, h.Version)
	}
	return h, nil
}

// --- skin decoder -----------------------------------------------------------

func decodeSkin(raw []byte, pos, pixels int64) (Skin, int64, error) {
	if pos+skinTagSize > int64(len(raw)) {
		return Skin{}, 0, ErrSectionOutOfRange
	}
	typ := int32(binary.LittleEndian.Uint32(raw[pos : pos+4]))
	pos += skinTagSize
	switch typ {
	case SkinSingle:
		s, np, err := decodeSingleSkin(raw, pos, pixels)
		if err != nil {
			return Skin{}, 0, err
		}
		return Skin{Type: SkinSingle, Single: s}, np, nil
	case SkinGroup:
		g, np, err := decodeGroupSkin(raw, pos, pixels)
		if err != nil {
			return Skin{}, 0, err
		}
		return Skin{Type: SkinGroup, Group: g}, np, nil
	default:
		return Skin{}, 0, fmt.Errorf("%w: %d", ErrBadSkinType, typ)
	}
}

func decodeSingleSkin(raw []byte, pos, pixels int64) (SingleSkin, int64, error) {
	if pixels < 0 || pos+pixels > int64(len(raw)) {
		return SingleSkin{}, 0, ErrSectionOutOfRange
	}
	out := SingleSkin{Pixels: append([]byte(nil), raw[pos:pos+pixels]...)}
	return out, pos + pixels, nil
}

func decodeGroupSkin(raw []byte, pos, pixels int64) (*GroupSkin, int64, error) {
	if pos+groupCountSize > int64(len(raw)) {
		return nil, 0, ErrSectionOutOfRange
	}
	num := int32(binary.LittleEndian.Uint32(raw[pos : pos+4]))
	pos += groupCountSize
	if num < 0 {
		return nil, 0, ErrSectionOutOfRange
	}
	g := &GroupSkin{Intervals: make([]float32, num), Skins: make([]SingleSkin, 0, num)}
	intervalsBytes := int64(num) * int64(intervalSize)
	if pos+intervalsBytes > int64(len(raw)) {
		return nil, 0, ErrSectionOutOfRange
	}
	for i := int32(0); i < num; i++ {
		off := pos + int64(i)*int64(intervalSize)
		g.Intervals[i] = math.Float32frombits(binary.LittleEndian.Uint32(raw[off : off+4]))
	}
	pos += intervalsBytes
	for i := int32(0); i < num; i++ {
		s, np, err := decodeSingleSkin(raw, pos, pixels)
		if err != nil {
			return nil, 0, err
		}
		g.Skins = append(g.Skins, s)
		pos = np
	}
	return g, pos, nil
}

// --- frame decoder ----------------------------------------------------------

func decodeFrame(raw []byte, pos int64, numVerts int32) (Frame, int64, error) {
	if pos+frameTagSize > int64(len(raw)) {
		return Frame{}, 0, ErrSectionOutOfRange
	}
	typ := int32(binary.LittleEndian.Uint32(raw[pos : pos+4]))
	pos += frameTagSize
	switch typ {
	case FrameSingle:
		f, np, err := decodeSingleFrame(raw, pos, numVerts)
		if err != nil {
			return Frame{}, 0, err
		}
		return Frame{Type: FrameSingle, Single: f}, np, nil
	case FrameGroup:
		g, np, err := decodeGroupFrame(raw, pos, numVerts)
		if err != nil {
			return Frame{}, 0, err
		}
		return Frame{Type: FrameGroup, Group: g}, np, nil
	default:
		return Frame{}, 0, fmt.Errorf("%w: %d", ErrBadFrameType, typ)
	}
}

func decodeSingleFrame(raw []byte, pos int64, numVerts int32) (SingleFrame, int64, error) {
	vertsBytes := int64(numVerts) * int64(triVertxSize)
	need := int64(singleFrameHdrSize) + vertsBytes
	if numVerts < 0 || pos+need > int64(len(raw)) {
		return SingleFrame{}, 0, ErrSectionOutOfRange
	}
	sf := SingleFrame{
		BBoxMin: readTriVertx(raw[pos : pos+triVertxSize]),
		BBoxMax: readTriVertx(raw[pos+triVertxSize : pos+2*triVertxSize]),
	}
	nameStart := int(pos) + 2*triVertxSize
	sf.Name = trimNul(string(raw[nameStart : nameStart+frameNameLen]))
	pos += int64(singleFrameHdrSize)
	sf.Verts = make([]TriVertx, numVerts)
	for i := int32(0); i < numVerts; i++ {
		off := pos + int64(i)*int64(triVertxSize)
		sf.Verts[i] = readTriVertx(raw[off : off+triVertxSize])
	}
	return sf, pos + vertsBytes, nil
}

func decodeGroupFrame(raw []byte, pos int64, numVerts int32) (*GroupFrame, int64, error) {
	// numframes (int32) + bboxmin (4) + bboxmax (4) all live before
	// the intervals table.
	const groupHeaderOverhead = groupCountSize + triVertxSize + triVertxSize
	if pos+int64(groupHeaderOverhead) > int64(len(raw)) {
		return nil, 0, ErrSectionOutOfRange
	}
	num := int32(binary.LittleEndian.Uint32(raw[pos : pos+4]))
	pos += groupCountSize
	if num < 0 {
		return nil, 0, ErrSectionOutOfRange
	}
	g := &GroupFrame{
		BBoxMin:   readTriVertx(raw[pos : pos+triVertxSize]),
		BBoxMax:   readTriVertx(raw[pos+triVertxSize : pos+2*triVertxSize]),
		Intervals: make([]float32, num),
		Frames:    make([]SingleFrame, 0, num),
	}
	pos += 2 * int64(triVertxSize)
	intervalsBytes := int64(num) * int64(intervalSize)
	if pos+intervalsBytes > int64(len(raw)) {
		return nil, 0, ErrSectionOutOfRange
	}
	for i := int32(0); i < num; i++ {
		off := pos + int64(i)*int64(intervalSize)
		g.Intervals[i] = math.Float32frombits(binary.LittleEndian.Uint32(raw[off : off+4]))
	}
	pos += intervalsBytes
	for i := int32(0); i < num; i++ {
		sf, np, err := decodeSingleFrame(raw, pos, numVerts)
		if err != nil {
			return nil, 0, err
		}
		g.Frames = append(g.Frames, sf)
		pos = np
	}
	return g, pos, nil
}

// --- fixed-size table decoders ----------------------------------------------

func decodeSTVerts(raw []byte, n int32) []STVert {
	out := make([]STVert, n)
	for i := int32(0); i < n; i++ {
		off := int(i) * stVertSize
		out[i].OnSeam = int32(binary.LittleEndian.Uint32(raw[off : off+4]))
		out[i].S = int32(binary.LittleEndian.Uint32(raw[off+4 : off+8]))
		out[i].T = int32(binary.LittleEndian.Uint32(raw[off+8 : off+12]))
	}
	return out
}

func decodeTriangles(raw []byte, n int32) []Triangle {
	out := make([]Triangle, n)
	for i := int32(0); i < n; i++ {
		off := int(i) * triangleSize
		out[i].FacesFront = int32(binary.LittleEndian.Uint32(raw[off : off+4]))
		out[i].VertIndex[0] = int32(binary.LittleEndian.Uint32(raw[off+4 : off+8]))
		out[i].VertIndex[1] = int32(binary.LittleEndian.Uint32(raw[off+8 : off+12]))
		out[i].VertIndex[2] = int32(binary.LittleEndian.Uint32(raw[off+12 : off+16]))
	}
	return out
}

// --- helpers ----------------------------------------------------------------

func readTriVertx(b []byte) TriVertx {
	return TriVertx{V: [3]byte{b[0], b[1], b[2]}, LightNormalIndex: b[3]}
}

// sliceN returns raw[pos : pos+n*unit], the new position past the
// section, or an error when the section runs off EOF. Negative counts
// fail with ErrSectionOutOfRange.
func sliceN(raw []byte, pos, n, unit int64) ([]byte, int64, error) {
	if n < 0 {
		return nil, 0, ErrSectionOutOfRange
	}
	end := pos + n*unit
	if pos < 0 || end < pos || end > int64(len(raw)) {
		return nil, 0, ErrSectionOutOfRange
	}
	return raw[pos:end], end, nil
}

func trimNul(s string) string {
	for i := 0; i < len(s); i++ {
		if s[i] == 0 {
			return s[:i]
		}
	}
	return s
}
