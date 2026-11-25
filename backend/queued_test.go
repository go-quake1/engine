// Copyright (c) 2026 the go-quake1/engine authors.
// SPDX-License-Identifier: GPL-2.0-or-later

package backend

import (
	"testing"

	"github.com/go-quake1/engine/sound"
)

type queuedFake struct {
	*fakeBackend
	n int
}

func (q *queuedFake) QueuedFrames() int { return q.n }

// wrapper is what a quit observer or a frame timer is: a struct embedding the
// Backend interface, which promotes Backend's methods and no others.
type wrapper struct{ Backend }

// ForwardQueue makes a wrapper implement QueuedAudio exactly when the backend it
// wraps does, and reports the wrapped backend's queue, live.
func TestForwardQueue(t *testing.T) {
	plain := &fakeBackend{}
	if _, ok := Backend(wrapper{plain}).(QueuedAudio); ok {
		t.Fatal("test premise: an embedding wrapper hides QueuedAudio")
	}
	if got := ForwardQueue(wrapper{plain}, plain); got == nil {
		t.Fatal("nil")
	} else if _, ok := got.(QueuedAudio); ok {
		t.Error("a wrapper over a backend with no queue reports one")
	}

	q := &queuedFake{fakeBackend: &fakeBackend{}, n: 41}
	got := ForwardQueue(wrapper{q}, q)
	qa, ok := got.(QueuedAudio)
	if !ok {
		t.Fatal("the wrapper over a queued backend hides its queue")
	}
	if n := qa.QueuedFrames(); n != 41 {
		t.Errorf("QueuedFrames = %d, want 41", n)
	}
	q.n = 7
	if n := qa.QueuedFrames(); n != 7 {
		t.Errorf("QueuedFrames = %d after the queue moved, want 7", n)
	}
	// The rest of the Backend still works through it.
	if err := got.QueueAudio(nil); err != nil {
		t.Errorf("QueueAudio through the forwarder: %v", err)
	}
	if q.fakeBackend.QueueCalls != 1 {
		t.Errorf("QueueAudio reached the backend %d times, want 1", q.fakeBackend.QueueCalls)
	}
}

type voicesFake struct {
	*queuedFake
	v *voicesStub
}

type voicesStub struct{ sound.Voices }

func (f *voicesFake) Voices() sound.Voices { return f.v }

// ForwardQueue forwards the voice mixer too, and only when the backend has one.
func TestForwardQueueForwardsTheVoices(t *testing.T) {
	plain := &queuedFake{fakeBackend: &fakeBackend{}, n: 3}
	if _, ok := ForwardQueue(wrapper{plain}, plain).(VoiceBackend); ok {
		t.Error("a wrapper over a backend with no voice mixer offers one")
	}
	vf := &voicesFake{queuedFake: &queuedFake{fakeBackend: &fakeBackend{}, n: 5}, v: &voicesStub{}}
	got := ForwardQueue(wrapper{vf}, vf)
	vb, ok := got.(VoiceBackend)
	if !ok || vb.Voices() != sound.Voices(vf.v) {
		t.Fatal("the wrapper hides the voice mixer")
	}
	if qa, ok := got.(QueuedAudio); !ok || qa.QueuedFrames() != 5 {
		t.Error("the voice wrapper lost the queue")
	}
}

// A voice backend with no queue reports an empty one, not a panic.
func TestVoiceForwardWithoutAQueueReportsNone(t *testing.T) {
	vf := &voicesOnly{fakeBackend: &fakeBackend{}}
	qa, ok := ForwardQueue(wrapper{vf}, vf).(QueuedAudio)
	if !ok {
		t.Fatal("the voice wrapper is not QueuedAudio")
	}
	if n := qa.QueuedFrames(); n != 0 {
		t.Fatalf("QueuedFrames = %d, want 0", n)
	}
}

type voicesOnly struct{ *fakeBackend }

func (voicesOnly) Voices() sound.Voices { return nil }
