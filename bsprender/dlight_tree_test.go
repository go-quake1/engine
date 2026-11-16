// Copyright (c) 2026 the go-quake1/engine authors.
// SPDX-License-Identifier: GPL-2.0-or-later

package bsprender_test

import (
	"bytes"
	"testing"

	"github.com/go-quake1/engine/bspfile"
	"github.com/go-quake1/engine/bspfile/synthbsp"
	"github.com/go-quake1/engine/bsprender"
	"github.com/go-quake1/engine/model"
	"github.com/go-quake1/engine/render"
)

// TestNewDLightTree wires a real brush model (the BuildWithFaces fixture: one
// root node on the plane x = 0 with its faces, a leaf on each side) into the
// tree R_MarkLights walks, and marks through it.
func TestNewDLightTree(t *testing.T) {
	data, size, err := synthbsp.BuildWithFaces()
	if err != nil {
		t.Fatal(err)
	}
	f, err := bspfile.Open(bytes.NewReader(data), size)
	if err != nil {
		t.Fatal(err)
	}
	bm, err := model.LoadBrush(f, 0)
	if err != nil {
		t.Fatal(err)
	}
	tree := bsprender.NewDLightTree(bm)
	if tree.Root != 0 {
		t.Fatalf("root %d", tree.Root)
	}
	raw := bm.Node(0)
	n := tree.Node(0)
	if n.Plane.Normal != [3]float32{1, 0, 0} || n.Children != [2]int{-1, -2} || n.FirstFace != int(raw.FirstFace) || n.NumFaces != int(raw.NumFaces) {
		t.Fatalf("node 0 = %+v, want the plane x = 0, children -1 and -2 (leaves) and the faces %d + %d", n, raw.FirstFace, raw.NumFaces)
	}
	var marked []int
	mark := func(face int, bit uint32) {
		if bit != 4 {
			t.Errorf("bit %d, want 4", bit)
		}
		marked = append(marked, face)
	}
	// a light on the plane reaches it: the node's faces are marked, in order
	bsprender.MarkLights(tree, render.DLight{Radius: 10}, 4, tree.Root, mark)
	if len(marked) != int(raw.NumFaces) {
		t.Fatalf("marked %v, want the node's %d faces", marked, raw.NumFaces)
	}
	for i, face := range marked {
		if face != int(raw.FirstFace)+i {
			t.Errorf("face %d is %d, want %d", i, face, int(raw.FirstFace)+i)
		}
	}
	// far in front of it: nothing, the front child is a leaf
	marked = nil
	bsprender.MarkLights(tree, render.DLight{Origin: [3]float32{100, 0, 0}, Radius: 10}, 4, tree.Root, mark)
	if len(marked) != 0 {
		t.Errorf("a light 100 units from the plane marked %v", marked)
	}
}
