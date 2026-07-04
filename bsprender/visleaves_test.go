// Copyright (c) 1996-1997 Id Software, Inc.
// Copyright (c) 2026 the go-quake1/engine authors.
// SPDX-License-Identifier: GPL-2.0-or-later

package bsprender

import (
	"bytes"
	"errors"
	"testing"
)

// ---------------------------------------------------------------------------
// DecompressVis
// ---------------------------------------------------------------------------

func TestDecompressVis_BasicRLE(t *testing.T) {
	// [0xFF, 0x00, 0x05, 0xAA] -> first byte verbatim, then 5 zero
	// bytes, then 0xAA. 7 bytes total -> 56 leaf bits.
	in := []byte{0xFF, 0x00, 0x05, 0xAA}
	out, err := DecompressVis(in, 56)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := []byte{0xFF, 0x00, 0x00, 0x00, 0x00, 0x00, 0xAA}
	if !bytes.Equal(out, want) {
		t.Fatalf("decompressed = %#v, want %#v", out, want)
	}
}

func TestDecompressVis_RoundsUpToByte(t *testing.T) {
	// numLeaves not a multiple of 8 -- output length is the ceiling
	// in bytes.
	in := []byte{0x01}
	out, err := DecompressVis(in, 5)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(out) != 1 || out[0] != 0x01 {
		t.Fatalf("got %#v, want [0x01]", out)
	}
}

func TestDecompressVis_RunPastEndIsClamped(t *testing.T) {
	// A zero-run longer than the remaining output bytes must not
	// panic; tyrquake relies on the static scratch buffer being
	// oversized -- the Go port clamps cleanly to outLen.
	in := []byte{0x00, 0xFF}          // 255 zero bytes requested
	out, err := DecompressVis(in, 16) // only 2 output bytes
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(out) != 2 || out[0] != 0 || out[1] != 0 {
		t.Fatalf("got %#v, want [0 0]", out)
	}
}

func TestDecompressVis_ExhaustedMidRow(t *testing.T) {
	// Input runs out before all leaves are emitted.
	_, err := DecompressVis([]byte{0x01}, 16)
	if !errors.Is(err, ErrVisPVSTooShort) {
		t.Fatalf("err = %v, want ErrVisPVSTooShort", err)
	}
}

func TestDecompressVis_EmptyInput_NonZeroLeaves(t *testing.T) {
	_, err := DecompressVis(nil, 8)
	if !errors.Is(err, ErrVisPVSTooShort) {
		t.Fatalf("err = %v, want ErrVisPVSTooShort", err)
	}
}

func TestDecompressVis_ExhaustedBeforeRunLengthByte(t *testing.T) {
	// A zero byte is the last byte -- the follow-up run-length byte
	// is missing.
	_, err := DecompressVis([]byte{0x00}, 8)
	if !errors.Is(err, ErrVisPVSTooShort) {
		t.Fatalf("err = %v, want ErrVisPVSTooShort", err)
	}
}

func TestDecompressVis_ZeroLeaves(t *testing.T) {
	out, err := DecompressVis(nil, 0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(out) != 0 {
		t.Fatalf("got %#v, want empty", out)
	}
}

func TestDecompressVis_NegativeLeavesTreatedAsZero(t *testing.T) {
	out, err := DecompressVis([]byte{0xFF}, -3)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(out) != 0 {
		t.Fatalf("got %#v, want empty", out)
	}
}

// ---------------------------------------------------------------------------
// MarkVisibleLeaves
// ---------------------------------------------------------------------------

// fakeWorld is a synthetic BSP for the MarkVisibleLeaves tests. It
// owns N leaves and a flat slice of nodes, each carrying a parent
// pointer and a visframe stamp.
type fakeWorld struct {
	numLeaves  int
	pvsRow     []byte
	leafParent []int               // leafParent[leafIdx] (1-based)
	nodeParent []int               // nodeParent[nodeIdx]
	nodeFrame  []FrameMarkSequence // current vis-frame per node
	leafFrame  []FrameMarkSequence // current vis-frame per leaf (1-based, slot 0 unused)
	leafCalls  []VisLeafIdx        // ordered SetLeafVisFrame log
	nodeCalls  []int               // ordered SetNodeVisFrame log
}

func (w *fakeWorld) ctx() MarkContext {
	return MarkContext{
		NumLeaves:  w.numLeaves,
		PVSForLeaf: func(VisLeafIdx) []byte { return w.pvsRow },
		LeafParentNode: func(l VisLeafIdx) int {
			return w.leafParent[int(l)]
		},
		NodeParent: func(n int) int { return w.nodeParent[n] },
		GetNodeVisFrame: func(n int) FrameMarkSequence {
			return w.nodeFrame[n]
		},
		SetLeafVisFrame: func(l VisLeafIdx, f FrameMarkSequence) {
			w.leafFrame[int(l)] = f
			w.leafCalls = append(w.leafCalls, l)
		},
		SetNodeVisFrame: func(n int, f FrameMarkSequence) {
			w.nodeFrame[n] = f
			w.nodeCalls = append(w.nodeCalls, n)
		},
	}
}

// makeFakeWorld builds a 5-leaf world with this BSP tree:
//
//	        node 0  (root)
//	       /        \
//	   node 1      node 2
//	   /   \        /   \
//	leaf1 leaf2  leaf3  node 3
//	                    /   \
//	                 leaf4  leaf5
//
// leafParent[l] is the immediate node parent of leaf l (1-based).
// nodeParent[n] is the parent of node n, with -1 for the root.
func makeFakeWorld(pvs []byte) *fakeWorld {
	return &fakeWorld{
		numLeaves:  5,
		pvsRow:     pvs,
		leafParent: []int{-1, 1, 1, 2, 3, 3}, // slot 0 unused
		nodeParent: []int{-1, 0, 0, 2},
		nodeFrame:  make([]FrameMarkSequence, 4),
		leafFrame:  make([]FrameMarkSequence, 6),
	}
}

// pvsBitsOnly turns a list of 1-based leaf indices into a raw PVS
// byte row (no RLE compression). DecompressVis treats non-zero bytes
// as verbatim emission, so as long as every byte is non-zero we get
// the bit pattern through unchanged. To stay safe we just emit a
// hand-rolled bitmap where every output byte has at least one bit
// set OR -- when a whole byte would be zero -- use a 0x00 / 0x00
// run (which would expand). To keep tests simple we encode 5 leaves
// in a single byte (no zeros possible to confuse the RLE).
func pvsBitsOnly(visible ...VisLeafIdx) []byte {
	row := []byte{0}
	for _, v := range visible {
		i := int(v) - 1
		row[i>>3] |= 1 << uint(i&7)
	}
	if row[0] == 0 {
		// All-zero byte would be interpreted as a zero-run marker
		// by DecompressVis. Encode as RLE: 0x00 followed by 0x01
		// (one zero byte).
		return []byte{0x00, 0x01}
	}
	return row
}

func TestMarkVisibleLeaves_MarksOnlyPVSBits(t *testing.T) {
	// Spec test: viewerLeaf = 2, PVS says leaves 1 and 3 are
	// visible. SetLeafVisFrame must be called for 1 and 3, NOT for
	// 2 itself (the synthetic PVS row excludes the viewer leaf).
	w := makeFakeWorld(pvsBitsOnly(1, 3))
	if err := MarkVisibleLeaves(w.ctx(), 2, 42); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	wantLeaves := []VisLeafIdx{1, 3}
	if !equalLeafCalls(w.leafCalls, wantLeaves) {
		t.Fatalf("leaf calls = %v, want %v", w.leafCalls, wantLeaves)
	}
	if w.leafFrame[2] != 0 {
		t.Fatalf("viewer leaf 2 should not be marked, got frame=%d", w.leafFrame[2])
	}
	if w.leafFrame[1] != 42 || w.leafFrame[3] != 42 {
		t.Fatalf("expected leaves 1,3 frame=42, got %d,%d", w.leafFrame[1], w.leafFrame[3])
	}
}

func TestMarkVisibleLeaves_WalksParentChainToRoot(t *testing.T) {
	// Only leaf 4 visible. Its parent chain is node 3 -> node 2 ->
	// node 0; all three should receive the mark, in that order.
	w := makeFakeWorld(pvsBitsOnly(4))
	if err := MarkVisibleLeaves(w.ctx(), 1, 7); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	wantNodes := []int{3, 2, 0}
	if len(w.nodeCalls) != len(wantNodes) {
		t.Fatalf("node calls = %v, want %v", w.nodeCalls, wantNodes)
	}
	for i, n := range wantNodes {
		if w.nodeCalls[i] != n {
			t.Fatalf("node calls = %v, want %v", w.nodeCalls, wantNodes)
		}
	}
	for _, n := range wantNodes {
		if w.nodeFrame[n] != 7 {
			t.Fatalf("node %d frame = %d, want 7", n, w.nodeFrame[n])
		}
	}
	// Untouched node 1 must remain unmarked.
	if w.nodeFrame[1] != 0 {
		t.Fatalf("node 1 should be untouched, got %d", w.nodeFrame[1])
	}
}

func TestMarkVisibleLeaves_EarlyStopWhenParentAlreadyMarked(t *testing.T) {
	// Two visible leaves under disjoint subtrees: leaf 2 (parent
	// node 1) and leaf 5 (parent node 3). Walking leaf 2 first
	// marks nodes 1 and 0. Walking leaf 5 then marks node 3, and
	// when it walks up to node 2's parent (node 0) it must
	// short-circuit because node 0 already carries this frame's
	// stamp.
	w := makeFakeWorld(pvsBitsOnly(2, 5))
	if err := MarkVisibleLeaves(w.ctx(), 1, 9); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// node 0 must appear EXACTLY once in the SetNodeVisFrame log.
	count := 0
	for _, n := range w.nodeCalls {
		if n == 0 {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("node 0 set %d times, want 1 (calls=%v)", count, w.nodeCalls)
	}
}

func TestMarkVisibleLeaves_IgnoresPaddingBits(t *testing.T) {
	// A 5-leaf model packs into 1 byte (3 bits are padding). If the
	// PVS row has the padding bits set, they must NOT trigger
	// SetLeafVisFrame for non-existent leaves 6/7/8.
	row := []byte{0xFF} // all 8 bits set
	w := &fakeWorld{
		numLeaves:  5,
		pvsRow:     row,
		leafParent: []int{-1, 1, 1, 2, 3, 3},
		nodeParent: []int{-1, 0, 0, 2},
		nodeFrame:  make([]FrameMarkSequence, 4),
		leafFrame:  make([]FrameMarkSequence, 6),
	}
	if err := MarkVisibleLeaves(w.ctx(), 1, 5); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(w.leafCalls) != 5 {
		t.Fatalf("leaf calls = %v, want exactly 5 (padding bits must be ignored)", w.leafCalls)
	}
}

func TestMarkVisibleLeaves_ErrNilModel(t *testing.T) {
	if err := MarkVisibleLeaves(MarkContext{}, 1, 0); !errors.Is(err, ErrVisNilModel) {
		t.Fatalf("err = %v, want ErrVisNilModel", err)
	}
}

func TestMarkVisibleLeaves_ErrLeafRange(t *testing.T) {
	w := makeFakeWorld(pvsBitsOnly(1))
	if err := MarkVisibleLeaves(w.ctx(), 0, 1); !errors.Is(err, ErrVisLeafRange) {
		t.Fatalf("viewerLeaf=0: err = %v, want ErrVisLeafRange", err)
	}
	if err := MarkVisibleLeaves(w.ctx(), 6, 1); !errors.Is(err, ErrVisLeafRange) {
		t.Fatalf("viewerLeaf=N+1: err = %v, want ErrVisLeafRange", err)
	}
}

func TestMarkVisibleLeaves_ToleratesShortPVS(t *testing.T) {
	// A truncated PVS row (real maps omit a trailing run of not-visible far
	// leaves) must NOT fail the frame: MarkVisibleLeaves marks the decoded
	// prefix and leaves the missing tail unmarked.
	w := &fakeWorld{
		numLeaves:  16,
		pvsRow:     []byte{0x01}, // 1 byte for 16 leaves: bit0 (leaf 1) set, tail missing
		leafParent: make([]int, 17),
		nodeParent: []int{-1},
		nodeFrame:  make([]FrameMarkSequence, 1),
		leafFrame:  make([]FrameMarkSequence, 17),
	}
	for i := range w.leafParent {
		w.leafParent[i] = 0
	}
	if err := MarkVisibleLeaves(w.ctx(), 1, 1); err != nil {
		t.Fatalf("err = %v, want nil (short PVS tolerated)", err)
	}
	if w.leafFrame[1] != 1 {
		t.Fatalf("leaf 1 (decoded prefix bit) not marked: %v", w.leafFrame[1])
	}
	if w.leafFrame[2] != 0 {
		t.Fatalf("leaf 2 (missing tail) should stay unmarked: %v", w.leafFrame[2])
	}
}

func TestMarkVisibleLeaves_LeafWithNoParent(t *testing.T) {
	// A leaf whose LeafParentNode returns -1 (no parent) must not
	// crash and must not write any node frames.
	w := &fakeWorld{
		numLeaves:  1,
		pvsRow:     []byte{0x01},
		leafParent: []int{-1, -1},
		nodeParent: []int{},
		nodeFrame:  []FrameMarkSequence{},
		leafFrame:  make([]FrameMarkSequence, 2),
	}
	if err := MarkVisibleLeaves(w.ctx(), 1, 3); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(w.nodeCalls) != 0 {
		t.Fatalf("expected no node marks, got %v", w.nodeCalls)
	}
	if w.leafFrame[1] != 3 {
		t.Fatalf("leaf 1 frame = %d, want 3", w.leafFrame[1])
	}
}

// equalLeafCalls reports whether got and want hold the same VisLeafIdx
// values in order.
func equalLeafCalls(got, want []VisLeafIdx) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}
