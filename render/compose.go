// Copyright (c) 1996-1997 Id Software, Inc.
// Copyright (c) 2026 the go-quake1/engine authors.
// SPDX-License-Identifier: GPL-2.0-or-later

package render

import "errors"

// Compose2D is the 2D-only frame composer: it clears the framebuffer
// to a background fill, draws the drop-down console at its current
// animated position, and lays the notify-line overlay on top. The
// 3D world walk + alias model rendering + status-bar HUD layer over
// the result in follow-up batches.
//
// This is the minimum-viable per-frame pipeline: with just the
// renderer foundation + draw primitives + console state, a host
// loop can already produce visible frames (console + notify text)
// even before the 3D path lands.
//
// tyrquake: the 2D portion of SCR_UpdateScreen in screen.c -- the
// background fill + Con_DrawConsole + Con_DrawNotify calls, minus
// the 3D world view + Sbar_Draw + menu/HUD layers.
type FrameContext struct {
	Screen  *Screen
	Console *Console
	Chars   *Pic
	Palette *Palette

	Now            float32 // wall-clock-like time for the notify overlay
	NotifyLifetime float32 // seconds a notify row stays visible
	MaxNotifyRows  int     // upper bound on the overlay row count

	BackgroundIdx byte // palette index for the framebuffer fill

	// SkipBackgroundFill suppresses the per-frame DrawFill that
	// clears the framebuffer to BackgroundIdx. Set when the caller
	// has already rasterized a 3D scene into fb (via a Runner
	// Pre2DDraw hook or equivalent) and Compose2D's job is to
	// overlay the 2D layers (console + notify) on top WITHOUT
	// wiping the 3D pixels first.
	SkipBackgroundFill bool

	// CenterPrintText is the optional banner string Compose2D draws
	// horizontally-centered at y = fb.Height * 2 / 5 (the upstream
	// SCR_CheckDrawCenterString anchor point: roughly 40% of screen
	// height). Empty string = no draw. tyrquake: scr_centerstring.
	CenterPrintText string

	// CenterPrintExpiry is the wall-clock (in Now's units) at which
	// the centerprint stops being drawn. When Now >= CenterPrintExpiry
	// the centerprint is treated as expired (no draw), matching
	// upstream's scr_centertime_off countdown. Zero = no active
	// centerprint (a freshly-zeroed FrameContext draws nothing).
	CenterPrintExpiry float32

	// Intermission, when true, switches Compose2D into intermission
	// mode: the centerprint banner is suppressed (intermission
	// supersedes any active "you got the shotgun" overlay -- mirrors
	// tyrquake's scr_centertime_off = 0 reset inside the
	// svc_intermission arm), and IntermissionLines is drawn as a
	// centered block of horizontally-centered text rows instead.
	// The in-game status-bar HUD is suppressed too once that layer
	// lands; the flag is plumbed now so callers wiring it from
	// [client.State.Intermission] don't need a follow-up change.
	//
	// IntermissionLines is the per-line text block (top-to-bottom,
	// each line drawn horizontally centered on Screen.CenterX).
	// An empty slice + Intermission == true draws nothing
	// intermission-side -- the renderer is tolerant of "Intermission
	// flag flipped but the runloop has not composed the line block
	// yet" so the next frame just renders the empty intermission
	// screen.
	//
	// Vertical layout: the block is centered around y = fb.Height /
	// 2; lines step by [CharHeight] pixels each. Out-of-frame rows
	// are clipped per-character by [DrawCharacter].
	//
	// tyrquake: SCR_DrawIntermission + Sbar_IntermissionOverlay --
	// the C upstream draws a "complete" header pic + per-stat rows
	// (TIME / SECRETS / KILLS) inside the dimmed framebuffer. The
	// Go port collapses that into one slice the runloop composes
	// from the client's stat bank.
	Intermission      bool
	IntermissionLines []string
}

var (
	ErrComposeNilFB      = errors.New("render: nil framebuffer in compose")
	ErrComposeNilScreen  = errors.New("render: nil screen in compose")
	ErrComposeNilConsole = errors.New("render: nil console in compose")
	ErrComposeNilChars   = errors.New("render: nil chars Pic in compose")
)

// Compose2D paints one full 2D frame into fb:
//
//  1. DrawFill the whole framebuffer to ctx.BackgroundIdx (skipped
//     when ctx.SkipBackgroundFill is true).
//  2. If the console is open (Screen.ConCurrent > 0): DrawConsole.
//  3. DrawNotify the recent-rows overlay.
//  4. If CenterPrintText is non-empty AND Now < CenterPrintExpiry,
//     DrawCenteredString the banner at y = fb.Height * 2 / 5.
//
// Returns:
//
//	ErrComposeNilFB        fb == nil
//	ErrComposeNilScreen    ctx.Screen == nil
//	ErrComposeNilConsole   ctx.Console == nil
//	ErrComposeNilChars     ctx.Chars == nil
//	(propagated draw errors)
//
// The palette field is NOT consumed by Compose2D itself (the
// composer writes palette-indexed bytes, not RGBA); it is bundled in
// the context for the convenience ExpandFrame helper that pipes
// the rendered framebuffer through Palette.Expand for display.
func Compose2D(fb *FrameBuffer, ctx FrameContext) error {
	if fb == nil {
		return ErrComposeNilFB
	}
	if ctx.Screen == nil {
		return ErrComposeNilScreen
	}
	if ctx.Console == nil {
		return ErrComposeNilConsole
	}
	if ctx.Chars == nil {
		return ErrComposeNilChars
	}

	// DrawFill's only error path is nil-fb, already caught above;
	// the call is infallible here. Skipped when the caller has
	// pre-drawn a 3D scene into fb.
	//
	// VWidth / VHeight so the wash matches the rest of the 2D
	// layer's coordinate space; at HUDScale == 1 this is identical
	// to the physical fb.Width / fb.Height.
	if !ctx.SkipBackgroundFill {
		_ = DrawFill(fb, 0, 0, fb.VWidth(), fb.VHeight(), ctx.BackgroundIdx)
	}

	if ctx.Screen.ConCurrent > 0 {
		if err := ctx.Screen.DrawConsole(fb, ctx.Console, ctx.Chars); err != nil {
			return err
		}
	}

	if err := ctx.Screen.DrawNotify(
		fb, ctx.Console, ctx.Chars,
		ctx.Now, ctx.NotifyLifetime, ctx.MaxNotifyRows,
	); err != nil {
		return err
	}

	// Intermission overlay supersedes the centerprint banner. When
	// the intermission flag is set, draw the per-line scoreboard /
	// finale text block centered vertically around fb.Height / 2.
	// Centerprint is intentionally NOT drawn in this branch; the
	// applyIntermission / applyFinale arms also clear the
	// centerprint state, but the render side guards too in case a
	// caller composes the FrameContext without going through the
	// client.State plumbing.
	if ctx.Intermission {
		// VHeight so the intermission block centers in virtual
		// space; at HUDScale == 1 this is identical to physical.
		if err := drawIntermission(fb, ctx.Chars, ctx.Screen.CenterX, fb.VHeight(), ctx.IntermissionLines); err != nil {
			return err
		}
		return nil
	}

	// Centerprint overlay. Drawn on top of console + notify so the
	// banner stays visible during a drop-down. Active iff the text is
	// non-empty AND the expiry is in the future (matches tyrquake's
	// scr_centertime_off > 0 guard in SCR_CheckDrawCenterString).
	if ctx.CenterPrintText != "" && ctx.Now < ctx.CenterPrintExpiry {
		// Anchor at 40% of screen height; horizontally centered on
		// CenterX (cached on Screen so the per-pixel hot loops don't
		// divide). tyrquake's SCR_DrawCenterString uses
		// vid.height * 0.35 for single-line messages; we round up to
		// 2/5 to keep the integer division clean and still hit the
		// upstream anchor band. VHeight so the anchor is in virtual
		// space (matches Screen.CenterX -- a virtual-space coord).
		y := fb.VHeight() * 2 / 5
		if err := DrawCenteredString(fb, ctx.Chars, ctx.Screen.CenterX, y, ctx.CenterPrintText); err != nil {
			return err
		}
	}

	return nil
}

// drawIntermission lays out the per-line scoreboard / finale text
// block centered vertically around vHeight / 2 (virtual coords).
// Each line is horizontally centered on centerX via
// [DrawCenteredString]; lines step by [CharHeight] pixels. An empty
// slice draws nothing (the renderer is tolerant of "Intermission
// flag flipped but no lines composed yet" -- the next frame just
// renders the empty intermission screen).
//
// vHeight is the framebuffer's virtual height (fb.VHeight()) so the
// vertical centering matches the rest of the 2D layer at HUDScale > 1.
//
// Propagates the first DrawCenteredString error (which can only
// be ErrDrawCharsShape from a malformed chars Pic).
func drawIntermission(fb *FrameBuffer, chars *Pic, centerX, vHeight int, lines []string) error {
	n := len(lines)
	if n == 0 {
		return nil
	}
	blockHeight := n * CharHeight
	y0 := vHeight/2 - blockHeight/2
	for i, line := range lines {
		if err := DrawCenteredString(fb, chars, centerX, y0+i*CharHeight, line); err != nil {
			return err
		}
	}
	return nil
}

// ExpandFrame runs Compose2D + then expands the resulting palette-
// indexed framebuffer to RGBA8 in dst (length must be >= fb.Width *
// fb.Height * 4). Convenience for backends that want a one-call
// "give me an RGBA frame" entry point.
//
// Returns ErrComposeNilFB / ErrComposeNilScreen / ... per Compose2D
// PLUS any error from FrameBuffer.Expand (notably ErrFBDstTooSmall).
func ExpandFrame(fb *FrameBuffer, dst []byte, ctx FrameContext) error {
	if err := Compose2D(fb, ctx); err != nil {
		return err
	}
	if ctx.Palette == nil {
		return ErrFBPaletteShape
	}
	return fb.Expand(dst, ctx.Palette)
}
