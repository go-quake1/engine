// Copyright (c) 2026 the go-quake1/engine authors.
// SPDX-License-Identifier: GPL-2.0-or-later

//go:build js && wasm

package main

import (
	"math"

	"github.com/go-quake1/engine/render"
	"github.com/go-quake1/engine/worldview"
)

// dlightScenario is the dynamic lights of a player firing a rocket launcher, as
// CL_RelinkEntities and CL_ParseTEnt make them (reference/NQ/cl_main.c,
// cl_tent.c), around view v at time 0: the muzzle flash (200 + 20 units, minlight
// 32, 16 up and 18 forward of the view), the rocket's light (200, 160 forward)
// and its explosion (350, decaying 300 a second, 320 forward and 40 up). Slots
// 0 to 2; the others are free.
func dlightScenario(v worldview.View) [render.MaxDLights]render.DLight {
	yaw := float64(v.Angles[1]) * math.Pi / 180
	fx, fy := float32(math.Cos(yaw)), float32(math.Sin(yaw))
	at := func(fwd, up float32) [3]float32 {
		return [3]float32{v.Origin[0] + fwd*fx, v.Origin[1] + fwd*fy, v.Origin[2] + up}
	}
	var l [render.MaxDLights]render.DLight
	l[0] = render.DLight{Origin: at(18, 16), Radius: 220, MinLight: 32, Die: 0.1}
	l[1] = render.DLight{Origin: at(160, 0), Radius: 200, Die: 0.01}
	l[2] = render.DLight{Origin: at(320, 40), Radius: 350, Die: 0.5, Decay: 300}
	return l
}
