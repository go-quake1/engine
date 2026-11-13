// Copyright (c) 2026 the go-quake1/engine authors.
// SPDX-License-Identifier: GPL-2.0-or-later

package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/go-quake1/engine/render"
	"github.com/go-quake1/engine/worldview"
)

type countingGPU struct {
	frames *int
	w, h   int
	closed *int
}

func (c countingGPU) Draw(worldview.View) ([]byte, error) {
	*c.frames++
	return bytes.Repeat([]byte{0x10}, c.w*c.h), nil
}
func (c countingGPU) Close() { *c.closed++ }

func TestGameFlagErrors(t *testing.T) {
	if _, _, err := run(t, "--pak", emptyPak(t), "--gpuworld-check"); err == nil || !strings.Contains(err.Error(), "needs --gpuworld") {
		t.Errorf("--gpuworld-check alone: %v", err)
	}
	if _, _, err := run(t, "--pak", emptyPak(t), "--gpuworld", "--gpuworld-check", "--worldref", "0"); err == nil || !strings.Contains(err.Error(), "game loop") {
		t.Errorf("--gpuworld-check with --worldref: %v", err)
	}
}

// TestGameWorldCopiesRows: the packed GPU frame goes into a frame buffer whose
// rows are wider (pitch), row by row, and the camera and field of view reach the
// GPU frame's view.
func TestGameWorldCopiesRows(t *testing.T) {
	const w, h = 6, 3
	fb, err := render.NewFrameBufferAligned(w, h, w+4)
	if err != nil {
		t.Fatal(err)
	}
	frames, closes := 0, 0
	g := &gameWorld{wv: &worldview.World{}, gd: fakeGPUFrame{frame: func() []byte {
		b := make([]byte, w*h)
		for i := range b {
			b[i] = byte(i + 1)
		}
		return b
	}(), frames: &frames, closed: &closes}, w: w}
	if err := g.DrawWorld(fb, [3]float32{1, 2, 3}, [3]float32{4, 5, 6}, 90, 0); err != nil {
		t.Fatal(err)
	}
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			if fb.Pixels[y*fb.Pitch+x] != byte(y*w+x+1) {
				t.Fatalf("pixel (%d,%d) = %d", x, y, fb.Pixels[y*fb.Pitch+x])
			}
		}
	}
	if g.wv.FOV != 90 || frames != 1 {
		t.Errorf("fov %v, frames %d", g.wv.FOV, frames)
	}
	g.gd = fakeGPUFrame{frame: make([]byte, 5), frames: &frames, closed: &closes}
	if err := g.DrawWorld(fb, [3]float32{}, [3]float32{}, 90, 0); err == nil {
		t.Error("a short GPU frame was accepted")
	}
	g.Close()
	if closes != 1 {
		t.Errorf("closed %d times", closes)
	}
}

type fakeGPUFrame struct {
	frame  []byte
	frames *int
	closed *int
}

func (f fakeGPUFrame) Draw(worldview.View) ([]byte, error) { *f.frames++; return f.frame, nil }
func (f fakeGPUFrame) Close()                              { *f.closed++ }

func TestCheckStats(t *testing.T) {
	const w, h = 8, 4
	cpu := refFrame(t, w, h)
	same := append([]byte(nil), cpu.Pixels...)
	var c checkStats
	var out bytes.Buffer
	if err := c.report(&out); err == nil {
		t.Error("no frame compared passed")
	}
	c.add(cpu, same)
	c.add(cpu, same)
	if err := c.report(&out); err != nil || !strings.Contains(out.String(), "2 frames, 0 of 64 pixels differ") || !strings.Contains(out.String(), "PASS") {
		t.Errorf("equal frames: %v\n%s", err, out.String())
	}
	wrong := append([]byte(nil), same...)
	for i := range wrong {
		wrong[i] += 77
	}
	c.add(cpu, wrong)
	if err := c.report(&out); err == nil || !strings.Contains(err.Error(), "above the tolerance") {
		t.Errorf("wrong frame: %v", err)
	}
}

// TestRunGameGPUWorld plays frames of the real map with the world drawn through
// a fake GPU: it is opened once, drawn once per frame, closed once, and every
// frame reaches the presenter. (Needs the freepak pak0.pak.)
func TestRunGameGPUWorld(t *testing.T) {
	p := &fakePresenter{}
	closes, gclose, gframes := 0, 0, 0
	withSurface(t, p, false, &closes)
	opened := 0
	old := openGPUWorld
	openGPUWorld = func(_ *worldview.World, _ *render.ColorMap, w, h int) (gpuDrawer, error) {
		opened++
		return countingGPU{frames: &gframes, w: w, h: h, closed: &gclose}, nil
	}
	t.Cleanup(func() { openGPUWorld = old })
	var out, errOut bytes.Buffer
	if err := runGameWith(&out, &errOut, realFS(t), "e1m1", 64, 40, 3, gameOpts{gpuWorld: true}); err != nil {
		t.Fatalf("%v\n%s", err, errOut.String())
	}
	if opened != 1 || gclose != 1 || gframes < 3 || p.frames != 3 {
		t.Errorf("opened %d, closed %d, GPU frames %d, presented %d", opened, gclose, gframes, p.frames)
	}
}
