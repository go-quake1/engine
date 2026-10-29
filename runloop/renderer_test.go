// Copyright (c) 2026 the go-quake1/engine authors.
// SPDX-License-Identifier: GPL-2.0-or-later

package runloop

import (
	"errors"
	"testing"

	"github.com/go-quake1/engine/backend"
	"github.com/go-quake1/engine/render"
)

// recRenderer wraps the software renderer and records the calls
// RunFrame makes through the seam.
type recRenderer struct {
	render.Software
	calls      []string
	composeErr error
	resolveErr error
}

func (r *recRenderer) DrawScene(fb *render.FrameBuffer, o, a [3]float32) error {
	r.calls = append(r.calls, "scene")
	return r.Software.DrawScene(fb, o, a)
}

func (r *recRenderer) Compose(fb *render.FrameBuffer, ctx render.FrameContext) error {
	r.calls = append(r.calls, "compose")
	if r.composeErr != nil {
		return r.composeErr
	}
	return r.Software.Compose(fb, ctx)
}

func (r *recRenderer) Resolve(fb *render.FrameBuffer, dst []byte, pal *render.Palette) error {
	r.calls = append(r.calls, "resolve")
	if r.resolveErr != nil {
		return r.resolveErr
	}
	return r.Software.Resolve(fb, dst, pal)
}

// TestRunFrame_RendererSeam: a configured Renderer replaces Pre2DDraw
// and is driven scene -> compose -> resolve, then the frame presents.
func TestRunFrame_RendererSeam(t *testing.T) {
	rec := backend.NewRecorder(0, 0)
	r, _ := newRunner(t, rec)
	r.Pre2DDraw = func(*render.FrameBuffer, [3]float32, [3]float32) error {
		t.Error("Pre2DDraw called although a Renderer is configured")
		return nil
	}
	rr := &recRenderer{}
	rr.Scene = func(fb *render.FrameBuffer, _, _ [3]float32) error {
		fb.Pixels[0] = 0x7F
		return nil
	}
	r.Renderer = rr
	if err := r.RunFrame(0.05, 1); err != nil {
		t.Fatalf("RunFrame: %v", err)
	}
	if got := len(rr.calls); got != 3 || rr.calls[0] != "scene" || rr.calls[1] != "compose" || rr.calls[2] != "resolve" {
		t.Fatalf("calls = %v", rr.calls)
	}
	if r.FrameBuffer.Pixels[0] != 0x7F {
		t.Fatal("scene pixel overwritten: background fill not skipped")
	}
	if len(rec.Frames) != 1 {
		t.Fatalf("frames = %d", len(rec.Frames))
	}
}

// TestRunFrame_RendererNoScene: a renderer without a scene gets the
// background fill and no DrawScene call.
func TestRunFrame_RendererNoScene(t *testing.T) {
	rec := backend.NewRecorder(0, 0)
	r, _ := newRunner(t, rec)
	rr := &recRenderer{}
	r.Renderer = rr
	if err := r.RunFrame(0.05, 1); err != nil {
		t.Fatalf("RunFrame: %v", err)
	}
	if len(rr.calls) != 2 || rr.calls[0] != "compose" {
		t.Fatalf("calls = %v", rr.calls)
	}
}

func TestRunFrame_RendererErrors(t *testing.T) {
	for _, tc := range []struct {
		name string
		set  func(*recRenderer, error)
	}{
		{"compose", func(rr *recRenderer, e error) { rr.composeErr = e }},
		{"resolve", func(rr *recRenderer, e error) { rr.resolveErr = e }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := backend.NewRecorder(0, 0)
			r, _ := newRunner(t, rec)
			rr := &recRenderer{}
			boom := errors.New(tc.name + " boom")
			tc.set(rr, boom)
			r.Renderer = rr
			ft, _ := render.NewFrameTimer(func() float64 { return 0 }, 4)
			r.Timer = ft
			if err := r.RunFrame(0.05, 1); !errors.Is(err, boom) {
				t.Fatalf("err = %v", err)
			}
			if len(rec.Frames) != 0 || ft.Len() != 0 {
				t.Fatalf("frames %d, timed %d after an error", len(rec.Frames), ft.Len())
			}
		})
	}
}

// TestRunFrame_Timer: each RunFrame records one sample whose stages
// carry the time spent between the marks.
func TestRunFrame_Timer(t *testing.T) {
	rec := backend.NewRecorder(0, 0)
	r, _ := newRunner(t, rec)
	now := 0.0
	ft, err := render.NewFrameTimer(func() float64 { now += 0.001; return now }, 8)
	if err != nil {
		t.Fatal(err)
	}
	r.Timer = ft
	r.Pre2DDraw = func(*render.FrameBuffer, [3]float32, [3]float32) error {
		now += 0.010
		return nil
	}
	for i := 0; i < 2; i++ {
		if err := r.RunFrame(0.05, float32(i)); err != nil {
			t.Fatalf("RunFrame: %v", err)
		}
	}
	s := ft.Samples()
	if len(s) != 2 {
		t.Fatalf("samples = %d", len(s))
	}
	// The runloop marks sim, world (the scene's residue), 2D and
	// present; models is left to a renderer that attributes it.
	for st := render.Stage(0); st < render.NumStages; st++ {
		if timed := s[1].Stages[st] > 0; timed != (st != render.StageModels) {
			t.Errorf("stage %v = %v", st, s[1].Stages[st])
		}
	}
	if s[1].Stages[render.StageWorld] < 0.010 || s[1].Interval <= 0 || s[1].Work <= 0 {
		t.Errorf("sample %+v", s[1])
	}
}
