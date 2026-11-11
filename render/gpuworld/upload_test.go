// Copyright (c) 2026 the go-quake1/engine authors.
// SPDX-License-Identifier: GPL-2.0-or-later

//go:build !js

package gpuworld

import (
	"bytes"
	"testing"

	"github.com/go-gfx/webgpu"
	"github.com/go-gfx/webgpu/wire"
)

// fakeHost is a remote device's sink standing in for the host: it keeps what
// WriteTexture wrote, row by row, answers CopyTextureToBuffer + ReadBuffer
// from it with the 256-byte row padding, and records the size of each submit.
type fakeHost struct {
	t       *testing.T
	rows    map[[2]uint32][]byte // (texture id, y) -> the row
	bufs    map[uint32][]byte    // buffer id -> bytes
	maxData int                  // largest data stream of one submit
	submits int
	writes  int // WriteTexture commands
}

func newFakeHost(t *testing.T) *fakeHost {
	return &fakeHost{t: t, rows: map[[2]uint32][]byte{}, bufs: map[uint32][]byte{}}
}

func (h *fakeHost) Create(id uint32, kind wire.Kind, desc []byte) error { return nil }
func (h *fakeHost) Destroy(id uint32) error                             { return nil }
func (h *fakeHost) Submit(words []uint32, data []byte) error {
	return h.SubmitRead(words, data, nil)
}

func (h *fakeHost) SubmitRead(words []uint32, data []byte, window []byte) error {
	h.submits++
	h.maxData = max(h.maxData, len(data))
	cmds, err := wire.Decode(words, uint32(len(data)), func(uint32) bool { return true })
	if err != nil {
		return err
	}
	for _, c := range cmds {
		a := c.Args
		switch c.Op {
		case wire.WriteTexture:
			h.writes++
			off, n, bpr, w, rows := a[6], a[7], a[10], a[12], a[13]
			if uint32(len(data)) < off+n {
				h.t.Fatalf("WriteTexture data [%d,+%d) past %d", off, n, len(data))
			}
			for y := uint32(0); y < rows; y++ {
				h.rows[[2]uint32{a[0], a[3] + y}] = append([]byte(nil), data[off+y*bpr:off+y*bpr+w]...)
			}
		case wire.CopyTextureToBuffer:
			bpr, w, rows := a[9], a[11], a[12]
			b := make([]byte, bpr*rows)
			for y := uint32(0); y < rows; y++ {
				copy(b[y*bpr:], h.rows[[2]uint32{a[0], a[3] + y}][:w])
			}
			h.bufs[a[6]] = b
		case wire.ReadBuffer:
			copy(window[a[4]:a[4]+a[3]], h.bufs[a[0]][a[1]:])
		}
	}
	return nil
}

func gradient(n int) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = byte(i*131 + i/251)
	}
	return b
}

func remoteTexture(t *testing.T, h *fakeHost, w, hgt int) (*webgpu.Device, *webgpu.Texture) {
	t.Helper()
	dev := webgpu.NewRemoteDevice(h, "test", &webgpu.Quota{MemoryBytes: 1 << 30, ReadBytes: ReadWindow})
	tex, err := dev.CreateTexture(webgpu.TextureDescriptor{Size: webgpu.Extent3D{Width: uint32(w), Height: uint32(hgt)},
		Format: webgpu.TextureFormatR8Uint, Usage: webgpu.TextureUsageCopyDst | webgpu.TextureUsageCopySrc})
	if err != nil {
		t.Fatal(err)
	}
	return dev, tex
}

// TestUploadTextureBands: a 16 MiB atlas (the real surface atlas is 33 MB,
// above the host's 32 MiB frame) goes up in bands of whole rows, none above
// MaxBandBytes, and the host holds every texel.
func TestUploadTextureBands(t *testing.T) {
	const w, hgt = 4096, 4096
	h := newFakeHost(t)
	dev, tex := remoteTexture(t, h, w, hgt)
	px := gradient(w * hgt)
	if err := uploadTexture(dev, tex, px, w, hgt); err != nil {
		t.Fatal(err)
	}
	if h.maxData > MaxBandBytes || h.writes != 2 {
		t.Errorf("largest submit %d bytes (cap %d), %d WriteTexture commands, want 2", h.maxData, MaxBandBytes, h.writes)
	}
	for y := 0; y < hgt; y++ {
		if !bytes.Equal(h.rows[[2]uint32{texID(t, h), uint32(y)}], px[y*w:(y+1)*w]) {
			t.Fatalf("row %d differs", y)
		}
	}
	if err := uploadTexture(dev, tex, px[:10], w, hgt); err == nil {
		t.Error("a short pixel slice was accepted")
	}
}

// texID is the id of the only texture the host saw written.
func texID(t *testing.T, h *fakeHost) uint32 {
	t.Helper()
	for k := range h.rows {
		return k[0]
	}
	t.Fatal("nothing written")
	return 0
}

// TestReadTextureDepads: a texture whose width is not a multiple of 256 and
// whose padded size is above the read window comes back in bands, de-padded,
// equal to what was written.
func TestReadTextureDepads(t *testing.T) {
	const w, hgt = 1000, 2000 // pitch 1024: 2 MB padded, 2 bands of the 1 MiB window
	h := newFakeHost(t)
	dev, tex := remoteTexture(t, h, w, hgt)
	px := gradient(w * hgt)
	if err := uploadTexture(dev, tex, px, w, hgt); err != nil {
		t.Fatal(err)
	}
	before := h.submits
	got, err := ReadTexture(dev, tex, w, hgt)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, px) {
		t.Error("read-back differs from the texels written")
	}
	if n := h.submits - before; n != 2 {
		t.Errorf("%d read submits, want 2", n)
	}
	if _, err := ReadTexture(dev, tex, 0, 5); err == nil {
		t.Error("a zero width was accepted")
	}
}

func TestPitchAndBands(t *testing.T) {
	for _, c := range []struct{ w, want int }{{1, 256}, {256, 256}, {257, 512}, {640, 768}, {1000, 1024}} {
		if got := Pitch(c.w); got != c.want {
			t.Errorf("Pitch(%d) = %d, want %d", c.w, got, c.want)
		}
	}
	if bandRows(4096, MaxBandBytes) != 2048 || bandRows(MaxBandBytes+1, MaxBandBytes) != 1 || bandRows(0, 10) != 1 {
		t.Error("bandRows")
	}
}
