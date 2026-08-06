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

// installFakeAudioContext replaces globalThis.AudioContext with a stub
// constructor that records every method call on globalThis.__acStats and
// starts in the autoplay-gated "suspended" state (like a real browser).
// The stub honours the full surface WebAudio touches: sampleRate, state,
// currentTime, destination, resume(), createBuffer() (whose result has
// copyToChannel) and createBufferSource() (whose result has connect +
// start). Returns a restore func that removes the stub afterwards so
// tests stay independent.
func installFakeAudioContext(t *testing.T, sampleRate int) (stats js.Value, restore func()) {
	t.Helper()
	js.Global().Call("eval", `
		globalThis.__acStats = { resume: 0, createBuffer: 0, bufferSource: 0, copyToChannel: 0, start: 0 };
		globalThis.__FakeAC = function () {
			this.sampleRate = `+itoa(sampleRate)+`;
			this.state = "suspended";
			this.currentTime = 0;
			this.destination = {};
			this.resume = function () { globalThis.__acStats.resume++; this.state = "running"; return Promise.resolve(); };
			this.createBuffer = function (ch, len, rate) {
				globalThis.__acStats.createBuffer++;
				return { copyToChannel: function () { globalThis.__acStats.copyToChannel++; } };
			};
			this.createBufferSource = function () {
				globalThis.__acStats.bufferSource++;
				return { buffer: null, connect: function () {}, start: function () { globalThis.__acStats.start++; } };
			};
		};
		globalThis.AudioContext = globalThis.__FakeAC;
	`)
	stats = js.Global().Get("__acStats")
	restore = func() {
		js.Global().Call("eval", `
			delete globalThis.AudioContext;
			delete globalThis.webkitAudioContext;
			delete globalThis.__FakeAC;
			delete globalThis.__acStats;
		`)
	}
	return stats, restore
}

// itoa is a tiny base-10 formatter so the eval'd script can embed the
// requested sample rate without importing strconv into the test's hot
// build-tagged path (keeps the fake self-contained).
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

func TestNewWebAudioMissingContext(t *testing.T) {
	// No AudioContext / webkitAudioContext in scope -> ErrAudioNoContext.
	js.Global().Call("eval", `delete globalThis.AudioContext; delete globalThis.webkitAudioContext;`)
	if _, err := NewWebAudio(); err != ErrAudioNoContext {
		t.Fatalf("NewWebAudio without a constructor: got err=%v, want %v", err, ErrAudioNoContext)
	}
}

func TestNewWebAudioResumesAtConstruction(t *testing.T) {
	stats, restore := installFakeAudioContext(t, 48000)
	defer restore()

	wa, err := NewWebAudio()
	if err != nil {
		t.Fatalf("NewWebAudio: unexpected error %v", err)
	}
	if wa.SampleRate() != 48000 {
		t.Fatalf("SampleRate: got %d, want 48000", wa.SampleRate())
	}
	// The autoplay-gated context must be resumed at construction so it is
	// not left silently suspended.
	if got := stats.Get("resume").Int(); got != 1 {
		t.Fatalf("resume() calls at construction: got %d, want 1", got)
	}
	if got := wa.State(); got != "running" {
		t.Fatalf("State after construction resume: got %q, want %q", got, "running")
	}
}

func TestWritePCMSchedulesBuffers(t *testing.T) {
	stats, restore := installFakeAudioContext(t, engineMixerRate) // srcRate==dstRate: 1 out frame per sample
	defer restore()

	wa, err := NewWebAudio()
	if err != nil {
		t.Fatalf("NewWebAudio: unexpected error %v", err)
	}

	samples := []sound.StereoSample{{L: 1000, R: -1000}, {L: 0, R: 0}, {L: -2000, R: 2000}}
	if err := wa.WritePCM(samples); err != nil {
		t.Fatalf("WritePCM: unexpected error %v", err)
	}

	if got := stats.Get("createBuffer").Int(); got != 1 {
		t.Fatalf("createBuffer calls: got %d, want 1", got)
	}
	if got := stats.Get("bufferSource").Int(); got != 1 {
		t.Fatalf("createBufferSource calls: got %d, want 1", got)
	}
	if got := stats.Get("copyToChannel").Int(); got != 2 { // L + R
		t.Fatalf("copyToChannel calls: got %d, want 2", got)
	}
	if got := stats.Get("start").Int(); got != 1 {
		t.Fatalf("start calls: got %d, want 1", got)
	}
	if got := wa.Scheduled(); got != 1 {
		t.Fatalf("Scheduled(): got %d, want 1", got)
	}
	// playhead advanced by len/sampleRate = 3/11025.
	wantHead := 3.0 / float64(engineMixerRate)
	if wa.playhead < wantHead-1e-9 || wa.playhead > wantHead+1e-9 {
		t.Fatalf("playhead: got %v, want %v", wa.playhead, wantHead)
	}
}

func TestWritePCMCatchesUpToCurrentTime(t *testing.T) {
	_, restore := installFakeAudioContext(t, engineMixerRate)
	defer restore()

	wa, err := NewWebAudio()
	if err != nil {
		t.Fatalf("NewWebAudio: unexpected error %v", err)
	}
	// A context clock already ahead of our playhead (0) must snap the
	// playhead forward so buffers schedule in the future, not the past.
	wa.ctx.Set("currentTime", 2.5)
	if err := wa.WritePCM([]sound.StereoSample{{L: 1, R: 1}, {L: 2, R: 2}}); err != nil {
		t.Fatalf("WritePCM: unexpected error %v", err)
	}
	want := 2.5 + 2.0/float64(engineMixerRate)
	if wa.playhead < want-1e-9 || wa.playhead > want+1e-9 {
		t.Fatalf("playhead after catch-up: got %v, want %v", wa.playhead, want)
	}
}

func TestWritePCMResumesWhenSuspended(t *testing.T) {
	stats, restore := installFakeAudioContext(t, engineMixerRate)
	defer restore()

	wa, err := NewWebAudio()
	if err != nil {
		t.Fatalf("NewWebAudio: unexpected error %v", err)
	}
	// Construction already resumed once (state -> running). Force the
	// context back to suspended (as a browser does when the tab is
	// backgrounded / before the unmuting gesture) and confirm WritePCM
	// self-heals with a second resume before scheduling.
	wa.ctx.Set("state", "suspended")
	if err := wa.WritePCM([]sound.StereoSample{{L: 1, R: 1}}); err != nil {
		t.Fatalf("WritePCM: unexpected error %v", err)
	}
	if got := stats.Get("resume").Int(); got != 2 {
		t.Fatalf("resume() calls after suspended WritePCM: got %d, want 2", got)
	}
	if got := wa.State(); got != "running" {
		t.Fatalf("State after self-heal: got %q, want %q", got, "running")
	}
}

func TestStateAbsentField(t *testing.T) {
	// A context object with no "state" field -> State() reports "".
	wa := &WebAudio{ctx: js.ValueOf(map[string]any{"sampleRate": 44100})}
	if got := wa.State(); got != "" {
		t.Fatalf("State with no state field: got %q, want empty", got)
	}
	// resume() must not panic when the context exposes no resume method.
	wa.resume()
}

func TestWritePCMZeroSampleRateSchedulesNothing(t *testing.T) {
	stats, restore := installFakeAudioContext(t, 0) // dstRate 0 -> resample yields nothing
	defer restore()

	wa, err := NewWebAudio()
	if err != nil {
		t.Fatalf("NewWebAudio: unexpected error %v", err)
	}
	if err := wa.WritePCM([]sound.StereoSample{{L: 5, R: 5}}); err != nil {
		t.Fatalf("WritePCM: unexpected error %v", err)
	}
	if got := stats.Get("createBuffer").Int(); got != 0 {
		t.Fatalf("createBuffer with empty resample: got %d, want 0", got)
	}
	if got := wa.Scheduled(); got != 0 {
		t.Fatalf("Scheduled with empty resample: got %d, want 0", got)
	}
}

func TestWritePCMEmptyNoOp(t *testing.T) {
	stats, restore := installFakeAudioContext(t, engineMixerRate)
	defer restore()

	wa, err := NewWebAudio()
	if err != nil {
		t.Fatalf("NewWebAudio: unexpected error %v", err)
	}
	if err := wa.WritePCM(nil); err != nil {
		t.Fatalf("WritePCM(nil): unexpected error %v", err)
	}
	if got := stats.Get("createBuffer").Int(); got != 0 {
		t.Fatalf("createBuffer on empty input: got %d, want 0", got)
	}
	if got := wa.Scheduled(); got != 0 {
		t.Fatalf("Scheduled after empty input: got %d, want 0", got)
	}
}
