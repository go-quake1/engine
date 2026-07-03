// Copyright (c) 2026 the go-quake1/engine authors.
// SPDX-License-Identifier: BSD-3-Clause

package ociassets

import (
	"bytes"
	"context"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// newFakeRegistry returns an httptest.Server that maps fileMap into
// the OCI v2 wire layout. The returned closer must be called by the
// test. blobFetch counter lets tests assert the single-flight + cache
// effects (the second Open should NOT trigger a second GET).
type registryFake struct {
	srv         *httptest.Server
	blobFetched int32
}

func newFakeRegistry(t *testing.T, files map[string][]byte) *registryFake {
	t.Helper()
	rf := &registryFake{}
	// Build the manifest body once.
	m := &Manifest{
		SchemaVersion: 2,
		MediaType:     MediaTypeManifest,
		Config:        Descriptor{MediaType: MediaTypeConfig, Digest: "sha256:00", Size: 0},
		Annotations:   map[string]string{},
	}
	blobs := map[string][]byte{}
	for name, data := range files {
		d := Sha256Digest(data)
		m.Layers = append(m.Layers, Descriptor{MediaType: "application/octet-stream", Digest: d, Size: int64(len(data))})
		m.Annotations[AnnotationPathPrefix+name] = d
		blobs[d] = data
	}
	body, err := EncodeManifest(m)
	if err != nil {
		t.Fatalf("EncodeManifest: %v", err)
	}
	rf.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/v2/repo/manifests/"):
			w.Header().Set("Content-Type", MediaTypeManifest)
			w.Write(body)
		case strings.HasPrefix(r.URL.Path, "/v2/repo/blobs/"):
			atomic.AddInt32(&rf.blobFetched, 1)
			digest := strings.TrimPrefix(r.URL.Path, "/v2/repo/blobs/")
			if b, ok := blobs[digest]; ok {
				w.Write(b)
				return
			}
			http.NotFound(w, r)
		default:
			http.NotFound(w, r)
		}
	}))
	return rf
}

func TestNewFS_EmptyMap(t *testing.T) {
	if _, err := NewFS(NewClient("http://x"), "repo", nil); !errors.Is(err, ErrManifestNoAnnotations) {
		t.Fatalf("NewFS(empty): %v", err)
	}
}

func TestFS_OpenAndRead(t *testing.T) {
	files := map[string][]byte{
		"pak0.pak":          []byte("PACKfake-pak"),
		"music/track02.ogg": []byte("OggS-pretend"),
	}
	rf := newFakeRegistry(t, files)
	defer rf.srv.Close()

	c := &Client{HTTP: rf.srv.Client(), Origin: rf.srv.URL}
	fsys, err := NewFSFromManifest(context.Background(), c, "repo", "latest")
	if err != nil {
		t.Fatalf("NewFSFromManifest: %v", err)
	}
	names := fsys.Names()
	if len(names) != 2 || names[0] != "music/track02.ogg" {
		t.Fatalf("Names: %v", names)
	}

	f, err := fsys.Open("pak0.pak")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	buf, _ := io.ReadAll(f)
	if !bytes.Equal(buf, files["pak0.pak"]) {
		t.Fatalf("read bytes mismatch: %q vs %q", buf, files["pak0.pak"])
	}
	st, _ := f.Stat()
	if st.Name() != "pak0.pak" || st.Size() != int64(len(files["pak0.pak"])) || st.IsDir() || st.Mode() == 0 || st.Sys() != nil {
		t.Fatalf("Stat: %#v", st)
	}
	if st.ModTime().IsZero() {
		t.Fatalf("ModTime: zero (nowFn should populate)")
	}
	f.Close()

	// Second open: should hit cache, no second blob fetch.
	first := atomic.LoadInt32(&rf.blobFetched)
	if _, err := fsys.Open("pak0.pak"); err != nil {
		t.Fatalf("second Open: %v", err)
	}
	if atomic.LoadInt32(&rf.blobFetched) != first {
		t.Fatalf("cache miss: blobFetched grew from %d", first)
	}
}

func TestFS_OpenNotExist(t *testing.T) {
	rf := newFakeRegistry(t, map[string][]byte{"pak0.pak": []byte("p")})
	defer rf.srv.Close()
	c := &Client{HTTP: rf.srv.Client(), Origin: rf.srv.URL}
	fsys, err := NewFSFromManifest(context.Background(), c, "repo", "latest")
	if err != nil {
		t.Fatalf("NewFSFromManifest: %v", err)
	}
	_, err = fsys.Open("absent")
	var pe *fs.PathError
	if !errors.As(err, &pe) || !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("Open(absent): %v", err)
	}
}

func TestFS_FetchError(t *testing.T) {
	// Registry returns 500 on blob fetch -> Open should surface a
	// PathError wrapping the transport-layer failure.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(500)
	}))
	defer srv.Close()
	c := &Client{HTTP: srv.Client(), Origin: srv.URL}
	fsys, err := NewFS(c, "repo", map[string]string{"pak0.pak": "sha256:abcd"})
	if err != nil {
		t.Fatalf("NewFS: %v", err)
	}
	if _, err := fsys.Open("pak0.pak"); err == nil {
		t.Fatal("Open(500): want error")
	}
}

func TestFS_ReadAllError(t *testing.T) {
	// Configure the FS with a digest that doesn't match the body we
	// will return -> Open must surface the digest mismatch.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("bytes that hash to something else"))
	}))
	defer srv.Close()
	c := &Client{HTTP: srv.Client(), Origin: srv.URL}
	fsys, err := NewFS(c, "repo", map[string]string{"pak0.pak": "sha256:00"})
	if err != nil {
		t.Fatalf("NewFS: %v", err)
	}
	if _, err := fsys.Open("pak0.pak"); err == nil || !strings.Contains(err.Error(), "digest mismatch") {
		t.Fatalf("Open(digest): err = %v", err)
	}
}

// bodyErrReader returns N bytes then errors -- drives FS.load's
// io.ReadAll error branch.
type bodyErrReader struct{ called bool }

func (b *bodyErrReader) Read(p []byte) (int, error) { return 0, errors.New("body broke") }
func (b *bodyErrReader) Close() error               { return nil }

// fakeBlobClient.Do returns a 200 OK with a body that fails on Read.
type fakeBlobClient struct{}

func (fakeBlobClient) Do(*http.Request) (*http.Response, error) {
	return &http.Response{
		StatusCode: 200,
		Body:       &bodyErrReader{},
		Header:     http.Header{},
	}, nil
}

func TestFS_BodyReadError(t *testing.T) {
	c := &Client{HTTP: fakeBlobClient{}, Origin: "http://x"}
	fsys, err := NewFS(c, "repo", map[string]string{"pak0.pak": "sha256:00"})
	if err != nil {
		t.Fatalf("NewFS: %v", err)
	}
	if _, err := fsys.Open("pak0.pak"); err == nil {
		t.Fatal("Open(body-broke): want error")
	}
}

func TestFS_NewFSFromManifest_ManifestError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(404)
	}))
	defer srv.Close()
	c := &Client{HTTP: srv.Client(), Origin: srv.URL}
	if _, err := NewFSFromManifest(context.Background(), c, "repo", "latest"); err == nil {
		t.Fatal("NewFSFromManifest(404): want error")
	}
}

func TestFS_NewFSFromManifest_NoLayers(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"schemaVersion":2,"annotations":{"quake.path/x":"sha256:00"}}`))
	}))
	defer srv.Close()
	c := &Client{HTTP: srv.Client(), Origin: srv.URL}
	_, err := NewFSFromManifest(context.Background(), c, "repo", "latest")
	if !errors.Is(err, ErrManifestNoLayers) {
		t.Fatalf("NewFSFromManifest: err = %v want ErrManifestNoLayers", err)
	}
}

func TestFS_NewFSFromManifest_NoAnnotations(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"schemaVersion":2,"layers":[{"mediaType":"x","digest":"sha256:00","size":0}]}`))
	}))
	defer srv.Close()
	c := &Client{HTTP: srv.Client(), Origin: srv.URL}
	_, err := NewFSFromManifest(context.Background(), c, "repo", "latest")
	if !errors.Is(err, ErrManifestNoAnnotations) {
		t.Fatalf("NewFSFromManifest: err = %v", err)
	}
}

func TestMemFile_Operations(t *testing.T) {
	mf := &memFile{name: "x", data: []byte("hello"), modTime: time.Unix(0, 0)}
	// Read partial
	b := make([]byte, 3)
	n, err := mf.Read(b)
	if n != 3 || err != nil || string(b) != "hel" {
		t.Fatalf("Read: n=%d err=%v b=%q", n, err, b)
	}
	// Seek to start
	if pos, err := mf.Seek(0, io.SeekStart); err != nil || pos != 0 {
		t.Fatalf("Seek(0,Start): pos=%d err=%v", pos, err)
	}
	// Seek current
	if pos, _ := mf.Seek(2, io.SeekCurrent); pos != 2 {
		t.Fatalf("Seek(2,Cur): %d", pos)
	}
	// Seek end
	if pos, _ := mf.Seek(-1, io.SeekEnd); pos != 4 {
		t.Fatalf("Seek(-1,End): %d", pos)
	}
	// Invalid whence
	if _, err := mf.Seek(0, 99); err == nil {
		t.Fatal("Seek(invalid whence): want error")
	}
	// Negative
	if _, err := mf.Seek(-100, io.SeekStart); err == nil {
		t.Fatal("Seek(-100): want error")
	}
	// ReadAt
	got := make([]byte, 2)
	if n, err := mf.ReadAt(got, 1); n != 2 || err != nil || string(got) != "el" {
		t.Fatalf("ReadAt: n=%d err=%v got=%q", n, err, got)
	}
	// ReadAt past end
	if _, err := mf.ReadAt(got, 100); err != io.EOF {
		t.Fatalf("ReadAt(past): err = %v", err)
	}
	// ReadAt short -> EOF + bytes-copied
	got4 := make([]byte, 4)
	mf.Seek(0, io.SeekStart)
	if n, err := mf.ReadAt(got4, 3); n != 2 || err != io.EOF {
		t.Fatalf("ReadAt(short): n=%d err=%v", n, err)
	}
	// Read full + EOF
	mf.Seek(0, io.SeekStart)
	all := make([]byte, 100)
	n, _ = mf.Read(all)
	if n != 5 {
		t.Fatalf("Read full: %d", n)
	}
	if _, err := mf.Read(all); err != io.EOF {
		t.Fatalf("Read EOF: %v", err)
	}
	// Bytes accessor
	if string(mf.Bytes()) != "hello" {
		t.Fatalf("Bytes: %q", mf.Bytes())
	}
	// Close + post-close
	mf.Close()
	if _, err := mf.Read(b); err != errClosed {
		t.Fatalf("post-close Read: %v", err)
	}
	if _, err := mf.ReadAt(b, 0); err != errClosed {
		t.Fatalf("post-close ReadAt: %v", err)
	}
	if _, err := mf.Seek(0, 0); err != errClosed {
		t.Fatalf("post-close Seek: %v", err)
	}
}

func TestFS_SingleFlight(t *testing.T) {
	// Slow handler -> concurrent Open(name) calls must collapse to ONE GET.
	var hits int32
	gate := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		<-gate
		w.Write([]byte("payload"))
	}))
	defer srv.Close()
	c := &Client{HTTP: srv.Client(), Origin: srv.URL}
	fsys, _ := NewFS(c, "repo", map[string]string{"x": Sha256Digest([]byte("payload"))})
	var wg sync.WaitGroup
	const N = 8
	wg.Add(N)
	for i := 0; i < N; i++ {
		go func() {
			defer wg.Done()
			_, err := fsys.Open("x")
			if err != nil {
				t.Errorf("Open: %v", err)
			}
		}()
	}
	// Tiny grace so all goroutines park on the inflight wait before
	// we release the handler.
	time.Sleep(20 * time.Millisecond)
	close(gate)
	wg.Wait()
	if got := atomic.LoadInt32(&hits); got != 1 {
		t.Fatalf("single-flight: got %d GETs, want 1", got)
	}
}

func TestFS_PersistedCacheHit(t *testing.T) {
	// Pre-seed the persistent cache (real cache is no-op on host; we
	// inject a synthetic one here to exercise the hit branch).
	rf := newFakeRegistry(t, map[string][]byte{"x": []byte("payload")})
	defer rf.srv.Close()
	c := &Client{HTTP: rf.srv.Client(), Origin: rf.srv.URL}
	fsys, _ := NewFSFromManifest(context.Background(), c, "repo", "latest")
	fsys.persisted = &memPersistCache{data: map[string][]byte{
		Sha256Digest([]byte("payload")): []byte("payload"),
	}}
	before := atomic.LoadInt32(&rf.blobFetched)
	if _, err := fsys.Open("x"); err != nil {
		t.Fatalf("Open: %v", err)
	}
	if atomic.LoadInt32(&rf.blobFetched) != before {
		t.Fatalf("persisted cache miss: GET happened")
	}
}

// memPersistCache is a host-side test double for the IndexedDB cache.
// Lives in test code only; production wasm uses fs_wasm.go.
type memPersistCache struct {
	mu   sync.Mutex
	data map[string][]byte
}

func (m *memPersistCache) Get(k string) ([]byte, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	v, ok := m.data[k]
	return v, ok
}
func (m *memPersistCache) Put(k string, v []byte) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.data == nil {
		m.data = map[string][]byte{}
	}
	m.data[k] = v
}

func TestNopReadCloser(t *testing.T) {
	// keep the package-local nopReadCloser exercised so its Close
	// counts in coverage.
	rc := nopReadCloser{bytes.NewReader([]byte("x"))}
	if err := rc.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

// slowBlobHandler streams `body` in fixed-size chunks with a tiny
// per-chunk pause so the progressReader fires multiple ticks instead
// of swallowing the whole body in a single Read. Used by progress
// tests to assert the bar would visibly grow during a real fetch.
type slowBlobHandler struct {
	body  []byte
	chunk int
	delay time.Duration
}

func (s slowBlobHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	flusher, _ := w.(http.Flusher)
	w.Header().Set("Content-Length", "")
	for off := 0; off < len(s.body); off += s.chunk {
		end := off + s.chunk
		if end > len(s.body) {
			end = len(s.body)
		}
		w.Write(s.body[off:end])
		if flusher != nil {
			flusher.Flush()
		}
		if s.delay > 0 {
			time.Sleep(s.delay)
		}
	}
}

func TestFS_SetProgress_EmitsTicks(t *testing.T) {
	// 256 KiB body streamed in 16 KiB HTTP chunks with a tiny pause so
	// the counting reader sees several Read calls. With the default 64
	// KiB tick cadence we expect at least 4 mid-stream ticks + the
	// initial 0/total + a final terminal tick.
	body := bytes.Repeat([]byte{0xAB}, 256*1024)
	srv := httptest.NewServer(slowBlobHandler{body: body, chunk: 16 * 1024, delay: 1 * time.Millisecond})
	defer srv.Close()
	c := &Client{HTTP: srv.Client(), Origin: srv.URL}
	digest := Sha256Digest(body)
	fsys, err := NewFS(c, "repo", map[string]string{"big": digest})
	if err != nil {
		t.Fatalf("NewFS: %v", err)
	}
	// hydrate the sizeMap manually so progress callbacks carry total.
	fsys.sizeMap[digest] = int64(len(body))

	type tick struct {
		name           string
		digest         string
		received, total int64
	}
	var (
		mu    sync.Mutex
		ticks []tick
	)
	fsys.SetProgress(func(name, dg string, received, total int64) {
		mu.Lock()
		defer mu.Unlock()
		ticks = append(ticks, tick{name, dg, received, total})
	})
	if _, err := fsys.Open("big"); err != nil {
		t.Fatalf("Open: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(ticks) < 3 {
		t.Fatalf("ticks: got %d, want >=3 (initial + at least one chunk + final)", len(ticks))
	}
	if ticks[0].received != 0 || ticks[0].total != int64(len(body)) {
		t.Fatalf("first tick: %+v, want initial 0/total", ticks[0])
	}
	last := ticks[len(ticks)-1]
	if last.received != int64(len(body)) {
		t.Fatalf("last tick: received=%d, want %d", last.received, len(body))
	}
	// Monotonic + bounded.
	var prev int64
	for i, tk := range ticks {
		if tk.name != "big" || tk.digest != digest {
			t.Fatalf("tick %d: name/digest mismatch: %+v", i, tk)
		}
		if tk.received < prev {
			t.Fatalf("tick %d: non-monotonic received: %d<%d", i, tk.received, prev)
		}
		if tk.received > int64(len(body)) {
			t.Fatalf("tick %d: received overshoot: %d>%d", i, tk.received, len(body))
		}
		prev = tk.received
	}
}

func TestFS_SetProgress_NilDisables(t *testing.T) {
	body := bytes.Repeat([]byte{0x01}, 4*1024)
	rf := newFakeRegistry(t, map[string][]byte{"x": body})
	defer rf.srv.Close()
	c := &Client{HTTP: rf.srv.Client(), Origin: rf.srv.URL}
	fsys, _ := NewFSFromManifest(context.Background(), c, "repo", "latest")
	var hits int32
	fsys.SetProgress(func(string, string, int64, int64) { atomic.AddInt32(&hits, 1) })
	if _, err := fsys.Open("x"); err != nil {
		t.Fatalf("Open(progress-on): %v", err)
	}
	if atomic.LoadInt32(&hits) == 0 {
		t.Fatal("progress-on: want >0 ticks")
	}
	// Disable + fetch a different name so the cache doesn't short-circuit.
	atomic.StoreInt32(&hits, 0)
	fsys.SetProgress(nil)
	// Force a fresh fetch path: drop the cache.
	fsys.mu.Lock()
	fsys.cache = map[string][]byte{}
	fsys.mu.Unlock()
	if _, err := fsys.Open("x"); err != nil {
		t.Fatalf("Open(progress-off): %v", err)
	}
	if got := atomic.LoadInt32(&hits); got != 0 {
		t.Fatalf("progress-off: got %d ticks, want 0", got)
	}
}

func TestFS_SetProgressChunk(t *testing.T) {
	// Custom cadence: 1 KiB cadence over a 4 KiB body served in 1 KiB
	// chunks. Expect 4+ ticks (1 per HTTP chunk) -- way more than the
	// default 64 KiB cadence would emit on this body.
	body := bytes.Repeat([]byte{0x55}, 4*1024)
	srv := httptest.NewServer(slowBlobHandler{body: body, chunk: 1024, delay: 1 * time.Millisecond})
	defer srv.Close()
	c := &Client{HTTP: srv.Client(), Origin: srv.URL}
	digest := Sha256Digest(body)
	fsys, _ := NewFS(c, "repo", map[string]string{"x": digest})

	var hits int32
	fsys.SetProgress(func(string, string, int64, int64) { atomic.AddInt32(&hits, 1) })
	fsys.SetProgressChunk(1024)
	if _, err := fsys.Open("x"); err != nil {
		t.Fatalf("Open: %v", err)
	}
	if got := atomic.LoadInt32(&hits); got < 4 {
		t.Fatalf("custom chunk: got %d ticks, want >=4", got)
	}
	// Reset to default via 0; the field should fall back to the package default.
	fsys.SetProgressChunk(0)
	fsys.mu.Lock()
	got := fsys.progressChunk
	fsys.mu.Unlock()
	if got != defaultProgressChunkBytes {
		t.Fatalf("SetProgressChunk(0): chunk=%d want default %d", got, defaultProgressChunkBytes)
	}
	// Negative also resets to default.
	fsys.SetProgressChunk(-1)
	fsys.mu.Lock()
	got = fsys.progressChunk
	fsys.mu.Unlock()
	if got != defaultProgressChunkBytes {
		t.Fatalf("SetProgressChunk(-1): chunk=%d want default", got)
	}
}

func TestFS_SetProgress_NilFSResetsChunk(t *testing.T) {
	// Construct an FS with a zeroed chunk to exercise the "reset to
	// default" branch of SetProgress.
	c := NewClient("http://x")
	fsys, _ := NewFS(c, "repo", map[string]string{"x": "sha256:00"})
	fsys.mu.Lock()
	fsys.progressChunk = 0
	fsys.mu.Unlock()
	fsys.SetProgress(func(string, string, int64, int64) {})
	fsys.mu.Lock()
	got := fsys.progressChunk
	fsys.mu.Unlock()
	if got != defaultProgressChunkBytes {
		t.Fatalf("SetProgress reset: chunk=%d want default", got)
	}
}

func TestProgressReader_EmitsFinalTickOnEOF(t *testing.T) {
	// Direct unit test on progressReader so we cover the error-branch
	// final-tick path without depending on the HTTP stack.
	body := bytes.Repeat([]byte{0x77}, 9*1024) // 9 KiB
	var ticks []int64
	pr := &progressReader{
		r:      bytes.NewReader(body),
		name:   "x",
		digest: "sha256:zz",
		total:  int64(len(body)),
		chunk:  4 * 1024, // 4 KiB cadence
		fn: func(_, _ string, received, _ int64) {
			ticks = append(ticks, received)
		},
	}
	if _, err := io.ReadAll(pr); err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if len(ticks) < 2 {
		t.Fatalf("ticks: %v (want >=2)", ticks)
	}
	if last := ticks[len(ticks)-1]; last != int64(len(body)) {
		t.Fatalf("final tick: %d, want %d", last, len(body))
	}
}

func TestProgressReader_NoFnIsNoop(t *testing.T) {
	// fn=nil branch: counting still happens but no callback fires +
	// must not panic.
	body := []byte("abcd")
	pr := &progressReader{
		r:     bytes.NewReader(body),
		chunk: 1,
		fn:    nil,
	}
	n, err := io.ReadAll(pr)
	if err != nil || len(n) != len(body) {
		t.Fatalf("ReadAll(no-fn): n=%d err=%v", len(n), err)
	}
	if pr.received != int64(len(body)) {
		t.Fatalf("received: %d want %d", pr.received, len(body))
	}
}

func TestFS_LoadDefensiveZeroChunkFallback(t *testing.T) {
	// Drives the defensive `if chunk <= 0 { chunk = default }` branch in
	// load. SetProgressChunk normally guards against this, but if a
	// caller twiddles the field directly OR a future refactor leaves a
	// zero-value FS in flight we still emit progress at the default
	// cadence -- proven here.
	body := bytes.Repeat([]byte{0x99}, 4*1024)
	rf := newFakeRegistry(t, map[string][]byte{"x": body})
	defer rf.srv.Close()
	c := &Client{HTTP: rf.srv.Client(), Origin: rf.srv.URL}
	fsys, _ := NewFSFromManifest(context.Background(), c, "repo", "latest")
	var hits int32
	fsys.SetProgress(func(string, string, int64, int64) { atomic.AddInt32(&hits, 1) })
	// Force zero AFTER SetProgress has restored the default. The load
	// path snapshots progressChunk under the lock + must fall back.
	fsys.mu.Lock()
	fsys.progressChunk = 0
	fsys.mu.Unlock()
	if _, err := fsys.Open("x"); err != nil {
		t.Fatalf("Open: %v", err)
	}
	if atomic.LoadInt32(&hits) == 0 {
		t.Fatal("want >=1 tick from defensive-default load path")
	}
}

func TestFS_NewFSFromManifest_HydratesSizeMap(t *testing.T) {
	// Confirms NewFSFromManifest fills sizeMap from layer descriptors
	// so progress totals are non-zero on the OCI happy path.
	files := map[string][]byte{
		"pak0.pak": bytes.Repeat([]byte{0x42}, 1024),
	}
	rf := newFakeRegistry(t, files)
	defer rf.srv.Close()
	c := &Client{HTTP: rf.srv.Client(), Origin: rf.srv.URL}
	fsys, err := NewFSFromManifest(context.Background(), c, "repo", "latest")
	if err != nil {
		t.Fatalf("NewFSFromManifest: %v", err)
	}
	digest := Sha256Digest(files["pak0.pak"])
	if got := fsys.sizeMap[digest]; got != int64(len(files["pak0.pak"])) {
		t.Fatalf("sizeMap[digest]=%d want %d", got, len(files["pak0.pak"]))
	}
}
