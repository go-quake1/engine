// Copyright (c) 2026 the go-quake1/engine authors.
// SPDX-License-Identifier: BSD-3-Clause

package runner

import "testing"

func TestParsePlayerStart(t *testing.T) {
	cases := []struct {
		name       string
		blob       string
		wantOK     bool
		wantOrigin [3]float32
		wantYaw    float32
	}{
		{
			name:       "origin and angle",
			blob:       `{ "classname" "worldspawn" }{ "classname" "info_player_start" "origin" "0 -160 424" "angle" "90" }`,
			wantOK:     true,
			wantOrigin: [3]float32{0, -160, 424},
			wantYaw:    90,
		},
		{
			name:       "missing angle defaults to zero",
			blob:       `{ "classname" "info_player_start" "origin" "16 32 -8" }`,
			wantOK:     true,
			wantOrigin: [3]float32{16, 32, -8},
			wantYaw:    0,
		},
		{
			name:   "no player start",
			blob:   `{ "classname" "worldspawn" }{ "classname" "light" "origin" "1 2 3" }`,
			wantOK: false,
		},
		{
			name:   "malformed origin is skipped",
			blob:   `{ "classname" "info_player_start" "origin" "nope" }`,
			wantOK: false,
		},
		{
			name:   "unparseable blob",
			blob:   `{ "classname"`,
			wantOK: false,
		},
		{
			name:       "first of several wins",
			blob:       `{ "classname" "info_player_start" "origin" "1 1 1" "angle" "45" }{ "classname" "info_player_start" "origin" "9 9 9" }`,
			wantOK:     true,
			wantOrigin: [3]float32{1, 1, 1},
			wantYaw:    45,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			origin, yaw, ok := parsePlayerStart([]byte(tc.blob))
			if ok != tc.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tc.wantOK)
			}
			if !tc.wantOK {
				return
			}
			if origin != tc.wantOrigin {
				t.Errorf("origin = %v, want %v", origin, tc.wantOrigin)
			}
			if yaw != tc.wantYaw {
				t.Errorf("yaw = %v, want %v", yaw, tc.wantYaw)
			}
		})
	}
}
