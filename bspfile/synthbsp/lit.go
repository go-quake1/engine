// Copyright (c) 2026 the go-quake1/engine authors.
// SPDX-License-Identifier: GPL-2.0-or-later

package synthbsp

import (
	"bytes"
	"encoding/binary"

	"github.com/go-quake1/engine/bspfile"
)

// Tree shapes of [BuildLit].
const (
	// TreeOneNode: one node on z = 0, leaf 1 above and leaf 2 below.
	TreeOneNode = iota
	// TreeFrontNode: the node above the plane is a second node (on z = 25,
	// leaf 1 on both sides, carrying the same face) instead of a leaf.
	TreeFrontNode
	// TreeBackNode: the node below the plane is that second node.
	TreeBackNode
)

// LitSpec describes the synthetic BSP of [BuildLit]: one face on the plane
// z = 0, the square (0,0)-(40,40) with texture vectors s = x and t = y, whose
// lightmap is 4 x 4 samples (texturemins 0, extents 48). The zero value is a
// valid map without lighting whose face is textured "wall"; the other fields
// bend it, to reach the error and edge cases of a light sampler.
type LitSpec struct {
	Lighting []byte                     // the lighting lump; the face's LightOfs is 0, or -1 when empty
	Styles   [bspfile.MaxLightmaps]byte // the face's light styles
	TexName  string                     // the face's texture name
	Tree     int                        // TreeOneNode, TreeFrontNode or TreeBackNode

	ReverseEdges   bool               // the face's surfedges are negative (each edge's V1)
	BadTexInfo     bool               // the face's texinfo index is out of range
	NoEdges        bool               // the face has no edges
	SpecialTexInfo bool               // the texinfo carries TEX_SPECIAL
	MipTexIndex    int32              // the texinfo's miptex index (0 is the face's texture)
	MissingTex     bool               // the texture's slot is the missing-texture sentinel (-1)
	BadPlane       bool               // the node's plane number is out of range
	SlantType      bool               // the plane is typed PlaneAnyZ: the general plane test
	ExtraFaces     uint16             // faces the node claims beyond the one that exists
	LightOfsDelta  int32              // added to the face's LightOfs
	NoSamples      bool               // the face has no lightmap (LightOfs -1) though the lump exists
	Corrupt        []bspfile.LumpKind // lumps whose length is made misaligned
}

// BuildLit produces the synthetic BSP of spec for light sampling.
func BuildLit(spec LitSpec) ([]byte, int64, error) {
	ptype := int32(bspfile.PlaneZ)
	if spec.SlantType {
		ptype = bspfile.PlaneAnyZ
	}
	planes := []bspfile.Plane{
		{Normal: [3]float32{0, 0, 1}, Dist: 0, Type: ptype},
		{Normal: [3]float32{0, 0, 1}, Dist: 25, Type: bspfile.PlaneZ},
	}
	root := bspfile.Node{PlaneNum: 0, Children: [2]int16{^int16(1), ^int16(2)}, FirstFace: 0, NumFaces: 1 + spec.ExtraFaces}
	if spec.BadPlane {
		root.PlaneNum = 7
	}
	nodes := []bspfile.Node{root}
	switch spec.Tree {
	case TreeFrontNode:
		nodes[0].Children[0] = 1
		nodes = append(nodes, bspfile.Node{PlaneNum: 1, Children: [2]int16{^int16(1), ^int16(1)}, FirstFace: 0, NumFaces: 1})
	case TreeBackNode:
		nodes[0].Children[1] = 1
		nodes = append(nodes, bspfile.Node{PlaneNum: 1, Children: [2]int16{^int16(1), ^int16(1)}, FirstFace: 0, NumFaces: 1})
	}
	leafs := []bspfile.Leaf{
		{Contents: bspfile.ContentsSolid, VisOfs: -1},
		{Contents: bspfile.ContentsEmpty, VisOfs: -1},
		{Contents: bspfile.ContentsSolid, VisOfs: -1},
	}
	clipnodes := []bspfile.ClipNode{{PlaneNum: 0, Children: [2]int16{bspfile.ContentsEmpty, bspfile.ContentsSolid}}}
	models := []bspfile.Model{{
		Mins: [3]float32{-100, -100, -100}, Maxs: [3]float32{100, 100, 100},
		Headnode: [bspfile.MaxMapHulls]int32{0, 0, 0, 0}, NumFaces: 1,
	}}
	vertices := []bspfile.Vertex{{X: 0, Y: 0, Z: 0}, {X: 40, Y: 0, Z: 0}, {X: 40, Y: 40, Z: 0}, {X: 0, Y: 40, Z: 0}}
	edges := []bspfile.Edge{{V0: 0, V1: 0}, {V0: 0, V1: 1}, {V0: 1, V1: 2}, {V0: 2, V1: 3}, {V0: 3, V1: 0}}
	surfedges := []bspfile.Surfedge{1, 2, 3, 4}
	if spec.ReverseEdges {
		// the same square, each edge taken backwards: vertex V1 of each
		surfedges = []bspfile.Surfedge{-4, -1, -2, -3}
	}
	ti := bspfile.TexInfo{Vecs: [2][4]float32{{1, 0, 0, 0}, {0, 1, 0, 0}}, MipTex: spec.MipTexIndex}
	if spec.SpecialTexInfo {
		ti.Flags = 1
	}
	texinfos := []bspfile.TexInfo{ti}
	ofs := int32(0)
	if len(spec.Lighting) == 0 || spec.NoSamples {
		ofs = -1
	} else {
		ofs += spec.LightOfsDelta
	}
	face := bspfile.Face{PlaneNum: 0, FirstEdge: 0, NumEdges: 4, TexInfo: 0, Styles: spec.Styles, LightOfs: ofs}
	if spec.BadTexInfo {
		face.TexInfo = 9
	}
	if spec.NoEdges {
		face.NumEdges = 0
	}
	faces := []bspfile.Face{face}

	// one miptex record: name, width, height, four zero offsets
	rec := make([]byte, 40)
	copy(rec[:16], spec.TexName)
	binary.LittleEndian.PutUint32(rec[16:], 64)
	binary.LittleEndian.PutUint32(rec[20:], 64)
	tex := &bytes.Buffer{}
	_ = binary.Write(tex, binary.LittleEndian, int32(1))
	if spec.MissingTex {
		_ = binary.Write(tex, binary.LittleEndian, int32(-1))
	} else {
		_ = binary.Write(tex, binary.LittleEndian, int32(8))
	}
	tex.Write(rec)

	lumps := []lump{
		{kind: bspfile.LumpPlanes, data: encodePlanes(planes)},
		{kind: bspfile.LumpNodes, data: encodeNodes(nodes)},
		{kind: bspfile.LumpLeafs, data: encodeLeafs(leafs)},
		{kind: bspfile.LumpClipnodes, data: encodeClipnodes(clipnodes)},
		{kind: bspfile.LumpModels, data: encodeModels(models)},
		{kind: bspfile.LumpVertexes, data: encodeVertices(vertices)},
		{kind: bspfile.LumpEdges, data: encodeEdges(edges)},
		{kind: bspfile.LumpSurfedges, data: encodeSurfedges(surfedges)},
		{kind: bspfile.LumpTexInfo, data: encodeTexInfos(texinfos)},
		{kind: bspfile.LumpFaces, data: encodeFaces(faces)},
		{kind: bspfile.LumpTextures, data: tex.Bytes()},
		{kind: bspfile.LumpLighting, data: spec.Lighting},
	}
	full := assembleBSP(lumps)
	// Corrupt a lump: one byte more than its records, or 2 bytes for the
	// textures directory (shorter than its count field).
	for _, k := range spec.Corrupt {
		at := 4 + int(k)*8 + 4
		n := binary.LittleEndian.Uint32(full[at:])
		if k == bspfile.LumpTextures {
			binary.LittleEndian.PutUint32(full[at:], 2)
		} else {
			binary.LittleEndian.PutUint32(full[at:], n+1)
		}
	}
	return full, int64(len(full)), nil
}
