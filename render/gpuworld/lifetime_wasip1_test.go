// Copyright (c) 2026 the go-quake1/engine authors.
// SPDX-License-Identifier: GPL-2.0-or-later

package gpuworld

import (
	"fmt"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/go-gfx/webgpu"
	"github.com/go-gfx/webgpu/wire"
)

// liveSink is a remote device's host as far as ids go: it refuses the creation
// of an id that is still live, as go-gfx/webgpu's batch.Context does ("batch:
// message refused: CREATE 12"), and an id is live until it is destroyed.
type liveSink struct {
	mu      sync.Mutex // Destroy also comes from the runtime's cleanup goroutine
	live    map[uint32]bool
	refused error
}

func (s *liveSink) Create(id uint32, _ wire.Kind, _ []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if id == 0 || s.live[id] {
		s.refused = fmt.Errorf("batch: message refused: CREATE %d", id)
		return s.refused
	}
	s.live[id] = true
	return nil
}
func (s *liveSink) Destroy(id uint32) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.live, id)
	return nil
}
func (s *liveSink) Submit([]uint32, []byte) error             { return nil }
func (s *liveSink) SubmitRead([]uint32, []byte, []byte) error { return nil }

// TestHandlesOutliveACollection: before go-gfx/webgpu v0.19.0 a remote device
// freed the id of a handle when the Go object was garbage collected without
// destroying it on the host, so a handle that only a local variable held (a
// shader module, a layout, the buffers a bind group names) handed its id to the
// next object created while the host still held the old one, and the host
// refused that creation and lost the device. The entities' layer lost it that
// way on the playground's CI lane, whose slower frames let a collection run
// before the first frame with entities. After EnableAlias and EnableAliasLayer
// and some collections, creating objects must not collide with a live id.
func TestHandlesOutliveACollection(t *testing.T) {
	sink := &liveSink{live: map[uint32]bool{}}
	dev := webgpu.NewRemoteDevice(sink, "test", nil)
	ub, err := dev.CreateBuffer(webgpu.BufferDescriptor{Size: UniformSize, Usage: webgpu.BufferUsageUniform | webgpu.BufferUsageCopyDst})
	if err != nil {
		t.Fatal(err)
	}
	cm, err := dev.CreateTexture(webgpu.TextureDescriptor{Size: webgpu.Extent3D{Width: 4, Height: 4}, Format: webgpu.TextureFormatR8Uint,
		Usage: webgpu.TextureUsageTextureBinding | webgpu.TextureUsageCopyDst})
	if err != nil {
		t.Fatal(err)
	}
	cmView, err := cm.CreateView(nil)
	if err != nil {
		t.Fatal(err)
	}
	r := &Renderer{dev: dev, width: 64, height: 32, ub: ub, cmView: cmView, rec: dev.NewRecorder()}
	set, err := BuildAlias(nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.EnableAlias(set); err != nil {
		t.Fatal(err)
	}
	if err := r.EnableAliasLayer(); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		runtime.GC()
		time.Sleep(20 * time.Millisecond) // the cleanups run in a goroutine of their own
	}
	var made []*webgpu.Buffer
	for i := 0; i < 64; i++ {
		b, err := dev.CreateBuffer(webgpu.BufferDescriptor{Size: 256, Usage: webgpu.BufferUsageMapRead | webgpu.BufferUsageCopyDst})
		if err != nil {
			t.Fatalf("buffer %d after a collection: %v (the host still holds the id of a handle that was collected)", i, err)
		}
		made = append(made, b)
	}
	runtime.KeepAlive(made)
	runtime.KeepAlive(r)
}
