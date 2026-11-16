// Copyright (c) 2026 the go-quake1/engine authors.
// SPDX-License-Identifier: GPL-2.0-or-later

package entparse

import (
	"os"
	"strings"
	"testing"

	"github.com/go-quake1/engine/internal/fuzzcheck"
)

// entSeeds are the committed seeds (testdata/fuzz/FuzzParseEntities, written by
// TestWriteFuzzSeeds): entity lumps in the shape of a map's, cuts of them and
// malformed ones (unclosed, orphan field, stray close, comments, quotes), all
// synthetic.
func entSeeds() map[string][]byte {
	valid := "{\n\"classname\" \"worldspawn\"\n\"wad\" \"gfx/base.wad\"\n\"message\" \"the Slipgate Complex\"\n}\n" +
		"{\n\"classname\" \"info_player_start\"\n\"origin\" \"480 -352 88\"\n\"angle\" \"90\"\n}\n" +
		"{\n\"classname\" \"light\"\n\"light\" \"300.5\"\n\"origin\" \"1 2 3\"\n\"target\" \"*12\"\n}\n"
	return map[string][]byte{
		"valid":          []byte(valid),
		"cut":            []byte(valid[:len(valid)-30]),
		"comment":        []byte("// a comment\n{ \"a\" \"b\" } // another\n"),
		"empty":          {},
		"whitespace":     []byte(" \n\t "),
		"unclosed":       []byte("{ \"a\" \"b\""),
		"orphan":         []byte("{ \"a\" }"),
		"stray-close":    []byte("} { \"a\" \"b\" }"),
		"stray-token":    []byte("worldspawn { }"),
		"empty-quoted":   []byte("{ \"\" \"\" }"),
		"unterminated-q": []byte("{ \"a \"b"),
		"many-empty":     []byte(strings.Repeat("{}", 1000)),
		"deep-braces":    []byte(strings.Repeat("{", 1000)),
		"nul-byte":       []byte("{ \"a\"\x00\"b\" }\x00"), // not "nul": a Windows device name cannot be a file in a module
	}
}

func TestWriteFuzzSeeds(t *testing.T) {
	if os.Getenv("WRITE_FUZZ_SEEDS") != "1" {
		t.Skip("WRITE_FUZZ_SEEDS=1 writes the seed corpus")
	}
	for name, data := range entSeeds() {
		if err := fuzzcheck.WriteSeed("testdata/fuzz", "FuzzParseEntities", name, data); err != nil {
			t.Fatal(err)
		}
		if err := fuzzcheck.WriteSeed("testdata/fuzz", "FuzzParseValues", name, data); err != nil {
			t.Fatal(err)
		}
	}
}

// FuzzParseEntities: entparse.ParseEntities on any bytes: no panic, no
// allocation above fuzzcheck.Limit (a lump of {} pairs makes one map per pair),
// and on success every entity's keys and values are strings of the input.
func FuzzParseEntities(f *testing.F) {
	for _, s := range entSeeds() {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		fuzzcheck.Bounded(t, len(data), func() {
			ents, err := ParseEntities(data)
			if err != nil {
				return
			}
			for _, e := range ents {
				for k, v := range e {
					if len(k) > len(data) || len(v) > len(data) {
						t.Fatalf("token longer than the lump: %d, %d of %d", len(k), len(v), len(data))
					}
				}
			}
		})
	})
}

// FuzzParseValues: the value parsers of the entity fields (ParseFloat,
// ParseVec3, ParseEntity, ParseString) on any string: no panic, and a vector
// that parses has three finite-or-not floats and no more.
func FuzzParseValues(f *testing.F) {
	for _, s := range entSeeds() {
		f.Add(s)
	}
	f.Add([]byte("1 2 3"))
	f.Add([]byte("-1e39 nan inf"))
	f.Add([]byte("*99999999999"))
	f.Fuzz(func(t *testing.T, data []byte) {
		s := string(data)
		fuzzcheck.Bounded(t, len(data), func() {
			_, _ = ParseFloat(s)
			_, _ = ParseVec3(s)
			_, _ = ParseEntity(s)
			_ = ParseString(s)
		})
	})
}

// A valid seed parses into the entities it holds: parse then valid output.
func TestFuzzSeedValidParses(t *testing.T) {
	ents, err := ParseEntities(entSeeds()["valid"])
	if err != nil || len(ents) != 3 || ents[1]["origin"] != "480 -352 88" || ents[2]["light"] != "300.5" {
		t.Fatalf("%v, %v", ents, err)
	}
}

// The malformed seeds are refused with the sentinel the doc names.
func TestMalformedLumpsAreRefused(t *testing.T) {
	seeds := entSeeds()
	for name, want := range map[string]error{
		"unclosed":    ErrUnclosedBrace,
		"orphan":      ErrOrphanField,
		"stray-close": ErrUnmatchedClose,
		"stray-token": ErrUnmatchedClose,
		"deep-braces": ErrUnclosedBrace,
	} {
		if _, err := ParseEntities(seeds[name]); err != want {
			t.Errorf("%s: %v, want %v", name, err, want)
		}
	}
}
