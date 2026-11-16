// Copyright (c) 2026 the go-quake1/engine authors.
// SPDX-License-Identifier: GPL-2.0-or-later

package main

import (
	"bytes"
	"testing"

	"github.com/go-quake1/engine/client"
	"github.com/go-quake1/engine/protocol"
	"github.com/go-quake1/engine/runloop"
)

// TestExplosionDLights: CL_ParseTEnt (reference/NQ/cl_tent.c, TE_EXPLOSION at
// :245 and TE_EXPLOSION2 at :260-271) makes a dynamic light of radius 350 at the
// point, dying 0.5 s later and decaying 300 units a second; TE_TAREXPLOSION makes
// none. Through the runner's own sink, on the real map. (Needs QUAKE_PAK.)
func TestExplosionDLights(t *testing.T) {
	for _, c := range []struct {
		name  string
		kind  int
		light bool
	}{
		{"TE_EXPLOSION", protocol.TEExplosion, true},
		{"TE_EXPLOSION2", protocol.TEExplosion2, true},
		{"TE_TAREXPLOSION", protocol.TETarExplosion, false},
		{"TE_GUNSHOT", protocol.TEGunshot, false},
	} {
		var got [client.MaxDLights]client.DLight
		var msgTime float32
		old := testRunner
		testRunner = func(r *runloop.Runner) {
			r.Client.EmitTempEntity(c.kind, [3]float32{1, 2, 3})
			got, msgTime = r.Client.DLights, r.Client.MsgTime
		}
		closes := 0
		withSurface(t, &fakePresenter{}, false, &closes)
		var out, errOut bytes.Buffer
		err := runGame(&out, &errOut, realFS(t), "e1m1", 64, 40, 1)
		testRunner = old
		if err != nil {
			t.Fatalf("%s: %v\n%s", c.name, err, errOut.String())
		}
		n := 0
		for _, l := range got {
			if l.Radius == 0 {
				continue
			}
			n++
			if l.Origin != [3]float32{1, 2, 3} || l.Radius != 350 || l.Decay != 300 || l.Die != msgTime+0.5 || l.Key != 0 {
				t.Errorf("%s: light %+v, want radius 350 at (1,2,3) decay 300 dying at %v", c.name, l, msgTime+0.5)
			}
		}
		if (n == 1) != c.light || n > 1 {
			t.Errorf("%s: %d lights, want light=%v", c.name, n, c.light)
		}
	}
}
