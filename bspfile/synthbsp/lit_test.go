// Copyright (c) 2026 the go-quake1/engine authors.
// SPDX-License-Identifier: GPL-2.0-or-later

package synthbsp

import (
	"bytes"
	"testing"

	"github.com/go-quake1/engine/bspfile"
)

// BuildLit's maps open, and its knobs show in the decoded lumps.
func TestBuildLit(t *testing.T) {
	data, size, err := BuildLit(LitSpec{Lighting: []byte{1, 2, 3}, TexName: "wall", Tree: TreeFrontNode, SlantType: true, ExtraFaces: 2})
	if err != nil {
		t.Fatal(err)
	}
	f, err := bspfile.Open(bytes.NewReader(data), size)
	if err != nil {
		t.Fatal(err)
	}
	nodes, _ := f.Nodes()
	planes, _ := f.Planes()
	faces, _ := f.Faces()
	tex, _ := f.Textures()
	mt, ok, _ := tex.MipTex(0)
	if len(nodes) != 2 || nodes[0].Children[0] != 1 || nodes[0].NumFaces != 3 || planes[0].Type != bspfile.PlaneAnyZ ||
		len(faces) != 1 || faces[0].LightOfs != 0 || !ok || mt.Name != "wall" || len(f.Lighting()) != 3 {
		t.Errorf("nodes %v planes %v faces %v tex %v %v lighting %v", nodes, planes, faces, ok, mt, f.Lighting())
	}
	// every remaining knob once
	data, size, _ = BuildLit(LitSpec{Lighting: []byte{1}, TexName: "x", BadTexInfo: true, NoEdges: true, SpecialTexInfo: true,
		MipTexIndex: 3, MissingTex: true, BadPlane: true, NoSamples: true, ReverseEdges: true, LightOfsDelta: 5})
	if _, err := bspfile.Open(bytes.NewReader(data), size); err != nil {
		t.Fatal(err)
	}
	data, size, _ = BuildLit(LitSpec{Lighting: []byte{1}, TexName: "x", LightOfsDelta: 5})
	f, _ = bspfile.Open(bytes.NewReader(data), size)
	if faces, _ = f.Faces(); faces[0].LightOfs != 5 {
		t.Errorf("LightOfsDelta: LightOfs = %d, want 5", faces[0].LightOfs)
	}
	// no lighting: the face has no samples; a damaged lump is misaligned
	data, size, _ = BuildLit(LitSpec{TexName: "x", Tree: TreeBackNode, Corrupt: []bspfile.LumpKind{bspfile.LumpEdges, bspfile.LumpTextures}})
	f, err = bspfile.Open(bytes.NewReader(data), size)
	if err != nil {
		t.Fatal(err)
	}
	faces, _ = f.Faces()
	if faces[0].LightOfs != -1 {
		t.Errorf("LightOfs = %d, want -1", faces[0].LightOfs)
	}
	if _, err := f.Edges(); err == nil {
		t.Error("a corrupt edges lump decoded")
	}
	if _, err := f.Textures(); err == nil {
		t.Error("a corrupt textures lump decoded")
	}
}
