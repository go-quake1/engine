// Copyright (c) 2026 the go-quake1/engine authors.
// SPDX-License-Identifier: BSD-3-Clause

package ociassets

import (
	"bytes"
	"context"
	"errors"
	"io"
	"io/fs"
	"path"
	"sort"
	"sync"
	"time"
)

// FS implements [io/fs.FS] over an OCI registry. Open(name) looks up
// the file's layer digest in the manifest's annotation map, GETs the
// blob from the registry (the first call only -- subsequent calls
// hit the in-memory cache), and returns an [io/fs.File] over the
// bytes.
//
// FS is safe for concurrent use: per-file locks serialize the
// first-time fetch while still letting unrelated Open calls run in
// parallel.
type FS struct {
	client  *Client
	repo    string
	fileMap map[string]string // vfs-name -> "sha256:..."
	sizeMap map[string]int64  // digest -> declared layer size (for progress totals)

	mu        sync.Mutex
	inflight  map[string]*pending
	cache     map[string][]byte
	persisted persistCache // wasm: IndexedDB; host: no-op

	// nowFn / ctxFn are injectable seams so tests can drive the
	// timestamp embedded in the returned fs.FileInfo + the context
	// the blob fetch is issued with.
	nowFn func() time.Time
	ctxFn func() context.Context

	// progress is the optional per-fetch progress sink. Fired on each
	// counting-reader chunk + once on completion. nil = no callback.
	progress ProgressFunc
	// progressChunk is the cadence (in bytes) at which progress is
	// emitted. Defaults to 64 KiB; SetProgress accepts a custom value.
	progressChunk int
}

// ProgressFunc is the callback signature for blob-fetch progress.
// `name` is the VFS-relative name (e.g. "pak0.pak"); `digest` is the
// "sha256:..." identifier; `received` is the running total of bytes
// pulled from the registry for THIS blob; `total` is the declared
// layer size (0 if unknown). The callback fires from the goroutine
// driving the fetch -- implementations MUST be cheap and MUST NOT
// re-enter FS.Open on the same name.
type ProgressFunc func(name, digest string, received, total int64)

// defaultProgressChunkBytes is the cadence at which a counting reader
// emits progress notifications when SetProgress is configured without
// a custom chunk size. 64 KiB ~= 2800 ticks across a 180 MB pak0,
// which is plenty for a smoothly-growing bar without flooding the
// renderer goroutine.
const defaultProgressChunkBytes = 64 * 1024

// pending is the single-flight handle that fans out a concurrent
// Open(name) burst to ONE blob fetch. Holders Wait on done; the
// initiator fills bytes (and err) before closing it.
type pending struct {
	done  chan struct{}
	bytes []byte
	err   error
}

// NewFS constructs an FS for the given manifest reference + file
// map. The fileMap is typically produced by [BuildFileMap] but the
// constructor accepts any map so the CLI / tests can supply a
// hand-built one.
//
// Returns ErrManifestNoAnnotations when fileMap is empty (FS would be
// useless and Open would always fail).
func NewFS(client *Client, repo string, fileMap map[string]string) (*FS, error) {
	if len(fileMap) == 0 {
		return nil, ErrManifestNoAnnotations
	}
	return &FS{
		client:        client,
		repo:          repo,
		fileMap:       fileMap,
		sizeMap:       make(map[string]int64),
		inflight:      make(map[string]*pending),
		cache:         make(map[string][]byte),
		persisted:     defaultPersistCache(),
		nowFn:         time.Now,
		ctxFn:         context.Background,
		progressChunk: defaultProgressChunkBytes,
	}, nil
}

// NewFSFromManifest is a convenience that fetches + decodes the
// manifest then constructs the FS. Equivalent to:
//
//	m, _ := client.Manifest(ctx, repo, ref)
//	fm, _ := BuildFileMap(m)
//	return NewFS(client, repo, fm)
//
// with the errors wired through.
func NewFSFromManifest(ctx context.Context, client *Client, repo, reference string) (*FS, error) {
	m, err := client.Manifest(ctx, repo, reference)
	if err != nil {
		return nil, err
	}
	if len(m.Layers) == 0 {
		return nil, ErrManifestNoLayers
	}
	fm, err := BuildFileMap(m)
	if err != nil {
		return nil, err
	}
	// NewFS can only error on empty fm, which BuildFileMap already
	// rejected as ErrManifestNoAnnotations -- so the error here is
	// unreachable on the live path. We still construct via NewFS (not
	// a bare struct literal) so any future invariant added inside
	// NewFS keeps applying here too; the err is swallowed because the
	// "empty-map" precondition has been ruled out one frame up.
	fsys, _ := NewFS(client, repo, fm)
	// Hydrate sizeMap from the manifest's layer descriptors so progress
	// callbacks can report received/total. Layers not referenced by an
	// annotation are still indexed (cheap + future-proof).
	for _, l := range m.Layers {
		fsys.sizeMap[l.Digest] = l.Size
	}
	return fsys, nil
}

// SetProgress installs a per-blob progress callback. fn=nil disables
// progress reporting. Thread-safe; callers may swap the callback at
// any time, including while a fetch is in flight (the next chunk tick
// will read + invoke the new value).
//
// The progress-tick cadence is the package default (64 KiB). To
// customise it, callers may use [FS.SetProgressChunk].
func (f *FS) SetProgress(fn ProgressFunc) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.progress = fn
	if f.progressChunk <= 0 {
		f.progressChunk = defaultProgressChunkBytes
	}
}

// SetProgressChunk overrides the progress-tick cadence (default 64
// KiB). chunkBytes <= 0 resets to the default. Thread-safe.
func (f *FS) SetProgressChunk(chunkBytes int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if chunkBytes <= 0 {
		f.progressChunk = defaultProgressChunkBytes
		return
	}
	f.progressChunk = chunkBytes
}

// progressReader wraps an io.Reader + fires a ProgressFunc every
// `chunk` bytes (and once on EOF). The total is informational only --
// passing 0 disables percentage but still emits a running count.
type progressReader struct {
	r        io.Reader
	name     string
	digest   string
	total    int64
	received int64
	emitted  int64
	chunk    int64
	fn       ProgressFunc
}

func (p *progressReader) Read(b []byte) (int, error) {
	n, err := p.r.Read(b)
	if n > 0 {
		p.received += int64(n)
	}
	if p.fn != nil {
		if p.received-p.emitted >= p.chunk || (err != nil && p.emitted != p.received) {
			// Tick on chunk boundary OR a final tick at EOF / error so
			// callers see the terminal count even when the last Read
			// returned <chunk bytes.
			p.emitted = p.received
			p.fn(p.name, p.digest, p.received, p.total)
		}
	}
	return n, err
}

// Open implements [io/fs.FS]. Name is the VFS-relative path under the
// quake.path/ annotation namespace (e.g. "pak0.pak",
// "music/track02.ogg"). Lookups are exact (no directory listing) --
// this matches what the engine's pak/vfs subsystem asks for.
func (f *FS) Open(name string) (fs.File, error) {
	name = path.Clean(name)
	digest, ok := f.fileMap[name]
	if !ok {
		return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrNotExist}
	}
	data, err := f.load(name, digest)
	if err != nil {
		return nil, &fs.PathError{Op: "open", Path: name, Err: err}
	}
	return &memFile{
		name:    name,
		data:    data,
		modTime: f.nowFn(),
	}, nil
}

// load returns the bytes for digest, fetching them from the registry
// on first call and caching the result for later. Concurrent calls
// for the same digest collapse to a single fetch (single-flight).
func (f *FS) load(name, digest string) ([]byte, error) {
	f.mu.Lock()
	if data, ok := f.cache[digest]; ok {
		f.mu.Unlock()
		return data, nil
	}
	if p, ok := f.inflight[digest]; ok {
		f.mu.Unlock()
		<-p.done
		return p.bytes, p.err
	}
	// Try the persistent cache (wasm IndexedDB on GOOS=js, no-op
	// elsewhere). A hit promotes to the in-memory cache + skips the
	// registry round-trip entirely.
	if data, ok := f.persisted.Get(digest); ok {
		f.cache[digest] = data
		f.mu.Unlock()
		return data, nil
	}
	p := &pending{done: make(chan struct{})}
	f.inflight[digest] = p
	f.mu.Unlock()

	defer func() {
		f.mu.Lock()
		delete(f.inflight, digest)
		f.mu.Unlock()
		close(p.done)
	}()

	rc, err := f.client.Blob(f.ctxFn(), f.repo, digest, nil)
	if err != nil {
		p.err = err
		return nil, err
	}
	defer rc.Close()

	// Snapshot the progress configuration under the lock so concurrent
	// SetProgress / SetProgressChunk calls can't race with us reading
	// the fields. The snapshot is the one in force AT FETCH START --
	// later swaps take effect on the next blob fetch.
	f.mu.Lock()
	progressFn := f.progress
	chunk := f.progressChunk
	total := f.sizeMap[digest]
	f.mu.Unlock()
	if chunk <= 0 {
		chunk = defaultProgressChunkBytes
	}

	var reader io.Reader = rc
	if progressFn != nil {
		// Emit an initial 0/total tick so callers see "starting" before
		// the first chunk lands -- the renderer goroutine can otherwise
		// flicker between an unpainted surface and the first 64 KiB tick
		// on fast localhost transports.
		progressFn(name, digest, 0, total)
		reader = &progressReader{
			r:      rc,
			name:   name,
			digest: digest,
			total:  total,
			chunk:  int64(chunk),
			fn:     progressFn,
		}
	}
	body, err := io.ReadAll(reader)
	if err != nil {
		p.err = err
		return nil, err
	}
	if err := VerifyDigest(body, digest); err != nil {
		p.err = err
		return nil, err
	}
	f.mu.Lock()
	f.cache[digest] = body
	f.mu.Unlock()
	f.persisted.Put(digest, body)
	p.bytes = body
	_ = name // kept for future per-name metrics
	return body, nil
}

// Names returns the sorted VFS-relative names known to the FS. Used
// by the CLI tool's verify mode + by tests.
func (f *FS) Names() []string {
	out := make([]string, 0, len(f.fileMap))
	for k := range f.fileMap {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// memFile is the [io/fs.File] adapter wrapping the cached bytes for
// one logical entry. Reads consume an internal byte cursor; Seek lets
// the engine's pak parser jump around within a small file.
type memFile struct {
	name    string
	data    []byte
	pos     int64
	modTime time.Time
	closed  bool
}

func (f *memFile) Read(p []byte) (int, error) {
	if f.closed {
		return 0, errClosed
	}
	if f.pos >= int64(len(f.data)) {
		return 0, io.EOF
	}
	n := copy(p, f.data[f.pos:])
	f.pos += int64(n)
	return n, nil
}

func (f *memFile) ReadAt(p []byte, off int64) (int, error) {
	if f.closed {
		return 0, errClosed
	}
	if off < 0 || off >= int64(len(f.data)) {
		return 0, io.EOF
	}
	n := copy(p, f.data[off:])
	if n < len(p) {
		return n, io.EOF
	}
	return n, nil
}

func (f *memFile) Seek(offset int64, whence int) (int64, error) {
	if f.closed {
		return 0, errClosed
	}
	var abs int64
	switch whence {
	case io.SeekStart:
		abs = offset
	case io.SeekCurrent:
		abs = f.pos + offset
	case io.SeekEnd:
		abs = int64(len(f.data)) + offset
	default:
		return 0, errInvalidWhence
	}
	if abs < 0 {
		return 0, errNegativeSeek
	}
	f.pos = abs
	return abs, nil
}

func (f *memFile) Close() error {
	f.closed = true
	return nil
}

func (f *memFile) Stat() (fs.FileInfo, error) {
	return &memFileInfo{name: f.name, size: int64(len(f.data)), modTime: f.modTime}, nil
}

// Bytes returns a reference to the underlying byte slice. Useful for
// callers that need bytes.NewReader-style random access without
// pulling in the io/fs adapter overhead.
func (f *memFile) Bytes() []byte { return f.data }

var (
	errClosed        = errors.New("ociassets: file closed")
	errInvalidWhence = errors.New("ociassets: invalid Seek whence")
	errNegativeSeek  = errors.New("ociassets: negative Seek offset")
)

type memFileInfo struct {
	name    string
	size    int64
	modTime time.Time
}

func (i *memFileInfo) Name() string       { return path.Base(i.name) }
func (i *memFileInfo) Size() int64        { return i.size }
func (i *memFileInfo) Mode() fs.FileMode  { return 0o444 }
func (i *memFileInfo) ModTime() time.Time { return i.modTime }
func (i *memFileInfo) IsDir() bool        { return false }
func (i *memFileInfo) Sys() any           { return nil }

// ensure memFile satisfies what callers expect.
var (
	_ fs.File     = (*memFile)(nil)
	_ io.ReaderAt = (*memFile)(nil)
	_ io.Seeker   = (*memFile)(nil)
)

// nopReadCloser wraps bytes for callers that want an io.ReadCloser
// over a precomputed buffer. Kept package-local so tests can use it
// when injecting a fake HTTP body.
type nopReadCloser struct{ *bytes.Reader }

func (nopReadCloser) Close() error { return nil }
