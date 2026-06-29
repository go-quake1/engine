// Copyright (c) 1996-1997 Id Software, Inc.
// Copyright (c) 2026 the go-quake1/engine authors.
// SPDX-License-Identifier: GPL-2.0-or-later

package render

import (
	"errors"
	"testing"
)

func TestNewFrameBufferHappy(t *testing.T) {
	fb, err := NewFrameBuffer(320, 200)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if fb.Width != 320 || fb.Height != 200 {
		t.Errorf("dims: got (%d,%d) want (320,200)", fb.Width, fb.Height)
	}
	if fb.Pitch != 320 {
		t.Errorf("pitch: got %d want 320", fb.Pitch)
	}
	if len(fb.Pixels) != 320*200 {
		t.Errorf("len(Pixels): got %d want %d", len(fb.Pixels), 320*200)
	}
	for i, b := range fb.Pixels {
		if b != 0 {
			t.Fatalf("Pixels[%d] = %d, want 0 (zeroed)", i, b)
		}
	}
}

func TestNewFrameBufferZeroDim(t *testing.T) {
	cases := []struct{ w, h int }{
		{0, 10},
		{10, 0},
		{0, 0},
	}
	for _, c := range cases {
		_, err := NewFrameBuffer(c.w, c.h)
		if !errors.Is(err, ErrFBZeroDim) {
			t.Errorf("NewFrameBuffer(%d,%d): got %v, want ErrFBZeroDim", c.w, c.h, err)
		}
	}
}

func TestNewFrameBufferNegativeDim(t *testing.T) {
	cases := []struct{ w, h int }{
		{-1, 10},
		{10, -1},
	}
	for _, c := range cases {
		_, err := NewFrameBuffer(c.w, c.h)
		if !errors.Is(err, ErrFBNegativeDim) {
			t.Errorf("NewFrameBuffer(%d,%d): got %v, want ErrFBNegativeDim", c.w, c.h, err)
		}
	}
}

func TestNewFrameBufferAlignedHappy(t *testing.T) {
	fb, err := NewFrameBufferAligned(10, 4, 16)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if fb.Width != 10 || fb.Height != 4 || fb.Pitch != 16 {
		t.Errorf("dims/pitch: got (%d,%d,%d) want (10,4,16)", fb.Width, fb.Height, fb.Pitch)
	}
	if len(fb.Pixels) != 16*4 {
		t.Errorf("len(Pixels): got %d want %d", len(fb.Pixels), 16*4)
	}
}

func TestNewFrameBufferAlignedPitchTooSmall(t *testing.T) {
	_, err := NewFrameBufferAligned(10, 4, 5)
	if !errors.Is(err, ErrFBPitchTooSmall) {
		t.Errorf("got %v, want ErrFBPitchTooSmall", err)
	}
}

func TestNewFrameBufferAlignedZeroDim(t *testing.T) {
	cases := []struct{ w, h, pitch int }{
		{0, 4, 4},
		{10, 0, 16},
	}
	for _, c := range cases {
		_, err := NewFrameBufferAligned(c.w, c.h, c.pitch)
		if !errors.Is(err, ErrFBZeroDim) {
			t.Errorf("NewFrameBufferAligned(%d,%d,%d): got %v, want ErrFBZeroDim", c.w, c.h, c.pitch, err)
		}
	}
}

func TestNewFrameBufferAlignedNegativeDim(t *testing.T) {
	cases := []struct{ w, h, pitch int }{
		{-1, 4, 4},
		{10, -1, 16},
	}
	for _, c := range cases {
		_, err := NewFrameBufferAligned(c.w, c.h, c.pitch)
		if !errors.Is(err, ErrFBNegativeDim) {
			t.Errorf("NewFrameBufferAligned(%d,%d,%d): got %v, want ErrFBNegativeDim", c.w, c.h, c.pitch, err)
		}
	}
}

func TestClearTight(t *testing.T) {
	fb, _ := NewFrameBuffer(8, 3)
	fb.Clear(0x42)
	for i, b := range fb.Pixels {
		if b != 0x42 {
			t.Fatalf("Pixels[%d] = %#x, want 0x42", i, b)
		}
	}
}

func TestClearAligned(t *testing.T) {
	fb, _ := NewFrameBufferAligned(10, 4, 16)
	fb.Clear(0xAB)
	for i, b := range fb.Pixels {
		if b != 0xAB {
			t.Fatalf("Pixels[%d] = %#x, want 0xAB", i, b)
		}
	}
}

func TestSetPixelHappy(t *testing.T) {
	fb, _ := NewFrameBuffer(4, 3)
	if err := fb.SetPixel(2, 1, 0x77); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := fb.Pixels[1*4+2]; got != 0x77 {
		t.Errorf("Pixels[6] = %#x, want 0x77", got)
	}
}

func TestSetPixelOutOfRange(t *testing.T) {
	fb, _ := NewFrameBuffer(4, 3)
	cases := []struct {
		name string
		x, y int
	}{
		{"x<0", -1, 0},
		{"x>=Width", 4, 0},
		{"y<0", 0, -1},
		{"y>=Height", 0, 3},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if err := fb.SetPixel(c.x, c.y, 1); !errors.Is(err, ErrFBPointOutOfRange) {
				t.Errorf("got %v, want ErrFBPointOutOfRange", err)
			}
		})
	}
}

func TestGetPixelHappy(t *testing.T) {
	fb, _ := NewFrameBuffer(4, 3)
	fb.Pixels[1*4+2] = 0x55
	got, err := fb.GetPixel(2, 1)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != 0x55 {
		t.Errorf("got %#x, want 0x55", got)
	}
}

func TestGetPixelOutOfRange(t *testing.T) {
	fb, _ := NewFrameBuffer(4, 3)
	cases := []struct {
		name string
		x, y int
	}{
		{"x<0", -1, 0},
		{"x>=Width", 4, 0},
		{"y<0", 0, -1},
		{"y>=Height", 0, 3},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := fb.GetPixel(c.x, c.y)
			if !errors.Is(err, ErrFBPointOutOfRange) {
				t.Errorf("got %v, want ErrFBPointOutOfRange", err)
			}
		})
	}
}

func TestSetPixelPitchLayout(t *testing.T) {
	// Pitch > Width: verify the write lands at y*Pitch + x in the
	// backing slice, not y*Width + x.
	fb, _ := NewFrameBufferAligned(4, 3, 8)
	if err := fb.SetPixel(2, 1, 0x99); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := fb.Pixels[1*8+2]; got != 0x99 {
		t.Errorf("Pixels[10] = %#x, want 0x99", got)
	}
	// The y*Width+x cell must remain untouched (0).
	if got := fb.Pixels[1*4+2]; got != 0 {
		t.Errorf("Pixels[6] = %#x, want 0 (Pitch-not-Width indexing)", got)
	}
}

// buildExpandFixture returns a 2x2 framebuffer + a palette that maps
// the 4 indices to distinguishable RGB triples.
func buildExpandFixture() (*FrameBuffer, *Palette) {
	fb, _ := NewFrameBuffer(2, 2)
	fb.Pixels[0] = 0 // (0,0)
	fb.Pixels[1] = 1 // (1,0)
	fb.Pixels[2] = 2 // (0,1)
	fb.Pixels[3] = 3 // (1,1)
	var pal Palette
	pal[0] = [3]byte{0x10, 0x20, 0x30}
	pal[1] = [3]byte{0x40, 0x50, 0x60}
	pal[2] = [3]byte{0x70, 0x80, 0x90}
	pal[3] = [3]byte{0xA0, 0xB0, 0xC0}
	return fb, &pal
}

func TestExpandHappy(t *testing.T) {
	fb, pal := buildExpandFixture()
	dst := make([]byte, 2*2*4)
	if err := fb.Expand(dst, pal); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := []byte{
		0x10, 0x20, 0x30, 0xFF,
		0x40, 0x50, 0x60, 0xFF,
		0x70, 0x80, 0x90, 0xFF,
		0xA0, 0xB0, 0xC0, 0xFF,
	}
	for i, w := range want {
		if dst[i] != w {
			t.Errorf("dst[%d] = %#x, want %#x", i, dst[i], w)
		}
	}
}

func TestExpandPitchLayout(t *testing.T) {
	// A Pitch > Width source must still produce a tightly packed
	// RGBA destination (the row stride in the source doesn't bleed
	// into the output).
	fb, _ := NewFrameBufferAligned(2, 2, 4)
	fb.Pixels[0] = 0
	fb.Pixels[1] = 1
	// fb.Pixels[2..3] are padding (Pitch=4, Width=2).
	fb.Pixels[4] = 2
	fb.Pixels[5] = 3
	var pal Palette
	pal[0] = [3]byte{0x11, 0x22, 0x33}
	pal[1] = [3]byte{0x44, 0x55, 0x66}
	pal[2] = [3]byte{0x77, 0x88, 0x99}
	pal[3] = [3]byte{0xAA, 0xBB, 0xCC}
	dst := make([]byte, 2*2*4)
	if err := fb.Expand(dst, &pal); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := []byte{
		0x11, 0x22, 0x33, 0xFF,
		0x44, 0x55, 0x66, 0xFF,
		0x77, 0x88, 0x99, 0xFF,
		0xAA, 0xBB, 0xCC, 0xFF,
	}
	for i, w := range want {
		if dst[i] != w {
			t.Errorf("dst[%d] = %#x, want %#x", i, dst[i], w)
		}
	}
}

func TestExpandDstTooSmall(t *testing.T) {
	fb, pal := buildExpandFixture()
	dst := make([]byte, 2*2*4-1)
	if err := fb.Expand(dst, pal); !errors.Is(err, ErrFBDstTooSmall) {
		t.Errorf("got %v, want ErrFBDstTooSmall", err)
	}
}

func TestExpandTo32Happy(t *testing.T) {
	fb, pal := buildExpandFixture()
	dst := make([]uint32, 2*2)
	if err := fb.ExpandTo32(dst, pal); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := []uint32{
		(0xFF << 24) | (0x10 << 16) | (0x20 << 8) | 0x30,
		(0xFF << 24) | (0x40 << 16) | (0x50 << 8) | 0x60,
		(0xFF << 24) | (0x70 << 16) | (0x80 << 8) | 0x90,
		(0xFF << 24) | (0xA0 << 16) | (0xB0 << 8) | 0xC0,
	}
	for i, w := range want {
		if dst[i] != w {
			t.Errorf("dst[%d] = %#x, want %#x", i, dst[i], w)
		}
	}
	// Sanity: A is in the high byte.
	if (dst[0] >> 24) != 0xFF {
		t.Errorf("alpha not in high byte: dst[0] = %#x", dst[0])
	}
}

func TestExpandTo32DstTooSmall(t *testing.T) {
	fb, pal := buildExpandFixture()
	dst := make([]uint32, 2*2-1)
	if err := fb.ExpandTo32(dst, pal); !errors.Is(err, ErrFBDstTooSmall) {
		t.Errorf("got %v, want ErrFBDstTooSmall", err)
	}
}

// TestFrameBufferHUDScaleDefault: a freshly-constructed FrameBuffer
// has HUDScale == 0 which EffectiveScale folds to 1 + VWidth/VHeight
// equal Width/Height verbatim (vanilla 1:1 behaviour).
func TestFrameBufferHUDScaleDefault(t *testing.T) {
	fb, err := NewFrameBuffer(320, 240)
	if err != nil {
		t.Fatalf("NewFrameBuffer: %v", err)
	}
	if fb.HUDScale != 0 {
		t.Errorf("default HUDScale: got %d, want 0", fb.HUDScale)
	}
	if fb.EffectiveScale() != 1 {
		t.Errorf("EffectiveScale: got %d, want 1", fb.EffectiveScale())
	}
	if fb.VWidth() != 320 || fb.VHeight() != 240 {
		t.Errorf("VWidth/VHeight: got (%d,%d), want (320,240)", fb.VWidth(), fb.VHeight())
	}
}

// TestFrameBufferHUDScaleNegativeFoldsToOne: a hostile / mis-wired
// HUDScale <= 0 (e.g. an embedder that subtracts and underflows)
// must NOT trigger a divide-by-zero in VWidth/VHeight.
func TestFrameBufferHUDScaleNegativeFoldsToOne(t *testing.T) {
	fb, err := NewFrameBuffer(640, 480)
	if err != nil {
		t.Fatalf("NewFrameBuffer: %v", err)
	}
	for _, s := range []int{-1, -100, 0} {
		fb.HUDScale = s
		if fb.EffectiveScale() != 1 {
			t.Errorf("HUDScale=%d: EffectiveScale = %d, want 1", s, fb.EffectiveScale())
		}
		if fb.VWidth() != 640 || fb.VHeight() != 480 {
			t.Errorf("HUDScale=%d: V dims = (%d,%d), want (640,480)",
				s, fb.VWidth(), fb.VHeight())
		}
	}
}

// TestFrameBufferHUDScaleTwo: HUDScale == 2 halves the virtual dims
// (900x700 -> 450x350) so the 2D layer's coordinate space stays
// aligned with the upscaled blits.
func TestFrameBufferHUDScaleTwo(t *testing.T) {
	fb, err := NewFrameBuffer(900, 700)
	if err != nil {
		t.Fatalf("NewFrameBuffer: %v", err)
	}
	fb.HUDScale = 2
	if fb.EffectiveScale() != 2 {
		t.Errorf("EffectiveScale: got %d, want 2", fb.EffectiveScale())
	}
	if fb.VWidth() != 450 {
		t.Errorf("VWidth: got %d, want 450", fb.VWidth())
	}
	if fb.VHeight() != 350 {
		t.Errorf("VHeight: got %d, want 350", fb.VHeight())
	}
}
