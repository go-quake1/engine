// Copyright (c) 1996-1997 Id Software, Inc.
// Copyright (c) 2026 the go-quake1/engine authors.
// SPDX-License-Identifier: GPL-2.0-or-later

package bspfile

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"sync"
)

// On-wire constants.
const (
	headerSize    = 4 + HeaderLumps*lumpEntrySize // 124 bytes
	lumpEntrySize = 8                             // fileofs + filelen, int32 each
	planeSize     = 20                            // 3*float + float + int32
	vertexSize    = 12                            // 3*float
	edgeSize      = 4                             // 2*uint16
	surfedgeSize  = 4                             // int32
	modelSize     = 64                            // 3*3*float + 4*int32 + 3*int32
)

// Sentinel errors.
var (
	ErrBadVersion        = errors.New("bspfile: unsupported BSP version (this build parses 29 only)")
	ErrSectionOutOfRange = errors.New("bspfile: lump fileofs/len outside file")
	ErrShortRead         = errors.New("bspfile: short read")
	ErrSectionMisaligned = errors.New("bspfile: lump length not a multiple of element size")
)

// Lump is one (fileofs, filelen) pair from the header. tyrquake:
// lump_t.
type Lump struct {
	FileOfs int32
	FileLen int32
}

// Header is the parsed dheader_t.
type Header struct {
	Version int32
	Lumps   [HeaderLumps]Lump
}

// File ties a parsed header to the source byte slice for lazy lump
// decoding. The whole BSP is held in memory because every lump must
// be available simultaneously to the renderer + collision code.
//
// Each typed-lump accessor (Vertexes / Edges / Surfedges / Planes /
// Models / Nodes / ClipNodes / Leafs / Faces / TexInfos /
// MarkSurfaces / Textures) caches its decoded slice on first call
// and returns the same backing array on every subsequent call. This
// eliminates the per-call re-decode that otherwise dominates the
// render loop (e.g. NewBrushFaceVerts at ~60 fps × 100 faces re-
// decodes 5 lumps per face = thousands of throwaway allocations per
// second). The cache is guarded by a single sync.Mutex, so calls
// from multiple goroutines are race-free; a decode error from the
// first call is propagated and the entry is re-decoded on the next
// call (no error caching — a corrupted file stays corrupted, but we
// don't want to mask a transient error from a future custom
// io.ReaderAt either).
//
// The returned slices are SHARED with the cache: callers must not
// mutate them. Mutation would silently corrupt every subsequent
// reader. The per-frame renderer treats the lumps as read-only
// reference data, so this matches the natural usage; a defensive
// per-call copy would defeat the optimization (the whole point is
// to stop allocating per call).
//
// No invalidation is needed: a *File is immutable after Open
// (the raw byte slice is owned + never rewritten).
type File struct {
	raw    []byte
	Header Header

	// Per-lump memoization. The mutex guards every cache slot;
	// each slot starts nil and is filled on first successful
	// decode. Holding the mutex across the decode serialises
	// concurrent first-callers but is correct, and after warm-up
	// every reader hits the cache + drops the lock in constant
	// time. The whole point of this cache is to amortise the
	// decode away from the render loop, so contention only
	// matters during one-shot initialisation.
	mu           sync.Mutex
	vertices     []Vertex
	edges        []Edge
	surfedges    []Surfedge
	planes       []Plane
	models       []Model
	nodes        []Node
	clipNodes    []ClipNode
	leafs        []Leaf
	faces        []Face
	texInfos     []TexInfo
	markSurfaces []MarkSurface
	textures     *MipTexLump
}

// Vertex is one dvertex_t.
type Vertex struct {
	X, Y, Z float32
}

// Edge is one bsp29_dedge_t. Vertex numbers index into the Vertexes
// lump.
type Edge struct {
	V0, V1 uint16
}

// Surfedge wraps the int32 stored in LUMP_SURFEDGES. Positive values
// index Edges directly; negative values mean "edge -X used backwards".
// tyrquake: the int32 is consumed via abs() then sign-checked.
type Surfedge int32

// Plane is one dplane_t. Type is a PlaneX..PlaneAnyZ tag.
type Plane struct {
	Normal [3]float32
	Dist   float32
	Type   int32
}

// Model is one dmodel_t. The first model is the world; subsequent
// models are brush entities (doors, lifts, etc.) whose origin is
// relative to the world.
type Model struct {
	Mins      [3]float32
	Maxs      [3]float32
	Origin    [3]float32
	Headnode  [MaxMapHulls]int32
	VisLeafs  int32
	FirstFace int32
	NumFaces  int32
}

// Open parses src as a BSP file. The src is read in full into
// memory; a typical Quake map is ~1 MiB so this is fine. Returns a
// *File whose Lumps method gives typed access. tyrquake:
// COM_LoadHunkFile + Mod_LoadBrushModel.
func Open(src io.ReaderAt, size int64) (*File, error) {
	if src == nil {
		return nil, errors.New("bspfile: nil src")
	}
	if size < headerSize {
		return nil, ErrShortRead
	}
	raw := make([]byte, size)
	n, err := src.ReadAt(raw, 0)
	if err != nil && err != io.EOF {
		return nil, fmt.Errorf("bspfile: read: %w", err)
	}
	if int64(n) < size {
		return nil, ErrShortRead
	}

	f := &File{raw: raw}
	f.Header.Version = int32(binary.LittleEndian.Uint32(raw[0:4]))
	if f.Header.Version != Version29 {
		return nil, fmt.Errorf("%w: got %#x", ErrBadVersion, f.Header.Version)
	}
	for i := 0; i < HeaderLumps; i++ {
		off := 4 + i*lumpEntrySize
		f.Header.Lumps[i] = Lump{
			FileOfs: int32(binary.LittleEndian.Uint32(raw[off : off+4])),
			FileLen: int32(binary.LittleEndian.Uint32(raw[off+4 : off+8])),
		}
	}
	// Validate every lump's offset+length lies within the file.
	for i, l := range f.Header.Lumps {
		if err := checkSection(int64(len(raw)), l.FileOfs, l.FileLen, 1); err != nil {
			return nil, fmt.Errorf("bspfile: lump %d: %w", i, err)
		}
	}
	return f, nil
}

// LumpBytes returns the raw byte slice for the given lump. Read-only.
// Callers that need typed decoding use the per-kind methods below.
func (f *File) LumpBytes(k LumpKind) []byte {
	l := f.Header.Lumps[k]
	return f.raw[l.FileOfs : l.FileLen+l.FileOfs]
}

// Entities returns the entity-string lump verbatim (NUL-terminated
// ASCII -- the entity-list serialization the QuakeC server consumes).
// tyrquake: LUMP_ENTITIES.
func (f *File) Entities() []byte { return f.LumpBytes(LumpEntities) }

// Vertexes returns the decoded LUMP_VERTEXES slice. Cached on first
// call; the returned slice is shared with the cache (do not mutate).
// On decode error, the next call retries.
func (f *File) Vertexes() ([]Vertex, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.vertices != nil {
		return f.vertices, nil
	}
	out, err := f.decodeVertexes()
	if err != nil {
		return nil, err
	}
	f.vertices = out
	return out, nil
}

func (f *File) decodeVertexes() ([]Vertex, error) {
	raw := f.LumpBytes(LumpVertexes)
	if len(raw)%vertexSize != 0 {
		return nil, ErrSectionMisaligned
	}
	n := len(raw) / vertexSize
	out := make([]Vertex, n)
	for i := 0; i < n; i++ {
		off := i * vertexSize
		out[i].X = math.Float32frombits(binary.LittleEndian.Uint32(raw[off : off+4]))
		out[i].Y = math.Float32frombits(binary.LittleEndian.Uint32(raw[off+4 : off+8]))
		out[i].Z = math.Float32frombits(binary.LittleEndian.Uint32(raw[off+8 : off+12]))
	}
	return out, nil
}

// Edges returns the decoded LUMP_EDGES slice (bsp29 16-bit-vertex
// form). Cached on first call; do not mutate the returned slice.
func (f *File) Edges() ([]Edge, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.edges != nil {
		return f.edges, nil
	}
	out, err := f.decodeEdges()
	if err != nil {
		return nil, err
	}
	f.edges = out
	return out, nil
}

func (f *File) decodeEdges() ([]Edge, error) {
	raw := f.LumpBytes(LumpEdges)
	if len(raw)%edgeSize != 0 {
		return nil, ErrSectionMisaligned
	}
	n := len(raw) / edgeSize
	out := make([]Edge, n)
	for i := 0; i < n; i++ {
		off := i * edgeSize
		out[i].V0 = binary.LittleEndian.Uint16(raw[off : off+2])
		out[i].V1 = binary.LittleEndian.Uint16(raw[off+2 : off+4])
	}
	return out, nil
}

// Surfedges returns the decoded LUMP_SURFEDGES slice (one int32 per
// entry). Cached on first call; do not mutate the returned slice.
func (f *File) Surfedges() ([]Surfedge, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.surfedges != nil {
		return f.surfedges, nil
	}
	out, err := f.decodeSurfedges()
	if err != nil {
		return nil, err
	}
	f.surfedges = out
	return out, nil
}

func (f *File) decodeSurfedges() ([]Surfedge, error) {
	raw := f.LumpBytes(LumpSurfedges)
	if len(raw)%surfedgeSize != 0 {
		return nil, ErrSectionMisaligned
	}
	n := len(raw) / surfedgeSize
	out := make([]Surfedge, n)
	for i := 0; i < n; i++ {
		out[i] = Surfedge(int32(binary.LittleEndian.Uint32(raw[i*4 : i*4+4])))
	}
	return out, nil
}

// Planes returns the decoded LUMP_PLANES slice. Cached on first
// call; do not mutate the returned slice.
func (f *File) Planes() ([]Plane, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.planes != nil {
		return f.planes, nil
	}
	out, err := f.decodePlanes()
	if err != nil {
		return nil, err
	}
	f.planes = out
	return out, nil
}

func (f *File) decodePlanes() ([]Plane, error) {
	raw := f.LumpBytes(LumpPlanes)
	if len(raw)%planeSize != 0 {
		return nil, ErrSectionMisaligned
	}
	n := len(raw) / planeSize
	out := make([]Plane, n)
	for i := 0; i < n; i++ {
		off := i * planeSize
		out[i].Normal[0] = math.Float32frombits(binary.LittleEndian.Uint32(raw[off : off+4]))
		out[i].Normal[1] = math.Float32frombits(binary.LittleEndian.Uint32(raw[off+4 : off+8]))
		out[i].Normal[2] = math.Float32frombits(binary.LittleEndian.Uint32(raw[off+8 : off+12]))
		out[i].Dist = math.Float32frombits(binary.LittleEndian.Uint32(raw[off+12 : off+16]))
		out[i].Type = int32(binary.LittleEndian.Uint32(raw[off+16 : off+20]))
	}
	return out, nil
}

// Models returns the decoded LUMP_MODELS slice. Cached on first
// call; do not mutate the returned slice.
func (f *File) Models() ([]Model, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.models != nil {
		return f.models, nil
	}
	out, err := f.decodeModels()
	if err != nil {
		return nil, err
	}
	f.models = out
	return out, nil
}

func (f *File) decodeModels() ([]Model, error) {
	raw := f.LumpBytes(LumpModels)
	if len(raw)%modelSize != 0 {
		return nil, ErrSectionMisaligned
	}
	n := len(raw) / modelSize
	out := make([]Model, n)
	for i := 0; i < n; i++ {
		off := i * modelSize
		readVec3 := func(at int) [3]float32 {
			return [3]float32{
				math.Float32frombits(binary.LittleEndian.Uint32(raw[at : at+4])),
				math.Float32frombits(binary.LittleEndian.Uint32(raw[at+4 : at+8])),
				math.Float32frombits(binary.LittleEndian.Uint32(raw[at+8 : at+12])),
			}
		}
		out[i].Mins = readVec3(off)
		out[i].Maxs = readVec3(off + 12)
		out[i].Origin = readVec3(off + 24)
		for j := 0; j < MaxMapHulls; j++ {
			out[i].Headnode[j] = int32(binary.LittleEndian.Uint32(raw[off+36+j*4 : off+36+j*4+4]))
		}
		out[i].VisLeafs = int32(binary.LittleEndian.Uint32(raw[off+52 : off+56]))
		out[i].FirstFace = int32(binary.LittleEndian.Uint32(raw[off+56 : off+60]))
		out[i].NumFaces = int32(binary.LittleEndian.Uint32(raw[off+60 : off+64]))
	}
	return out, nil
}

// checkSection validates [ofs, ofs+len*unit) lies in [0, fileSize).
// An empty section is valid wherever its offset lies in the file: its offset is
// still used to slice the file (LumpBytes), so one outside [0, fileSize] is
// refused (it used to panic there: found by FuzzOpen).
func checkSection(fileSize int64, ofs, length int32, unit int) error {
	if length == 0 {
		if ofs < 0 || int64(ofs) > fileSize {
			return ErrSectionOutOfRange
		}
		return nil
	}
	if ofs < 0 || length < 0 {
		return ErrSectionOutOfRange
	}
	end := int64(ofs) + int64(length)*int64(unit)
	if int64(ofs) > fileSize || end > fileSize {
		return ErrSectionOutOfRange
	}
	return nil
}
