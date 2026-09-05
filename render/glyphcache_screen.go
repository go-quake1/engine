// Copyright (c) 1996-1997 Id Software, Inc.
// Copyright (c) 2026 the go-quake1/engine authors.
// SPDX-License-Identifier: GPL-2.0-or-later

package render

// DrawConsoleCached is the retained-mode fast path for [Screen.DrawConsole]:
// identical row-walking + backscroll-arrow logic, but every glyph is
// emitted through the pre-expanded [GlyphCache] tiles instead of
// re-decoding the conchars sheet per character. Output is
// byte-identical to [Screen.DrawConsole] built from the same sheet.
//
// The cache MUST have been built for fb.EffectiveScale() (call
// [GlyphCache.EnsureFor] on a video-mode / HUDScale change); a mismatch
// surfaces as the per-glyph ErrGlyphBadScale.
//
// Errors: [ErrScreenDrawFB] / [ErrScreenCons] on nil fb/con,
// [ErrScreenChars] on a nil cache. Per-glyph errors are surfaced.
//
// tyrquake: Con_DrawConsole (console.c lines 576-630).
func (s *Screen) DrawConsoleCached(fb *FrameBuffer, con *Console, gc *GlyphCache) error {
	if fb == nil {
		return ErrScreenDrawFB
	}
	if con == nil {
		return ErrScreenCons
	}
	if gc == nil {
		return ErrScreenChars
	}
	rows := s.CharRows()
	if rows <= 0 {
		return nil
	}
	bottomY := s.ConCurrent - CharHeight
	const colXOffset = CharWidth
	cols := con.Width
	if maxCols := (fb.VWidth() - colXOffset) / CharWidth; maxCols < cols {
		cols = maxCols
	}
	if cols <= 0 {
		return nil
	}
	for i := 0; i < rows; i++ {
		y := bottomY - i*CharHeight
		if i == 0 && con.BackScroll > 0 {
			for x := 0; x < cols; x += 4 {
				if err := gc.DrawCharacter(fb, colXOffset+x*CharWidth, y, '^'); err != nil {
					return err
				}
			}
			continue
		}
		rowIdx := con.VisibleRow(i)
		if rowIdx < 0 {
			continue
		}
		for x := 0; x < cols; x++ {
			ch := con.Cell(x, rowIdx)
			if err := gc.DrawCharacter(fb, colXOffset+x*CharWidth, y, ch); err != nil {
				return err
			}
		}
	}
	return nil
}

// DrawNotifyCached is the retained-mode fast path for [Screen.DrawNotify]:
// identical notify-row walking, cached-glyph blits. Byte-identical to
// [Screen.DrawNotify].
//
// tyrquake: Con_DrawNotify (console.c lines 449-510).
func (s *Screen) DrawNotifyCached(fb *FrameBuffer, con *Console, gc *GlyphCache, now, lifetime float32, maxRows int) error {
	if fb == nil {
		return ErrScreenDrawFB
	}
	if con == nil {
		return ErrScreenCons
	}
	if gc == nil {
		return ErrScreenChars
	}
	rows := con.NotifyRows(now, lifetime, maxRows)
	if len(rows) == 0 {
		return nil
	}
	const colXOffset = CharWidth
	cols := con.Width
	if maxCols := (fb.VWidth() - colXOffset) / CharWidth; maxCols < cols {
		cols = maxCols
	}
	if cols <= 0 {
		return nil
	}
	for i, rowIdx := range rows {
		y := i * CharHeight
		for x := 0; x < cols; x++ {
			ch := con.Cell(x, rowIdx)
			if err := gc.DrawCharacter(fb, colXOffset+x*CharWidth, y, ch); err != nil {
				return err
			}
		}
	}
	return nil
}
