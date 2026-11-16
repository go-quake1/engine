// Copyright (c) 2026 the go-quake1/engine authors.
// SPDX-License-Identifier: GPL-2.0-or-later

// Command quake-wctr is Quake as a wasip1 guest of the wasmcontainers
// playground (wctr): the engine runs with the software renderer and shows
// each frame on a wasi-gfx:surface, the pane of the playground's dashboard,
// through render/wasipresent. Keys, the mouse buttons and relative mouse
// motion come from the surface: the game asks for the pointer lock, which
// the page grants on the first click on the pane (Escape releases it).
//
//	GOOS=wasip1 GOARCH=wasm go build -o quake-wctr.wasm ./cmd/quake-wctr
//	wctr run --rm --gpu -m 512m quake            # play, from the main menu
//	wctr run --rm --gpu -m 512m quake --map e1m1 # skip the menu, start on e1m1
//	wctr run --rm --gpu -m 512m quake --worldref 4
//
// With no --map the game boots into the main menu, as the browser build
// does: New Game, then a skill, starts the "start" hub, whose portals change
// level. With --map the menu is skipped and that map runs from the first
// tic (Escape still opens the menu).
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
	"runtime"
	"time"

	"github.com/spf13/cobra"

	"github.com/go-quake1/engine/assets"
	"github.com/go-quake1/engine/backend"
	"github.com/go-quake1/engine/backend/wasisurface"
	"github.com/go-quake1/engine/pak"
	"github.com/go-quake1/engine/render"
	"github.com/go-quake1/engine/render/gpuworld"
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
// frame, the two key streams and the mouse (each stream nil for none).
type gpuSurface struct {
	Presenter render.Presenter
	Down, Up  wasisurface.KeyPoller
	// Motion is the relative pointer motion, ButtonDown and ButtonUp the
	// mouse buttons (gpu's surface extension).
	Motion               wasisurface.MotionPoller
	ButtonDown, ButtonUp wasisurface.ButtonPoller
	Close                func()
}

// ErrNoGPUWorld is returned by --gpuworld when this build has no batch device
// (only GOOS=wasip1 does).
var ErrNoGPUWorld = errors.New("quake-wctr: no GPU world in this build (build with GOOS=wasip1 and run with wctr --gpu)")

// gpuDrawer draws the world on the GPU: one frame of view v, the index frame
// read back (width*height bytes).
type gpuDrawer interface {
	Draw(v worldview.View) ([]byte, error)
	Close()
}

// openGPUWorld builds the GPU content of wv for a w x h frame on the batch
// device. gpuworld_wasip1.go sets it; it is nil in any other build, and tests
// set it to a fake.
var openGPUWorld func(wv *worldview.World, cm *render.ColorMap, w, h int) (gpuDrawer, error)

// openSurface opens a w x h surface. surface_wasip1.go sets it; it is nil
// in any other build, and tests set it to a fake.
var openSurface func(w, h int) (*gpuSurface, error)

// RootCmd returns the command.
func RootCmd() *cobra.Command {
	var pakPath, mapSlug string
	var w, h, worldref, frames int
	var gpuWorld, gpuCheck, cpuWorld, corruptSkins bool
	var minEnts int
	var gpuEntities bool
	c := &cobra.Command{
		Use:           "quake-wctr",
		Short:         "Quake in a wasmcontainers playground container: the game on a wasi-gfx surface, from the main menu",
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
			if corruptSkins && !gpuCheck {
				return errors.New("quake-wctr: --corrupt-gpu-skins needs --gpuworld-check")
			}
			if cpuWorld && (gpuWorld || gpuCheck) {
				return errors.New("quake-wctr: --cpuworld excludes --gpuworld and --gpuworld-check")
			}
			if gpuCheck && !gpuWorld {
				return errors.New("quake-wctr: --gpuworld-check needs --gpuworld")
			}
			if gpuCheck && cmd.Flags().Changed("worldref") {
				return errors.New("quake-wctr: --gpuworld-check is for the game loop, not --worldref (which always compares)")
			}
			if cmd.Flags().Changed("worldref") {
				slug := mapSlug
				if slug == "" {
					slug = "e1m1" // the fixed views are of a map: --worldref's default
				}
				return runWorldref(cmd.OutOrStdout(), p, slug, worldref, w, h, gpuWorld, frames)
			}
			return runGameWith(cmd.OutOrStdout(), cmd.ErrOrStderr(), p, mapSlug, w, h, frames, gameOpts{gpuWorld: gpuWorld, check: gpuCheck, auto: !cpuWorld, corruptSkins: corruptSkins, minGPUEntities: minEnts, gpuEntities: gpuEntities})
		},
	}
	fl := c.Flags()
	fl.StringVar(&pakPath, "pak", DefaultPak, "pak0.pak")
	fl.StringVar(&mapSlug, "map", "", "map slug: start on this map and skip the menu (default: boot into the main menu; --worldref defaults to e1m1)")
	fl.IntVar(&w, "width", 640, "frame width (the surface has the same size)")
	fl.IntVar(&h, "height", 400, "frame height")
	fl.IntVar(&worldref, "worldref", 0, "draw fixed view N (the last is the player start), print its hashes and present it, instead of playing")
	fl.BoolVar(&gpuWorld, "gpuworld", false, "with --worldref: also draw the view on the GPU (render/gpuworld on the batch device), compare it with the software frame at worldcheck's tolerance and present the GPU frame")
	fl.BoolVar(&corruptSkins, "corrupt-gpu-skins", false, "with --gpuworld-check: the planted defect of the entity check, the GPU draws the alias entities with every skin index shifted by 16, which the check must fail")
	fl.BoolVar(&gpuEntities, "gpu-entities", false, "with the GPU world, draw the alias entities on the GPU too, in a layer of their own (off by default: the device was lost on the macOS CI lane at the first frame with entities; the CPU draws them)")
	fl.IntVar(&minEnts, "min-gpu-entities", 0, "the fewest alias entities in a frame for which the GPU draws them (0: 3; the entity layer costs a second pass and read-back, so a frame with fewer keeps them on the CPU; --gpuworld-check uses 1)")
	fl.BoolVar(&cpuWorld, "cpuworld", false, "in the game loop, draw the world on the CPU even when a GPU is granted (by default the GPU draws it, and the CPU when none is granted)")
	fl.BoolVar(&gpuCheck, "gpuworld-check", false, "with --gpuworld in the game loop: draw every frame's world on the CPU too, compare at worldcheck's tolerance, and fail above it (needs --frames)")
	fl.BoolVar(&fixedMixer, "fixed-mixer", false, "paint a fixed 512 mix frames every tic instead of pacing the mixer to the sound output's queue (what the game did before the pacing; for measuring the real-time ratio)")
	_ = fl.MarkHidden("fixed-mixer")
	fl.BoolVar(&memStats, "memstats", false, "print the Go heap (live, in use, system, GC count) every 60 frames, to find a leak")
	_ = fl.MarkHidden("memstats")
	fl.IntVar(&frames, "frames", 0, "play this many frames, then print the mean frame time (0: until the surface is dropped); with --worldref, redraw the view this many times after a first frame and print the warm-up apart, then the mean, median, min and max per frame (software, or with --gpuworld the GPU frame), present excluded")
	return c
}

// runWorldref draws view n of the map with the world-only software
// reference and prints the two hashes the playground's e2e compares.
func runWorldref(out io.Writer, fsys fs.FS, slug string, n, w, h int, gpuWorld bool, frames int) error {
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
	pixels, pitch := fb.Pixels, fb.Pitch
	if gpuWorld {
		frame, err := gpuWorldFrame(out, wv, cm, views[n], n, w, h, fb)
		if err != nil {
			return err
		}
		pixels, pitch = frame, w
	}
	if frames > 0 {
		// Timed redraws of the same view, present excluded: the software frame,
		// or with --gpuworld the GPU frame (one submit with its read-back).
		var err error
		if gpuWorld {
			err = timeGPU(out, wv, cm, views[n], n, w, h, frames)
		} else {
			err = timeSoftware(out, wv, cm, views[n], n, w, h, frames, fb)
		}
		if err != nil {
			return err
		}
	}
	if openSurface == nil {
		return nil // native: the hashes are the result
	}
	gs, err := openSurface(w, h)
	if err != nil {
		return err
	}
	defer gs.Close()
	for i := 0; i < worldrefPresents; i++ {
		if err := gs.Presenter.Present(pixels, fb.Width, fb.Height, pitch, pal); err != nil {
			return err
		}
	}
	return nil
}

// gpuWorldFrame draws view v on the GPU, reads the index frame back in the same
// submit and compares it with the software frame ref: a GPU frame is not
// hash-equal to the CPU's (cmd/quake-worldcheck: tens of pixels of a million
// differ by one pixel of shift), so the verdict is worldcheck's, the share of
// pixels a one-pixel shift does not explain against gpuworld.Tolerance. Over it,
// it is an error. The two hashes it prints are information, not a check.
func gpuWorldFrame(out io.Writer, wv *worldview.World, cm *render.ColorMap, v worldview.View, n, w, h int, ref *render.FrameBuffer) ([]byte, error) {
	if openGPUWorld == nil {
		return nil, ErrNoGPUWorld
	}
	gd, err := openGPUWorld(wv, cm, w, h)
	if err != nil {
		return nil, err
	}
	defer gd.Close()
	frame, err := gd.Draw(v)
	if err != nil {
		return nil, err
	}
	if len(frame) != w*h {
		return nil, fmt.Errorf("quake-wctr: the GPU frame has %d bytes, want %dx%d", len(frame), w, h)
	}
	differ, unexplained := gpuworld.Compare(ref.Pixels, ref.Pitch, frame, w, h)
	rate := float64(unexplained) / float64(w*h)
	sum := sha256.Sum256(frame)
	fmt.Fprintf(out, "view %d %dx%d gpuworld sha256(indices) %s\n", n, w, h, hex.EncodeToString(sum[:]))
	fmt.Fprintf(out, "view %d %dx%d gpuworld %d of %d pixels differ, %d unexplained (%.4f, tolerance %.3f)\n",
		n, w, h, differ, w*h, unexplained, rate, gpuworld.Tolerance)
	if rate > gpuworld.Tolerance {
		return nil, fmt.Errorf("quake-wctr: the GPU frame differs from the software frame by %.4f unexplained, above the tolerance %.3f", rate, gpuworld.Tolerance)
	}
	fmt.Fprintf(out, "view %d %dx%d gpuworld PASS\n", n, w, h)
	return frame, nil
}

// testRunner, set by a test, receives the runner once it is set up (nil in
// the command).
var testRunner func(*runloop.Runner)

// runGame plays on the surface until it is dropped, or for frames frames
// when frames > 0: the map slug, or the main menu when slug is "".
func runGame(out, errOut io.Writer, fsys fs.FS, slug string, w, h, frames int) error {
	return runGameWith(out, errOut, fsys, slug, w, h, frames, gameOpts{})
}

// runGameWith is runGame with the GPU options: with gpuWorld the map is built
// on the batch device once, before the first frame, and every frame draws its
// lit world faces there (one submit with the read-back); the software pass draws
// the sky and water, the entities, the particles, the view model, the HUD and
// the menu over them, in its own order. With check every frame is also drawn
// whole on the CPU and compared at worldcheck's measure.
func runGameWith(out, errOut io.Writer, fsys fs.FS, slug string, w, h, frames int, g gameOpts) error {
	if openSurface == nil {
		return ErrNoSurface
	}
	gs, err := openSurface(w, h)
	if err != nil {
		return err
	}
	defer gs.Close()
	// No map: boot into the main menu, as the browser build does (the runner
	// spawns the "start" hub behind it, the attract demo off: its stream
	// desyncs the client). A map: skip the menu and run that map.
	so := runner.SetupOpts{
		Backend:            nil,
		PakFS:              fsys,
		FBWidth:            w,
		FBHeight:           h,
		MapSlug:            slug,
		SkipMenu:           slug != "",
		DisableAttractDemo: true,
		Logf:               func(format string, args ...any) { fmt.Fprintf(errOut, "QUAKE: "+format+"\n", args...) },
	}
	var stats *checkStats
	if g.gpuWorld || g.auto {
		world := slug
		if world == "" {
			world = "start"
		}
		gw, err := openGameWorld(fsys, world, w, h)
		switch {
		case err != nil && g.auto && !g.gpuWorld:
			// The default: no GPU granted (or the device refused the map), so
			// the CPU draws the world. --gpuworld asks for the GPU and fails.
			fmt.Fprintf(errOut, "QUAKE: GPU world unavailable for %s (%v): the CPU draws its world\n", world, err)
		case errors.Is(err, gpuworld.ErrAtlasFull):
			// The map's surfaces do not fit the GPU atlas: play it on the CPU.
			fmt.Fprintf(errOut, "QUAKE: GPU world unavailable for %s (%v): the CPU draws its world\n", slug, err)
		case err != nil:
			return err
		default:
			defer gw.Close()
			gw.corrupt = g.corruptSkins
			gw.entities = g.gpuEntities
			so.MinGPUEntities = g.minGPUEntities
			so.WorldDraw = gw
			if g.check {
				stats = &checkStats{ents: gw}
				so.WorldCheck = stats.add
				so.EntityCheck = stats.addEntities
				so.MinGPUEntities = 1 // the check draws every entity on the GPU: a frame with one is a test of it
			}
		}
	}
	be := wasisurface.New(w, h, gs.Down, gs.Up).WithMouse(gs.Motion, gs.ButtonDown, gs.ButtonUp)
	closeAudio := attachAudio(be, errOut)
	defer closeAudio()
	so.Backend = be
	r, err := runner.Setup(so)
	if err != nil {
		return err
	}
	if testRunner != nil {
		testRunner(r)
	}
	rend, err := render.NewPresentRenderer(r.Pre2DDraw, gs.Presenter)
	if err != nil {
		return err
	}
	r.Renderer = rend
	var gt *gpuTimes
	if gw, ok := so.WorldDraw.(*gameWorld); ok {
		gt = &gw.times
	}
	if err := loop(out, r, mixerBackend(be), frames, gt); err != nil {
		return err
	}
	if stats != nil {
		return stats.report(out)
	}
	return nil
}

// memStats is --memstats.
var memStats bool

// yield is what the frame loop calls once per frame (runtime.Gosched; a test
// replaces it to count the calls).
var yield = runtime.Gosched

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
func loop(out io.Writer, r *runloop.Runner, be backend.Backend, frames int, gt *gpuTimes) error {
	obs := &quitObserver{Backend: be}
	r.Backend = backend.ForwardQueue(obs, be)
	start := time.Now()
	if frames > 0 {
		// Time the frame's stages (the --frames run prints them below).
		r.Timer, _ = render.NewFrameTimer(func() float64 { return time.Since(start).Seconds() }, frames)
	}
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
		// A wasm guest has one P and the frame never sleeps (the host call that
		// presents blocks in the browser): without a yield the collector's
		// background workers never run, the heap grows by the frame's garbage
		// until -m is hit, and the container dies "out of memory" (playground#56).
		yield()
		if memStats && n%60 == 0 {
			var m runtime.MemStats
			runtime.ReadMemStats(&m)
			fmt.Fprintf(out, "memstats frame %d at %d ms: alloc %d KiB, inuse %d KiB, sys %d KiB, gcs %d, total alloc %d KiB, mallocs %d\n", n, time.Since(start).Milliseconds(), m.HeapAlloc>>10, m.HeapInuse>>10, m.Sys>>10, m.NumGC, m.TotalAlloc>>10, m.Mallocs)
		}
		dt = max(float32(now-last), runloop.MinFrameTime)
		last = now
	}
	el := time.Since(start)
	fmt.Fprintf(out, "%d frames in %v: %.2f ms per frame\n", frames, el.Round(time.Millisecond), el.Seconds()*1000/float64(frames))
	writeAudioStats(out, be, el, frames, r.MixDropped, r.MixStalls)
	writePhases(out, r.Timer.Samples(), gt)
	return nil
}
