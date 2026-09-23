// Copyright (c) 1996-1997 Id Software, Inc.
// Copyright (c) 2026 the go-quake1/engine authors.
// SPDX-License-Identifier: GPL-2.0-or-later

package render

import (
	"bytes"
	"errors"
	"testing"
)

// variedCharsSheet returns a 128x128 conchars Pic whose bytes span the
// full 0..255 range in a deterministic non-uniform pattern -- crucially
// it contains TransparentIndex (255) pixels so the transparent-skip
// branch of the blit is exercised, and it varies WITHIN each 8x8 glyph
// so a naive "copy the whole tile" bug can't pass by luck.
func variedCharsSheet() *Pic {
	p := &Pic{
		Width:  conSheetDim,
		Height: conSheetDim,
		Pixels: make([]byte, conSheetDim*conSheetDim),
	}
	for i := range p.Pixels {
		p.Pixels[i] = byte((i*37 + (i>>3)*11 + (i>>7)*5) & 0xff)
	}
	return p
}

// ----- constructor + invalidation ---------------------------------

func TestNewGlyphCacheErrors(t *testing.T) {
	good := variedCharsSheet()
	cases := []struct {
		name  string
		chars *Pic
		scale int
		want  error
	}{
		{"nil chars", nil, 1, ErrDrawCharsNilSrc},
		{"bad shape", &Pic{Width: 64, Height: 64, Pixels: make([]byte, 64*64)}, 1, ErrDrawCharsShape},
		{"bad pixels len", &Pic{Width: 128, Height: 128, Pixels: make([]byte, 10)}, 1, ErrDrawCharsShape},
		{"zero scale", good, 0, ErrGlyphBadScale},
		{"negative scale", good, -2, ErrGlyphBadScale},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, err := NewGlyphCache(c.chars, c.scale); !errors.Is(err, c.want) {
				t.Fatalf("NewGlyphCache err = %v want %v", err, c.want)
			}
		})
	}
}

func TestNewGlyphCacheGettersAndShape(t *testing.T) {
	chars := variedCharsSheet()
	gc, err := NewGlyphCache(chars, 2)
	if err != nil {
		t.Fatalf("NewGlyphCache: %v", err)
	}
	if gc.Scale() != 2 {
		t.Fatalf("Scale() = %d want 2", gc.Scale())
	}
	if gc.Chars() != chars {
		t.Fatalf("Chars() identity mismatch")
	}
	if gc.dim != CharWidth*2 {
		t.Fatalf("dim = %d want %d", gc.dim, CharWidth*2)
	}
	if len(gc.tiles) != 256 {
		t.Fatalf("tiles len = %d want 256", len(gc.tiles))
	}
	for ch := 0; ch < 256; ch++ {
		if len(gc.tiles[ch]) != gc.dim*gc.dim {
			t.Fatalf("tile[%d] len = %d want %d", ch, len(gc.tiles[ch]), gc.dim*gc.dim)
		}
	}
}

func TestGlyphCacheEnsureFor(t *testing.T) {
	a := variedCharsSheet()
	b := variedCharsSheet()
	gc, err := NewGlyphCache(a, 1)
	if err != nil {
		t.Fatalf("NewGlyphCache: %v", err)
	}

	// Same (chars, scale) -> no rebuild.
	if rebuilt, err := gc.EnsureFor(a, 1); err != nil || rebuilt {
		t.Fatalf("EnsureFor(same) = (%v, %v) want (false, nil)", rebuilt, err)
	}
	// Changed scale -> rebuild.
	if rebuilt, err := gc.EnsureFor(a, 2); err != nil || !rebuilt {
		t.Fatalf("EnsureFor(scale change) = (%v, %v) want (true, nil)", rebuilt, err)
	}
	if gc.Scale() != 2 {
		t.Fatalf("after rebuild Scale() = %d want 2", gc.Scale())
	}
	// Changed sheet identity -> rebuild.
	if rebuilt, err := gc.EnsureFor(b, 2); err != nil || !rebuilt {
		t.Fatalf("EnsureFor(sheet change) = (%v, %v) want (true, nil)", rebuilt, err)
	}
	if gc.Chars() != b {
		t.Fatalf("after rebuild Chars() identity mismatch")
	}

	// Error paths leave the cache untouched.
	prevChars, prevScale := gc.Chars(), gc.Scale()
	if _, err := gc.EnsureFor(nil, 2); !errors.Is(err, ErrDrawCharsNilSrc) {
		t.Fatalf("EnsureFor(nil) err = %v want ErrDrawCharsNilSrc", err)
	}
	if _, err := gc.EnsureFor(b, 0); !errors.Is(err, ErrGlyphBadScale) {
		t.Fatalf("EnsureFor(scale 0) err = %v want ErrGlyphBadScale", err)
	}
	if gc.Chars() != prevChars || gc.Scale() != prevScale {
		t.Fatalf("errored EnsureFor mutated the cache")
	}
}

// ----- identical-output PROOF vs the free DrawCharacter ------------

// TestGlyphCacheParityExhaustive is the byte-for-byte identical-output
// proof: for every scale in {1,2,3}, every glyph 0..255, and a spread
// of positions (on-screen, partial top-left clip, partial bottom-right
// overrun, and fully off-screen), the cached blit must produce a
// framebuffer byte-identical to the free DrawCharacter.
func TestGlyphCacheParityExhaustive(t *testing.T) {
	chars := variedCharsSheet()
	for _, scale := range []int{1, 2, 3} {
		gc, err := NewGlyphCache(chars, scale)
		if err != nil {
			t.Fatalf("NewGlyphCache(scale=%d): %v", scale, err)
		}
		// Physical framebuffer; virtual dims are phys/scale.
		const pw, ph = 120, 96
		vw, vh := pw/scale, ph/scale
		positions := [][2]int{
			{3, 3},               // fully on-screen
			{0, 0},               // top-left corner
			{-1, -1},             // partial top-left clip
			{-2, 4},              // clip left only
			{vw - 2, vh - 2},     // partial bottom-right overrun
			{vw + 50, vh + 50},   // fully off-screen (cw/hh == 0)
			{-vw - 50, -vh - 50}, // fully off-screen negative
		}
		for ch := 0; ch < 256; ch++ {
			for _, p := range positions {
				want := newTestFB(t, pw, ph, 0)
				want.HUDScale = scale
				got := newTestFB(t, pw, ph, 0)
				got.HUDScale = scale

				if err := DrawCharacter(want, chars, p[0], p[1], byte(ch)); err != nil {
					t.Fatalf("free DrawCharacter(scale=%d ch=%d pos=%v): %v", scale, ch, p, err)
				}
				if err := gc.DrawCharacter(got, p[0], p[1], byte(ch)); err != nil {
					t.Fatalf("cached DrawCharacter(scale=%d ch=%d pos=%v): %v", scale, ch, p, err)
				}
				if !bytes.Equal(want.Pixels, got.Pixels) {
					t.Fatalf("MISMATCH scale=%d ch=%d pos=%v: cached != free", scale, ch, p)
				}
			}
		}
	}
}

// ----- DrawCharacter error + no-op branches ------------------------

func TestGlyphCacheDrawCharacterGuards(t *testing.T) {
	chars := variedCharsSheet()
	gc, err := NewGlyphCache(chars, 1)
	if err != nil {
		t.Fatalf("NewGlyphCache: %v", err)
	}
	fb := newTestFB(t, 32, 16, 0)

	if err := gc.DrawCharacter(nil, 0, 0, 'A'); !errors.Is(err, ErrDrawNilFB) {
		t.Fatalf("nil fb err = %v want ErrDrawNilFB", err)
	}

	// Scale mismatch: cache built at 1, fb wants 2.
	fb2 := newTestFB(t, 32, 16, 0)
	fb2.HUDScale = 2
	if err := gc.DrawCharacter(fb2, 0, 0, 'A'); !errors.Is(err, ErrGlyphBadScale) {
		t.Fatalf("scale mismatch err = %v want ErrGlyphBadScale", err)
	}

	// ch == 0 and ch == ' ' are no-ops: framebuffer unchanged.
	snapshot := append([]byte(nil), fb.Pixels...)
	if err := gc.DrawCharacter(fb, 0, 0, 0); err != nil {
		t.Fatalf("ch=0: %v", err)
	}
	if err := gc.DrawCharacter(fb, 0, 0, ' '); err != nil {
		t.Fatalf("ch=' ': %v", err)
	}
	if !bytes.Equal(snapshot, fb.Pixels) {
		t.Fatalf("no-op glyph modified the framebuffer")
	}

	// Fully off-screen -> clip-zero early return, framebuffer unchanged.
	if err := gc.DrawCharacter(fb, 1000, 1000, 'A'); err != nil {
		t.Fatalf("offscreen: %v", err)
	}
	if !bytes.Equal(snapshot, fb.Pixels) {
		t.Fatalf("off-screen glyph modified the framebuffer")
	}
}

// ----- string helpers parity + error propagation ------------------

func TestGlyphCacheStringParity(t *testing.T) {
	chars := variedCharsSheet()
	gc, err := NewGlyphCache(chars, 2)
	if err != nil {
		t.Fatalf("NewGlyphCache: %v", err)
	}
	const s = "Quake! 42 %"
	type pair struct {
		name   string
		free   func(fb *FrameBuffer) error
		cached func(fb *FrameBuffer) error
	}
	pairs := []pair{
		{
			"DrawString",
			func(fb *FrameBuffer) error { return DrawString(fb, chars, 5, 5, s) },
			func(fb *FrameBuffer) error { return gc.DrawString(fb, 5, 5, s) },
		},
		{
			"DrawColorString",
			func(fb *FrameBuffer) error { return DrawColorString(fb, chars, 5, 5, s) },
			func(fb *FrameBuffer) error { return gc.DrawColorString(fb, 5, 5, s) },
		},
		{
			"DrawCenteredString",
			func(fb *FrameBuffer) error { return DrawCenteredString(fb, chars, 60, 5, s) },
			func(fb *FrameBuffer) error { return gc.DrawCenteredString(fb, 60, 5, s) },
		},
	}
	for _, p := range pairs {
		t.Run(p.name, func(t *testing.T) {
			want := newTestFB(t, 240, 96, 0)
			want.HUDScale = 2
			got := newTestFB(t, 240, 96, 0)
			got.HUDScale = 2
			if err := p.free(want); err != nil {
				t.Fatalf("free: %v", err)
			}
			if err := p.cached(got); err != nil {
				t.Fatalf("cached: %v", err)
			}
			if !bytes.Equal(want.Pixels, got.Pixels) {
				t.Fatalf("%s: cached != free", p.name)
			}
		})
	}
}

func TestGlyphCacheStringErrorPropagation(t *testing.T) {
	chars := variedCharsSheet()
	gc, err := NewGlyphCache(chars, 1)
	if err != nil {
		t.Fatalf("NewGlyphCache: %v", err)
	}
	// nil fb makes the first per-glyph blit fail; the wrapper must
	// surface it (covers the error-return branch of each helper).
	if err := gc.DrawString(nil, 0, 0, "x"); !errors.Is(err, ErrDrawNilFB) {
		t.Fatalf("DrawString err = %v want ErrDrawNilFB", err)
	}
	if err := gc.DrawColorString(nil, 0, 0, "x"); !errors.Is(err, ErrDrawNilFB) {
		t.Fatalf("DrawColorString err = %v want ErrDrawNilFB", err)
	}
	if err := gc.DrawCenteredString(nil, 0, 0, "x"); !errors.Is(err, ErrDrawNilFB) {
		t.Fatalf("DrawCenteredString err = %v want ErrDrawNilFB", err)
	}
	// Empty string: loops don't run, all return nil.
	fb := newTestFB(t, 16, 16, 0)
	if err := gc.DrawString(fb, 0, 0, ""); err != nil {
		t.Fatalf("DrawString empty: %v", err)
	}
	if err := gc.DrawColorString(fb, 0, 0, ""); err != nil {
		t.Fatalf("DrawColorString empty: %v", err)
	}
}

// ----- cached console / notify parity + guards --------------------

// fillConsole writes a full grid of printable glyphs into con so the
// draw path walks non-space cells.
func fillConsole(con *Console) {
	for r := 0; r < con.Lines; r++ {
		for c := 0; c < con.Width; c++ {
			con.SetCell(c, r, byte('!'+((r*con.Width+c)%94)))
		}
	}
	con.CurrentRow = con.Lines - 1
}

func TestDrawConsoleCachedParity(t *testing.T) {
	chars := variedCharsSheet()
	for _, scale := range []int{1, 2} {
		gc, err := NewGlyphCache(chars, scale)
		if err != nil {
			t.Fatalf("NewGlyphCache: %v", err)
		}
		con, err := NewConsole(40, 25)
		if err != nil {
			t.Fatalf("NewConsole: %v", err)
		}
		fillConsole(con)

		pw, ph := 320*scale, 200*scale
		s, err := NewScreen(pw, ph)
		if err != nil {
			t.Fatalf("NewScreen: %v", err)
		}
		s.ConCurrent = ph // fully open

		for _, back := range []int{0, 3} {
			con.BackScroll = back
			want := newTestFB(t, pw, ph, 0)
			want.HUDScale = scale
			got := newTestFB(t, pw, ph, 0)
			got.HUDScale = scale
			if err := s.DrawConsole(want, con, chars); err != nil {
				t.Fatalf("DrawConsole: %v", err)
			}
			if err := s.DrawConsoleCached(got, con, gc); err != nil {
				t.Fatalf("DrawConsoleCached: %v", err)
			}
			if !bytes.Equal(want.Pixels, got.Pixels) {
				t.Fatalf("console scale=%d back=%d: cached != free", scale, back)
			}
		}
	}
}

func TestDrawConsoleCachedGuards(t *testing.T) {
	chars := variedCharsSheet()
	gc, _ := NewGlyphCache(chars, 1)
	con, _ := NewConsole(40, 25)
	fillConsole(con)
	s, _ := NewScreen(320, 200)
	s.ConCurrent = 200
	fb := newTestFB(t, 320, 200, 0)

	if err := s.DrawConsoleCached(nil, con, gc); !errors.Is(err, ErrScreenDrawFB) {
		t.Fatalf("nil fb err = %v", err)
	}
	if err := s.DrawConsoleCached(fb, nil, gc); !errors.Is(err, ErrScreenCons) {
		t.Fatalf("nil con err = %v", err)
	}
	if err := s.DrawConsoleCached(fb, con, nil); !errors.Is(err, ErrScreenChars) {
		t.Fatalf("nil gc err = %v", err)
	}

	// rows <= 0 (console closed) -> nil, no draw.
	closed, _ := NewScreen(320, 200)
	closed.ConCurrent = 0
	if err := closed.DrawConsoleCached(fb, con, gc); err != nil {
		t.Fatalf("closed console: %v", err)
	}

	// cols <= 0: a framebuffer too narrow for even one column after
	// the left pad drops the column count to <= 0.
	narrowS, _ := NewScreen(CharWidth+2, 200)
	narrowFB := newTestFB(t, CharWidth+2, 200, 0)
	narrowS.ConCurrent = 200
	if err := narrowS.DrawConsoleCached(narrowFB, con, gc); err != nil {
		t.Fatalf("narrow console: %v", err)
	}

	// Per-glyph error surfaces: scale mismatch on a live draw.
	fb2 := newTestFB(t, 320, 200, 0)
	fb2.HUDScale = 2
	if err := s.DrawConsoleCached(fb2, con, gc); !errors.Is(err, ErrGlyphBadScale) {
		t.Fatalf("scale-mismatch draw err = %v want ErrGlyphBadScale", err)
	}
	// Same, on the back-scroll arrow row.
	con.BackScroll = 2
	if err := s.DrawConsoleCached(fb2, con, gc); !errors.Is(err, ErrGlyphBadScale) {
		t.Fatalf("scale-mismatch arrow err = %v want ErrGlyphBadScale", err)
	}
	con.BackScroll = 0
}

func TestDrawNotifyCachedParity(t *testing.T) {
	chars := variedCharsSheet()
	gc, _ := NewGlyphCache(chars, 1)
	con, _ := NewConsole(40, 25)
	// Print a few timestamped lines so NotifyRows returns them.
	for i := 0; i < 3; i++ {
		con.Linefeed(1.0)
		con.Print("notify line here")
	}
	s, _ := NewScreen(320, 200)

	want := newTestFB(t, 320, 200, 0)
	got := newTestFB(t, 320, 200, 0)
	if err := s.DrawNotify(want, con, chars, 1.0, 10.0, MaxNotifyLines); err != nil {
		t.Fatalf("DrawNotify: %v", err)
	}
	if err := s.DrawNotifyCached(got, con, gc, 1.0, 10.0, MaxNotifyLines); err != nil {
		t.Fatalf("DrawNotifyCached: %v", err)
	}
	if !bytes.Equal(want.Pixels, got.Pixels) {
		t.Fatalf("notify: cached != free")
	}
}

func TestDrawNotifyCachedGuards(t *testing.T) {
	chars := variedCharsSheet()
	gc, _ := NewGlyphCache(chars, 1)
	con, _ := NewConsole(40, 25)
	s, _ := NewScreen(320, 200)
	fb := newTestFB(t, 320, 200, 0)

	if err := s.DrawNotifyCached(nil, con, gc, 1, 10, 4); !errors.Is(err, ErrScreenDrawFB) {
		t.Fatalf("nil fb err = %v", err)
	}
	if err := s.DrawNotifyCached(fb, nil, gc, 1, 10, 4); !errors.Is(err, ErrScreenCons) {
		t.Fatalf("nil con err = %v", err)
	}
	if err := s.DrawNotifyCached(fb, con, nil, 1, 10, 4); !errors.Is(err, ErrScreenChars) {
		t.Fatalf("nil gc err = %v", err)
	}
	// No notify rows (nothing printed) -> nil.
	if err := s.DrawNotifyCached(fb, con, gc, 1, 10, 4); err != nil {
		t.Fatalf("empty notify: %v", err)
	}

	// cols <= 0 path: print a line, then draw into a too-narrow fb.
	con.Linefeed(1.0)
	con.Print("x")
	narrowS, _ := NewScreen(CharWidth+2, 200)
	narrowFB := newTestFB(t, CharWidth+2, 200, 0)
	if err := narrowS.DrawNotifyCached(narrowFB, con, gc, 1, 10, 4); err != nil {
		t.Fatalf("narrow notify: %v", err)
	}

	// Per-glyph error surfaces (scale mismatch).
	fb2 := newTestFB(t, 320, 200, 0)
	fb2.HUDScale = 2
	if err := s.DrawNotifyCached(fb2, con, gc, 1, 10, 4); !errors.Is(err, ErrGlyphBadScale) {
		t.Fatalf("scale-mismatch notify err = %v want ErrGlyphBadScale", err)
	}
}

// ================= BENCHMARKS =====================================

// benchCharsSheet is a package-level fixture for the benchmarks so the
// sheet build cost stays out of the timed loop.
var benchCharsSheet = variedCharsSheet()

// benchConsoleText fills a Console with a dense screenful of glyphs.
func benchConsoleText(w, h int) *Console {
	con, _ := NewConsole(w, h)
	for r := 0; r < h; r++ {
		for c := 0; c < w; c++ {
			con.SetCell(c, r, byte('!'+((r*w+c)%94)))
		}
	}
	con.CurrentRow = h - 1
	return con
}

// benchmarkConsole runs a full-console-screen draw either through the
// per-glyph re-decoding free path or the cached tile path.
func benchmarkConsole(b *testing.B, scale int, cached bool) {
	chars := benchCharsSheet
	con := benchConsoleText(80, 25)
	// Fixed VIRTUAL size (640x400) at every scale so the glyph COUNT
	// is identical and the only variable is per-pixel work: physical
	// pixels per glyph grow as scale^2, which is exactly what the
	// cached tile path removes the per-pixel divide from.
	pw, ph := 640*scale, 400*scale
	fb, _ := NewFrameBuffer(pw, ph)
	fb.HUDScale = scale
	s, _ := NewScreen(pw, ph)
	// ConCurrent is a virtual coordinate; fill the virtual height so
	// every row lands on-screen regardless of scale.
	s.ConCurrent = fb.VHeight()
	var gc *GlyphCache
	if cached {
		gc, _ = NewGlyphCache(chars, scale)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if cached {
			if err := s.DrawConsoleCached(fb, con, gc); err != nil {
				b.Fatal(err)
			}
		} else {
			if err := s.DrawConsole(fb, con, chars); err != nil {
				b.Fatal(err)
			}
		}
	}
}

func BenchmarkConsoleScreenUncachedScale1(b *testing.B) { benchmarkConsole(b, 1, false) }
func BenchmarkConsoleScreenCachedScale1(b *testing.B)   { benchmarkConsole(b, 1, true) }
func BenchmarkConsoleScreenUncachedScale2(b *testing.B) { benchmarkConsole(b, 2, false) }
func BenchmarkConsoleScreenCachedScale2(b *testing.B)   { benchmarkConsole(b, 2, true) }
func BenchmarkConsoleScreenUncachedScale3(b *testing.B) { benchmarkConsole(b, 3, false) }
func BenchmarkConsoleScreenCachedScale3(b *testing.B)   { benchmarkConsole(b, 3, true) }

// benchmarkString draws a HUD-length string many times.
func benchmarkString(b *testing.B, scale int, cached bool) {
	chars := benchCharsSheet
	const s = "HEALTH 100  ARMOR 200  AMMO 999"
	fb, _ := NewFrameBuffer(640*scale, 64*scale)
	fb.HUDScale = scale
	var gc *GlyphCache
	if cached {
		gc, _ = NewGlyphCache(chars, scale)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if cached {
			if err := gc.DrawString(fb, 8, 8, s); err != nil {
				b.Fatal(err)
			}
		} else {
			if err := DrawString(fb, chars, 8, 8, s); err != nil {
				b.Fatal(err)
			}
		}
	}
}

func BenchmarkHUDStringUncachedScale1(b *testing.B) { benchmarkString(b, 1, false) }
func BenchmarkHUDStringCachedScale1(b *testing.B)   { benchmarkString(b, 1, true) }
func BenchmarkHUDStringUncachedScale2(b *testing.B) { benchmarkString(b, 2, false) }
func BenchmarkHUDStringCachedScale2(b *testing.B)   { benchmarkString(b, 2, true) }

// BenchmarkGlyphCacheBuild measures the one-time cache build cost so
// the amortisation (build once per video-mode change vs. save per
// frame) is visible.
func BenchmarkGlyphCacheBuild(b *testing.B) {
	chars := benchCharsSheet
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := NewGlyphCache(chars, 2); err != nil {
			b.Fatal(err)
		}
	}
}
