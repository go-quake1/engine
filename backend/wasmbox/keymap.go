// Copyright (c) 1996-1997 Id Software, Inc.
// Copyright (c) 2026 the go-quake1/engine authors.
// SPDX-License-Identifier: GPL-2.0-or-later

package wasmbox

import "github.com/go-quake1/engine/backend"

// MapDOMKey translates a DOM KeyboardEvent.code (or the synthetic
// "Mouse1" / "Mouse2" sentinel produced by mousedown/mouseup
// translation) into a backend.KeyCode. Unmapped values return ok=false
// and are dropped by Backend.PollInput.
//
// The wasmbox compositor forwards the original KeyboardEvent.code
// verbatim through the wire protocol, so this table mirrors
// backend/wasm/keymap.go exactly — keeping the two backends behaviorally
// indistinguishable for the engine's input layer.
func MapDOMKey(code string) (backend.KeyCode, bool) {
	switch code {
	case "Escape":
		return backend.KeyEscape, true
	case "Enter", "NumpadEnter":
		return backend.KeyEnter, true
	case "Space":
		return backend.KeySpace, true
	case "Tab":
		return backend.KeyTab, true
	case "KeyW":
		return backend.KeyW, true
	case "KeyA":
		return backend.KeyA, true
	case "KeyS":
		return backend.KeyS, true
	case "KeyD":
		return backend.KeyD, true
	case "ShiftLeft", "ShiftRight":
		return backend.KeyShift, true
	case "ControlLeft", "ControlRight":
		return backend.KeyCtrl, true
	case "ArrowUp":
		return backend.KeyUp, true
	case "ArrowDown":
		return backend.KeyDown, true
	case "ArrowLeft":
		return backend.KeyLeft, true
	case "ArrowRight":
		return backend.KeyRight, true
	case "Backquote":
		return backend.KeyTilde, true
	case "Digit1":
		return backend.Key1, true
	case "Digit2":
		return backend.Key2, true
	case "Digit3":
		return backend.Key3, true
	case "Digit4":
		return backend.Key4, true
	case "Digit5":
		return backend.Key5, true
	case "Digit6":
		return backend.Key6, true
	case "Digit7":
		return backend.Key7, true
	case "Digit8":
		return backend.Key8, true
	case "Mouse1":
		return backend.KeyMouse1, true
	case "Mouse2":
		return backend.KeyMouse2, true
	default:
		return 0, false
	}
}

// MouseButtonCode maps a DOM MouseEvent.button value to the synthetic
// code MapDOMKey understands. Returns "" for buttons the engine does
// not bind.
//
// Per the DOM spec:
//
//	0 = primary (usually left)
//	1 = auxiliary (usually wheel click)
//	2 = secondary (usually right)
//
// Kept in a pure-Go file so the lookup table is unit-testable on host
// platforms without a JS runtime.
func MouseButtonCode(btn int) string {
	switch btn {
	case 0:
		return "Mouse1"
	case 2:
		return "Mouse2"
	default:
		return ""
	}
}
