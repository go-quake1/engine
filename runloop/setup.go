// Copyright (c) 1996-1997 Id Software, Inc.
// Copyright (c) 2026 the go-quake1/engine authors.
// SPDX-License-Identifier: GPL-2.0-or-later

package runloop

import (
	"errors"

	"github.com/go-quake1/engine/assets"
	"github.com/go-quake1/engine/backend"
	"github.com/go-quake1/engine/client"
	"github.com/go-quake1/engine/render"
	"github.com/go-quake1/engine/server"
	"github.com/go-quake1/engine/sound"
	"github.com/go-quake1/engine/vfs"
)

// SetupOpts bundles the constructor parameters for NewRunnerFromVFS.
// The vfs.SearchPath must already be populated (e.g. caller has
// added the pak0.pak or gfx.wad source). The backend supplies the
// preferred framebuffer dimensions via Size().
type SetupOpts struct {
	VFS     *vfs.SearchPath
	Host    HostFramer
	Client  *client.State
	Conn    server.NetConn
	Backend backend.Backend

	// Optional overrides; zero-value means "use the default".
	BackgroundIdx  byte    // default 0
	NotifyLifetime float32 // default 3 seconds
	MaxNotifyRows  int     // default 4
	SoundChannels  int     // default 0 (no sound mixing)

	// HUDScale, when > 0, is the integer upscale the 2D layer
	// (HUD, menu, console, centerprint, intermission) is rendered
	// at. zero / negative is treated as 1 (vanilla 1:1 layout).
	// On a 900x700 surface a scale of 2 makes the HUD blit as
	// 640x48 (sbar BG = 320x24 * 2x2) so the bar reads at the
	// same apparent size as on the canonical 320x240 surface.
	// The 3D viewport is NOT scaled -- it always fills the full
	// fb.Width x fb.Height at native resolution.
	HUDScale int
}

var (
	ErrSetupNilVFS     = errors.New("runloop: nil vfs in SetupOpts")
	ErrSetupNilHost    = errors.New("runloop: nil Host in SetupOpts")
	ErrSetupNilClient  = errors.New("runloop: nil Client in SetupOpts")
	ErrSetupNilConn    = errors.New("runloop: nil Conn in SetupOpts")
	ErrSetupNilBackend = errors.New("runloop: nil Backend in SetupOpts")
)

// NewRunnerFromVFS builds a Runner ready to call RunUntilQuit on.
// Loads the standard asset set (palette/colormap/conchars) from
// the vfs, allocates the framebuffer + RGBA buffer + console +
// screen + mix buffer, wires every piece into a Runner.
//
// On any setup failure, returns nil + the propagated error. Tests
// can supply a fstest.MapFS-backed vfs.SearchPath to exercise this
// without real pak0 data.
func NewRunnerFromVFS(opts SetupOpts) (*Runner, error) {
	if opts.VFS == nil {
		return nil, ErrSetupNilVFS
	}
	if opts.Host == nil {
		return nil, ErrSetupNilHost
	}
	if opts.Client == nil {
		return nil, ErrSetupNilClient
	}
	if opts.Conn == nil {
		return nil, ErrSetupNilConn
	}
	if opts.Backend == nil {
		return nil, ErrSetupNilBackend
	}

	set, err := assets.LoadStandard(opts.VFS)
	if err != nil {
		return nil, err
	}

	w, h := opts.Backend.Size()
	fb, err := render.NewFrameBuffer(w, h)
	if err != nil {
		return nil, err
	}
	hudScale := opts.HUDScale
	if hudScale <= 0 {
		hudScale = 1
	}
	fb.HUDScale = hudScale

	// Virtual-space dims for the 2D layer. HUDScale > 1 shrinks
	// the addressable width so the console + screen column / row
	// math lines up with the upscaled HUD blits. At HUDScale == 1
	// vW / vH equal w / h verbatim (vanilla 1:1 behaviour).
	vW := fb.VWidth()
	vH := fb.VHeight()
	conW := render.MinConsoleWidth
	if maxCols := vW / render.CharWidth; maxCols > conW {
		conW = maxCols
	}
	// NewConsole + NewScreen never fail when their dimensions
	// passed NewFrameBuffer's check above (conW >= MinConsoleWidth
	// by the max guard; lines = MinConsoleLines*4 > MinConsoleLines).
	con, _ := render.NewConsole(conW, render.MinConsoleLines*4)
	screen, _ := render.NewScreen(vW, vH)

	// Defaults for optional fields.
	notifyLifetime := opts.NotifyLifetime
	if notifyLifetime == 0 {
		notifyLifetime = 3.0
	}
	maxNotifyRows := opts.MaxNotifyRows
	if maxNotifyRows == 0 {
		maxNotifyRows = 4
	}

	r := &Runner{
		Host:           opts.Host,
		Client:         opts.Client,
		Conn:           opts.Conn,
		Backend:        opts.Backend,
		FrameBuffer:    fb,
		Console:        con,
		Screen:         screen,
		Chars:          set.ConChars,
		Palette:        set.Palette,
		RGBA:           make([]byte, w*h*4),
		BackgroundIdx:  opts.BackgroundIdx,
		NotifyLifetime: notifyLifetime,
		MaxNotifyRows:  maxNotifyRows,
		Speeds:         client.DefaultInputSpeeds(),
	}

	// Optional sound pool + mix buffer.
	if opts.SoundChannels > 0 {
		pool, err := sound.NewPool(opts.SoundChannels)
		if err != nil {
			return nil, err
		}
		r.SoundPool = pool
		r.MixBuffer = make([]sound.StereoSample, sound.MixBufferStereoFrames)
	}

	return r, nil
}
