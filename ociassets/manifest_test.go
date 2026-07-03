// Copyright (c) 2026 the go-quake1/engine authors.
// SPDX-License-Identifier: BSD-3-Clause

package ociassets

import (
	"errors"
	"strings"
	"testing"
)

func TestDecodeManifest_Ok(t *testing.T) {
	body := []byte(`{
	"schemaVersion": 2,
	"mediaType": "application/vnd.oci.image.manifest.v1+json",
	"config": {"mediaType": "application/vnd.go-quake1.config.v1+json", "digest": "sha256:abc", "size": 5},
	"layers": [
		{"mediaType": "application/vnd.go-quake1.pak.v1", "digest": "sha256:def", "size": 10}
	],
	"annotations": {"quake.path/pak0.pak": "sha256:def"}
}`)
	m, err := DecodeManifest(body)
	if err != nil {
		t.Fatalf("DecodeManifest: %v", err)
	}
	if m.SchemaVersion != 2 || len(m.Layers) != 1 || m.Annotations["quake.path/pak0.pak"] != "sha256:def" {
		t.Fatalf("DecodeManifest: shape = %#v", m)
	}
}

func TestDecodeManifest_BadJSON(t *testing.T) {
	if _, err := DecodeManifest([]byte("not-json")); err == nil {
		t.Fatal("DecodeManifest(garbage): want error, got nil")
	}
}

func TestDecodeManifest_BadSchemaVersion(t *testing.T) {
	_, err := DecodeManifest([]byte(`{"schemaVersion": 1}`))
	if err == nil || !strings.Contains(err.Error(), "schemaVersion must be 2") {
		t.Fatalf("DecodeManifest(v1): err = %v", err)
	}
}

func TestBuildFileMap_Ok(t *testing.T) {
	m := &Manifest{
		Annotations: map[string]string{
			"quake.path/pak0.pak":          "sha256:aaa",
			"quake.path/music/track02.ogg": "sha256:bbb",
			"org.other/key":                "ignored",
			"quake.path/":                  "skipped-empty-name",
			"quake.path/empty":             "",
		},
	}
	fm, err := BuildFileMap(m)
	if err != nil {
		t.Fatalf("BuildFileMap: %v", err)
	}
	if len(fm) != 2 || fm["pak0.pak"] != "sha256:aaa" || fm["music/track02.ogg"] != "sha256:bbb" {
		t.Fatalf("BuildFileMap: %v", fm)
	}
}

func TestBuildFileMap_Empty(t *testing.T) {
	m := &Manifest{
		Annotations: map[string]string{"org.other/key": "ignored"},
	}
	_, err := BuildFileMap(m)
	if !errors.Is(err, ErrManifestNoAnnotations) {
		t.Fatalf("BuildFileMap: err = %v want ErrManifestNoAnnotations", err)
	}
}

func TestEncodeManifest_RoundTrip(t *testing.T) {
	in := &Manifest{
		SchemaVersion: 2,
		MediaType:     MediaTypeManifest,
		Config:        Descriptor{MediaType: MediaTypeConfig, Digest: "sha256:c", Size: 1},
		Layers: []Descriptor{
			{MediaType: MediaTypeLayerPak, Digest: "sha256:d", Size: 2},
		},
		Annotations: map[string]string{AnnotationPathPrefix + "pak0.pak": "sha256:d"},
	}
	body, err := EncodeManifest(in)
	if err != nil {
		t.Fatalf("EncodeManifest: %v", err)
	}
	out, err := DecodeManifest(body)
	if err != nil {
		t.Fatalf("DecodeManifest(round-trip): %v", err)
	}
	if out.Layers[0].Digest != "sha256:d" || out.Annotations[AnnotationPathPrefix+"pak0.pak"] != "sha256:d" {
		t.Fatalf("round-trip mismatch: %#v", out)
	}
}
