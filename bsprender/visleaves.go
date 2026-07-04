// Copyright (c) 1996-1997 Id Software, Inc.
// Copyright (c) 2026 the go-quake1/engine authors.
// SPDX-License-Identifier: GPL-2.0-or-later

package bsprender

import "errors"

// FrameMarkSequence is the per-frame counter used to tag visible
// leaves/nodes. The renderer increments this once per frame before
// the PVS walk; the subsequent BSP traversal then skips any
// leaf/node whose visframe != current. Caller maintains the counter
// (typically the Host frame counter). tyrquake: r_visframecount.
type FrameMarkSequence int32

// VisLeafIdx is the BSP-leaf index (1..numLeaves; index 0 is the
// "outside" leaf and never receives a PVS mark). tyrquake uses the
// same 1-based indexing for PVS lookups -- leaf 0 is the solid /
// outside-the-map sentinel.
type VisLeafIdx int

// Errors returned by the vis primitives.
var (
	ErrVisNilModel    = errors.New("render: nil world model in vis op")
	ErrVisLeafRange   = errors.New("render: viewer leaf index out of range")
	ErrVisPVSTooShort = errors.New("render: PVS RLE bytes exhausted before all leaves marked")
)

// DecompressVis decodes one RLE-encoded PVS row back into a bit
// array. Each set bit at position i (bit `1 << (i % 8)` of byte
// `out[i / 8]`) means leaf (i+1) is visible from the source leaf.
//
// Encoding (vanilla Quake / tyrquake Mod_DecompressVis):
//
//   - a non-zero byte is emitted verbatim into the output stream
//   - a zero byte is followed by a run-length byte N; N zero bytes
//     are emitted into the output stream
//
// The decoded array is (numLeaves+7)/8 bytes long. Upstream
// allocates a single static 256-byte work buffer; the Go port
// allocates per call (callers that need to pool can wrap this).
//
// Behaviour for special cases:
//
//   - numLeaves <= 0: returns an empty slice and no error (a model
//     with zero leaves has no PVS to decompress).
//   - len(compressed) == 0 with numLeaves > 0, or compressed runs out
//     mid-row: returns the DECODED PREFIX (remaining bytes left zero) +
//     ErrVisPVSTooShort. Real Quake maps truncate a trailing run of
//     not-visible far leaves, so callers that treat the zeroed tail as
//     "not visible" (see [MarkVisibleLeaves]) render correctly rather
//     than failing; the error is retained so stricter callers can still
//     detect the short row.
//
// tyrquake's leafblock_t variant of this routine performs the same
// RLE but accumulates into machine-word-sized leaf blocks with a
// shift accumulator; the byte-stream output here is equivalent for
// little-endian targets and is what callers consume bit-by-bit.
func DecompressVis(compressed []byte, numLeaves int) ([]byte, error) {
	if numLeaves <= 0 {
		return []byte{}, nil
	}
	outLen := (numLeaves + 7) >> 3
	out := make([]byte, outLen)
	inPos := 0
	outPos := 0
	for outPos < outLen {
		if inPos >= len(compressed) {
			return out, ErrVisPVSTooShort
		}
		b := compressed[inPos]
		inPos++
		if b != 0 {
			out[outPos] = b
			outPos++
			continue
		}
		// Zero byte -> next byte is a run length of zeros.
		if inPos >= len(compressed) {
			return out, ErrVisPVSTooShort
		}
		count := int(compressed[inPos])
		inPos++
		// Emit `count` zero bytes, but never overrun the output.
		// tyrquake lets the count run past the row end (the static
		// scratch buffer is sized so this is safe); the Go version
		// stops at outLen to keep the slice bounds clean.
		end := outPos + count
		if end > outLen {
			end = outLen
		}
		// out is already zero-initialised; just advance the cursor.
		outPos = end
	}
	return out, nil
}

// MarkContext bundles the per-model callbacks [MarkVisibleLeaves]
// needs. Keeping it as plain closures lets this package stay
// independent of engine/model's eventual mleaf_t / mnode_t shape and
// makes synthetic-BSP testing trivial.
//
// Field contract:
//
//   - NumLeaves: total leaves in the model. Leaf indices 1..NumLeaves
//     are valid (index 0 is the outside-the-map sentinel).
//
//   - PVSForLeaf: returns the compressed PVS row for the given leaf.
//     If a leaf has no vis info (BSP encodes that as VisOfs == -1),
//     the caller is responsible for synthesising an "all visible"
//     row beforehand; this package treats whatever bytes it gets
//     as the canonical PVS for that leaf.
//
//   - LeafParentNode: returns the parent-node index for a leaf. The
//     parent chain is then walked via NodeParent.
//
//   - NodeParent: returns the parent-node index, or a negative value
//     to indicate "this node is the root" (the walk stops).
//
//   - GetNodeVisFrame: returns the current vis-frame stamp on a
//     node; the walk stops at the first ancestor already marked at
//     this frame (matches tyrquake's `if (node->visframe == r_visframecount) break;`).
//
//   - SetLeafVisFrame / SetNodeVisFrame: writes the per-frame mark
//     to the leaf or node respectively.
type MarkContext struct {
	NumLeaves       int
	PVSForLeaf      func(leafIdx VisLeafIdx) []byte
	LeafParentNode  func(leafIdx VisLeafIdx) int
	NodeParent      func(nodeIdx int) int
	GetNodeVisFrame func(nodeIdx int) FrameMarkSequence
	SetLeafVisFrame func(leafIdx VisLeafIdx, frame FrameMarkSequence)
	SetNodeVisFrame func(nodeIdx int, frame FrameMarkSequence)
}

// MarkVisibleLeaves walks the PVS for viewerLeaf and writes frame
// into every visible leaf's VisFrame field; for each marked leaf it
// then walks up the parent-node chain stamping each ancestor, until
// it reaches the root or hits a node already marked at this frame.
// tyrquake: R_MarkLeaves (one frame's worth of work).
//
// Behaviour notes:
//
//   - The viewer leaf is itself marked only if the PVS row says so;
//     vanilla Quake's vis tool DOES include the source leaf in its
//     own row, but the spec test exercises a synthetic row that
//     doesn't -- this function just trusts the PVS bits it's given.
//
//   - A leaf bit corresponding to an index > NumLeaves is ignored
//     (the PVS bit array is byte-padded so trailing bits are
//     undefined; tyrquake masks them via num_out vs numleafs).
//
//   - Parent-chain walking matches tyrquake exactly: descend from
//     leaf to leaf.parent, then node.parent, node.parent... breaking
//     when the parent is the sentinel (NodeParent returns < 0) or
//     already carries this frame's mark.
//
// Returns:
//
//   - ErrVisNilModel if NumLeaves <= 0 (no world model loaded).
//   - ErrVisLeafRange if viewerLeaf < 1 or viewerLeaf > NumLeaves.
//   - A short/truncated PVS row is NOT an error: the decoded prefix is
//     marked and the missing tail is treated as not visible.
func MarkVisibleLeaves(ctx MarkContext, viewerLeaf VisLeafIdx, frame FrameMarkSequence) error {
	if ctx.NumLeaves <= 0 {
		return ErrVisNilModel
	}
	if int(viewerLeaf) < 1 || int(viewerLeaf) > ctx.NumLeaves {
		return ErrVisLeafRange
	}
	compressed := ctx.PVSForLeaf(viewerLeaf)
	// DecompressVis's only failure is ErrVisPVSTooShort -- a PVS row that
	// ends before the last leaf. Real Quake maps truncate a trailing run of
	// not-visible far leaves (vanilla Mod_DecompressVis just reads past the
	// row into masked-off bytes); DecompressVis returns the decoded prefix
	// with the remainder zeroed, so those far leaves are treated as not
	// visible. Render the prefix rather than failing the frame -- the error
	// is intentionally ignored.
	bits, _ := DecompressVis(compressed, ctx.NumLeaves)
	for i := 0; i < ctx.NumLeaves; i++ {
		if bits[i>>3]&(1<<uint(i&7)) == 0 {
			continue
		}
		// Bit i set -> leaf (i+1) is visible.
		leafIdx := VisLeafIdx(i + 1)
		ctx.SetLeafVisFrame(leafIdx, frame)
		// Walk up the parent chain, stamping each node until we
		// reach the root sentinel or hit an already-marked node.
		node := ctx.LeafParentNode(leafIdx)
		for node >= 0 {
			if ctx.GetNodeVisFrame(node) == frame {
				break
			}
			ctx.SetNodeVisFrame(node, frame)
			node = ctx.NodeParent(node)
		}
	}
	return nil
}
