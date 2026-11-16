// Copyright (c) 1996-1997 Id Software, Inc.
// Copyright (c) 2026 the go-quake1/engine authors.
// SPDX-License-Identifier: GPL-2.0-or-later

package runner

import (
	"math"

	"github.com/go-quake1/engine/client"
	"github.com/go-quake1/engine/render"
)

// The entity effect bits of the update message (reference/common/include/model.h
// ENT_EF_*) that make a dynamic light.
const (
	efMuzzleFlash = 1 << 1
	efBrightLight = 1 << 2
	efDimLight    = 1 << 3
	efBlue        = 1 << 6
	efRed         = 1 << 7
)

// allocDLight is CL_AllocDlight (reference/NQ/cl_main.c): the slot of the entity
// key if there is one (key != 0), else the first light that has died (die <
// now), else slot 0; the slot is cleared and keyed.
func allocDLight(lights *[client.MaxDLights]client.DLight, key int, now float32) *client.DLight {
	if key != 0 {
		for i := range lights {
			if lights[i].Key == key {
				lights[i] = client.DLight{Key: key}
				return &lights[i]
			}
		}
	}
	for i := range lights {
		if lights[i].Die < now {
			lights[i] = client.DLight{Key: key}
			return &lights[i]
		}
	}
	lights[0] = client.DLight{Key: key}
	return &lights[0]
}

// decayDLights is CL_DecayLights: a light that has not died shrinks by decay
// times the time since the last frame, to nothing at most.
func decayDLights(lights *[client.MaxDLights]client.DLight, now, dt float32) {
	for i := range lights {
		l := &lights[i]
		if l.Die < now || l.Radius == 0 {
			continue
		}
		l.Radius -= dt * l.Decay
		if l.Radius < 0 {
			l.Radius = 0
		}
	}
}

// entityDLights is the dynamic lights CL_RelinkEntities makes for entity i
// (reference/NQ/cl_main.c): the muzzle flash 18 units in front of the entity and
// 16 up (200 + rand & 31 units, minlight 32, 0.1 s), the bright light (400 + rand
// & 31, 16 up) and dim light (200 + rand & 31), the red and blue lights (200 +
// rand & 31), and the rocket's light (200 units for 0.01 s, flag MOD_EF_ROCKET).
// rnd is rand() & 31.
func entityDLights(lights *[client.MaxDLights]client.DLight, i int, es client.EntityState, rocket bool, now float32, rnd func() int) {
	if es.Effects&efMuzzleFlash != 0 {
		l := allocDLight(lights, i, now)
		l.Origin = es.Origin
		l.Origin[2] += 16
		fv := angleForward(es.Angles)
		for k := range l.Origin {
			l.Origin[k] += 18 * fv[k]
		}
		l.Radius = float32(200 + rnd())
		l.MinLight = 32
		l.Die = now + 0.1
	}
	if es.Effects&efBrightLight != 0 {
		l := allocDLight(lights, i, now)
		l.Origin = es.Origin
		l.Origin[2] += 16
		l.Radius = float32(400 + rnd())
		l.Die = now + 0.001
	}
	if es.Effects&efDimLight != 0 {
		l := allocDLight(lights, i, now)
		l.Origin = es.Origin
		l.Radius = float32(200 + rnd())
		l.Die = now + 0.001
	}
	if es.Effects&(efRed|efBlue) != 0 { // red, blue or both: the colour is not used by the software renderer
		l := allocDLight(lights, i, now)
		l.Origin = es.Origin
		l.Radius = float32(200 + rnd())
		l.Die = now + 0.001
	}
	if rocket {
		l := allocDLight(lights, i, now)
		l.Origin = es.Origin
		l.Radius = 200
		l.Die = now + 0.01
	}
}

// explosionDLight is the light of TE_EXPLOSION and TE_EXPLOSION2
// (reference/NQ/cl_tent.c): 350 units at the point, 0.5 s, decaying 300 units a
// second.
func explosionDLight(lights *[client.MaxDLights]client.DLight, origin [3]float32, now float32) {
	l := allocDLight(lights, 0, now)
	l.Origin = origin
	l.Radius = 350
	l.Die = now + 0.5
	l.Decay = 300
}

// aliasDLights is the dynamic-light term of R_AliasSetupLighting for an entity
// at origin: the levels (radius - distance, when positive) of the lights that
// have not died ([render.AliasDLightLevels]), appended to buf.
func aliasDLights(buf []int, origin [3]float32, lights *[client.MaxDLights]client.DLight, now float32) []int {
	for i := range lights {
		l := &lights[i]
		if l.Die < now {
			continue
		}
		if level := render.AliasDLightLevel(origin, l.Origin, l.Radius); level > 0 {
			buf = append(buf, level)
		}
	}
	return buf
}

// angleForward is the forward vector of AngleVectors(angles): pitch, yaw, roll
// in degrees.
func angleForward(a [3]float32) [3]float32 {
	pitch, yaw := float64(a[0])*math.Pi/180, float64(a[1])*math.Pi/180
	sp, cp := math.Sincos(pitch)
	sy, cy := math.Sincos(yaw)
	return [3]float32{float32(cp * cy), float32(cp * sy), float32(-sp)}
}
