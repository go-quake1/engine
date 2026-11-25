package wasisurface

import (
	"errors"
	"testing"

	"github.com/wasmdesk/wasmbox/audioring"

	"github.com/go-quake1/engine/backend"
)

type fakeVoiceHost struct {
	st     VoiceStats
	statE  error
	closed int
}

func (f *fakeVoiceHost) Load(id, bits, rate, loopStart, frames int, pcm []byte) error { return nil }
func (f *fakeVoiceHost) Commit([]int32, *[2 * audioring.VoiceSlots]uint32) error      { return nil }
func (f *fakeVoiceHost) Stats() (VoiceStats, error)                                   { return f.st, f.statE }
func (f *fakeVoiceHost) Close()                                                       { f.closed++ }

func TestVoicesAreAbsentUntilAHostIsGiven(t *testing.T) {
	b := New(640, 400, nil, nil)
	if b.Voices() != nil {
		t.Fatal("a backend with no voice host offers a mixer (a typed nil in the interface?)")
	}
	if _, ok := b.VoiceStats(); ok {
		t.Fatal("stats without a host")
	}
	b.WithVoices(&fakeVoiceHost{})
	if b.Voices() == nil || b.VoiceMixer() == nil {
		t.Fatal("no mixer after WithVoices")
	}
	b.WithVoices(nil)
	if b.Voices() != nil {
		t.Fatal("WithVoices(nil) left the mixer")
	}
}

func TestAudioStatsAreTheMixersPaintedFrames(t *testing.T) {
	h := &fakeVoiceHost{st: VoiceStats{Painted: 66150, Underruns: 3}}
	b := New(640, 400, nil, nil).WithVoices(h)
	st := b.AudioStats()
	if st.Offered != 66150 || st.Accepted != 66150 || st.Silent {
		t.Fatalf("%+v", st)
	}
	h.statE = errors.New("gone")
	if st := b.AudioStats(); st.Offered != 0 {
		t.Fatalf("a host that cannot say still reports %+v", st)
	}
}

func TestForwardQueueKeepsTheVoices(t *testing.T) {
	inner := New(640, 400, nil, nil).WithVoices(&fakeVoiceHost{})
	outer := backend.ForwardQueue(struct{ backend.Backend }{inner}, inner)
	vb, ok := outer.(backend.VoiceBackend)
	if !ok || vb.Voices() == nil {
		t.Fatal("a wrapper hides the voice mixer from the runloop")
	}
	if _, ok := outer.(backend.QueuedAudio); !ok {
		t.Fatal("the wrapper lost the queue")
	}
}

func TestCloseVoicesClosesTheHost(t *testing.T) {
	h := &fakeVoiceHost{}
	New(640, 400, nil, nil).WithVoices(h).CloseVoices()
	if h.closed != 1 {
		t.Fatalf("closed %d times", h.closed)
	}
}
