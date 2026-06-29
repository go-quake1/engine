// Copyright (c) 1996-1997 Id Software, Inc.
// Copyright (c) 2026 the go-quake1/engine authors.
// SPDX-License-Identifier: GPL-2.0-or-later

package render

import "errors"

// Pic is a palette-indexed 2D bitmap (the engine's qpic_t equivalent).
// Pixels is row-major, tightly packed (length == Width*Height). The
// transparency convention is the same as upstream: palette index 255
// is "transparent" for DrawTransPic / DrawCharacter; opaque
// everywhere else. tyrquake: qpic_t in common.h.
type Pic struct {
	Width  int
	Height int
	Pixels []byte
}

// TransparentIndex is the palette slot the engine reserves for
// "skip this pixel" in the transparent-blit path. tyrquake hard-codes
// 255; we name the constant so the choice is documented.
const TransparentIndex byte = 255

// CharHeight + CharWidth are the conchars glyph dimensions.
// tyrquake: 8x8 glyphs packed into a 128x128 conchars texture (16
// columns x 16 rows). Each glyph is byte-packed palette indices.
const (
	CharWidth  = 8
	CharHeight = 8
)

// Sentinel errors returned by the draw primitives.
var (
	ErrPicNilSrc       = errors.New("render: nil source Pic")
	ErrPicShape        = errors.New("render: Pic dimensions / Pixels length mismatch")
	ErrDrawNilFB       = errors.New("render: nil destination framebuffer")
	ErrDrawCharsNilSrc = errors.New("render: nil conchars Pic")
	ErrDrawCharsShape  = errors.New("render: conchars Pic must be 128x128")
)

// conSheetDim is the on-disk dimension (square) of the conchars sheet.
// 16 columns * 8px = 128, 16 rows * 8px = 128. tyrquake: pic.stride =
// 128 in Draw_CharacterAlpha.
const conSheetDim = 128

// clipRect intersects the source rectangle (x,y,w,h) with the
// destination [0,dstW) x [0,dstH) rectangle. It returns the clamped
// destination origin (dx,dy), the number of source columns/rows
// skipped from the top-left (sx,sy), and the clipped (cw,ch). When
// the result is fully off-screen, cw or ch is 0.
//
// Matches the C upstream's two-step clip: source-offset on negative
// origin, end-clamp on overrun. tyrquake's Draw_* asserts the no-clip
// happy path; here we promote the clip to a real implementation so
// off-screen HUD draws (e.g. console-slide partial reveal) are safe.
func clipRect(x, y, w, h, dstW, dstH int) (dx, dy, sx, sy, cw, ch int) {
	dx, dy = x, y
	sx, sy = 0, 0
	cw, ch = w, h
	if dx < 0 {
		sx = -dx
		cw += dx // cw -= -dx
		dx = 0
	}
	if dy < 0 {
		sy = -dy
		ch += dy
		dy = 0
	}
	if dx+cw > dstW {
		cw = dstW - dx
	}
	if dy+ch > dstH {
		ch = dstH - dy
	}
	if cw < 0 {
		cw = 0
	}
	if ch < 0 {
		ch = 0
	}
	return
}

// DrawFill fills the framebuffer rectangle (x,y,w,h) with palette
// index c. Off-framebuffer pixels are silently clipped (zero-w/h
// after clipping = no-op). ErrDrawNilFB if fb == nil. tyrquake:
// Draw_Fill.
//
// Negative x or y values cause the source rectangle to be clipped
// to the framebuffer's [0,W) x [0,H) region; the function does not
// panic for out-of-range coords.
//
// (x, y, w, h) are VIRTUAL coordinates: each input pixel becomes a
// fb.EffectiveScale() x fb.EffectiveScale() block on the physical
// surface. The default (HUDScale == 0 or 1) is the vanilla 1:1
// behaviour: virtual == physical so existing call sites keep working.
func DrawFill(fb *FrameBuffer, x, y, w, h int, c byte) error {
	if fb == nil {
		return ErrDrawNilFB
	}
	scale := fb.EffectiveScale()
	dx, dy, _, _, cw, ch := clipRect(x*scale, y*scale, w*scale, h*scale, fb.Width, fb.Height)
	if cw == 0 || ch == 0 {
		return nil
	}
	for v := 0; v < ch; v++ {
		row := fb.Pixels[(dy+v)*fb.Pitch+dx:]
		for u := 0; u < cw; u++ {
			row[u] = c
		}
	}
	return nil
}

// DrawPic blits src (a palette-indexed Pic) into fb at (x,y).
// OPAQUE: every byte in src is written verbatim (no transparency
// handling -- callers needing the conchar-style skip use
// DrawTransPic). Clipped to the framebuffer bounds.
//
// Errors:
//
//	ErrDrawNilFB  fb == nil
//	ErrPicNilSrc  src == nil
//	ErrPicShape   len(src.Pixels) != src.Width*src.Height
//
// (x, y) and the pic's pixel dimensions are VIRTUAL: each source
// pixel becomes a fb.EffectiveScale() x fb.EffectiveScale() block.
// The default (HUDScale 0 or 1) preserves the 1:1 vanilla behaviour.
//
// tyrquake: Draw_Pic.
func DrawPic(fb *FrameBuffer, x, y int, src *Pic) error {
	if fb == nil {
		return ErrDrawNilFB
	}
	if src == nil {
		return ErrPicNilSrc
	}
	if len(src.Pixels) != src.Width*src.Height {
		return ErrPicShape
	}
	return blitPic(fb, x, y, src, false)
}

// DrawTransPic is DrawPic with the transparent-index skip: any
// source byte equal to TransparentIndex is NOT written to the
// destination. Same error shape; tyrquake: Draw_TransPic.
//
// (x, y) and the pic's pixel dimensions are VIRTUAL: scaled up by
// fb.EffectiveScale() (pixel-replicated) before clipping + blit.
func DrawTransPic(fb *FrameBuffer, x, y int, src *Pic) error {
	if fb == nil {
		return ErrDrawNilFB
	}
	if src == nil {
		return ErrPicNilSrc
	}
	if len(src.Pixels) != src.Width*src.Height {
		return ErrPicShape
	}
	return blitPic(fb, x, y, src, true)
}

// blitPic is the shared scale-aware pic-blit core for DrawPic +
// DrawTransPic. The trans flag selects the per-pixel transparent-
// index skip. (x, y) and src.Width / src.Height are VIRTUAL --
// each source pixel becomes a scale x scale block on fb.Pixels.
//
// Layout: with scale == s, a src pixel at (u, v) lands at physical
// (dx + u*s + du, dy + v*s + dv) for du, dv in 0..s. The clipRect
// step works in PHYSICAL coords (x*s, y*s, w*s, h*s) so partial
// off-screen blits drop only the truly-out-of-range physical
// pixels, never a fractional virtual cell. With scale == 1 the
// inner loops collapse to a per-pixel copy that matches the
// pre-HUDScale behaviour byte-for-byte.
func blitPic(fb *FrameBuffer, x, y int, src *Pic, trans bool) error {
	scale := fb.EffectiveScale()
	physW := src.Width * scale
	physH := src.Height * scale
	dx, dy, sxOff, syOff, cw, ch := clipRect(x*scale, y*scale, physW, physH, fb.Width, fb.Height)
	if cw == 0 || ch == 0 {
		return nil
	}
	for v := 0; v < ch; v++ {
		srcY := (syOff + v) / scale
		srcRow := src.Pixels[srcY*src.Width:]
		dstRow := fb.Pixels[(dy+v)*fb.Pitch+dx:]
		for u := 0; u < cw; u++ {
			b := srcRow[(sxOff+u)/scale]
			if trans && b == TransparentIndex {
				continue
			}
			dstRow[u] = b
		}
	}
	return nil
}

// DrawCharacter writes one 8x8 glyph from the conchars sheet into
// fb at (x,y). ch is an ASCII byte (0..255); the corresponding 8x8
// region of conchars is the source. Transparent-blit (skips
// TransparentIndex source pixels). Clipped to framebuffer bounds.
//
// chars is the 128x128 conchars Pic (loaded once by the caller from
// the conchars WAD lump; this function does not load it).
//
// Errors:
//
//	ErrDrawNilFB        fb == nil
//	ErrDrawCharsNilSrc  chars == nil
//	ErrDrawCharsShape   chars not exactly 128x128
//
// ch == 0 (NUL) is a no-op (matches tyrquake's Draw_Character
// short-circuit). ch == ' ' (0x20) is ALSO a no-op (the conchars
// space glyph is blank but the cycle saves matter at HUD scale).
//
// tyrquake: Draw_Character.
func DrawCharacter(fb *FrameBuffer, chars *Pic, x, y int, ch byte) error {
	if fb == nil {
		return ErrDrawNilFB
	}
	if chars == nil {
		return ErrDrawCharsNilSrc
	}
	if chars.Width != conSheetDim || chars.Height != conSheetDim ||
		len(chars.Pixels) != conSheetDim*conSheetDim {
		return ErrDrawCharsShape
	}
	if ch == 0 || ch == ' ' {
		return nil
	}
	// Source glyph origin in the 16x16 grid. tyrquake:
	//   row = num >> 4; col = num & 15;
	//   pixels = draw_chars + (row << 10) + (col << 3)
	row := int(ch >> 4)
	col := int(ch & 15)
	srcX := col * CharWidth
	srcY := row * CharHeight

	scale := fb.EffectiveScale()
	dx, dy, sxOff, syOff, cw, hh := clipRect(x*scale, y*scale, CharWidth*scale, CharHeight*scale, fb.Width, fb.Height)
	if cw == 0 || hh == 0 {
		return nil
	}
	for v := 0; v < hh; v++ {
		// Each physical row maps to one source row inside the 8x8
		// glyph; the divide-by-scale collapses to a no-op at
		// scale == 1 so the vanilla 1:1 path stays bit-exact.
		gv := (syOff + v) / scale
		srcRow := chars.Pixels[(srcY+gv)*conSheetDim+srcX:]
		dstRow := fb.Pixels[(dy+v)*fb.Pitch+dx:]
		for u := 0; u < cw; u++ {
			b := srcRow[(sxOff+u)/scale]
			if b != TransparentIndex {
				dstRow[u] = b
			}
		}
	}
	return nil
}
