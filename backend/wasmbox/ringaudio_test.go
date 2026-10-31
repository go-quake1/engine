// Copyright (c) 1996-1997 Id Software, Inc.
// Copyright (c) 2026 the go-quake1/engine authors.
// SPDX-License-Identifier: GPL-2.0-or-later

//go:build js && wasm

package wasmbox

import (
	"syscall/js"
	"testing"

	"github.com/go-quake1/engine/sound"
)

func TestRingAudioRelaysPCM(t *testing.T) {
	g := js.Global()
	g.Call("eval", `globalThis.__ringMsgs = []; globalThis.postMessage = function (m) { globalThis.__ringMsgs.push(m); };`)
	defer g.Call("eval", `delete globalThis.postMessage; delete globalThis.__ringMsgs;`)

	ra, err := NewRingAudio(7)
	if err != nil {
		t.Fatal(err)
	}
	msgs := g.Get("__ringMsgs")
	if msgs.Length() != 1 {
		t.Fatalf("audio_open messages = %d, want 1", msgs.Length())
	}
	m := msgs.Index(0)
	if m.Get("type").String() != "audio_open" || m.Get("sampleRate").Int() != engineMixerRate ||
		m.Get("channels").Int() != 2 || m.Get("capacityFrames").Int() != ringCapacityFrames || m.Get("window_id").Int() != 7 {
		t.Fatalf("bad audio_open: %v", m)
	}
	if ra.SampleRate() != engineMixerRate {
		t.Fatalf("SampleRate = %d", ra.SampleRate())
	}

	if err := ra.WritePCM(nil); err != nil { // empty is a no-op
		t.Fatal(err)
	}
	if err := ra.WritePCM([]sound.StereoSample{{L: 16384, R: -32768}, {L: 0, R: 8192}}); err != nil {
		t.Fatal(err)
	}
	sab := m.Get("sab")
	data := g.Get("Float32Array").New(sab, 64)
	for i, want := range []float64{0.5, -1, 0, 0.25} {
		if got := data.Index(i).Float(); got != want {
			t.Fatalf("sample %d = %v, want %v", i, got, want)
		}
	}
	hdr := g.Get("Int32Array").New(sab, 0, 16)
	if w := g.Get("Atomics").Call("load", hdr, 0).Int(); w != 2 {
		t.Fatalf("write index = %d, want 2", w)
	}
	if s := ra.Stats(); s.Played != 0 || s.Underruns != 0 || s.Overruns != 0 {
		t.Fatalf("stats = %+v", s)
	}
	// A second, larger chunk reuses the scratch buffer.
	if err := ra.WritePCM(make([]sound.StereoSample, 100)); err != nil {
		t.Fatal(err)
	}
	// The planted-negative hook: with it set, nothing more is written.
	before := g.Get("Atomics").Call("load", hdr, 0).Int()
	ra.noWrite = true
	if err := ra.WritePCM(make([]sound.StereoSample, 10)); err != nil {
		t.Fatal(err)
	}
	if g.Get("Atomics").Call("load", hdr, 0).Int() != before {
		t.Fatal("noWrite still wrote")
	}
	g.Set("__wasmboxNoAudioWrite", true)
	if ra2, err := NewRingAudio(8); err != nil || !ra2.noWrite {
		t.Fatalf("hook not read: %v", err)
	}
	g.Delete("__wasmboxNoAudioWrite")
	ra.Close()
	if g.Get("Atomics").Call("load", hdr, 5).Int() != 1 {
		t.Fatal("Close not visible to the consumer")
	}
}
