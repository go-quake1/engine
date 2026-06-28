// Copyright (c) 2026 the go-quake1/engine authors.
// SPDX-License-Identifier: BSD-3-Clause

//go:build js && wasm

package ociassets

import (
	"syscall/js"
)

// persistCache is the cross-platform shape (mirrors fs_default.go).
type persistCache interface {
	Get(digest string) ([]byte, bool)
	Put(digest string, data []byte)
}

// indexedDBCache is a write-through IndexedDB cache. Lookups + writes
// are asynchronous in the IndexedDB API but the wasm runtime is
// single-threaded so we use the synchronous-feeling Go channel pattern
// (the cache goroutine waits on a JS callback that re-enters via
// js.FuncOf).
//
// Object store layout:
//
//	database: "quake-oci-assets"  version: 1
//	store:    "blobs"             key: digest (string), value: Uint8Array
//
// First-time setup creates the database on demand. If the browser
// blocks IndexedDB (private mode, storage quota), Get/Put silently
// degrade to a no-op so the in-memory cache + network fetch still
// work; we never let a cache failure abort an Open.
type indexedDBCache struct {
	dbName    string
	storeName string
}

func defaultPersistCache() persistCache {
	return &indexedDBCache{dbName: "quake-oci-assets", storeName: "blobs"}
}

// openDB opens (or creates) the IndexedDB database synchronously
// from Go's POV. The bridge spins on a channel waiting for the
// IDBOpenDBRequest's onsuccess / onupgradeneeded / onerror callbacks.
// Returns a JS object representing the IDBDatabase, or js.Undefined
// on any failure (caller treats undefined as "cache unavailable").
func (c *indexedDBCache) openDB() js.Value {
	idb := js.Global().Get("indexedDB")
	if idb.IsUndefined() || idb.IsNull() {
		return js.Undefined()
	}
	req := idb.Call("open", c.dbName, 1)
	done := make(chan js.Value, 1)
	upgrade := js.FuncOf(func(this js.Value, args []js.Value) any {
		db := req.Get("result")
		// IDBDatabase.objectStoreNames is a DOMStringList PROPERTY, not a
		// method -- call Get, not Call. The prior `db.Call(...)` form
		// raised "property objectStoreNames is not a function" during the
		// first OCI fetch + killed the worker before runner.Setup could
		// even reach the pak0.pak Read.
		if !db.IsUndefined() && !db.Get("objectStoreNames").Call("contains", c.storeName).Bool() {
			db.Call("createObjectStore", c.storeName)
		}
		return nil
	})
	success := js.FuncOf(func(this js.Value, args []js.Value) any {
		done <- req.Get("result")
		return nil
	})
	failure := js.FuncOf(func(this js.Value, args []js.Value) any {
		done <- js.Undefined()
		return nil
	})
	defer upgrade.Release()
	defer success.Release()
	defer failure.Release()
	req.Set("onupgradeneeded", upgrade)
	req.Set("onsuccess", success)
	req.Set("onerror", failure)
	req.Set("onblocked", failure)
	return <-done
}

// Get reads digest's bytes back out of the object store. Misses (no
// such key, store missing, JS disabled) return (nil, false).
func (c *indexedDBCache) Get(digest string) ([]byte, bool) {
	db := c.openDB()
	if db.IsUndefined() {
		return nil, false
	}
	tx := db.Call("transaction", c.storeName, "readonly")
	store := tx.Call("objectStore", c.storeName)
	req := store.Call("get", digest)
	done := make(chan js.Value, 1)
	success := js.FuncOf(func(this js.Value, args []js.Value) any {
		done <- req.Get("result")
		return nil
	})
	failure := js.FuncOf(func(this js.Value, args []js.Value) any {
		done <- js.Undefined()
		return nil
	})
	defer success.Release()
	defer failure.Release()
	req.Set("onsuccess", success)
	req.Set("onerror", failure)
	result := <-done
	if result.IsUndefined() || result.IsNull() {
		return nil, false
	}
	n := result.Get("byteLength").Int()
	out := make([]byte, n)
	js.CopyBytesToGo(out, result)
	return out, true
}

// Put writes digest's bytes into the object store. Errors are
// swallowed (cache is best-effort -- the next page load will refetch
// if the write didn't stick).
func (c *indexedDBCache) Put(digest string, data []byte) {
	db := c.openDB()
	if db.IsUndefined() {
		return
	}
	tx := db.Call("transaction", c.storeName, "readwrite")
	store := tx.Call("objectStore", c.storeName)
	// Copy Go bytes into a JS Uint8Array for storage. The temporary
	// js.CopyBytesToJS path is allocation-heavy but the call sites
	// are once-per-asset (pak / 10 OGGs) so the cost is paid at boot.
	jsArr := js.Global().Get("Uint8Array").New(len(data))
	js.CopyBytesToJS(jsArr, data)
	store.Call("put", jsArr, digest)
	done := make(chan struct{}, 1)
	complete := js.FuncOf(func(this js.Value, args []js.Value) any {
		done <- struct{}{}
		return nil
	})
	failure := js.FuncOf(func(this js.Value, args []js.Value) any {
		done <- struct{}{}
		return nil
	})
	defer complete.Release()
	defer failure.Release()
	tx.Set("oncomplete", complete)
	tx.Set("onerror", failure)
	tx.Set("onabort", failure)
	<-done
}
