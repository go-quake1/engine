// Copyright (c) 2026 the go-quake1/engine authors.
// SPDX-License-Identifier: GPL-2.0-or-later

// Package webgpupresent shows the software renderer's 8-bit frame
// through WebGPU (github.com/go-gfx/webgpu, GOOS=js): the index buffer is
// uploaded as an R8 texture, the palette as a 256x1 texture, and a
// fragment shader does the palette lookup and the scaling to the canvas.
// One Recorder submit per frame; the CPU never expands the frame to RGBA
// and never calls putImageData.
//
// This file is the platform-neutral part: the shader and the CPU
// reference the browser output is checked against, pixel for pixel. The
// Presenter itself is in presenter_js.go.
package webgpupresent

import "github.com/go-quake1/engine/render"

// ShaderWGSL is the presenter's shader: a full-screen triangle whose
// fragment stage reads the index texture (binding 0, r8uint) at the
// nearest source texel and looks the colour up in the palette texture
// (binding 1, rgba8unorm, 256x1). The output is opaque.
const ShaderWGSL = `
@group(0) @binding(0) var idx: texture_2d<u32>;
@group(0) @binding(1) var pal: texture_2d<f32>;

struct VOut {
  @builtin(position) pos: vec4f,
  @location(0) uv: vec2f,
};

@vertex fn vs(@builtin(vertex_index) i: u32) -> VOut {
  let p = vec2f(f32((i << 1u) & 2u), f32(i & 2u));
  var o: VOut;
  o.pos = vec4f(p * 2.0 - 1.0, 0.0, 1.0);
  o.uv = vec2f(p.x, 1.0 - p.y);
  return o;
}

@fragment fn fs(v: VOut) -> @location(0) vec4f {
  let d = vec2u(textureDimensions(idx));
  let t = min(vec2u(v.uv * vec2f(d)), d - vec2u(1u));
  let k = textureLoad(idx, t, 0).r;
  return vec4f(textureLoad(pal, vec2u(k, 0u), 0).rgb, 1.0);
}
`

// PaletteRGBA expands pal to the 256x1 RGBA8 texture the shader reads.
func PaletteRGBA(pal *render.Palette) []byte {
	out := make([]byte, 256*4)
	for i, c := range pal {
		out[4*i], out[4*i+1], out[4*i+2], out[4*i+3] = c[0], c[1], c[2], 255
	}
	return out
}

// Reference is the CPU's version of what the shader draws: the width x
// height index frame (pitch bytes per row) through pal, scaled to
// outW x outH by nearest source texel (the texel under the output
// pixel's centre), as RGBA8 rows. It is exact for a scale of 1 and for
// any integer upscale; for other ratios the float interpolation in the
// shader may pick the neighbouring texel on an exact boundary.
// ok is false for a non-positive size or a frame shorter than its
// dimensions say.
func Reference(pixels []byte, width, height, pitch int, pal *render.Palette, outW, outH int) (rgba []byte, ok bool) {
	if width < 1 || height < 1 || outW < 1 || outH < 1 || pitch < width || pal == nil ||
		len(pixels) < pitch*(height-1)+width {
		return nil, false
	}
	rgba = make([]byte, outW*outH*4)
	o := 0
	for oy := 0; oy < outH; oy++ {
		sy := (2*oy + 1) * height / (2 * outH)
		row := pixels[sy*pitch:]
		for ox := 0; ox < outW; ox++ {
			c := pal[row[(2*ox+1)*width/(2*outW)]]
			rgba[o], rgba[o+1], rgba[o+2], rgba[o+3] = c[0], c[1], c[2], 255
			o += 4
		}
	}
	return rgba, true
}
