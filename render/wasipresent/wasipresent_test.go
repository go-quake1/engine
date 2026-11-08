// Copyright (c) 2026 the go-quake1/engine authors.
// SPDX-License-Identifier: GPL-2.0-or-later

package wasipresent

import (
	"bytes"
	"encoding/binary"
	"errors"
	"math/rand"
	"strings"
	"testing"

	"github.com/go-gfx/webgpu/batch"

	"github.com/go-quake1/engine/render"
	"github.com/go-quake1/engine/render/webgpupresent"
)

func randomPalette(r *rand.Rand) *render.Palette {
	var pal render.Palette
	for i := range pal {
		pal[i] = [3]byte{byte(r.Intn(256)), byte(r.Intn(256)), byte(r.Intn(256))}
	}
	return &pal
}

func randomFrame(r *rand.Rand, pitch, height int) []byte {
	px := make([]byte, pitch*height)
	for i := range px {
		px[i] = byte(r.Intn(256))
	}
	return px
}

func words(b []byte) []uint32 {
	w := make([]uint32, len(b)/4)
	for i := range w {
		w[i] = binary.LittleEndian.Uint32(b[4*i:])
	}
	return w
}

// TestEmulateEqualsReference holds the shader's arithmetic, run in Go over
// the buffers the guest uploads, equal to webgpupresent.Reference for 1:1,
// integer upscales and other ratios, with a pitch wider than the width and
// a frame whose size is not a multiple of four.
func TestEmulateEqualsReference(t *testing.T) {
	r := rand.New(rand.NewSource(1))
	for _, c := range []struct{ w, h, pitch, ow, oh int }{
		{320, 200, 320, 320, 200},
		{640, 400, 640, 640, 400},
		{320, 200, 320, 640, 400},
		{7, 5, 7, 7, 5},
		{7, 5, 9, 7, 5},
		{13, 9, 16, 29, 17},
		{64, 48, 64, 33, 20},
		{1, 1, 1, 5, 5},
	} {
		pal := randomPalette(r)
		px := randomFrame(r, c.pitch, c.h)
		want, ok := webgpupresent.Reference(px, c.w, c.h, c.pitch, pal, c.ow, c.oh)
		if !ok {
			t.Fatalf("%+v: Reference refused the frame", c)
		}
		packed := PackIndices(nil, px, c.pitch, c.h)
		got := Emulate(words(packed), PaletteWords(pal), c.w, c.h, c.pitch, c.ow, c.oh)
		if !bytes.Equal(got, want) {
			t.Errorf("%+v: Emulate differs from Reference", c)
		}
	}
}

func TestPackIndices(t *testing.T) {
	px := []byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10}
	// 3 columns of pitch 5 and 2 rows: the last row is shorter than a pitch.
	got := PackIndices(nil, px[:8], 5, 2)
	if len(got) != IndexWords(5, 2)*4 || len(got) != 12 {
		t.Fatalf("len = %d", len(got))
	}
	if !bytes.Equal(got, []byte{1, 2, 3, 4, 5, 6, 7, 8, 0, 0, 0, 0}) {
		t.Errorf("packed = %v", got)
	}
	// dst with room is reused and stale bytes are cleared.
	dst := bytes.Repeat([]byte{0xff}, 64)
	got = PackIndices(dst, px[:8], 5, 2)
	if &got[0] != &dst[0] || !bytes.Equal(got, []byte{1, 2, 3, 4, 5, 6, 7, 8, 0, 0, 0, 0}) {
		t.Errorf("reuse: %v", got)
	}
	if w := words(got); w[0] != 0x04030201 {
		t.Errorf("word 0 = %#x, not little endian", w[0])
	}
}

func TestPaletteWords(t *testing.T) {
	var pal render.Palette
	pal[0] = [3]byte{1, 2, 3}
	pal[255] = [3]byte{255, 128, 0}
	w := PaletteWords(&pal)
	if len(w) != 256 || w[0] != 0xff030201 || w[255] != 0xff0080ff {
		t.Errorf("words = %#x ... %#x (%d)", w[0], w[255], len(w))
	}
}

// TestShaderBoundedByHost runs the host's own loop-budget parser over the
// shader: it must parse and, having no loop, come back unchanged. A loop in
// a fragment stage would be refused at create-shader-module.
func TestShaderBoundedByHost(t *testing.T) {
	src := Shader(640, 400, 640, 1280, 800)
	// Group 3 is the playground host's reserved group (webgpu Browser.loopBudget).
	out, rewritten, err := batch.LoopBudget{Work: 1 << 36, Group: 3}.Instrument(src)
	if err != nil {
		t.Fatalf("host parser refused the shader: %v", err)
	}
	if rewritten || out != src {
		t.Error("shader was rewritten: it contains a loop")
	}
	for _, want := range []string{"640u / 2560u", "400u / 1600u", "sy * 640u + sx"} {
		if !strings.Contains(src, want) {
			t.Errorf("shader lacks %q:\n%s", want, src)
		}
	}
	if strings.Contains(src, "for") || strings.Contains(src, "while") || strings.Contains(src, "loop") {
		t.Error("shader has a loop keyword")
	}
}

type fakeDriver struct {
	pipelines []string
	buffers   []int
	indices   [][]byte
	palettes  [][]byte
	draws     int
	closed    bool
	fail      map[string]error
}

func (f *fakeDriver) Pipeline(w string) error {
	if err := f.fail["pipeline"]; err != nil {
		return err
	}
	f.pipelines = append(f.pipelines, w)
	return nil
}
func (f *fakeDriver) Buffers(n int) error {
	if err := f.fail["buffers"]; err != nil {
		return err
	}
	f.buffers = append(f.buffers, n)
	return nil
}
func (f *fakeDriver) WriteIndices(b []byte) error {
	if err := f.fail["indices"]; err != nil {
		return err
	}
	f.indices = append(f.indices, append([]byte(nil), b...))
	return nil
}
func (f *fakeDriver) WritePalette(b []byte) error {
	if err := f.fail["palette"]; err != nil {
		return err
	}
	f.palettes = append(f.palettes, append([]byte(nil), b...))
	return nil
}
func (f *fakeDriver) Draw() error {
	if err := f.fail["draw"]; err != nil {
		return err
	}
	f.draws++
	return nil
}
func (f *fakeDriver) Close() { f.closed = true }

func TestNew(t *testing.T) {
	if _, err := New(nil, 10, 10); err == nil {
		t.Error("nil driver accepted")
	}
	if _, err := New(&fakeDriver{}, 0, 10); err == nil {
		t.Error("zero width accepted")
	}
	if _, err := New(&fakeDriver{}, 10, 0); err == nil {
		t.Error("zero height accepted")
	}
}

func TestPresentLifecycle(t *testing.T) {
	r := rand.New(rand.NewSource(2))
	drv := &fakeDriver{}
	p, err := New(drv, 64, 40)
	if err != nil {
		t.Fatal(err)
	}
	pal := randomPalette(r)
	px := randomFrame(r, 32, 20)

	if err := p.Present(px, 32, 20, 32, pal); err != nil {
		t.Fatal(err)
	}
	if len(drv.pipelines) != 1 || len(drv.buffers) != 1 || drv.buffers[0] != 640 || drv.draws != 1 ||
		len(drv.palettes) != 1 || len(drv.indices) != 1 || len(drv.indices[0]) != 640 {
		t.Fatalf("first frame: %+v", drv)
	}
	if drv.pipelines[0] != Shader(32, 20, 32, 64, 40) {
		t.Error("pipeline is not the shader of the geometry")
	}

	// Same geometry, same palette: one index write and one draw.
	px[0] ^= 0xff
	if err := p.Present(px, 32, 20, 32, pal); err != nil {
		t.Fatal(err)
	}
	if len(drv.pipelines) != 1 || len(drv.buffers) != 1 || len(drv.palettes) != 1 || len(drv.indices) != 2 || drv.draws != 2 {
		t.Fatalf("second frame rebuilt something: %+v", drv)
	}
	if drv.indices[1][0] != px[0] {
		t.Error("second frame's indices not uploaded")
	}

	// A palette change is written again.
	pal[3][0] ^= 1
	if err := p.Present(px, 32, 20, 32, pal); err != nil {
		t.Fatal(err)
	}
	if len(drv.palettes) != 2 || len(drv.pipelines) != 1 {
		t.Fatalf("palette change: %+v", drv)
	}

	// A new geometry rebuilds the pipeline and the buffers, and rewrites
	// the palette into the new buffer.
	px2 := randomFrame(r, 48, 24)
	if err := p.Present(px2, 40, 24, 48, pal); err != nil {
		t.Fatal(err)
	}
	if len(drv.pipelines) != 2 || len(drv.buffers) != 2 || drv.buffers[1] != 48*24 || len(drv.palettes) != 3 {
		t.Fatalf("new geometry: %+v", drv)
	}

	p.Close()
	if !drv.closed {
		t.Error("Close did not reach the driver")
	}
}

func TestPresentRejectsBadFrames(t *testing.T) {
	p, _ := New(&fakeDriver{}, 8, 8)
	var pal render.Palette
	px := make([]byte, 64)
	for name, err := range map[string]error{
		"zero width":  p.Present(px, 0, 8, 8, &pal),
		"zero height": p.Present(px, 8, 0, 8, &pal),
		"short pitch": p.Present(px, 8, 8, 7, &pal),
		"nil palette": p.Present(px, 8, 8, 8, nil),
		"short frame": p.Present(px[:60], 8, 8, 8, &pal),
	} {
		if !errors.Is(err, ErrBadFrame) {
			t.Errorf("%s: err = %v, want ErrBadFrame", name, err)
		}
	}
}

func TestPresentPropagatesDriverErrors(t *testing.T) {
	boom := errors.New("boom")
	var pal render.Palette
	px := make([]byte, 64)
	for _, stage := range []string{"pipeline", "buffers", "palette", "indices", "draw"} {
		p, _ := New(&fakeDriver{fail: map[string]error{stage: boom}}, 8, 8)
		if err := p.Present(px, 8, 8, 8, &pal); !errors.Is(err, boom) {
			t.Errorf("%s: err = %v, want boom", stage, err)
		}
	}
}

// TestRendererPresents: the presenter plugs into render.PresentRenderer.
func TestRendererPresents(t *testing.T) {
	drv := &fakeDriver{}
	p, _ := New(drv, 8, 8)
	rend, err := render.NewPresentRenderer(nil, p)
	if err != nil {
		t.Fatal(err)
	}
	fb, err := render.NewFrameBuffer(8, 8)
	if err != nil {
		t.Fatal(err)
	}
	var pal render.Palette
	if err := rend.Resolve(fb, nil, &pal); err != nil || drv.draws != 1 {
		t.Fatalf("Resolve: %v, draws %d", err, drv.draws)
	}
}
