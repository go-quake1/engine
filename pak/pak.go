// Copyright (c) 1996-1997 Id Software, Inc.
// Copyright (c) 2026 the go-quake1/engine authors.
// SPDX-License-Identifier: GPL-2.0-or-later

package pak

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"sort"
	"time"
)

// On-wire constants taken verbatim from common/common.c.
const (
	headerSize = 12
	entrySize  = 64
	nameLen    = 56 // MAX_PACKPATH
	magic      = "PACK"
)

// ErrBadMagic is returned by Open when the PAK header magic bytes
// are not "PACK". tyrquake: Sys_Error "%s is not a packfile".
var ErrBadMagic = errors.New("pak: not a packfile (bad magic)")

// ErrBadDirectory is returned by Open when the directory length is
// not a multiple of entrySize (64 bytes per entry) -- the archive is
// truncated or corrupted.
var ErrBadDirectory = errors.New("pak: directory length not a multiple of 64 bytes")

// FS is the read-only [fs.FS] view of one .pak archive. Construct
// via Open.
type FS struct {
	src     io.ReaderAt
	entries []entry
	byName  map[string]int // path -> index into entries
}

type entry struct {
	name   string
	offset int64
	size   int64
}

// Open reads the PAK header + directory from src and returns a
// queryable FS. src is retained for lazy payload reads; the caller
// must keep it valid for the FS's lifetime. tyrquake: COM_LoadPackFile.
func Open(src io.ReaderAt) (*FS, error) {
	if src == nil {
		return nil, errors.New("pak: nil src")
	}
	var hdr [headerSize]byte
	if _, err := src.ReadAt(hdr[:], 0); err != nil {
		return nil, fmt.Errorf("pak: read header: %w", err)
	}
	if string(hdr[0:4]) != magic {
		return nil, ErrBadMagic
	}
	dirofs := int64(int32(binary.LittleEndian.Uint32(hdr[4:8])))
	dirlen := int64(int32(binary.LittleEndian.Uint32(hdr[8:12])))
	if dirlen%entrySize != 0 || dirlen < 0 || dirofs < 0 {
		return nil, ErrBadDirectory
	}

	// The directory is read a chunk at a time: dirlen is the file's claim, and
	// a header that says 2 GiB must not buy 2 GiB before a byte of it is read
	// (the allocation follows what the source holds).
	var entries []entry
	byName := map[string]int{}
	count := dirlen / entrySize
	chunk := make([]byte, dirChunkEntries*entrySize)
	for done := int64(0); done < count; {
		n := min(count-done, dirChunkEntries)
		buf := chunk[:n*entrySize]
		if _, err := src.ReadAt(buf, dirofs+done*entrySize); err != nil {
			return nil, fmt.Errorf("pak: read directory: %w", err)
		}
		for i := int64(0); i < n; i++ {
			raw := buf[i*entrySize : (i+1)*entrySize]
			// NUL-terminate the name (entry slot is fixed 56 bytes).
			end := bytes.IndexByte(raw[:nameLen], 0)
			if end < 0 {
				end = nameLen
			}
			name := string(raw[:end])
			pos := int64(int32(binary.LittleEndian.Uint32(raw[56:60])))
			size := int64(int32(binary.LittleEndian.Uint32(raw[60:64])))
			entries = append(entries, entry{name: name, offset: pos, size: size})
			byName[name] = len(entries) - 1
		}
		done += n
	}

	return &FS{src: src, entries: entries, byName: byName}, nil
}

// dirChunkEntries is how many directory entries Open reads at a time.
const dirChunkEntries = 1024

// ErrBadEntry is returned by FS.Open for an entry whose offset or size lies
// outside the archive (a negative size, or bytes the source does not hold).
var ErrBadEntry = errors.New("pak: entry outside the archive")

// Len returns the number of files in the archive.
func (f *FS) Len() int { return len(f.entries) }

// Open implements [fs.FS]. The name uses forward-slash separators
// (Quake convention) and is matched case-sensitively against the
// stored entry names. The special name "." returns a synthesised
// root directory whose ReadDir lists every entry.
func (f *FS) Open(name string) (fs.File, error) {
	if !fs.ValidPath(name) {
		return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrInvalid}
	}
	if name == "." {
		return f.rootDir(), nil
	}
	idx, ok := f.byName[name]
	if !ok {
		return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrNotExist}
	}
	e := f.entries[idx]
	// The size is the directory's claim: fs.ReadFile sizes its buffer from it, so
	// an entry that says 2 GiB in a 1 KiB archive is refused here (one byte read
	// at its end proves the source holds it).
	if e.size < 0 || e.offset < 0 {
		return nil, &fs.PathError{Op: "open", Path: name, Err: ErrBadEntry}
	}
	if e.size > 0 {
		var last [1]byte
		if n, _ := f.src.ReadAt(last[:], e.offset+e.size-1); n != 1 {
			return nil, &fs.PathError{Op: "open", Path: name, Err: ErrBadEntry}
		}
	}
	return &file{
		fs:   f,
		name: e.name,
		size: e.size,
		base: e.offset,
	}, nil
}

// ReadDir lists every archive entry. Names are returned in lexical
// order so the engine's "first .bsp in maps/" probe is deterministic.
// (Implements [fs.ReadDirFS] for `.` and the synthetic root only;
// nested directory listings would need a synthetic tree we don't
// build because tyrquake never enumerates sub-paths.)
func (f *FS) ReadDir(name string) ([]fs.DirEntry, error) {
	if name != "." {
		return nil, &fs.PathError{Op: "readdir", Path: name, Err: fs.ErrNotExist}
	}
	names := make([]string, 0, len(f.entries))
	for _, e := range f.entries {
		names = append(names, e.name)
	}
	sort.Strings(names)
	out := make([]fs.DirEntry, 0, len(names))
	for _, n := range names {
		e := f.entries[f.byName[n]]
		out = append(out, &fileInfo{name: n, size: e.size})
	}
	return out, nil
}

// --- fs.File / fs.FileInfo ---------------------------------------------------

type file struct {
	fs   *FS
	name string
	size int64
	base int64
	pos  int64
}

func (f *file) Stat() (fs.FileInfo, error) {
	return &fileInfo{name: f.name, size: f.size}, nil
}

func (f *file) Read(p []byte) (int, error) {
	if f.pos >= f.size {
		return 0, io.EOF
	}
	remain := f.size - f.pos
	if int64(len(p)) > remain {
		p = p[:remain]
	}
	n, err := f.fs.src.ReadAt(p, f.base+f.pos)
	f.pos += int64(n)
	return n, err
}

func (f *file) Close() error { return nil }

type fileInfo struct {
	name string
	size int64
	dir  bool
}

func (i *fileInfo) Name() string { return i.name }
func (i *fileInfo) Size() int64  { return i.size }
func (i *fileInfo) Mode() fs.FileMode {
	if i.dir {
		return fs.ModeDir | 0555
	}
	return 0444
}
func (i *fileInfo) ModTime() time.Time         { return time.Time{} }
func (i *fileInfo) IsDir() bool                { return i.dir }
func (i *fileInfo) Sys() any                   { return nil }
func (i *fileInfo) Info() (fs.FileInfo, error) { return i, nil }
func (i *fileInfo) Type() fs.FileMode          { return i.Mode() & fs.ModeType }

// --- root directory ---------------------------------------------------------

type rootDir struct {
	fs   *FS
	stat *fileInfo
	read bool
}

func (f *FS) rootDir() fs.File {
	return &rootDir{fs: f, stat: &fileInfo{name: ".", dir: true}}
}

func (d *rootDir) Stat() (fs.FileInfo, error) { return d.stat, nil }
func (d *rootDir) Read([]byte) (int, error) {
	return 0, &fs.PathError{Op: "read", Path: ".", Err: fs.ErrInvalid}
}
func (d *rootDir) Close() error { return nil }
func (d *rootDir) ReadDir(n int) ([]fs.DirEntry, error) {
	if d.read {
		return nil, io.EOF
	}
	d.read = true
	return d.fs.ReadDir(".")
}
