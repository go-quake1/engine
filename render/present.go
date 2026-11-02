// Copyright (c) 2026 the go-quake1/engine authors.
// SPDX-License-Identifier: GPL-2.0-or-later

package render

import "errors"

// KindWebGPUPresent is the software rasterizer with a GPU presenter: the
// frame is still drawn into the 8-bit [FrameBuffer] on the CPU, but the
// palette lookup (index to RGBA) and the scaling to the display happen
// on the GPU, so the CPU never expands the frame to RGBA. It is the
// first step towards [KindGPU]: only the last stage of the frame moved.
const KindWebGPUPresent Kind = "webgpu-present"

// ErrNilPresenter is returned by [NewPresentRenderer] for a nil presenter.
var ErrNilPresenter = errors.New("render: nil presenter")

// Presenter shows one finished 8-bit frame. The implementation owns the
// palette lookup and the scaling (see package render/webgpupresent).
type Presenter interface {
	// Present uploads the width x height index frame (pitch bytes per
	// row, as [FrameBuffer]) and the palette, and puts the result on
	// the display.
	Present(pixels []byte, width, height, pitch int, pal *Palette) error
}

// PresentRenderer is the software [Renderer] whose Resolve hands the
// frame to a [Presenter] instead of expanding it to RGBA. The RGBA
// buffer the runner passes is left untouched, and the backend's
// PresentFrame must ignore it (the backend of a GPU-presented page has
// no pixels to show).
type PresentRenderer struct {
	Software
	// P shows the frame.
	P Presenter
}

// NewPresentRenderer returns the presenting renderer over the scene
// closure (nil for a 2D-only frame).
func NewPresentRenderer(scene func(fb *FrameBuffer, viewOrigin, viewAngles [3]float32) error, p Presenter) (*PresentRenderer, error) {
	if p == nil {
		return nil, ErrNilPresenter
	}
	return &PresentRenderer{Software: Software{Scene: scene}, P: p}, nil
}

// Kind returns [KindWebGPUPresent].
func (r *PresentRenderer) Kind() Kind { return KindWebGPUPresent }

// Resolve presents fb through the presenter; dst is not written.
func (r *PresentRenderer) Resolve(fb *FrameBuffer, _ []byte, pal *Palette) error {
	if pal == nil {
		return ErrFBPaletteShape
	}
	return r.P.Present(fb.Pixels, fb.Width, fb.Height, fb.Pitch, pal)
}
