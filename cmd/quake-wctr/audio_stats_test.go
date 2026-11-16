// Copyright (c) 2026 the go-quake1/engine authors.
// SPDX-License-Identifier: GPL-2.0-or-later

package main

import (
	"bytes"
	"testing"
	"time"

	"github.com/go-quake1/engine/backend"
	"github.com/go-quake1/engine/backend/wasisurface"
	"github.com/go-quake1/engine/runloop"
)

type statsBackend struct {
	backend.Backend
	st wasisurface.AudioStats
}

func (s statsBackend) AudioStats() wasisurface.AudioStats { return s.st }

// The summary line says what explains a ratio under 1: the frame rate, the mix
// the stalled tics were owed and not paid back, the mix the queue had already
// paid, the ratio with them, and the tic lengths. 10 s at 11025 Hz is 110250
// frames: 66150 offered (0.600), 33075 dropped in 3 stalls and 11025 prepaid by
// the queue make 1.000.
func TestWriteAudioStatsAccountsForTheStalls(t *testing.T) {
	var out bytes.Buffer
	be := statsBackend{st: wasisurface.AudioStats{Offered: 66150, Accepted: 66150}}
	r := &runloop.Runner{MixDropped: 33075, MixStalls: 3, MixPrepaid: 11025, MixTicsOver: [4]int{9, 5, 3, 1}, MixLongest: 0.62}
	writeAudioStats(&out, be, 10*time.Second, 150, r)
	want := "audio: playing, 66150 mix frames offered, 66150 accepted, in 10.00 s: real-time ratio 0.600; 15.0 frames a second, " +
		"33075 mix frames dropped in 3 stalled tics, 11025 prepaid by the queue, ratio with them 1.000; tics over 0.1 0.2 0.35 0.5 s: 9 5 3 1, longest 0.620 s\n"
	if out.String() != want {
		t.Errorf("got  %q\nwant %q", out.String(), want)
	}
}
