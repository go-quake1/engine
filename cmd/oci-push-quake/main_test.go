// Copyright (c) 2026 the go-quake1/engine authors.
// SPDX-License-Identifier: BSD-3-Clause

package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// ----- fixture helpers ---------------------------------------------

// digestOf returns the canonical "sha256:<hex>" digest of b.
func digestOf(b []byte) string {
	sum := sha256.Sum256(b)
	return "sha256:" + fmt.Sprintf("%x", sum)
}

// writeBlob writes b into the OCI image-layout blobs/<algo>/<hex>
// path under layoutDir and returns its digest.
func writeBlob(t *testing.T, layoutDir string, b []byte) string {
	t.Helper()
	dig := digestOf(b)
	algo, hex, _ := strings.Cut(dig, ":")
	dir := filepath.Join(layoutDir, "blobs", algo)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir blobs: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, hex), b, 0o644); err != nil {
		t.Fatalf("write blob: %v", err)
	}
	return dig
}

// buildLayout writes a minimal but valid OCI image layout (index ->
// manifest -> config + one layer) into a fresh temp dir and returns
// the dir.
func buildLayout(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()

	configBlob := []byte(`{"architecture":"amd64","os":"linux"}`)
	layerBlob := []byte("PACK\x00\x00\x00\x00fake-pak-bytes")
	configDig := writeBlob(t, dir, configBlob)
	layerDig := writeBlob(t, dir, layerBlob)

	mani := ociManifest{
		SchemaVersion: 2,
		MediaType:     "application/vnd.oci.image.manifest.v1+json",
		Config: ociDescriptor{
			MediaType: "application/vnd.oci.image.config.v1+json",
			Digest:    configDig,
			Size:      int64(len(configBlob)),
		},
		Layers: []ociDescriptor{{
			MediaType: "application/vnd.quake.pak.layer.v1",
			Digest:    layerDig,
			Size:      int64(len(layerBlob)),
		}},
	}
	maniBlob, err := json.Marshal(mani)
	if err != nil {
		t.Fatalf("marshal manifest: %v", err)
	}
	maniDig := writeBlob(t, dir, maniBlob)

	idx := ociIndex{
		SchemaVersion: 2,
		Manifests: []ociIndexManifestEntry{{
			MediaType: mani.MediaType,
			Digest:    maniDig,
			Size:      int64(len(maniBlob)),
		}},
	}
	idxBlob, err := json.Marshal(idx)
	if err != nil {
		t.Fatalf("marshal index: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "index.json"), idxBlob, 0o644); err != nil {
		t.Fatalf("write index.json: %v", err)
	}
	return dir
}

// fakeRegistry is an in-memory OCI Distribution v2 endpoint. headOK
// controls whether HEAD blobs report 200 (already present) or 404.
type fakeRegistry struct {
	mu       sync.Mutex
	headOK   bool // when true, HEAD blob -> 200 (skip upload)
	puts     int  // blob PUTs observed
	manifest []byte
	// fault knobs (default 0/"" = behave correctly)
	postStatus       int  // override POST uploads status (0 -> 202)
	noLocation       bool // POST uploads omits Location header
	putBlobStatus    int  // override PUT blob status (0 -> 201)
	manifStatus      int  // override PUT manifest status (0 -> 201)
	locationOverride string
}

func (f *fakeRegistry) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	switch {
	case r.Method == http.MethodHead && strings.Contains(r.URL.Path, "/blobs/"):
		if f.headOK {
			w.WriteHeader(http.StatusOK)
		} else {
			w.WriteHeader(http.StatusNotFound)
		}
	case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/blobs/uploads/"):
		if !f.noLocation {
			loc := f.locationOverride
			if loc == "" {
				loc = r.URL.Path + "abc123" // relative; pusher prefixes baseURL
			}
			w.Header().Set("Location", loc)
		}
		st := f.postStatus
		if st == 0 {
			st = http.StatusAccepted
		}
		w.WriteHeader(st)
	case r.Method == http.MethodPut && strings.Contains(r.URL.Path, "/blobs/"):
		f.puts++
		io.Copy(io.Discard, r.Body)
		st := f.putBlobStatus
		if st == 0 {
			st = http.StatusCreated
		}
		w.WriteHeader(st)
	case r.Method == http.MethodPut && strings.Contains(r.URL.Path, "/manifests/"):
		body, _ := io.ReadAll(r.Body)
		f.manifest = body
		st := f.manifStatus
		if st == 0 {
			st = http.StatusCreated
		}
		w.WriteHeader(st)
	default:
		w.WriteHeader(http.StatusNotImplemented)
	}
}

// refFor turns an httptest.Server URL into a "registry/repo:tag" ref.
func refFor(srv *httptest.Server, repo, tag string) string {
	host := strings.TrimPrefix(srv.URL, "http://")
	return host + "/" + repo + ":" + tag
}

// ----- run() integration tests -------------------------------------

func TestRun_HappyPush(t *testing.T) {
	reg := &fakeRegistry{}
	srv := httptest.NewServer(reg)
	defer srv.Close()
	dir := buildLayout(t)

	var out, errb bytes.Buffer
	code := run([]string{"-in", dir, "-ref", refFor(srv, "quake-assets", "1.0")}, &out, &errb)
	if code != 0 {
		t.Fatalf("run code = %d, stderr=%q", code, errb.String())
	}
	if reg.puts != 2 {
		t.Errorf("blob PUTs = %d, want 2 (config + 1 layer)", reg.puts)
	}
	if len(reg.manifest) == 0 {
		t.Errorf("manifest not received")
	}
	if !strings.Contains(out.String(), "pushed manifest") {
		t.Errorf("stdout missing 'pushed manifest': %q", out.String())
	}
}

func TestRun_BlobsAlreadyPresent(t *testing.T) {
	reg := &fakeRegistry{headOK: true}
	srv := httptest.NewServer(reg)
	defer srv.Close()
	dir := buildLayout(t)

	var out, errb bytes.Buffer
	code := run([]string{"-in", dir, "-ref", refFor(srv, "quake-assets", "1.0")}, &out, &errb)
	if code != 0 {
		t.Fatalf("run code = %d, stderr=%q", code, errb.String())
	}
	if reg.puts != 0 {
		t.Errorf("blob PUTs = %d, want 0 (all already present)", reg.puts)
	}
	if !strings.Contains(out.String(), "already present") {
		t.Errorf("stdout missing 'already present': %q", out.String())
	}
}

func TestRun_DefaultTag(t *testing.T) {
	reg := &fakeRegistry{}
	srv := httptest.NewServer(reg)
	defer srv.Close()
	dir := buildLayout(t)
	host := strings.TrimPrefix(srv.URL, "http://")

	var out, errb bytes.Buffer
	code := run([]string{"-in", dir, "-ref", host + "/quake-assets"}, &out, &errb)
	if code != 0 {
		t.Fatalf("run code = %d, stderr=%q", code, errb.String())
	}
	if !strings.Contains(out.String(), ":latest") {
		t.Errorf("default tag not 'latest': %q", out.String())
	}
}

func TestRun_FlagParseError(t *testing.T) {
	var out, errb bytes.Buffer
	code := run([]string{"-nope"}, &out, &errb)
	if code != 2 {
		t.Fatalf("run code = %d, want 2", code)
	}
}

func TestRun_MissingRef(t *testing.T) {
	var out, errb bytes.Buffer
	code := run([]string{"-in", t.TempDir()}, &out, &errb)
	if code != 2 || !strings.Contains(errb.String(), "-ref is required") {
		t.Fatalf("code=%d err=%q", code, errb.String())
	}
}

func TestRun_BadRef(t *testing.T) {
	var out, errb bytes.Buffer
	code := run([]string{"-ref", "no-slash-here"}, &out, &errb)
	if code != 2 || !strings.Contains(errb.String(), "bad -ref") {
		t.Fatalf("code=%d err=%q", code, errb.String())
	}
}

func TestRun_MissingIndex(t *testing.T) {
	var out, errb bytes.Buffer
	code := run([]string{"-in", t.TempDir(), "-ref", "localhost:5000/x:1"}, &out, &errb)
	if code != 1 || !strings.Contains(errb.String(), "read index.json") {
		t.Fatalf("code=%d err=%q", code, errb.String())
	}
}

func TestRun_BadIndexJSON(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "index.json"), []byte("{not json"), 0o644)
	var out, errb bytes.Buffer
	code := run([]string{"-in", dir, "-ref", "localhost:5000/x:1"}, &out, &errb)
	if code != 1 || !strings.Contains(errb.String(), "parse index.json") {
		t.Fatalf("code=%d err=%q", code, errb.String())
	}
}

func TestRun_EmptyManifests(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "index.json"), []byte(`{"schemaVersion":2,"manifests":[]}`), 0o644)
	var out, errb bytes.Buffer
	code := run([]string{"-in", dir, "-ref", "localhost:5000/x:1"}, &out, &errb)
	if code != 1 || !strings.Contains(errb.String(), "no manifests") {
		t.Fatalf("code=%d err=%q", code, errb.String())
	}
}

func TestRun_MissingManifestBlob(t *testing.T) {
	dir := t.TempDir()
	idx := `{"schemaVersion":2,"manifests":[{"mediaType":"m","digest":"sha256:deadbeef","size":1}]}`
	os.WriteFile(filepath.Join(dir, "index.json"), []byte(idx), 0o644)
	var out, errb bytes.Buffer
	code := run([]string{"-in", dir, "-ref", "localhost:5000/x:1"}, &out, &errb)
	if code != 1 || !strings.Contains(errb.String(), "read manifest blob") {
		t.Fatalf("code=%d err=%q", code, errb.String())
	}
}

func TestRun_BadManifestJSON(t *testing.T) {
	dir := t.TempDir()
	bad := writeBlob(t, dir, []byte("{not a manifest"))
	idx := fmt.Sprintf(`{"schemaVersion":2,"manifests":[{"mediaType":"m","digest":%q,"size":1}]}`, bad)
	os.WriteFile(filepath.Join(dir, "index.json"), []byte(idx), 0o644)
	var out, errb bytes.Buffer
	code := run([]string{"-in", dir, "-ref", "localhost:5000/x:1"}, &out, &errb)
	if code != 1 || !strings.Contains(errb.String(), "parse manifest") {
		t.Fatalf("code=%d err=%q", code, errb.String())
	}
}

func TestRun_MissingConfigBlob(t *testing.T) {
	// Manifest references a config blob that doesn't exist on disk:
	// the per-blob readBlob fails.
	dir := t.TempDir()
	mani := ociManifest{
		SchemaVersion: 2,
		MediaType:     "m",
		Config:        ociDescriptor{Digest: "sha256:deadbeef", Size: 1},
	}
	maniBlob, _ := json.Marshal(mani)
	maniDig := writeBlob(t, dir, maniBlob)
	idx := fmt.Sprintf(`{"schemaVersion":2,"manifests":[{"mediaType":"m","digest":%q,"size":1}]}`, maniDig)
	os.WriteFile(filepath.Join(dir, "index.json"), []byte(idx), 0o644)
	var out, errb bytes.Buffer
	code := run([]string{"-in", dir, "-ref", "localhost:5000/x:1"}, &out, &errb)
	if code != 1 || !strings.Contains(errb.String(), "read blob") {
		t.Fatalf("code=%d err=%q", code, errb.String())
	}
}

func TestRun_HeadBlobError(t *testing.T) {
	// No server listening at the ref's host => HEAD fails.
	dir := buildLayout(t)
	var out, errb bytes.Buffer
	code := run([]string{"-in", dir, "-ref", "127.0.0.1:1/x:1"}, &out, &errb)
	if code != 1 || !strings.Contains(errb.String(), "HEAD blob") {
		t.Fatalf("code=%d err=%q", code, errb.String())
	}
}

func TestRun_PushBlobError(t *testing.T) {
	reg := &fakeRegistry{postStatus: http.StatusInternalServerError}
	srv := httptest.NewServer(reg)
	defer srv.Close()
	dir := buildLayout(t)
	var out, errb bytes.Buffer
	code := run([]string{"-in", dir, "-ref", refFor(srv, "x", "1")}, &out, &errb)
	if code != 1 || !strings.Contains(errb.String(), "push blob") {
		t.Fatalf("code=%d err=%q", code, errb.String())
	}
}

func TestRun_PushManifestError(t *testing.T) {
	reg := &fakeRegistry{manifStatus: http.StatusBadRequest}
	srv := httptest.NewServer(reg)
	defer srv.Close()
	dir := buildLayout(t)
	var out, errb bytes.Buffer
	code := run([]string{"-in", dir, "-ref", refFor(srv, "x", "1")}, &out, &errb)
	if code != 1 || !strings.Contains(errb.String(), "push manifest") {
		t.Fatalf("code=%d err=%q", code, errb.String())
	}
}

// ----- parseRef unit tests -----------------------------------------

func TestParseRef(t *testing.T) {
	cases := []struct {
		ref                        string
		wantReg, wantRepo, wantTag string
		wantErr                    bool
	}{
		{"localhost:5000/quake:1.0", "localhost:5000", "quake", "1.0", false},
		{"reg.io/ns/repo:v2", "reg.io", "ns/repo", "v2", false},
		{"localhost:5000/quake", "localhost:5000", "quake", "latest", false},
		{"noslash", "", "", "", true},
		{"reg/:tag", "", "", "", true},  // empty repo
		{"/repo:tag", "", "", "", true}, // empty registry
		{"reg/repo:", "", "", "", true}, // empty tag
	}
	for _, c := range cases {
		reg, repo, tag, err := parseRef(c.ref)
		if c.wantErr {
			if err == nil {
				t.Errorf("parseRef(%q): want error, got nil", c.ref)
			}
			continue
		}
		if err != nil {
			t.Errorf("parseRef(%q): %v", c.ref, err)
			continue
		}
		if reg != c.wantReg || repo != c.wantRepo || tag != c.wantTag {
			t.Errorf("parseRef(%q) = (%q,%q,%q), want (%q,%q,%q)",
				c.ref, reg, repo, tag, c.wantReg, c.wantRepo, c.wantTag)
		}
	}
}

// ----- readBlob unit tests -----------------------------------------

func TestReadBlob_BadDigest(t *testing.T) {
	_, err := readBlob(t.TempDir(), "no-colon-digest")
	if err == nil || !strings.Contains(err.Error(), "bad digest") {
		t.Fatalf("readBlob bad digest: %v", err)
	}
}

// ----- network-helper edge cases not hit by run() ------------------

func TestPushBlob_NoLocationHeader(t *testing.T) {
	reg := &fakeRegistry{noLocation: true}
	srv := httptest.NewServer(reg)
	defer srv.Close()
	err := pushBlob(&http.Client{}, srv.URL, "x", "sha256:ab", []byte("z"))
	if err == nil || !strings.Contains(err.Error(), "no Location header") {
		t.Fatalf("want no-Location error, got %v", err)
	}
}

func TestPushBlob_PostError(t *testing.T) {
	// Unreachable host: client.Post fails.
	err := pushBlob(&http.Client{}, "http://127.0.0.1:1", "x", "sha256:ab", []byte("z"))
	if err == nil || !strings.Contains(err.Error(), "POST uploads") {
		t.Fatalf("want POST uploads error, got %v", err)
	}
}

func TestPushBlob_PutBlobBadStatus(t *testing.T) {
	reg := &fakeRegistry{putBlobStatus: http.StatusForbidden}
	srv := httptest.NewServer(reg)
	defer srv.Close()
	err := pushBlob(&http.Client{}, srv.URL, "x", "sha256:ab", []byte("z"))
	if err == nil || !strings.Contains(err.Error(), "PUT blob: status") {
		t.Fatalf("want PUT-blob status error, got %v", err)
	}
}

func TestPushBlob_AbsoluteLocationWithQuery(t *testing.T) {
	// Location already absolute AND already contains a query string:
	// exercises the sep="&" branch + the non-"/"-prefixed branch.
	reg := &fakeRegistry{}
	srv := httptest.NewServer(reg)
	defer srv.Close()
	reg.locationOverride = srv.URL + "/v2/x/blobs/uploads/abc?state=xyz"
	err := pushBlob(&http.Client{}, srv.URL, "x", "sha256:ab", []byte("z"))
	if err != nil {
		t.Fatalf("pushBlob abs+query location: %v", err)
	}
}

func TestPushManifest_PutError(t *testing.T) {
	err := pushManifest(&http.Client{}, "http://127.0.0.1:1", "x", "t", "m", []byte("z"))
	if err == nil || !strings.Contains(err.Error(), "PUT manifest") {
		t.Fatalf("want PUT manifest error, got %v", err)
	}
}

func TestBlobExists_HeadError(t *testing.T) {
	_, err := blobExists(&http.Client{}, "http://127.0.0.1:1", "x", "sha256:ab")
	if err == nil {
		t.Fatalf("want HEAD error, got nil")
	}
}

// fakeRT is an http.RoundTripper that returns a canned 202 with a
// caller-chosen Location header on POST and delegates everything else
// to an error. It bypasses the net/http server's header-value
// validation so we can feed pushBlob a Location that makes the
// downstream http.NewRequest(PUT) fail.
type fakeRT struct {
	location string
}

func (rt fakeRT) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.Method == http.MethodPost {
		h := make(http.Header)
		h.Set("Location", rt.location)
		return &http.Response{
			StatusCode: http.StatusAccepted,
			Header:     h,
			Body:       io.NopCloser(strings.NewReader("")),
			Request:    r,
		}, nil
	}
	// Any non-POST (the follow-up PUT) errors at the transport layer.
	return nil, fmt.Errorf("transport refused %s", r.Method)
}

// A Location with a control byte makes the downstream
// http.NewRequest(PUT, putURL, ...) fail, exercising pushBlob's
// "build PUT" branch.
func TestPushBlob_BuildRequestError(t *testing.T) {
	client := &http.Client{Transport: fakeRT{location: "http://example.com/\x7f"}}
	err := pushBlob(client, "http://example.com", "x", "sha256:ab", []byte("z"))
	if err == nil || !strings.Contains(err.Error(), "build PUT") {
		t.Fatalf("want build PUT error, got %v", err)
	}
}

// POST uploads succeeds with a valid Location, but the follow-up PUT
// fails at the transport layer, hitting pushBlob's "PUT blob: %w"
// network-error branch.
func TestPushBlob_PutDoError(t *testing.T) {
	client := &http.Client{Transport: fakeRT{location: "http://example.com/v2/x/blobs/uploads/abc"}}
	err := pushBlob(client, "http://example.com", "x", "sha256:ab", []byte("z"))
	if err == nil || !strings.Contains(err.Error(), "PUT blob:") {
		t.Fatalf("want PUT blob transport error, got %v", err)
	}
}

// A baseURL containing a control byte makes pushManifest's
// http.NewRequest(PUT, ...) fail (the tag is PathEscaped, so the bad
// byte has to live in baseURL), exercising the "build PUT manifest"
// branch.
func TestPushManifest_BuildRequestError(t *testing.T) {
	err := pushManifest(&http.Client{}, "http://example.com/\x7f", "x", "t", "m", []byte("z"))
	if err == nil || !strings.Contains(err.Error(), "build PUT manifest") {
		t.Fatalf("want build PUT manifest error, got %v", err)
	}
}

// ----- main() shell via osExit seam --------------------------------

func TestMain_NonZeroExitsViaSeam(t *testing.T) {
	origExit := osExit
	origArgs := os.Args
	defer func() { osExit = origExit; os.Args = origArgs }()

	var gotCode int
	called := false
	osExit = func(code int) { gotCode = code; called = true }
	os.Args = []string{"oci-push-quake"} // no -ref => run returns 2
	main()
	if !called || gotCode != 2 {
		t.Fatalf("main: osExit called=%v code=%d, want true/2", called, gotCode)
	}
}

func TestMain_ZeroDoesNotExit(t *testing.T) {
	reg := &fakeRegistry{}
	srv := httptest.NewServer(reg)
	defer srv.Close()
	dir := buildLayout(t)

	origExit := osExit
	origArgs := os.Args
	defer func() { osExit = origExit; os.Args = origArgs }()
	called := false
	osExit = func(int) { called = true }
	os.Args = []string{"oci-push-quake", "-in", dir, "-ref", refFor(srv, "x", "1")}
	main()
	if called {
		t.Fatalf("main: osExit called on success, want not called")
	}
}
