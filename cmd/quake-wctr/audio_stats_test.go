// Copyright (c) 2026 the go-quake1/engine authors.
// SPDX-License-Identifier: GPL-2.0-or-later

package main

import (
	"bytes"
	"testing"
	"time"

	"github.com/go-quake1/engine/backend"
	"github.com/go-quake1/engine/backend/wasisurface"
)

type statsBackend struct {
	backend.Backend
	st wasisurface.AudioStats
}

func (s statsBackend) AudioStats() wasisurface.AudioStats { return s.st }

// The summary line says what explains a ratio under 1: the frame rate, the mix
// the stalled tics were owed and not paid back, and the ratio with them. 10 s
// at 11025 Hz is 110250 frames: 66150 offered (0.600) and 44100 dropped in 3
// stalls make 1.000.
func TestWriteAudioStatsAccountsForTheStalls(t *testing.T) {
	var out bytes.Buffer
	be := statsBackend{st: wasisurface.AudioStats{Offered: 66150, Accepted: 66150}}
	writeAudioStats(&out, be, 10*time.Second, 150, 44100, 3)
	want := "audio: playing, 66150 mix frames offered, 66150 accepted, in 10.00 s: real-time ratio 0.600; 15.0 frames a second, 44100 mix frames dropped in 3 stalled tics, ratio with them 1.000\n"
	if out.String() != want {
		t.Errorf("got  %q\nwant %q", out.String(), want)
	}
}
