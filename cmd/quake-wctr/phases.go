// Copyright (c) 2026 the go-quake1/engine authors.
// SPDX-License-Identifier: GPL-2.0-or-later

package main

import (
	"fmt"
	"io"
	"time"

	"github.com/go-quake1/engine/render"
)

// gpuTimes are the per-frame times the GPU world drawer spends in its two
// parts: the submit with the read-back, and the copy of the index rows into
// the frame buffer. The runner counts both inside its world stage.
type gpuTimes struct {
	gpu, copy []time.Duration
}

func (g *gpuTimes) add(gpu, cp time.Duration) {
	g.gpu = append(g.gpu, gpu)
	g.copy = append(g.copy, cp)
}

// meanMS is the mean of d in milliseconds, 0 for none.
func meanMS(d []time.Duration) float64 {
	if len(d) == 0 {
		return 0
	}
	var sum time.Duration
	for _, x := range d {
		sum += x
	}
	return ms(sum) / float64(len(d))
}

// tail drops the first frame (the warm-up) of d, unless it is the only one.
func tail(d []time.Duration) []time.Duration {
	if len(d) > 1 {
		return d[1:]
	}
	return d
}

// writePhases prints where a game frame's time goes, as one line of
// key=value milliseconds per frame, over the frames after the first:
//
//	sim-ms     input, server ticks, client tick, particle step
//	world-ms   the software world pass (BSP walk, sky, water and, without a
//	           GPU world, every lit face): the runner's world stage less gpu and copy
//	gpu-ms     GPU world submit and read-back (0 without --gpuworld)
//	copy-ms    GPU frame into the frame buffer (0 without --gpuworld)
//	models-ms  alias models, sprites, particles, beams, view model
//	composite-ms  the 2D layers (status bar, menu, console)
//	present-ms the palette resolve and the hand-off to the surface
//	wait-ms    the time between the end of a frame and the start of the next
//	frame-ms   the frame to frame wall time, the sum of the above (plus
//	           whatever the stages do not mark)
//
// gpu may be nil.
func writePhases(out io.Writer, samples []render.FrameSample, gpu *gpuTimes) {
	if len(samples) < 2 {
		return
	}
	sum := render.Summarize(samples[1:])
	var g, c float64
	if gpu != nil {
		g, c = meanMS(tail(gpu.gpu)), meanMS(tail(gpu.copy))
	}
	st := func(s render.Stage) float64 { return sum.Stages[s].Mean }
	wait := max(sum.Interval.Mean-sum.Work.Mean, 0)
	fmt.Fprintf(out, "phases over %d frames: sim-ms=%.3f world-ms=%.3f gpu-ms=%.3f copy-ms=%.3f models-ms=%.3f composite-ms=%.3f present-ms=%.3f wait-ms=%.3f frame-ms=%.3f\n",
		sum.Frames, st(render.StageSim), max(st(render.StageWorld)-g-c, 0), g, c,
		st(render.StageModels), st(render.Stage2D), st(render.StagePresent), wait, sum.Interval.Mean)
}
