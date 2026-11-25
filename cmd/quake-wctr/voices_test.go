// Copyright (c) 2026 the go-quake1/engine authors.
// SPDX-License-Identifier: GPL-2.0-or-later

package main

import (
	"bytes"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/wasmdesk/wasmbox/audioring"

	"github.com/go-quake1/engine/backend"
	"github.com/go-quake1/engine/backend/wasisurface"
	"github.com/go-quake1/engine/runloop"
)

// fakeVoiceHost is the page's mixer: it counts what the game sends it.
type fakeVoiceHost struct {
	loads, commits, closed int
	cmdWords               int
}

func (f *fakeVoiceHost) Load(id, bits, rate, loopStart, frames int, pcm []byte) error {
	f.loads++
	return nil
}

func (f *fakeVoiceHost) Commit(words []int32, snap *[2 * audioring.VoiceSlots]uint32) error {
	f.commits++
	f.cmdWords += len(words)
	return nil
}

func (f *fakeVoiceHost) Stats() (wasisurface.VoiceStats, error) {
	return wasisurface.VoiceStats{Painted: 1234, Underruns: 2, Starved: 77}, nil
}

func (f *fakeVoiceHost) Close() { f.closed++ }

func withVoices(t *testing.T, open func() (wasisurface.VoiceHost, error)) {
	t.Helper()
	old := openVoices
	openVoices = open
	t.Cleanup(func() { openVoices = old })
}

// A host with the voice mixer: the game sends it commands each tic instead of
// writing frames, reads its counters at the end and closes it.
func TestVoicesGranted(t *testing.T) {
	closes := 0
	withSurface(t, &fakePresenter{}, false, &closes)
	fo := &fakeAudioOut{}
	withAudio(t, func() (wasisurface.AudioOut, error) { return fo, nil })
	fv := &fakeVoiceHost{}
	withVoices(t, func() (wasisurface.VoiceHost, error) { return fv, nil })
	var out, errOut bytes.Buffer
	if err := runGame(&out, &errOut, realFS(t), "e1m1", 64, 40, 6); err != nil {
		t.Fatalf("%v\n%s", err, errOut.String())
	}
	if !strings.Contains(errOut.String(), "sound on (wctr_audio_out voices, mixed in the page)") {
		t.Errorf("errOut:\n%s", errOut.String())
	}
	if fv.commits < 6 {
		t.Errorf("%d commits in 6 tics, want one a tic", fv.commits)
	}
	if fv.loads == 0 {
		t.Error("no sound was loaded into the bank")
	}
	if fv.closed != 1 {
		t.Errorf("the mixer was closed %d times, want 1", fv.closed)
	}
	if fo.writes != 0 {
		t.Errorf("the game wrote %d frames to the stream beside the mixer", fo.frames)
	}
	if !strings.Contains(out.String(), "voices: painted 1234 frames, underruns 2 (77 frames starved)") {
		t.Errorf("no voices summary:\n%s", out.String())
	}
}

// A refused voice_open falls back to the stream, and says why.
func TestVoicesRefusedFallsBackToTheStream(t *testing.T) {
	closes := 0
	withSurface(t, &fakePresenter{}, false, &closes)
	fo := &fakeAudioOut{}
	withAudio(t, func() (wasisurface.AudioOut, error) { return fo, nil })
	withVoices(t, func() (wasisurface.VoiceHost, error) { return nil, errors.New("unsupported") })
	var out, errOut bytes.Buffer
	if err := runGame(&out, &errOut, realFS(t), "e1m1", 64, 40, 4); err != nil {
		t.Fatalf("%v\n%s", err, errOut.String())
	}
	if !strings.Contains(errOut.String(), "voice mixer unavailable: unsupported") ||
		!strings.Contains(errOut.String(), "sound on (wctr_audio_out, 11025 Hz stereo)") {
		t.Errorf("errOut:\n%s", errOut.String())
	}
	if fo.writes == 0 {
		t.Error("the stream got nothing after the fallback")
	}
	if strings.Contains(out.String(), "voices: painted") {
		t.Errorf("a voices summary without a mixer:\n%s", out.String())
	}
}

// --fixed-mixer is the stream's measuring aid: it never opens the voices.
func TestFixedMixerSkipsTheVoices(t *testing.T) {
	closes := 0
	withSurface(t, &fakePresenter{}, false, &closes)
	withAudio(t, func() (wasisurface.AudioOut, error) { return &fakeAudioOut{}, nil })
	opened := 0
	withVoices(t, func() (wasisurface.VoiceHost, error) { opened++; return &fakeVoiceHost{}, nil })
	old := fixedMixer
	fixedMixer = true
	t.Cleanup(func() { fixedMixer = old })
	var out, errOut bytes.Buffer
	if err := runGame(&out, &errOut, realFS(t), "e1m1", 64, 40, 3); err != nil {
		t.Fatalf("%v\n%s", err, errOut.String())
	}
	if opened != 0 {
		t.Error("--fixed-mixer opened the voice mixer")
	}
}

// A backend that is not the surface (a wrapper hiding it) has no counters to
// print: the summary is silent, not a panic and not zeros.
func TestWriteVoiceStatsNeedsAMixerBackend(t *testing.T) {
	var out bytes.Buffer
	var be wasisurface.Backend
	writeVoiceStats(&out, struct{ backend.Backend }{}, &runloop.Runner{})
	writeVoiceStats(&out, &be, &runloop.Runner{VoiceRefused: 3})
	if out.Len() != 0 {
		t.Fatalf("a summary without a mixer: %q", out.String())
	}
	be.WithVoices(&fakeVoiceHost{})
	writeVoiceStats(&out, &be, &runloop.Runner{VoiceRefused: 3})
	if !strings.Contains(out.String(), "bad commands 0, refused 3") {
		t.Fatalf("summary %q", out.String())
	}
}

func withStall(t *testing.T, ms, every int) *[]time.Duration {
	t.Helper()
	oldMs, oldEvery, oldSleep := stallMs, stallEvery, stallSleep
	var slept []time.Duration
	stallMs, stallEvery = ms, every
	stallSleep = func(d time.Duration) { slept = append(slept, d) }
	t.Cleanup(func() { stallMs, stallEvery, stallSleep = oldMs, oldEvery, oldSleep })
	return &slept
}

// The stall is due on every Nth frame, never on the first, never without both
// flags.
func TestStallTicIsDueEveryNthFrame(t *testing.T) {
	slept := withStall(t, 250, 3)
	for n := 0; n < 10; n++ {
		stallTic(n)
	}
	if len(*slept) != 3 || (*slept)[0] != 250*time.Millisecond {
		t.Fatalf("slept %v over frames 0..9 every 3, want 3 stalls of 250 ms (frames 3, 6, 9)", *slept)
	}
	for _, c := range [][2]int{{0, 3}, {250, 0}, {-1, 3}, {250, -3}} {
		s := withStall(t, c[0], c[1])
		for n := 0; n < 10; n++ {
			stallTic(n)
		}
		if len(*s) != 0 {
			t.Fatalf("--stall-ms %d --stall-every %d stalled %d times", c[0], c[1], len(*s))
		}
	}
}

// The game loop calls it: a run of 7 frames with a stall every 3 holds twice.
func TestTheGameLoopStalls(t *testing.T) {
	closes := 0
	withSurface(t, &fakePresenter{}, false, &closes)
	slept := withStall(t, 5, 3)
	var out, errOut bytes.Buffer
	if err := runGame(&out, &errOut, realFS(t), "e1m1", 64, 40, 7); err != nil {
		t.Fatalf("%v\n%s", err, errOut.String())
	}
	if len(*slept) != 2 {
		t.Fatalf("%d stalls in 7 frames every 3, want 2 (frames 3 and 6)", len(*slept))
	}
}
