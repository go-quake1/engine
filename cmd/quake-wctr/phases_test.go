// Copyright (c) 2026 the go-quake1/engine authors.
// SPDX-License-Identifier: GPL-2.0-or-later

package main

import (
	"bytes"
	"testing"
	"time"

	"github.com/go-quake1/engine/render"
)

func TestWritePhases(t *testing.T) {
	ms := func(x float64) float64 { return x / 1000 }
	frame := func(sim, world, models, d2, present, work, interval float64) render.FrameSample {
		return render.FrameSample{
			Stages:   [render.NumStages]float64{ms(sim), ms(world), ms(models), ms(d2), ms(present)},
			Work:     ms(work),
			Interval: ms(interval),
		}
	}
	// The first frame is the warm-up and is left out.
	samples := []render.FrameSample{
		frame(99, 99, 99, 99, 99, 99, 0),
		frame(1, 10, 2, 3, 4, 20, 25),
		frame(3, 12, 4, 5, 6, 30, 35),
	}
	g := &gpuTimes{}
	g.add(50*time.Millisecond, 9*time.Millisecond) // warm-up, left out
	g.add(4*time.Millisecond, time.Millisecond)
	g.add(6*time.Millisecond, time.Millisecond)
	var out bytes.Buffer
	writePhases(&out, samples, g)
	want := "phases over 2 frames: sim-ms=2.000 world-ms=5.000 gpu-ms=5.000 copy-ms=1.000 models-ms=3.000 composite-ms=4.000 present-ms=5.000 wait-ms=5.000 frame-ms=30.000\n"
	if out.String() != want {
		t.Errorf("got  %q\nwant %q", out.String(), want)
	}
	// Without a GPU world the world stage is the software world.
	out.Reset()
	writePhases(&out, samples, nil)
	want = "phases over 2 frames: sim-ms=2.000 world-ms=11.000 gpu-ms=0.000 copy-ms=0.000 models-ms=3.000 composite-ms=4.000 present-ms=5.000 wait-ms=5.000 frame-ms=30.000\n"
	if out.String() != want {
		t.Errorf("software: got %q\nwant %q", out.String(), want)
	}
	// One frame has no steady state to report.
	out.Reset()
	writePhases(&out, samples[:1], nil)
	if out.Len() != 0 {
		t.Errorf("one frame printed %q", out.String())
	}
}
