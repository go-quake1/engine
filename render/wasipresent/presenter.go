// Copyright (c) 2026 the go-quake1/engine authors.
// SPDX-License-Identifier: GPL-2.0-or-later

package wasipresent

import (
	"errors"
	"fmt"
	"time"

	"github.com/go-quake1/engine/render"
)

// ErrBadFrame is returned by Present for a non-positive size, a pitch
// shorter than the width, a frame shorter than its dimensions say, or a nil
// palette.
var ErrBadFrame = errors.New("wasipresent: bad frame")

// Driver is the GPU side of a presenter: a pipeline over the shader, the
// two storage buffers and one draw-and-present. driver_wasip1.go implements
// it over wasigpu; a test fakes it.
type Driver interface {
	// Pipeline compiles the shader, replacing (and dropping) any earlier one.
	Pipeline(wgsl string) error
	// Buffers allocates the index buffer of indexBytes bytes and the
	// 1 KiB palette buffer, replacing (and dropping) any earlier ones.
	Buffers(indexBytes int) error
	// WriteIndices and WritePalette fill the buffers from offset 0.
	WriteIndices(data []byte) error
	WritePalette(data []byte) error
	// Draw clears the surface, draws the three vertices and presents.
	Draw() error
	// Close drops everything the driver holds.
	Close()
}

// ReadyDriver is a Driver that can say whether Draw would hand its frame over
// without waiting for the page's acknowledgement of an earlier one
// (go-wasmcontainers/gpu's context.ready-to-present).
type ReadyDriver interface {
	Driver
	ReadyToPresent() (bool, error)
}

// Slice is how long a presenter sleeps between two polls of ReadyToPresent, and
// MaxWait how long it waits in all before drawing anyway (the host gives up on
// the page's acknowledgement after a second itself, webgpu.AckWait).
const (
	Slice   = 2 * time.Millisecond
	MaxWait = time.Second
)

// Presenter is a [render.Presenter] over a [Driver]. It rebuilds the
// pipeline and the buffers when the frame geometry changes, writes the
// palette only when it changed, and otherwise costs one buffer write and
// one draw per frame.
type Presenter struct {
	drv        Driver
	outW, outH int

	width, height, pitch int
	ready                bool

	packed []byte
	pal    [256]uint32
	palSet bool

	// idle is what the game does while the page has not acknowledged a frame:
	// WinQuake's S_ExtraUpdate (SetIdle). sleep is time.Sleep, replaced by a test.
	idle  func() error
	sleep func(time.Duration)
	now   func() time.Time // time.Now, replaced by a test

	last PresentStats
}

// PresentStats is where the time of the last Present went: Wait in the slices
// that wait for the page (polls, idle work and sleeps), Draw in the driver's
// Draw (the host's pacing, the GPU commands, the hand-over to the page), and
// SleepOver the most any one sleep of the wait overshot its slice (a timer that
// the worker's event loop served late).
type PresentStats struct {
	Wait, Draw, SleepOver time.Duration
	Slices                int
	// Slowest is the longest host call of the Draw (its import name) and how long
	// it took, when the driver can say (SlowDriver).
	Slowest    string
	SlowestFor time.Duration
}

// SlowDriver is a Driver that times its host calls: ResetSlowest starts a
// reading, SlowestCall returns the longest call since it.
type SlowDriver interface {
	Driver
	ResetSlowest()
	SlowestCall() (string, time.Duration)
}

// LastPresent returns the stats of the last Present.
func (p *Presenter) LastPresent() PresentStats { return p.last }

// SetIdle sets the work done while a frame waits for the page: Present polls
// the driver's ReadyToPresent in slices and calls f between them, so a long
// wait for the page's acknowledgement never leaves the sound mix unfilled
// (S_ExtraUpdate, snd_dma.c lines 844-854). A driver that cannot say whether it
// would wait (not a ReadyDriver) never calls f.
func (p *Presenter) SetIdle(f func() error) { p.idle = f }

// waitReady is the wait of Present for the page, in slices.
func (p *Presenter) waitReady() error {
	rd, ok := p.drv.(ReadyDriver)
	if !ok || p.idle == nil {
		return nil
	}
	sleep, now := p.sleep, p.now
	if sleep == nil {
		sleep = time.Sleep
	}
	if now == nil {
		now = time.Now
	}
	t0 := now()
	defer func() { p.last.Wait = now().Sub(t0) }()
	for waited := time.Duration(0); waited < MaxWait; waited += Slice {
		ready, err := rd.ReadyToPresent()
		if err != nil || ready {
			return err
		}
		if err := p.idle(); err != nil {
			return err
		}
		s0 := now()
		sleep(Slice)
		p.last.Slices++
		if over := now().Sub(s0) - Slice; over > p.last.SleepOver {
			p.last.SleepOver = over
		}
	}
	return nil
}

var _ render.Presenter = (*Presenter)(nil)

// New returns a presenter for a surface of outW x outH pixels.
func New(drv Driver, outW, outH int) (*Presenter, error) {
	if drv == nil || outW < 1 || outH < 1 {
		return nil, fmt.Errorf("wasipresent: need a driver and a positive surface size, got %dx%d", outW, outH)
	}
	return &Presenter{drv: drv, outW: outW, outH: outH}, nil
}

// Present shows the width x height index frame (pitch bytes per row)
// through pal.
func (p *Presenter) Present(pixels []byte, width, height, pitch int, pal *render.Palette) error {
	if width < 1 || height < 1 || pitch < width || pal == nil || len(pixels) < pitch*(height-1)+width {
		return ErrBadFrame
	}
	if !p.ready || width != p.width || height != p.height || pitch != p.pitch {
		if err := p.drv.Pipeline(Shader(width, height, pitch, p.outW, p.outH)); err != nil {
			return err
		}
		if err := p.drv.Buffers(IndexWords(pitch, height) * 4); err != nil {
			return err
		}
		p.width, p.height, p.pitch, p.ready = width, height, pitch, true
		p.palSet = false // the buffers are new
	}
	if words := PaletteWords(pal); !p.palSet || [256]uint32(words) != p.pal {
		if err := p.drv.WritePalette(wordBytes(words)); err != nil {
			return err
		}
		p.pal, p.palSet = [256]uint32(words), true
	}
	p.packed = PackIndices(p.packed, pixels, pitch, height)
	if err := p.drv.WriteIndices(p.packed); err != nil {
		return err
	}
	p.last = PresentStats{}
	if err := p.waitReady(); err != nil {
		return err
	}
	now := p.now
	if now == nil {
		now = time.Now
	}
	sd, timed := p.drv.(SlowDriver)
	if timed {
		sd.ResetSlowest()
	}
	t0 := now()
	err := p.drv.Draw()
	p.last.Draw = now().Sub(t0)
	if timed {
		p.last.Slowest, p.last.SlowestFor = sd.SlowestCall()
	}
	return err
}

// ResetHostSlowest starts a reading of the longest host call, for a caller that
// times a span of the frame the Draw does not cover (the world draw). A driver
// that cannot time its calls ignores it.
func (p *Presenter) ResetHostSlowest() {
	if sd, ok := p.drv.(SlowDriver); ok {
		sd.ResetSlowest()
	}
}

// HostSlowest is the longest host call since ResetHostSlowest (empty name: the
// driver cannot say).
func (p *Presenter) HostSlowest() (string, time.Duration) {
	if sd, ok := p.drv.(SlowDriver); ok {
		return sd.SlowestCall()
	}
	return "", 0
}

// Close drops the driver's GPU objects.
func (p *Presenter) Close() { p.drv.Close() }

// wordBytes lays words out little endian.
func wordBytes(words []uint32) []byte {
	b := make([]byte, 4*len(words))
	for i, w := range words {
		b[4*i], b[4*i+1], b[4*i+2], b[4*i+3] = byte(w), byte(w>>8), byte(w>>16), byte(w>>24)
	}
	return b
}
