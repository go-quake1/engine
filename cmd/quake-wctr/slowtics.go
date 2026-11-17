// Copyright (c) 2026 the go-quake1/engine authors.
// SPDX-License-Identifier: GPL-2.0-or-later

package main

import (
	"fmt"
	"io"
	"runtime/metrics"
	"runtime/pprof"
	"slices"
	"strings"
	"time"

	"github.com/go-quake1/engine/render"
	"github.com/go-quake1/engine/runloop"
	"github.com/go-quake1/engine/sound"
)

// slowTicBound is the tic length (seconds) over which a frame is reported with
// where its time went: the mixer's mix-ahead, the longest a tic can be before
// the sound output may run dry.
const slowTicBound = 0.1

// audioWriteTimer is a backend that times its sound writes (the host call
// stream_write): TakeWriteTime returns the time spent since the last call.
type audioWriteTimer interface{ TakeWriteTime() time.Duration }

// audioWriteLog is a backend that says when its last sound write returned and
// what the host reported queued after it.
type audioWriteLog interface {
	LastWrite() (at time.Time, queued int, ok bool)
}

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
	Yield      []time.Duration // the loop's yield (runtime.Gosched) after the frame
	ProbeCost  []time.Duration // what this probe's own reading cost
	WriteAt    []time.Time     // when the frame's last sound write returned (zero: none yet)
	Queued     []int           // the frames the host said were queued after it
	YieldGC    []time.Duration // the collector's background mark time that ran during the yield
	Goroutines []int           // goroutines alive after the yield

	steps func() int
	wl    audioWriteLog

	ys      [8]metrics.Sample // what the runtime's own goroutines do (see readYield)
	pend    yieldReading      // the yield just timed (taken by frame)
	pendGor int

	// What the runtime's background goroutines did during each frame's yield: the
	// scavenger's time, the cleanups (runtime.AddCleanup: the GPU handle ids,
	// whose cleanup sends a DESTROY) and the finalizers run.
	YieldScav       []time.Duration
	YieldCleanups   []int
	YieldFinalizers []int
	// Cleanups is the cleanups that ran in the whole frame, yield or not.
	Cleanups     []int
	cleanupsSeen uint64
	cleanupsNow  uint64
}

// yieldReading is the runtime's cumulative counters at one moment.
type yieldReading struct {
	mark, scav             float64 // collector mark workers, scavenger: cpu-seconds
	cleanups, finalizers   uint64  // executed so far
	cleanupsQ, finalizersQ uint64  // queued so far
}

// readYield reads the runtime's counters for its own goroutines: the collector's
// mark workers (dedicated and idle classes), the background scavenger, the
// cleanups and finalizers executed and queued, and the goroutine count.
func (p *frameProbe) readYield() (r yieldReading, goroutines int) {
	names := [...]string{
		"/cpu/classes/gc/mark/dedicated:cpu-seconds", "/cpu/classes/gc/mark/idle:cpu-seconds",
		"/cpu/classes/scavenge/background:cpu-seconds",
		"/gc/cleanups/executed:cleanups", "/gc/finalizers/executed:finalizers",
		"/gc/cleanups/queued:cleanups", "/gc/finalizers/queued:finalizers",
		"/sched/goroutines:goroutines",
	}
	for i, n := range names {
		p.ys[i].Name = n
	}
	metrics.Read(p.ys[:])
	f := func(i int) float64 {
		if p.ys[i].Value.Kind() == metrics.KindFloat64 {
			return p.ys[i].Value.Float64()
		}
		return 0
	}
	u := func(i int) uint64 {
		if p.ys[i].Value.Kind() == metrics.KindUint64 {
			return p.ys[i].Value.Uint64()
		}
		return 0
	}
	r = yieldReading{mark: f(0) + f(1), scav: f(2), cleanups: u(3), finalizers: u(4), cleanupsQ: u(5), finalizersQ: u(6)}
	return r, int(u(7))
}

// timeYield runs fn, the loop's yield, and returns how long it took. It notes
// what the collector's mark workers did during it, and how many goroutines there
// are, for the frame call that follows: what runs while the guest yields.
func (p *frameProbe) timeYield(fn func()) time.Duration {
	if p == nil {
		t0 := time.Now()
		fn()
		return time.Since(t0)
	}
	before, _ := p.readYield()
	t0 := time.Now()
	fn()
	d := time.Since(t0)
	after, gor := p.readYield()
	p.pend = yieldReading{mark: after.mark - before.mark, scav: after.scav - before.scav,
		cleanups: after.cleanups - before.cleanups, finalizers: after.finalizers - before.finalizers}
	p.pendGor = gor
	p.cleanupsNow = after.cleanups
	return d
}

// newFrameProbe probes be (an audioWriteTimer, or not) and steps, the runner's
// count of server ticks in its last frame (nil: not recorded).
func newFrameProbe(be any, steps func() int) *frameProbe {
	p := &frameProbe{steps: steps}
	p.samples[0].Name = "/gc/cycles/total:gc-cycles"
	p.samples[1].Name = "/gc/pauses:seconds"
	p.samples[2].Name = "/sched/pauses/total/gc:seconds"
	p.wt, _ = be.(audioWriteTimer)
	p.wl, _ = be.(audioWriteLog)
	p.read() // the baseline
	r, _ := p.readYield()
	p.cleanupsSeen, p.cleanupsNow = r.cleanups, r.cleanups
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

// frame is called after each RunFrame and its yield, which took yield.
func (p *frameProbe) frame(yield time.Duration) {
	if p == nil {
		return
	}
	t0 := time.Now()
	defer func() { p.ProbeCost = append(p.ProbeCost, time.Since(t0)) }()
	p.Yield = append(p.Yield, yield)
	p.YieldGC = append(p.YieldGC, time.Duration(p.pend.mark*float64(time.Second)))
	p.Goroutines = append(p.Goroutines, p.pendGor)
	p.YieldScav = append(p.YieldScav, time.Duration(p.pend.scav*float64(time.Second)))
	p.YieldCleanups = append(p.YieldCleanups, int(p.pend.cleanups))
	p.YieldFinalizers = append(p.YieldFinalizers, int(p.pend.finalizers))
	p.Cleanups = append(p.Cleanups, int(p.cleanupsNow-p.cleanupsSeen))
	p.cleanupsSeen = p.cleanupsNow
	p.pend, p.pendGor = yieldReading{}, 0
	var at time.Time
	var q int
	if p.wl != nil {
		at, q, _ = p.wl.LastWrite()
	}
	p.WriteAt, p.Queued = append(p.WriteAt, at), append(p.Queued, q)
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
	p.Yield, p.ProbeCost, p.WriteAt, p.Queued = p.Yield[:0], p.ProbeCost[:0], p.WriteAt[:0], p.Queued[:0]
	p.YieldGC, p.Goroutines = p.YieldGC[:0], p.Goroutines[:0]
	p.YieldScav, p.YieldCleanups, p.YieldFinalizers, p.Cleanups = p.YieldScav[:0], p.YieldCleanups[:0], p.YieldFinalizers[:0], p.Cleanups[:0]
}

// at returns s[i] or the zero value past its end (a probe filled by hand).
func at[T any](s []T, i int) T {
	var z T
	if i >= 0 && i < len(s) {
		return s[i]
	}
	return z
}

// writeAudioGaps says, for each sound write, whether the output could bridge the
// time since the one before it on what that one left queued: the gap between two
// writes against the frames queued after the first. A gap longer than its queue is
// a run-dry (an underrun the listener hears), and the line gives the gap, the
// queue and the shortfall, so a stall's length can be read against the audio's
// depth. The summary counts the dry gaps and the writes over 50, 100 and 200 ms.
func writeAudioGaps(out io.Writer, p *frameProbe) {
	if p == nil || p.wl == nil {
		return
	}
	var prevAt time.Time
	var prevQ int
	var writes, dry int
	var worst, total float64
	var over [3]int
	var queues []float64
	for j := range p.WriteAt {
		now := p.WriteAt[j]
		if now.IsZero() || now.Equal(prevAt) { // no write in this frame
			continue
		}
		if !prevAt.IsZero() {
			writes++
			gap := now.Sub(prevAt).Seconds()
			for i, b := range [3]float64{0.05, 0.1, 0.2} {
				if gap > b {
					over[i]++
				}
			}
			held := float64(prevQ) / sound.DefaultSampleRate
			if short := gap - held; short > 0 {
				dry++
				total += short
				worst = max(worst, short)
				queues = append(queues, held)
				fmt.Fprintf(out, "audiogap frame=%d gap-ms=%.1f queued-ms=%.1f short-ms=%.1f\n", j, gap*1e3, held*1e3, short*1e3)
			}
		}
		prevAt, prevQ = now, p.Queued[j]
	}
	slices.Sort(queues)
	med := 0.0
	if len(queues) > 0 {
		med = queues[len(queues)/2]
	}
	fmt.Fprintf(out, "audiogaps: %d of %d writes ran dry, %.0f ms short in all, worst %.1f ms, median queue before a dry gap %.1f ms; writes more than 50 100 200 ms after the one before: %d %d %d\n",
		dry, writes, total*1e3, worst*1e3, med*1e3, over[0], over[1], over[2])
}

// dumpGoroutines prints, once, the goroutines the guest has and where each one
// is (runtime/pprof, grouped by stack): the candidates for what runs while the
// loop yields. Each line is prefixed "goroutines:" so a log reader can keep it.
func dumpGoroutines(out io.Writer) {
	if goroutinesDumped {
		return
	}
	goroutinesDumped = true
	var b strings.Builder
	if err := pprof.Lookup("goroutine").WriteTo(&b, 1); err != nil {
		fmt.Fprintf(out, "goroutines: %v\n", err)
		return
	}
	for _, l := range strings.Split(strings.TrimRight(b.String(), "\n"), "\n") {
		if l = strings.TrimSpace(l); l != "" {
			fmt.Fprintf(out, "goroutines: %s\n", l)
		}
	}
}

var goroutinesDumped bool

// reportWindow prints the slow tics of the frames since the last report and
// starts a new window: the timer, the probe and the GPU times are emptied.
func reportWindow(out io.Writer, r *runloop.Runner, gpu *gpuTimes, p *frameProbe) {
	writeSlowTics(out, r.Timer.Samples(), gpu, p)
	writeAudioGaps(out, p)
	dumpGoroutines(out)
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
	var tot struct{ iv, sim, world, gpu, copy, models, comp, present, unstaged, outside, write, gc, yield, yieldGC, yieldScav float64 }
	var cleanups, finalizers, frameCleanups int
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
		fmt.Fprintf(out, "slowtic frame=%d steps=%d interval-ms=%.1f sim=%.1f world=%.1f gpu=%.1f copy=%.1f models=%.1f composite=%.1f present=%.1f unstaged=%.1f outside=%.1f yield=%.2f probe=%.2f yield-gc=%.2f gor=%d scav=%.2f cleanups=%d finalizers=%d frame-cleanups=%d audio-write=%.2f gc-cycles=%d gc-pause-ms=%.2f\n",
			i, steps, s.Interval*1e3, s.Stages[render.StageSim]*1e3, world*1e3, ms(g), ms(c), s.Stages[render.StageModels]*1e3, s.Stages[render.Stage2D]*1e3,
			s.Stages[render.StagePresent]*1e3, unstaged*1e3, outside*1e3, ms(at(p.Yield, i)), ms(at(p.ProbeCost, i)), ms(at(p.YieldGC, i)), at(p.Goroutines, i), ms(at(p.YieldScav, i)), at(p.YieldCleanups, i), at(p.YieldFinalizers, i), at(p.Cleanups, i), w*1e3, p.GC[i], gp*1e3)
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
		tot.yield += at(p.Yield, i).Seconds()
		tot.yieldGC += min(at(p.YieldGC, i).Seconds(), at(p.Yield, i).Seconds())
		tot.yieldScav += min(at(p.YieldScav, i).Seconds(), at(p.Yield, i).Seconds())
		cleanups += at(p.YieldCleanups, i)
		finalizers += at(p.YieldFinalizers, i)
		frameCleanups += at(p.Cleanups, i)
	}
	if n == 0 {
		fmt.Fprintf(out, "slowtics: none over %.0f ms in %d frames\n", slowTicBound*1e3, len(samples))
		return
	}
	pc := func(v float64) float64 { return 100 * v / tot.iv }
	fmt.Fprintf(out, "slowtics: %d over %.0f ms, %.0f ms in all; share of that time: sim %.0f%% world %.0f%% gpu %.0f%% copy %.0f%% models %.0f%% composite %.0f%% present %.0f%% unstaged(audio mix, other) %.0f%% outside(yield, host wait) %.0f%%; of it audio-write %.1f%% gc-pause %.1f%%\n",
		n, slowTicBound*1e3, tot.iv*1e3, pc(tot.sim), pc(tot.world), pc(tot.gpu), pc(tot.copy), pc(tot.models), pc(tot.comp), pc(tot.present), pc(tot.unstaged), pc(tot.outside), pc(tot.write), pc(tot.gc))
	if tot.yield > 0 {
		fmt.Fprintf(out, "yieldshare: of the %.0f ms the slow tics spent in the yield, collector background mark %.0f ms (%.0f%%), other goroutines %.0f ms (%.0f%%)\n",
			tot.yield*1e3, tot.yieldGC*1e3, 100*tot.yieldGC/tot.yield, (tot.yield-tot.yieldGC)*1e3, 100*(tot.yield-tot.yieldGC)/tot.yield)
		// What the other goroutines were: the scavenger's own time is measured; the
		// cleanups and finalizers are counted (a cleanup of a GPU handle id sends a
		// DESTROY into the batch stream, webgpu record.go freeID); what is left is
		// the sweeper and whatever else the runtime runs.
		rest := max(tot.yield-tot.yieldGC-tot.yieldScav, 0)
		fmt.Fprintf(out, "yieldwho: of the other goroutines' %.0f ms: scavenger %.0f ms; %d cleanups and %d finalizers ran in those yields (%d cleanups in the whole frames of the slow tics); sweeper and the rest %.0f ms\n",
			(tot.yield-tot.yieldGC)*1e3, tot.yieldScav*1e3, cleanups, finalizers, frameCleanups, rest*1e3)
	}
}
