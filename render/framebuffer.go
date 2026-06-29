// Copyright (c) 1996-1997 Id Software, Inc.
// Copyright (c) 2026 the go-quake1/engine authors.
// SPDX-License-Identifier: GPL-2.0-or-later

package render

import "errors"

// Error sentinels returned by the framebuffer constructors + ops.
var (
	ErrFBNegativeDim     = errors.New("render: framebuffer dimensions must be non-negative")
	ErrFBZeroDim         = errors.New("render: framebuffer dimensions must be positive")
	ErrFBPitchTooSmall   = errors.New("render: framebuffer pitch must be >= width")
	ErrFBPointOutOfRange = errors.New("render: point out of framebuffer range")
	ErrFBPaletteShape    = errors.New("render: palette must be exactly 256 entries")
	ErrFBDstTooSmall     = errors.New("render: destination buffer too small for expand")
)

// FrameBuffer is Quake 1's palettized 8-bit display surface. Each
// pixel is a single byte indexing into a 256-entry RGB palette (set
// out-of-band; the framebuffer carries no palette of its own --
// callers thread the palette through Expand calls). tyrquake: the
// `vid.buffer` byte array + `vid.width` / `vid.height` in vid.h.
//
// HUDScale is the integer upscale the 2D layer (HUD, menu, console,
// centerprint, intermission) renders at: 1 = vanilla 320-wide layout
// at 1:1 (everything painted natively); 2 = each virtual-coordinate
// pixel becomes a 2x2 block on physical fb.Pixels so the bottom-bar
// + menu + text overlays read at the same apparent size on a 900x700
// surface as they did at 320x240. The 3D renderer (BSP walk, alias
// models, sky, turbulent) is NOT affected by HUDScale -- it always
// fills the full Width x Height viewport at native resolution.
// HUDScale <= 0 is treated as 1 (no upscale, vanilla layout).
//
// VWidth / VHeight return the "virtual" framebuffer dimensions the 2D
// layer should lay out against -- callers do their layout math against
// the virtuals and the low-level draw primitives (DrawPic, DrawTransPic,
// DrawCharacter, DrawFill) project to physical via HUDScale.
type FrameBuffer struct {
	Width    int
	Height   int
	Pitch    int    // bytes per scanline (>= Width; allows >stride layouts)
	Pixels   []byte // length == Pitch * Height
	HUDScale int    // integer upscale for the 2D layer; <= 0 means 1
}

// EffectiveScale returns the integer scale factor the 2D primitives
// apply, normalising HUDScale <= 0 to 1.
func (fb *FrameBuffer) EffectiveScale() int {
	if fb.HUDScale <= 0 {
		return 1
	}
	return fb.HUDScale
}

// VWidth is the virtual-coordinate width the 2D layer lays out
// against. With HUDScale == 1 it equals Width verbatim; with
// HUDScale == 2 on a 900x700 fb it is 450.
func (fb *FrameBuffer) VWidth() int {
	return fb.Width / fb.EffectiveScale()
}

// VHeight is the virtual-coordinate height the 2D layer lays out
// against. Mirror of VWidth.
func (fb *FrameBuffer) VHeight() int {
	return fb.Height / fb.EffectiveScale()
}

// NewFrameBuffer returns a FrameBuffer with Width=w, Height=h,
// Pitch=w (the tight-packed layout), and Pixels zeroed.
// Returns ErrFBZeroDim if w or h <= 0; ErrFBNegativeDim if either
// is negative (the < 0 check fires first).
func NewFrameBuffer(w, h int) (*FrameBuffer, error) {
	if w < 0 || h < 0 {
		return nil, ErrFBNegativeDim
	}
	if w == 0 || h == 0 {
		return nil, ErrFBZeroDim
	}
	return &FrameBuffer{
		Width:  w,
		Height: h,
		Pitch:  w,
		Pixels: make([]byte, w*h),
	}, nil
}

// NewFrameBufferAligned is the variant that lets the caller choose
// Pitch -- useful for SIMD-aligned backends. Same dim checks plus
// ErrFBPitchTooSmall if pitch < w.
func NewFrameBufferAligned(w, h, pitch int) (*FrameBuffer, error) {
	if w < 0 || h < 0 {
		return nil, ErrFBNegativeDim
	}
	if w == 0 || h == 0 {
		return nil, ErrFBZeroDim
	}
	if pitch < w {
		return nil, ErrFBPitchTooSmall
	}
	return &FrameBuffer{
		Width:  w,
		Height: h,
		Pitch:  pitch,
		Pixels: make([]byte, pitch*h),
	}, nil
}

// Clear sets every byte in Pixels to the given palette index.
// Tight loop; no bounds check inside.
func (fb *FrameBuffer) Clear(paletteIdx byte) {
	for i := range fb.Pixels {
		fb.Pixels[i] = paletteIdx
	}
}

// SetPixel writes paletteIdx at (x, y). Returns ErrFBPointOutOfRange
// if x or y is outside [0,Width) x [0,Height). The Pitch indexing
// means the wire offset is y*Pitch + x.
func (fb *FrameBuffer) SetPixel(x, y int, paletteIdx byte) error {
	if x < 0 || x >= fb.Width || y < 0 || y >= fb.Height {
		return ErrFBPointOutOfRange
	}
	fb.Pixels[y*fb.Pitch+x] = paletteIdx
	return nil
}

// GetPixel reads paletteIdx at (x, y). Same bounds rule.
func (fb *FrameBuffer) GetPixel(x, y int) (byte, error) {
	if x < 0 || x >= fb.Width || y < 0 || y >= fb.Height {
		return 0, ErrFBPointOutOfRange
	}
	return fb.Pixels[y*fb.Pitch+x], nil
}

// Palette is a 256-entry RGB lookup table. R/G/B each 0..255. Caller
// loads this from the gfx.wad PALETTE lump (a sibling agent owns
// that loader). The framebuffer doesn't store a palette itself --
// Expand takes one per-call so callers can swap palettes per frame
// (the rare gun-flash / underwater shifted-palette effect).
type Palette [256][3]byte

// Expand writes the framebuffer's palette-indexed pixels into dst as
// RGBA8 (4 bytes per pixel, A=255). Returns ErrFBDstTooSmall if
// len(dst) < Width*Height*4.
//
// Performance note: a follow-up SIMD path will replace the per-pixel
// loop with a table-driven gather. For this commit, the scalar
// version is correct + readable.
func (fb *FrameBuffer) Expand(dst []byte, pal *Palette) error {
	if len(dst) < fb.Width*fb.Height*4 {
		return ErrFBDstTooSmall
	}
	o := 0
	for y := 0; y < fb.Height; y++ {
		row := fb.Pixels[y*fb.Pitch:]
		for x := 0; x < fb.Width; x++ {
			rgb := pal[row[x]]
			dst[o+0] = rgb[0]
			dst[o+1] = rgb[1]
			dst[o+2] = rgb[2]
			dst[o+3] = 255
			o += 4
		}
	}
	return nil
}

// ExpandTo32 is the 32-bit-word variant for backends that take a
// uint32 buffer (TamaGo display drivers + the SDL ARGB / RGBA paths).
// Each output word is packed as (A<<24) | (R<<16) | (G<<8) | B
// regardless of host endianness. ErrFBDstTooSmall if len(dst) <
// Width*Height.
func (fb *FrameBuffer) ExpandTo32(dst []uint32, pal *Palette) error {
	if len(dst) < fb.Width*fb.Height {
		return ErrFBDstTooSmall
	}
	o := 0
	for y := 0; y < fb.Height; y++ {
		row := fb.Pixels[y*fb.Pitch:]
		for x := 0; x < fb.Width; x++ {
			rgb := pal[row[x]]
			dst[o] = (uint32(255) << 24) |
				(uint32(rgb[0]) << 16) |
				(uint32(rgb[1]) << 8) |
				uint32(rgb[2])
			o++
		}
	}
	return nil
}
