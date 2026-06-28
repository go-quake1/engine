// Copyright (c) 1996-1997 Id Software, Inc.
// Copyright (c) 2026 the go-quake1/engine authors.
// SPDX-License-Identifier: BSD-3-Clause

package runner

import (
	"bytes"
	"io/fs"

	"github.com/go-quake1/engine/wad"
)

// wadOverlay wraps an [fs.FS] so that an Open miss on a `gfx/<name>.lmp`
// path transparently falls through to a lazily-parsed WAD2 archive
// (typically pak0:gfx/gfx.wad).
type wadOverlay struct {
	base    fs.FS
	wadPath string
	parsed  bool
	w       *wad.FS
	wadBlob []byte
}

// newWADOverlay returns an overlay rooted at base. The WAD itself is
// not opened until the first miss requires it.
func newWADOverlay(base fs.FS, wadPath string) *wadOverlay {
	return &wadOverlay{base: base, wadPath: wadPath}
}

// Open implements [fs.FS].
func (o *wadOverlay) Open(name string) (fs.File, error) {
	if o == nil || o.base == nil {
		return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrInvalid}
	}
	f, err := o.base.Open(name)
	if err == nil {
		return f, nil
	}
	lump, ok := wadLumpName(name)
	if !ok {
		return nil, err
	}
	w := o.openWAD()
	if w == nil {
		return nil, err
	}
	wf, werr := w.Open(lump)
	if werr != nil {
		return nil, err
	}
	return wf, nil
}

// openWAD lazily loads + parses the configured WAD path.
func (o *wadOverlay) openWAD() *wad.FS {
	if o.parsed {
		return o.w
	}
	o.parsed = true
	blob, ok := tryReadPakFile(o.base, o.wadPath)
	if !ok {
		return nil
	}
	o.wadBlob = blob
	w, err := wad.Open(bytes.NewReader(blob))
	if err != nil {
		return nil
	}
	o.w = w
	return o.w
}

// wadLumpName converts `gfx/<name>.lmp` into the bare WAD lump name.
func wadLumpName(name string) (string, bool) {
	const prefix = "gfx/"
	const suffix = ".lmp"
	if len(name) <= len(prefix)+len(suffix) {
		return "", false
	}
	if name[:len(prefix)] != prefix {
		return "", false
	}
	if name[len(name)-len(suffix):] != suffix {
		return "", false
	}
	return name[len(prefix) : len(name)-len(suffix)], true
}
