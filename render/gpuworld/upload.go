// Copyright (c) 2026 the go-quake1/engine authors.
// SPDX-License-Identifier: GPL-2.0-or-later

package gpuworld

import (
	"fmt"

	"github.com/go-gfx/webgpu"
)

// MaxBandBytes is the most bytes of one upload command. A remote device (the
// batched stream of a wasip1 guest) carries one frame of at most the host's
// MaxFrame (32 MiB by default) and refuses a single message above it, and
// Queue.WriteTexture is not carried at all; the 33 MB surface atlas is above
// both. Uploads are therefore cut into bands of whole rows, each at most this
// size, and each band is one Recorder submit.
const MaxBandBytes = 8 << 20

// ReadWindow is the host's read window per submit (batchsink.MaxRead); a
// read-back is cut into bands of whole rows that each fit it.
const ReadWindow = 1 << 20

// bandRows is how many rows of rowBytes bytes a band of at most max bytes
// holds, never fewer than one.
func bandRows(rowBytes, max int) int {
	if rowBytes <= 0 || rowBytes >= max {
		return 1
	}
	return max / rowBytes
}

// uploadTexture writes the width x height single-byte texels px into the
// top mip of tex, in bands of whole rows of at most MaxBandBytes.
func uploadTexture(dev *webgpu.Device, tex *webgpu.Texture, px []byte, width, height int) error {
	if width <= 0 || height <= 0 || len(px) < width*height {
		return fmt.Errorf("gpuworld: %d bytes for a %dx%d texture", len(px), width, height)
	}
	step := bandRows(width, MaxBandBytes)
	rec := dev.NewRecorder()
	for y := 0; y < height; y += step {
		rows := min(step, height-y)
		rec.WriteTexture(webgpu.TexelCopyTextureInfo{Texture: tex, Origin: webgpu.Origin3D{Y: uint32(y)}},
			px[y*width:(y+rows)*width],
			webgpu.TexelCopyBufferLayout{BytesPerRow: uint32(width), RowsPerImage: uint32(rows)},
			webgpu.Extent3D{Width: uint32(width), Height: uint32(rows)})
		if _, err := rec.Submit(); err != nil {
			return err
		}
	}
	return nil
}

// writeBuffer writes data at offset 0 of b in pieces of at most
// MaxBandBytes, one queue write each.
func writeBuffer(dev *webgpu.Device, b *webgpu.Buffer, data []byte) error {
	for off := 0; off < len(data); off += MaxBandBytes {
		if err := dev.Queue().WriteBuffer(b, uint64(off), data[off:min(off+MaxBandBytes, len(data))]); err != nil {
			return err
		}
	}
	return nil
}
