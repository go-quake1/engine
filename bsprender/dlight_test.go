// Copyright (c) 2026 the go-quake1/engine authors.
// SPDX-License-Identifier: GPL-2.0-or-later

package bsprender

import (
	"reflect"
	"sort"
	"testing"

	"github.com/go-quake1/engine/render"
)

// dlightTree is a two-node BSP: node 0 splits at x = 0 (faces 0 and 1 on it,
// front child node 1, back child a leaf), node 1 splits at y = 0 (face 2 on
// it, both children leaves).
func dlightTree() DLightTree {
	nodes := []DLightNode{
		{Plane: render.Plane{Normal: [3]float32{1, 0, 0}}, Children: [2]int{1, -1}, FirstFace: 0, NumFaces: 2},
		{Plane: render.Plane{Normal: [3]float32{0, 1, 0}}, Children: [2]int{-2, -3}, FirstFace: 2, NumFaces: 1},
	}
	return DLightTree{Root: 0, Node: func(i int) DLightNode { return nodes[i] }}
}

func marked(t DLightTree, l render.DLight) map[int]uint32 {
	got := map[int]uint32{}
	MarkLights(t, l, 1, t.Root, func(face int, bit uint32) { got[face] |= bit })
	return got
}

func faces(m map[int]uint32) []int {
	var out []int
	for f := range m {
		out = append(out, f)
	}
	sort.Ints(out)
	return out
}

// TestMarkLights pins R_MarkLights on a tree worked by hand: a light marks the
// faces of the nodes whose plane its sphere reaches, and descends only the
// side it reaches.
func TestMarkLights(t *testing.T) {
	tree := dlightTree()
	for _, c := range []struct {
		name   string
		origin [3]float32
		radius float32
		want   []int
	}{
		// dist to x = 0 is 100 > 50: only the front child; there the light is on
		// the plane y = 0 and marks face 2. Node 0's own faces are not marked.
		{"front of the root, on the second plane", [3]float32{100, 0, 0}, 50, []int{2}},
		// 30 <= 50: it straddles x = 0 (faces 0 and 1), then y = 0 (face 2).
		{"straddling both", [3]float32{30, 0, 0}, 50, []int{0, 1, 2}},
		// behind the root by more than the radius: the leaf, nothing marked.
		{"behind the root", [3]float32{-100, 0, 0}, 50, nil},
		// straddles x = 0 (30 <= 50), but 80 > 50 in front of y = 0: its front
		// child is a leaf.
		{"straddling the root only", [3]float32{30, 80, 0}, 50, []int{0, 1}},
		// dist == radius is not "> radius": the sphere touches the plane.
		{"touching the root plane", [3]float32{50, 80, 0}, 50, []int{0, 1}},
		// one unit further: the root's front child only, which is then 80 > 50
		// from y = 0: a leaf.
		{"just past the root plane", [3]float32{51, 80, 0}, 50, nil},
		// behind y = 0 by more than the radius: face 2 is skipped, the back
		// child is a leaf.
		{"behind the second plane", [3]float32{30, -80, 0}, 50, []int{0, 1}},
	} {
		got := faces(marked(tree, render.DLight{Origin: c.origin, Radius: c.radius}))
		if !reflect.DeepEqual(got, c.want) && !(len(got) == 0 && len(c.want) == 0) {
			t.Errorf("%s: marked %v, want %v", c.name, got, c.want)
		}
	}
}

// TestPushDlights: the lights that are alive (die >= now) and have a radius
// mark with their own bit, 1 << index.
func TestPushDlights(t *testing.T) {
	tree := dlightTree()
	lights := make([]render.DLight, 6)
	lights[0] = render.DLight{Origin: [3]float32{30, 0, 0}, Radius: 50, Die: 5}  // faces 0 1 2
	lights[1] = render.DLight{Origin: [3]float32{30, 0, 0}, Radius: 50, Die: 1}  // dead at now = 2
	lights[2] = render.DLight{Origin: [3]float32{30, 0, 0}, Radius: 0, Die: 5}   // no radius
	lights[3] = render.DLight{Origin: [3]float32{100, 0, 0}, Radius: 50, Die: 2} // die == now: alive; face 2
	lights[5] = render.DLight{Origin: [3]float32{100, 0, 0}, Radius: 50, Die: 9} // face 2
	got := map[int]uint32{}
	PushDlights(tree, lights, 2, func(face int, bit uint32) { got[face] |= bit })
	want := map[int]uint32{0: 1 << 0, 1: 1 << 0, 2: 1<<0 | 1<<3 | 1<<5}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("bits %v, want %v", got, want)
	}
}
