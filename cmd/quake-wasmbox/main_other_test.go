// Copyright (c) 2026 the go-quake1/engine authors.
// SPDX-License-Identifier: BSD-3-Clause

//go:build !(js && wasm)

package main

import "testing"

func TestNativeMainRefusesToRun(t *testing.T) {
	var code int
	old := osExit
	defer func() { osExit = old }()
	osExit = func(c int) { code = c }
	main()
	if code != 2 {
		t.Errorf("exit code = %d, want 2", code)
	}
}
