// Copyright (c) 2026 the go-quake1/engine authors.
// SPDX-License-Identifier: GPL-2.0-or-later

package gpuworld

import (
	"fmt"

	"github.com/go-gfx/webgpu"
	"github.com/go-gfx/webgpu/wire"
)

// Pitch is the row stride of a texture-to-buffer copy of width single-byte
// texels: width rounded up to the 256 bytes the copy demands.
func Pitch(width int) int {
	a := wire.CopyBytesPerRowAlignment
	return (width + a - 1) / a * a
}

// ReadTexture reads the width x height single-byte texels of tex (it needs
// the CopySrc usage) from a remote device: Recorder.CopyTextureToBuffer into a
// MAP_READ buffer and Recorder.ReadBuffer, in bands of whole rows that each fit
// [ReadWindow], one submit each; the rows come back de-padded.
func ReadTexture(dev *webgpu.Device, tex *webgpu.Texture, width, height int) ([]byte, error) {
	pitch := Pitch(width)
	if width <= 0 || height <= 0 || pitch > ReadWindow {
		return nil, fmt.Errorf("gpuworld: cannot read back a %dx%d texture", width, height)
	}
	step := bandRows(pitch, ReadWindow)
	buf, err := dev.CreateBuffer(webgpu.BufferDescriptor{Label: "world readback", Size: uint64(pitch * step),
		Usage: webgpu.BufferUsageMapRead | webgpu.BufferUsageCopyDst})
	if err != nil {
		return nil, err
	}
	defer buf.Destroy()
	out := make([]byte, 0, width*height)
	rec := dev.NewRecorder()
	for y := 0; y < height; y += step {
		rows := min(step, height-y)
		rec.CopyTextureToBuffer(webgpu.TexelCopyTextureInfo{Texture: tex, Origin: webgpu.Origin3D{Y: uint32(y)}}, buf,
			webgpu.TexelCopyBufferLayout{BytesPerRow: uint32(pitch), RowsPerImage: uint32(rows)},
			webgpu.Extent3D{Width: uint32(width), Height: uint32(rows)})
		rb := rec.ReadBuffer(buf, 0, uint64(pitch*rows))
		if _, err := rec.Submit(); err != nil {
			return nil, err
		}
		b, err := rb.Bytes()
		if err != nil {
			return nil, err
		}
		out = Depad(out, b, width, pitch, rows)
	}
	return out, nil
}

// Depad appends the rows of src (rows rows of pitch bytes) to dst, keeping
// the first width bytes of each.
func Depad(dst, src []byte, width, pitch, rows int) []byte {
	for y := 0; y < rows; y++ {
		dst = append(dst, src[y*pitch:y*pitch+width]...)
	}
	return dst
}
