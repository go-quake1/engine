// Copyright (c) 2026 the go-quake1/engine authors.
// SPDX-License-Identifier: GPL-2.0-or-later

package mdl

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"math"
	"testing"
)

func putU32(b *bytes.Buffer, v uint32)  { _ = binary.Write(b, binary.LittleEndian, v) }
func putI32(b *bytes.Buffer, v int32)   { _ = binary.Write(b, binary.LittleEndian, v) }
func putF32(b *bytes.Buffer, v float32) { _ = binary.Write(b, binary.LittleEndian, v) }

type singleSkinSpec struct{ pixels []byte }
type groupSkinSpec struct {
	intervals []float32
	skins     []singleSkinSpec
}
type skinSpec struct {
	kind   int32
	single singleSkinSpec
	group  groupSkinSpec
}

type singleFrameSpec struct {
	bboxMin TriVertx
	bboxMax TriVertx
	name    string // up to 16 bytes
	verts   []TriVertx
}
type groupFrameSpec struct {
	bboxMin   TriVertx
	bboxMax   TriVertx
	intervals []float32
	frames    []singleFrameSpec
}
type frameSpec struct {
	kind   int32
	single singleFrameSpec
	group  groupFrameSpec
}

type buildSpec struct {
	ident      uint32 // 0 -> IDPolyHeader
	version    int32  // 0 -> Version
	skinWidth  int32
	skinHeight int32
	numVerts   int32
	numTris    int32
	skins      []skinSpec
	stverts    []STVert
	triangles  []Triangle
	frames     []frameSpec
}

func encodeSingleSkin(b *bytes.Buffer, s singleSkinSpec) { b.Write(s.pixels) }

func encodeTriVertx(b *bytes.Buffer, v TriVertx) {
	b.WriteByte(v.V[0])
	b.WriteByte(v.V[1])
	b.WriteByte(v.V[2])
	b.WriteByte(v.LightNormalIndex)
}

func encodeSingleFrame(b *bytes.Buffer, f singleFrameSpec) {
	encodeTriVertx(b, f.bboxMin)
	encodeTriVertx(b, f.bboxMax)
	name := make([]byte, frameNameLen)
	copy(name, f.name)
	b.Write(name)
	for _, v := range f.verts {
		encodeTriVertx(b, v)
	}
}

func build(s buildSpec) ([]byte, int64) {
	ident := s.ident
	if ident == 0 {
		ident = IDPolyHeader
	}
	ver := s.version
	if ver == 0 {
		ver = Version
	}
	buf := &bytes.Buffer{}
	// Header (84 bytes).
	putU32(buf, ident)
	putI32(buf, ver)
	// scale
	putF32(buf, 1)
	putF32(buf, 1)
	putF32(buf, 1)
	// scale_origin
	putF32(buf, 0)
	putF32(buf, 0)
	putF32(buf, 0)
	putF32(buf, 16) // boundingradius
	// eyeposition
	putF32(buf, 0)
	putF32(buf, 0)
	putF32(buf, 32)
	putI32(buf, int32(len(s.skins)))
	putI32(buf, s.skinWidth)
	putI32(buf, s.skinHeight)
	putI32(buf, s.numVerts)
	putI32(buf, s.numTris)
	putI32(buf, int32(len(s.frames)))
	putI32(buf, SyncSync)
	putI32(buf, 0)   // flags
	putF32(buf, 1.0) // size

	// Skins.
	for _, sk := range s.skins {
		putI32(buf, sk.kind)
		switch sk.kind {
		case SkinSingle:
			encodeSingleSkin(buf, sk.single)
		case SkinGroup:
			putI32(buf, int32(len(sk.group.intervals)))
			for _, iv := range sk.group.intervals {
				putF32(buf, iv)
			}
			for _, ss := range sk.group.skins {
				encodeSingleSkin(buf, ss)
			}
		}
	}

	// STVerts.
	for _, st := range s.stverts {
		putI32(buf, st.OnSeam)
		putI32(buf, st.S)
		putI32(buf, st.T)
	}

	// Triangles.
	for _, tr := range s.triangles {
		putI32(buf, tr.FacesFront)
		putI32(buf, tr.VertIndex[0])
		putI32(buf, tr.VertIndex[1])
		putI32(buf, tr.VertIndex[2])
	}

	// Frames.
	for _, fr := range s.frames {
		putI32(buf, fr.kind)
		switch fr.kind {
		case FrameSingle:
			encodeSingleFrame(buf, fr.single)
		case FrameGroup:
			putI32(buf, int32(len(fr.group.frames)))
			encodeTriVertx(buf, fr.group.bboxMin)
			encodeTriVertx(buf, fr.group.bboxMax)
			for _, iv := range fr.group.intervals {
				putF32(buf, iv)
			}
			for _, sf := range fr.group.frames {
				encodeSingleFrame(buf, sf)
			}
		}
	}

	out := buf.Bytes()
	return out, int64(len(out))
}

// --- happy paths ----------------------------------------------------------

func TestLoad_MinimalSingleSkinSingleFrame(t *testing.T) {
	raw, sz := build(buildSpec{
		skinWidth: 4, skinHeight: 4,
		numVerts: 2, numTris: 1,
		skins: []skinSpec{{kind: SkinSingle, single: singleSkinSpec{
			pixels: bytes.Repeat([]byte{7}, 4*4),
		}}},
		stverts: []STVert{
			{OnSeam: 0, S: 0, T: 0},
			{OnSeam: 0x20, S: 64, T: 32},
		},
		triangles: []Triangle{
			{FacesFront: 1, VertIndex: [3]int32{0, 1, 0}},
		},
		frames: []frameSpec{{kind: FrameSingle, single: singleFrameSpec{
			bboxMin: TriVertx{V: [3]byte{0, 0, 0}, LightNormalIndex: 0},
			bboxMax: TriVertx{V: [3]byte{8, 8, 8}, LightNormalIndex: 0},
			name:    "frame0",
			verts: []TriVertx{
				{V: [3]byte{1, 2, 3}, LightNormalIndex: 4},
				{V: [3]byte{5, 6, 7}, LightNormalIndex: 8},
			},
		}}},
	})
	m, err := Load(bytes.NewReader(raw), sz)
	if err != nil {
		t.Fatal(err)
	}
	if m.Header.NumSkins != 1 || len(m.Skins) != 1 {
		t.Errorf("skins: %d", len(m.Skins))
	}
	if got := m.Skins[0].Single.Pixels; len(got) != 16 || got[0] != 7 {
		t.Errorf("skin payload: %v", got)
	}
	if len(m.STVerts) != 2 || m.STVerts[1].S != 64 {
		t.Errorf("stverts: %v", m.STVerts)
	}
	if len(m.Triangles) != 1 || m.Triangles[0].VertIndex[1] != 1 {
		t.Errorf("triangles: %v", m.Triangles)
	}
	if len(m.Frames) != 1 || m.Frames[0].Single.Name != "frame0" {
		t.Errorf("frame: %+v", m.Frames[0].Single)
	}
	if len(m.Frames[0].Single.Verts) != 2 || m.Frames[0].Single.Verts[1].V[2] != 7 {
		t.Errorf("frame verts: %+v", m.Frames[0].Single.Verts)
	}
}

func TestLoad_GroupSkinAndGroupFrame(t *testing.T) {
	raw, sz := build(buildSpec{
		skinWidth: 2, skinHeight: 2,
		numVerts: 1, numTris: 0,
		skins: []skinSpec{{kind: SkinGroup, group: groupSkinSpec{
			intervals: []float32{0.1, 0.2},
			skins: []singleSkinSpec{
				{pixels: bytes.Repeat([]byte{1}, 4)},
				{pixels: bytes.Repeat([]byte{2}, 4)},
			},
		}}},
		stverts: []STVert{{OnSeam: 0, S: 0, T: 0}},
		frames: []frameSpec{{kind: FrameGroup, group: groupFrameSpec{
			bboxMin:   TriVertx{V: [3]byte{1, 2, 3}},
			bboxMax:   TriVertx{V: [3]byte{4, 5, 6}},
			intervals: []float32{0.5, 1.0},
			frames: []singleFrameSpec{
				{name: "f0", verts: []TriVertx{{V: [3]byte{0, 0, 0}}}},
				{name: "f1", verts: []TriVertx{{V: [3]byte{9, 9, 9}}}},
			},
		}}},
	})
	m, err := Load(bytes.NewReader(raw), sz)
	if err != nil {
		t.Fatal(err)
	}
	gs := m.Skins[0].Group
	if gs == nil || len(gs.Intervals) != 2 || gs.Intervals[1] != 0.2 {
		t.Errorf("group skin intervals: %v", gs)
	}
	if len(gs.Skins) != 2 || gs.Skins[1].Pixels[0] != 2 {
		t.Errorf("group skin frames: %v", gs)
	}
	gf := m.Frames[0].Group
	if gf == nil || len(gf.Frames) != 2 || gf.Frames[1].Verts[0].V[2] != 9 {
		t.Errorf("group frame: %+v", gf)
	}
	if gf.BBoxMin.V[0] != 1 || gf.BBoxMax.V[2] != 6 {
		t.Errorf("group bbox: %+v", gf)
	}
}

// --- error paths ----------------------------------------------------------

func TestLoad_NilSrc(t *testing.T) {
	if _, err := Load(nil, 100); err == nil {
		t.Error("expected nil-src error")
	}
}

func TestLoad_SizeTooSmall(t *testing.T) {
	if _, err := Load(bytes.NewReader([]byte{1, 2}), 2); !errors.Is(err, ErrShortRead) {
		t.Errorf("got %v want ErrShortRead", err)
	}
}

type errReader struct{}

func (errReader) ReadAt([]byte, int64) (int, error) { return 0, errors.New("io fail") }

func TestLoad_ReadFails(t *testing.T) {
	if _, err := Load(errReader{}, 200); err == nil {
		t.Error("expected io error")
	}
}

type shortReader struct{}

func (shortReader) ReadAt(p []byte, _ int64) (int, error) { return len(p) / 2, io.EOF }

func TestLoad_ShortRead(t *testing.T) {
	if _, err := Load(shortReader{}, 200); !errors.Is(err, ErrShortRead) {
		t.Errorf("got %v want ErrShortRead", err)
	}
}

func TestLoad_BadMagic(t *testing.T) {
	raw, sz := build(buildSpec{ident: 0xDEADBEEF})
	if _, err := Load(bytes.NewReader(raw), sz); !errors.Is(err, ErrBadMagic) {
		t.Errorf("got %v want ErrBadMagic", err)
	}
}

func TestLoad_BadVersion(t *testing.T) {
	raw, sz := build(buildSpec{version: 99})
	if _, err := Load(bytes.NewReader(raw), sz); !errors.Is(err, ErrBadVersion) {
		t.Errorf("got %v want ErrBadVersion", err)
	}
}

func TestLoad_NegativeNumSkinsInHeader(t *testing.T) {
	raw, sz := build(buildSpec{})
	// Patch NumSkins to -1.
	binary.LittleEndian.PutUint32(raw[48:52], 0xFFFFFFFF)
	if _, err := Load(bytes.NewReader(raw), sz); !errors.Is(err, ErrInvalidCounts) {
		t.Errorf("got %v want ErrInvalidCounts", err)
	}
}

func TestLoad_UnknownSkinType(t *testing.T) {
	raw, sz := build(buildSpec{
		skinWidth: 1, skinHeight: 1,
		skins: []skinSpec{{kind: 42}},
	})
	if _, err := Load(bytes.NewReader(raw), sz); !errors.Is(err, ErrBadSkinType) {
		t.Errorf("got %v want ErrBadSkinType", err)
	}
}

func TestLoad_UnknownFrameType(t *testing.T) {
	raw, sz := build(buildSpec{
		frames: []frameSpec{{kind: 42}},
	})
	if _, err := Load(bytes.NewReader(raw), sz); !errors.Is(err, ErrBadFrameType) {
		t.Errorf("got %v want ErrBadFrameType", err)
	}
}

// --- truncation in each section ------------------------------------------

func TestLoad_TruncatedSkinTag(t *testing.T) {
	raw, sz := build(buildSpec{})
	// Patch NumSkins=1 with no skin payload following.
	binary.LittleEndian.PutUint32(raw[48:52], 1)
	if _, err := Load(bytes.NewReader(raw), sz); !errors.Is(err, ErrSectionOutOfRange) {
		t.Errorf("got %v want ErrSectionOutOfRange", err)
	}
}

func TestLoad_TruncatedSingleSkinPixels(t *testing.T) {
	// SkinSingle tag is present but the file ends before the full
	// pixel block.
	buf := &bytes.Buffer{}
	putU32(buf, IDPolyHeader)
	putI32(buf, Version)
	for i := 0; i < (84-8)/4; i++ {
		putI32(buf, 0)
	}
	// Patch NumSkins=1, SkinWidth=4, SkinHeight=4.
	out := buf.Bytes()
	binary.LittleEndian.PutUint32(out[48:52], 1)
	binary.LittleEndian.PutUint32(out[52:56], 4)
	binary.LittleEndian.PutUint32(out[56:60], 4)
	out = append(out, 0, 0, 0, 0) // SkinSingle tag = 0
	out = append(out, 1, 2, 3)    // only 3 pixel bytes instead of 16
	if _, err := Load(bytes.NewReader(out), int64(len(out))); !errors.Is(err, ErrSectionOutOfRange) {
		t.Errorf("got %v want ErrSectionOutOfRange", err)
	}
}

func TestLoad_TruncatedGroupSkinIntervals(t *testing.T) {
	buf := &bytes.Buffer{}
	putU32(buf, IDPolyHeader)
	putI32(buf, Version)
	for i := 0; i < (84-8)/4; i++ {
		putI32(buf, 0)
	}
	out := buf.Bytes()
	binary.LittleEndian.PutUint32(out[48:52], 1) // NumSkins=1
	binary.LittleEndian.PutUint32(out[52:56], 1) // SkinWidth
	binary.LittleEndian.PutUint32(out[56:60], 1) // SkinHeight
	tail := &bytes.Buffer{}
	putI32(tail, SkinGroup)
	putI32(tail, 3) // claims 3 intervals
	// no interval bytes
	out = append(out, tail.Bytes()...)
	if _, err := Load(bytes.NewReader(out), int64(len(out))); !errors.Is(err, ErrSectionOutOfRange) {
		t.Errorf("got %v want ErrSectionOutOfRange", err)
	}
}

func TestLoad_NegativeGroupSkinCount(t *testing.T) {
	buf := &bytes.Buffer{}
	putU32(buf, IDPolyHeader)
	putI32(buf, Version)
	for i := 0; i < (84-8)/4; i++ {
		putI32(buf, 0)
	}
	out := buf.Bytes()
	binary.LittleEndian.PutUint32(out[48:52], 1)
	tail := &bytes.Buffer{}
	putI32(tail, SkinGroup)
	putI32(tail, -1)
	out = append(out, tail.Bytes()...)
	if _, err := Load(bytes.NewReader(out), int64(len(out))); !errors.Is(err, ErrSectionOutOfRange) {
		t.Errorf("got %v want ErrSectionOutOfRange", err)
	}
}

func TestLoad_TruncatedSTVerts(t *testing.T) {
	raw, sz := build(buildSpec{})
	// Bump NumVerts to 1 without supplying any stvert payload.
	binary.LittleEndian.PutUint32(raw[60:64], 1)
	if _, err := Load(bytes.NewReader(raw), sz); !errors.Is(err, ErrSectionOutOfRange) {
		t.Errorf("got %v want ErrSectionOutOfRange", err)
	}
}

func TestLoad_TruncatedTriangles(t *testing.T) {
	raw, sz := build(buildSpec{})
	binary.LittleEndian.PutUint32(raw[64:68], 1) // NumTris=1, no payload
	if _, err := Load(bytes.NewReader(raw), sz); !errors.Is(err, ErrSectionOutOfRange) {
		t.Errorf("got %v want ErrSectionOutOfRange", err)
	}
}

func TestLoad_TruncatedFrameTag(t *testing.T) {
	raw, sz := build(buildSpec{})
	binary.LittleEndian.PutUint32(raw[68:72], 1) // NumFrames=1, no frame
	if _, err := Load(bytes.NewReader(raw), sz); !errors.Is(err, ErrSectionOutOfRange) {
		t.Errorf("got %v want ErrSectionOutOfRange", err)
	}
}

func TestLoad_TruncatedSingleFrame(t *testing.T) {
	// Header says NumVerts=3, but single-frame verts run off EOF.
	raw, _ := build(buildSpec{
		numVerts: 3,
		stverts:  []STVert{{}, {}, {}}, // satisfy stvert section
		frames: []frameSpec{{kind: FrameSingle, single: singleFrameSpec{
			name:  "x",
			verts: []TriVertx{{}, {}, {}}, // produce a valid frame at build time
		}}},
	})
	// Now patch the file size down by trimming 4 bytes of frame verts.
	trimmed := raw[:len(raw)-4]
	if _, err := Load(bytes.NewReader(trimmed), int64(len(trimmed))); !errors.Is(err, ErrSectionOutOfRange) {
		t.Errorf("got %v want ErrSectionOutOfRange", err)
	}
}

func TestLoad_TruncatedGroupFrameHeader(t *testing.T) {
	buf := &bytes.Buffer{}
	putU32(buf, IDPolyHeader)
	putI32(buf, Version)
	for i := 0; i < (84-8)/4; i++ {
		putI32(buf, 0)
	}
	out := buf.Bytes()
	binary.LittleEndian.PutUint32(out[68:72], 1) // NumFrames=1
	tail := &bytes.Buffer{}
	putI32(tail, FrameGroup)
	// no group header bytes
	out = append(out, tail.Bytes()...)
	if _, err := Load(bytes.NewReader(out), int64(len(out))); !errors.Is(err, ErrSectionOutOfRange) {
		t.Errorf("got %v want ErrSectionOutOfRange", err)
	}
}

func TestLoad_NegativeGroupFrameCount(t *testing.T) {
	buf := &bytes.Buffer{}
	putU32(buf, IDPolyHeader)
	putI32(buf, Version)
	for i := 0; i < (84-8)/4; i++ {
		putI32(buf, 0)
	}
	out := buf.Bytes()
	binary.LittleEndian.PutUint32(out[68:72], 1)
	tail := &bytes.Buffer{}
	putI32(tail, FrameGroup)
	putI32(tail, -1)
	encodeTriVertx(tail, TriVertx{})
	encodeTriVertx(tail, TriVertx{})
	out = append(out, tail.Bytes()...)
	if _, err := Load(bytes.NewReader(out), int64(len(out))); !errors.Is(err, ErrSectionOutOfRange) {
		t.Errorf("got %v want ErrSectionOutOfRange", err)
	}
}

func TestLoad_TruncatedGroupFrameIntervals(t *testing.T) {
	buf := &bytes.Buffer{}
	putU32(buf, IDPolyHeader)
	putI32(buf, Version)
	for i := 0; i < (84-8)/4; i++ {
		putI32(buf, 0)
	}
	out := buf.Bytes()
	binary.LittleEndian.PutUint32(out[68:72], 1)
	tail := &bytes.Buffer{}
	putI32(tail, FrameGroup)
	putI32(tail, 3) // 3 intervals expected
	encodeTriVertx(tail, TriVertx{})
	encodeTriVertx(tail, TriVertx{})
	// no intervals follow
	out = append(out, tail.Bytes()...)
	if _, err := Load(bytes.NewReader(out), int64(len(out))); !errors.Is(err, ErrSectionOutOfRange) {
		t.Errorf("got %v want ErrSectionOutOfRange", err)
	}
}

// Covers decodeGroupSkin's "no room for the count int32" guard --
// the file ends right after the SkinGroup tag.
func TestLoad_TruncatedGroupSkinCount(t *testing.T) {
	buf := &bytes.Buffer{}
	putU32(buf, IDPolyHeader)
	putI32(buf, Version)
	for i := 0; i < (84-8)/4; i++ {
		putI32(buf, 0)
	}
	out := buf.Bytes()
	binary.LittleEndian.PutUint32(out[48:52], 1) // NumSkins=1
	binary.LittleEndian.PutUint32(out[52:56], 1)
	binary.LittleEndian.PutUint32(out[56:60], 1)
	out = append(out, 1, 0, 0, 0) // SkinGroup tag only, no count
	if _, err := Load(bytes.NewReader(out), int64(len(out))); !errors.Is(err, ErrSectionOutOfRange) {
		t.Errorf("got %v want ErrSectionOutOfRange", err)
	}
}

// Group skin claims 2 sub-skins but the second one's pixel block is
// truncated. Covers the per-sub-skin error propagation in
// decodeGroupSkin's inner loop.
func TestLoad_TruncatedGroupSubSkin(t *testing.T) {
	buf := &bytes.Buffer{}
	putU32(buf, IDPolyHeader)
	putI32(buf, Version)
	for i := 0; i < (84-8)/4; i++ {
		putI32(buf, 0)
	}
	out := buf.Bytes()
	binary.LittleEndian.PutUint32(out[48:52], 1) // NumSkins=1
	binary.LittleEndian.PutUint32(out[52:56], 2) // SkinWidth=2
	binary.LittleEndian.PutUint32(out[56:60], 2) // SkinHeight=2 -> 4 px each

	tail := &bytes.Buffer{}
	putI32(tail, SkinGroup)
	putI32(tail, 2) // 2 sub-skins
	putF32(tail, 0.1)
	putF32(tail, 0.2)              // 2 intervals
	tail.Write([]byte{1, 1, 1, 1}) // first sub-skin OK (4 bytes)
	tail.Write([]byte{2, 2})       // second sub-skin truncated (2 of 4)
	out = append(out, tail.Bytes()...)
	if _, err := Load(bytes.NewReader(out), int64(len(out))); !errors.Is(err, ErrSectionOutOfRange) {
		t.Errorf("got %v want ErrSectionOutOfRange", err)
	}
}

// Group frame claims 2 sub-frames but the second is truncated mid-
// header. Covers decodeGroupFrame's inner-loop error propagation.
func TestLoad_TruncatedGroupSubFrame(t *testing.T) {
	buf := &bytes.Buffer{}
	putU32(buf, IDPolyHeader)
	putI32(buf, Version)
	for i := 0; i < (84-8)/4; i++ {
		putI32(buf, 0)
	}
	out := buf.Bytes()
	binary.LittleEndian.PutUint32(out[68:72], 1) // NumFrames=1
	// NumVerts stays 0 so each single frame is just 24 header bytes.

	tail := &bytes.Buffer{}
	putI32(tail, FrameGroup)
	putI32(tail, 2) // 2 sub-frames
	encodeTriVertx(tail, TriVertx{})
	encodeTriVertx(tail, TriVertx{})
	putF32(tail, 0.1)
	putF32(tail, 0.2) // 2 intervals
	// First sub-frame complete (24-byte header, 0 verts).
	encodeSingleFrame(tail, singleFrameSpec{name: "f0"})
	// Second sub-frame: write 10 bytes (less than the 24-byte header).
	tail.Write(make([]byte, 10))
	out = append(out, tail.Bytes()...)
	if _, err := Load(bytes.NewReader(out), int64(len(out))); !errors.Is(err, ErrSectionOutOfRange) {
		t.Errorf("got %v want ErrSectionOutOfRange", err)
	}
}

// --- helpers --------------------------------------------------------------

func TestTrimNul(t *testing.T) {
	if got := trimNul("hello\x00world"); got != "hello" {
		t.Errorf("got %q", got)
	}
	if got := trimNul("noNul"); got != "noNul" {
		t.Errorf("got %q", got)
	}
}

func TestSliceN_NegativeCount(t *testing.T) {
	if _, _, err := sliceN(make([]byte, 10), 0, -1, 4); !errors.Is(err, ErrSectionOutOfRange) {
		t.Errorf("got %v", err)
	}
}

func TestSliceN_OffsetPastEnd(t *testing.T) {
	if _, _, err := sliceN(make([]byte, 10), 20, 1, 4); !errors.Is(err, ErrSectionOutOfRange) {
		t.Errorf("got %v", err)
	}
}

func TestSliceN_OverflowEnd(t *testing.T) {
	// pos very large + n*unit very large to provoke overflow.
	if _, _, err := sliceN(make([]byte, 10), math.MaxInt64-2, 1, 4); !errors.Is(err, ErrSectionOutOfRange) {
		t.Errorf("got %v", err)
	}
}

// --- PoseAt ----------------------------------------------------------------

func TestPoseAt_SingleFrameIgnoresTime(t *testing.T) {
	single := SingleFrame{Name: "still", Verts: []TriVertx{{V: [3]byte{7, 0, 0}}}}
	f := Frame{Type: FrameSingle, Single: single}
	for _, tm := range []float32{0, 0.5, 99} {
		got := f.PoseAt(tm)
		if got.Name != "still" || len(got.Verts) != 1 || got.Verts[0].V[0] != 7 {
			t.Fatalf("PoseAt(%v) = %+v, want single 'still' verts[0].X=7", tm, got)
		}
	}
}

// Classic 3-frame group at 10 Hz: intervals = {0.1, 0.2, 0.3}. The
// sub-frame chosen depends on time mod 0.3.
func TestPoseAt_GroupCyclesAtConfiguredRate(t *testing.T) {
	g := &GroupFrame{
		Intervals: []float32{0.1, 0.2, 0.3},
		Frames: []SingleFrame{
			{Name: "f0", Verts: []TriVertx{{V: [3]byte{0, 0, 0}}}},
			{Name: "f1", Verts: []TriVertx{{V: [3]byte{1, 0, 0}}}},
			{Name: "f2", Verts: []TriVertx{{V: [3]byte{2, 0, 0}}}},
		},
	}
	f := Frame{Type: FrameGroup, Group: g}
	cases := []struct {
		time float32
		want string
	}{
		{0, "f0"},
		{0.05, "f0"},
		{0.1, "f1"},
		{0.15, "f1"},
		{0.25, "f2"},
		{0.3 + 0.05, "f0"}, // wraps modulo 0.3
		{0.3 + 0.15, "f1"},
		{0.3*3 + 0.25, "f2"},
	}
	for _, c := range cases {
		got := f.PoseAt(c.time)
		if got.Name != c.want {
			t.Errorf("PoseAt(%v) = %q, want %q", c.time, got.Name, c.want)
		}
	}
}

// Animation liveness test: at uniformly-sampled times across the
// cycle the helper must visit ALL sub-frames. A regression that
// hardcodes Frames[0] (the previous behaviour) would only visit one
// name and this test fires.
func TestPoseAt_VisitsAllSubFramesOverCycle(t *testing.T) {
	g := &GroupFrame{
		Intervals: []float32{0.1, 0.2, 0.3, 0.4, 0.5, 0.6},
		Frames: []SingleFrame{
			{Name: "a"}, {Name: "b"}, {Name: "c"},
			{Name: "d"}, {Name: "e"}, {Name: "f"},
		},
	}
	f := Frame{Type: FrameGroup, Group: g}
	seen := map[string]bool{}
	for i := 0; i < 60; i++ {
		tm := float32(i) * 0.01
		seen[f.PoseAt(tm).Name] = true
	}
	if len(seen) != len(g.Frames) {
		t.Fatalf("PoseAt cycle visited %d/%d sub-frames: %v (expected all six)",
			len(seen), len(g.Frames), seen)
	}
}

// Malformed group: empty Frames -> empty SingleFrame, no panic.
func TestPoseAt_EmptyGroup(t *testing.T) {
	f := Frame{Type: FrameGroup, Group: &GroupFrame{}}
	got := f.PoseAt(1.0)
	if got.Name != "" || got.Verts != nil {
		t.Fatalf("empty group: PoseAt = %+v, want zero SingleFrame", got)
	}
}

// Nil group: empty SingleFrame, no nil-deref.
func TestPoseAt_NilGroup(t *testing.T) {
	f := Frame{Type: FrameGroup, Group: nil}
	got := f.PoseAt(1.0)
	if got.Name != "" || got.Verts != nil {
		t.Fatalf("nil group: PoseAt = %+v, want zero SingleFrame", got)
	}
}

// Empty Intervals -> falls back to Frames[0].
func TestPoseAt_NoIntervalsFallback(t *testing.T) {
	g := &GroupFrame{
		Frames: []SingleFrame{{Name: "only"}},
	}
	f := Frame{Type: FrameGroup, Group: g}
	if got := f.PoseAt(99); got.Name != "only" {
		t.Fatalf("no-intervals fallback: got %q, want 'only'", got.Name)
	}
}

// Non-positive total interval -> fallback to Frames[0] (defensive
// against malformed mdl files).
func TestPoseAt_ZeroTotalFallback(t *testing.T) {
	g := &GroupFrame{
		Intervals: []float32{0, 0},
		Frames: []SingleFrame{
			{Name: "a"}, {Name: "b"},
		},
	}
	f := Frame{Type: FrameGroup, Group: g}
	if got := f.PoseAt(5); got.Name != "a" {
		t.Fatalf("zero-total fallback: got %q, want 'a'", got.Name)
	}
}

// Negative time clamps to 0 (first sub-frame).
func TestPoseAt_NegativeTimeClampsToZero(t *testing.T) {
	g := &GroupFrame{
		Intervals: []float32{0.1, 0.2},
		Frames:    []SingleFrame{{Name: "first"}, {Name: "second"}},
	}
	f := Frame{Type: FrameGroup, Group: g}
	if got := f.PoseAt(-3.5); got.Name != "first" {
		t.Fatalf("negative-time clamp: got %q, want 'first'", got.Name)
	}
}

// Floating-point edge: time exactly == total returns last sub-frame
// (not panics on the t<end loop exhausting).
func TestPoseAt_TimeExactlyTotalReturnsLast(t *testing.T) {
	g := &GroupFrame{
		Intervals: []float32{0.1, 0.3},
		Frames:    []SingleFrame{{Name: "first"}, {Name: "last"}},
	}
	f := Frame{Type: FrameGroup, Group: g}
	// 0.3 mod 0.3 == 0 with int truncation; but we also want the loop
	// to handle t-just-under-total correctly. Test the just-under case.
	if got := f.PoseAt(0.29999); got.Name != "last" {
		t.Fatalf("near-total: got %q, want 'last'", got.Name)
	}
}

// Malformed: Intervals has more entries than Frames. The loop's
// `i < len(Frames)` guard skips the extra intervals; if t exceeds the
// last in-range interval, the safety-net return fires.
func TestPoseAt_IntervalsLongerThanFrames(t *testing.T) {
	g := &GroupFrame{
		Intervals: []float32{0.1, 0.2, 0.3, 0.4},
		Frames:    []SingleFrame{{Name: "a"}, {Name: "b"}},
	}
	f := Frame{Type: FrameGroup, Group: g}
	// t=0.35 is past Intervals[1]=0.2 (the last with a matching
	// frame); Intervals[2]/[3] are skipped by the i<len(Frames)
	// guard; safety-net returns last Frames[1]="b".
	if got := f.PoseAt(0.35); got.Name != "b" {
		t.Fatalf("intervals>frames: got %q, want 'b'", got.Name)
	}
}
