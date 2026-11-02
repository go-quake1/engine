// Copyright (c) 2026 the go-quake1/engine authors.
// SPDX-License-Identifier: GPL-2.0-or-later

//go:build !js

// quake-presentcheck is the browser test of the WebGPU presenter; it runs
// under js/wasm only (build it with GOOS=js GOARCH=wasm and run it in a
// browser: see present_test.go).
package main

import (
	"fmt"
	"os"
)

var osExit = os.Exit

func main() {
	fmt.Fprintln(os.Stderr, "quake-presentcheck: build with GOOS=js GOARCH=wasm and run it in a browser")
	osExit(2)
}
