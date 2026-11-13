// Copyright (c) 2026 the go-quake1/engine authors.
// SPDX-License-Identifier: GPL-2.0-or-later

package main

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/go-quake1/engine/render"
	"github.com/go-quake1/engine/worldview"
)

type fakeGPU struct {
	frame  []byte
	err    error
	closed *int
}

func (f fakeGPU) Draw(worldview.View) ([]byte, error) { return f.frame, f.err }
func (f fakeGPU) Close()                              { *f.closed++ }

// refFrame is a w x h software frame of vertical stripes.
func refFrame(t *testing.T, w, h int) *render.FrameBuffer {
	t.Helper()
	fb, err := render.NewFrameBuffer(w, h)
	if err != nil {
		t.Fatal(err)
	}
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			fb.Pixels[y*fb.Pitch+x] = byte(1 + x/3%200)
		}
	}
	return fb
}

func packed(fb *render.FrameBuffer) []byte {
	out := make([]byte, 0, fb.Width*fb.Height)
	for y := 0; y < fb.Height; y++ {
		out = append(out, fb.Pixels[y*fb.Pitch:y*fb.Pitch+fb.Width]...)
	}
	return out
}

func withGPU(t *testing.T, f fakeGPU, closes *int) {
	t.Helper()
	old := openGPUWorld
	openGPUWorld = func(*worldview.World, *render.ColorMap, int, int) (gpuDrawer, error) {
		f.closed = closes
		return f, nil
	}
	t.Cleanup(func() { openGPUWorld = old })
}

// TestGPUWorldFrameVerdict: the GPU frame is judged by worldcheck's measure,
// not a hash. A frame equal to the reference, and one shifted by a pixel, pass;
// a frame with the wrong texels fails; the device is closed every time.
func TestGPUWorldFrameVerdict(t *testing.T) {
	const w, h = 96, 64
	ref := refFrame(t, w, h)
	same := packed(ref)
	shifted := make([]byte, len(same))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			shifted[y*w+x] = same[y*w+min(x+1, w-1)]
		}
	}
	wrong := make([]byte, len(same))
	for i, b := range same {
		wrong[i] = b + 50
	}
	for _, c := range []struct {
		name  string
		frame []byte
		pass  bool
		want  string
	}{
		{"equal", same, true, "gpuworld 0 of 6144 pixels differ, 0 unexplained"},
		{"shifted by one pixel", shifted, true, "0 unexplained"},
		{"wrong texels", wrong, false, "above the tolerance"},
	} {
		closes := 0
		withGPU(t, fakeGPU{frame: c.frame}, &closes)
		var out bytes.Buffer
		got, err := gpuWorldFrame(&out, nil, nil, worldview.View{}, 4, w, h, ref)
		if c.pass != (err == nil) {
			t.Errorf("%s: err %v, want pass %v\n%s", c.name, err, c.pass, out.String())
		}
		text := out.String()
		if err != nil {
			text = err.Error()
		} else if !bytes.Equal(got, c.frame) || !strings.Contains(text, "gpuworld PASS") {
			t.Errorf("%s: frame or PASS line missing:\n%s", c.name, text)
		}
		if !strings.Contains(text, c.want) {
			t.Errorf("%s: %q lacks %q", c.name, text, c.want)
		}
		if closes != 1 {
			t.Errorf("%s: device closed %d times", c.name, closes)
		}
	}
}

func TestGPUWorldFrameErrors(t *testing.T) {
	ref := refFrame(t, 8, 8)
	old := openGPUWorld
	openGPUWorld = nil
	if _, err := gpuWorldFrame(&bytes.Buffer{}, nil, nil, worldview.View{}, 0, 8, 8, ref); !errors.Is(err, ErrNoGPUWorld) {
		t.Errorf("no device: %v", err)
	}
	openGPUWorld = old
	boom := errors.New("lost")
	closes := 0
	withGPU(t, fakeGPU{err: boom}, &closes)
	if _, err := gpuWorldFrame(&bytes.Buffer{}, nil, nil, worldview.View{}, 0, 8, 8, ref); !errors.Is(err, boom) || closes != 1 {
		t.Errorf("draw error: %v, closes %d", err, closes)
	}
	withGPU(t, fakeGPU{frame: make([]byte, 5)}, &closes)
	if _, err := gpuWorldFrame(&bytes.Buffer{}, nil, nil, worldview.View{}, 0, 8, 8, ref); err == nil {
		t.Error("a short frame was accepted")
	}
	openGPUWorld = func(*worldview.World, *render.ColorMap, int, int) (gpuDrawer, error) { return nil, boom }
	t.Cleanup(func() { openGPUWorld = old })
	if _, err := gpuWorldFrame(&bytes.Buffer{}, nil, nil, worldview.View{}, 0, 8, 8, ref); !errors.Is(err, boom) {
		t.Errorf("open error: %v", err)
	}
}

// TestGPUWorldInTheGameNeedsASurface: --gpuworld alone is the game loop on the
// GPU, which needs a surface like any game.
func TestGPUWorldInTheGameNeedsASurface(t *testing.T) {
	old := openSurface
	openSurface = nil
	t.Cleanup(func() { openSurface = old })
	if _, _, err := run(t, "--pak", emptyPak(t), "--gpuworld"); !errors.Is(err, ErrNoSurface) {
		t.Errorf("--gpuworld alone: %v", err)
	}
}
