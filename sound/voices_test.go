// Copyright (c) 2026 the go-quake1/engine authors.
// SPDX-License-Identifier: GPL-2.0-or-later

package sound

import "testing"

type callLog struct {
	starts, vols, stops []int
	stopAlls            int
	retired             map[int]bool
	remaining           map[int]int
}

func (c *callLog) Start(slot int, _ *Sample, _ bool, _, _ int) { c.starts = append(c.starts, slot) }
func (c *callLog) SetVolume(slot, _, _ int)                    { c.vols = append(c.vols, slot) }
func (c *callLog) Stop(slot int)                               { c.stops = append(c.stops, slot) }
func (c *callLog) StopAll()                                    { c.stopAlls++ }
func (c *callLog) Commit() error                               { return nil }
func (c *callLog) Retired(slot int) bool                       { return c.retired[slot] }
func (c *callLog) Remaining(slot int) int {
	if r, ok := c.remaining[slot]; ok {
		return r
	}
	return -1
}
func (c *callLog) SetMusic(_, _ []int16, _ int) error { return nil }

func playing(p *Pool, slot, end int) {
	p.Channels[slot] = Channel{Sfx: &Sample{Name: "s", NumSamples: end}, EndPos: end, LeftVol: 10, RightVol: 20}
}

// guard runs f and fails t if it panics: the calls below must ignore what is
// outside their range, and a panic would be that check missing.
func guard(t *testing.T, what string, f func()) {
	t.Helper()
	defer func() {
		if r := recover(); r != nil {
			t.Errorf("%s panicked: %v", what, r)
		}
	}()
	f()
}

func TestAttachVoicesStartsTheChannelsAlreadyPlaying(t *testing.T) {
	p, _ := NewPool(8)
	playing(p, 1, 100)
	playing(p, 9, 100)
	c := &callLog{}
	p.AttachVoices(c)
	if len(c.starts) != 2 || c.starts[0] != 1 || c.starts[1] != 9 || len(c.stops) != 0 {
		t.Fatalf("starts %v stops %v", c.starts, c.stops)
	}
	guard(t, "AttachVoices(nil)", func() { p.AttachVoices(nil) })
	if p.Voices != nil {
		t.Fatal("not detached")
	}
}

func TestWithoutVoicesTheVoiceCallsDoNothing(t *testing.T) {
	p, _ := NewPool(8)
	playing(p, 1, 100)
	guard(t, "Launch", func() { p.Launch(1) })
	guard(t, "Retune", func() { p.Retune(1) })
	guard(t, "Sync", func() { p.Sync() })
	guard(t, "StopAll", func() { p.StopAll() })
}

func TestLaunchAndRetuneIgnoreSlotsOutsideThePool(t *testing.T) {
	p, _ := NewPool(8)
	c := &callLog{}
	p.Voices = c
	for _, slot := range []int{-1, MaxChannels, MaxChannels + 5} {
		guard(t, "Launch", func() { p.Launch(slot) })
		guard(t, "Retune", func() { p.Retune(slot) })
	}
	if len(c.starts)+len(c.stops)+len(c.vols) != 0 {
		t.Fatalf("calls for slots outside the pool: %v %v %v", c.starts, c.stops, c.vols)
	}
}

func TestLaunchOfAFreeChannelStopsItAndOfAPlayingOneStartsIt(t *testing.T) {
	p, _ := NewPool(8)
	c := &callLog{}
	p.Voices = c
	p.Launch(3)
	playing(p, 4, 50)
	p.Launch(4)
	if len(c.stops) != 1 || c.stops[0] != 3 || len(c.starts) != 1 || c.starts[0] != 4 {
		t.Fatalf("stops %v starts %v", c.stops, c.starts)
	}
}

func TestRetuneSendsPlayingChannelsOnly(t *testing.T) {
	p, _ := NewPool(8)
	c := &callLog{}
	p.Voices = c
	p.Retune(3) // free
	playing(p, 4, 50)
	p.Retune(4)
	if len(c.vols) != 1 || c.vols[0] != 4 {
		t.Fatalf("volumes %v", c.vols)
	}
}

func TestSyncFreesRetiredChannelsAndMovesThePositionOfTheOthers(t *testing.T) {
	p, _ := NewPool(8)
	c := &callLog{retired: map[int]bool{9: true}, remaining: map[int]int{10: 30, 11: 101, 12: 0}}
	p.Voices = c
	for _, s := range []int{9, 10, 11, 12} {
		playing(p, s, 100)
	}
	p.Sync()
	if !p.Channels[9].Free() {
		t.Fatal("a retired channel stays")
	}
	if p.Channels[10].Position != 70 {
		t.Fatalf("position %d, want 70", p.Channels[10].Position)
	}
	if p.Channels[11].Position != 0 {
		t.Fatalf("a remaining past the end (101 > 100) moved the position to %d", p.Channels[11].Position)
	}
	if p.Channels[12].Position != 100 || p.Channels[12].Free() {
		t.Fatalf("remaining 0: position %d free %v", p.Channels[12].Position, p.Channels[12].Free())
	}
}

func TestAllocNeverHandsOutTheMusicSlots(t *testing.T) {
	p, _ := NewPool(8)
	p.Voices = &callLog{}
	for i := 0; i < 2*MaxChannels; i++ {
		slot, err := p.Alloc(100+i, 1)
		if err != nil || slot >= MaxChannels-MusicSlots {
			t.Fatalf("slot %d err %v", slot, err)
		}
		playing(p, slot, 10)
		p.Channels[slot].EntNum, p.Channels[slot].EntChannel = 100+i, 1
	}
}

func TestStopAllSilencesTheChannelsAndTheMixer(t *testing.T) {
	p, _ := NewPool(8)
	playing(p, 1, 100)
	c := &callLog{}
	p.Voices = c
	p.StopAll()
	if c.stopAlls != 1 || !p.Channels[1].Free() {
		t.Fatalf("mixer StopAll calls %d, channel free %v", c.stopAlls, p.Channels[1].Free())
	}
}
