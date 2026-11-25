// Copyright (c) 2026 the go-quake1/engine authors.
// SPDX-License-Identifier: GPL-2.0-or-later

package runloop

import (
	"testing"

	"github.com/go-quake1/engine/backend"
	"github.com/go-quake1/engine/client"
	"github.com/go-quake1/engine/music"
	"github.com/go-quake1/engine/sound"
)

// recVoices records what the music driver hands the mixer.
type recVoices struct {
	sets      int
	l, r      []int16
	loopStart int
	stops     int
	commits   int
}

func (v *recVoices) Start(int, *sound.Sample, bool, int, int) {}
func (v *recVoices) SetVolume(int, int, int)                  {}
func (v *recVoices) Stop(int)                                 {}
func (v *recVoices) StopAll()                                 {}
func (v *recVoices) Commit() error                            { v.commits++; return nil }
func (v *recVoices) Retired(int) bool                         { return false }
func (v *recVoices) Remaining(int) int                        { return -1 }
func (v *recVoices) SetMusic(l, r []int16, ls int) error {
	if l == nil {
		v.stops++
		return nil
	}
	v.sets++
	v.l, v.r, v.loopStart = l, r, ls
	return nil
}

// stereo returns n frames of a constant stereo level.
func stereo(n int, level float32) []float32 {
	d := make([]float32, 2*n)
	for i := range d {
		d[i] = level
	}
	return d
}

func musicRunner(tracks map[int]*fakeMusicDecoder, track, loop int) *Runner {
	r := &Runner{
		Client: client.NewState(),
		MusicOpen: func(t int) ([]byte, bool) {
			_, ok := tracks[t]
			return []byte{byte(t)}, ok
		},
		MusicDecoder: func(b []byte) (music.Decoder, error) {
			d := *tracks[int(b[0])]
			return &d, nil
		},
		MusicVolume: 1,
	}
	r.Client.MusicEpoch, r.Client.MusicTrack, r.Client.MusicLoopTrack = 1, track, loop
	return r
}

// run ticks the driver until the mixer has the track, at most max tics.
func run(r *Runner, v *recVoices, max int) int {
	for i := 1; i <= max; i++ {
		r.tickMusicVoices(v)
		if v.sets > 0 {
			return i
		}
	}
	return -1
}

func TestMusicVoices_DecodesASliceATicThenUploadsOnce(t *testing.T) {
	const frames = 3*musicChunkFrames + 100
	r := musicRunner(map[int]*fakeMusicDecoder{2: {data: stereo(frames, 0.5), rate: 11025, channels: 2}}, 2, 0)
	v := &recVoices{}
	tics := run(r, v, 20)
	if tics != 4 {
		t.Fatalf("track uploaded after %d tics, want 4 (3 full slices and a tail)", tics)
	}
	if len(v.l) != frames || len(v.r) != frames || v.loopStart != -1 || v.sets != 1 {
		t.Fatalf("frames %d/%d loopStart %d sets %d", len(v.l), len(v.r), v.loopStart, v.sets)
	}
	if v.l[0] < 16000 || v.r[frames-1] < 16000 {
		t.Fatalf("levels %d %d", v.l[0], v.r[frames-1])
	}
	r.tickMusicVoices(v)
	if v.sets != 1 {
		t.Fatal("a finished track was uploaded again")
	}
}

func TestMusicVoices_AHigherRateIsReducedToTheMixRate(t *testing.T) {
	r := musicRunner(map[int]*fakeMusicDecoder{2: {data: stereo(4000, 0.5), rate: 22050, channels: 2}}, 2, 0)
	v := &recVoices{}
	run(r, v, 5)
	if len(v.l) != 2000 {
		t.Fatalf("%d frames from 4000 at 22050 Hz, want 2000", len(v.l))
	}
}

func TestMusicVoices_LoopOntoItselfLoopsFromTheStart(t *testing.T) {
	r := musicRunner(map[int]*fakeMusicDecoder{3: {data: stereo(500, 0.25), rate: 11025, channels: 2}}, 3, 3)
	v := &recVoices{}
	run(r, v, 5)
	if v.loopStart != 0 || len(v.l) != 500 {
		t.Fatalf("loopStart %d frames %d", v.loopStart, len(v.l))
	}
}

func TestMusicVoices_AnIntroIsFollowedByItsLoopTrack(t *testing.T) {
	r := musicRunner(map[int]*fakeMusicDecoder{
		2: {data: stereo(300, 0.5), rate: 11025, channels: 2},
		3: {data: stereo(200, -0.5), rate: 11025, channels: 2},
	}, 2, 3)
	v := &recVoices{}
	run(r, v, 10)
	if len(v.l) != 500 || v.loopStart != 300 {
		t.Fatalf("frames %d loopStart %d, want 500 and 300", len(v.l), v.loopStart)
	}
	if v.l[299] <= 0 || v.l[300] >= 0 {
		t.Fatalf("the loop track does not start at frame 300: %d %d", v.l[299], v.l[300])
	}
}

func TestMusicVoices_AMissingLoopTrackLeavesTheIntroPlayingOnce(t *testing.T) {
	r := musicRunner(map[int]*fakeMusicDecoder{2: {data: stereo(300, 0.5), rate: 11025, channels: 2}}, 2, 9)
	v := &recVoices{}
	run(r, v, 10)
	if len(v.l) != 300 || v.loopStart != -1 {
		t.Fatalf("frames %d loopStart %d", len(v.l), v.loopStart)
	}
}

func TestMusicVoices_ANewTrackStopsTheOldAndTrackZeroIsSilence(t *testing.T) {
	r := musicRunner(map[int]*fakeMusicDecoder{2: {data: stereo(100, 0.5), rate: 11025, channels: 2}}, 2, 0)
	v := &recVoices{}
	run(r, v, 5)
	r.Client.MusicEpoch, r.Client.MusicTrack = 2, 0
	r.tickMusicVoices(v)
	if v.stops != 2 || v.sets != 1 {
		t.Fatalf("stops %d sets %d, want the first epoch's stop and this one's", v.stops, v.sets)
	}
}

func TestMusicVoices_ATrackIsCutAtTheCap(t *testing.T) {
	d := &fakeMusicDecoder{data: stereo(musicMaxFrames+5*musicChunkFrames, 0.1), rate: 11025, channels: 2}
	r := musicRunner(map[int]*fakeMusicDecoder{2: d}, 2, 0)
	v := &recVoices{}
	run(r, v, 400)
	if len(v.l) != musicMaxFrames {
		t.Fatalf("%d frames, want the cap %d", len(v.l), musicMaxFrames)
	}
}

// retiringVoices reports every slot retired, to see Sync run in the step.
type retiringVoices struct{ recVoices }

func (v *retiringVoices) Retired(int) bool { return true }

func TestVoiceStep_AttachesOnceSyncsAndCommitsEachTic(t *testing.T) {
	p, _ := sound.NewPool(8)
	p.Channels[2].Sfx = &sound.Sample{Name: "x", NumSamples: 4}
	r := &Runner{SoundPool: p}
	v := &retiringVoices{}
	for i := 0; i < 3; i++ {
		if err := r.voiceStep(v); err != nil {
			t.Fatal(err)
		}
	}
	if v.commits != 3 || p.Voices != sound.Voices(v) {
		t.Fatalf("commits %d, attached %v", v.commits, p.Voices != nil)
	}
	if !p.Channels[2].Free() {
		t.Fatal("a sound the mixer retired still holds its channel after the step")
	}
}

type voiceBackendStub struct {
	backend.Backend
	v sound.Voices
}

func (b voiceBackendStub) Voices() sound.Voices { return b.v }

func TestVoices_AnAbsentMixerIsNilNotATypedNil(t *testing.T) {
	r := &Runner{Backend: voiceBackendStub{}}
	if r.voices() != nil {
		t.Fatal("a backend with no mixer yields a non-nil sound.Voices")
	}
	v := &recVoices{}
	r.Backend = voiceBackendStub{v: v}
	if r.voices() != sound.Voices(v) {
		t.Fatal("the backend's mixer is not returned")
	}
}
