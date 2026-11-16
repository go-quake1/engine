// Copyright (c) 2026 the go-quake1/engine authors.
// SPDX-License-Identifier: GPL-2.0-or-later

package sound

import (
	"encoding/binary"
	"testing"
)

// Reference: tyrquake snd_mix.c, S_PaintChannels:
//
//	if (ltime >= ch->end) {
//	    if (ch->looping && sc->loopstart >= 0) {
//	        ch->pos = sc->loopstart;
//	        ch->end = ltime + sc->length - ch->pos;
//	    } else { ch->sfx = NULL; break; }
//	}
//
// i.e. a looping channel that runs out restarts at loopstart and the frames
// that remain in the paint come from there.

// ramp is an 8-bit sample of n samples, sample i = i+1 (signed bytes).
func ramp(n, loopStart int) *Sample {
	d := make([]byte, n)
	for i := range d {
		d[i] = byte(i + 1)
	}
	return &Sample{Name: "ramp", SampleRate: 11025, BitsPerSam: 8, LoopStart: loopStart, NumSamples: n, Data: d}
}

// expect is the mix of sample index idx at the channel's volume (lv=rv=256:
// s * 256 / 256 = s), by the formula in snd_mix.c, independent of Paint.
func expect(idx []int) []StereoSample {
	out := make([]StereoSample, len(idx))
	for i, k := range idx {
		out[i] = StereoSample{L: int16(k + 1), R: int16(k + 1)}
	}
	return out
}

func paintOne(t *testing.T, s *Sample, looping bool, frames int) (*Channel, []StereoSample) {
	t.Helper()
	p, _ := NewPool(1)
	ch := &p.Channels[0]
	ch.Sfx, ch.EndPos, ch.Looping = s, s.NumSamples, looping
	ch.LeftVol, ch.RightVol = 256, 256
	out := make([]StereoSample, frames)
	if err := Paint(p, out, frames); err != nil {
		t.Fatal(err)
	}
	return ch, out
}

// A looped sample painted past its end wraps to loopstart, as many times as the
// frames need: 10 samples, loopstart 4, 25 frames = 0..9, then 4..9 twice, then
// 4, 5, 6; the channel is still playing at position 7.
func TestPaintLoopWrapsToLoopStart(t *testing.T) {
	ch, out := paintOne(t, ramp(10, 4), true, 25)
	idx := []int{0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 4, 5, 6, 7, 8, 9, 4, 5, 6, 7, 8, 9, 4, 5, 6}
	want := expect(idx)
	for i := range want {
		if out[i] != want[i] {
			t.Fatalf("frame %d = %+v, want %+v (sample %d)", i, out[i], want[i], idx[i])
		}
	}
	if ch.Free() || ch.Position != 7 {
		t.Errorf("after the paint: free %v position %d, want playing at 7", ch.Free(), ch.Position)
	}
}

// Loopstart 0 loops the whole sound; a paint of exactly the length ends exactly
// at the wrap (position back at loopstart, not stopped).
func TestPaintLoopFromZeroAndExactEnd(t *testing.T) {
	ch, out := paintOne(t, ramp(4, 0), true, 10)
	want := expect([]int{0, 1, 2, 3, 0, 1, 2, 3, 0, 1})
	for i := range want {
		if out[i] != want[i] {
			t.Fatalf("frame %d = %+v, want %+v", i, out[i], want[i])
		}
	}
	if ch.Free() || ch.Position != 2 {
		t.Errorf("free %v position %d, want playing at 2", ch.Free(), ch.Position)
	}
	ch, _ = paintOne(t, ramp(4, 2), true, 4)
	if ch.Free() || ch.Position != 2 {
		t.Errorf("a paint ending exactly at the end: free %v position %d, want back at loopstart 2", ch.Free(), ch.Position)
	}
}

// Not looping (the channel's flag, or no loop point in the sample): the sound
// ends at its end, the rest of the frames are silence, and the slot is free.
func TestPaintNoLoopEnds(t *testing.T) {
	for _, c := range []struct {
		name    string
		loopSt  int
		looping bool
	}{{"loop point but the channel is not looping", 4, false}, {"looping but no loop point", -1, true}} {
		ch, out := paintOne(t, ramp(10, c.loopSt), c.looping, 14)
		want := expect([]int{0, 1, 2, 3, 4, 5, 6, 7, 8, 9})
		for i := range want {
			if out[i] != want[i] {
				t.Fatalf("%s: frame %d = %+v, want %+v", c.name, i, out[i], want[i])
			}
		}
		for i := 10; i < 14; i++ {
			if out[i] != (StereoSample{}) {
				t.Errorf("%s: frame %d = %+v, want silence", c.name, i, out[i])
			}
		}
		if !ch.Free() {
			t.Errorf("%s: the channel is still playing", c.name)
		}
	}
}

// A loop point at or past the end cannot advance: it ends rather than spins.
func TestPaintLoopPointPastEndEnds(t *testing.T) {
	ch, _ := paintOne(t, ramp(10, 10), true, 30)
	if !ch.Free() {
		t.Error("a loop point at the end kept the channel playing")
	}
	ch, _ = paintOne(t, ramp(10, 99), true, 30)
	if !ch.Free() {
		t.Error("a loop point past the end kept the channel playing")
	}
}

// 16-bit samples wrap the same way (the byte index is doubled, the position is
// in samples).
func TestPaintLoop16Bit(t *testing.T) {
	d := make([]byte, 6*2)
	for i := 0; i < 6; i++ {
		binary.LittleEndian.PutUint16(d[2*i:], uint16(int16((i+1)*256)))
	}
	s := &Sample{Name: "s16", SampleRate: 11025, BitsPerSam: 16, LoopStart: 3, NumSamples: 6, Data: d}
	_, out := paintOne(t, s, true, 9)
	// s*256>>8 = s = (i+1)*256 ... lv=256: int16(s * 256 >> 8) = s.
	idx := []int{0, 1, 2, 3, 4, 5, 3, 4, 5}
	for i, k := range idx {
		if want := int16((k + 1) * 256); out[i].L != want || out[i].R != want {
			t.Fatalf("frame %d = %+v, want %d (sample %d)", i, out[i], want, k)
		}
	}
}

// Many paints of a few frames, as the game loop does (a paced mixer paints about
// 184 frames a tic): the loop is seamless across calls and never goes silent, for
// far longer than the sample (the defect: a channel played once and was gone after
// 25 s of mix).
func TestPaintLoopForever(t *testing.T) {
	p, _ := NewPool(1)
	s := ramp(100, 20)
	ch := &p.Channels[0]
	ch.Sfx, ch.EndPos, ch.Looping, ch.LeftVol, ch.RightVol = s, s.NumSamples, true, 256, 256
	pos := 0 // the oracle: the sample index each frame must carry
	for tic := 0; tic < 5000; tic++ {
		out := make([]StereoSample, 184)
		if err := Paint(p, out, 184); err != nil {
			t.Fatal(err)
		}
		for i := range out {
			if want := int16(pos + 1); out[i].L != want {
				t.Fatalf("tic %d frame %d = %d, want %d", tic, i, out[i].L, want)
			}
			if pos++; pos >= 100 {
				pos = 20
			}
		}
	}
	if ch.Free() {
		t.Fatal("a looping channel ended")
	}
}

// LoadWav reads the cue point (tyrquake GetWavinfo: info.loopstart is the cue
// point's sample offset), and a LIST chunk with a "mark" after it ends the sound
// at loopstart + the loop length: info.samples = loopstart + i.
func TestLoadWavLoopMarker(t *testing.T) {
	chunk := func(id string, body []byte) []byte {
		b := append([]byte(id), 0, 0, 0, 0)
		binary.LittleEndian.PutUint32(b[4:], uint32(len(body)))
		b = append(b, body...)
		if len(body)%2 == 1 {
			b = append(b, 0)
		}
		return b
	}
	fmtBody := make([]byte, 16)
	binary.LittleEndian.PutUint16(fmtBody[0:], 1)
	binary.LittleEndian.PutUint16(fmtBody[2:], 1)
	binary.LittleEndian.PutUint32(fmtBody[4:], 11025)
	binary.LittleEndian.PutUint16(fmtBody[14:], 8)
	cue := make([]byte, 28)
	binary.LittleEndian.PutUint32(cue[0:], 1)
	binary.LittleEndian.PutUint32(cue[24:], 1000) // the cue point's sample offset
	list := make([]byte, 32)
	copy(list[0:], "adtl")
	binary.LittleEndian.PutUint32(list[16:], 500) // samples in the loop
	copy(list[20:], "mark")
	build := func(withList bool, mark uint32) []byte {
		var body []byte
		body = append(body, "WAVE"...)
		body = append(body, chunk("fmt ", fmtBody)...)
		body = append(body, chunk("data", make([]byte, 2000))...)
		body = append(body, chunk("cue ", cue)...)
		if withList {
			l := append([]byte(nil), list...)
			binary.LittleEndian.PutUint32(l[16:], mark)
			body = append(body, chunk("LIST", l)...)
		}
		out := append([]byte("RIFF"), 0, 0, 0, 0)
		binary.LittleEndian.PutUint32(out[4:], uint32(len(body)))
		return append(out, body...)
	}
	s, err := LoadWav("t.wav", build(false, 0))
	if err != nil || s.LoopStart != 1000 || s.NumSamples != 2000 {
		t.Fatalf("cue only: %+v, %v; want loopstart 1000 over 2000 samples", s, err)
	}
	s, err = LoadWav("t.wav", build(true, 500))
	if err != nil || s.LoopStart != 1000 || s.NumSamples != 1500 {
		t.Fatalf("cue and mark: %+v, %v; want loopstart 1000, 1000 + 500 = 1500 samples", s, err)
	}
	// A marker beyond the data is a bad loop length: ignored.
	s, err = LoadWav("t.wav", build(true, 5000))
	if err != nil || s.NumSamples != 2000 {
		t.Fatalf("a marker past the data: %+v, %v; want all 2000 samples", s, err)
	}
}
