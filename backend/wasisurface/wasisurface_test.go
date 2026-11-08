// Copyright (c) 2026 the go-quake1/engine authors.
// SPDX-License-Identifier: GPL-2.0-or-later

package wasisurface

import (
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/go-quake1/engine/backend"
	"github.com/go-wasmcontainers/gpu/wasigpu"
)

func TestMapKeyTable(t *testing.T) {
	cases := []struct {
		label string
		want  backend.KeyCode
		ok    bool
	}{
		{"escape", backend.KeyEscape, true},
		{"enter", backend.KeyEnter, true},
		{"numpad-enter", backend.KeyEnter, true},
		{"space", backend.KeySpace, true},
		{"tab", backend.KeyTab, true},
		{"key-w", backend.KeyW, true},
		{"key-a", backend.KeyA, true},
		{"key-s", backend.KeyS, true},
		{"key-d", backend.KeyD, true},
		{"shift-left", backend.KeyShift, true},
		{"shift-right", backend.KeyShift, true},
		{"control-left", backend.KeyCtrl, true},
		{"control-right", backend.KeyCtrl, true},
		{"arrow-up", backend.KeyUp, true},
		{"arrow-down", backend.KeyDown, true},
		{"arrow-left", backend.KeyLeft, true},
		{"arrow-right", backend.KeyRight, true},
		{"backquote", backend.KeyTilde, true},
		{"digit1", backend.Key1, true},
		{"digit2", backend.Key2, true},
		{"digit3", backend.Key3, true},
		{"digit4", backend.Key4, true},
		{"digit5", backend.Key5, true},
		{"digit6", backend.Key6, true},
		{"digit7", backend.Key7, true},
		{"digit8", backend.Key8, true},
		{"", 0, false},
		{"key-q", 0, false},
		{"digit0", 0, false},
		{"f1", 0, false},
		{"backspace", 0, false},
		{"Escape", 0, false}, // DOM spelling is not a surface label
	}
	for _, c := range cases {
		got, ok := MapKey(c.label)
		if ok != c.ok || got != c.want {
			t.Errorf("MapKey(%q) = (%v, %v), want (%v, %v)", c.label, got, ok, c.want, c.ok)
		}
	}
}

type fakeKeys struct {
	batches [][]wasigpu.KeyEvent
	closed  bool
	err     error
}

func (f *fakeKeys) Poll() ([]wasigpu.KeyEvent, error) {
	if f.err != nil {
		return nil, f.err
	}
	if len(f.batches) == 0 {
		return nil, nil
	}
	b := f.batches[0]
	f.batches = f.batches[1:]
	return b, nil
}
func (f *fakeKeys) Closed() bool { return f.closed }

func TestPollInput(t *testing.T) {
	down := &fakeKeys{batches: [][]wasigpu.KeyEvent{
		{{Key: "key-w"}, {Key: "f5"}, {Key: ""}, {Key: "arrow-left"}},
	}}
	up := &fakeKeys{batches: [][]wasigpu.KeyEvent{{{Key: "key-w"}}}}
	b := New(320, 200, down, up)

	s, err := b.PollInput()
	if err != nil {
		t.Fatal(err)
	}
	if want := []backend.KeyCode{backend.KeyW, backend.KeyLeft}; !reflect.DeepEqual(s.KeysDown, want) {
		t.Errorf("KeysDown = %v, want %v", s.KeysDown, want)
	}
	if want := []backend.KeyCode{backend.KeyW}; !reflect.DeepEqual(s.KeysUp, want) {
		t.Errorf("KeysUp = %v, want %v", s.KeysUp, want)
	}
	if s.QuitRequested {
		t.Error("QuitRequested before the stream ended")
	}

	s, err = b.PollInput()
	if err != nil || len(s.KeysDown) != 0 || len(s.KeysUp) != 0 {
		t.Errorf("second poll = %+v, %v; want empty", s, err)
	}

	down.closed = true
	if s, _ = b.PollInput(); !s.QuitRequested {
		t.Error("QuitRequested not set after the key-down stream closed")
	}
}

func TestPollInputNilStreamsAndErrors(t *testing.T) {
	b := New(320, 200, nil, nil)
	if s, err := b.PollInput(); err != nil || s.QuitRequested || len(s.KeysDown) != 0 {
		t.Errorf("nil streams: %+v, %v", s, err)
	}
	boom := errors.New("boom")
	b = New(320, 200, &fakeKeys{err: boom}, nil)
	if _, err := b.PollInput(); !errors.Is(err, boom) {
		t.Errorf("err = %v, want boom", err)
	}
}

func TestBackendBasics(t *testing.T) {
	b := New(640, 400, nil, nil)
	if w, h := b.Size(); w != 640 || h != 400 {
		t.Errorf("Size = %dx%d", w, h)
	}
	if err := b.PresentFrame(nil, 640, 400); err != nil {
		t.Errorf("PresentFrame = %v, want nil (no-op)", err)
	}
	if b.SampleRate() != 22050 || b.QueueAudio(nil) != nil {
		t.Error("audio is not NoAudio")
	}
	t0 := b.start
	b.now = func() time.Time { return t0.Add(1500 * time.Millisecond) }
	if got := b.Now(); got != 1.5 {
		t.Errorf("Now = %v, want 1.5", got)
	}
}
