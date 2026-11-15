// Copyright (c) 1996-1997 Id Software, Inc.
// Copyright (c) 2026 the go-quake1/engine authors.
// SPDX-License-Identifier: GPL-2.0-or-later

package model

import (
	"errors"
	"fmt"

	"github.com/go-quake1/engine/bspfile"
	"github.com/go-quake1/engine/bsptrace"
)

// HullSize is one (mins, maxs) pair for the box swept-trace tests
// the collision walker applies per hull index.
type HullSize struct {
	Mins, Maxs [3]float32
}

// BrushHullSizes is the player + monster bounding-box pair the
// upstream Quake collision system uses for hulls 1 and 2. Hulls 0
// (the rendering BSP collapsed into clipnodes) and 3 (unused in
// id1, kept as the identity hull) carry zero offsets.
//
// Per-hull bounds (from tyrquake's Mod_MakeClipHulls):
//
//	hull 0 -- rendering tree (no offset, drawn from Nodes)
//	hull 1 -- player size      (-16, -16, -24) .. (16, 16, 32)
//	hull 2 -- crouch monster   (-32, -32, -24) .. (32, 32, 64)
//	hull 3 -- unused in id1, included for table completeness
var BrushHullSizes = [bspfile.MaxMapHulls]HullSize{
	{Mins: [3]float32{0, 0, 0}, Maxs: [3]float32{0, 0, 0}},
	{Mins: [3]float32{-16, -16, -24}, Maxs: [3]float32{16, 16, 32}},
	{Mins: [3]float32{-32, -32, -24}, Maxs: [3]float32{32, 32, 64}},
	{Mins: [3]float32{0, 0, 0}, Maxs: [3]float32{0, 0, 0}},
}

// BrushModel is a loaded BSP brushmodel: the source [bspfile.File]
// plus the 4 [bsptrace.Hull] values the collision walker traces
// against. tyrquake: brushmodel_t.
//
// Beyond the raw [bspfile] data, BrushModel keeps decoded wrappers
// around the leaf + node arrays ([Leaf], [Node]) that add the two
// runtime fields the renderer's per-frame PVS walk needs (VisFrame +
// ParentNode). Access them via the [BrushModel.Leaf], [BrushModel.Node],
// and matching VisFrame / ParentNode accessors; the
// [github.com/go-quake1/engine/bsprender] package consumes those via
// the MarkContext closures returned by its NewMarkContext helper.
type BrushModel struct {
	File   *bspfile.File
	Hulls  [bspfile.MaxMapHulls]bsptrace.Hull
	leaves []Leaf // index 0 is the outside-the-map sentinel
	nodes  []Node
}

// LoadBrush builds a BrushModel for the given submodel index of
// file. submodelIdx=0 is the world model; >0 are the brush entities
// (doors, lifts, trains) listed in the file's Models lump.
//
// Mapping vs tyrquake@6531579:
//
//	common/model.c:Mod_MakeDrawHull       -> hull 0 (Nodes folded into ClipNodes)
//	common/model.c:Mod_MakeClipHulls      -> hulls 1, 2 (use ClipNodes lump verbatim)
//	common/model.c:Mod_SetupSubmodels     -> per-submodel firstclipnode from
//	                                         models[idx].Headnode[0..3]
func LoadBrush(file *bspfile.File, submodelIdx int) (*BrushModel, error) {
	if file == nil {
		return nil, errors.New("model: nil bspfile")
	}
	models, err := file.Models()
	if err != nil {
		return nil, fmt.Errorf("model: read models lump: %w", err)
	}
	if submodelIdx < 0 || submodelIdx >= len(models) {
		return nil, fmt.Errorf("model: submodel index %d out of range [0, %d)", submodelIdx, len(models))
	}
	planes, err := file.Planes()
	if err != nil {
		return nil, fmt.Errorf("model: read planes lump: %w", err)
	}
	nodes, err := file.Nodes()
	if err != nil {
		return nil, fmt.Errorf("model: read nodes lump: %w", err)
	}
	leafs, err := file.Leafs()
	if err != nil {
		return nil, fmt.Errorf("model: read leafs lump: %w", err)
	}
	clipnodes, err := file.ClipNodes()
	if err != nil {
		return nil, fmt.Errorf("model: read clipnodes lump: %w", err)
	}

	hull0Clipnodes, err := makeDrawHull(nodes, leafs)
	if err != nil {
		return nil, err
	}
	headnode := &models[submodelIdx].Headnode

	bm := &BrushModel{File: file}
	// Build the leaf + node wrappers the renderer's PVS walk writes
	// through. Initialise ParentNode = -1 everywhere; setParent below
	// overwrites the real values for nodes reachable from the root.
	// Leaves unreachable from the root (the outside-the-map sentinel
	// in particular) keep ParentNode = -1.
	bm.leaves = make([]Leaf, len(leafs))
	for i := range leafs {
		bm.leaves[i] = Leaf{Leaf: leafs[i], ParentNode: -1}
	}
	bm.nodes = make([]Node, len(nodes))
	for i := range nodes {
		bm.nodes[i] = Node{Node: nodes[i], ParentNode: -1}
	}
	if len(bm.nodes) > 0 && !bm.setParent(0, -1) {
		return nil, ErrNodeCycle
	}

	bm.Hulls[0] = bsptrace.Hull{
		ClipNodes:     hull0Clipnodes,
		Planes:        planes,
		FirstClipNode: headnode[0],
		LastClipNode:  int32(len(hull0Clipnodes) - 1),
		ClipMins:      BrushHullSizes[0].Mins,
		ClipMaxs:      BrushHullSizes[0].Maxs,
	}
	for h := 1; h < bspfile.MaxMapHulls; h++ {
		bm.Hulls[h] = bsptrace.Hull{
			ClipNodes:     clipnodes,
			Planes:        planes,
			FirstClipNode: headnode[h],
			LastClipNode:  int32(len(clipnodes) - 1),
			ClipMins:      BrushHullSizes[h].Mins,
			ClipMaxs:      BrushHullSizes[h].Maxs,
		}
	}
	return bm, nil
}

// makeDrawHull collapses the BSP rendering tree (Nodes) into a flat
// ClipNode slice the collision walker can trace through. tyrquake:
// Mod_MakeDrawHull.
//
// Encoding bridge between the two tree formats:
//
//   - Node.Children[j] is int16 from disk; >= 0 is a node index in
//     nodes, < 0 is the bitwise-NOT of a leaf index in leafs (so the
//     leaf index is ^Children[j] or equivalently -1 - Children[j]).
//
//   - ClipNode.Children[j] is int16 where >= 0 is a clipnode index
//     and < 0 is the CONTENTS_* tag directly (CONTENTS_EMPTY = -1,
//     CONTENTS_SOLID = -2, etc).
//
// For non-leaf children, the index value carries through unchanged
// (Nodes and the derived ClipNodes have the same index space). For
// leaf children, the leaf's Contents tag replaces the leaf index.
func makeDrawHull(nodes []bspfile.Node, leafs []bspfile.Leaf) ([]bspfile.ClipNode, error) {
	out := make([]bspfile.ClipNode, len(nodes))
	for i, n := range nodes {
		out[i].PlaneNum = n.PlaneNum
		for j := 0; j < 2; j++ {
			raw := n.Children[j]
			if raw >= 0 {
				out[i].Children[j] = raw
				continue
			}
			leafIdx := int(^raw)
			if leafIdx < 0 || leafIdx >= len(leafs) {
				return nil, fmt.Errorf("model: node %d child %d encodes leaf %d outside [0, %d)", i, j, leafIdx, len(leafs))
			}
			contents := leafs[leafIdx].Contents
			if contents > 0 || contents < -32768 {
				return nil, fmt.Errorf("model: node %d child %d leaf %d has contents %d outside int16 range", i, j, leafIdx, contents)
			}
			out[i].Children[j] = int16(contents)
		}
	}
	return out, nil
}
