package collection

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const validCollection = `
collection_schema = 1
id = "example.com/collections/coding"
version = "v1.2.0"

[metadata]
name = "Coding Essentials"
description = "A portable coding stack"
homepage = "https://example.com/coding"

[[plugins]]
module = "example.com/plugins/shell"
version = "v1.0.0"

[[plugins]]
module = "example.com/plugins/approval"
version = "v0.4.0"
`

func TestParseCollectionStrictSchemaAndCanonicalDigest(t *testing.T) {
	t.Parallel()
	loaded, err := Parse([]byte(validCollection), "fixture.toml")
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Collection.ID != "example.com/collections/coding" || len(loaded.Collection.Plugins) != 2 {
		t.Fatalf("loaded = %#v", loaded)
	}
	formatted := strings.ReplaceAll(validCollection, " = ", "=")
	second, err := Parse([]byte(formatted), "formatted.toml")
	if err != nil {
		t.Fatal(err)
	}
	if second.Digest != loaded.Digest {
		t.Fatalf("formatting changed digest: %s != %s", second.Digest, loaded.Digest)
	}
	unknown := strings.Replace(validCollection, "collection_schema = 1", "collection_schema = 1\nunknown = true", 1)
	if _, err := Parse([]byte(unknown), "unknown.toml"); err == nil || !strings.Contains(err.Error(), "INGOT-COLLECTION-PARSE") {
		t.Fatalf("unknown field error = %v", err)
	}
	duplicate := validCollection + "\n[[plugins]]\nmodule='example.com/plugins/shell'\nversion='v1.0.0'\n"
	if _, err := Parse([]byte(duplicate), "duplicate.toml"); err == nil || !strings.Contains(err.Error(), "INGOT-COLLECTION-DUPLICATE-PLUGIN") {
		t.Fatalf("duplicate error = %v", err)
	}
}

func TestCollectionIdentityAndMetadataValidation(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		replace string
		with    string
		code    string
	}{
		{name: "major", replace: `version = "v1.2.0"`, with: `version = "v2.0.0"`, code: "INGOT-COLLECTION-VERSION"},
		{name: "name", replace: `name = "Coding Essentials"`, with: `name = ""`, code: "INGOT-COLLECTION-METADATA"},
		{name: "homepage credentials", replace: `homepage = "https://example.com/coding"`, with: `homepage = "https://user@example.com/coding"`, code: "INGOT-COLLECTION-METADATA"},
		{name: "plugin query", replace: `version = "v1.0.0"`, with: `version = "latest"`, code: "INGOT-COLLECTION-PLUGIN-VERSION"},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			data := strings.Replace(validCollection, test.replace, test.with, 1)
			if _, err := Parse([]byte(data), test.name); err == nil || !strings.Contains(err.Error(), test.code) {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func TestLoaderLocalHTTPSAndDigestPin(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	path := filepath.Join(directory, "collection.toml")
	if err := os.WriteFile(path, []byte(validCollection), 0o600); err != nil {
		t.Fatal(err)
	}
	local, err := (Loader{}).Load(context.Background(), path, "")
	if err != nil {
		t.Fatal(err)
	}
	if !filepath.IsAbs(local.Source) {
		t.Fatalf("local source = %q", local.Source)
	}
	if _, err := (Loader{}).Load(context.Background(), path, "sha256:"+strings.Repeat("0", 64)); err == nil || !strings.Contains(err.Error(), "INGOT-COLLECTION-DIGEST") {
		t.Fatalf("digest mismatch error = %v", err)
	}

	requests := 0
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		requests++
		return response(request, http.StatusOK, validCollection), nil
	})}
	remote, err := (Loader{Client: client}).Load(context.Background(), "https://example.com/collection.toml", local.Digest)
	if err != nil {
		t.Fatal(err)
	}
	if remote.Digest != local.Digest || requests != 1 {
		t.Fatalf("remote digest=%s requests=%d", remote.Digest, requests)
	}
	if _, err := (Loader{}).Load(context.Background(), "http://example.com/collection.toml", ""); err == nil || !strings.Contains(err.Error(), "only local files and HTTPS") {
		t.Fatalf("HTTP source error = %v", err)
	}
}

func TestLoaderRejectsOversizedResponsesAndHTTPSDowngrade(t *testing.T) {
	t.Parallel()
	oversized := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		result := response(request, http.StatusOK, "")
		result.ContentLength = MaxDocumentSize + 1
		return result, nil
	})}
	if _, err := (Loader{Client: oversized}).Load(context.Background(), "https://example.com/large.toml", ""); err == nil || !strings.Contains(err.Error(), "INGOT-COLLECTION-SIZE") {
		t.Fatalf("oversized error = %v", err)
	}

	downgrade := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		result := response(request, http.StatusFound, "")
		result.Header.Set("Location", "http://example.com/collection.toml")
		return result, nil
	})}
	if _, err := (Loader{Client: downgrade}).Load(context.Background(), "https://example.com/redirect.toml", ""); err == nil || !strings.Contains(err.Error(), "INGOT-COLLECTION-FETCH") {
		t.Fatalf("downgrade error = %v", err)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (function roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}

func response(request *http.Request, status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Status:     fmt.Sprintf("%d %s", status, http.StatusText(status)),
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(body)),
		Request:    request,
	}
}
