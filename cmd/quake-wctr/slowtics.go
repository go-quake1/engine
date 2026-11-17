// Copyright (c) 2026 the go-quake1/engine authors.
// SPDX-License-Identifier: GPL-2.0-or-later

package main

import (
	"fmt"
	"io"
	"runtime/metrics"
	"time"

	"github.com/go-quake1/engine/render"
	"github.com/go-quake1/engine/runloop"
)

// slowTicBound is the tic length (seconds) over which a frame is reported with
// where its time went: the mixer's mix-ahead, the longest a tic can be before
// the sound output may run dry.
const slowTicBound = 0.1

// audioWriteTimer is a backend that times its sound writes (the host call
// stream_write): TakeWriteTime returns the time spent since the last call.
type audioWriteTimer interface{ TakeWriteTime() time.Duration }

// frameProbe records, per frame, what the frame's stages do not say: the Go
// collector's cycles and pauses and the time of the sound write. Its readings
// go with the render.FrameSample of the same frame into the slow-tic lines.
type frameProbe struct {
	samples [3]metrics.Sample
	wt      audioWriteTimer

	gcCycles   uint64
	gcPause    float64
	GC         []int           // collector cycles that finished during the frame
	GCPause    []time.Duration // pause time added during the frame
	AudioWrite []time.Duration // stream_write time during the frame
	Steps      []int           // fixed server ticks the frame ran (the runner's LastSimSteps)

	steps func() int
}

// newFrameProbe probes be (an audioWriteTimer, or not) and steps, the runner's
// count of server ticks in its last frame (nil: not recorded).
func newFrameProbe(be any, steps func() int) *frameProbe {
	p := &frameProbe{steps: steps}
	p.samples[0].Name = "/gc/cycles/total:gc-cycles"
	p.samples[1].Name = "/gc/pauses:seconds"
	p.samples[2].Name = "/sched/pauses/total/gc:seconds"
	p.wt, _ = be.(audioWriteTimer)
	p.read() // the baseline
	return p
}

// read returns the collector's cycle count and total pause time so far.
func (p *frameProbe) read() (cycles uint64, pause float64) {
	metrics.Read(p.samples[:])
	if p.samples[0].Value.Kind() == metrics.KindUint64 {
		cycles = p.samples[0].Value.Uint64()
	}
	for _, s := range p.samples[1:] {
		if s.Value.Kind() != metrics.KindFloat64Histogram {
			continue
		}
		h := s.Value.Float64Histogram()
		var sum float64
		for i, c := range h.Counts {
			lo, hi := h.Buckets[i], h.Buckets[i+1]
			if lo < 0 || lo > 1e9 {
				lo = hi
			}
			if hi > 1e9 {
				hi = lo
			}
			sum += float64(c) * (lo + hi) / 2
		}
		if sum > pause { // the two names carry the same pauses; keep the larger
			pause = sum
		}
	}
	return cycles, pause
}

// frame is called after each RunFrame.
func (p *frameProbe) frame() {
	if p == nil {
		return
	}
	c, ps := p.read()
	p.GC = append(p.GC, int(c-p.gcCycles))
	p.GCPause = append(p.GCPause, time.Duration((ps-p.gcPause)*float64(time.Second)))
	p.gcCycles, p.gcPause = c, ps
	var w time.Duration
	if p.wt != nil {
		w = p.wt.TakeWriteTime()
	}
	p.AudioWrite = append(p.AudioWrite, w)
	n := 0
	if p.steps != nil {
		n = p.steps()
	}
	p.Steps = append(p.Steps, n)
}

// reset forgets the frames read so far (the next window starts empty).
func (p *frameProbe) reset() {
	if p == nil {
		return
	}
	p.GC, p.GCPause, p.AudioWrite, p.Steps = p.GC[:0], p.GCPause[:0], p.AudioWrite[:0], p.Steps[:0]
}

// reportWindow prints the slow tics of the frames since the last report and
// starts a new window: the timer, the probe and the GPU times are emptied.
func reportWindow(out io.Writer, r *runloop.Runner, gpu *gpuTimes, p *frameProbe) {
	writeSlowTics(out, r.Timer.Samples(), gpu, p)
	r.Timer.Reset()
	p.reset()
	if gpu != nil {
		gpu.gpu, gpu.copy = gpu.gpu[:0], gpu.copy[:0]
	}
}

// writeSlowTics prints one line for every frame whose wall time (the interval
// since the previous frame began) was over slowTicBound, with the time of each
// stage, what the stages do not mark (the audio mix and the rest), the time
// outside the frame (the yield and the host's wait between frames), the GPU
// submit and read-back, the sound write and the collector. It ends with the
// totals: how much of the slow tics' time each part holds.
func writeSlowTics(out io.Writer, samples []render.FrameSample, gpu *gpuTimes, p *frameProbe) {
	if len(samples) < 2 || p == nil {
		return
	}
	var gpuAt func(i int) (g, c time.Duration)
	gpuAt = func(int) (time.Duration, time.Duration) { return 0, 0 }
	if gpu != nil && len(gpu.gpu) > 0 {
		off := len(samples) - len(gpu.gpu)
		gpuAt = func(i int) (time.Duration, time.Duration) {
			if j := i - off; j >= 0 && j < len(gpu.gpu) {
				return gpu.gpu[j], gpu.copy[j]
			}
			return 0, 0
		}
	}
	var n int
	var tot struct{ iv, sim, world, gpu, copy, models, comp, present, unstaged, outside, write, gc float64 }
	// A sample's Interval is the wall time from the previous frame's start to its
	// own: the previous frame's work and the gap after it (the yield, the host's
	// wait). The tic that lasted that long is therefore the PREVIOUS frame, and
	// it is its stages, its GPU times, its collector and write readings that are
	// reported with it.
	for k, next := range samples {
		i := k - 1
		if i < 0 || i >= len(p.GC) || next.Interval <= slowTicBound {
			continue
		}
		s := samples[i]
		s.Interval = next.Interval
		g, c := gpuAt(i)
		var staged float64
		for _, v := range s.Stages {
			staged += v
		}
		unstaged := max(s.Work-staged, 0)
		outside := max(s.Interval-s.Work, 0)
		world := max(s.Stages[render.StageWorld]-g.Seconds()-c.Seconds(), 0)
		w, gp := p.AudioWrite[i].Seconds(), p.GCPause[i].Seconds()
		steps := 0
		if i < len(p.Steps) {
			steps = p.Steps[i]
		}
		fmt.Fprintf(out, "slowtic frame=%d steps=%d interval-ms=%.1f sim=%.1f world=%.1f gpu=%.1f copy=%.1f models=%.1f composite=%.1f present=%.1f unstaged=%.1f outside=%.1f audio-write=%.2f gc-cycles=%d gc-pause-ms=%.2f\n",
			i, steps, s.Interval*1e3, s.Stages[render.StageSim]*1e3, world*1e3, ms(g), ms(c), s.Stages[render.StageModels]*1e3, s.Stages[render.Stage2D]*1e3,
			s.Stages[render.StagePresent]*1e3, unstaged*1e3, outside*1e3, w*1e3, p.GC[i], gp*1e3)
		n++
		tot.iv += s.Interval
		tot.sim += s.Stages[render.StageSim]
		tot.world += world
		tot.gpu += g.Seconds()
		tot.copy += c.Seconds()
		tot.models += s.Stages[render.StageModels]
		tot.comp += s.Stages[render.Stage2D]
		tot.present += s.Stages[render.StagePresent]
		tot.unstaged += unstaged
		tot.outside += outside
		tot.write += w
		tot.gc += gp
	}
	if n == 0 {
		fmt.Fprintf(out, "slowtics: none over %.0f ms in %d frames\n", slowTicBound*1e3, len(samples))
		return
	}
	pc := func(v float64) float64 { return 100 * v / tot.iv }
	fmt.Fprintf(out, "slowtics: %d over %.0f ms, %.0f ms in all; share of that time: sim %.0f%% world %.0f%% gpu %.0f%% copy %.0f%% models %.0f%% composite %.0f%% present %.0f%% unstaged(audio mix, other) %.0f%% outside(yield, host wait) %.0f%%; of it audio-write %.1f%% gc-pause %.1f%%\n",
		n, slowTicBound*1e3, tot.iv*1e3, pc(tot.sim), pc(tot.world), pc(tot.gpu), pc(tot.copy), pc(tot.models), pc(tot.comp), pc(tot.present), pc(tot.unstaged), pc(tot.outside), pc(tot.write), pc(tot.gc))
}
