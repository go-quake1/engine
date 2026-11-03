// Copyright (c) 2026 the go-quake1/engine authors.
// SPDX-License-Identifier: BSD-3-Clause

//go:build !(js && wasm)

// quake-wasmbox runs under js/wasm only (see main.go). This native main
// exists so that `go install github.com/go-quake1/engine/cmd/...` links:
// without it the package has no main function off wasm.
package main

import (
	"fmt"
	"os"
)

var osExit = os.Exit

func main() {
	fmt.Fprintln(os.Stderr, "quake-wasmbox: build with GOOS=js GOARCH=wasm and load it in a wasmbox worker")
	osExit(2)
}
