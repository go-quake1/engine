// Copyright (c) 2026 the go-quake1/engine authors.
// SPDX-License-Identifier: GPL-2.0-or-later

package main

import (
	"bytes"
	"testing"
	"time"

	"github.com/go-quake1/engine/backend"
	"github.com/go-quake1/engine/render"
	"github.com/go-quake1/engine/runloop"
	"github.com/go-quake1/engine/sound"
	"github.com/go-quake1/engine/worldview"
)

// siteBackend is a queued audio output that is always empty, so that every
// refill queues a mix, and counts them by the site (Runner.MixSite) that made
// them.
type siteBackend struct {
	backend.Backend
	r     *runloop.Runner
	sites map[string]int
}

func (b *siteBackend) QueuedFrames() int { return 0 }
func (b *siteBackend) QueueAudio(s []sound.StereoSample) error {
	b.sites[b.r.MixSite]++
	return nil
}

// WinQuake calls S_ExtraUpdate inside rendering, where the frame is CPU-heavy
// (r_main.c:937 after the world, 982 after the entities, 991 after the
// particles): a frame of the game must refill the sound at those places, not
// only after the draw. Before, a frame's refills were its tic-end, "world" and
// "compose" ones.
func TestRenderingRefillsTheSoundAtWinQuakesSites(t *testing.T) {
	p := &fakePresenter{}
	closes := 0
	withSurface(t, p, false, &closes)
	oldTR := testRunner
	sb := &siteBackend{sites: map[string]int{}}
	testRunner = func(r *runloop.Runner) { sb.r = r }
	oldTM := testMixer
	testMixer = func(be backend.Backend) backend.Backend { sb.Backend = be; return sb }
	t.Cleanup(func() { testRunner, testMixer = oldTR, oldTM })
	var out, errOut bytes.Buffer
	if err := runGameWith(&out, &errOut, realFS(t), "e1m1", 64, 40, 4, gameOpts{}); err != nil {
		t.Fatalf("%v\n%s", err, errOut.String())
	}
	for _, site := range []string{"tic-end", "world", "world-faces", "entities", "particles", "compose"} {
		if sb.sites[site] == 0 {
			t.Errorf("no refill at %q over 4 frames: %v", site, sb.sites)
		}
	}
}

// waitedGPU is a drawer that says how its draw split (waitTimer, bakeTimer).
type waitedGPU struct{ w, h int }

func (g waitedGPU) Draw(worldview.View) ([]byte, error) { return make([]byte, g.w*g.h), nil }
func (g waitedGPU) Close()                              {}
func (g waitedGPU) Waited() (time.Duration, time.Duration) {
	return 60 * time.Millisecond, 20 * time.Millisecond
}
func (g waitedGPU) Bake() (time.Duration, time.Duration) {
	return 9 * time.Millisecond, 3 * time.Millisecond
}

// The world draw of a frame carries how it split: the wait for the GPU, the part
// of it that ran the sound mix, the re-bakes and their upload.
func TestWorldDrawReadsTheWaitAndTheBake(t *testing.T) {
	const w, h = 6, 3
	fb, err := render.NewFrameBufferAligned(w, h, w)
	if err != nil {
		t.Fatal(err)
	}
	g := &gameWorld{wv: &worldview.World{}, w: w, gd: waitedGPU{w, h}}
	if err := g.DrawWorld(fb, [3]float32{}, [3]float32{}, 90, 0); err != nil {
		t.Fatal(err)
	}
	d, ok := g.takeWorldDraw()
	if !ok || d.wait != 60*time.Millisecond || d.idle != 20*time.Millisecond || d.bake != 9*time.Millisecond || d.upload != 3*time.Millisecond {
		t.Errorf("world draw %+v", d)
	}
}

// slowGPU is a gpuDrawer whose frame takes slices polls of the host to finish:
// each poll runs the idle function it was given, as batchsink.Sink does while
// submit_ready says the frame is still running.
type slowGPU struct {
	w, h   int
	slices int
	idle   *func() error
	runs   *int
	errs   *int
	closed *int
}

func (s slowGPU) Draw(worldview.View) ([]byte, error) {
	for range s.slices {
		if f := *s.idle; f != nil {
			*s.runs++
			if err := f(); err != nil {
				*s.errs++
			}
		}
	}
	return bytes.Repeat([]byte{0x10}, s.w*s.h), nil
}
func (s slowGPU) Close() { *s.closed++ }

// idleGPU is slowGPU that also takes the idle function (Sink.SetIdle).
type idleGPU struct{ slowGPU }

func (g idleGPU) SetIdle(f func() error) { *g.idle = f }

// WinQuake calls S_ExtraUpdate inside rendering (r_main.c:937, 982, 991,
// r_edge.c:728, draw.c:850, 868, gl_rmain.c:961), so a slow world draw must not
// leave the sound mix unfed: the world drawer is given the runner's
// S_ExtraUpdate to run between the polls of its GPU wait. Before this, nothing
// was given, and the wait blocked.
func TestWorldWaitRunsExtraUpdate(t *testing.T) {
	p := &fakePresenter{}
	closes := 0
	withSurface(t, p, false, &closes)
	var idle func() error
	runs, errs, gclose := 0, 0, 0
	old := openGPUWorld
	openGPUWorld = func(_ *worldview.World, _ *render.ColorMap, w, h int) (gpuDrawer, error) {
		return idleGPU{slowGPU{w: w, h: h, slices: 4, idle: &idle, runs: &runs, errs: &errs, closed: &gclose}}, nil
	}
	t.Cleanup(func() { openGPUWorld = old })
	oldTR := testRunner
	var runner *runloop.Runner
	testRunner = func(r *runloop.Runner) { runner = r }
	t.Cleanup(func() { testRunner = oldTR })
	var out, errOut bytes.Buffer
	if err := runGameWith(&out, &errOut, realFS(t), "e1m1", 64, 40, 3, gameOpts{gpuWorld: true}); err != nil {
		t.Fatalf("%v\n%s", err, errOut.String())
	}
	if idle == nil {
		t.Fatal("the world drawer was given no idle function: its GPU wait blocks and the sound mix is not refilled during it")
	}
	if runs < 4*3 || errs != 0 {
		t.Errorf("idle ran %d times (%d errors) over 3 slow frames of 4 polls, want at least 12 and none failing", runs, errs)
	}
	if runner == nil {
		t.Fatal("no runner")
	}
}

// timedGPU is a drawer that times its host calls, as batchsink.Sink does; the
// batch device's submit is not a call of the presenter's device, so the probe's
// world-slowest used to be empty.
type timedGPU struct {
	w, h   int
	name   string
	d      time.Duration
	resets *int
	cur    *string
	curD   *time.Duration
}

func (g timedGPU) Draw(worldview.View) ([]byte, error) {
	*g.cur, *g.curD = g.name, g.d
	return make([]byte, g.w*g.h), nil
}
func (g timedGPU) Close()                           {}
func (g timedGPU) ResetSlowest()                    { *g.resets++; *g.cur, *g.curD = "", 0 }
func (g timedGPU) Slowest() (string, time.Duration) { return *g.cur, *g.curD }

// presenterTimer is the presenter's device clock.
type presenterTimer struct {
	name string
	d    time.Duration
}

func (p presenterTimer) ResetHostSlowest()                    {}
func (p presenterTimer) HostSlowest() (string, time.Duration) { return p.name, p.d }

// The longest host call of the world draw is the longer of the drawer's own
// (the sink's submit) and the presenter device's, and it is reset per frame.
func TestWorldSlowestNamesTheSinksSubmit(t *testing.T) {
	const w, h = 6, 3
	fb, err := render.NewFrameBufferAligned(w, h, w)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		name     string
		pres     hostTimer
		want     string
		wantFor  time.Duration
		sinkName string
		sinkFor  time.Duration
	}{
		{"the sink's submit is the longest", presenterTimer{"[method]queue.write-buffer", 3 * time.Millisecond}, "submit_end", 70 * time.Millisecond, "submit_end", 70 * time.Millisecond},
		{"the presenter's call is longer", presenterTimer{"[method]context.present", 90 * time.Millisecond}, "[method]context.present", 90 * time.Millisecond, "submit_end", 70 * time.Millisecond},
		{"no presenter timer", nil, "submit_begin", 5 * time.Millisecond, "submit_begin", 5 * time.Millisecond},
		{"nothing timed", presenterTimer{}, "", 0, "", 0},
	} {
		resets := 0
		var cur string
		var curD time.Duration
		g := &gameWorld{wv: &worldview.World{}, w: w, gd: timedGPU{w: w, h: h, name: c.sinkName, d: c.sinkFor, resets: &resets, cur: &cur, curD: &curD}}
		if c.pres != nil {
			g.slow = c.pres
		}
		if err := g.DrawWorld(fb, [3]float32{}, [3]float32{}, 90, 0); err != nil {
			t.Fatal(err)
		}
		d, ok := g.takeWorldDraw()
		if !ok || d.slowName != c.want || d.slowFor != c.wantFor || resets != 1 {
			t.Errorf("%s: slowest %q %v (reset %d times), want %q %v once", c.name, d.slowName, d.slowFor, resets, c.want, c.wantFor)
		}
	}
}

// The probe's per-frame instrumentation of the world draw (the three clock
// reads, the host-call reset and read, the worldDraw record and its take)
// adds no allocation to the draw: the text (worldDetail) is built only for a frame the probe
// reports. Measured as an exact allocation count, which the load does not move.
func TestWorldDrawInstrumentationAddsNoAllocation(t *testing.T) {
	const w, h = 64, 40
	fb, err := render.NewFrameBufferAligned(w, h, w)
	if err != nil {
		t.Fatal(err)
	}
	frame := make([]byte, w*h)
	var cur string
	var curD time.Duration
	resets := 0
	timed := framedTimedGPU{timedGPU{w: w, h: h, name: "submit", d: time.Millisecond, resets: &resets, cur: &cur, curD: &curD}, frame}
	count := func(g *gameWorld) float64 {
		return testing.AllocsPerRun(200, func() {
			if err := g.DrawWorld(fb, [3]float32{}, [3]float32{}, 90, 0); err != nil {
				t.Fatal(err)
			}
			g.takeWorldDraw()
		})
	}
	// The control is the same draw with no host-call timing at all: whatever the
	// draw itself allocates (the animation state) is not the instrumentation's.
	plain := count(&gameWorld{wv: &worldview.World{}, w: w, gd: countingFrame{frame}})
	probed := count(&gameWorld{wv: &worldview.World{}, w: w, slow: presenterTimer{"x", time.Millisecond}, gd: timed})
	if probed != plain {
		t.Errorf("a world draw with the probe's timing allocates %v times, the same draw without it %v", probed, plain)
	}
}

// countingFrame is a drawer with no timing that returns a frame it holds.
type countingFrame struct{ frame []byte }

func (c countingFrame) Draw(worldview.View) ([]byte, error) { return c.frame, nil }
func (c countingFrame) Close()                              {}

// framedTimedGPU is a timedGPU that returns a frame it already holds.
type framedTimedGPU struct {
	timedGPU
	frame []byte
}

func (g framedTimedGPU) Draw(worldview.View) ([]byte, error) {
	*g.cur, *g.curD = g.name, g.d
	return g.frame, nil
}

// A drawer that cannot run an idle function (the CPU path, the plain device)
// still draws: SetIdle is optional.
func TestWorldWaitWithoutAnIdleDrawer(t *testing.T) {
	p := &fakePresenter{}
	closes := 0
	withSurface(t, p, false, &closes)
	frames, gclose := 0, 0
	old := openGPUWorld
	openGPUWorld = func(_ *worldview.World, _ *render.ColorMap, w, h int) (gpuDrawer, error) {
		return countingGPU{frames: &frames, w: w, h: h, closed: &gclose}, nil
	}
	t.Cleanup(func() { openGPUWorld = old })
	var out, errOut bytes.Buffer
	if err := runGameWith(&out, &errOut, realFS(t), "e1m1", 64, 40, 3, gameOpts{gpuWorld: true}); err != nil || frames < 3 {
		t.Fatalf("err %v, %d GPU frames\n%s", err, frames, errOut.String())
	}
}
