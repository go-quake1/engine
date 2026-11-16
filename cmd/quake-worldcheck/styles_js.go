// Copyright (c) 2026 the go-quake1/engine authors.
// SPDX-License-Identifier: GPL-2.0-or-later

//go:build js && wasm

package main

import "github.com/go-quake1/engine/client"

// serverStyles is d_lightstylevalue at time t for light styles a server set
// with its own strings (svc_lightstyle, reference/NQ/cl_parse.c:1053-1061), through
// the engine's client.State.LightStyleValue (R_AnimateLight): not the default
// table of worldview.LightStyleValue. The strings of styles 1 to 11 are different
// from the default ones, so that a renderer that kept the default table
// draws other surfaces; the others stay empty (256).
func serverStyles(t float32) func(byte) int {
	cs := client.NewState()
	for i, m := range []string{
		1: "a", 2: "z", 3: "a", 4: "z", 5: "a", 6: "z", 7: "azazazaz", 8: "zazazaza", 9: "a", 10: "z", 11: "a",
	} {
		cs.LightStyles[i].Anim = []byte(m)
	}
	cs.HaveLightStyles = true
	return func(s byte) int { return cs.LightStyleValue(s, t) }
}
