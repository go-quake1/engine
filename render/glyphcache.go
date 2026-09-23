// Copyright (c) 1996-1997 Id Software, Inc.
// Copyright (c) 2026 the go-quake1/engine authors.
// SPDX-License-Identifier: GPL-2.0-or-later

package render

import "errors"

// ErrGlyphBadScale is returned by [NewGlyphCache] / [GlyphCache.EnsureFor]
// when the requested integer upscale is < 1.
var ErrGlyphBadScale = errors.New("render: glyph cache scale must be >= 1")

// GlyphCache is a retained-mode fast path for the 2D overlay layer
// (console, menu, HUD text, centerprint). The per-frame overlay is
// dominated by [DrawCharacter], which for EVERY glyph re-derives the
// source origin in the 128x128 conchars sheet, walks the sheet with a
// strided (128-wide) read, and -- critically -- performs a per-pixel
// integer divide (`(off+u)/scale`) to project virtual pixels onto the
// HUDScale-upscaled framebuffer. That work is identical on every frame
// because the charset and scale don't change between video-mode
// switches, yet it is redone tens of thousands of times per second
// (a full 80x25 console screen is 2000 glyphs * 64+ pixels each).
//
// GlyphCache pre-expands each of the 256 conchars glyphs ONCE, at the
// active scale, into a contiguous, ready-to-blit tile (8*scale wide,
// 8*scale tall, row-major). The per-frame draw then collapses to a
// contiguous-row copy with the same transparent-index skip -- no
// row/col math, no strided sheet reads, no per-pixel divide, no
// per-call shape validation. Output is BYTE-IDENTICAL to
// [DrawCharacter] (see glyphcache_test.go's exhaustive parity test).
//
// Cache key: (conchars sheet identity, scale). The glyph tiles hold
// palette-INDEXED bytes exactly as the framebuffer does -- the RGB
// palette is applied downstream by [FrameBuffer.Expand], AFTER the
// whole 2D layer is composited, so a palette swap (the gun-flash /
// underwater tint) does NOT invalidate the cache: the indexed glyph
// bytes are palette-independent. Only a charset reload (video-mode
// change re-decodes conchars) or a HUDScale change requires a rebuild;
// [GlyphCache.EnsureFor] performs that invalidation.
//
// A GlyphCache is not safe for concurrent mutation (EnsureFor), but
// the engine's 2D layer runs single-threaded in the main loop, matching
// the upstream tyrquake draw model.
type GlyphCache struct {
	chars *Pic     // source sheet identity (pointer compared for invalidation)
	scale int      // integer upscale the tiles were expanded at (>= 1)
	dim   int      // tile side length in physical pixels == CharWidth*scale
	tiles [][]byte // 256 entries; tiles[ch] is dim*dim palette-indexed bytes
}

// validateChars checks the conchars sheet is the exact 128x128 shape
// the tile expander assumes. Shared with [DrawCharacter]'s guard.
func validateChars(chars *Pic) error {
	if chars == nil {
		return ErrDrawCharsNilSrc
	}
	if chars.Width != conSheetDim || chars.Height != conSheetDim ||
		len(chars.Pixels) != conSheetDim*conSheetDim {
		return ErrDrawCharsShape
	}
	return nil
}

// NewGlyphCache builds a glyph cache for the given 128x128 conchars
// sheet at the given integer scale (the framebuffer's HUDScale;
// [FrameBuffer.EffectiveScale] normalises <= 0 to 1, so pass that).
//
// Errors:
//
//	ErrDrawCharsNilSrc  chars == nil
//	ErrDrawCharsShape   chars not exactly 128x128
//	ErrGlyphBadScale    scale < 1
func NewGlyphCache(chars *Pic, scale int) (*GlyphCache, error) {
	if err := validateChars(chars); err != nil {
		return nil, err
	}
	if scale < 1 {
		return nil, ErrGlyphBadScale
	}
	gc := &GlyphCache{}
	gc.build(chars, scale)
	return gc, nil
}

// build (re)populates the 256 glyph tiles from chars at scale. It is
// the sole writer of the cache fields; callers reach it via
// [NewGlyphCache] or [GlyphCache.EnsureFor].
func (gc *GlyphCache) build(chars *Pic, scale int) {
	dim := CharWidth * scale // == CharHeight*scale (glyphs are square)
	tiles := make([][]byte, 256)
	// One backing allocation for all 256 tiles keeps the glyphs
	// contiguous in memory (cache-friendly) and avoids 256 separate
	// heap objects -- matching the engine's arena discipline.
	backing := make([]byte, 256*dim*dim)
	for ch := 0; ch < 256; ch++ {
		tile := backing[ch*dim*dim : (ch+1)*dim*dim : (ch+1)*dim*dim]
		// Source glyph origin in the 16x16 grid (same math as
		// DrawCharacter: row = ch>>4, col = ch&15).
		srcX := (ch & 15) * CharWidth
		srcY := (ch >> 4) * CharHeight
		for pv := 0; pv < dim; pv++ {
			// pv/scale is the source row inside the 8x8 glyph; the
			// divide runs ONCE per output row here, at build time,
			// instead of once per pixel per frame.
			srcRow := chars.Pixels[(srcY+pv/scale)*conSheetDim+srcX:]
			dstRow := tile[pv*dim:]
			for pu := 0; pu < dim; pu++ {
				dstRow[pu] = srcRow[pu/scale]
			}
		}
		tiles[ch] = tile
	}
	gc.chars = chars
	gc.scale = scale
	gc.dim = dim
	gc.tiles = tiles
}

// Scale reports the integer upscale the cached tiles were expanded at.
func (gc *GlyphCache) Scale() int { return gc.scale }

// Chars reports the conchars sheet the cache was built from (identity
// used for invalidation).
func (gc *GlyphCache) Chars() *Pic { return gc.chars }

// EnsureFor validates the cache against the desired (chars, scale) and
// rebuilds the tiles if either differs from what is cached. It is the
// invalidation hook the renderer calls on a video-mode / charset /
// HUDScale change (a palette swap does NOT need it -- see the type
// doc). Returns true if a rebuild happened.
//
// Errors mirror [NewGlyphCache]; on error the existing tiles are left
// untouched so a bad call can't corrupt a live cache.
func (gc *GlyphCache) EnsureFor(chars *Pic, scale int) (rebuilt bool, err error) {
	if err := validateChars(chars); err != nil {
		return false, err
	}
	if scale < 1 {
		return false, ErrGlyphBadScale
	}
	if gc.chars == chars && gc.scale == scale {
		return false, nil
	}
	gc.build(chars, scale)
	return true, nil
}

// DrawCharacter blits one cached glyph tile for byte ch into fb at the
// VIRTUAL coordinate (x, y). It is the drop-in fast-path replacement
// for the free [DrawCharacter] function and is byte-identical to it.
//
// The framebuffer's [FrameBuffer.EffectiveScale] MUST equal the cache's
// scale (the tiles were expanded for that scale); a mismatch returns
// ErrGlyphBadScale so the caller re-invalidates via [GlyphCache.EnsureFor]
// rather than silently drawing at the wrong size.
//
// Errors:
//
//	ErrDrawNilFB      fb == nil
//	ErrGlyphBadScale  fb.EffectiveScale() != gc.scale
//
// ch == 0 (NUL) and ch == ' ' (space, blank glyph) are no-ops, exactly
// as [DrawCharacter] short-circuits them.
func (gc *GlyphCache) DrawCharacter(fb *FrameBuffer, x, y int, ch byte) error {
	if fb == nil {
		return ErrDrawNilFB
	}
	if fb.EffectiveScale() != gc.scale {
		return ErrGlyphBadScale
	}
	if ch == 0 || ch == ' ' {
		return nil
	}
	scale := gc.scale
	dim := gc.dim
	tile := gc.tiles[ch]
	dx, dy, sxOff, syOff, cw, hh := clipRect(x*scale, y*scale, dim, dim, fb.Width, fb.Height)
	if cw == 0 || hh == 0 {
		return nil
	}
	for v := 0; v < hh; v++ {
		srcRow := tile[(syOff+v)*dim+sxOff:]
		dstRow := fb.Pixels[(dy+v)*fb.Pitch+dx:]
		for u := 0; u < cw; u++ {
			b := srcRow[u]
			if b != TransparentIndex {
				dstRow[u] = b
			}
		}
	}
	return nil
}

// DrawString is the cached counterpart of the free [DrawString]:
// blits s at (x, y) using the white glyph set, advancing [CharWidth]
// per glyph. Propagates the first per-glyph error.
func (gc *GlyphCache) DrawString(fb *FrameBuffer, x, y int, s string) error {
	for i := 0; i < len(s); i++ {
		if err := gc.DrawCharacter(fb, x+i*CharWidth, y, s[i]); err != nil {
			return err
		}
	}
	return nil
}

// DrawColorString is the cached counterpart of [DrawColorString]:
// OR's [HighBitMask] into each byte to select the yellow glyph variant.
func (gc *GlyphCache) DrawColorString(fb *FrameBuffer, x, y int, s string) error {
	for i := 0; i < len(s); i++ {
		if err := gc.DrawCharacter(fb, x+i*CharWidth, y, s[i]|HighBitMask); err != nil {
			return err
		}
	}
	return nil
}

// DrawCenteredString is the cached counterpart of [DrawCenteredString]:
// blits s horizontally centred on centerX at row y.
func (gc *GlyphCache) DrawCenteredString(fb *FrameBuffer, centerX, y int, s string) error {
	width := len(s) * CharWidth
	left := centerX - width/2
	return gc.DrawString(fb, left, y, s)
}
