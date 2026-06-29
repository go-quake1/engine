// Copyright (c) 1996-1997 Id Software, Inc.
// Copyright (c) 2026 the go-quake1/engine authors.
// SPDX-License-Identifier: GPL-2.0-or-later

package render

import (
	"errors"
	"testing"
)

// newTestFB returns a tight-packed (Pitch == Width) framebuffer
// pre-cleared to `bg`. Helper -- production code uses NewFrameBuffer.
func newTestFB(t *testing.T, w, h int, bg byte) *FrameBuffer {
	t.Helper()
	fb, err := NewFrameBuffer(w, h)
	if err != nil {
		t.Fatalf("NewFrameBuffer(%d,%d): %v", w, h, err)
	}
	fb.Clear(bg)
	return fb
}

// newTestFBAligned returns a Pitch > Width framebuffer pre-cleared
// to `bg`. Used to verify the per-scanline blit does not touch the
// trailing pitch bytes.
func newTestFBAligned(t *testing.T, w, h, pitch int, bg byte) *FrameBuffer {
	t.Helper()
	fb, err := NewFrameBufferAligned(w, h, pitch)
	if err != nil {
		t.Fatalf("NewFrameBufferAligned(%d,%d,%d): %v", w, h, pitch, err)
	}
	fb.Clear(bg)
	return fb
}

// makePic builds a w*h Pic whose pixel (x,y) equals `byte(y*w+x+base)`
// modulo 256. Distinct values per cell make blit checks unambiguous.
func makePic(w, h int, base byte) *Pic {
	p := &Pic{Width: w, Height: h, Pixels: make([]byte, w*h)}
	for i := range p.Pixels {
		p.Pixels[i] = byte(int(base) + i)
	}
	return p
}

// ----- DrawFill ----------------------------------------------------

func TestDrawFillHappy(t *testing.T) {
	fb := newTestFB(t, 10, 6, 0xAA)
	if err := DrawFill(fb, 2, 1, 3, 2, 0x42); err != nil {
		t.Fatalf("DrawFill: %v", err)
	}
	for y := 0; y < fb.Height; y++ {
		for x := 0; x < fb.Width; x++ {
			want := byte(0xAA)
			if x >= 2 && x < 5 && y >= 1 && y < 3 {
				want = 0x42
			}
			got := fb.Pixels[y*fb.Pitch+x]
			if got != want {
				t.Fatalf("(%d,%d) = %#x want %#x", x, y, got, want)
			}
		}
	}
}

func TestDrawFillNilFB(t *testing.T) {
	if err := DrawFill(nil, 0, 0, 1, 1, 0); !errors.Is(err, ErrDrawNilFB) {
		t.Fatalf("DrawFill(nil): %v want ErrDrawNilFB", err)
	}
}

func TestDrawFillClipXNeg(t *testing.T) {
	fb := newTestFB(t, 8, 4, 0)
	// x = -2, w = 5 -> rect ends at x=3 in dest; clamp left to 0.
	if err := DrawFill(fb, -2, 0, 5, 2, 0x11); err != nil {
		t.Fatalf("DrawFill: %v", err)
	}
	for y := 0; y < fb.Height; y++ {
		for x := 0; x < fb.Width; x++ {
			want := byte(0)
			if x < 3 && y < 2 {
				want = 0x11
			}
			if fb.Pixels[y*fb.Pitch+x] != want {
				t.Fatalf("(%d,%d) = %#x want %#x", x, y, fb.Pixels[y*fb.Pitch+x], want)
			}
		}
	}
}

func TestDrawFillClipYNeg(t *testing.T) {
	fb := newTestFB(t, 6, 6, 0)
	if err := DrawFill(fb, 1, -2, 2, 5, 0x22); err != nil {
		t.Fatalf("DrawFill: %v", err)
	}
	for y := 0; y < fb.Height; y++ {
		for x := 0; x < fb.Width; x++ {
			want := byte(0)
			if x >= 1 && x < 3 && y < 3 {
				want = 0x22
			}
			if fb.Pixels[y*fb.Pitch+x] != want {
				t.Fatalf("(%d,%d) = %#x want %#x", x, y, fb.Pixels[y*fb.Pitch+x], want)
			}
		}
	}
}

func TestDrawFillClipRight(t *testing.T) {
	fb := newTestFB(t, 6, 4, 0)
	// x=4, w=5 -> right edge would land at 9; clamp to 6.
	if err := DrawFill(fb, 4, 0, 5, 2, 0x33); err != nil {
		t.Fatalf("DrawFill: %v", err)
	}
	for y := 0; y < fb.Height; y++ {
		for x := 0; x < fb.Width; x++ {
			want := byte(0)
			if x >= 4 && y < 2 {
				want = 0x33
			}
			if fb.Pixels[y*fb.Pitch+x] != want {
				t.Fatalf("(%d,%d) = %#x want %#x", x, y, fb.Pixels[y*fb.Pitch+x], want)
			}
		}
	}
}

func TestDrawFillClipBottom(t *testing.T) {
	fb := newTestFB(t, 4, 4, 0)
	if err := DrawFill(fb, 0, 3, 2, 5, 0x44); err != nil {
		t.Fatalf("DrawFill: %v", err)
	}
	for y := 0; y < fb.Height; y++ {
		for x := 0; x < fb.Width; x++ {
			want := byte(0)
			if x < 2 && y == 3 {
				want = 0x44
			}
			if fb.Pixels[y*fb.Pitch+x] != want {
				t.Fatalf("(%d,%d) = %#x want %#x", x, y, fb.Pixels[y*fb.Pitch+x], want)
			}
		}
	}
}

func TestDrawFillFullyOffscreen(t *testing.T) {
	// Each subcase exercises a different cw<0 / ch<0 clamp path or
	// an "origin past the edge" no-op.
	cases := []struct {
		name                 string
		x, y, w, h, fbW, fbH int
	}{
		// w + x < 0 -> cw becomes negative after sx clamp.
		{"x_neg_overshoot", -10, 0, 3, 2, 8, 4},
		// h + y < 0 -> ch becomes negative.
		{"y_neg_overshoot", 0, -10, 2, 3, 8, 4},
		// origin past right edge -> cw = fbW - dx < 0.
		{"x_past_right", 10, 0, 2, 2, 4, 4},
		// origin past bottom edge -> ch = fbH - dy < 0.
		{"y_past_bottom", 0, 10, 2, 2, 4, 4},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fb := newTestFB(t, tc.fbW, tc.fbH, 0x77)
			if err := DrawFill(fb, tc.x, tc.y, tc.w, tc.h, 0x99); err != nil {
				t.Fatalf("DrawFill: %v", err)
			}
			for _, p := range fb.Pixels {
				if p != 0x77 {
					t.Fatalf("untouched fb mutated: %#x", p)
				}
			}
		})
	}
}

func TestDrawFillPitchGreaterThanWidth(t *testing.T) {
	const w, h, pitch = 4, 3, 7
	fb := newTestFBAligned(t, w, h, pitch, 0x55)
	if err := DrawFill(fb, 0, 0, w, h, 0xCC); err != nil {
		t.Fatalf("DrawFill: %v", err)
	}
	for y := 0; y < h; y++ {
		// In-Width bytes were filled.
		for x := 0; x < w; x++ {
			if got := fb.Pixels[y*pitch+x]; got != 0xCC {
				t.Fatalf("filled (%d,%d) = %#x", x, y, got)
			}
		}
		// Trailing pitch bytes (x in [w,pitch)) MUST remain 0x55.
		for x := w; x < pitch; x++ {
			if got := fb.Pixels[y*pitch+x]; got != 0x55 {
				t.Fatalf("trailing pitch (%d,%d) = %#x want 0x55", x, y, got)
			}
		}
	}
}

// ----- DrawPic -----------------------------------------------------

func TestDrawPicHappy(t *testing.T) {
	fb := newTestFB(t, 8, 6, 0)
	src := makePic(4, 4, 1) // bytes 1..16
	if err := DrawPic(fb, 2, 1, src); err != nil {
		t.Fatalf("DrawPic: %v", err)
	}
	for y := 0; y < fb.Height; y++ {
		for x := 0; x < fb.Width; x++ {
			want := byte(0)
			if x >= 2 && x < 6 && y >= 1 && y < 5 {
				want = byte((y-1)*4 + (x - 2) + 1)
			}
			got := fb.Pixels[y*fb.Pitch+x]
			if got != want {
				t.Fatalf("(%d,%d) = %#x want %#x", x, y, got, want)
			}
		}
	}
}

func TestDrawPicNilFB(t *testing.T) {
	src := makePic(2, 2, 0)
	if err := DrawPic(nil, 0, 0, src); !errors.Is(err, ErrDrawNilFB) {
		t.Fatalf("DrawPic(nil fb): %v", err)
	}
}

func TestDrawPicNilSrc(t *testing.T) {
	fb := newTestFB(t, 4, 4, 0)
	if err := DrawPic(fb, 0, 0, nil); !errors.Is(err, ErrPicNilSrc) {
		t.Fatalf("DrawPic(nil src): %v", err)
	}
}

func TestDrawPicShape(t *testing.T) {
	fb := newTestFB(t, 4, 4, 0)
	bad := &Pic{Width: 3, Height: 2, Pixels: []byte{1, 2, 3}} // need 6
	if err := DrawPic(fb, 0, 0, bad); !errors.Is(err, ErrPicShape) {
		t.Fatalf("DrawPic(bad shape): %v", err)
	}
}

func TestDrawPicClipXNeg(t *testing.T) {
	fb := newTestFB(t, 6, 4, 0)
	src := makePic(4, 2, 1) // 1..8
	if err := DrawPic(fb, -2, 0, src); err != nil {
		t.Fatalf("DrawPic: %v", err)
	}
	// Dest cols 0..1 = src cols 2..3 of each row.
	if fb.Pixels[0] != 3 || fb.Pixels[1] != 4 {
		t.Fatalf("row0 = %d,%d", fb.Pixels[0], fb.Pixels[1])
	}
	if fb.Pixels[fb.Pitch] != 7 || fb.Pixels[fb.Pitch+1] != 8 {
		t.Fatalf("row1 = %d,%d", fb.Pixels[fb.Pitch], fb.Pixels[fb.Pitch+1])
	}
}

func TestDrawPicClipYNeg(t *testing.T) {
	fb := newTestFB(t, 4, 4, 0)
	src := makePic(2, 4, 1) // rows of [1,2] [3,4] [5,6] [7,8]
	if err := DrawPic(fb, 0, -2, src); err != nil {
		t.Fatalf("DrawPic: %v", err)
	}
	if fb.Pixels[0] != 5 || fb.Pixels[1] != 6 {
		t.Fatalf("row0 = %d,%d", fb.Pixels[0], fb.Pixels[1])
	}
	if fb.Pixels[fb.Pitch] != 7 || fb.Pixels[fb.Pitch+1] != 8 {
		t.Fatalf("row1 = %d,%d", fb.Pixels[fb.Pitch], fb.Pixels[fb.Pitch+1])
	}
}

func TestDrawPicClipRight(t *testing.T) {
	fb := newTestFB(t, 4, 3, 0)
	src := makePic(3, 2, 1)
	if err := DrawPic(fb, 2, 0, src); err != nil {
		t.Fatalf("DrawPic: %v", err)
	}
	// Only first 2 src cols make it (dest x=2,3).
	if fb.Pixels[2] != 1 || fb.Pixels[3] != 2 {
		t.Fatalf("row0 = %d,%d", fb.Pixels[2], fb.Pixels[3])
	}
}

func TestDrawPicClipBottom(t *testing.T) {
	fb := newTestFB(t, 4, 3, 0)
	src := makePic(2, 3, 1)
	if err := DrawPic(fb, 0, 2, src); err != nil {
		t.Fatalf("DrawPic: %v", err)
	}
	if fb.Pixels[2*fb.Pitch] != 1 || fb.Pixels[2*fb.Pitch+1] != 2 {
		t.Fatalf("row2 = %d,%d", fb.Pixels[2*fb.Pitch], fb.Pixels[2*fb.Pitch+1])
	}
}

func TestDrawPicFullyOffscreen(t *testing.T) {
	fb := newTestFB(t, 4, 4, 0x33)
	src := makePic(2, 2, 0xFF)
	if err := DrawPic(fb, 100, 0, src); err != nil {
		t.Fatalf("DrawPic: %v", err)
	}
	for _, p := range fb.Pixels {
		if p != 0x33 {
			t.Fatalf("offscreen DrawPic mutated fb: %#x", p)
		}
	}
}

// ----- DrawTransPic ------------------------------------------------

func TestDrawTransPicHappy(t *testing.T) {
	fb := newTestFB(t, 4, 2, 0x77)
	src := &Pic{
		Width: 4, Height: 2,
		Pixels: []byte{
			0x10, TransparentIndex, 0x12, TransparentIndex,
			TransparentIndex, 0x21, TransparentIndex, 0x23,
		},
	}
	if err := DrawTransPic(fb, 0, 0, src); err != nil {
		t.Fatalf("DrawTransPic: %v", err)
	}
	want := []byte{
		0x10, 0x77, 0x12, 0x77,
		0x77, 0x21, 0x77, 0x23,
	}
	for i, w := range want {
		if fb.Pixels[i] != w {
			t.Fatalf("pixel %d = %#x want %#x", i, fb.Pixels[i], w)
		}
	}
}

func TestDrawTransPicNilFB(t *testing.T) {
	if err := DrawTransPic(nil, 0, 0, &Pic{}); !errors.Is(err, ErrDrawNilFB) {
		t.Fatalf("DrawTransPic(nil fb): %v", err)
	}
}

func TestDrawTransPicNilSrc(t *testing.T) {
	fb := newTestFB(t, 2, 2, 0)
	if err := DrawTransPic(fb, 0, 0, nil); !errors.Is(err, ErrPicNilSrc) {
		t.Fatalf("DrawTransPic(nil src): %v", err)
	}
}

func TestDrawTransPicShape(t *testing.T) {
	fb := newTestFB(t, 2, 2, 0)
	bad := &Pic{Width: 2, Height: 2, Pixels: []byte{1}}
	if err := DrawTransPic(fb, 0, 0, bad); !errors.Is(err, ErrPicShape) {
		t.Fatalf("DrawTransPic(bad shape): %v", err)
	}
}

func TestDrawTransPicFullyOffscreen(t *testing.T) {
	fb := newTestFB(t, 4, 4, 0xAB)
	src := makePic(2, 2, 1)
	if err := DrawTransPic(fb, -100, -100, src); err != nil {
		t.Fatalf("DrawTransPic: %v", err)
	}
	for _, p := range fb.Pixels {
		if p != 0xAB {
			t.Fatalf("offscreen DrawTransPic mutated fb: %#x", p)
		}
	}
}

// ----- DrawCharacter -----------------------------------------------

// makeCharsSheet returns a 128x128 conchars Pic where glyph (row,col)
// is filled with the byte value `0x10 + row*16 + col` (clamped per-
// cell to a single value). That lets us assert "we copied glyph N"
// without enumerating 8x8 bytes.
func makeCharsSheet(t *testing.T) *Pic {
	t.Helper()
	p := &Pic{
		Width:  conSheetDim,
		Height: conSheetDim,
		Pixels: make([]byte, conSheetDim*conSheetDim),
	}
	for row := 0; row < 16; row++ {
		for col := 0; col < 16; col++ {
			glyph := byte(0x10 + row*16 + col)
			for v := 0; v < CharHeight; v++ {
				base := (row*CharHeight+v)*conSheetDim + col*CharWidth
				for u := 0; u < CharWidth; u++ {
					p.Pixels[base+u] = glyph
				}
			}
		}
	}
	return p
}

func TestDrawCharacterHappy(t *testing.T) {
	fb := newTestFB(t, 32, 16, 0)
	chars := makeCharsSheet(t)
	// 'A' = 0x41 -> row=4, col=1 -> glyph byte 0x10 + 4*16 + 1 = 0x51.
	if err := DrawCharacter(fb, chars, 3, 2, 'A'); err != nil {
		t.Fatalf("DrawCharacter: %v", err)
	}
	for y := 0; y < fb.Height; y++ {
		for x := 0; x < fb.Width; x++ {
			want := byte(0)
			if x >= 3 && x < 3+CharWidth && y >= 2 && y < 2+CharHeight {
				want = 0x51
			}
			if got := fb.Pixels[y*fb.Pitch+x]; got != want {
				t.Fatalf("(%d,%d) = %#x want %#x", x, y, got, want)
			}
		}
	}
}

func TestDrawCharacterTransparentSkip(t *testing.T) {
	fb := newTestFB(t, 16, 8, 0x77)
	chars := makeCharsSheet(t)
	// Punch transparency into one pixel of glyph 'B' (0x42 -> row=4,
	// col=2). Then verify only that pixel keeps the dest background.
	row, col := int('B'>>4), int('B'&15)
	chars.Pixels[(row*CharHeight+0)*conSheetDim+col*CharWidth+0] = TransparentIndex
	if err := DrawCharacter(fb, chars, 0, 0, 'B'); err != nil {
		t.Fatalf("DrawCharacter: %v", err)
	}
	if fb.Pixels[0] != 0x77 {
		t.Fatalf("transparent src did not preserve dest: %#x", fb.Pixels[0])
	}
	// Adjacent pixel in the same row was opaque -> wrote glyph byte
	// 0x10 + 4*16 + 2 = 0x52.
	if fb.Pixels[1] != 0x52 {
		t.Fatalf("opaque src did not write: %#x", fb.Pixels[1])
	}
}

func TestDrawCharacterNilFB(t *testing.T) {
	if err := DrawCharacter(nil, &Pic{}, 0, 0, 'A'); !errors.Is(err, ErrDrawNilFB) {
		t.Fatalf("DrawCharacter(nil fb): %v", err)
	}
}

func TestDrawCharacterNilChars(t *testing.T) {
	fb := newTestFB(t, 8, 8, 0)
	if err := DrawCharacter(fb, nil, 0, 0, 'A'); !errors.Is(err, ErrDrawCharsNilSrc) {
		t.Fatalf("DrawCharacter(nil chars): %v", err)
	}
}

func TestDrawCharacterCharsShape(t *testing.T) {
	fb := newTestFB(t, 8, 8, 0)
	// Wrong dimensions.
	bad := &Pic{Width: 64, Height: 64, Pixels: make([]byte, 64*64)}
	if err := DrawCharacter(fb, bad, 0, 0, 'A'); !errors.Is(err, ErrDrawCharsShape) {
		t.Fatalf("DrawCharacter(bad chars dims): %v", err)
	}
	// Correct dims but Pixels length mismatch.
	badLen := &Pic{Width: conSheetDim, Height: conSheetDim, Pixels: make([]byte, 4)}
	if err := DrawCharacter(fb, badLen, 0, 0, 'A'); !errors.Is(err, ErrDrawCharsShape) {
		t.Fatalf("DrawCharacter(bad chars len): %v", err)
	}
}

func TestDrawCharacterNULNoop(t *testing.T) {
	fb := newTestFB(t, 8, 8, 0x66)
	chars := makeCharsSheet(t)
	if err := DrawCharacter(fb, chars, 0, 0, 0); err != nil {
		t.Fatalf("DrawCharacter(NUL): %v", err)
	}
	for _, p := range fb.Pixels {
		if p != 0x66 {
			t.Fatalf("NUL mutated fb: %#x", p)
		}
	}
}

func TestDrawCharacterSpaceNoop(t *testing.T) {
	fb := newTestFB(t, 8, 8, 0x66)
	chars := makeCharsSheet(t)
	if err := DrawCharacter(fb, chars, 0, 0, ' '); err != nil {
		t.Fatalf("DrawCharacter(space): %v", err)
	}
	for _, p := range fb.Pixels {
		if p != 0x66 {
			t.Fatalf("SPACE mutated fb: %#x", p)
		}
	}
}

func TestDrawCharacterFullyOffscreen(t *testing.T) {
	fb := newTestFB(t, 8, 8, 0x55)
	chars := makeCharsSheet(t)
	if err := DrawCharacter(fb, chars, 100, 100, 'A'); err != nil {
		t.Fatalf("DrawCharacter: %v", err)
	}
	for _, p := range fb.Pixels {
		if p != 0x55 {
			t.Fatalf("offscreen DrawCharacter mutated fb: %#x", p)
		}
	}
}

func TestDrawCharacterPartialClipTopLeft(t *testing.T) {
	// Tests the sxOff/syOff branches in DrawCharacter clipping.
	fb := newTestFB(t, 16, 16, 0)
	chars := makeCharsSheet(t)
	// 'C' = 0x43 -> row=4, col=3 -> glyph byte 0x53.
	if err := DrawCharacter(fb, chars, -3, -2, 'C'); err != nil {
		t.Fatalf("DrawCharacter: %v", err)
	}
	// Dest top-left of the visible glyph chunk:
	//   width covered = CharWidth - 3 = 5
	//   height covered = CharHeight - 2 = 6
	for y := 0; y < 6; y++ {
		for x := 0; x < 5; x++ {
			if got := fb.Pixels[y*fb.Pitch+x]; got != 0x53 {
				t.Fatalf("(%d,%d) = %#x want 0x53", x, y, got)
			}
		}
	}
	// Outside that region must still be 0.
	for y := 0; y < fb.Height; y++ {
		for x := 0; x < fb.Width; x++ {
			if x < 5 && y < 6 {
				continue
			}
			if got := fb.Pixels[y*fb.Pitch+x]; got != 0 {
				t.Fatalf("(%d,%d) = %#x want 0", x, y, got)
			}
		}
	}
}

// ----- Constants ---------------------------------------------------

func TestDrawConstants(t *testing.T) {
	if CharWidth != 8 || CharHeight != 8 {
		t.Fatalf("CharWidth/Height = %d/%d want 8/8", CharWidth, CharHeight)
	}
	if TransparentIndex != 255 {
		t.Fatalf("TransparentIndex = %d want 255", TransparentIndex)
	}
}

// TestDrawPicHUDScaleTwo: with HUDScale == 2, a 2x2 source Pic blits
// as a 4x4 block on the physical surface (each src pixel replicated
// scale x scale times). The input (x, y) is in VIRTUAL coords:
// passing (1, 1) lands at physical (2, 2).
func TestDrawPicHUDScaleTwo(t *testing.T) {
	fb, _ := NewFrameBuffer(16, 16)
	fb.HUDScale = 2
	src := &Pic{
		Width:  2,
		Height: 2,
		Pixels: []byte{1, 2, 3, 4},
	}
	if err := DrawPic(fb, 1, 1, src); err != nil {
		t.Fatalf("DrawPic: %v", err)
	}
	// Expected: src (1,1) -> physical block (2,2)..(5,5), pixel
	// replication = each src cell becomes 2x2.
	//
	//   (2,2) (3,2) | (4,2) (5,2)        1 1 | 2 2
	//   (2,3) (3,3) | (4,3) (5,3)   =>   1 1 | 2 2
	//   ----------- + -----------        ---- + ----
	//   (2,4) (3,4) | (4,4) (5,4)        3 3 | 4 4
	//   (2,5) (3,5) | (4,5) (5,5)        3 3 | 4 4
	want := map[[2]int]byte{
		{2, 2}: 1, {3, 2}: 1, {4, 2}: 2, {5, 2}: 2,
		{2, 3}: 1, {3, 3}: 1, {4, 3}: 2, {5, 3}: 2,
		{2, 4}: 3, {3, 4}: 3, {4, 4}: 4, {5, 4}: 4,
		{2, 5}: 3, {3, 5}: 3, {4, 5}: 4, {5, 5}: 4,
	}
	for pos, b := range want {
		got, _ := fb.GetPixel(pos[0], pos[1])
		if got != b {
			t.Errorf("DrawPic@(%d,%d) = %d, want %d", pos[0], pos[1], got, b)
		}
	}
	// Spot-check that pixels OUTSIDE the upscaled block are
	// untouched (still 0).
	for _, pos := range [][2]int{{0, 0}, {1, 1}, {6, 6}, {15, 15}} {
		got, _ := fb.GetPixel(pos[0], pos[1])
		if got != 0 {
			t.Errorf("DrawPic leaked into (%d,%d) = %d, want 0", pos[0], pos[1], got)
		}
	}
}

// TestDrawTransPicHUDScaleTwo: same upscale as DrawPic, plus the
// transparent-index skip survives the per-cell replication so the
// pre-existing dst pixels show through a transparent src cell.
func TestDrawTransPicHUDScaleTwo(t *testing.T) {
	fb, _ := NewFrameBuffer(16, 16)
	fb.HUDScale = 2
	// Pre-fill with sentinel 99 so we can see what got overwritten.
	fb.Clear(99)
	src := &Pic{
		Width:  2,
		Height: 2,
		Pixels: []byte{1, TransparentIndex, TransparentIndex, 4},
	}
	if err := DrawTransPic(fb, 0, 0, src); err != nil {
		t.Fatalf("DrawTransPic: %v", err)
	}
	// (0,0) cell = 1 -> 2x2 block at (0..1, 0..1)
	for y := 0; y < 2; y++ {
		for x := 0; x < 2; x++ {
			got, _ := fb.GetPixel(x, y)
			if got != 1 {
				t.Errorf("opaque cell @(%d,%d) = %d, want 1", x, y, got)
			}
		}
	}
	// (1,0) cell = transparent -> block (2..3, 0..1) untouched
	for y := 0; y < 2; y++ {
		for x := 2; x < 4; x++ {
			got, _ := fb.GetPixel(x, y)
			if got != 99 {
				t.Errorf("transparent cell @(%d,%d) leaked = %d, want 99", x, y, got)
			}
		}
	}
	// (1,1) cell = 4 -> block (2..3, 2..3)
	for y := 2; y < 4; y++ {
		for x := 2; x < 4; x++ {
			got, _ := fb.GetPixel(x, y)
			if got != 4 {
				t.Errorf("opaque cell @(%d,%d) = %d, want 4", x, y, got)
			}
		}
	}
}

// TestDrawCharacterHUDScaleTwo: a single 8x8 glyph blits as 16x16 on
// the physical surface at HUDScale == 2. Position is virtual: (1, 1)
// virtual = (2, 2) physical.
func TestDrawCharacterHUDScaleTwo(t *testing.T) {
	fb, _ := NewFrameBuffer(64, 64)
	fb.HUDScale = 2
	// Build a 128x128 conchars sheet where the glyph for 'A' (0x41)
	// is filled with palette index 7 so we can locate the upscaled
	// block by its colour.
	chars := &Pic{
		Width:  128,
		Height: 128,
		Pixels: make([]byte, 128*128),
	}
	// Pre-fill chars with TransparentIndex so non-glyph cells skip.
	for i := range chars.Pixels {
		chars.Pixels[i] = TransparentIndex
	}
	// Glyph 'A' (0x41) = row 4, col 1 -> srcX=8, srcY=32.
	for y := 0; y < CharHeight; y++ {
		for x := 0; x < CharWidth; x++ {
			chars.Pixels[(32+y)*128+(8+x)] = 7
		}
	}
	if err := DrawCharacter(fb, chars, 1, 1, 'A'); err != nil {
		t.Fatalf("DrawCharacter: %v", err)
	}
	// Expected: 16x16 block at (2,2)..(17,17) all = 7.
	for y := 2; y < 18; y++ {
		for x := 2; x < 18; x++ {
			got, _ := fb.GetPixel(x, y)
			if got != 7 {
				t.Errorf("DrawCharacter @(%d,%d) = %d, want 7", x, y, got)
			}
		}
	}
	// Outside the upscaled block (e.g. (0,0), (1,1), (18,18)) must
	// be untouched.
	for _, pos := range [][2]int{{0, 0}, {1, 1}, {18, 18}, {20, 20}} {
		got, _ := fb.GetPixel(pos[0], pos[1])
		if got != 0 {
			t.Errorf("DrawCharacter leaked into (%d,%d) = %d, want 0", pos[0], pos[1], got)
		}
	}
}

// TestDrawFillHUDScaleTwo: a virtual (0,0,4,4) DrawFill upscales to
// an (0,0,8,8) physical block.
func TestDrawFillHUDScaleTwo(t *testing.T) {
	fb, _ := NewFrameBuffer(16, 16)
	fb.HUDScale = 2
	if err := DrawFill(fb, 0, 0, 4, 4, 5); err != nil {
		t.Fatalf("DrawFill: %v", err)
	}
	for y := 0; y < 8; y++ {
		for x := 0; x < 8; x++ {
			got, _ := fb.GetPixel(x, y)
			if got != 5 {
				t.Errorf("DrawFill @(%d,%d) = %d, want 5", x, y, got)
			}
		}
	}
	// Spot-check pixel outside the upscaled rect.
	got, _ := fb.GetPixel(8, 8)
	if got != 0 {
		t.Errorf("DrawFill leaked into (8,8) = %d, want 0", got)
	}
}
