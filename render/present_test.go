// Copyright (c) 2026 the go-quake1/engine authors.
// SPDX-License-Identifier: GPL-2.0-or-later

package render

import (
	"errors"
	"testing"
)

type fakePresenter struct {
	calls       int
	w, h, pitch int
	pixels      []byte
	pal         *Palette
	err         error
}

func (f *fakePresenter) Present(p []byte, w, h, pitch int, pal *Palette) error {
	f.calls++
	f.pixels, f.w, f.h, f.pitch, f.pal = p, w, h, pitch, pal
	return f.err
}

func TestPresentRenderer(t *testing.T) {
	if _, err := NewPresentRenderer(nil, nil); !errors.Is(err, ErrNilPresenter) {
		t.Fatalf("nil presenter: %v", err)
	}
	fp := &fakePresenter{}
	scene := func(*FrameBuffer, [3]float32, [3]float32) error { return nil }
	r, err := NewPresentRenderer(scene, fp)
	if err != nil {
		t.Fatal(err)
	}
	if r.Kind() != KindWebGPUPresent || !r.HasScene() {
		t.Fatalf("kind %q scene %v", r.Kind(), r.HasScene())
	}
	fb, _ := NewFrameBuffer(4, 3)
	fb.Pixels[5] = 9
	var pal Palette
	dst := []byte{1, 2, 3}
	if err := r.Resolve(fb, dst, &pal); err != nil {
		t.Fatal(err)
	}
	if fp.calls != 1 || fp.w != 4 || fp.h != 3 || fp.pitch != fb.Pitch || fp.pal != &pal || fp.pixels[5] != 9 {
		t.Fatalf("presenter saw %+v", fp)
	}
	if dst[0] != 1 || dst[1] != 2 || dst[2] != 3 {
		t.Fatal("Resolve wrote the RGBA buffer")
	}
	if err := r.Resolve(fb, dst, nil); !errors.Is(err, ErrFBPaletteShape) || fp.calls != 1 {
		t.Fatalf("nil palette: %v (calls %d)", err, fp.calls)
	}
	boom := errors.New("device lost")
	fp.err = boom
	if err := r.Resolve(fb, dst, &pal); !errors.Is(err, boom) {
		t.Fatalf("presenter error: %v", err)
	}
}
