// Copyright (c) 2026 the go-quake1/engine authors.
// SPDX-License-Identifier: GPL-2.0-or-later

//go:build !js

// quake-worldcheck is the browser test of the GPU world; it runs under
// js/wasm only (see world_test.go).
package main

import (
	"fmt"
	"os"
)

var osExit = os.Exit

func main() {
	fmt.Fprintln(os.Stderr, "quake-worldcheck: build with GOOS=js GOARCH=wasm and run it in a browser")
	osExit(2)
}
