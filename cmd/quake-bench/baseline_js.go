// Copyright (c) 2026 the go-quake1/engine authors.
// SPDX-License-Identifier: GPL-2.0-or-later

//go:build js

package main

import "github.com/spf13/cobra"

// addBaseline is a no-op under js/wasm: the baseline driver runs natively.
func addBaseline(*cobra.Command) {}

// addPresent is a no-op under js/wasm too.
func addPresent(*cobra.Command) {}
