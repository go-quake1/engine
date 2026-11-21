// Copyright (c) 2026 the go-quake1/engine authors.
// SPDX-License-Identifier: GPL-2.0-or-later

package main

import (
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/go-quake1/engine/assets"
	"github.com/go-quake1/engine/client"
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
	auto     bool // the default: the GPU when one is granted, else the CPU (no --cpuworld)
	// corruptSkins is the planted defect of the entity check (--corrupt-gpu-skins):
	// the GPU draws the entities with every skin index shifted by 16.
	corruptSkins bool
	// gpuEntities (--gpu-entities) lets the GPU draw the alias entities.
	gpuEntities bool
	// minGPUEntities is runner.SetupOpts.MinGPUEntities (0: its default).
	minGPUEntities int
}

// gameWorld is the runner's WorldDrawer over a [gpuDrawer]: the map is built
// and the atlases uploaded once, when it is opened; a frame is one submit with
// its read-back, and the index rows are copied into the frame buffer.
type gameWorld struct {
	wv *worldview.World
	gd gpuDrawer
	w  int
	// styles is d_lightstylevalue from the server's light styles (nil: the
	// default table of worldview.AnimationAt).
	styles func(style byte) int

	times gpuTimes // per frame, for the --frames phase line

	// last is the frame's world draw, read by the frame probe; slow times the
	// host calls of it (nil: not timed).
	last worldDraw
	slow hostTimer

	// The alias entities on the GPU (runner.EntityDrawer): ld is the drawer's
	// entity layer (nil: none), ents the frame's entities, layer the last frame's
	// entity layer; swap, with the planted defect, maps a model to the copy whose
	// skins are shifted; entCount and entFrames are the entities drawn and the
	// frames they were drawn in, for the check's summary.
	entities            bool // --gpu-entities
	ld                  layerDrawer
	ents                []worldview.AliasEnt
	layer               []byte
	swap                map[*worldview.AliasModel]*worldview.AliasModel
	corrupt             bool
	entCount, entFrames int
}

// idleSetter is a [gpuDrawer] whose wait for the GPU can run a function in
// slices (the batch device's submit_begin / submit_ready polling).
type idleSetter interface{ SetIdle(f func() error) }

// SetIdle makes the drawer's wait for the GPU run f between polls, when the
// drawer can (WinQuake calls S_ExtraUpdate inside rendering: r_main.c:937, 982,
// 991, r_edge.c:728, draw.c:850, 868, gl_rmain.c:961). Another drawer blocks.
func (g *gameWorld) SetIdle(f func() error) {
	if is, ok := g.gd.(idleSetter); ok {
		is.SetIdle(f)
	}
}

// SetIdleAt is SetIdle for the drawer's own CPU work, with the place in the
// frame given to f (a drawer that cannot say keeps SetIdle's function).
func (g *gameWorld) SetIdleAt(f func(site string) error) {
	if is, ok := g.gd.(interface{ SetIdleAt(func(string) error) }); ok {
		is.SetIdleAt(f)
	}
}

// errEntitiesOff is why the GPU draws no alias entity: --gpu-entities=false.
var errEntitiesOff = errors.New("--gpu-entities=false")

// layerDrawer is a [gpuDrawer] that also draws alias entities into a layer of
// their own, in the same submit and read-back (gpuworld.Renderer.DrawReadLayers).
type layerDrawer interface {
	EnableEntities(models []*worldview.AliasModel) error
	DrawLayers(v worldview.View, ents []worldview.AliasEnt) (world, layer []byte, err error)
}

// SetAliasModels implements runner.EntityDrawer. With the planted defect
// (--corrupt-gpu-skins) the GPU gets copies of the models whose skins have every
// palette index shifted by 16 while the CPU keeps the true ones.
func (g *gameWorld) SetAliasModels(models []*worldview.AliasModel) error {
	if !g.entities {
		return errEntitiesOff
	}
	ld, ok := g.gd.(layerDrawer)
	if !ok {
		return fmt.Errorf("this GPU drawer has no entity layer")
	}
	use := models
	if g.corrupt {
		g.swap = map[*worldview.AliasModel]*worldview.AliasModel{}
		use = make([]*worldview.AliasModel, len(models))
		for i, am := range models {
			if am == nil {
				continue
			}
			c := *am
			c.Skins = nil
			for _, s := range am.Skins {
				px := make([]byte, len(s.Pixels))
				for k, b := range s.Pixels {
					px[k] = b + 16
				}
				c.Skins = append(c.Skins, &render.Pic{Width: s.Width, Height: s.Height, Pixels: px})
			}
			g.swap[am] = &c
			use[i] = &c
		}
	}
	if err := ld.EnableEntities(use); err != nil {
		return err
	}
	g.ld = ld
	return nil
}

// SetEntities implements runner.EntityDrawer.
func (g *gameWorld) SetEntities(ents []worldview.AliasEnt) {
	g.ents = ents
	g.entCount += len(ents)
	g.entFrames++
}

// CompositeEntities implements runner.EntityDrawer: the entities of the last
// DrawWorld go over fb (after the CPU drew the sky and water).
func (g *gameWorld) CompositeEntities(fb *render.FrameBuffer) {
	if len(g.layer) == g.w*fb.Height {
		gpuworld.CompositeLayer(fb.Pixels, fb.Pitch, g.layer, g.w, fb.Height)
	}
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

// SetDLights implements runner.DynamicLighter: the lights of the frame go to
// the world, which marks the faces they reach and bakes them into the surfaces.
func (g *gameWorld) SetDLights(lights *[client.MaxDLights]client.DLight, now float32) {
	for i := range lights {
		l := &lights[i]
		g.wv.DLights[i] = render.DLight{Origin: l.Origin, Radius: l.Radius, Die: l.Die, Decay: l.Decay, MinLight: l.MinLight, Key: l.Key, Color: l.Color}
	}
	g.wv.Now = now
}

// SetLightStyles implements runner.LightStyler: the light styles the server
// sent, which the frame's animation uses in place of the default table.
func (g *gameWorld) SetLightStyles(value func(style byte) int) { g.styles = value }

// DrawWorld implements runner.WorldDrawer.
func (g *gameWorld) DrawWorld(fb *render.FrameBuffer, origin, angles [3]float32, fov, t float32) error {
	g.wv.FOV = fov
	// The light styles and '+' texture frames of cl.time, as the CPU world draws
	// them: the renderer re-bakes and uploads the surfaces that moved.
	g.wv.Anim = g.wv.AnimationAt(t)
	if g.styles != nil {
		g.wv.Anim.Style = g.styles
	}
	if g.slow != nil {
		g.slow.ResetHostSlowest()
	}
	dt, _ := g.gd.(drawTimer)
	if dt != nil {
		dt.ResetSlowest()
	}
	t0 := time.Now()
	var frame []byte
	var err error
	view := worldview.View{Origin: origin, Angles: angles}
	if g.ld != nil && len(g.ents) > 0 {
		ents := g.ents
		if g.swap != nil {
			ents = make([]worldview.AliasEnt, len(g.ents))
			for i, e := range g.ents {
				e.Model = g.swap[e.Model]
				ents[i] = e
			}
		}
		frame, g.layer, err = g.ld.DrawLayers(view, ents)
	} else {
		g.layer = nil
		frame, err = g.gd.Draw(view)
	}
	if err != nil {
		return err
	}
	t1 := time.Now()
	if len(frame) != g.w*fb.Height || fb.Width != g.w {
		return fmt.Errorf("the GPU frame has %d bytes for a %dx%d frame buffer", len(frame), fb.Width, fb.Height)
	}
	for y := 0; y < fb.Height; y++ {
		copy(fb.Pixels[y*fb.Pitch:y*fb.Pitch+g.w], frame[y*g.w:(y+1)*g.w])
	}
	cp := time.Since(t1)
	g.times.add(t1.Sub(t0), cp)
	g.last = worldDraw{gpu: t1.Sub(t0), copy: cp}
	if g.slow != nil {
		g.last.slowName, g.last.slowFor = g.slow.HostSlowest()
	}
	// The frame's own submit crosses through the batch sink, which the
	// presenter's device does not see: the longer of the two is the draw's.
	if dt != nil {
		if n, d := dt.Slowest(); n != "" && d > g.last.slowFor {
			g.last.slowName, g.last.slowFor = n, d
		}
	}
	// How the draw's time splits: the wait for the GPU (where the idle function
	// feeds the sound) and the program's own work (where nothing does, but the
	// slices of the faces' walk): the re-bakes and their upload record.
	if wt, ok := g.gd.(waitTimer); ok {
		g.last.wait, g.last.idle = wt.Waited()
	}
	if bt, ok := g.gd.(bakeTimer); ok {
		g.last.bake, g.last.upload = bt.Bake()
	}
	return nil
}

// waitTimer is a [gpuDrawer] that says how long its last draw waited for the GPU
// and how much of it ran the idle function (batchsink.Sink.Waited).
type waitTimer interface {
	Waited() (wait, idle time.Duration)
}

// bakeTimer is a [gpuDrawer] that says what its last draw spent re-baking the
// surfaces the animation moved and recording their upload.
type bakeTimer interface {
	Bake() (bake, upload time.Duration)
}

// drawTimer is a [gpuDrawer] that times its own host calls (batchsink.Sink).
type drawTimer interface {
	ResetSlowest()
	Slowest() (string, time.Duration)
}

// worldDraw is what the last DrawWorld spent: the submit with its read-back,
// the copy into the frame buffer, and the longest host call of the two.
//
// gpu is the whole of the drawer's call; wait the part of it spent waiting for
// the GPU and idle the part of that the sound mix ran; bake and upload the part
// that re-baked surfaces and recorded their upload. gpu - wait is the program's
// own CPU, which only the slices of the faces' walk reach.
type worldDraw struct {
	gpu, copy    time.Duration
	wait, idle   time.Duration
	bake, upload time.Duration
	slowName     string
	slowFor      time.Duration
}

// hostTimer is what the world draw reads the longest host call from
// (wasipresent.Presenter).
type hostTimer interface {
	ResetHostSlowest()
	HostSlowest() (string, time.Duration)
}

// takeWorldDraw returns the world draw of the frame since the last call and
// forgets it, so a frame with no world draw (the menu) reports none.
func (g *gameWorld) takeWorldDraw() (worldDraw, bool) {
	d := g.last
	g.last = worldDraw{}
	return d, d != (worldDraw{})
}

// checkStats accumulates the per-frame comparison of --gpuworld-check.
type checkStats struct {
	frames, pixels, differ, unexplained int
	worst                               float64    // largest share of unexplained pixels in a frame
	ents                                *gameWorld // when set, its entity counts are in the summary

	// The pixels the alias entities change on either side (the GPU's layer, or
	// what the CPU's entities drew over its frame): how many, how many differ
	// between the two frames and how many a one-pixel shift does not explain.
	entPixels, entDiffer, entUnexplained int
}

// addEntities is the runner's EntityCheck: the entity pixels are those the GPU's
// layer holds or the CPU's alias entities changed (cpu against cpuNoEntities);
// over them the CPU frame and the GPU-mode frame are compared.
func (c *checkStats) addEntities(cpu *render.FrameBuffer, cpuNoEntities, gpu []byte) {
	if c.ents == nil || len(c.ents.layer) != cpu.Width*cpu.Height {
		return
	}
	mask := make([]bool, cpu.Width*cpu.Height)
	for y := 0; y < cpu.Height; y++ {
		for x := 0; x < cpu.Width; x++ {
			mask[y*cpu.Width+x] = c.ents.layer[y*cpu.Width+x] != gpuworld.EntityClear || cpu.Pixels[y*cpu.Pitch+x] != cpuNoEntities[y*cpu.Pitch+x]
		}
	}
	n, d, u := gpuworld.CompareMasked(cpu.Pixels, cpu.Pitch, gpu, cpu.Pitch, cpu.Width, cpu.Height, mask)
	c.entPixels += n
	c.entDiffer += d
	c.entUnexplained += u
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
	if c.ents != nil {
		fmt.Fprintf(out, "gpuworld check: %d alias entities drawn by the GPU over %d frames\n", c.ents.entCount, c.ents.entFrames)
	}
	if c.frames == 0 {
		return fmt.Errorf("quake-wctr: --gpuworld-check compared no frame")
	}
	if c.ents != nil && c.entPixels > 0 {
		rate := float64(c.entUnexplained) / float64(c.entPixels)
		fmt.Fprintf(out, "gpuworld check: entities: %d pixels changed by an entity, %d differ, %d unexplained (%.4f, tolerance %.3f)\n",
			c.entPixels, c.entDiffer, c.entUnexplained, rate, gpuworld.AliasTolerance)
		if rate > gpuworld.AliasTolerance {
			return fmt.Errorf("quake-wctr: the entities differ from the CPU's by %.4f unexplained, above the tolerance %.3f", rate, gpuworld.AliasTolerance)
		}
	}
	if c.worst > gpuworld.Tolerance {
		return fmt.Errorf("quake-wctr: a frame differs from the CPU's by %.4f unexplained, above the tolerance %.3f", c.worst, gpuworld.Tolerance)
	}
	fmt.Fprintln(out, "gpuworld check PASS")
	return nil
}
