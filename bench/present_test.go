// Copyright (c) 2026 the go-quake1/engine authors.
// SPDX-License-Identifier: GPL-2.0-or-later

//go:build !js

package bench

import (
	"errors"
	"strings"
	"testing"

	"github.com/go-quake1/engine/render"
)

// fakeDisplay is a display path that counts what it is given. It
// presents like the webgpu one: the renderer does not write RGBA.
type fakeDisplay struct {
	frames, waits int
	closed        bool
}

func (f *fakeDisplay) Present(string) error { f.frames++; return nil }

func withPresent(t *testing.T, setup func(Config) (*PresentSetup, error)) {
	t.Helper()
	old := newPresent
	t.Cleanup(func() { newPresent = old })
	newPresent = setup
}

func TestRunWithoutABrowserRefusesAPresentPath(t *testing.T) {
	fsys, _, _ := OpenPak("")
	for _, p := range []string{PresentCanvas2D, PresentWebGPU} {
		c := smallConfig()
		c.Present = p
		if _, err := Run(c, fsys, "x"); !errors.Is(err, ErrNoPresent) {
			t.Errorf("Run(present %q) = %v, want ErrNoPresent", p, err)
		}
	}
	c := smallConfig()
	c.Present = "vulkan"
	if _, err := Run(c, fsys, "x"); !errors.Is(err, ErrConfig) {
		t.Errorf("unknown present = %v, want ErrConfig", err)
	}
}

func TestRunThroughAPresentPath(t *testing.T) {
	fsys, _, _ := OpenPak("")
	d := &fakeDisplay{}
	withPresent(t, func(Config) (*PresentSetup, error) {
		return &PresentSetup{
			Present: func([]byte, int, int) error { d.frames++; return nil },
			Wait:    func() error { d.waits++; return nil },
			Adapter: "apple metal", Close: func() { d.closed = true },
		}, nil
	})
	c := smallConfig()
	c.Present = PresentCanvas2D
	res, err := Run(c, fsys, "x")
	if err != nil {
		t.Fatal(err)
	}
	if d.frames != c.Warmup+c.Frames || d.waits != 2 || !d.closed {
		t.Errorf("frames %d waits %d closed %v", d.frames, d.waits, d.closed)
	}
	if res.Adapter != "apple metal" || res.AdapterFallback || res.WallMSPerFrame <= 0 {
		t.Errorf("result %+v", res)
	}
}

type countingPresenter struct{ n int }

func (p *countingPresenter) Present([]byte, int, int, int, *render.Palette) error { p.n++; return nil }

func TestRunWithAPresentRenderer(t *testing.T) {
	fsys, _, _ := OpenPak("")
	cp := &countingPresenter{}
	withPresent(t, func(Config) (*PresentSetup, error) {
		return &PresentSetup{Renderer: func(scene func(*render.FrameBuffer, [3]float32, [3]float32) error) (render.Renderer, error) {
			return render.NewPresentRenderer(scene, cp)
		}}, nil
	})
	c := smallConfig()
	c.Present = PresentWebGPU
	if _, err := Run(c, fsys, "x"); err != nil {
		t.Fatal(err)
	}
	if cp.n != c.Warmup+c.Frames {
		t.Errorf("presenter saw %d frames, want %d", cp.n, c.Warmup+c.Frames)
	}
}

func TestPresentSetupErrors(t *testing.T) {
	fsys, _, _ := OpenPak("")
	boom := errors.New("boom")
	c := smallConfig()
	c.Present = PresentWebGPU

	withPresent(t, func(Config) (*PresentSetup, error) { return nil, boom })
	if _, err := Run(c, fsys, "x"); !errors.Is(err, boom) {
		t.Errorf("setup error = %v", err)
	}
	withPresent(t, func(Config) (*PresentSetup, error) {
		return &PresentSetup{Adapter: "swiftshader", Fallback: true}, nil
	})
	req := c
	req.RequireGPU = true
	if _, err := Run(req, fsys, "x"); !errors.Is(err, ErrFallbackAdapter) {
		t.Errorf("a fallback adapter where a GPU is required = %v", err)
	}
	// Without the requirement a fallback adapter runs and says so.
	res, err := Run(c, fsys, "x")
	if err != nil || !res.AdapterFallback {
		t.Errorf("fallback allowed: %v, %+v", err, res)
	}
	withPresent(t, func(Config) (*PresentSetup, error) {
		return &PresentSetup{Renderer: func(func(*render.FrameBuffer, [3]float32, [3]float32) error) (render.Renderer, error) {
			return nil, boom
		}}, nil
	})
	if _, err := Run(c, fsys, "x"); !errors.Is(err, boom) {
		t.Errorf("renderer error = %v", err)
	}
	waitErr := func(at int) func(Config) (*PresentSetup, error) {
		return func(Config) (*PresentSetup, error) {
			n := 0
			return &PresentSetup{Wait: func() error {
				n++
				if n == at {
					return boom
				}
				return nil
			}}, nil
		}
	}
	for at := 1; at <= 2; at++ {
		withPresent(t, waitErr(at))
		if _, err := Run(c, fsys, "x"); !errors.Is(err, boom) {
			t.Errorf("wait %d error = %v", at, err)
		}
	}
}

func TestPresentArgs(t *testing.T) {
	c := DefaultConfig()
	c.Present, c.RequireGPU = PresentWebGPU, true
	if a := strings.Join(c.Args(), " "); !strings.HasSuffix(a, "--present webgpu --require-gpu") {
		t.Errorf("Args = %q", a)
	}
}
