// Copyright (c) 2026 the go-quake1/engine authors.
// SPDX-License-Identifier: GPL-2.0-or-later

package model

import (
	"bytes"
	"encoding/binary"
	"errors"
	"testing"

	"github.com/go-quake1/engine/bspfile"
	"github.com/go-quake1/engine/bspfile/synthbsp"
)

// --- Synthetic BSP with PVS data + a 4-node / 5-leaf tree -----------------
//
// We need a richer tree than buildMinimalBSPForBrushModel to exercise
// setParent + the PVS / parent-walk accessors. The tree:
//
//	            node 0  (root, parent = -1)
//	           /        \
//	       node 1      node 2
//	       /   \        /   \
//	    leaf1 leaf2  leaf3  node 3
//	                        /   \
//	                     leaf4  leaf5
//
// Stored 0-based: leafs = [outside, leaf1, leaf2, leaf3, leaf4, leaf5]
// so the 1-based VisLeafIdx the renderer uses maps to slice index
// directly. Leaf 0 (outside) is a SOLID placeholder, the other five
// carry an EMPTY contents tag + an explicit VisOfs into our PVS blob.

// pvsLeaf is one (1-based) leaf entry with the bits visible from it.
type pvsLeaf struct {
	visible []int // 1-based leaf indices visible from this leaf
}

// buildBSPWithPVS encodes the 5-leaf tree above plus a LumpVisibility
// blob whose row layout is: leaf i's PVS row starts at visdata offset
// pvsOffsets[i]. Each row uses the raw "no-RLE" form (a contiguous
// byte array sized for NumLeaves bits = 5 bits = 1 byte). Since the
// rows happen to be non-zero, DecompressVis copies them verbatim.
func buildBSPWithPVS(t *testing.T, pvs []pvsLeaf) ([]byte, int64) {
	t.Helper()

	planes := []bspfile.Plane{
		{Normal: [3]float32{1, 0, 0}, Dist: 0, Type: bspfile.PlaneX},
		{Normal: [3]float32{0, 1, 0}, Dist: 0, Type: bspfile.PlaneY},
		{Normal: [3]float32{0, 0, 1}, Dist: 0, Type: bspfile.PlaneZ},
		{Normal: [3]float32{1, 1, 0}, Dist: 0, Type: bspfile.PlaneAnyX},
	}
	// 4 nodes wiring the tree shown above.
	//   node 0 children: node 1 (=1), node 2 (=2)
	//   node 1 children: leaf 1 (=^1), leaf 2 (=^2)
	//   node 2 children: leaf 3 (=^3), node 3 (=3)
	//   node 3 children: leaf 4 (=^4), leaf 5 (=^5)
	nodes := []bspfile.Node{
		{PlaneNum: 0, Children: [2]int16{1, 2}},
		{PlaneNum: 1, Children: [2]int16{^int16(1), ^int16(2)}},
		{PlaneNum: 2, Children: [2]int16{^int16(3), 3}},
		{PlaneNum: 3, Children: [2]int16{^int16(4), ^int16(5)}},
	}
	// Build the LumpVisibility blob. One row per PVS-trackable leaf
	// (5 rows for 5 leaves), 1 byte each.
	const pvsRowBytes = 1
	visBlob := make([]byte, pvsRowBytes*len(pvs))
	for i, p := range pvs {
		for _, v := range p.visible {
			bit := v - 1
			visBlob[i*pvsRowBytes+bit/8] |= 1 << uint(bit%8)
		}
	}

	// 6 leaves total: [outside, leaf1..leaf5]. Outside is SOLID and
	// VisOfs = -1; the five PVS leaves get VisOfs = (i-1) * pvsRowBytes.
	leafs := []bspfile.Leaf{
		{Contents: bspfile.ContentsSolid, VisOfs: -1},
	}
	for i := range pvs {
		leafs = append(leafs, bspfile.Leaf{
			Contents: bspfile.ContentsEmpty,
			VisOfs:   int32(i * pvsRowBytes),
		})
	}

	clipnodes := []bspfile.ClipNode{
		{PlaneNum: 0, Children: [2]int16{bspfile.ContentsEmpty, bspfile.ContentsSolid}},
	}
	models := []bspfile.Model{
		{
			Mins:     [3]float32{-100, -100, -100},
			Maxs:     [3]float32{100, 100, 100},
			Headnode: [bspfile.MaxMapHulls]int32{0, 0, 0, 0},
		},
	}

	// Encode lumps to bytes.
	pb := encodePlanes(planes)
	nb := encodeNodes(nodes)
	lb := encodeLeafs(leafs)
	cnb := encodeClipnodes(clipnodes)
	mb := encodeModels(models)

	const headerSize = 4 + 15*8
	body := &bytes.Buffer{}

	type lumpInfo struct {
		kind bspfile.LumpKind
		data []byte
	}
	lumps := []lumpInfo{
		{kind: bspfile.LumpPlanes, data: pb},
		{kind: bspfile.LumpVisibility, data: visBlob},
		{kind: bspfile.LumpNodes, data: nb},
		{kind: bspfile.LumpLeafs, data: lb},
		{kind: bspfile.LumpClipnodes, data: cnb},
		{kind: bspfile.LumpModels, data: mb},
	}
	offsetByKind := map[bspfile.LumpKind]int32{}
	lenByKind := map[bspfile.LumpKind]int32{}
	for _, l := range lumps {
		offsetByKind[l.kind] = int32(headerSize) + int32(body.Len())
		body.Write(l.data)
		lenByKind[l.kind] = int32(len(l.data))
	}

	hdr := &bytes.Buffer{}
	_ = binary.Write(hdr, binary.LittleEndian, int32(bspfile.Version29))
	for k := bspfile.LumpKind(0); int(k) < bspfile.HeaderLumps; k++ {
		_ = binary.Write(hdr, binary.LittleEndian, offsetByKind[k])
		_ = binary.Write(hdr, binary.LittleEndian, lenByKind[k])
	}
	full := append(hdr.Bytes(), body.Bytes()...)
	return full, int64(len(full))
}

func encodePlanes(planes []bspfile.Plane) []byte {
	b := &bytes.Buffer{}
	for _, p := range planes {
		_ = binary.Write(b, binary.LittleEndian, p.Normal[0])
		_ = binary.Write(b, binary.LittleEndian, p.Normal[1])
		_ = binary.Write(b, binary.LittleEndian, p.Normal[2])
		_ = binary.Write(b, binary.LittleEndian, p.Dist)
		_ = binary.Write(b, binary.LittleEndian, p.Type)
	}
	return b.Bytes()
}

func encodeNodes(nodes []bspfile.Node) []byte {
	b := &bytes.Buffer{}
	for _, n := range nodes {
		_ = binary.Write(b, binary.LittleEndian, n.PlaneNum)
		_ = binary.Write(b, binary.LittleEndian, n.Children[0])
		_ = binary.Write(b, binary.LittleEndian, n.Children[1])
		for j := 0; j < 3; j++ {
			_ = binary.Write(b, binary.LittleEndian, n.Mins[j])
		}
		for j := 0; j < 3; j++ {
			_ = binary.Write(b, binary.LittleEndian, n.Maxs[j])
		}
		_ = binary.Write(b, binary.LittleEndian, n.FirstFace)
		_ = binary.Write(b, binary.LittleEndian, n.NumFaces)
	}
	return b.Bytes()
}

func encodeLeafs(leafs []bspfile.Leaf) []byte {
	b := &bytes.Buffer{}
	for _, l := range leafs {
		_ = binary.Write(b, binary.LittleEndian, l.Contents)
		_ = binary.Write(b, binary.LittleEndian, l.VisOfs)
		for j := 0; j < 3; j++ {
			_ = binary.Write(b, binary.LittleEndian, l.Mins[j])
		}
		for j := 0; j < 3; j++ {
			_ = binary.Write(b, binary.LittleEndian, l.Maxs[j])
		}
		_ = binary.Write(b, binary.LittleEndian, l.FirstMarkSurface)
		_ = binary.Write(b, binary.LittleEndian, l.NumMarkSurfaces)
		b.Write(l.AmbientLevel[:])
	}
	return b.Bytes()
}

func encodeClipnodes(cs []bspfile.ClipNode) []byte {
	b := &bytes.Buffer{}
	for _, c := range cs {
		_ = binary.Write(b, binary.LittleEndian, c.PlaneNum)
		_ = binary.Write(b, binary.LittleEndian, c.Children[0])
		_ = binary.Write(b, binary.LittleEndian, c.Children[1])
	}
	return b.Bytes()
}

func encodeModels(ms []bspfile.Model) []byte {
	b := &bytes.Buffer{}
	for _, m := range ms {
		for j := 0; j < 3; j++ {
			_ = binary.Write(b, binary.LittleEndian, m.Mins[j])
		}
		for j := 0; j < 3; j++ {
			_ = binary.Write(b, binary.LittleEndian, m.Maxs[j])
		}
		for j := 0; j < 3; j++ {
			_ = binary.Write(b, binary.LittleEndian, m.Origin[j])
		}
		for j := 0; j < bspfile.MaxMapHulls; j++ {
			_ = binary.Write(b, binary.LittleEndian, m.Headnode[j])
		}
		_ = binary.Write(b, binary.LittleEndian, m.VisLeafs)
		_ = binary.Write(b, binary.LittleEndian, m.FirstFace)
		_ = binary.Write(b, binary.LittleEndian, m.NumFaces)
	}
	return b.Bytes()
}

// loadPVSWorld builds the 5-leaf tree with the per-leaf PVS rows and
// returns the resulting *BrushModel.
func loadPVSWorld(t *testing.T, pvs []pvsLeaf) *BrushModel {
	t.Helper()
	data, size := buildBSPWithPVS(t, pvs)
	f, err := bspfile.Open(bytes.NewReader(data), size)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	bm, err := LoadBrush(f, 0)
	if err != nil {
		t.Fatalf("LoadBrush: %v", err)
	}
	return bm
}

// --- NumLeaves / Leaf / Node accessors -----------------------------------

func TestBrushModel_NumLeavesAndAccessors(t *testing.T) {
	bm := loadPVSWorld(t, []pvsLeaf{
		{visible: []int{1}},
		{visible: []int{2}},
		{visible: []int{3}},
		{visible: []int{4}},
		{visible: []int{5}},
	})

	// 6 raw leaves -> 5 PVS-trackable.
	if got := bm.NumLeaves(); got != 5 {
		t.Errorf("NumLeaves: got %d want 5", got)
	}
	// Leaf 0 is the outside sentinel (SOLID).
	if bm.Leaf(0).Contents != bspfile.ContentsSolid {
		t.Errorf("Leaf(0).Contents: got %d want SOLID", bm.Leaf(0).Contents)
	}
	if bm.Leaf(0).ParentNode != -1 {
		t.Errorf("Leaf(0).ParentNode: got %d want -1 (sentinel unreachable from tree)", bm.Leaf(0).ParentNode)
	}
	// Leaf 1..5 are EMPTY.
	for i := 1; i <= 5; i++ {
		if bm.Leaf(i).Contents != bspfile.ContentsEmpty {
			t.Errorf("Leaf(%d).Contents: got %d want EMPTY", i, bm.Leaf(i).Contents)
		}
	}
	// 4 nodes.
	if bm.Node(0).PlaneNum != 0 {
		t.Errorf("Node(0).PlaneNum: got %d want 0", bm.Node(0).PlaneNum)
	}
	if bm.Node(3).PlaneNum != 3 {
		t.Errorf("Node(3).PlaneNum: got %d want 3", bm.Node(3).PlaneNum)
	}
}

// Empty / 1-leaf models report NumLeaves = 0 (no PVS work).
func TestBrushModel_NumLeavesEmptyAndSingleton(t *testing.T) {
	bm := &BrushModel{}
	if got := bm.NumLeaves(); got != 0 {
		t.Errorf("empty NumLeaves: got %d want 0", got)
	}
	bm.leaves = []Leaf{{}}
	if got := bm.NumLeaves(); got != 0 {
		t.Errorf("singleton NumLeaves: got %d want 0", got)
	}
}

// --- VisFrame round-trip --------------------------------------------------

func TestBrushModel_VisFrameRoundTrip(t *testing.T) {
	bm := loadPVSWorld(t, []pvsLeaf{
		{}, {}, {}, {}, {},
	})

	bm.SetLeafVisFrame(2, 17)
	if got := bm.Leaf(2).VisFrame; got != 17 {
		t.Errorf("SetLeafVisFrame round-trip: leaf 2 VisFrame=%d want 17", got)
	}

	bm.SetNodeVisFrame(1, 99)
	if got := bm.GetNodeVisFrame(1); got != 99 {
		t.Errorf("SetNodeVisFrame/GetNodeVisFrame round-trip: got %d want 99", got)
	}
	// Untouched node still 0.
	if got := bm.GetNodeVisFrame(0); got != 0 {
		t.Errorf("untouched node 0 VisFrame: got %d want 0", got)
	}
}

// --- Parent walk (setParent invoked from LoadBrush) ----------------------

func TestBrushModel_SetParentTreeWalk(t *testing.T) {
	bm := loadPVSWorld(t, []pvsLeaf{
		{}, {}, {}, {}, {},
	})

	// Root is node 0, parent must be -1.
	if got := bm.NodeParent(0); got != -1 {
		t.Errorf("root NodeParent: got %d want -1", got)
	}
	// Both children of root are nodes 1 and 2; their parent is 0.
	if got := bm.NodeParent(1); got != 0 {
		t.Errorf("node 1 parent: got %d want 0", got)
	}
	if got := bm.NodeParent(2); got != 0 {
		t.Errorf("node 2 parent: got %d want 0", got)
	}
	// Node 3 hangs off node 2.
	if got := bm.NodeParent(3); got != 2 {
		t.Errorf("node 3 parent: got %d want 2", got)
	}
	// Leaf parents.
	if got := bm.LeafParentNode(1); got != 1 {
		t.Errorf("leaf 1 parent: got %d want 1", got)
	}
	if got := bm.LeafParentNode(2); got != 1 {
		t.Errorf("leaf 2 parent: got %d want 1", got)
	}
	if got := bm.LeafParentNode(3); got != 2 {
		t.Errorf("leaf 3 parent: got %d want 2", got)
	}
	if got := bm.LeafParentNode(4); got != 3 {
		t.Errorf("leaf 4 parent: got %d want 3", got)
	}
	if got := bm.LeafParentNode(5); got != 3 {
		t.Errorf("leaf 5 parent: got %d want 3", got)
	}
}

// setParent bails on out-of-range node indices (used by submodel
// headnodes that reach into the same nodes array with a different
// root). Construct the case directly so the guard branch is exercised.
func TestBrushModel_SetParentOutOfRange(t *testing.T) {
	bm := &BrushModel{
		nodes:  []Node{{ParentNode: -1}},
		leaves: []Leaf{{ParentNode: -1}},
	}
	// Negative index -- no-op.
	bm.setParent(-1, 99)
	if bm.nodes[0].ParentNode != -1 {
		t.Errorf("nodes[0] mutated by negative setParent: got %d", bm.nodes[0].ParentNode)
	}
	// Past-end index -- no-op.
	bm.setParent(99, 0)
	if bm.nodes[0].ParentNode != -1 {
		t.Errorf("nodes[0] mutated by out-of-range setParent: got %d", bm.nodes[0].ParentNode)
	}
}

// setParent silently skips child leaf indices outside the leaves
// slice. Build a node whose ^child decodes to a leaf index past the
// slice and verify the walk doesn't panic + the in-range leaf is
// still tagged.
func TestBrushModel_SetParentSkipsOutOfRangeLeafChild(t *testing.T) {
	bm := &BrushModel{
		// One node, child 0 -> leaf 0 (valid), child 1 -> leaf 99 (invalid).
		nodes:  []Node{{Node: bspfile.Node{Children: [2]int16{^int16(0), ^int16(99)}}, ParentNode: -1}},
		leaves: []Leaf{{ParentNode: -1}},
	}
	bm.setParent(0, -1)
	if bm.leaves[0].ParentNode != 0 {
		t.Errorf("leaf 0 parent: got %d want 0", bm.leaves[0].ParentNode)
	}
	// No panic == success.
}

// --- PVSForLeaf -----------------------------------------------------------

func TestBrushModel_PVSForLeaf_HasVisData(t *testing.T) {
	// Leaf 1's row: leaves 2 and 4 visible (bits 1 and 3 set -> 0x0A).
	// Leaf 2's row: leaf 5 visible (bit 4 set -> 0x10).
	bm := loadPVSWorld(t, []pvsLeaf{
		{visible: []int{2, 4}},
		{visible: []int{5}},
		{},
		{},
		{},
	})

	row1 := bm.PVSForLeaf(1)
	if len(row1) < 1 || row1[0] != 0x0A {
		t.Errorf("PVSForLeaf(1)[0]: got %#x want 0x0A", row1[0])
	}
	row2 := bm.PVSForLeaf(2)
	if len(row2) < 1 || row2[0] != 0x10 {
		t.Errorf("PVSForLeaf(2)[0]: got %#x want 0x10", row2[0])
	}
}

func TestBrushModel_PVSForLeaf_NoVisInfoReturnsAllVisible(t *testing.T) {
	// Reach the "all 0xFF" branch: rebuild the model and poison the
	// leaf 1 VisOfs to -1 directly (after LoadBrush has already
	// decoded it).
	bm := loadPVSWorld(t, []pvsLeaf{
		{visible: []int{1}}, {}, {}, {}, {},
	})
	bm.leaves[1].VisOfs = -1

	row := bm.PVSForLeaf(1)
	wantLen := (bm.NumLeaves() + 7) / 8
	if len(row) != wantLen {
		t.Fatalf("PVSForLeaf no-vis row len: got %d want %d", len(row), wantLen)
	}
	for i, b := range row {
		if b != 0xFF {
			t.Errorf("PVSForLeaf no-vis byte %d: got %#x want 0xFF", i, b)
		}
	}
}

func TestBrushModel_PVSForLeaf_OutOfRange(t *testing.T) {
	bm := loadPVSWorld(t, []pvsLeaf{
		{visible: []int{1}}, {}, {}, {}, {},
	})
	if bm.PVSForLeaf(0) != nil {
		t.Error("PVSForLeaf(0): want nil")
	}
	if bm.PVSForLeaf(99) != nil {
		t.Error("PVSForLeaf(99): want nil")
	}
}

// Also exercise the "VisOfs >= len(visdata)" branch (treated as
// "missing vis" -> all visible).
func TestBrushModel_PVSForLeaf_VisOfsPastEnd(t *testing.T) {
	bm := loadPVSWorld(t, []pvsLeaf{
		{visible: []int{1}}, {}, {}, {}, {},
	})
	bm.leaves[1].VisOfs = int32(len(bm.File.Visibility()) + 100)
	row := bm.PVSForLeaf(1)
	if len(row) == 0 {
		t.Fatal("expected an all-visible row, got empty")
	}
	for _, b := range row {
		if b != 0xFF {
			t.Errorf("expected 0xFF byte, got %#x", b)
		}
	}
}

// --- NumNodes / TotalLeaves -----------------------------------------------

func TestBrushModel_NumNodesAndTotalLeaves(t *testing.T) {
	bm := loadPVSWorld(t, []pvsLeaf{{}, {}, {}, {}, {}})
	// 4 nodes wiring the 5-leaf tree, plus the outside sentinel = 6 raw leaves.
	if got := bm.NumNodes(); got != 4 {
		t.Errorf("NumNodes: got %d want 4", got)
	}
	if got := bm.TotalLeaves(); got != 6 {
		t.Errorf("TotalLeaves: got %d want 6", got)
	}
}

func TestBrushModel_NumNodesEmpty(t *testing.T) {
	bm := &BrushModel{}
	if got := bm.NumNodes(); got != 0 {
		t.Errorf("empty NumNodes: got %d want 0", got)
	}
	if got := bm.TotalLeaves(); got != 0 {
		t.Errorf("empty TotalLeaves: got %d want 0", got)
	}
}

// --- LeafFaceIndices ------------------------------------------------------

// buildBSPWithMarksurfaces wraps buildBSPWithPVS plus a LumpMarksurfaces
// blob + per-leaf (FirstMarkSurface, NumMarkSurfaces) spans. marks[i]
// is the marksurfaces span for the i-th PVS leaf (1-based; the outside
// sentinel at index 0 always carries an empty span).
func buildBSPWithMarksurfaces(t *testing.T, pvs []pvsLeaf, marks [][]uint16) ([]byte, int64) {
	t.Helper()
	if len(marks) != len(pvs) {
		t.Fatalf("len(marks)=%d != len(pvs)=%d", len(marks), len(pvs))
	}
	// Encode the marksurfaces blob + per-leaf (first, count) spans.
	// All per-leaf spans live in one shared blob, concatenated in
	// PVS-leaf order.
	marksBlob := &bytes.Buffer{}
	type span struct{ first, count uint16 }
	spans := make([]span, len(marks))
	cursor := uint16(0)
	for i, m := range marks {
		spans[i] = span{first: cursor, count: uint16(len(m))}
		for _, v := range m {
			_ = binary.Write(marksBlob, binary.LittleEndian, v)
		}
		cursor += uint16(len(m))
	}

	planes := []bspfile.Plane{
		{Normal: [3]float32{1, 0, 0}, Dist: 0, Type: bspfile.PlaneX},
		{Normal: [3]float32{0, 1, 0}, Dist: 0, Type: bspfile.PlaneY},
		{Normal: [3]float32{0, 0, 1}, Dist: 0, Type: bspfile.PlaneZ},
		{Normal: [3]float32{1, 1, 0}, Dist: 0, Type: bspfile.PlaneAnyX},
	}
	nodes := []bspfile.Node{
		{PlaneNum: 0, Children: [2]int16{1, 2}},
		{PlaneNum: 1, Children: [2]int16{^int16(1), ^int16(2)}},
		{PlaneNum: 2, Children: [2]int16{^int16(3), 3}},
		{PlaneNum: 3, Children: [2]int16{^int16(4), ^int16(5)}},
	}
	const pvsRowBytes = 1
	visBlob := make([]byte, pvsRowBytes*len(pvs))
	for i, p := range pvs {
		for _, v := range p.visible {
			bit := v - 1
			visBlob[i*pvsRowBytes+bit/8] |= 1 << uint(bit%8)
		}
	}
	leafs := []bspfile.Leaf{
		{Contents: bspfile.ContentsSolid, VisOfs: -1},
	}
	for i := range pvs {
		leafs = append(leafs, bspfile.Leaf{
			Contents:         bspfile.ContentsEmpty,
			VisOfs:           int32(i * pvsRowBytes),
			FirstMarkSurface: spans[i].first,
			NumMarkSurfaces:  spans[i].count,
		})
	}
	clipnodes := []bspfile.ClipNode{
		{PlaneNum: 0, Children: [2]int16{bspfile.ContentsEmpty, bspfile.ContentsSolid}},
	}
	models := []bspfile.Model{
		{
			Mins:     [3]float32{-100, -100, -100},
			Maxs:     [3]float32{100, 100, 100},
			Headnode: [bspfile.MaxMapHulls]int32{0, 0, 0, 0},
		},
	}

	const headerSize = 4 + 15*8
	body := &bytes.Buffer{}
	type lumpInfo struct {
		kind bspfile.LumpKind
		data []byte
	}
	lumps := []lumpInfo{
		{kind: bspfile.LumpPlanes, data: encodePlanes(planes)},
		{kind: bspfile.LumpVisibility, data: visBlob},
		{kind: bspfile.LumpNodes, data: encodeNodes(nodes)},
		{kind: bspfile.LumpLeafs, data: encodeLeafs(leafs)},
		{kind: bspfile.LumpClipnodes, data: encodeClipnodes(clipnodes)},
		{kind: bspfile.LumpModels, data: encodeModels(models)},
		{kind: bspfile.LumpMarksurfaces, data: marksBlob.Bytes()},
	}
	offsetByKind := map[bspfile.LumpKind]int32{}
	lenByKind := map[bspfile.LumpKind]int32{}
	for _, l := range lumps {
		offsetByKind[l.kind] = int32(headerSize) + int32(body.Len())
		body.Write(l.data)
		lenByKind[l.kind] = int32(len(l.data))
	}
	hdr := &bytes.Buffer{}
	_ = binary.Write(hdr, binary.LittleEndian, int32(bspfile.Version29))
	for k := bspfile.LumpKind(0); int(k) < bspfile.HeaderLumps; k++ {
		_ = binary.Write(hdr, binary.LittleEndian, offsetByKind[k])
		_ = binary.Write(hdr, binary.LittleEndian, lenByKind[k])
	}
	full := append(hdr.Bytes(), body.Bytes()...)
	return full, int64(len(full))
}

func loadMarksurfacesWorld(t *testing.T, pvs []pvsLeaf, marks [][]uint16) *BrushModel {
	t.Helper()
	data, size := buildBSPWithMarksurfaces(t, pvs, marks)
	f, err := bspfile.Open(bytes.NewReader(data), size)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	bm, err := LoadBrush(f, 0)
	if err != nil {
		t.Fatalf("LoadBrush: %v", err)
	}
	return bm
}

func TestBrushModel_LeafFaceIndices_HappyPath(t *testing.T) {
	bm := loadMarksurfacesWorld(t,
		[]pvsLeaf{{}, {}, {}, {}, {}},
		[][]uint16{
			{10, 11, 12}, // leaf 1
			{},           // leaf 2
			{20},         // leaf 3
			{30, 31},     // leaf 4
			{},           // leaf 5
		},
	)
	got, err := bm.LeafFaceIndices(1)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	want := []int{10, 11, 12}
	if len(got) != len(want) {
		t.Fatalf("leaf 1: got %v want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("leaf 1[%d]: got %d want %d", i, got[i], want[i])
		}
	}
	// Leaf 2 has zero marksurfaces -> empty slice (no decode).
	if r, _ := bm.LeafFaceIndices(2); len(r) != 0 {
		t.Errorf("leaf 2: got %v want empty", r)
	}
	// Leaf 4 (offset 4 from a span of 6) -> [30, 31].
	r4, _ := bm.LeafFaceIndices(4)
	if len(r4) != 2 || r4[0] != 30 || r4[1] != 31 {
		t.Errorf("leaf 4: got %v want [30 31]", r4)
	}
	// Outside sentinel leaf 0 has zero marksurfaces.
	if r, _ := bm.LeafFaceIndices(0); len(r) != 0 {
		t.Errorf("leaf 0: got %v want empty", r)
	}
}

func TestBrushModel_LeafFaceIndices_OutOfRange(t *testing.T) {
	bm := loadPVSWorld(t, []pvsLeaf{{}, {}, {}, {}, {}})
	if r, err := bm.LeafFaceIndices(-1); err != nil || r != nil {
		t.Errorf("LeafFaceIndices(-1): got (%v, %v) want (nil, nil)", r, err)
	}
	if r, err := bm.LeafFaceIndices(999); err != nil || r != nil {
		t.Errorf("LeafFaceIndices(999): got (%v, %v) want (nil, nil)", r, err)
	}
}

// LeafFaceIndices returns an empty slice when the leaf's
// (FirstMarkSurface, NumMarkSurfaces) span over-runs the lump.
func TestBrushModel_LeafFaceIndices_SpanOverflow(t *testing.T) {
	bm := loadMarksurfacesWorld(t,
		[]pvsLeaf{{}, {}, {}, {}, {}},
		[][]uint16{{10}, {}, {}, {}, {}},
	)
	// Poison leaf 1's span to over-run the 1-entry blob.
	bm.leaves[1].FirstMarkSurface = 0
	bm.leaves[1].NumMarkSurfaces = 99
	r, err := bm.LeafFaceIndices(1)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if r != nil {
		t.Errorf("over-run span: got %v want nil", r)
	}
}

// LeafFaceIndices propagates a MarkSurfaces decode error (malformed
// lump byte length). Build a BSP whose LumpMarksurfaces blob has an
// odd byte count so the loader's `len % marksurfaceSize == 0` check
// trips.
func TestBrushModel_LeafFaceIndices_DecodeError(t *testing.T) {
	// Reuse buildBSPWithPVS but overlay an odd-length marksurfaces lump.
	data, _ := buildBSPWithMarksurfaces(t,
		[]pvsLeaf{{}, {}, {}, {}, {}},
		[][]uint16{{10}, {}, {}, {}, {}},
	)
	// Patch the marksurfaces lump byte length in the header to a
	// non-multiple of 2 (the marksurface record size). Lump 11 is
	// LumpMarksurfaces; header layout: int32 version + per-lump
	// (int32 offset, int32 length). So the length byte we need to
	// flip lives at offset 4 + 11*8 + 4 = 96.
	const marksLumpLenOff = 4 + int(bspfile.LumpMarksurfaces)*8 + 4
	// Write a 1-byte odd length.
	binary.LittleEndian.PutUint32(data[marksLumpLenOff:marksLumpLenOff+4], 1)
	f, err := bspfile.Open(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	bm, err := LoadBrush(f, 0)
	if err != nil {
		t.Fatalf("LoadBrush: %v", err)
	}
	if r, err := bm.LeafFaceIndices(1); err == nil {
		t.Errorf("got (%v, nil) want err", r)
	}
}

// --- PointInLeaf ----------------------------------------------------------
//
// The two synthbsp fixtures span the interesting cases:
//
//   - BuildFiveLeafPVS: 4 interior nodes splitting on X / Y / Z / X+Y, so
//     the descent visits every code path (front + back at multiple
//     depths, on-plane fallback to back-child).
//   - BuildWithFaces: a single-node BSP where the descent terminates on
//     the very first interior step. Doubles as the smallest viable
//     fixture.

// loadSynthFiveLeafPVS opens the canonical 5-leaf BSP from
// bspfile/synthbsp and returns a fully-loaded BrushModel for it.
func loadSynthFiveLeafPVS(t *testing.T) *BrushModel {
	t.Helper()
	data, size, err := synthbsp.BuildFiveLeafPVS()
	if err != nil {
		t.Fatalf("synthbsp.BuildFiveLeafPVS: %v", err)
	}
	f, err := bspfile.Open(bytes.NewReader(data), size)
	if err != nil {
		t.Fatalf("bspfile.Open: %v", err)
	}
	bm, err := LoadBrush(f, 0)
	if err != nil {
		t.Fatalf("LoadBrush: %v", err)
	}
	return bm
}

// loadSynthWithFaces opens the BuildWithFaces single-node BSP.
func loadSynthWithFaces(t *testing.T) *BrushModel {
	t.Helper()
	data, size, err := synthbsp.BuildWithFaces()
	if err != nil {
		t.Fatalf("synthbsp.BuildWithFaces: %v", err)
	}
	f, err := bspfile.Open(bytes.NewReader(data), size)
	if err != nil {
		t.Fatalf("bspfile.Open: %v", err)
	}
	bm, err := LoadBrush(f, 0)
	if err != nil {
		t.Fatalf("LoadBrush: %v", err)
	}
	return bm
}

// TestBrushModel_PointInLeaf_FiveLeafPVS walks every leaf of the
// 5-leaf tree by handing PointInLeaf a point known to fall into that
// leaf, and asserts the returned index matches.
//
// Tree (from synthbsp.BuildFiveLeafPVS):
//
//	node 0: plane (1,0,0) dist 0   -- splits by X
//	node 1: plane (0,1,0) dist 0   -- splits by Y
//	node 2: plane (0,0,1) dist 0   -- splits by Z
//	node 3: plane (1,1,0) dist 0   -- splits by x+y
//
// Children: node 0 -> [n1, n2]; node 1 -> [^1, ^2]; node 2 -> [^3, n3];
// node 3 -> [^4, ^5]. Descent uses `dot - dist > 0` for the front
// (Children[0]) branch.
func TestBrushModel_PointInLeaf_FiveLeafPVS(t *testing.T) {
	bm := loadSynthFiveLeafPVS(t)

	cases := []struct {
		name  string
		point [3]float32
		want  int
	}{
		{"x>0, y>0 -> leaf 1", [3]float32{5, 5, 0}, 1},
		{"x>0, y<0 -> leaf 2", [3]float32{5, -5, 0}, 2},
		{"x<0, z>0 -> leaf 3", [3]float32{-5, 0, 5}, 3},
		{"x<0, z<0, x+y>0 -> leaf 4", [3]float32{-1, 5, -5}, 4},
		{"x<0, z<0, x+y<0 -> leaf 5", [3]float32{-5, -5, -5}, 5},
		// On-plane case at the root: x=0 falls to back child (node 2),
		// then z>0 -> leaf 3. Verifies the `dist > 0` strict-positive
		// gating matches tyrquake.
		{"x=0 (on plane) z>0 -> leaf 3", [3]float32{0, 0, 5}, 3},
	}
	for _, tc := range cases {
		got := bm.PointInLeaf(tc.point)
		if got != tc.want {
			t.Errorf("%s: PointInLeaf(%v) = %d, want %d", tc.name, tc.point, got, tc.want)
		}
	}
}

// TestBrushModel_PointInLeaf_FiveLeafPVSConsistency picks a cluster of
// points within the same half-space partition and asserts they all
// agree on the destination leaf -- a property a buggy descent (e.g.
// flipped child polarity) would not preserve.
func TestBrushModel_PointInLeaf_FiveLeafPVSConsistency(t *testing.T) {
	bm := loadSynthFiveLeafPVS(t)

	// All four points satisfy x > 0 and y > 0, so they must all land
	// in leaf 1 -- the front-front descent of nodes 0 and 1.
	leaf1Cluster := [][3]float32{
		{1, 1, 0},
		{10, 0.5, -100},
		{1e-3, 1e-3, 1e6},
		{42, 42, 42},
	}
	want := bm.PointInLeaf(leaf1Cluster[0])
	if want != 1 {
		t.Fatalf("cluster anchor: got %d want 1", want)
	}
	for _, p := range leaf1Cluster[1:] {
		got := bm.PointInLeaf(p)
		if got != want {
			t.Errorf("cluster inconsistency: PointInLeaf(%v) = %d, want %d (anchor result)", p, got, want)
		}
	}

	// And the symmetric back-back-back cluster (x<0, z<0, x+y<0) all
	// lands in leaf 5.
	leaf5Cluster := [][3]float32{
		{-1, -1, -1},
		{-10, -10, -10},
		{-100, -100, -50},
	}
	for _, p := range leaf5Cluster {
		if got := bm.PointInLeaf(p); got != 5 {
			t.Errorf("leaf 5 cluster: PointInLeaf(%v) = %d, want 5", p, got)
		}
	}
}

// TestBrushModel_PointInLeaf_WithFaces validates the single-interior-
// node BSP from synthbsp.BuildWithFaces: front-child is leaf 0 (EMPTY,
// no outside sentinel in this fixture), back-child is leaf 1 (SOLID).
func TestBrushModel_PointInLeaf_WithFaces(t *testing.T) {
	bm := loadSynthWithFaces(t)

	if got := bm.PointInLeaf([3]float32{5, 0, 0}); got != 0 {
		t.Errorf("front-of-plane: got %d want 0", got)
	}
	if got := bm.PointInLeaf([3]float32{-5, 0, 0}); got != 1 {
		t.Errorf("back-of-plane: got %d want 1", got)
	}
}

// TestBrushModel_PointInLeaf_EmptyModel exercises the early "no nodes"
// guard: a hand-built empty BrushModel returns -1 regardless of input.
func TestBrushModel_PointInLeaf_EmptyModel(t *testing.T) {
	bm := &BrushModel{}
	if got := bm.PointInLeaf([3]float32{0, 0, 0}); got != -1 {
		t.Errorf("empty model: got %d want -1", got)
	}
}

// TestBrushModel_PointInLeaf_BadPlaneNum poisons a node's PlaneNum so
// the descent's planes-bounds check fires; PointInLeaf must surface -1
// rather than panic.
func TestBrushModel_PointInLeaf_BadPlaneNum(t *testing.T) {
	bm := loadSynthFiveLeafPVS(t)
	bm.nodes[0].PlaneNum = 9999
	if got := bm.PointInLeaf([3]float32{0, 0, 0}); got != -1 {
		t.Errorf("bad plane num: got %d want -1", got)
	}
	// And the negative side of the same guard.
	bm.nodes[0].PlaneNum = -1
	if got := bm.PointInLeaf([3]float32{0, 0, 0}); got != -1 {
		t.Errorf("negative plane num: got %d want -1", got)
	}
}

// TestBrushModel_PointInLeaf_OutOfRangeNodeChild poisons a node's
// Children to reference an out-of-range node, exercising the in-loop
// bounds check at the next iteration.
func TestBrushModel_PointInLeaf_OutOfRangeNodeChild(t *testing.T) {
	bm := loadSynthFiveLeafPVS(t)
	// Force the front-child to a node id past the slice.
	bm.nodes[0].Children[0] = 99
	if got := bm.PointInLeaf([3]float32{5, 0, 0}); got != -1 {
		t.Errorf("out-of-range node child: got %d want -1", got)
	}
}

// TestBrushModel_PointInLeaf_OutOfRangeLeafChild poisons a node's
// Children to encode a leaf index past the leaves slice, exercising
// the post-descend bounds check.
func TestBrushModel_PointInLeaf_OutOfRangeLeafChild(t *testing.T) {
	bm := loadSynthFiveLeafPVS(t)
	// Override node 1's front child (^1 = leaf 1) with an oversized
	// leaf encoding so a point heading into leaf 1 lands in the guard.
	bm.nodes[1].Children[0] = ^int16(99)
	if got := bm.PointInLeaf([3]float32{5, 5, 0}); got != -1 {
		t.Errorf("out-of-range leaf child: got %d want -1", got)
	}
}

// TestBrushModel_PointInLeaf_PlanesDecodeError corrupts the underlying
// LumpPlanes byte length so File.Planes() returns an error, and
// PointInLeaf must surface -1.
func TestBrushModel_PointInLeaf_PlanesDecodeError(t *testing.T) {
	data, size, err := synthbsp.BuildFiveLeafPVS()
	if err != nil {
		t.Fatalf("synthbsp.BuildFiveLeafPVS: %v", err)
	}
	// Patch the planes lump length in the header to a non-multiple of
	// the on-disk plane record size so the decoder errors. Header
	// layout: int32 version + per-lump (int32 offset, int32 length).
	// LumpPlanes is lump 1; the length field starts at 4 + 1*8 + 4 = 16.
	const planesLumpLenOff = 4 + int(bspfile.LumpPlanes)*8 + 4
	binary.LittleEndian.PutUint32(data[planesLumpLenOff:planesLumpLenOff+4], 1)
	f, err := bspfile.Open(bytes.NewReader(data), size)
	if err != nil {
		t.Fatalf("bspfile.Open: %v", err)
	}
	// LoadBrush also reads planes -- it will error out. Reach into the
	// BrushModel state directly: build a minimal shell with the bad
	// File and a one-node skeleton so PointInLeaf's planes-decode path
	// is the one that trips.
	bm := &BrushModel{
		File:   f,
		nodes:  []Node{{Node: bspfile.Node{PlaneNum: 0, Children: [2]int16{^int16(0), ^int16(1)}}, ParentNode: -1}},
		leaves: []Leaf{{ParentNode: -1}, {ParentNode: -1}},
	}
	if got := bm.PointInLeaf([3]float32{0, 0, 0}); got != -1 {
		t.Errorf("planes decode error: got %d want -1", got)
	}
}

// --- FaceMipTexIdx ---------------------------------------------------------

// FaceMipTexIdx resolves face -> texinfo -> miptex on the BuildWithFaces
// fixture (faces[0,1] TexInfo 0 -> MipTex 0; face 2 TexInfo 1 -> MipTex 1).
func TestBrushModel_FaceMipTexIdx_HappyPath(t *testing.T) {
	bm := loadSynthWithFaces(t)
	cases := []struct {
		faceIdx, wantIdx int
		wantOK           bool
	}{
		{0, 0, true},
		{1, 0, true},
		{2, 1, true},
	}
	for _, tc := range cases {
		idx, ok, err := bm.FaceMipTexIdx(tc.faceIdx)
		if err != nil {
			t.Fatalf("face %d: err %v", tc.faceIdx, err)
		}
		if ok != tc.wantOK || idx != tc.wantIdx {
			t.Errorf("face %d: got (idx=%d, ok=%v) want (idx=%d, ok=%v)", tc.faceIdx, idx, ok, tc.wantIdx, tc.wantOK)
		}
	}
}

// face 3 references TexInfo 99 which is out of range; FaceMipTexIdx
// reports (-1, false, nil) the same shape a missing miptex slot would.
func TestBrushModel_FaceMipTexIdx_TexInfoOutOfRange(t *testing.T) {
	bm := loadSynthWithFaces(t)
	idx, ok, err := bm.FaceMipTexIdx(3)
	if err != nil {
		t.Fatalf("face 3: err %v", err)
	}
	if ok || idx != -1 {
		t.Errorf("face 3 (TexInfo 99): got (idx=%d, ok=%v) want (idx=-1, ok=false)", idx, ok)
	}
}

// Out-of-range face index reports (-1, false, nil) without touching
// the TexInfos lump.
func TestBrushModel_FaceMipTexIdx_FaceOutOfRange(t *testing.T) {
	bm := loadSynthWithFaces(t)
	for _, fi := range []int{-1, 9999} {
		idx, ok, err := bm.FaceMipTexIdx(fi)
		if err != nil || ok || idx != -1 {
			t.Errorf("face %d: got (idx=%d, ok=%v, err=%v) want (-1, false, nil)", fi, idx, ok, err)
		}
	}
}

// Corrupt LumpFaces -> the cached decoder errors and the helper
// propagates the error to the caller without panicking.
func TestBrushModel_FaceMipTexIdx_FacesDecodeError(t *testing.T) {
	data, size, err := synthbsp.BuildWithFaces()
	if err != nil {
		t.Fatalf("BuildWithFaces: %v", err)
	}
	// LumpFaces length -> non-multiple of faceSize (20).
	const facesLumpLenOff = 4 + int(bspfile.LumpFaces)*8 + 4
	binary.LittleEndian.PutUint32(data[facesLumpLenOff:facesLumpLenOff+4], 1)
	f, err := bspfile.Open(bytes.NewReader(data), size)
	if err != nil {
		t.Fatalf("bspfile.Open: %v", err)
	}
	bm := &BrushModel{File: f}
	if _, _, err := bm.FaceMipTexIdx(0); err == nil {
		t.Fatal("expected error from corrupt faces lump")
	}
}

// Corrupt LumpTexInfo -> the texinfo decoder errors after Faces() has
// succeeded; helper surfaces the error.
func TestBrushModel_FaceMipTexIdx_TexInfosDecodeError(t *testing.T) {
	data, size, err := synthbsp.BuildWithFaces()
	if err != nil {
		t.Fatalf("BuildWithFaces: %v", err)
	}
	// LumpTexInfo length -> non-multiple of texInfoSize (40).
	const tiLumpLenOff = 4 + int(bspfile.LumpTexInfo)*8 + 4
	binary.LittleEndian.PutUint32(data[tiLumpLenOff:tiLumpLenOff+4], 1)
	f, err := bspfile.Open(bytes.NewReader(data), size)
	if err != nil {
		t.Fatalf("bspfile.Open: %v", err)
	}
	bm := &BrushModel{File: f}
	if _, _, err := bm.FaceMipTexIdx(0); err == nil {
		t.Fatal("expected error from corrupt texinfo lump")
	}
}

// --- Hostile node tables (found by bspfile's FuzzOpen) ---------------------

// A node whose child points back at the root is not a tree: setParent reports it
// (it used to recurse until the stack overflowed, a fatal error).
func TestBrushModel_SetParentReportsACycle(t *testing.T) {
	bm := &BrushModel{
		nodes:  []Node{{Node: bspfile.Node{Children: [2]int16{1, ^int16(0)}}, ParentNode: -1}, {Node: bspfile.Node{Children: [2]int16{0, ^int16(0)}}, ParentNode: -1}},
		leaves: []Leaf{{ParentNode: -1}},
	}
	if bm.setParent(0, -1) {
		t.Error("a node table with a cycle was walked as a tree")
	}
	// Two parents for one node (a shared subtree) is the same refusal.
	bm = &BrushModel{
		nodes:  []Node{{Node: bspfile.Node{Children: [2]int16{1, 1}}, ParentNode: -1}, {Node: bspfile.Node{Children: [2]int16{^int16(0), ^int16(0)}}, ParentNode: -1}},
		leaves: []Leaf{{ParentNode: -1}},
	}
	if bm.setParent(0, -1) {
		t.Error("a node reached twice was walked as a tree")
	}
}

// LoadBrush refuses a map whose node table has a cycle, and PointInLeaf answers
// -1 for one rather than looping.
func TestLoadBrush_RefusesACycleInTheNodeTable(t *testing.T) {
	data, size, err := synthbsp.BuildFiveLeafPVS()
	if err != nil {
		t.Fatal(err)
	}
	f, err := bspfile.Open(bytes.NewReader(data), size)
	if err != nil {
		t.Fatal(err)
	}
	// Node 1's first child (a leaf) becomes node 0, the root: bsp29_dnode_t is 24
	// bytes, the two int16 children at offset 4.
	nodes := f.LumpBytes(bspfile.LumpNodes)
	binary.LittleEndian.PutUint16(nodes[24+4:], 0)
	if _, err := LoadBrush(f, 0); !errors.Is(err, ErrNodeCycle) {
		t.Fatalf("LoadBrush = %v, want ErrNodeCycle", err)
	}

	// PointInLeaf on a model whose nodes loop: the fixture's root sends every
	// point to node 1, which sends it back to the root.
	bm := loadSynthFiveLeafPVS(t)
	bm.nodes[0].Children = [2]int16{1, 1}
	bm.nodes[1].Children = [2]int16{0, 0}
	if got := bm.PointInLeaf([3]float32{1, 2, 3}); got != -1 {
		t.Errorf("PointInLeaf in a loop = %d, want -1", got)
	}
}
