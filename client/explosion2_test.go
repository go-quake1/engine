// Copyright (c) 2026 the go-quake1/engine authors.
// SPDX-License-Identifier: GPL-2.0-or-later

package client

import "testing"

// TestApplyExplosion2Sink: TE_EXPLOSION2 hands its origin and colour range to
// EmitExplosion2 (cl_tent.c:260-266) before EmitTempEntity; no other kind does.
func TestApplyExplosion2Sink(t *testing.T) {
	s := NewState()
	var order []string
	var gotO [3]float32
	var gotS, gotL int
	s.EmitExplosion2 = func(o [3]float32, cs, cl int) { order = append(order, "colours"); gotO, gotS, gotL = o, cs, cl }
	s.EmitTempEntity = func(kind int, _ [3]float32) { order = append(order, "te") }
	if err := Apply(s, DecodedTempEntity{Kind: TEExplosion2, Origin: [3]float32{1, 2, 3}, ColorStart: 73, ColorLength: 24}, 1); err != nil {
		t.Fatal(err)
	}
	if len(order) != 2 || order[0] != "colours" || order[1] != "te" || gotO != [3]float32{1, 2, 3} || gotS != 73 || gotL != 24 {
		t.Errorf("order %v, %v %d %d", order, gotO, gotS, gotL)
	}
	order = nil
	_ = Apply(s, DecodedTempEntity{Kind: TEExplosion, Origin: [3]float32{1, 2, 3}}, 1)
	if len(order) != 1 || order[0] != "te" {
		t.Errorf("TE_EXPLOSION: %v", order)
	}
}
