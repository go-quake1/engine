// Copyright (c) 2026 the go-quake1/engine authors.
// SPDX-License-Identifier: GPL-2.0-or-later

package render

import (
	"bytes"
	"errors"
	"testing"
)

func TestParseKind(t *testing.T) {
	for in, want := range map[string]Kind{"": KindSoft, "soft": KindSoft, "gpu": KindGPU} {
		got, err := ParseKind(in)
		if err != nil || got != want {
			t.Errorf("ParseKind(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	if _, err := ParseKind("vulkan"); !errors.Is(err, ErrUnknownKind) {
		t.Errorf("ParseKind(vulkan) err = %v, want ErrUnknownKind", err)
	}
}

type fakeGPU struct{ Software }

func (*fakeGPU) Kind() Kind { return KindGPU }

func TestChoose(t *testing.T) {
	soft := &Software{}
	if _, err := Choose(KindSoft, nil, nil); !errors.Is(err, ErrNilSoftRenderer) {
		t.Fatalf("nil soft: err = %v", err)
	}
	if r, err := Choose(KindSoft, soft, nil); r != soft || err != nil {
		t.Fatalf("soft: %v, %v", r, err)
	}
	if r, err := Choose(KindGPU, soft, nil); r != soft || !errors.Is(err, ErrGPUNotImplemented) {
		t.Fatalf("gpu, no factory: %v, %v", r, err)
	}
	boom := errors.New("no adapter")
	if r, err := Choose(KindGPU, soft, func() (Renderer, error) { return nil, boom }); r != soft || !errors.Is(err, boom) {
		t.Fatalf("gpu, factory error: %v, %v", r, err)
	}
	if r, err := Choose(KindGPU, soft, func() (Renderer, error) { return nil, nil }); r != soft || !errors.Is(err, ErrGPUNotImplemented) {
		t.Fatalf("gpu, nil renderer: %v, %v", r, err)
	}
	g := &fakeGPU{}
	if r, err := Choose(KindGPU, soft, func() (Renderer, error) { return g, nil }); r != g || err != nil {
		t.Fatalf("gpu: %v, %v", r, err)
	}
}

func TestSoftwareScene(t *testing.T) {
	s := &Software{}
	if s.Kind() != KindSoft || s.HasScene() {
		t.Fatalf("empty Software: kind %q scene %v", s.Kind(), s.HasScene())
	}
	fb, _ := NewFrameBuffer(4, 4)
	if err := s.DrawScene(fb, [3]float32{}, [3]float32{}); err != nil {
		t.Fatalf("nil scene: %v", err)
	}
	calls := 0
	s.Scene = func(f *FrameBuffer, o, a [3]float32) error {
		calls++
		if f != fb || o != [3]float32{1, 2, 3} || a != [3]float32{4, 5, 6} {
			t.Errorf("scene args %p %v %v", f, o, a)
		}
		return nil
	}
	if !s.HasScene() {
		t.Fatal("HasScene false with a scene")
	}
	if err := s.DrawScene(fb, [3]float32{1, 2, 3}, [3]float32{4, 5, 6}); err != nil || calls != 1 {
		t.Fatalf("scene: %v calls %d", err, calls)
	}
}

// TestSoftwareMatchesExpandFrame pins the no-behaviour-change claim:
// Compose then Resolve produce the same bytes as ExpandFrame.
func TestSoftwareMatchesExpandFrame(t *testing.T) {
	fbA, ctx := newComposeCtx(t)
	ctx.Console.Print("hello seam\n")
	var pal Palette
	for i := range pal {
		pal[i] = [3]byte{byte(i), byte(255 - i), byte(i * 7)}
	}
	ctx.Palette = &pal
	n := fbA.Width * fbA.Height * 4
	want := make([]byte, n)
	if err := ExpandFrame(fbA, want, ctx); err != nil {
		t.Fatal(err)
	}
	got := make([]byte, n)
	fbB, _ := NewFrameBuffer(fbA.Width, fbA.Height)
	s := &Software{}
	if err := s.Compose(fbB, ctx); err != nil {
		t.Fatal(err)
	}
	if err := s.Resolve(fbB, got, ctx.Palette); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatal("Compose+Resolve differs from ExpandFrame")
	}
	if err := s.Resolve(fbB, got, nil); !errors.Is(err, ErrFBPaletteShape) {
		t.Fatalf("nil palette: %v", err)
	}
}
