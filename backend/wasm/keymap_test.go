// Copyright (c) 1996-1997 Id Software, Inc.
// Copyright (c) 2026 the go-quake1/engine authors.
// SPDX-License-Identifier: GPL-2.0-or-later

package wasm

import (
	"testing"

	"github.com/go-quake1/engine/backend"
)

func TestMapDOMKey_table(t *testing.T) {
	cases := []struct {
		code string
		want backend.KeyCode
		ok   bool
	}{
		{"Escape", backend.KeyEscape, true},
		{"Enter", backend.KeyEnter, true},
		{"NumpadEnter", backend.KeyEnter, true},
		{"Space", backend.KeySpace, true},
		{"Tab", backend.KeyTab, true},
		{"KeyW", backend.KeyW, true},
		{"KeyA", backend.KeyA, true},
		{"KeyS", backend.KeyS, true},
		{"KeyD", backend.KeyD, true},
		{"ShiftLeft", backend.KeyShift, true},
		{"ShiftRight", backend.KeyShift, true},
		{"ControlLeft", backend.KeyCtrl, true},
		{"ControlRight", backend.KeyCtrl, true},
		{"ArrowUp", backend.KeyUp, true},
		{"ArrowDown", backend.KeyDown, true},
		{"ArrowLeft", backend.KeyLeft, true},
		{"ArrowRight", backend.KeyRight, true},
		{"Backquote", backend.KeyTilde, true},
		{"Digit1", backend.Key1, true},
		{"Digit2", backend.Key2, true},
		{"Digit3", backend.Key3, true},
		{"Digit4", backend.Key4, true},
		{"Digit5", backend.Key5, true},
		{"Digit6", backend.Key6, true},
		{"Digit7", backend.Key7, true},
		{"Digit8", backend.Key8, true},
		{"Mouse1", backend.KeyMouse1, true},
		{"Mouse2", backend.KeyMouse2, true},
		{"", 0, false},
		{"KeyQ", 0, false},
		{"F1", 0, false},
		{"Backspace", 0, false},
	}
	for _, c := range cases {
		got, ok := MapDOMKey(c.code)
		if ok != c.ok || got != c.want {
			t.Errorf("MapDOMKey(%q): got (%v, %v) want (%v, %v)", c.code, got, ok, c.want, c.ok)
		}
	}
}

func TestMouseButtonCode(t *testing.T) {
	cases := []struct {
		btn  int
		want string
	}{
		{0, "Mouse1"},
		{1, ""},
		{2, "Mouse2"},
		{3, ""},
		{-1, ""},
	}
	for _, c := range cases {
		if got := mouseButtonCode(c.btn); got != c.want {
			t.Errorf("mouseButtonCode(%d): got %q want %q", c.btn, got, c.want)
		}
	}
}
