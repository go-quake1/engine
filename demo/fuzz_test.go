// Copyright (c) 2026 the go-quake1/engine authors.
// SPDX-License-Identifier: GPL-2.0-or-later

package demo

import (
	"bytes"
	"encoding/binary"
	"errors"
	"os"
	"testing"

	"github.com/go-quake1/engine/internal/fuzzcheck"
)

// demoSeeds are the committed seeds (testdata/fuzz/FuzzParse, written by
// TestWriteFuzzSeeds): a header and ticks as EncodeHeader and EncodeTick write
// them, cuts of them and corrupted lengths, all synthetic.
func demoSeeds() map[string][]byte {
	enc := func(track string, ticks ...DemoTick) []byte {
		var b bytes.Buffer
		_ = EncodeHeader(&b, track)
		for _, tk := range ticks {
			_ = EncodeTick(&b, tk)
		}
		return b.Bytes()
	}
	valid := enc("1", DemoTick{Message: []byte("hello"), ViewAngles: [3]float32{1, 2, 3}},
		DemoTick{Message: []byte{}}, DemoTick{Message: bytes.Repeat([]byte{9}, 100), ViewAngles: [3]float32{-1, 0, 90}})
	put := func(b []byte, off int, v uint32) []byte {
		c := bytes.Clone(b)
		binary.LittleEndian.PutUint32(c[off:], v)
		return c
	}
	// The first tick starts after the header ("1\n", 2 bytes).
	return map[string][]byte{
		"valid":            valid,
		"no-ticks":         enc("-1"),
		"cut-body":         valid[:len(valid)-20],
		"cut-tick-header":  valid[:2+7],
		"cut-header":       []byte("12345"),
		"empty":            {},
		"header-too-long":  []byte("0123456789abcdef\n"),
		"msglen-max":       put(valid, 2, MaxDemoMessageLen),
		"msglen-over-max":  put(valid, 2, MaxDemoMessageLen+1),
		"msglen-huge":      put(valid, 2, 0x7fffffff),
		"msglen-negative":  put(valid, 2, 0xfffffff0),
		"many-empty-ticks": append([]byte("\n"), bytes.Repeat(make([]byte, tickHeaderBytes), 500)...),
	}
}

func TestWriteFuzzSeeds(t *testing.T) {
	if os.Getenv("WRITE_FUZZ_SEEDS") != "1" {
		t.Skip("WRITE_FUZZ_SEEDS=1 writes the seed corpus")
	}
	for name, data := range demoSeeds() {
		if err := fuzzcheck.WriteSeed("testdata/fuzz", "FuzzParse", name, data); err != nil {
			t.Fatal(err)
		}
	}
}

// FuzzParse: demo.Parse (header, then every tic) on any bytes: no panic, and no
// allocation above fuzzcheck.Limit however a tic's length reads (at most
// MaxDemoMessageLen is made for it, and freed on a short read); a demo that
// parses re-encodes to the bytes it was parsed from.
func FuzzParse(f *testing.F) {
	for _, s := range demoSeeds() {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		fuzzcheck.Bounded(t, len(data), func() {
			header, ticks, err := Parse(bytes.NewReader(data))
			if err != nil {
				return
			}
			var b bytes.Buffer
			if err := EncodeHeader(&b, header); err != nil {
				t.Fatalf("a parsed header does not encode: %v", err)
			}
			for _, tk := range ticks {
				if err := EncodeTick(&b, tk); err != nil {
					t.Fatalf("a parsed tic does not encode: %v", err)
				}
			}
			// ViewAngles NaNs keep their bits through Float32bits, so the bytes agree.
			if !bytes.Equal(b.Bytes(), data) {
				t.Fatalf("re-encoded %d bytes differ from the %d parsed", b.Len(), len(data))
			}
		})
	})
}

// A valid seed parses into its ticks: parse then valid output.
func TestFuzzSeedValidParses(t *testing.T) {
	h, ticks, err := Parse(bytes.NewReader(demoSeeds()["valid"]))
	if err != nil || h != "1" || len(ticks) != 3 || string(ticks[0].Message) != "hello" || len(ticks[2].Message) != 100 {
		t.Fatalf("%q, %d ticks, %v", h, len(ticks), err)
	}
}

// The hostile seeds are refused with an error, not parsed and not a panic.
func TestHostileDemosAreRefused(t *testing.T) {
	seeds := demoSeeds()
	for _, name := range []string{"cut-body", "cut-tick-header", "cut-header", "empty", "header-too-long", "msglen-max", "msglen-over-max", "msglen-huge", "msglen-negative"} {
		if _, _, err := Parse(bytes.NewReader(seeds[name])); err == nil {
			t.Errorf("%s: parsed", name)
		}
	}
	if _, _, err := Parse(bytes.NewReader(seeds["msglen-over-max"])); !errors.Is(err, ErrDemoNegMsgLen) {
		t.Errorf("msglen-over-max: %v, want ErrDemoNegMsgLen", err)
	}
}
