// Copyright (c) 2026 the go-quake1/engine authors.
// SPDX-License-Identifier: BSD-3-Clause

package ociassets

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// fakeDoer is an HTTPDoer that returns a canned response or error.
// Used to exercise the unhappy branches that an httptest.Server
// can't reach (e.g. network failure mid-transit).
type fakeDoer struct {
	resp *http.Response
	err  error
	last *http.Request
}

func (f *fakeDoer) Do(req *http.Request) (*http.Response, error) {
	f.last = req
	if f.err != nil {
		return nil, f.err
	}
	return f.resp, nil
}

func TestParseReference_RegistryStyle(t *testing.T) {
	r, err := ParseReference("ghcr.io/go-quake1/quake-assets:latest")
	if err != nil {
		t.Fatalf("ParseReference: %v", err)
	}
	if r.Origin != "https://ghcr.io" || r.Repo != "go-quake1/quake-assets" || r.Tag != "latest" {
		t.Fatalf("ParseReference: %#v", r)
	}
	if r.String() != "https://ghcr.io/go-quake1/quake-assets:latest" {
		t.Fatalf("String: %q", r.String())
	}
}

func TestParseReference_LocalhostHTTP(t *testing.T) {
	r, err := ParseReference("localhost:5000/quake-assets")
	if err != nil {
		t.Fatalf("ParseReference: %v", err)
	}
	if r.Origin != "http://localhost:5000" || r.Repo != "quake-assets" || r.Tag != "latest" {
		t.Fatalf("ParseReference: %#v", r)
	}
}

func TestParseReference_Loopback127(t *testing.T) {
	r, err := ParseReference("127.0.0.1:5000/q:v1")
	if err != nil {
		t.Fatalf("ParseReference: %v", err)
	}
	if r.Origin != "http://127.0.0.1:5000" || r.Tag != "v1" {
		t.Fatalf("ParseReference: %#v", r)
	}
}

func TestParseReference_URLStyle(t *testing.T) {
	r, err := ParseReference("http://127.0.0.1:8081/quake-assets:v2")
	if err != nil {
		t.Fatalf("ParseReference: %v", err)
	}
	if r.Origin != "http://127.0.0.1:8081" || r.Repo != "quake-assets" || r.Tag != "v2" {
		t.Fatalf("ParseReference: %#v", r)
	}
}

func TestParseReference_URLStyleBadURL(t *testing.T) {
	if _, err := ParseReference("http://%zz/quake-assets:v1"); err == nil {
		t.Fatal("ParseReference(bad url): want error")
	}
}

func TestParseReference_Errors(t *testing.T) {
	cases := []string{"", "noslash:tag", "host/"}
	for _, in := range cases {
		if _, err := ParseReference(in); err == nil {
			t.Fatalf("ParseReference(%q): want error", in)
		}
	}
}

func TestNewClient_TrimsTrailingSlash(t *testing.T) {
	c := NewClient("https://ghcr.io/")
	if c.Origin != "https://ghcr.io" {
		t.Fatalf("NewClient: origin = %q", c.Origin)
	}
	if c.HTTP != http.DefaultClient {
		t.Fatal("NewClient: HTTP != DefaultClient")
	}
}

func TestClientManifest_Ok(t *testing.T) {
	body := []byte(`{"schemaVersion":2,"layers":[{"mediaType":"x","digest":"sha256:abc","size":1}],"annotations":{"quake.path/foo":"sha256:abc"}}`)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v2/repo/manifests/latest" {
			t.Errorf("manifest path = %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", MediaTypeManifest)
		w.Write(body)
	}))
	defer srv.Close()
	c := &Client{HTTP: srv.Client(), Origin: srv.URL}
	m, err := c.Manifest(context.Background(), "repo", "latest")
	if err != nil {
		t.Fatalf("Manifest: %v", err)
	}
	if len(m.Layers) != 1 {
		t.Fatalf("layers = %d", len(m.Layers))
	}
}

func TestClientManifest_404(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	defer srv.Close()
	c := &Client{HTTP: srv.Client(), Origin: srv.URL}
	_, err := c.Manifest(context.Background(), "repo", "latest")
	if err == nil || !strings.Contains(err.Error(), "status 404") {
		t.Fatalf("Manifest(404): err = %v", err)
	}
}

func TestClientManifest_TransportError(t *testing.T) {
	c := &Client{HTTP: &fakeDoer{err: errors.New("dial failed")}, Origin: "http://nowhere"}
	_, err := c.Manifest(context.Background(), "repo", "latest")
	if err == nil || !strings.Contains(err.Error(), "dial failed") {
		t.Fatalf("Manifest(transport err): err = %v", err)
	}
}

func TestClientManifest_BadJSONBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("not json"))
	}))
	defer srv.Close()
	c := &Client{HTTP: srv.Client(), Origin: srv.URL}
	if _, err := c.Manifest(context.Background(), "repo", "latest"); err == nil {
		t.Fatal("Manifest(bad json): want error")
	}
}

// errReader is an io.Reader that fails on every Read. Used to drive
// the read-body error path of Manifest().
type errReader struct{}

func (errReader) Read([]byte) (int, error) { return 0, errors.New("read failed") }
func (errReader) Close() error             { return nil }

func TestClientManifest_BodyReadError(t *testing.T) {
	c := &Client{
		HTTP: &fakeDoer{resp: &http.Response{
			StatusCode: 200,
			Body:       errReader{},
			Header:     http.Header{},
		}},
		Origin: "http://x",
	}
	if _, err := c.Manifest(context.Background(), "repo", "latest"); err == nil || !strings.Contains(err.Error(), "read") {
		t.Fatalf("Manifest(body read err): err = %v", err)
	}
}

func TestClientManifest_BadRequestURL(t *testing.T) {
	c := &Client{HTTP: http.DefaultClient, Origin: "://bad"}
	if _, err := c.Manifest(context.Background(), "repo", "latest"); err == nil {
		t.Fatal("Manifest(bad url): want error")
	}
}

func TestClientBlob_Ok(t *testing.T) {
	want := []byte("blob-bytes")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/v2/repo/blobs/sha256:") {
			t.Errorf("blob path = %s", r.URL.Path)
		}
		w.Write(want)
	}))
	defer srv.Close()
	c := &Client{HTTP: srv.Client(), Origin: srv.URL}
	rc, err := c.Blob(context.Background(), "repo", "sha256:deadbeef", nil)
	if err != nil {
		t.Fatalf("Blob: %v", err)
	}
	defer rc.Close()
	got, _ := io.ReadAll(rc)
	if string(got) != string(want) {
		t.Fatalf("Blob: got %q want %q", got, want)
	}
}

func TestClientBlob_RangeRequest(t *testing.T) {
	full := []byte("0123456789")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Range") != "bytes=2-5" {
			t.Errorf("Range header = %q", r.Header.Get("Range"))
		}
		w.WriteHeader(http.StatusPartialContent)
		w.Write(full[2:6])
	}))
	defer srv.Close()
	c := &Client{HTTP: srv.Client(), Origin: srv.URL}
	rc, err := c.Blob(context.Background(), "repo", "sha256:deadbeef", &ByteRange{Start: 2, End: 5})
	if err != nil {
		t.Fatalf("Blob: %v", err)
	}
	defer rc.Close()
	got, _ := io.ReadAll(rc)
	if string(got) != "2345" {
		t.Fatalf("Blob: got %q want 2345", got)
	}
}

func TestClientBlob_BadDigest(t *testing.T) {
	c := NewClient("http://x")
	if _, err := c.Blob(context.Background(), "repo", "md5:notvalid", nil); err == nil {
		t.Fatal("Blob(bad digest): want error")
	}
}

func TestClientBlob_NotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	defer srv.Close()
	c := &Client{HTTP: srv.Client(), Origin: srv.URL}
	_, err := c.Blob(context.Background(), "repo", "sha256:deadbeef", nil)
	if err == nil || !strings.Contains(err.Error(), "status 404") {
		t.Fatalf("Blob: err = %v", err)
	}
}

func TestClientBlob_TransportError(t *testing.T) {
	c := &Client{HTTP: &fakeDoer{err: errors.New("dial err")}, Origin: "http://x"}
	if _, err := c.Blob(context.Background(), "repo", "sha256:deadbeef", nil); err == nil {
		t.Fatal("Blob(transport err): want error")
	}
}

func TestClientBlob_BadRequestURL(t *testing.T) {
	c := &Client{HTTP: http.DefaultClient, Origin: "://bad"}
	if _, err := c.Blob(context.Background(), "repo", "sha256:deadbeef", nil); err == nil {
		t.Fatal("Blob(bad url): want error")
	}
}

func TestVerifyDigest_OkAndMismatch(t *testing.T) {
	data := []byte("hello")
	d := Sha256Digest(data)
	if err := VerifyDigest(data, d); err != nil {
		t.Fatalf("VerifyDigest(ok): %v", err)
	}
	if err := VerifyDigest(data, "sha256:00"); err == nil {
		t.Fatal("VerifyDigest(mismatch): want error")
	}
}

func TestSha256Digest(t *testing.T) {
	d := Sha256Digest([]byte(""))
	want := "sha256:e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
	if d != want {
		t.Fatalf("Sha256Digest(empty) = %q want %q", d, want)
	}
}

// Sanity: fakeDoer's last-request capture works (drives the "I sent
// the right headers" assertions in the wider test pack).
func TestFakeDoer_CapturesRequest(t *testing.T) {
	f := &fakeDoer{resp: &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(""))}}
	c := &Client{HTTP: f, Origin: "http://x"}
	_, _ = c.Manifest(context.Background(), "repo", "latest")
	if f.last == nil || f.last.Method != "GET" {
		t.Fatalf("fakeDoer.last = %#v", f.last)
	}
	_ = fmt.Sprint(f.last)
}
