// Copyright (c) 1996-1997 Id Software, Inc.
// Copyright (c) 2026 the go-quake1/engine authors.
// SPDX-License-Identifier: GPL-2.0-or-later

package wasm

import "github.com/go-quake1/engine/backend"

// MapDOMKey translates a DOM KeyboardEvent.code (or the synthetic
// "Mouse1" / "Mouse2" sentinel produced by mousedown/mouseup) into a
// backend.KeyCode. Unmapped values return ok=false and are dropped
// by Backend.PollInput.
//
// Reference: https://developer.mozilla.org/docs/Web/API/UI_Events/Keyboard_event_code_values
// (the W3C UI Events KeyboardEvent code values registry).
//
// Notes:
//
//   - The mapping uses event.code (physical key, layout-independent),
//     NOT event.key (the layout-dependent character). This matches
//     Quake's "WASD-as-positions" expectation on every keyboard.
//   - Both left and right modifier variants (ShiftLeft / ShiftRight,
//     ControlLeft / ControlRight) map to the same backend KeyCode.
//   - Both Enter and NumpadEnter map to KeyEnter (id Software's
//     original code didn't distinguish them either).
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
