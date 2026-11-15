// Copyright (c) 1996-1997 Id Software, Inc.
// Copyright (c) 2026 the go-quake1/engine authors.
// SPDX-License-Identifier: GPL-2.0-or-later

package wad

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"sort"
	"strings"
	"time"
)

// On-wire constants from tyrquake/include/wad.h.
const (
	headerSize    = 12
	lumpEntrySize = 32
	nameLen       = 16
	magic         = "WAD2"
)

// Compression / Type byte values from wad.h. Kept as exported
// constants because the engine's BSP loader + status-bar loader
// switch on them.
const (
	CmpNone byte = 0
	CmpLZSS byte = 1

	TypNone    byte = 0
	TypLabel   byte = 1
	TypLumpy   byte = 64 // base; specific kinds layer on top
	TypPalette byte = 64
	TypQTex    byte = 65
	TypQPic    byte = 66
	TypSound   byte = 67
	TypMipTex  byte = 68
)

// Sentinel errors.
var (
	ErrBadMagic  = errors.New("wad: not a WAD2 file (bad magic)")
	ErrShortRead = errors.New("wad: short read")
)

// Lump is one directory entry. Names are stored in canonical
// lowercased form (W_CleanupName).
type Lump struct {
	FilePos     int32
	DiskSize    int32
	Size        int32 // uncompressed
	Type        byte
	Compression byte
	Name        string
}

// FS is the read-only [io/fs.FS] view of one WAD2 archive.
type FS struct {
	src    io.ReaderAt
	lumps  []Lump
	byName map[string]int // canonical (lowercase) name -> index
}

// Open parses src and returns a queryable FS. src is retained for
// lazy payload reads; the caller must keep it valid for the FS's
// lifetime. tyrquake: W_LoadWadFile.
func Open(src io.ReaderAt) (*FS, error) {
	if src == nil {
		return nil, errors.New("wad: nil src")
	}
	var hdr [headerSize]byte
	if _, err := src.ReadAt(hdr[:], 0); err != nil {
		return nil, fmt.Errorf("wad: read header: %w", err)
	}
	if string(hdr[0:4]) != magic {
		return nil, ErrBadMagic
	}
	numlumps := int32(binary.LittleEndian.Uint32(hdr[4:8]))
	infotableofs := int32(binary.LittleEndian.Uint32(hdr[8:12]))
	if numlumps < 0 || infotableofs < 0 {
		return nil, ErrShortRead
	}

	// The directory is read a chunk at a time: numlumps is the file's claim, and
	// a header that says 2 G lumps must not buy 64 GiB before a byte of it is read.
	var lumps []Lump
	byName := map[string]int{}
	chunk := make([]byte, dirChunkLumps*lumpEntrySize)
	for done := int64(0); done < int64(numlumps); {
		n := min(int64(numlumps)-done, dirChunkLumps)
		raw := chunk[:n*lumpEntrySize]
		if _, err := src.ReadAt(raw, int64(infotableofs)+done*lumpEntrySize); err != nil {
			return nil, fmt.Errorf("wad: read directory: %w", err)
		}
		for i := int64(0); i < n; i++ {
			entry := raw[i*lumpEntrySize : (i+1)*lumpEntrySize]
			// NUL-terminate the name (entry slot is fixed 16 bytes).
			nameBytes := entry[16:32]
			end := bytes.IndexByte(nameBytes, 0)
			if end < 0 {
				end = nameLen
			}
			name := cleanName(string(nameBytes[:end]))
			lumps = append(lumps, Lump{
				FilePos:     int32(binary.LittleEndian.Uint32(entry[0:4])),
				DiskSize:    int32(binary.LittleEndian.Uint32(entry[4:8])),
				Size:        int32(binary.LittleEndian.Uint32(entry[8:12])),
				Type:        entry[12],
				Compression: entry[13],
				Name:        name,
			})
			byName[name] = len(lumps) - 1
		}
		done += n
	}

	return &FS{src: src, lumps: lumps, byName: byName}, nil
}

// dirChunkLumps is how many directory entries Open reads at a time.
const dirChunkLumps = 1024

// ErrBadLump is returned by FS.Open for a lump whose position or size lies
// outside the archive (a negative size, or bytes the source does not hold).
var ErrBadLump = errors.New("wad: lump outside the archive")

// cleanName lowercases an ASCII name and strips any trailing NULs.
// Matches tyrquake W_CleanupName: only A-Z -> a-z (no locale
// folding), stop at the first NUL.
func cleanName(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c == 0 {
			break
		}
		if c >= 'A' && c <= 'Z' {
			c += 'a' - 'A'
		}
		b.WriteByte(c)
	}
	return b.String()
}

// Len returns the number of lumps in the archive.
func (f *FS) Len() int { return len(f.lumps) }

// Lumps returns a copy of the lump directory. Useful for tools that
// want to inspect the archive without going through fs.FS.
func (f *FS) Lumps() []Lump {
	out := make([]Lump, len(f.lumps))
	copy(out, f.lumps)
	return out
}

// Open implements [fs.FS]. Name is matched case-insensitively
// against the stored (lowercased) lump names. The special name "."
// returns a synthesised root directory whose ReadDir lists every
// lump.
func (f *FS) Open(name string) (fs.File, error) {
	if !fs.ValidPath(name) {
		return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrInvalid}
	}
	if name == "." {
		return f.rootDir(), nil
	}
	idx, ok := f.byName[cleanName(name)]
	if !ok {
		return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrNotExist}
	}
	l := f.lumps[idx]
	// The size is the directory's claim: fs.ReadFile sizes its buffer from it, so
	// a lump that says 2 GiB in a 1 KiB archive is refused here (one byte read at
	// its end proves the source holds it).
	if l.DiskSize < 0 || l.FilePos < 0 {
		return nil, &fs.PathError{Op: "open", Path: name, Err: ErrBadLump}
	}
	if l.DiskSize > 0 {
		var last [1]byte
		if n, _ := f.src.ReadAt(last[:], int64(l.FilePos)+int64(l.DiskSize)-1); n != 1 {
			return nil, &fs.PathError{Op: "open", Path: name, Err: ErrBadLump}
		}
	}
	return &file{
		fs:   f,
		name: l.Name,
		size: int64(l.DiskSize),
		base: int64(l.FilePos),
	}, nil
}

// ReadDir lists every lump in lexical order. tyrquake never
// enumerates lumps by subdirectory (the WAD2 format is flat), so
// only "." is accepted.
func (f *FS) ReadDir(name string) ([]fs.DirEntry, error) {
	if name != "." {
		return nil, &fs.PathError{Op: "readdir", Path: name, Err: fs.ErrNotExist}
	}
	names := make([]string, 0, len(f.lumps))
	for _, l := range f.lumps {
		names = append(names, l.Name)
	}
	sort.Strings(names)
	out := make([]fs.DirEntry, 0, len(names))
	for _, n := range names {
		l := f.lumps[f.byName[n]]
		out = append(out, &fileInfo{name: n, size: int64(l.DiskSize)})
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
func (d *rootDir) ReadDir(int) ([]fs.DirEntry, error) {
	if d.read {
		return nil, io.EOF
	}
	d.read = true
	return d.fs.ReadDir(".")
}
