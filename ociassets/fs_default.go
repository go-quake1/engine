// Copyright (c) 2026 the go-quake1/engine authors.
// SPDX-License-Identifier: BSD-3-Clause

//go:build !js || !wasm

package ociassets

// persistCache is the cross-platform shape for [FS]'s second-level
// cache. On host builds the implementation is a no-op (lookups always
// miss; Puts are dropped) -- a real disk cache could be added later
// but the host path is mostly used for tests / CLI tooling where
// in-process LRU is enough.
type persistCache interface {
	Get(digest string) ([]byte, bool)
	Put(digest string, data []byte)
}

// noopPersistCache is the host build's persistCache. It exists as a
// concrete type (not a nil interface) so the FS struct field is
// always safe to call methods on, no nil-check needed at every Open.
type noopPersistCache struct{}

func (noopPersistCache) Get(string) ([]byte, bool) { return nil, false }
func (noopPersistCache) Put(digest string, data []byte) {
	// The host-side persistent cache is intentionally a no-op; the
	// _ = ... lines exist so the coverage tool registers a statement
	// the optimizer cannot inline away to "Put has no body".
	_ = digest
	_ = data
}

func defaultPersistCache() persistCache { return noopPersistCache{} }
