// Copyright (c) 1996-1997 Id Software, Inc.
// Copyright (c) 2026 the go-quake1/engine authors.
// SPDX-License-Identifier: GPL-2.0-or-later

package bsprender

import (
	"github.com/go-quake1/engine/model"
	"github.com/go-quake1/engine/render"
)

// DLightNode is the part of an mnode_t that R_MarkLights reads: its splitting
// plane, its children (a negative child is a leaf, contents < 0, where the
// recursion stops) and the faces attached to the node itself.
type DLightNode struct {
	Plane               render.Plane
	Children            [2]int
	FirstFace, NumFaces int
}

// DLightTree is a BSP as R_MarkLights walks it: Node(i) for an interior node
// index i, Root the first.
type DLightTree struct {
	Root int
	Node func(i int) DLightNode
}

// NewDLightTree wires a loaded brush model as a [DLightTree] rooted at the
// world's first node (cl.worldmodel->nodes). The children are the on-disk
// ones: negative for a leaf.
func NewDLightTree(bm *model.BrushModel) DLightTree {
	planes, _ := bm.File.Planes()
	return DLightTree{Root: 0, Node: func(i int) DLightNode {
		n := bm.Node(i)
		var pl render.Plane
		if pn := int(n.PlaneNum); pn >= 0 && pn < len(planes) {
			pl = render.Plane{Normal: planes[pn].Normal, Dist: planes[pn].Dist}
		}
		return DLightNode{Plane: pl, Children: [2]int{int(n.Children[0]), int(n.Children[1])},
			FirstFace: int(n.FirstFace), NumFaces: int(n.NumFaces)}
	}}
}

// MarkLights is R_MarkLights (reference/common/r_light.c): it walks the tree
// from node, descending only into the side of each splitting plane the light's
// sphere reaches (both when it straddles the plane), and calls mark(face, bit)
// for the faces of every node it passes. The caller's mark does what the C
// does to the msurface_t: if its dlightframe is not the frame being prepared,
// clear its dlightbits and set the frame; then or the bit in.
func MarkLights(t DLightTree, l render.DLight, bit uint32, node int, mark func(face int, bit uint32)) {
	for {
		if node < 0 {
			return // node->contents < 0: a leaf
		}
		n := t.Node(node)
		dist := l.Origin[0]*n.Plane.Normal[0] + l.Origin[1]*n.Plane.Normal[1] + l.Origin[2]*n.Plane.Normal[2] - n.Plane.Dist
		if dist > l.Radius {
			node = n.Children[0]
			continue
		}
		if dist < -l.Radius {
			node = n.Children[1]
			continue
		}
		for i := 0; i < n.NumFaces; i++ {
			mark(n.FirstFace+i, bit)
		}
		MarkLights(t, l, bit, n.Children[0], mark)
		node = n.Children[1]
	}
}

// PushDlights is R_PushDlights: every light of lights (cl_dlights) that has not
// died (die >= now) and has a radius marks the surfaces it reaches with its
// bit, 1 << its index, from the tree's root.
func PushDlights(t DLightTree, lights []render.DLight, now float32, mark func(face int, bit uint32)) {
	for i := range lights {
		l := lights[i]
		if l.Die < now || l.Radius == 0 {
			continue
		}
		MarkLights(t, l, 1<<uint(i), t.Root, mark)
	}
}
