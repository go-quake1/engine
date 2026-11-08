// Copyright (c) 1996-1997 Id Software, Inc.
// Copyright (c) 2026 the go-quake1/engine authors.
// SPDX-License-Identifier: GPL-2.0-or-later

package wasisurface

import "github.com/go-quake1/engine/backend"

// MapKey translates a wasi-gfx:surface key label (the UI Events
// KeyboardEvent.code names in kebab-case: "key-w", "arrow-up", "digit1",
// "shift-left") into a backend.KeyCode. Unmapped labels, and "" for a key
// the host could not identify, return ok=false and are dropped by
// Backend.PollInput.
//
// The table mirrors backend/wasmbox.MapDOMKey, label for label, so the two
// backends feed the engine the same keys. The surface carries no pointer
// button in a pointer event, so there is no Mouse1 / Mouse2 here.
func MapKey(label string) (backend.KeyCode, bool) {
	switch label {
	case "escape":
		return backend.KeyEscape, true
	case "enter", "numpad-enter":
		return backend.KeyEnter, true
	case "space":
		return backend.KeySpace, true
	case "tab":
		return backend.KeyTab, true
	case "key-w":
		return backend.KeyW, true
	case "key-a":
		return backend.KeyA, true
	case "key-s":
		return backend.KeyS, true
	case "key-d":
		return backend.KeyD, true
	case "shift-left", "shift-right":
		return backend.KeyShift, true
	case "control-left", "control-right":
		return backend.KeyCtrl, true
	case "arrow-up":
		return backend.KeyUp, true
	case "arrow-down":
		return backend.KeyDown, true
	case "arrow-left":
		return backend.KeyLeft, true
	case "arrow-right":
		return backend.KeyRight, true
	case "backquote":
		return backend.KeyTilde, true
	case "digit1":
		return backend.Key1, true
	case "digit2":
		return backend.Key2, true
	case "digit3":
		return backend.Key3, true
	case "digit4":
		return backend.Key4, true
	case "digit5":
		return backend.Key5, true
	case "digit6":
		return backend.Key6, true
	case "digit7":
		return backend.Key7, true
	case "digit8":
		return backend.Key8, true
	default:
		return 0, false
	}
}
