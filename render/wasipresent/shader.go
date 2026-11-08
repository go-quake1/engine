// Copyright (c) 2026 the go-quake1/engine authors.
// SPDX-License-Identifier: GPL-2.0-or-later

// Package wasipresent shows the software renderer's 8-bit frame on a
// wasi-gfx:surface from a wasip1 guest (package wasigpu of
// github.com/go-wasmcontainers/gpu).
//
// The guest's bind groups take buffers only (no texture writes), so the
// frame goes up as a storage buffer of 8-bit indices, four to a 32-bit
// word, and the palette as a second storage buffer of 256 packed RGBA
// words. A fragment shader without loops (the host refuses a loop in a
// fragment stage) picks the source texel under each output pixel, looks
// the colour up and writes it opaque. The CPU never expands the frame to
// RGBA.
//
// This file is the platform-neutral part: the shader, the packing and
// Emulate, the shader's arithmetic in Go, which the tests hold equal to
// webgpupresent.Reference (the CPU oracle of the browser presenter). The
// Presenter is in presenter.go; the GPU driver is in driver_wasip1.go.
package wasipresent

import (
	"fmt"

	"github.com/go-quake1/engine/render"
)

// Shader returns the WGSL of a presenter for a width x height index frame
// of pitch bytes per row shown on an outW x outH surface. The geometry is
// baked in as constants, so the shader is built once per geometry.
//
// Binding 0 is the index buffer (array<u32>, index n in byte n&3 of word
// n>>2, little endian), binding 1 the palette (256 words, R in the low
// byte, then G, B, A). The vertex stage draws one full-screen triangle
// (three vertices, no vertex buffer). The fragment stage maps the output
// pixel (ox, oy) to the source texel
//
//	sx = (2*ox+1) * width  / (2*outW)
//	sy = (2*oy+1) * height / (2*outH)
//
// in integer arithmetic, exactly as webgpupresent.Reference does.
func Shader(width, height, pitch, outW, outH int) string {
	return fmt.Sprintf(`@group(0) @binding(0) var<storage, read> idx: array<u32>;
@group(0) @binding(1) var<storage, read> pal: array<u32>;

struct VOut {
  @builtin(position) pos: vec4f,
};

@vertex fn vs(@builtin(vertex_index) i: u32) -> VOut {
  let p = vec2f(f32((i << 1u) & 2u), f32(i & 2u));
  var o: VOut;
  o.pos = vec4f(p * 2.0 - 1.0, 0.0, 1.0);
  return o;
}

@fragment fn fs(v: VOut) -> @location(0) vec4f {
  let ox = u32(v.pos.x);
  let oy = u32(v.pos.y);
  let sx = (2u * ox + 1u) * %du / %du;
  let sy = (2u * oy + 1u) * %du / %du;
  let n = sy * %du + sx;
  let k = (idx[n >> 2u] >> ((n & 3u) * 8u)) & 255u;
  let c = pal[k];
  return vec4f(f32(c & 255u), f32((c >> 8u) & 255u), f32((c >> 16u) & 255u), 255.0) / 255.0;
}
`, width, 2*outW, height, 2*outH, pitch)
}

// IndexWords is the number of 32-bit words the index buffer of a frame of
// height rows of pitch bytes holds.
func IndexWords(pitch, height int) int { return (pitch*height + 3) / 4 }

// PackIndices packs pixels (pitch bytes per row, height rows; only the
// first pitch*(height-1)+width bytes need exist) into dst, four indices to
// a little-endian word, and returns dst resized to IndexWords words. Bytes
// past the end of pixels pack as zero. dst is reused when it has room.
func PackIndices(dst []byte, pixels []byte, pitch, height int) []byte {
	n := IndexWords(pitch, height) * 4
	if cap(dst) < n {
		dst = make([]byte, n)
	}
	dst = dst[:n]
	m := copy(dst, pixels[:min(len(pixels), pitch*height)])
	clear(dst[m:])
	return dst
}

// PaletteWords packs pal into the 256 words of binding 1: R in the low
// byte, then G, B and an opaque A.
func PaletteWords(pal *render.Palette) []uint32 {
	out := make([]uint32, 256)
	for i, c := range pal {
		out[i] = uint32(c[0]) | uint32(c[1])<<8 | uint32(c[2])<<16 | 255<<24
	}
	return out
}

// Emulate runs the shader's arithmetic in Go: for every output pixel the
// source texel, the byte picked out of its packed word, the palette word,
// and the float the fragment stage returns, rounded to 8 bits the way a
// unorm surface stores it. idx is the packed index buffer (see
// PackIndices, as words), pal the palette words. The result is outW x outH
// RGBA8 rows.
func Emulate(idx []uint32, pal []uint32, width, height, pitch, outW, outH int) []byte {
	out := make([]byte, outW*outH*4)
	o := 0
	for oy := 0; oy < outH; oy++ {
		sy := uint32(2*oy+1) * uint32(height) / uint32(2*outH)
		for ox := 0; ox < outW; ox++ {
			sx := uint32(2*ox+1) * uint32(width) / uint32(2*outW)
			n := sy*uint32(pitch) + sx
			k := (idx[n>>2] >> ((n & 3) * 8)) & 255
			c := pal[k]
			for _, v := range [4]uint32{c & 255, (c >> 8) & 255, (c >> 16) & 255, 255} {
				// f32(v)/255 stored as unorm8: round(x*255) == v exactly.
				out[o] = byte(float32(float32(v)/255)*255 + 0.5)
				o++
			}
		}
	}
	return out
}
