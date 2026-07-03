// Copyright (c) 1996-1997 Id Software, Inc.
// Copyright (c) 2026 the go-quake1/engine authors.
// SPDX-License-Identifier: GPL-2.0-or-later

package wasm

import (
	"errors"
	"fmt"
	"testing"

	"github.com/go-quake1/engine/backend"
	"github.com/go-quake1/engine/sound"
)

// ----- mocks -------------------------------------------------------

// fakeFB is an in-memory Framebuffer for the host-side tests.
type fakeFB struct {
	w, h       int
	present    int
	lastRGBA   []byte
	presentErr error
}

func newFakeFB(w, h int) *fakeFB { return &fakeFB{w: w, h: h} }

func (f *fakeFB) PresentRGBA(rgba []byte) error {
	f.present++
	if f.presentErr != nil {
		return f.presentErr
	}
	f.lastRGBA = append(f.lastRGBA[:0], rgba...)
	return nil
}
func (f *fakeFB) Width() int  { return f.w }
func (f *fakeFB) Height() int { return f.h }

// fakeInput hands out canned events.
type fakeInput struct {
	events []InputEvent
	err    error
	calls  int
}

func (f *fakeInput) PollEvents() ([]InputEvent, error) {
	f.calls++
	if f.err != nil {
		return nil, f.err
	}
	out := f.events
	f.events = nil
	return out, nil
}

// fakeAudio captures WritePCM calls + can be told to return an error.
type fakeAudio struct {
	rate     int
	lastIn   []sound.StereoSample
	writeErr error
	writes   int
}

func (f *fakeAudio) WritePCM(samples []sound.StereoSample) error {
	f.writes++
	f.lastIn = append(f.lastIn[:0], samples...)
	return f.writeErr
}
func (f *fakeAudio) SampleRate() int { return f.rate }

// ----- New ---------------------------------------------------------

func TestNew_nilFB(t *testing.T) {
	if _, err := New(nil, nil, nil, nil); !errors.Is(err, ErrWASMNilFB) {
		t.Fatalf("nil fb: got err %v, want ErrWASMNilFB", err)
	}
}

func TestNew_okMinimal(t *testing.T) {
	fb := newFakeFB(320, 240)
	b, err := New(fb, nil, nil, nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if b.FB != fb {
		t.Fatalf("FB field not assigned")
	}
}

// ----- PresentFrame -------------------------------------------------

func TestPresentFrame_dimensionMismatch(t *testing.T) {
	fb := newFakeFB(320, 240)
	b, _ := New(fb, nil, nil, nil)
	if err := b.PresentFrame(make([]byte, 320*240*4), 100, 100); !errors.Is(err, ErrWASMRGBASize) {
		t.Fatalf("wrong dims: got %v want ErrWASMRGBASize", err)
	}
}

func TestPresentFrame_byteLenMismatch(t *testing.T) {
	fb := newFakeFB(320, 240)
	b, _ := New(fb, nil, nil, nil)
	// Right dims advertised, wrong slice length.
	if err := b.PresentFrame(make([]byte, 10), 320, 240); !errors.Is(err, ErrWASMRGBASize) {
		t.Fatalf("wrong size: got %v want ErrWASMRGBASize", err)
	}
}

func TestPresentFrame_passthrough(t *testing.T) {
	fb := newFakeFB(4, 4)
	b, _ := New(fb, nil, nil, nil)
	rgba := make([]byte, 4*4*4)
	for i := range rgba {
		rgba[i] = byte(i)
	}
	if err := b.PresentFrame(rgba, 4, 4); err != nil {
		t.Fatalf("PresentFrame: %v", err)
	}
	if fb.present != 1 {
		t.Fatalf("present count: got %d want 1", fb.present)
	}
	for i, v := range fb.lastRGBA {
		if v != byte(i) {
			t.Fatalf("rgba[%d]: got %d want %d", i, v, byte(i))
		}
	}
}

func TestPresentFrame_propagatesError(t *testing.T) {
	want := errors.New("canvas busted")
	fb := newFakeFB(4, 4)
	fb.presentErr = want
	b, _ := New(fb, nil, nil, nil)
	if err := b.PresentFrame(make([]byte, 4*4*4), 4, 4); !errors.Is(err, want) {
		t.Fatalf("PresentFrame err: got %v want %v", err, want)
	}
}

// ----- Size --------------------------------------------------------

func TestSize(t *testing.T) {
	fb := newFakeFB(320, 240)
	b, _ := New(fb, nil, nil, nil)
	if w, h := b.Size(); w != 320 || h != 240 {
		t.Fatalf("Size: got %dx%d want 320x240", w, h)
	}
}

// ----- QueueAudio --------------------------------------------------

func TestQueueAudio_nilAudio(t *testing.T) {
	fb := newFakeFB(320, 240)
	b, _ := New(fb, nil, nil, nil)
	if err := b.QueueAudio([]sound.StereoSample{{L: 1, R: 2}}); err != nil {
		t.Fatalf("nil audio: %v", err)
	}
	if rate := b.SampleRate(); rate != 44100 {
		t.Fatalf("nil audio SampleRate: got %d want 44100", rate)
	}
}

func TestQueueAudio_ok(t *testing.T) {
	fb := newFakeFB(320, 240)
	au := &fakeAudio{rate: 48000}
	b, _ := New(fb, nil, au, nil)
	samples := []sound.StereoSample{{L: 1, R: 2}, {L: 3, R: 4}}
	if err := b.QueueAudio(samples); err != nil {
		t.Fatalf("QueueAudio: %v", err)
	}
	if au.writes != 1 {
		t.Fatalf("audio writes: got %d want 1", au.writes)
	}
	if b.SampleRate() != 48000 {
		t.Fatalf("SampleRate: got %d want 48000", b.SampleRate())
	}
}

func TestQueueAudio_swallowsErrUnsupported(t *testing.T) {
	fb := newFakeFB(320, 240)
	au := &fakeAudio{writeErr: fmt.Errorf("ctx not resumed: %w", backend.ErrUnsupported)}
	b, _ := New(fb, nil, au, nil)
	if err := b.QueueAudio([]sound.StereoSample{{}}); err != nil {
		t.Fatalf("QueueAudio swallowed ErrUnsupported: %v", err)
	}
}

func TestQueueAudio_propagatesOther(t *testing.T) {
	want := errors.New("audio buffer overrun")
	fb := newFakeFB(320, 240)
	au := &fakeAudio{writeErr: want}
	b, _ := New(fb, nil, au, nil)
	if err := b.QueueAudio([]sound.StereoSample{{}}); !errors.Is(err, want) {
		t.Fatalf("QueueAudio err: got %v want %v", err, want)
	}
}

// ----- PollInput ---------------------------------------------------

func TestPollInput_nilInput(t *testing.T) {
	fb := newFakeFB(320, 240)
	b, _ := New(fb, nil, nil, nil)
	snap, err := b.PollInput()
	if err != nil {
		t.Fatalf("PollInput: %v", err)
	}
	if len(snap.KeysDown) != 0 || len(snap.KeysUp) != 0 || snap.MouseDX != 0 || snap.MouseDY != 0 || snap.QuitRequested {
		t.Fatalf("nil input: expected zero snapshot, got %+v", snap)
	}
}

func TestPollInput_propagatesError(t *testing.T) {
	want := errors.New("ring drained")
	fb := newFakeFB(320, 240)
	in := &fakeInput{err: want}
	b, _ := New(fb, in, nil, nil)
	if _, err := b.PollInput(); !errors.Is(err, want) {
		t.Fatalf("PollInput err: got %v want %v", err, want)
	}
}

func TestPollInput_translatesEvents(t *testing.T) {
	fb := newFakeFB(320, 240)
	in := &fakeInput{events: []InputEvent{
		{Kind: EventKey, Code: "KeyW", Value: 1},
		{Kind: EventKey, Code: "KeyA", Value: 0},
		{Kind: EventKey, Code: "DoesNotExist", Value: 1}, // unmapped — dropped
		{Kind: EventKey, Code: "KeyS", Value: 2},         // not 0/1 — dropped
		{Kind: EventMouseDown, Code: "Mouse1"},
		{Kind: EventMouseDown, Code: "BogusBtn"}, // unmapped
		{Kind: EventMouseUp, Code: "Mouse1"},
		{Kind: EventMouseUp, Code: "BogusBtn"}, // unmapped
		{Kind: EventRelX, Value: 3},
		{Kind: EventRelY, Value: -2},
		{Kind: EventQuit},
	}}
	b, _ := New(fb, in, nil, nil)
	snap, err := b.PollInput()
	if err != nil {
		t.Fatalf("PollInput: %v", err)
	}
	wantDown := []backend.KeyCode{backend.KeyW, backend.KeyMouse1}
	wantUp := []backend.KeyCode{backend.KeyA, backend.KeyMouse1}
	if !kcEq(snap.KeysDown, wantDown) {
		t.Fatalf("KeysDown: got %v want %v", snap.KeysDown, wantDown)
	}
	if !kcEq(snap.KeysUp, wantUp) {
		t.Fatalf("KeysUp: got %v want %v", snap.KeysUp, wantUp)
	}
	if snap.MouseDX != 3 || snap.MouseDY != -2 {
		t.Fatalf("mouse: got dx=%v dy=%v want 3,-2", snap.MouseDX, snap.MouseDY)
	}
	if !snap.QuitRequested {
		t.Fatalf("QuitRequested: got false want true")
	}

	// Second poll should be drained.
	snap2, err := b.PollInput()
	if err != nil {
		t.Fatalf("PollInput 2: %v", err)
	}
	if len(snap2.KeysDown) != 0 || len(snap2.KeysUp) != 0 || snap2.MouseDX != 0 || snap2.MouseDY != 0 || snap2.QuitRequested {
		t.Fatalf("expected drained snap, got %+v", snap2)
	}
}

func kcEq(a, b []backend.KeyCode) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// ----- Now ---------------------------------------------------------

func TestNow_injectedClock(t *testing.T) {
	fb := newFakeFB(320, 240)
	calls := 0
	b, _ := New(fb, nil, nil, func() float64 {
		calls++
		return 42.0
	})
	if got := b.Now(); got != 42.0 {
		t.Fatalf("Now: got %v want 42.0", got)
	}
	if calls != 1 {
		t.Fatalf("clock calls: got %d want 1", calls)
	}
}

func TestNow_syntheticTick(t *testing.T) {
	fb := newFakeFB(320, 240)
	b, _ := New(fb, nil, nil, nil)
	if got := b.Now(); got != 0 {
		t.Fatalf("Now tick 0: got %v want 0", got)
	}
	if got := b.Now(); got != defaultClockStep {
		t.Fatalf("Now tick 1: got %v want %v", got, defaultClockStep)
	}
	if got := b.Now(); got != 2*defaultClockStep {
		t.Fatalf("Now tick 2: got %v want %v", got, 2*defaultClockStep)
	}
}
