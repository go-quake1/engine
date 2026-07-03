// Copyright (c) 2026 the go-quake1/engine authors.
// SPDX-License-Identifier: BSD-3-Clause

// oci-push-quake pushes an OCI image layout (produced by
// oci-pack-quake) to an OCI Distribution v2 registry via plain HTTP.
// Pure-Go, no external CLI dependency (no oras, no skopeo, no docker).
//
// Usage:
//
//	go run ./cmd/oci-push-quake -in /tmp/quake-oci -ref localhost:5000/quake-assets:1.0
//
// Push flow per OCI Distribution v2 spec:
//
//  1. POST   /v2/<name>/blobs/uploads/   → 202 + Location header
//  2. PUT    <Location>?digest=sha256:<hex>  with body = blob bytes
//  3. (repeat for every blob: config + each layer)
//  4. PUT    /v2/<name>/manifests/<reference>  with body = manifest JSON
//
// Plain HTTP only (matches `registry:2` defaults on localhost:5000);
// no auth, no TLS. Add the obvious knobs when pushing to a real
// registry.
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

var osExit = os.Exit

func main() {
	if code := run(os.Args[1:], os.Stdout, os.Stderr); code != 0 {
		osExit(code)
	}
}

type ociIndex struct {
	SchemaVersion int                     `json:"schemaVersion"`
	Manifests     []ociIndexManifestEntry `json:"manifests"`
}

type ociIndexManifestEntry struct {
	MediaType   string            `json:"mediaType"`
	Digest      string            `json:"digest"`
	Size        int64             `json:"size"`
	Annotations map[string]string `json:"annotations,omitempty"`
}

type ociManifest struct {
	SchemaVersion int               `json:"schemaVersion"`
	MediaType     string            `json:"mediaType"`
	Config        ociDescriptor     `json:"config"`
	Layers        []ociDescriptor   `json:"layers"`
	Annotations   map[string]string `json:"annotations,omitempty"`
}

type ociDescriptor struct {
	MediaType string `json:"mediaType"`
	Digest    string `json:"digest"`
	Size      int64  `json:"size"`
}

func run(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("oci-push-quake", flag.ContinueOnError)
	fs.SetOutput(stderr)
	in := fs.String("in", "_oci", "OCI image-layout input directory (produced by oci-pack-quake)")
	ref := fs.String("ref", "", "Target reference, e.g. localhost:5000/quake-assets:1.0 (registry + repo + tag)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *ref == "" {
		fmt.Fprintln(stderr, "oci-push-quake: -ref is required (e.g. localhost:5000/quake-assets:1.0)")
		return 2
	}

	registry, repo, tag, err := parseRef(*ref)
	if err != nil {
		fmt.Fprintf(stderr, "oci-push-quake: bad -ref: %v\n", err)
		return 2
	}

	indexBlob, err := os.ReadFile(filepath.Join(*in, "index.json"))
	if err != nil {
		fmt.Fprintf(stderr, "oci-push-quake: read index.json: %v\n", err)
		return 1
	}
	var idx ociIndex
	if err := json.Unmarshal(indexBlob, &idx); err != nil {
		fmt.Fprintf(stderr, "oci-push-quake: parse index.json: %v\n", err)
		return 1
	}
	if len(idx.Manifests) == 0 {
		fmt.Fprintln(stderr, "oci-push-quake: index.json has no manifests")
		return 1
	}
	manifestEntry := idx.Manifests[0]

	manifestBlob, err := readBlob(*in, manifestEntry.Digest)
	if err != nil {
		fmt.Fprintf(stderr, "oci-push-quake: read manifest blob: %v\n", err)
		return 1
	}
	var mani ociManifest
	if err := json.Unmarshal(manifestBlob, &mani); err != nil {
		fmt.Fprintf(stderr, "oci-push-quake: parse manifest: %v\n", err)
		return 1
	}

	client := &http.Client{}
	baseURL := "http://" + registry

	// Push config + each layer blob.
	allBlobs := append([]ociDescriptor{mani.Config}, mani.Layers...)
	for i, desc := range allBlobs {
		blob, err := readBlob(*in, desc.Digest)
		if err != nil {
			fmt.Fprintf(stderr, "oci-push-quake: read blob %s: %v\n", desc.Digest, err)
			return 1
		}
		exists, err := blobExists(client, baseURL, repo, desc.Digest)
		if err != nil {
			fmt.Fprintf(stderr, "oci-push-quake: HEAD blob %s: %v\n", desc.Digest, err)
			return 1
		}
		if exists {
			fmt.Fprintf(stdout, "[%d/%d] %s already present (%d bytes)\n",
				i+1, len(allBlobs), desc.Digest[:19], desc.Size)
			continue
		}
		if err := pushBlob(client, baseURL, repo, desc.Digest, blob); err != nil {
			fmt.Fprintf(stderr, "oci-push-quake: push blob %s: %v\n", desc.Digest, err)
			return 1
		}
		fmt.Fprintf(stdout, "[%d/%d] pushed %s (%d bytes)\n",
			i+1, len(allBlobs), desc.Digest[:19], desc.Size)
	}

	// Push the manifest under the tag.
	if err := pushManifest(client, baseURL, repo, tag, manifestEntry.MediaType, manifestBlob); err != nil {
		fmt.Fprintf(stderr, "oci-push-quake: push manifest: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "pushed manifest %s/%s:%s (%d bytes)\n", registry, repo, tag, len(manifestBlob))
	return 0
}

// parseRef splits "registry/repo:tag" into its three parts. registry
// is everything before the first slash; repo is the middle; tag is
// after the last colon. Sensible defaults: tag = "latest" when
// omitted.
func parseRef(ref string) (registry, repo, tag string, err error) {
	slash := strings.IndexByte(ref, '/')
	if slash < 0 {
		return "", "", "", fmt.Errorf("expected registry/repo[:tag], got %q", ref)
	}
	registry = ref[:slash]
	rest := ref[slash+1:]
	if colon := strings.LastIndexByte(rest, ':'); colon >= 0 {
		repo = rest[:colon]
		tag = rest[colon+1:]
	} else {
		repo = rest
		tag = "latest"
	}
	if registry == "" || repo == "" || tag == "" {
		return "", "", "", fmt.Errorf("registry/repo/tag must all be non-empty in %q", ref)
	}
	return registry, repo, tag, nil
}

func readBlob(layoutDir, digest string) ([]byte, error) {
	algo, hex, ok := strings.Cut(digest, ":")
	if !ok {
		return nil, fmt.Errorf("bad digest %q (want algo:hex)", digest)
	}
	return os.ReadFile(filepath.Join(layoutDir, "blobs", algo, hex))
}

func blobExists(client *http.Client, baseURL, repo, digest string) (bool, error) {
	u := fmt.Sprintf("%s/v2/%s/blobs/%s", baseURL, repo, url.PathEscape(digest))
	resp, err := client.Head(u)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()
	return resp.StatusCode == http.StatusOK, nil
}

// pushBlob runs the monolithic-upload path: POST → PUT with ?digest.
// Skips the chunked path since blobs we ship are well under 16 MB
// each and localhost loopback doesn't benefit from chunking.
func pushBlob(client *http.Client, baseURL, repo, digest string, blob []byte) error {
	startURL := fmt.Sprintf("%s/v2/%s/blobs/uploads/", baseURL, repo)
	startResp, err := client.Post(startURL, "application/octet-stream", nil)
	if err != nil {
		return fmt.Errorf("POST uploads: %w", err)
	}
	io.Copy(io.Discard, startResp.Body)
	startResp.Body.Close()
	if startResp.StatusCode != http.StatusAccepted {
		return fmt.Errorf("POST uploads: status %d", startResp.StatusCode)
	}
	loc := startResp.Header.Get("Location")
	if loc == "" {
		return fmt.Errorf("POST uploads: no Location header")
	}
	if strings.HasPrefix(loc, "/") {
		loc = baseURL + loc
	}
	sep := "?"
	if strings.Contains(loc, "?") {
		sep = "&"
	}
	putURL := fmt.Sprintf("%s%sdigest=%s", loc, sep, url.QueryEscape(digest))

	req, err := http.NewRequest(http.MethodPut, putURL, bytes.NewReader(blob))
	if err != nil {
		return fmt.Errorf("build PUT: %w", err)
	}
	req.Header.Set("Content-Type", "application/octet-stream")
	req.ContentLength = int64(len(blob))
	putResp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("PUT blob: %w", err)
	}
	io.Copy(io.Discard, putResp.Body)
	putResp.Body.Close()
	if putResp.StatusCode != http.StatusCreated {
		return fmt.Errorf("PUT blob: status %d", putResp.StatusCode)
	}
	return nil
}

func pushManifest(client *http.Client, baseURL, repo, tag, mediaType string, manifest []byte) error {
	u := fmt.Sprintf("%s/v2/%s/manifests/%s", baseURL, repo, url.PathEscape(tag))
	req, err := http.NewRequest(http.MethodPut, u, bytes.NewReader(manifest))
	if err != nil {
		return fmt.Errorf("build PUT manifest: %w", err)
	}
	req.Header.Set("Content-Type", mediaType)
	req.ContentLength = int64(len(manifest))
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("PUT manifest: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusCreated {
		return fmt.Errorf("PUT manifest: status %d: %s", resp.StatusCode, string(body))
	}
	return nil
}
