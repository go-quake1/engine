// Copyright (c) 1996-1997 Id Software, Inc.
// Copyright (c) 2026 the go-quake1/engine authors.
// SPDX-License-Identifier: GPL-2.0-or-later

package model

import (
	"errors"

	"github.com/go-quake1/engine/bspfile"
)

// Leaf is the in-memory wrapper around a [bspfile.Leaf] decoded from
// the LumpLeafs lump. It adds the two runtime fields the renderer's
// per-frame PVS walk writes to:
//
//   - VisFrame: the frame-counter stamp written by
//     [github.com/go-quake1/engine/bsprender.MarkVisibleLeaves] when
//     the leaf is in the viewer's PVS. Subsequent BSP traversal skips
//     leaves whose VisFrame != current frame.
//
//   - ParentNode: the index into BrushModel.nodes of the leaf's
//     immediate parent node (the one whose plane sliced this leaf off
//     the BSP tree). Populated once at load time by the recursive
//     setParent walk; the renderer marches up this chain stamping each
//     ancestor node's VisFrame. -1 means "no parent" (only possible
//     for the outside-the-map sentinel leaf 0 of an empty tree).
//
// tyrquake: mleaf_t with the same visframe + parent fields embedded
// next to the raw lump data.
type Leaf struct {
	bspfile.Leaf
	VisFrame   int32
	ParentNode int
}

// Node is the in-memory wrapper around a [bspfile.Node] decoded from
// the LumpNodes lump. Carries the same runtime additions as [Leaf]:
//
//   - VisFrame: per-frame visibility stamp.
//   - ParentNode: index into BrushModel.nodes of this node's parent,
//     or -1 for the root.
//
// tyrquake: mnode_t.
type Node struct {
	bspfile.Node
	VisFrame   int32
	ParentNode int
}

// NumLeaves returns the count of PVS-trackable leaves (i.e. raw-leaf
// count minus 1 for the outside-the-map sentinel at index 0). This is
// the value the renderer feeds to
// [github.com/go-quake1/engine/bsprender.MarkContext.NumLeaves]:
// PVS bit `i` (0 <= i < NumLeaves) refers to [Leaf] index `i + 1`.
// An empty or single-leaf model returns 0 (no PVS work to do).
func (bm *BrushModel) NumLeaves() int {
	if len(bm.leaves) <= 1 {
		return 0
	}
	return len(bm.leaves) - 1
}

// TotalLeaves returns the count of raw leaves including the outside-
// the-map sentinel at index 0. Used by the BSP-walk wiring to size
// the unified node+leaf id space (the recurse package addresses nodes
// at id 0..NumNodes-1 and leaves at id NumNodes..NumNodes+TotalLeaves-1).
// An empty model returns 0.
func (bm *BrushModel) TotalLeaves() int { return len(bm.leaves) }

// NumNodes returns the number of interior BSP nodes. The recurse
// package's [github.com/go-quake1/engine/bsprender.WalkContext.NumNodes]
// reads this. An empty model returns 0.
func (bm *BrushModel) NumNodes() int { return len(bm.nodes) }

// Leaf returns a pointer to the leaf at the 0-based index i (so
// callers can write through to VisFrame). Index 0 is the outside-the-
// map sentinel; PVS-shaped methods take 1-based indices instead.
// Panics if i is out of [0, len(bm.leaves)).
func (bm *BrushModel) Leaf(i int) *Leaf { return &bm.leaves[i] }

// Node returns a pointer to the node at the 0-based index i. Panics
// if i is out of [0, len(bm.nodes)).
func (bm *BrushModel) Node(i int) *Node { return &bm.nodes[i] }

// LeafFaceIndices returns the face indices linked to leaf i via the
// LumpMarksurfaces span. The returned slice is freshly allocated; the
// caller owns it.
//
// Resolution: the leaf carries (FirstMarkSurface, NumMarkSurfaces) into
// the MarkSurfaces lump; each entry is a uint16 face index. tyrquake:
// the marksurfaces[] slice mleaf_t.firstmarksurface points at.
//
// Returns:
//
//   - empty slice if i is out of range, NumMarkSurfaces == 0, or the
//     model carries no LumpMarksurfaces (synthetic BSPs in tests).
//   - the propagated error if MarkSurfaces lump decoding fails.
func (bm *BrushModel) LeafFaceIndices(i int) ([]int, error) {
	if i < 0 || i >= len(bm.leaves) {
		return nil, nil
	}
	leaf := &bm.leaves[i]
	if leaf.NumMarkSurfaces == 0 {
		return nil, nil
	}
	marks, err := bm.File.MarkSurfaces()
	if err != nil {
		return nil, err
	}
	first := int(leaf.FirstMarkSurface)
	n := int(leaf.NumMarkSurfaces)
	if first < 0 || first+n > len(marks) {
		return nil, nil
	}
	out := make([]int, n)
	for j := 0; j < n; j++ {
		out[j] = int(marks[first+j])
	}
	return out, nil
}

// PVSForLeaf returns the compressed PVS row for the 1-based leaf
// index i, as a slice over the BSP file's LumpVisibility blob.
//
// Semantics match tyrquake's `visdata + leaf->visofs`:
//
//   - If the leaf's VisOfs is -1 (no vis info), an all-0xFF byte slice
//     sized for [BrushModel.NumLeaves] is returned, so the decoder
//     marks every leaf visible. tyrquake: the
//     `if (!in) memset(out, 0xff, ...)` short-circuit in
//     Mod_DecompressVis.
//
//   - Otherwise the returned slice starts at `visdata + VisOfs` and
//     runs to the end of the visibility lump. The RLE decoder stops
//     when it has emitted enough output bytes, so a slice trailing
//     into the next leaf's row (or off the end) is fine.
//
// Out-of-range i (i < 1 or i > NumLeaves) returns nil so callers can
// surface ErrVisLeafRange themselves; this method is intentionally
// non-panicking because the renderer treats "missing PVS" as a soft
// failure (re-use the previous frame's leaves).
func (bm *BrushModel) PVSForLeaf(i int) []byte {
	if i < 1 || i >= len(bm.leaves) {
		return nil
	}
	leaf := &bm.leaves[i]
	visdata := bm.File.Visibility()
	if leaf.VisOfs < 0 || int(leaf.VisOfs) >= len(visdata) {
		// "no vis" -> all visible. The decoder reads bytes up to one
		// per leaf in the worst case, plus the run-length encoding
		// can swallow groups of 8 in two bytes, so we hand it a fully-
		// expanded all-1s row. Size matches what NumLeaves implies
		// (one bit per PVS-trackable leaf, rounded up to a byte).
		n := bm.NumLeaves()
		row := make([]byte, (n+7)>>3)
		for j := range row {
			row[j] = 0xFF
		}
		return row
	}
	return visdata[leaf.VisOfs:]
}

// SetLeafVisFrame writes the per-frame stamp to the 1-based leaf
// index i. tyrquake: `leaf->visframe = r_visframecount`.
func (bm *BrushModel) SetLeafVisFrame(i int, frame int32) {
	bm.leaves[i].VisFrame = frame
}

// SetNodeVisFrame writes the per-frame stamp to node index i.
// tyrquake: `node->visframe = r_visframecount`.
func (bm *BrushModel) SetNodeVisFrame(i int, frame int32) {
	bm.nodes[i].VisFrame = frame
}

// GetNodeVisFrame reads the per-frame stamp on node i. The
// MarkVisibleLeaves walk uses this to short-circuit the parent chain
// when an ancestor was already stamped at this frame.
func (bm *BrushModel) GetNodeVisFrame(i int) int32 {
	return bm.nodes[i].VisFrame
}

// LeafParentNode returns the parent-node index of the 1-based leaf i.
// Used by the renderer to start the leaf -> root upward stamping walk.
// A return of -1 means the leaf has no parent in the tree (the empty-
// model edge case).
func (bm *BrushModel) LeafParentNode(i int) int {
	return bm.leaves[i].ParentNode
}

// NodeParent returns the parent-node index of node i, or -1 for the
// root. The renderer walks up the chain until it hits -1 or a node
// already stamped at the live frame.
func (bm *BrushModel) NodeParent(i int) int {
	return bm.nodes[i].ParentNode
}

// FaceMipTexIdx returns the LUMP_TEXTURES miptex index referenced by
// the face at faceIdx, resolved through the face's TexInfo entry.
//
// Returns (-1, false, nil) when:
//
//   - faceIdx is out of [0, len(Faces)).
//   - the face's TexInfo index is out of [0, len(TexInfos)) (corrupt
//     BSP -- treated as "no texture", same shape as the renderer's
//     missing-miptex fallback path).
//
// Returns the propagated error when the Faces or TexInfos lump fails
// to decode. The decoded lumps are cached on the parent *bspfile.File
// so per-frame calls don't re-decode.
//
// Used by the renderer's WAD/miptex bridge to pick the right pixel
// buffer for each emitted SurfaceRef without round-tripping through
// bsprender.NewBrushFaceVerts (which builds a full FaceVerts bundle).
func (bm *BrushModel) FaceMipTexIdx(faceIdx int) (int, bool, error) {
	faces, err := bm.File.Faces()
	if err != nil {
		return -1, false, err
	}
	if faceIdx < 0 || faceIdx >= len(faces) {
		return -1, false, nil
	}
	ti := int(faces[faceIdx].TexInfo)
	tis, err := bm.File.TexInfos()
	if err != nil {
		return -1, false, err
	}
	if ti < 0 || ti >= len(tis) {
		return -1, false, nil
	}
	return int(tis[ti].MipTex), true, nil
}

// PointInLeaf descends the BSP from the root (node 0), using the
// splitting plane at each interior node to pick the front (Children[0])
// or back (Children[1]) child, until it reaches a leaf reference.
// Returns the 1-based leaf index (matching the
// [github.com/go-quake1/engine/bsprender.VisLeafIdx] convention --
// leaf 0 is the outside-the-map sentinel a valid in-map point never
// lands on).
//
// The per-node side test is `dot(point, plane.Normal) - plane.Dist`:
// strictly positive -> front child; otherwise back child. This matches
// tyrquake's Mod_PointInLeaf, which uses the same `> 0` short-circuit
// (the on-plane case falls through to the back child). Inlining the
// arithmetic here keeps this package import-cycle-free; the engine's
// render package owns the equivalent [render.PlaneSide] helper for
// other consumers.
//
// Returns -1 (defensive) if the descent escapes the BSP, i.e.:
//
//   - the model has no nodes (empty BSP)
//   - an interior node's PlaneNum is out of range vs the planes lump
//   - the planes lump fails to decode (shouldn't happen for a
//     [BrushModel] produced by [LoadBrush], which already decoded the
//     planes once, but the check keeps the method panic-free under
//     exotic input)
//   - a child reference decodes to a leaf index outside the leaves
//     slice
//
// tyrquake: Mod_PointInLeaf.
func (bm *BrushModel) PointInLeaf(point [3]float32) int {
	if len(bm.nodes) == 0 {
		return -1
	}
	planes, err := bm.File.Planes()
	if err != nil {
		return -1
	}
	nodeIdx := 0
	// A walk down a tree visits each node at most once: more steps than nodes is a
	// cycle in a hostile node table, answered as "no leaf" rather than looping.
	for steps := 0; steps <= len(bm.nodes); steps++ {
		if nodeIdx < 0 || nodeIdx >= len(bm.nodes) {
			return -1
		}
		n := &bm.nodes[nodeIdx]
		pn := int(n.PlaneNum)
		if pn < 0 || pn >= len(planes) {
			return -1
		}
		p := &planes[pn]
		d := p.Normal[0]*point[0] + p.Normal[1]*point[1] + p.Normal[2]*point[2] - p.Dist
		var raw int16
		if d > 0 {
			raw = n.Children[0]
		} else {
			raw = n.Children[1]
		}
		if raw < 0 {
			leafIdx := int(^raw)
			if leafIdx < 0 || leafIdx >= len(bm.leaves) {
				return -1
			}
			return leafIdx
		}
		nodeIdx = int(raw)
	}
	return -1
}

// setParent walks the BSP tree rooted at node index nodeIdx,
// recursively populating each node + leaf's ParentNode field with
// parentIdx (the immediate parent index, or -1 for the root). Called
// exactly once by [LoadBrush] after the leaves + nodes wrappers are
// built. tyrquake: Mod_SetParent.
//
// The walk decodes bspfile.Node.Children with the same negative-NOT
// trick LoadBrush already uses for the draw hull: child >= 0 is a
// node index; child < 0 means leaf index ^child. Out-of-range
// references are silently skipped — LoadBrush's makeDrawHull has
// already validated the same node array, so the only way to reach
// this code with a bad reference is via a brush submodel that points
// at the same nodes array via a different headnode; the bounds checks
// here keep the walk panic-free under that case without making
// LoadBrush re-validate the lump.
//
// It reports false when a node is reached a second time from the root: a node
// table whose children point back up the tree (or share a subtree) is not a BSP
// tree, and the walk used to recurse until the stack overflowed on it (a fatal
// error, not a panic; found by FuzzOpen). LoadBrush refuses such a map.
func (bm *BrushModel) setParent(nodeIdx, parentIdx int) bool {
	return bm.setParentSeen(nodeIdx, parentIdx, make([]bool, len(bm.nodes)))
}

func (bm *BrushModel) setParentSeen(nodeIdx, parentIdx int, seen []bool) bool {
	if nodeIdx < 0 || nodeIdx >= len(bm.nodes) {
		return true
	}
	if seen[nodeIdx] {
		return false
	}
	seen[nodeIdx] = true
	bm.nodes[nodeIdx].ParentNode = parentIdx
	for _, raw := range bm.nodes[nodeIdx].Children {
		if raw >= 0 {
			if !bm.setParentSeen(int(raw), nodeIdx, seen) {
				return false
			}
			continue
		}
		leafIdx := int(^raw)
		if leafIdx >= 0 && leafIdx < len(bm.leaves) {
			bm.leaves[leafIdx].ParentNode = nodeIdx
		}
	}
	return true
}

// ErrNodeCycle is returned by LoadBrush for a node table that is not a tree: a
// node reachable twice from the root.
var ErrNodeCycle = errors.New("model: node table is not a tree (a node is reached twice)")
