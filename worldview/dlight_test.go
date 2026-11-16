// Copyright (c) 2026 the go-quake1/engine authors.
// SPDX-License-Identifier: GPL-2.0-or-later

package worldview_test

import (
	"bytes"
	"testing"

	"github.com/go-quake1/engine/render"
)

// TestDynamicLightMarksFacesItReaches (needs QUAKE_PAK): R_PushDlights marks
// with the light's bit only faces on planes the light's sphere reaches (a face
// is on its node's plane, so |distance| <= radius), none for a dead light or one
// without a radius, and the surfaces it marks build another light grid.
func TestDynamicLightMarksFacesItReaches(t *testing.T) {
	w, cm := load(t)
	view := w.Views()[0]
	light := render.DLight{Origin: [3]float32{view.Origin[0] + 18, view.Origin[1], view.Origin[2] + 16}, Radius: 220, MinLight: 32, Die: 0.1}
	marked := func() (n int, bad int, wrongBits int) {
		for _, f := range w.Faces {
			if f == nil || f.Surf.Desc.DLightFrame != w.LightFrame() {
				continue
			}
			n++
			d := &f.Surf.Desc
			if d.DLightBits != 1 {
				wrongBits++
			}
			dist := light.Origin[0]*d.Plane.Normal[0] + light.Origin[1]*d.Plane.Normal[1] + light.Origin[2]*d.Plane.Normal[2] - d.Plane.Dist
			if dist < -light.Radius-1 || dist > light.Radius+1 {
				bad++
			}
		}
		return
	}
	visible := func() {
		t.Helper()
		if _, err := w.Visible(view, 320, 200); err != nil {
			t.Fatal(err)
		}
	}
	visible()
	if n, _, _ := marked(); n != 0 {
		t.Fatalf("no light: %d faces marked", n)
	}
	w.DLights[0] = light
	visible()
	n, bad, wrongBits := marked()
	if n == 0 || bad != 0 || wrongBits != 0 {
		t.Fatalf("%d faces marked, %d beyond the radius, %d with bits other than 1", n, bad, wrongBits)
	}
	total := 0
	for _, f := range w.Faces {
		if f != nil {
			total++
		}
	}
	if n >= total {
		t.Fatalf("the light marked every face (%d of %d)", n, total)
	}
	t.Logf("a 220-unit light marks %d of %d faces", n, total)

	// the light is in the frame: a frame with it differs from one without
	frame := func() []byte {
		fb, _ := render.NewFrameBuffer(320, 200)
		if err := w.DrawSoftware(fb, cm, view); err != nil {
			t.Fatal(err)
		}
		return append([]byte(nil), fb.Pixels...)
	}
	with := frame()
	w.Now = 1 // dead: die < now
	without := frame()
	if n, _, _ := marked(); n != 0 {
		t.Fatalf("a dead light marked %d faces", n)
	}
	w.Now = 0
	w.DLights[0].Radius = 0 // no radius
	visible()
	if n, _, _ := marked(); n != 0 {
		t.Fatalf("a light without a radius marked %d faces", n)
	}
	if bytes.Equal(with, without) {
		t.Fatal("the dynamic light did not change the frame")
	}
	w.DLights[0] = light
	if !bytes.Equal(frame(), with) {
		t.Fatal("the same light gives another frame")
	}
}
