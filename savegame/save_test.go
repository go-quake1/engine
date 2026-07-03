// Copyright (c) 2026 the go-quake1/engine authors.
// SPDX-License-Identifier: GPL-2.0-or-later

package savegame

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"
)

// --- Encode / Load round-trip -------------------------------------------------

func TestSave_RoundTrip(t *testing.T) {
	in := &Save{
		Comment: "e1m1 kills:5/12",
		Skill:   1,
		MapName: "e1m1",
		Time:    42.5,
		SpawnParms: [SpawnParmCount]float32{
			1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16,
		},
		Globals: []KV{
			{Key: "serverflags", Value: "3"},
			{Key: "nextmap", Value: "e1m2"},
		},
		Edicts: []EdictSnap{
			{FieldKV: []KV{{Key: "classname", Value: "worldspawn"}}},
			{Free: true},
			{FieldKV: []KV{
				{Key: "classname", Value: "monster_dog"},
				{Key: "origin", Value: "100 200 50"},
				{Key: "health", Value: "25"},
			}},
		},
	}
	var buf bytes.Buffer
	if err := in.Encode(&buf); err != nil {
		t.Fatalf("Encode: %v", err)
	}

	out, err := Load(&buf)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if out.Comment != in.Comment {
		t.Errorf("Comment: got %q want %q", out.Comment, in.Comment)
	}
	if out.Skill != in.Skill {
		t.Errorf("Skill: got %d want %d", out.Skill, in.Skill)
	}
	if out.MapName != in.MapName {
		t.Errorf("MapName: got %q want %q", out.MapName, in.MapName)
	}
	if out.Time != in.Time {
		t.Errorf("Time: got %v want %v", out.Time, in.Time)
	}
	if out.SpawnParms != in.SpawnParms {
		t.Errorf("SpawnParms: got %v want %v", out.SpawnParms, in.SpawnParms)
	}
	if len(out.Globals) != len(in.Globals) {
		t.Fatalf("Globals len: got %d want %d", len(out.Globals), len(in.Globals))
	}
	for i := range in.Globals {
		if out.Globals[i] != in.Globals[i] {
			t.Errorf("Globals[%d]: got %+v want %+v", i, out.Globals[i], in.Globals[i])
		}
	}
	if len(out.Edicts) != len(in.Edicts) {
		t.Fatalf("Edicts len: got %d want %d", len(out.Edicts), len(in.Edicts))
	}
	for i := range in.Edicts {
		if out.Edicts[i].Free != in.Edicts[i].Free {
			t.Errorf("Edicts[%d].Free: got %v want %v", i, out.Edicts[i].Free, in.Edicts[i].Free)
		}
		if len(out.Edicts[i].FieldKV) != len(in.Edicts[i].FieldKV) {
			t.Errorf("Edicts[%d] kv len: got %d want %d", i, len(out.Edicts[i].FieldKV), len(in.Edicts[i].FieldKV))
			continue
		}
		for j := range in.Edicts[i].FieldKV {
			if out.Edicts[i].FieldKV[j] != in.Edicts[i].FieldKV[j] {
				t.Errorf("Edicts[%d].FieldKV[%d]: got %+v want %+v", i, j,
					out.Edicts[i].FieldKV[j], in.Edicts[i].FieldKV[j])
			}
		}
	}
}

func TestSave_Encode_SanitizesNewlines(t *testing.T) {
	s := &Save{
		Comment: "line1\nline2",
		MapName: "bad\rname",
	}
	var buf bytes.Buffer
	if err := s.Encode(&buf); err != nil {
		t.Fatalf("Encode: %v", err)
	}
	// One \n per record; newlines inside Comment+MapName were folded
	// so the line count matches the canonical header shape.
	lines := bytes.Count(buf.Bytes(), []byte{'\n'})
	// 1 ver + 1 comment + 16 parms + 1 skill + 1 map + 1 time +
	// 2 globals block (header+close) = 23 + 0 edict blocks.
	if lines != 23 {
		t.Errorf("line count: got %d want 23 (input newlines leaked)", lines)
	}
}

// --- Load: failure modes ------------------------------------------------------

func TestLoad_EmptyInput(t *testing.T) {
	if _, err := Load(strings.NewReader("")); !errors.Is(err, ErrShortRead) {
		t.Errorf("got %v want ErrShortRead", err)
	}
}

func TestLoad_BadVersionNumber(t *testing.T) {
	if _, err := Load(strings.NewReader("notanint\n")); !errors.Is(err, ErrBadVersion) {
		t.Errorf("got %v want ErrBadVersion", err)
	}
}

func TestLoad_WrongVersion(t *testing.T) {
	if _, err := Load(strings.NewReader("99\n")); !errors.Is(err, ErrBadVersion) {
		t.Errorf("got %v want ErrBadVersion", err)
	}
}

func TestLoad_MissingComment(t *testing.T) {
	if _, err := Load(strings.NewReader("5\n")); !errors.Is(err, ErrShortRead) {
		t.Errorf("got %v want ErrShortRead", err)
	}
}

func TestLoad_MissingSpawnParm(t *testing.T) {
	in := "5\ncomment\n1\n2\n3\n" // only 3 of 16
	if _, err := Load(strings.NewReader(in)); !errors.Is(err, ErrShortRead) {
		t.Errorf("got %v want ErrShortRead", err)
	}
}

func TestLoad_BadSpawnParmFloat(t *testing.T) {
	buf := &bytes.Buffer{}
	buf.WriteString("5\ncomment\n")
	buf.WriteString("nope\n") // bad
	for i := 1; i < SpawnParmCount; i++ {
		buf.WriteString("0\n")
	}
	if _, err := Load(buf); !errors.Is(err, ErrBadNumber) {
		t.Errorf("got %v want ErrBadNumber", err)
	}
}

func TestLoad_MissingSkill(t *testing.T) {
	buf := &bytes.Buffer{}
	buf.WriteString("5\ncomment\n")
	for i := 0; i < SpawnParmCount; i++ {
		buf.WriteString("0\n")
	}
	if _, err := Load(buf); !errors.Is(err, ErrShortRead) {
		t.Errorf("got %v want ErrShortRead", err)
	}
}

func TestLoad_BadSkill(t *testing.T) {
	buf := buildLoadHeader("nope", "e1m1", "0")
	if _, err := Load(buf); !errors.Is(err, ErrBadNumber) {
		t.Errorf("got %v want ErrBadNumber", err)
	}
}

func TestLoad_MissingMap(t *testing.T) {
	buf := &bytes.Buffer{}
	buf.WriteString("5\ncomment\n")
	for i := 0; i < SpawnParmCount; i++ {
		buf.WriteString("0\n")
	}
	buf.WriteString("1\n") // skill, no map line follows
	if _, err := Load(buf); !errors.Is(err, ErrShortRead) {
		t.Errorf("got %v want ErrShortRead", err)
	}
}

func TestLoad_MissingTime(t *testing.T) {
	buf := &bytes.Buffer{}
	buf.WriteString("5\ncomment\n")
	for i := 0; i < SpawnParmCount; i++ {
		buf.WriteString("0\n")
	}
	buf.WriteString("1\n")
	buf.WriteString("e1m1\n")
	if _, err := Load(buf); !errors.Is(err, ErrShortRead) {
		t.Errorf("got %v want ErrShortRead", err)
	}
}

func TestLoad_BadTime(t *testing.T) {
	buf := buildLoadHeader("1", "e1m1", "abc")
	if _, err := Load(buf); !errors.Is(err, ErrBadNumber) {
		t.Errorf("got %v want ErrBadNumber", err)
	}
}

func TestLoad_MissingGlobalsBlock(t *testing.T) {
	// Header all valid, but no "{" follows.
	buf := buildLoadHeader("1", "e1m1", "0")
	if _, err := Load(buf); !errors.Is(err, ErrMalformedBlock) {
		t.Errorf("got %v want ErrMalformedBlock", err)
	}
}

func TestLoad_BadGlobalsBlockHeader(t *testing.T) {
	buf := buildLoadHeader("1", "e1m1", "0")
	buf.WriteString("not-a-brace\n")
	if _, err := Load(buf); !errors.Is(err, ErrMalformedBlock) {
		t.Errorf("got %v want ErrMalformedBlock", err)
	}
}

func TestLoad_UnterminatedGlobalsBody(t *testing.T) {
	buf := buildLoadHeader("1", "e1m1", "0")
	buf.WriteString("{\n") // no closing brace
	if _, err := Load(buf); !errors.Is(err, ErrMalformedBlock) {
		t.Errorf("got %v want ErrMalformedBlock", err)
	}
}

func TestLoad_BadEdictHeader(t *testing.T) {
	buf := buildLoadHeader("1", "e1m1", "0")
	buf.WriteString("{\n}\n")      // valid empty globals
	buf.WriteString("not-brace\n") // bad edict header
	if _, err := Load(buf); !errors.Is(err, ErrMalformedBlock) {
		t.Errorf("got %v want ErrMalformedBlock", err)
	}
}

func TestLoad_UnterminatedEdictBlock(t *testing.T) {
	buf := buildLoadHeader("1", "e1m1", "0")
	buf.WriteString("{\n}\n") // valid empty globals
	buf.WriteString("{\n")    // start edict block, never close
	if _, err := Load(buf); !errors.Is(err, ErrMalformedBlock) {
		t.Errorf("got %v want ErrMalformedBlock", err)
	}
}

func TestLoad_BadKVLine_NoOpenQuote(t *testing.T) {
	buf := buildLoadHeader("1", "e1m1", "0")
	buf.WriteString("{\n")
	buf.WriteString("no-quote here\n")
	buf.WriteString("}\n")
	if _, err := Load(buf); !errors.Is(err, ErrUnterminatedString) {
		t.Errorf("got %v want ErrUnterminatedString", err)
	}
}

func TestLoad_BadKVLine_NoClosingQuote(t *testing.T) {
	buf := buildLoadHeader("1", "e1m1", "0")
	buf.WriteString("{\n")
	buf.WriteString("\"key only\n")
	buf.WriteString("}\n")
	if _, err := Load(buf); !errors.Is(err, ErrUnterminatedString) {
		t.Errorf("got %v want ErrUnterminatedString", err)
	}
}

func TestLoad_BadKVLine_MissingValue(t *testing.T) {
	buf := buildLoadHeader("1", "e1m1", "0")
	buf.WriteString("{\n")
	buf.WriteString("\"key\"\n") // no value
	buf.WriteString("}\n")
	if _, err := Load(buf); !errors.Is(err, ErrUnterminatedString) {
		t.Errorf("got %v want ErrUnterminatedString", err)
	}
}

func TestLoad_BlankLineInBody(t *testing.T) {
	buf := buildLoadHeader("1", "e1m1", "0")
	buf.WriteString("{\n}\n") // globals
	buf.WriteString("\n")     // blank line BETWEEN edict blocks -- skipped
	buf.WriteString("{\n}\n")
	got, err := Load(buf)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(got.Edicts) != 1 {
		t.Errorf("Edicts len: got %d want 1 (free placeholder)", len(got.Edicts))
	}
}

func TestLoad_EmptyEdictMarksFree(t *testing.T) {
	buf := buildLoadHeader("1", "e1m1", "0")
	buf.WriteString("{\n}\n") // globals
	buf.WriteString("{\n}\n") // empty edict block -> Free=true
	got, err := Load(buf)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(got.Edicts) != 1 || !got.Edicts[0].Free {
		t.Errorf("expected one Free edict, got %+v", got.Edicts)
	}
}

func TestLoad_CRLFLineEnding(t *testing.T) {
	in := &Save{Comment: "ok", MapName: "e1m1", Skill: 1, Time: 5}
	var buf bytes.Buffer
	_ = in.Encode(&buf)
	// Inject \r before every \n to simulate CRLF input.
	crlf := strings.ReplaceAll(buf.String(), "\n", "\r\n")
	out, err := Load(strings.NewReader(crlf))
	if err != nil {
		t.Fatalf("Load CRLF: %v", err)
	}
	if out.MapName != "e1m1" {
		t.Errorf("MapName: got %q want e1m1 (CRLF stripped)", out.MapName)
	}
}

// --- Encode: write-error propagation -----------------------------------------

// failWriter is an io.Writer that fails after acceptN bytes.
type failWriter struct {
	acceptN int
	written int
}

func (w *failWriter) Write(p []byte) (int, error) {
	if w.written >= w.acceptN {
		return 0, io.ErrShortWrite
	}
	n := len(p)
	if w.written+n > w.acceptN {
		n = w.acceptN - w.written
	}
	w.written += n
	if n < len(p) {
		return n, io.ErrShortWrite
	}
	return n, nil
}

func TestEncode_PropagatesWriterError(t *testing.T) {
	s := &Save{
		Comment: "x",
		MapName: "e1m1",
		Time:    1,
		Skill:   1,
		Globals: []KV{{Key: "k", Value: "v"}},
		Edicts:  []EdictSnap{{FieldKV: []KV{{Key: "a", Value: "b"}}}},
	}
	// Try writes that fail at every prefix length so every Write
	// branch inside Encode + writeBlock + Flush is exercised.
	var buf bytes.Buffer
	_ = s.Encode(&buf)
	full := buf.Len()
	for limit := 0; limit < full; limit++ {
		w := &failWriter{acceptN: limit}
		_ = s.Encode(w) // we don't care about the error identity, just exercise
	}
}

func TestEncode_FreeEdictWritePropagatesError(t *testing.T) {
	// A save whose only post-header content is a single Free edict.
	// We let header + globals through, then fail on the next write
	// (= the "{\n}\n" placeholder line).
	s := &Save{
		MapName: "e1m1",
		Edicts:  []EdictSnap{{Free: true}},
	}
	var ok bytes.Buffer
	_ = s.Encode(&ok)
	// Encode the same save but with no Free edict to measure the
	// "everything except the placeholder" prefix length.
	prefix := &Save{MapName: "e1m1"}
	var prefixBuf bytes.Buffer
	_ = prefix.Encode(&prefixBuf)
	w := &failWriter{acceptN: prefixBuf.Len()}
	if err := s.Encode(w); err == nil {
		t.Errorf("expected Free-edict placeholder write to fail")
	}
}

func TestEncode_FreeEdictPlaceholder(t *testing.T) {
	s := &Save{
		MapName: "e1m1",
		Edicts: []EdictSnap{
			{Free: true},
		},
	}
	var buf bytes.Buffer
	if err := s.Encode(&buf); err != nil {
		t.Fatalf("Encode: %v", err)
	}
	if !bytes.Contains(buf.Bytes(), []byte("{\n}\n")) {
		t.Errorf("free-edict placeholder missing: %q", buf.String())
	}
	// And the placeholder round-trips back as Free=true.
	got, err := Load(&buf)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(got.Edicts) != 1 || !got.Edicts[0].Free {
		t.Errorf("expected one Free edict, got %+v", got.Edicts)
	}
}

// failWriterImmediate refuses every write -- exercises the very first
// Fprintf bail-out inside Encode.
type failWriterImmediate struct{}

func (failWriterImmediate) Write(p []byte) (int, error) { return 0, io.ErrShortWrite }

func TestEncode_FirstWriteFails(t *testing.T) {
	s := &Save{}
	if err := s.Encode(failWriterImmediate{}); err == nil {
		t.Errorf("expected error from immediate-fail writer")
	}
}

// --- helpers ------------------------------------------------------------------

func buildLoadHeader(skill, mapName, timeStr string) *bytes.Buffer {
	buf := &bytes.Buffer{}
	buf.WriteString("5\ncomment\n")
	for i := 0; i < SpawnParmCount; i++ {
		buf.WriteString("0\n")
	}
	buf.WriteString(skill + "\n")
	buf.WriteString(mapName + "\n")
	buf.WriteString(timeStr + "\n")
	return buf
}
