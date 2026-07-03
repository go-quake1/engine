// Copyright (c) 2026 the go-quake1/engine authors.
// SPDX-License-Identifier: BSD-3-Clause

package ociassets

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// ErrInvalidReference is returned by [ParseReference] when the input
// can't be split into the (origin + repo + reference) triple used by
// [Client]. The error message names which part was missing so
// callers can surface it verbatim in misconfiguration warnings.
var ErrInvalidReference = errors.New("ociassets: invalid reference")

// Reference is the parsed form of an OCI reference string. We accept
// two shapes:
//
//   - registry-style: "host[:port]/repo[:tag]"  (e.g. "localhost:5000/quake-assets:latest")
//   - URL-style: "http(s)://host[:port]/repo[:tag]" (e.g. "http://127.0.0.1:8081/quake-assets:latest")
//
// The URL-style form is what wasm builds use because the browser
// fetch() implementation needs an absolute scheme; the registry-style
// form is what oras/podman/skopeo accept. Both round-trip through
// [Reference.String].
type Reference struct {
	// Origin is the scheme + host (no trailing slash). For
	// registry-style inputs we default to https; localhost / 127.0.0.1
	// defaults to http to match the cleartext registries everyone
	// runs in dev.
	Origin string

	// Repo is the path segment between the host and the colon. May
	// contain slashes ("library/foo/bar").
	Repo string

	// Tag is the human-readable reference after the colon. Defaults
	// to "latest" when the input has no colon-suffix.
	Tag string
}

// ParseReference splits a reference string into its (origin, repo, tag)
// triple. See [Reference] for the accepted input shapes.
func ParseReference(s string) (*Reference, error) {
	if s == "" {
		return nil, fmt.Errorf("%w: empty string", ErrInvalidReference)
	}
	var origin string
	rest := s
	if strings.HasPrefix(s, "http://") || strings.HasPrefix(s, "https://") {
		u, err := url.Parse(s)
		if err != nil {
			return nil, fmt.Errorf("%w: %v", ErrInvalidReference, err)
		}
		origin = u.Scheme + "://" + u.Host
		rest = strings.TrimPrefix(u.Path, "/")
	} else if strings.HasPrefix(s, "/") {
		// Same-origin reference "/repo[:tag]": Origin is left empty and
		// resolved at fetch time from the host page (in the browser the
		// worker publishes it on globalThis.__quakeOCIBase). This lets a
		// static /v2 mirror served beside the page work even under a base
		// path -- e.g. GitHub Pages' /<project>/ -- where a fixed
		// <scheme>://<host>/v2 origin would miss the prefix.
		origin = ""
		rest = strings.TrimPrefix(s, "/")
	} else {
		// registry-style: host[:port]/repo[:tag]. Find the first '/'
		// to split host from repo. A bare "host:tag" with no slash
		// is a syntactic error -- we need a repo.
		slash := strings.Index(s, "/")
		if slash < 0 {
			return nil, fmt.Errorf("%w: no '/' between host and repo in %q", ErrInvalidReference, s)
		}
		host := s[:slash]
		rest = s[slash+1:]
		scheme := "https"
		if strings.HasPrefix(host, "localhost") || strings.HasPrefix(host, "127.") {
			scheme = "http"
		}
		origin = scheme + "://" + host
	}
	repo, tag := rest, "latest"
	if colon := strings.LastIndex(rest, ":"); colon >= 0 {
		repo, tag = rest[:colon], rest[colon+1:]
	}
	if repo == "" {
		return nil, fmt.Errorf("%w: empty repo in %q", ErrInvalidReference, s)
	}
	return &Reference{Origin: origin, Repo: repo, Tag: tag}, nil
}

// String renders the reference in URL-style. Round-trips through
// [ParseReference].
func (r *Reference) String() string {
	return r.Origin + "/" + r.Repo + ":" + r.Tag
}

// HTTPDoer is the minimum surface [Client] needs from net/http. Tests
// inject an httptest.Server-backed http.Client; production uses
// http.DefaultClient (which on wasm transparently routes through the
// browser fetch() API).
type HTTPDoer interface {
	Do(*http.Request) (*http.Response, error)
}

// Client talks to one OCI Distribution v2 origin. It is stateless
// beyond the configured HTTP client + origin, so a single Client is
// safe to share across goroutines.
type Client struct {
	HTTP   HTTPDoer
	Origin string // e.g. "https://ghcr.io" -- no trailing slash
}

// NewClient builds a Client for the given origin using http.DefaultClient.
// Pass an Origin without trailing slash; the constructor strips one
// if present so callers don't have to.
func NewClient(origin string) *Client {
	return &Client{
		HTTP:   http.DefaultClient,
		Origin: strings.TrimRight(origin, "/"),
	}
}

// Manifest fetches the manifest body for (repo, reference) and decodes
// it. The Accept header advertises both the standard OCI image-manifest
// type and our vendor type so any spec-compliant registry replies.
func (c *Client) Manifest(ctx context.Context, repo, reference string) (*Manifest, error) {
	u := c.Origin + "/v2/" + repo + "/manifests/" + reference
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, fmt.Errorf("ociassets: build manifest request: %w", err)
	}
	req.Header.Set("Accept", "application/vnd.oci.image.manifest.v1+json, "+MediaTypeManifest)
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("ociassets: GET %s: %w", u, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("ociassets: GET %s: status %d", u, resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("ociassets: read manifest body: %w", err)
	}
	return DecodeManifest(body)
}

// Blob streams the bytes of a single layer. The digest must include
// the "sha256:" prefix. byteRange is optional: when non-nil it issues a
// Range: bytes=<start>-<end> request (end is inclusive, matching HTTP
// semantics). The caller must Close the returned io.ReadCloser.
//
// On a 2xx response the body is returned verbatim; the caller is
// responsible for digest verification (use [VerifyDigest] on the
// fully-read bytes).
func (c *Client) Blob(ctx context.Context, repo, digest string, byteRange *ByteRange) (io.ReadCloser, error) {
	if !strings.HasPrefix(digest, "sha256:") {
		return nil, fmt.Errorf("ociassets: digest %q missing sha256: prefix", digest)
	}
	u := c.Origin + "/v2/" + repo + "/blobs/" + digest
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, fmt.Errorf("ociassets: build blob request: %w", err)
	}
	if byteRange != nil {
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", byteRange.Start, byteRange.End))
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("ociassets: GET %s: %w", u, err)
	}
	// 200 OK = full body; 206 Partial Content = honoured Range
	// request. Anything else is a failure we must surface (but we
	// must drain the body first so the connection can be reused).
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusPartialContent {
		resp.Body.Close()
		return nil, fmt.Errorf("ociassets: GET %s: status %d", u, resp.StatusCode)
	}
	return resp.Body, nil
}

// ByteRange is the (start, end) inclusive byte range for a partial
// blob fetch. Mirrors the HTTP Range header semantics.
type ByteRange struct {
	Start int64
	End   int64
}

// VerifyDigest recomputes sha256 over data and asserts it matches the
// expected "sha256:<hex>" string. Used by [FS.Open] after a full-body
// download to detect cache / transport corruption.
func VerifyDigest(data []byte, expected string) error {
	want := strings.TrimPrefix(expected, "sha256:")
	sum := sha256.Sum256(data)
	got := hex.EncodeToString(sum[:])
	if got != want {
		return fmt.Errorf("ociassets: digest mismatch: want %s got sha256:%s", expected, got)
	}
	return nil
}

// Sha256Digest hashes data and returns the canonical "sha256:<hex>"
// digest string. Used by the CLI packer when emitting blob filenames.
func Sha256Digest(data []byte) string {
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:])
}
