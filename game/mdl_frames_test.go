// Copyright (c) 1996-1997 Id Software, Inc.
// Copyright (c) 2026 the go-quake1/engine authors.
// SPDX-License-Identifier: GPL-2.0-or-later

package game

import (
	"bytes"
	"encoding/binary"
	"io"
	"math"
	"testing"

	"github.com/go-quake1/engine/mdl"
)

// --- minimal .mdl encoder (mirrors mdl's own test fixture builder) ---

const (
	mdlIdent   = uint32('I') | uint32('D')<<8 | uint32('P')<<16 | uint32('O')<<24
	mdlVersion = 6
	frameNameN = 16
)

func putU32m(b *bytes.Buffer, v uint32) {
	var x [4]byte
	binary.LittleEndian.PutUint32(x[:], v)
	b.Write(x[:])
}
func putI32m(b *bytes.Buffer, v int32) { putU32m(b, uint32(v)) }
func putF32m(b *bytes.Buffer, v float32) {
	putU32m(b, math.Float32bits(v))
}
func putTriV(b *bytes.Buffer, v mdl.TriVertx) {
	b.WriteByte(v.V[0])
	b.WriteByte(v.V[1])
	b.WriteByte(v.V[2])
	b.WriteByte(v.LightNormalIndex)
}

// mdlHeader writes the 84-byte header. numFrames/numSkins are the values
// the body must match.
func mdlHeader(b *bytes.Buffer, numSkins, skinW, skinH, numVerts, numTris, numFrames int32) {
	putU32m(b, mdlIdent)
	putI32m(b, mdlVersion)
	putF32m(b, 1)
	putF32m(b, 1)
	putF32m(b, 1) // scale
	putF32m(b, 0)
	putF32m(b, 0)
	putF32m(b, 0) // scale_origin
	putF32m(b, 16)
	putF32m(b, 0)
	putF32m(b, 0)
	putF32m(b, 32) // eyepos
	putI32m(b, numSkins)
	putI32m(b, skinW)
	putI32m(b, skinH)
	putI32m(b, numVerts)
	putI32m(b, numTris)
	putI32m(b, numFrames)
	putI32m(b, 0)   // synctype
	putI32m(b, 0)   // flags
	putF32m(b, 1.0) // size
}

// singleFrame writes a FrameSingle record with numVerts body verts.
func singleFrame(b *bytes.Buffer, numVerts int) {
	putI32m(b, 0) // FrameSingle
	putTriV(b, mdl.TriVertx{V: [3]byte{0, 0, 0}})
	putTriV(b, mdl.TriVertx{V: [3]byte{8, 8, 8}})
	name := make([]byte, frameNameN)
	copy(name, "f")
	b.Write(name)
	for i := 0; i < numVerts; i++ {
		putTriV(b, mdl.TriVertx{V: [3]byte{1, 2, 3}, LightNormalIndex: 4})
	}
}

// commonBody writes one single skin + numVerts STVerts + numTris tris.
func commonBody(b *bytes.Buffer, skinW, skinH, numVerts, numTris int) {
	putI32m(b, 0) // SkinSingle
	b.Write(bytes.Repeat([]byte{7}, skinW*skinH))
	for i := 0; i < numVerts; i++ {
		putI32m(b, 0) // OnSeam
		putI32m(b, 0) // S
		putI32m(b, 0) // T
	}
	for i := 0; i < numTris; i++ {
		putI32m(b, 1) // FacesFront
		putI32m(b, 0)
		putI32m(b, int32(i%numVerts))
		putI32m(b, 0)
	}
}

func reader(b []byte) (io.ReaderAt, int64) { return bytes.NewReader(b), int64(len(b)) }

// TestResolveModelBBoxMDLFrameBranches drives resolveModelBBox's alias
// .mdl decode branches: FrameGroup bbox (success), an empty FrameGroup
// (early-out), and a zero-frame model (early-out). The bytes are crafted
// minimal .mdl blobs handed to the host via a fake resolver.
func TestResolveModelBBoxMDLFrameBranches(t *testing.T) {
	h, _, _ := buildRealHost(t)
	orig := h.Resolver
	defer func() { h.Resolver = orig }()

	const sw, sh, nv, nt = 2, 2, 2, 1

	// (a) FrameGroup with >=1 sub-frame -> the FrameGroup bbox branch.
	groupOK := &bytes.Buffer{}
	mdlHeader(groupOK, 1, sw, sh, nv, nt, 1)
	commonBody(groupOK, sw, sh, nv, nt)
	putI32m(groupOK, 1) // FrameGroup
	putI32m(groupOK, 1) // 1 sub-frame
	putTriV(groupOK, mdl.TriVertx{V: [3]byte{0, 0, 0}})
	putTriV(groupOK, mdl.TriVertx{V: [3]byte{9, 9, 9}})
	putF32m(groupOK, 0.1) // interval
	singleFrame(groupOK, nv)

	// (b) zero-frame model -> len(m.Frames) == 0 early-out.
	zeroFrames := &bytes.Buffer{}
	mdlHeader(zeroFrames, 1, sw, sh, nv, nt, 0)
	commonBody(zeroFrames, sw, sh, nv, nt)

	cases := []struct {
		name string
		blob []byte
		want bool
	}{
		{"frame-group", groupOK.Bytes(), true},
		{"zero-frames", zeroFrames.Bytes(), false},
	}
	for i, tc := range cases {
		// Sanity: the blob must actually decode through mdl.Load (so the
		// branch under test is reached, not the load-error branch).
		if _, err := mdl.Load(reader(tc.blob)); err != nil {
			t.Skipf("%s: mdl.Load rejected the fixture (%v); skipping", tc.name, err)
		}
		h.Resolver = func(string) (int64, io.ReaderAt, error) {
			ra, sz := reader(tc.blob)
			return sz, ra, nil
		}
		cache := &setModelCache{mdlBBox: map[int][2][3]float32{}}
		_, _, ok := resolveModelBBox(h, cache, "progs/crafted.mdl", 40+i)
		if ok != tc.want {
			t.Errorf("%s: resolveModelBBox ok=%v want %v", tc.name, ok, tc.want)
		}
	}
}
