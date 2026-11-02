// Copyright (c) 2026 the go-quake1/engine authors.
// SPDX-License-Identifier: GPL-2.0-or-later

// Package bench is the frame-time harness for the renderer seam. It
// drives the full engine (server tick, client, world, models, 2D layers,
// RGBA resolve) for a fixed number of frames on the deterministic
// demo-orbit camera path with a null display, and reports the
// per-stage frame times ([render.FrameTimer]) and the allocations per
// frame (runtime.MemStats, exact whatever the machine load).
//
// The same Run works natively and under js/wasm, so the software
// renderer and any later renderer are measured by one program. The
// engine / renderer split is the seam's: the sim stage is the engine,
// world + models + 2d + present is the renderer.
package bench

import (
	"errors"
	"io/fs"
	"os"
	"runtime"
	"time"

	"github.com/go-quake1/engine/backend"
	"github.com/go-quake1/engine/embedpak"
	"github.com/go-quake1/engine/game"
	"github.com/go-quake1/engine/pak"
	"github.com/go-quake1/engine/render"
)

// FrameDT is the fixed simulation step, in seconds (60 Hz).
const FrameDT = float32(1.0 / 60.0)

// Config selects what to run.
type Config struct {
	// Frames is the number of measured frames (>= 1).
	Frames int `json:"frames"`
	// Warmup frames run first and are not measured.
	Warmup int `json:"warmup"`
	// Width, Height are the framebuffer size.
	Width  int `json:"width"`
	Height int `json:"height"`
	// Renderer is "soft" (default) or "gpu".
	Renderer string `json:"renderer"`
	// Map is the map slug ("start" by default).
	Map string `json:"map"`
	// Present selects the display path (js/wasm only): "" discards the
	// frame (the baseline), "canvas2d" is the software path of the
	// standalone page (RGBA on the CPU, putImageData), "webgpu" is the
	// GPU presenter (palette lookup and scaling in a fragment shader).
	Present string `json:"present,omitempty"`
	// RequireGPU makes a "webgpu" run fail on a fallback (software)
	// adapter such as SwiftShader: that is never a GPU measurement.
	RequireGPU bool `json:"require_gpu,omitempty"`
}

// Present paths of [Config].
const (
	PresentNone     = ""
	PresentCanvas2D = "canvas2d"
	PresentWebGPU   = "webgpu"
)

// PresentSetup is a display path a browser build wires (see present_js.go).
type PresentSetup struct {
	// Renderer builds the renderer over the scene closure; nil keeps the
	// software renderer.
	Renderer func(scene func(*render.FrameBuffer, [3]float32, [3]float32) error) (render.Renderer, error)
	// Present replaces the null display's PresentFrame; nil discards.
	Present func(rgba []byte, w, h int) error
	// Wait blocks until the display has finished every frame handed to
	// it (the GPU is idle); nil when presenting is synchronous.
	Wait func() error
	// Adapter names the GPU adapter and Fallback says it is a software
	// one; empty for a path without a GPU.
	Adapter  string
	Fallback bool
	// Close releases the path.
	Close func()
}

// ErrNoPresent is returned by [Run] for a present path this build cannot
// provide (every path needs js/wasm in a browser).
var ErrNoPresent = errors.New("bench: the present path needs the js/wasm build in a browser")

// ErrFallbackAdapter is returned when Config.RequireGPU is set and the
// adapter is a software one.
var ErrFallbackAdapter = errors.New("bench: a fallback (software) adapter where a GPU is required")

// newPresent builds the display path of cfg.Present; present_js.go sets it.
var newPresent = func(Config) (*PresentSetup, error) { return nil, ErrNoPresent }

// DefaultConfig is the canonical run: 320x200, 600 frames after 60 of
// warm-up, software renderer.
func DefaultConfig() Config {
	return Config{Frames: 600, Warmup: 60, Width: 320, Height: 200, Renderer: "soft", Map: "start"}
}

// ErrConfig is returned by [Run] for a config that cannot run.
var ErrConfig = errors.New("bench: frames and the framebuffer size must be positive, warmup not negative, present one of none, canvas2d, webgpu")

// Split is the engine / renderer share of the mean frame work.
type Split struct {
	EngineMS      float64 `json:"engine_ms"`
	RendererMS    float64 `json:"renderer_ms"`
	EnginePercent float64 `json:"engine_percent"`
}

// Result is one run.
type Result struct {
	Config  Config         `json:"config"`
	Pak     string         `json:"pak"`
	Browser string         `json:"browser,omitempty"`
	Target  string         `json:"target"`
	Go      string         `json:"go"`
	Procs   int            `json:"gomaxprocs"`
	Summary render.Summary `json:"summary"`
	Split   Split          `json:"split"`
	// AllocsPerFrame and BytesPerFrame are heap allocations over the
	// measured frames, divided by the frame count.
	AllocsPerFrame float64 `json:"allocs_per_frame"`
	BytesPerFrame  float64 `json:"bytes_per_frame"`
	// WallMSPerFrame is the wall time of the measured frames plus the
	// wait for the display to finish them, over the frame count: the
	// end-to-end cost, GPU included (the frame timer sees the CPU side).
	WallMSPerFrame float64 `json:"wall_ms_per_frame"`
	// Adapter and AdapterFallback describe the GPU adapter of a webgpu
	// run; a fallback adapter is software, not a GPU.
	Adapter         string `json:"adapter,omitempty"`
	AdapterFallback bool   `json:"adapter_fallback,omitempty"`
}

// nullBackend discards every frame and never asks to quit.
type nullBackend struct {
	backend.NoAudio
	w, h    int
	start   time.Time
	pollErr error // a test hook: PollInput fails with it
	// present, when set, takes the frame instead of discarding it.
	present func(rgba []byte, w, h int) error
}

func (b *nullBackend) PollInput() (backend.InputSnapshot, error) {
	return backend.InputSnapshot{}, b.pollErr
}

func (b *nullBackend) PresentFrame(rgba []byte, w, h int) error {
	if b.present != nil {
		return b.present(rgba, w, h)
	}
	return nil
}
func (b *nullBackend) Size() (int, int) { return b.w, b.h }
func (b *nullBackend) Now() float64     { return time.Since(b.start).Seconds() }

// OpenPak opens path as a pak archive file; an empty path returns the
// embedded pak, which is empty in a stock build (the engine then
// falls back to its procedural assets). The second result names the
// source: "pak0" for a file, "procedural" for an empty embedded pak.
func OpenPak(path string) (fs.FS, string, error) {
	if path != "" {
		f, err := os.Open(path)
		if err != nil {
			return nil, "", err
		}
		p, err := pak.Open(f)
		if err != nil {
			return nil, "", err
		}
		return p, "pak0", nil
	}
	p, err := openEmbedded()
	if errors.Is(err, embedpak.ErrEmbedPakEmpty) {
		return emptyFS{}, "procedural", nil
	}
	if err != nil {
		return nil, "", err
	}
	return p, "embedded", nil
}

// openEmbedded is a seam for the tests.
var openEmbedded = embedpak.OpenAsFS

// emptyFS holds no file: the engine then uses its procedural assets.
type emptyFS struct{}

func (emptyFS) Open(name string) (fs.File, error) {
	return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrNotExist}
}

// Run measures cfg against pakFS. pakSource is recorded in the result.
func Run(cfg Config, pakFS fs.FS, pakSource string) (Result, error) {
	if cfg.Frames < 1 || cfg.Warmup < 0 || cfg.Width < 1 || cfg.Height < 1 ||
		(cfg.Present != PresentNone && cfg.Present != PresentCanvas2D && cfg.Present != PresentWebGPU) {
		return Result{}, ErrConfig
	}
	return run(cfg, &nullBackend{w: cfg.Width, h: cfg.Height, start: time.Now()}, pakFS, pakSource)
}

func run(cfg Config, be *nullBackend, pakFS fs.FS, pakSource string) (Result, error) {
	clock := func() float64 { return time.Since(be.start).Seconds() }
	// cfg.Frames >= 1 and clock is non-nil, the only two errors.
	timer, _ := render.NewFrameTimer(clock, cfg.Frames)
	kind, err := render.ParseKind(cfg.Renderer)
	if err != nil {
		return Result{}, err
	}
	sess, err := game.Build(pakFS, be, game.Options{Map: cfg.Map, DemoOrbit: true})
	if err != nil {
		return Result{}, err
	}
	r := sess.Runner
	// The same wiring runner.installRenderer does: the software renderer
	// draws through the scene closure game.Build installed.
	r.Renderer, _ = render.Choose(kind, &render.Software{Scene: r.Pre2DDraw}, nil)
	r.Timer = timer
	var ps *PresentSetup
	if cfg.Present != PresentNone {
		if ps, err = newPresent(cfg); err != nil {
			return Result{}, err
		}
		if ps.Close != nil {
			defer ps.Close()
		}
		if cfg.RequireGPU && cfg.Present == PresentWebGPU && ps.Fallback {
			return Result{}, ErrFallbackAdapter
		}
		be.present = ps.Present
		if ps.Renderer != nil {
			if r.Renderer, err = ps.Renderer(r.Pre2DDraw); err != nil {
				return Result{}, err
			}
		}
	}
	var m0, m1 runtime.MemStats
	var wall0 time.Time
	for i := 0; i < cfg.Warmup+cfg.Frames; i++ {
		if i == cfg.Warmup {
			if ps != nil && ps.Wait != nil {
				if err := ps.Wait(); err != nil {
					return Result{}, err
				}
			}
			timer.Reset()
			runtime.GC()
			runtime.ReadMemStats(&m0)
			wall0 = time.Now()
		}
		if err := r.RunFrame(FrameDT, float32(i)*FrameDT); err != nil {
			return Result{}, err
		}
	}
	runtime.ReadMemStats(&m1)
	sum := render.Summarize(timer.Samples())
	if ps != nil && ps.Wait != nil {
		if err := ps.Wait(); err != nil {
			return Result{}, err
		}
	}
	wall := time.Since(wall0)
	n := float64(cfg.Frames)
	res := Result{
		Config:         cfg,
		Pak:            pakSource,
		Target:         runtime.GOOS + "/" + runtime.GOARCH,
		Go:             runtime.Version(),
		Procs:          runtime.GOMAXPROCS(0),
		Summary:        sum,
		Split:          splitOf(sum),
		AllocsPerFrame: float64(m1.Mallocs-m0.Mallocs) / n,
		BytesPerFrame:  float64(m1.TotalAlloc-m0.TotalAlloc) / n,
		WallMSPerFrame: float64(wall) / float64(time.Millisecond) / n,
	}
	if ps != nil {
		res.Adapter, res.AdapterFallback = ps.Adapter, ps.Fallback
	}
	return res, nil
}

// splitOf attributes the mean stage times to the engine (sim) and the
// renderer (everything the Renderer seam drives).
func splitOf(s render.Summary) Split {
	var sp Split
	for _, st := range s.Stages {
		if st.Stage == render.StageSim.String() {
			sp.EngineMS += st.Mean
		} else {
			sp.RendererMS += st.Mean
		}
	}
	if t := sp.EngineMS + sp.RendererMS; t > 0 {
		sp.EnginePercent = 100 * sp.EngineMS / t
	}
	return sp
}
