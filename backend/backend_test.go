// Copyright (c) 1996-1997 Id Software, Inc.
// Copyright (c) 2026 the go-quake1/engine authors.
// SPDX-License-Identifier: GPL-2.0-or-later

package backend

import (
	"errors"
	"testing"

	"github.com/go-quake1/engine/sound"
)

// fakeBackend implements every Backend method for the integration test.
type fakeBackend struct {
	PresentCalls int
	QueueCalls   int
	PollCalls    int
	NowReturn    float64
	LastFrame    []byte
	LastSamples  []sound.StereoSample
	NextInput    InputSnapshot
}

func (f *fakeBackend) PresentFrame(rgba []byte, w, h int) error {
	f.PresentCalls++
	f.LastFrame = rgba
	_ = w
	_ = h
	return nil
}
func (f *fakeBackend) Size() (int, int) { return 320, 200 }
func (f *fakeBackend) QueueAudio(s []sound.StereoSample) error {
	f.QueueCalls++
	f.LastSamples = s
	return nil
}
func (f *fakeBackend) SampleRate() int { return 22050 }
func (f *fakeBackend) PollInput() (InputSnapshot, error) {
	f.PollCalls++
	return f.NextInput, nil
}
func (f *fakeBackend) Now() float64 { return f.NowReturn }

// ----- Backend interface satisfaction -----------------------------

func TestFakeBackendImplementsBackend(t *testing.T) {
	var b Backend = &fakeBackend{}
	if b == nil {
		t.Fatalf("fakeBackend does not satisfy Backend")
	}
}

func TestBackend_PresentFrame(t *testing.T) {
	b := &fakeBackend{}
	frame := []byte{1, 2, 3, 4}
	if err := b.PresentFrame(frame, 1, 1); err != nil {
		t.Fatalf("PresentFrame: %v", err)
	}
	if b.PresentCalls != 1 {
		t.Fatalf("PresentCalls = %d want 1", b.PresentCalls)
	}
	if len(b.LastFrame) != 4 {
		t.Fatalf("LastFrame len = %d want 4", len(b.LastFrame))
	}
}

func TestBackend_Size(t *testing.T) {
	b := &fakeBackend{}
	w, h := b.Size()
	if w != 320 || h != 200 {
		t.Fatalf("Size = %dx%d want 320x200", w, h)
	}
}

func TestBackend_QueueAudio(t *testing.T) {
	b := &fakeBackend{}
	samples := []sound.StereoSample{{L: 100, R: -100}}
	if err := b.QueueAudio(samples); err != nil {
		t.Fatalf("QueueAudio: %v", err)
	}
	if b.QueueCalls != 1 {
		t.Fatalf("QueueCalls = %d want 1", b.QueueCalls)
	}
}

func TestBackend_SampleRate(t *testing.T) {
	b := &fakeBackend{}
	if r := b.SampleRate(); r != 22050 {
		t.Fatalf("SampleRate = %d want 22050", r)
	}
}

func TestBackend_PollInput(t *testing.T) {
	b := &fakeBackend{NextInput: InputSnapshot{
		KeysDown: []KeyCode{KeyW, KeySpace},
		MouseDX:  5,
		MouseDY:  -3,
	}}
	in, err := b.PollInput()
	if err != nil {
		t.Fatalf("PollInput: %v", err)
	}
	if len(in.KeysDown) != 2 {
		t.Fatalf("KeysDown = %v want 2 entries", in.KeysDown)
	}
	if in.MouseDX != 5 || in.MouseDY != -3 {
		t.Fatalf("mouse delta = (%v, %v) want (5, -3)", in.MouseDX, in.MouseDY)
	}
}

func TestBackend_Now(t *testing.T) {
	b := &fakeBackend{NowReturn: 1234.5}
	if n := b.Now(); n != 1234.5 {
		t.Fatalf("Now = %v want 1234.5", n)
	}
}

// ----- NoAudio / NoInput stubs ------------------------------------

func TestNoAudio_DropsSilently(t *testing.T) {
	var a Audio = NoAudio{}
	if err := a.QueueAudio([]sound.StereoSample{{L: 1, R: 1}}); err != nil {
		t.Fatalf("NoAudio QueueAudio: %v", err)
	}
	if r := a.SampleRate(); r != 22050 {
		t.Fatalf("NoAudio SampleRate = %d want 22050", r)
	}
}

func TestNoInput_AlwaysEmpty(t *testing.T) {
	var i Input = NoInput{}
	snap, err := i.PollInput()
	if err != nil {
		t.Fatalf("NoInput PollInput: %v", err)
	}
	if len(snap.KeysDown) != 0 || len(snap.KeysUp) != 0 {
		t.Fatalf("NoInput returned non-empty events")
	}
	if snap.MouseDX != 0 || snap.MouseDY != 0 {
		t.Fatalf("NoInput returned non-zero delta")
	}
}

// ----- ErrUnsupported sentinel ------------------------------------

func TestErrUnsupportedIsError(t *testing.T) {
	if !errors.Is(ErrUnsupported, ErrUnsupported) {
		t.Fatalf("ErrUnsupported isn't its own value")
	}
}

// ----- KeyCode constants drift detector ---------------------------

func TestKeyCodes(t *testing.T) {
	if KeyEscape != 1 {
		t.Fatalf("KeyEscape = %d want 1", KeyEscape)
	}
	if KeyMouse2 != 10 {
		t.Fatalf("KeyMouse2 = %d want 10", KeyMouse2)
	}
}

func TestImpulseForKey(t *testing.T) {
	// Key1..Key8 -> impulse 1..8 (weapon select).
	for i, k := range []KeyCode{Key1, Key2, Key3, Key4, Key5, Key6, Key7, Key8} {
		imp, ok := ImpulseForKey(k)
		if !ok || imp != uint8(i+1) {
			t.Errorf("ImpulseForKey(Key%d) = (%d, %v) want (%d, true)", i+1, imp, ok, i+1)
		}
	}
	// Non-number keys yield no impulse.
	for _, k := range []KeyCode{KeyEscape, KeyW, KeySpace, KeyTilde, KeyMouse1} {
		if imp, ok := ImpulseForKey(k); ok || imp != 0 {
			t.Errorf("ImpulseForKey(%v) = (%d, %v) want (0, false)", k, imp, ok)
		}
	}
}
