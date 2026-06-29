// Copyright (c) 1996-1997 Id Software, Inc.
// Copyright (c) 2026 the go-quake1/engine authors.
// SPDX-License-Identifier: GPL-2.0-or-later

package render

import "errors"

// Screen is the high-level composition state that drives the per-frame
// pipeline: track the framebuffer dimensions (so the per-pixel hot
// loops can avoid a divide), the animated drop-down position of the
// developer console, and the per-tick scroll speed.
//
// tyrquake: a hand-rolled bundle of vid_t (vid.width/vid.height), the
// scr_* globals (scr_conlines, scr_con_current, scr_conspeed) in
// screen.c, and the con_* current-state values in console.c. The C
// upstream scatters these across two translation units + a handful of
// cvars; here they collapse into a single value type the renderer
// threads through its draw entrypoints.
type Screen struct {
	Width       int // framebuffer pixel width
	Height      int // framebuffer pixel height
	CenterX     int // Width/2 (cached so the per-pixel hot loops don't divide)
	CenterY     int // Height/2
	ConLines    int // current visible console row count (pixels, NOT char rows)
	ConCurrent  int // current animated drop-down position (0 = closed, ConLines = fully open)
	ScrollSpeed int // per-tick advance for the drop-down animation (pixels/tic)
}

// Error sentinels returned by the Screen entrypoints.
var (
	ErrScreenDim    = errors.New("render: screen dimensions must be positive")
	ErrScreenDrawFB = errors.New("render: nil framebuffer in screen op")
	ErrScreenCons   = errors.New("render: nil console in screen op")
	ErrScreenChars  = errors.New("render: nil conchars Pic in screen op")
)

// DefaultScrollSpeed is the per-tick console drop-down advance, in
// framebuffer pixels. tyrquake derives the same magnitude from
// `scr_conspeed * host_frametime * vid.height / 200` in
// SCR_SetUpToDrawConsole; we cache the integer result on the Screen
// rather than re-deriving it every tick.
const DefaultScrollSpeed = 8

// NewScreen returns a Screen with derived CenterX/CenterY + sensible
// defaults for ConLines/ConCurrent/ScrollSpeed.
//
// ConLines defaults to height/2 (Quake's "half-screen" console mode,
// see screen.c line 379), ConCurrent defaults to 0 (console closed),
// and ScrollSpeed defaults to [DefaultScrollSpeed] pixels per tick.
//
// Errors: [ErrScreenDim] if width <= 0 OR height <= 0.
func NewScreen(width, height int) (*Screen, error) {
	if width <= 0 || height <= 0 {
		return nil, ErrScreenDim
	}
	return &Screen{
		Width:       width,
		Height:      height,
		CenterX:     width / 2,
		CenterY:     height / 2,
		ConLines:    height / 2,
		ConCurrent:  0,
		ScrollSpeed: DefaultScrollSpeed,
	}, nil
}

// AnimateConsole advances ConCurrent toward `target` by ScrollSpeed
// pixels. Called once per frame. tyrquake: SCR_UpdateScreen's
// per-frame "scr_conlines vs scr_con_current" arithmetic in
// SCR_SetUpToDrawConsole (screen.c lines 388-396).
//
// `target` is 0 (closing) or s.ConLines (opening); intermediate
// frames lerp via ScrollSpeed. If the remaining distance is smaller
// than ScrollSpeed (or ScrollSpeed is non-positive) the value snaps
// straight to `target` rather than overshooting.
func (s *Screen) AnimateConsole(target int) {
	if s.ConCurrent == target {
		return
	}
	step := s.ScrollSpeed
	if step <= 0 {
		// Degenerate speed -> snap. Avoids an infinite stall when a
		// caller mis-configures the field.
		s.ConCurrent = target
		return
	}
	if s.ConCurrent < target {
		s.ConCurrent += step
		if s.ConCurrent > target {
			s.ConCurrent = target
		}
		return
	}
	s.ConCurrent -= step
	if s.ConCurrent < target {
		s.ConCurrent = target
	}
}

// CharRows returns the number of complete character rows visible in
// the current ConCurrent drop-down position.
//
// tyrquake: scr_con_current / chars->height (where chars->height is
// the conchars glyph height, [CharHeight] = 8). Integer division --
// a partial row at the top is not counted.
func (s *Screen) CharRows() int {
	if s.ConCurrent <= 0 {
		return 0
	}
	return s.ConCurrent / CharHeight
}

// DrawConsole renders the dropdown console into fb. Walks the most
// recent (s.ConCurrent / CharHeight) rows of con and DrawCharacters
// each glyph, anchored against the bottom of the visible drop-down
// region.
//
// `chars` is the 128x128 conchars Pic (loaded once by the caller from
// the gfx.wad conchars lump). The function does NOT clear the console
// region first -- the caller decides whether to DrawFill the
// background.
//
// When [Console.BackScroll] is non-zero the bottom row is replaced
// with a row of '^' markers, matching tyrquake's
// Con_DrawConsole "draw arrows to show the buffer is backscrolled"
// path (console.c lines 597-603).
//
// Errors: [ErrScreenDrawFB] / [ErrScreenCons] / [ErrScreenChars] on
// nil inputs. Per-glyph DrawCharacter failures are also surfaced.
//
// tyrquake: Con_DrawConsole (console.c lines 576-630).
func (s *Screen) DrawConsole(fb *FrameBuffer, con *Console, chars *Pic) error {
	if fb == nil {
		return ErrScreenDrawFB
	}
	if con == nil {
		return ErrScreenCons
	}
	if chars == nil {
		return ErrScreenChars
	}
	rows := s.CharRows()
	if rows <= 0 {
		return nil
	}
	// y for the BOTTOM-most row of the drop-down. Walking upward,
	// each row's baseline is `y - i*CharHeight`. tyrquake anchors the
	// bottom of the text region at scr_con_current; we do the same.
	bottomY := s.ConCurrent - CharHeight
	// One column of horizontal padding so the glyphs don't kiss the
	// left edge -- matches tyrquake's `(x + 1) << 3` offset.
	const colXOffset = CharWidth

	// Number of text columns actually drawn = min(framebuffer cols,
	// console width). The framebuffer may be narrower than the
	// console's column count on small video modes.
	cols := con.Width
	// VWidth so the column count is in the 2D layer's virtual
	// coordinate space (HUDScale-aware). At HUDScale == 1 it's
	// identical to fb.Width.
	if maxCols := (fb.VWidth() - colXOffset) / CharWidth; maxCols < cols {
		cols = maxCols
	}
	if cols <= 0 {
		return nil
	}

	for i := 0; i < rows; i++ {
		y := bottomY - i*CharHeight
		// On the bottom row, when the buffer is back-scrolled, draw
		// the arrow marker instead of the literal text. tyrquake
		// puts the arrows just above the input line; we collapse
		// that to the bottom of the visible region since the input
		// line lives in a follow-up batch.
		if i == 0 && con.BackScroll > 0 {
			for x := 0; x < cols; x += 4 {
				if err := DrawCharacter(fb, chars, colXOffset+x*CharWidth, y, '^'); err != nil {
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
			if err := DrawCharacter(fb, chars, colXOffset+x*CharWidth, y, ch); err != nil {
				return err
			}
		}
	}
	return nil
}

// DrawNotify renders the per-row notify overlay (the lines that have
// been printed within `lifetime` seconds of `now`). Walks at most
// `maxRows` rows starting from the top of the screen.
//
// `chars` is the 128x128 conchars Pic (same source as DrawConsole).
//
// Errors: [ErrScreenDrawFB] / [ErrScreenCons] / [ErrScreenChars] on
// nil inputs. Per-glyph DrawCharacter failures are also surfaced.
//
// tyrquake: Con_DrawNotify (console.c lines 449-510). The chat
// prompt + input line at the bottom of the upstream's notify path
// lives in a follow-up batch (it needs the key-binding subsystem).
func (s *Screen) DrawNotify(fb *FrameBuffer, con *Console, chars *Pic, now, lifetime float32, maxRows int) error {
	if fb == nil {
		return ErrScreenDrawFB
	}
	if con == nil {
		return ErrScreenCons
	}
	if chars == nil {
		return ErrScreenChars
	}
	rows := con.NotifyRows(now, lifetime, maxRows)
	if len(rows) == 0 {
		return nil
	}
	const colXOffset = CharWidth
	cols := con.Width
	// VWidth: column count in virtual-coordinate space (the
	// physical fb.Width is upscaled by HUDScale, the virtual is
	// what the 2D layer addresses). At HUDScale == 1 it's a no-op.
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
			if err := DrawCharacter(fb, chars, colXOffset+x*CharWidth, y, ch); err != nil {
				return err
			}
		}
	}
	return nil
}
