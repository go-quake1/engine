// Copyright (c) 2026 the go-quake1/engine authors.
// SPDX-License-Identifier: GPL-2.0-or-later

package main

import (
	"fmt"
	"io"

	"github.com/go-quake1/engine/assets"
	"github.com/go-quake1/engine/render"
	"github.com/go-quake1/engine/render/gpuworld"
	"github.com/go-quake1/engine/vfs"
	"github.com/go-quake1/engine/worldview"

	"io/fs"
)

// gameOpts are the game loop's GPU options.
type gameOpts struct {
	gpuWorld bool // draw the lit world faces on the batch device
	check    bool // with gpuWorld: compare every frame with the CPU's
}

// gameWorld is the runner's WorldDrawer over a [gpuDrawer]: the map is built
// and the atlases uploaded once, when it is opened; a frame is one submit with
// its read-back, and the index rows are copied into the frame buffer.
type gameWorld struct {
	wv *worldview.World
	gd gpuDrawer
	w  int
}

// openGameWorld loads the map's GPU content for a w x h frame.
func openGameWorld(fsys fs.FS, slug string, w, h int) (*gameWorld, error) {
	if openGPUWorld == nil {
		return nil, ErrNoGPUWorld
	}
	wv, err := worldview.LoadMap(fsys, slug)
	if err != nil {
		return nil, err
	}
	v := vfs.New()
	v.Add(fsys)
	cm, err := assets.LoadColorMapFrom(v)
	if err != nil {
		return nil, err
	}
	gd, err := openGPUWorld(wv, cm, w, h)
	if err != nil {
		return nil, err
	}
	return &gameWorld{wv: wv, gd: gd, w: w}, nil
}

func (g *gameWorld) Close() { g.gd.Close() }

// DrawWorld implements runner.WorldDrawer.
func (g *gameWorld) DrawWorld(fb *render.FrameBuffer, origin, angles [3]float32, fov, t float32) error {
	g.wv.FOV = fov
	frame, err := g.gd.Draw(worldview.View{Origin: origin, Angles: angles})
	if err != nil {
		return err
	}
	if len(frame) != g.w*fb.Height || fb.Width != g.w {
		return fmt.Errorf("the GPU frame has %d bytes for a %dx%d frame buffer", len(frame), fb.Width, fb.Height)
	}
	for y := 0; y < fb.Height; y++ {
		copy(fb.Pixels[y*fb.Pitch:y*fb.Pitch+g.w], frame[y*g.w:(y+1)*g.w])
	}
	return nil
}

// checkStats accumulates the per-frame comparison of --gpuworld-check.
type checkStats struct {
	frames, pixels, differ, unexplained int
	worst                               float64 // largest share of unexplained pixels in a frame
}

func (c *checkStats) add(cpu *render.FrameBuffer, gpu []byte) {
	d, u := gpuworld.CompareStrided(cpu.Pixels, cpu.Pitch, gpu, cpu.Pitch, cpu.Width, cpu.Height)
	c.frames++
	c.pixels += cpu.Width * cpu.Height
	c.differ += d
	c.unexplained += u
	c.worst = max(c.worst, float64(u)/float64(cpu.Width*cpu.Height))
}

// report prints the verdict and returns an error if a frame was above the
// tolerance, or no frame was compared.
func (c *checkStats) report(out io.Writer) error {
	fmt.Fprintf(out, "gpuworld check: %d frames, %d of %d pixels differ, %d unexplained, worst frame %.4f unexplained (tolerance %.3f)\n",
		c.frames, c.differ, c.pixels, c.unexplained, c.worst, gpuworld.Tolerance)
	if c.frames == 0 {
		return fmt.Errorf("quake-wctr: --gpuworld-check compared no frame")
	}
	if c.worst > gpuworld.Tolerance {
		return fmt.Errorf("quake-wctr: a frame differs from the CPU's by %.4f unexplained, above the tolerance %.3f", c.worst, gpuworld.Tolerance)
	}
	fmt.Fprintln(out, "gpuworld check PASS")
	return nil
}
