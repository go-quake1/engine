// Copyright (c) 2026 the go-quake1/engine authors.
// SPDX-License-Identifier: GPL-2.0-or-later

// Command quake-wctr is Quake as a wasip1 guest of the wasmcontainers
// playground (wctr): the engine runs e1m1 with the software renderer and
// shows each frame on a wasi-gfx:surface, the pane of the playground's
// dashboard, through render/wasipresent. Keys come from the surface.
//
//	GOOS=wasip1 GOARCH=wasm go build -o quake-wctr.wasm ./cmd/quake-wctr
//	wctr run --rm --gpu -m 512m quake            # play
//	wctr run --rm --gpu -m 512m quake --worldref 4
//
// The game data is a pak0.pak (--pak, default /usr/share/quake/pak0.pak:
// the playground image ships go-quake1/freepak there).
//
// --worldref N draws one fixed view (worldview.AcceptanceViews: the generic
// views of quake-worldref, four yaws per origin, then, as the last number,
// the eye at info_player_start; on the freepak e1m1 that is 4), prints the SHA-256 of its 8-bit index frame (the
// figure quake-worldref prints for the same view) and of the RGBA frame the
// browser must show (webgpupresent.Reference), and, where a surface exists,
// presents it. With no --worldref the game runs until the surface is
// dropped, or --frames frames have been drawn (then the mean frame time is
// printed).
package main

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/go-quake1/engine/assets"
	"github.com/go-quake1/engine/backend"
	"github.com/go-quake1/engine/backend/wasisurface"
	"github.com/go-quake1/engine/pak"
	"github.com/go-quake1/engine/render"
	"github.com/go-quake1/engine/render/webgpupresent"
	"github.com/go-quake1/engine/runloop"
	"github.com/go-quake1/engine/runner"
	"github.com/go-quake1/engine/vfs"
	"github.com/go-quake1/engine/worldview"
)

// DefaultPak is where the playground image puts the game data.
const DefaultPak = "/usr/share/quake/pak0.pak"

// worldrefPresents is how many times --worldref presents its frame: the
// host paces presents, and the page needs one to arrive.
const worldrefPresents = 3

var osExit = os.Exit

func main() {
	if err := RootCmd().Execute(); err != nil {
		osExit(1)
	}
}

// ErrNoSurface is returned when a run needs a wasi-gfx surface and this
// build has none (only GOOS=wasip1 does).
var ErrNoSurface = errors.New("quake-wctr: no wasi-gfx surface in this build (build with GOOS=wasip1 and run with wctr --gpu)")

// gpuSurface is what a wasi-gfx surface gives the game: a presenter for the
// frame and the two key streams.
type gpuSurface struct {
	Presenter render.Presenter
	Down, Up  wasisurface.KeyPoller
	Close     func()
}

// openSurface opens a w x h surface. surface_wasip1.go sets it; it is nil
// in any other build, and tests set it to a fake.
var openSurface func(w, h int) (*gpuSurface, error)

// RootCmd returns the command.
func RootCmd() *cobra.Command {
	var pakPath, mapSlug string
	var w, h, worldref, frames int
	c := &cobra.Command{
		Use:           "quake-wctr",
		Short:         "Quake in a wasmcontainers playground container: e1m1 on a wasi-gfx surface",
		SilenceUsage:  true,
		SilenceErrors: false,
		Args:          cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if w < 1 || h < 1 {
				return fmt.Errorf("quake-wctr: bad frame size %dx%d", w, h)
			}
			f, err := os.Open(pakPath)
			if err != nil {
				return err
			}
			defer f.Close()
			p, err := pak.Open(f)
			if err != nil {
				return fmt.Errorf("quake-wctr: %s: %w", pakPath, err)
			}
			if cmd.Flags().Changed("worldref") {
				return runWorldref(cmd.OutOrStdout(), p, mapSlug, worldref, w, h)
			}
			return runGame(cmd.OutOrStdout(), cmd.ErrOrStderr(), p, mapSlug, w, h, frames)
		},
	}
	fl := c.Flags()
	fl.StringVar(&pakPath, "pak", DefaultPak, "pak0.pak")
	fl.StringVar(&mapSlug, "map", "e1m1", "map slug")
	fl.IntVar(&w, "width", 640, "frame width (the surface has the same size)")
	fl.IntVar(&h, "height", 400, "frame height")
	fl.IntVar(&worldref, "worldref", 0, "draw fixed view N (the last is the player start), print its hashes and present it, instead of playing")
	fl.IntVar(&frames, "frames", 0, "play this many frames, then print the mean frame time (0: until the surface is dropped)")
	return c
}

// runWorldref draws view n of the map with the world-only software
// reference and prints the two hashes the playground's e2e compares.
func runWorldref(out io.Writer, fsys fs.FS, slug string, n, w, h int) error {
	wv, err := worldview.LoadMap(fsys, slug)
	if err != nil {
		return err
	}
	views, err := wv.AcceptanceViews()
	if err != nil {
		return err
	}
	if n < 0 || n >= len(views) {
		return fmt.Errorf("quake-wctr: --worldref %d: %s has %d fixed views (0-%d)", n, slug, len(views), len(views)-1)
	}
	v := vfs.New()
	v.Add(fsys)
	cm, err := assets.LoadColorMapFrom(v)
	if err != nil {
		return err
	}
	pal, err := assets.LoadPaletteFrom(v)
	if err != nil {
		return err
	}
	fb, err := render.NewFrameBuffer(w, h)
	if err != nil {
		return err
	}
	if err := wv.DrawSoftware(fb, cm, views[n]); err != nil {
		return err
	}
	rgba, ok := webgpupresent.Reference(fb.Pixels, fb.Width, fb.Height, fb.Pitch, pal, w, h)
	if !ok {
		return errors.New("quake-wctr: the reference refused the frame")
	}
	idxSum := sha256.Sum256(fb.Pixels)
	rgbaSum := sha256.Sum256(rgba)
	fmt.Fprintf(out, "view %d %dx%d sha256(indices) %s\n", n, w, h, hex.EncodeToString(idxSum[:]))
	fmt.Fprintf(out, "view %d %dx%d sha256(rgba) %s\n", n, w, h, hex.EncodeToString(rgbaSum[:]))
	if openSurface == nil {
		return nil // native: the hashes are the result
	}
	gs, err := openSurface(w, h)
	if err != nil {
		return err
	}
	defer gs.Close()
	for i := 0; i < worldrefPresents; i++ {
		if err := gs.Presenter.Present(fb.Pixels, fb.Width, fb.Height, fb.Pitch, pal); err != nil {
			return err
		}
	}
	return nil
}

// runGame plays the map on the surface until it is dropped, or for frames
// frames when frames > 0.
func runGame(out, errOut io.Writer, fsys fs.FS, slug string, w, h, frames int) error {
	if openSurface == nil {
		return ErrNoSurface
	}
	gs, err := openSurface(w, h)
	if err != nil {
		return err
	}
	defer gs.Close()
	be := wasisurface.New(w, h, gs.Down, gs.Up)
	r, err := runner.Setup(runner.SetupOpts{
		Backend:  be,
		PakFS:    fsys,
		FBWidth:  w,
		FBHeight: h,
		MapSlug:  slug,
		SkipMenu: true,
		Logf:     func(format string, args ...any) { fmt.Fprintf(errOut, "QUAKE: "+format+"\n", args...) },
	})
	if err != nil {
		return err
	}
	rend, err := render.NewPresentRenderer(r.Pre2DDraw, gs.Presenter)
	if err != nil {
		return err
	}
	r.Renderer = rend
	return loop(out, r, be, frames)
}

// quitObserver reports whether a poll carried QuitRequested.
type quitObserver struct {
	backend.Backend
	quit bool
}

func (q *quitObserver) PollInput() (backend.InputSnapshot, error) {
	snap, err := q.Backend.PollInput()
	if snap.QuitRequested {
		q.quit = true
	}
	return snap, err
}

// loop drives the runner frame by frame, as runloop.RunUntilQuit does, so
// it can stop after a number of frames and time them.
func loop(out io.Writer, r *runloop.Runner, be backend.Backend, frames int) error {
	obs := &quitObserver{Backend: be}
	r.Backend = obs
	start := time.Now()
	last := be.Now()
	dt := runloop.MinFrameTime
	for n := 0; frames == 0 || n < frames; n++ {
		now := be.Now()
		if err := r.RunFrame(dt, float32(now)); err != nil {
			return err
		}
		if obs.quit {
			return nil
		}
		dt = max(float32(now-last), runloop.MinFrameTime)
		last = now
	}
	el := time.Since(start)
	fmt.Fprintf(out, "%d frames in %v: %.2f ms per frame\n", frames, el.Round(time.Millisecond), el.Seconds()*1000/float64(frames))
	return nil
}
