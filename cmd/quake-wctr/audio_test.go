// Copyright (c) 2026 the go-quake1/engine authors.
// SPDX-License-Identifier: GPL-2.0-or-later

package main

import (
	"bytes"
	"errors"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/go-quake1/engine/backend/wasisurface"
)

// fakeAudioOut is a stream the page plays in real time on no clock at all: it
// accepts everything and reports a small queue, so a native run of a few frames
// can count what the game offered it.
type fakeAudioOut struct {
	writes, frames, closed int
	queued                 float64
	last                   time.Time
}

func (f *fakeAudioOut) Write(pcm []float32) (int, int, error) {
	now := time.Now()
	if !f.last.IsZero() {
		f.queued -= now.Sub(f.last).Seconds() * 11025
		if f.queued < 0 {
			f.queued = 0
		}
	}
	f.last = now
	f.writes++
	f.frames += len(pcm) / 2
	f.queued += float64(len(pcm) / 2)
	return len(pcm) / 2, int(f.queued), nil
}

func (f *fakeAudioOut) Close() { f.closed++ }

func withAudio(t *testing.T, open func() (wasisurface.AudioOut, error)) {
	t.Helper()
	old := openAudio
	openAudio = open
	t.Cleanup(func() { openAudio = old })
}

func offered(t *testing.T, out string) int {
	t.Helper()
	m := regexp.MustCompile(`audio: (\w+), (\d+) mix frames offered, (\d+) accepted, in [0-9.]+ s: real-time ratio [0-9.]+`).FindStringSubmatch(out)
	if m == nil {
		t.Fatalf("no audio line in:\n%s", out)
	}
	n, _ := strconv.Atoi(m[2])
	return n
}

// Without the grant (Denied: not run with --audio; Unsupported: native wctr) the
// game runs and raises no error: silent, and it says so.
func TestAudioNotGranted(t *testing.T) {
	for _, msg := range []string{"denied: run the container with --audio", "unsupported: this host has no audio output"} {
		closes := 0
		withSurface(t, &fakePresenter{}, false, &closes)
		withAudio(t, func() (wasisurface.AudioOut, error) { return nil, errors.New(msg) })
		var out, errOut bytes.Buffer
		if err := runGame(&out, &errOut, realFS(t), "e1m1", 64, 40, 5); err != nil {
			t.Fatalf("%s: the game stopped: %v\n%s", msg, err, errOut.String())
		}
		if !strings.Contains(errOut.String(), "sound off: "+msg) {
			t.Errorf("no word about the silence:\n%s", errOut.String())
		}
		if !strings.Contains(out.String(), "audio: silent, ") {
			t.Errorf("no silent audio summary:\n%s", out.String())
		}
		if offered(t, out.String()) == 0 {
			t.Error("a silent game painted no mix at all: the sounds would not be consumed")
		}
	}
}

// A build without the host module (no tag): silent, no error.
func TestAudioBuildWithoutModule(t *testing.T) {
	closes := 0
	withSurface(t, &fakePresenter{}, false, &closes)
	withAudio(t, nil)
	var out, errOut bytes.Buffer
	if err := runGame(&out, &errOut, realFS(t), "e1m1", 64, 40, 3); err != nil {
		t.Fatalf("%v\n%s", err, errOut.String())
	}
	if !strings.Contains(errOut.String(), "sound off (this build has no wctr_audio_out)") {
		t.Errorf("errOut:\n%s", errOut.String())
	}
}

// Granted: the game writes its mix to the stream, one call a tic, and closes the
// stream when it ends; the paced mix is far less than the fixed chunk would be.
func TestAudioGranted(t *testing.T) {
	closes := 0
	withSurface(t, &fakePresenter{}, false, &closes)
	fo := &fakeAudioOut{}
	withAudio(t, func() (wasisurface.AudioOut, error) { return fo, nil })
	var out, errOut bytes.Buffer
	if err := runGame(&out, &errOut, realFS(t), "e1m1", 64, 40, 6); err != nil {
		t.Fatalf("%v\n%s", err, errOut.String())
	}
	if !strings.Contains(errOut.String(), "sound on (wctr_audio_out, 11025 Hz stereo)") {
		t.Errorf("errOut:\n%s", errOut.String())
	}
	if fo.writes == 0 || fo.frames == 0 {
		t.Fatalf("the game wrote nothing to the stream: %+v", fo)
	}
	if fo.closed < 1 {
		t.Error("the stream was not closed when the game ended")
	}
	if !strings.Contains(out.String(), "audio: playing, ") {
		t.Errorf("no playing audio summary:\n%s", out.String())
	}
	paced := offered(t, out.String())

	// --fixed-mixer is what the game did before the pacing: 512 frames every tic.
	closes = 0
	fixedMixer = true
	t.Cleanup(func() { fixedMixer = false })
	fo2 := &fakeAudioOut{}
	withAudio(t, func() (wasisurface.AudioOut, error) { return fo2, nil })
	out.Reset()
	if err := runGame(&out, &errOut, realFS(t), "e1m1", 64, 40, 6); err != nil {
		t.Fatal(err)
	}
	fixed := offered(t, out.String())
	if fixed != 6*512 {
		t.Errorf("the fixed mixer offered %d mix frames in 6 tics, want %d", fixed, 6*512)
	}
	if paced >= fixed {
		t.Errorf("the paced mix (%d frames) is not less than the fixed chunk (%d) over 6 fast tics", paced, fixed)
	}
}
