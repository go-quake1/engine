// Copyright (c) 2026 the go-quake1/engine authors.
// SPDX-License-Identifier: GPL-2.0-or-later

package synthbsp

import (
	"bytes"
	"encoding/binary"

	"github.com/go-quake1/engine/bspfile"
)

// BuildLit produces a synthetic BSP for light sampling: one node on the plane
// z = 0 (normal up, children: leaf 1 above, leaf 2 below), one face on it, the
// square (0,0)-(40,40) with texture vectors s = x and t = y, so its lightmap
// is 4 x 4 samples (texturemins 0, extents 48). The face's light styles and
// the lighting lump are the caller's (an empty lump gives a map without
// lighting; the face's LightOfs is 0, or -1 when the lump is empty); texName
// is the face's texture name.
func BuildLit(lighting []byte, styles [bspfile.MaxLightmaps]byte, texName string) ([]byte, int64, error) {
	planes := []bspfile.Plane{{Normal: [3]float32{0, 0, 1}, Dist: 0, Type: bspfile.PlaneZ}}
	nodes := []bspfile.Node{{PlaneNum: 0, Children: [2]int16{^int16(1), ^int16(2)}, FirstFace: 0, NumFaces: 1}}
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
	texinfos := []bspfile.TexInfo{{Vecs: [2][4]float32{{1, 0, 0, 0}, {0, 1, 0, 0}}, MipTex: 0}}
	ofs := int32(0)
	if len(lighting) == 0 {
		ofs = -1
	}
	faces := []bspfile.Face{{PlaneNum: 0, FirstEdge: 0, NumEdges: 4, TexInfo: 0, Styles: styles, LightOfs: ofs}}

	// one miptex record: name, width, height, four zero offsets
	rec := make([]byte, 40)
	copy(rec[:16], texName)
	binary.LittleEndian.PutUint32(rec[16:], 64)
	binary.LittleEndian.PutUint32(rec[20:], 64)
	tex := &bytes.Buffer{}
	_ = binary.Write(tex, binary.LittleEndian, int32(1))
	_ = binary.Write(tex, binary.LittleEndian, int32(8))
	tex.Write(rec)

	full := assembleBSP([]lump{
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
		{kind: bspfile.LumpLighting, data: lighting},
	})
	return full, int64(len(full)), nil
}
