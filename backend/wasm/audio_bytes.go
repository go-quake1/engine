// Copyright (c) 1996-1997 Id Software, Inc.
// Copyright (c) 2026 the go-quake1/engine authors.
// SPDX-License-Identifier: GPL-2.0-or-later

package wasm

import (
	"encoding/binary"
	"math"
	"unsafe"
)

// hostLittleEndian reports whether the host stores multi-byte values
// little-endian. It is a var (not a const) so tests can exercise the
// big-endian copy path on a little-endian host.
var hostLittleEndian = func() bool {
	var x uint16 = 1
	return *(*byte)(unsafe.Pointer(&x)) == 1
}()

// float32SliceAsBytes returns the float32 slice as little-endian bytes
// (4 bytes per element) -- the byte order the JS Float32Array expects in
// every browser we target, and the order wasm itself uses.
//
// On a little-endian host (every real build target, incl. js/wasm) the
// result aliases the input's backing array WITHOUT copying, so callers must
// keep the input alive until the bytes are consumed. On a big-endian host
// (only reached under the s390x cross-arch CI check) it returns a freshly
// little-endian-encoded copy, so the bytes are correct regardless of host
// byte order.
func float32SliceAsBytes(s []float32) []byte {
	if len(s) == 0 {
		return nil
	}
	if hostLittleEndian {
		return unsafe.Slice((*byte)(unsafe.Pointer(&s[0])), len(s)*4)
	}
	b := make([]byte, len(s)*4)
	for i, v := range s {
		binary.LittleEndian.PutUint32(b[i*4:], math.Float32bits(v))
	}
	return b
}
