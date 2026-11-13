// Copyright (c) 2026 the go-quake1/engine authors.
// SPDX-License-Identifier: GPL-2.0-or-later

package main

import (
	"fmt"
	"io"
	"slices"
	"time"

	"github.com/go-quake1/engine/render"
	"github.com/go-quake1/engine/worldview"
)

// frameStats are the per-frame wall-clock times of a --worldref --frames run.
type frameStats struct {
	Warmup       time.Duration // the first frame (on the GPU: device, map build, upload)
	Mean, Median time.Duration // of the frames after it
	Min, Max     time.Duration
	Frames       int
}

// statsOf summarises the frames after the first: warm is the first frame's
// time, rest the others'.
func statsOf(warm time.Duration, rest []time.Duration) frameStats {
	s := frameStats{Warmup: warm, Frames: len(rest)}
	if len(rest) == 0 {
		return s
	}
	d := slices.Clone(rest)
	slices.Sort(d)
	var sum time.Duration
	for _, x := range d {
		sum += x
	}
	s.Mean, s.Min, s.Max = sum/time.Duration(len(d)), d[0], d[len(d)-1]
	if n := len(d); n%2 == 1 {
		s.Median = d[n/2]
	} else {
		s.Median = (d[n/2-1] + d[n/2]) / 2
	}
	return s
}

func ms(d time.Duration) float64 { return float64(d.Microseconds()) / 1000 }

// print writes one line: mode, warm-up apart, then mean, median, min and max
// of the others, in milliseconds. Present time is not in any of them.
func (s frameStats) print(out io.Writer, mode string, n, w, h int) {
	fmt.Fprintf(out, "view %d %dx%d %s frames: warmup %.3f ms, then %d frames: mean %.3f ms, median %.3f ms, min %.3f ms, max %.3f ms\n",
		n, w, h, mode, ms(s.Warmup), s.Frames, ms(s.Mean), ms(s.Median), ms(s.Min), ms(s.Max))
}

// timeFrames runs draw frames+1 times, the first timed apart.
func timeFrames(frames int, draw func() error) (frameStats, error) {
	var warm time.Duration
	rest := make([]time.Duration, 0, frames)
	for i := 0; i <= frames; i++ {
		t := time.Now()
		if err := draw(); err != nil {
			return frameStats{}, err
		}
		if d := time.Since(t); i == 0 {
			warm = d
		} else {
			rest = append(rest, d)
		}
	}
	return statsOf(warm, rest), nil
}

// timeSoftware times frames+1 software draws of view v into fb.
func timeSoftware(out io.Writer, wv *worldview.World, cm *render.ColorMap, v worldview.View, n, w, h, frames int, fb *render.FrameBuffer) error {
	s, err := timeFrames(frames, func() error { return wv.DrawSoftware(fb, cm, v) })
	if err != nil {
		return err
	}
	s.print(out, "software", n, w, h)
	return nil
}

// timeGPU times frames+1 GPU draws of view v (pass, copy and read-back in one
// submit) on a device of its own: the first frame includes opening it, building
// the map and uploading the atlases.
func timeGPU(out io.Writer, wv *worldview.World, cm *render.ColorMap, v worldview.View, n, w, h, frames int) error {
	if openGPUWorld == nil {
		return ErrNoGPUWorld
	}
	var gd gpuDrawer
	s, err := timeFrames(frames, func() (err error) {
		if gd == nil {
			if gd, err = openGPUWorld(wv, cm, w, h); err != nil {
				return err
			}
		}
		_, err = gd.Draw(v)
		return err
	})
	if gd != nil {
		gd.Close()
	}
	if err != nil {
		return err
	}
	s.print(out, "gpuworld", n, w, h)
	return nil
}
