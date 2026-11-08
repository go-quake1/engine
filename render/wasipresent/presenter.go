// Copyright (c) 2026 the go-quake1/engine authors.
// SPDX-License-Identifier: GPL-2.0-or-later

package wasipresent

import (
	"errors"
	"fmt"

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
	return p.drv.Draw()
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
