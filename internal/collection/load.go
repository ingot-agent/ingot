package collection

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

const MaxDocumentSize = int64(1 << 20)

var digestPattern = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

// Loader reads local or HTTPS Collection documents. Client is injectable for
// TLS and redirect policy tests.
type Loader struct {
	Client   *http.Client
	MaxBytes int64
}

// Load obtains, parses, and optionally pins one Collection semantic digest.
func (loader Loader) Load(ctx context.Context, source, expectedDigest string) (Loaded, error) {
	if source == "" {
		return Loaded{}, fmt.Errorf("INGOT-COLLECTION-SOURCE: source must be non-empty")
	}
	if expectedDigest != "" && !digestPattern.MatchString(expectedDigest) {
		return Loaded{}, fmt.Errorf("INGOT-COLLECTION-DIGEST: invalid expected digest %q", expectedDigest)
	}
	limit := loader.MaxBytes
	if limit <= 0 {
		limit = MaxDocumentSize
	}
	var data []byte
	normalized := source
	var err error
	switch {
	case strings.HasPrefix(strings.ToLower(source), "https://"):
		data, err = loader.loadHTTPS(ctx, source, limit)
	case strings.Contains(source, "://"):
		return Loaded{}, fmt.Errorf("INGOT-COLLECTION-SOURCE: only local files and HTTPS URLs are supported")
	default:
		normalized, err = filepath.Abs(source)
		if err == nil {
			data, err = readLocal(normalized, limit)
		}
	}
	if err != nil {
		return Loaded{}, err
	}
	loaded, err := Parse(data, normalized)
	if err != nil {
		return Loaded{}, err
	}
	if expectedDigest != "" && loaded.Digest != expectedDigest {
		return Loaded{}, fmt.Errorf("INGOT-COLLECTION-DIGEST: expected %s, got %s", expectedDigest, loaded.Digest)
	}
	return loaded, nil
}

func readLocal(path string, limit int64) ([]byte, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("INGOT-COLLECTION-SOURCE: %s: %w", path, err)
	}
	if !info.Mode().IsRegular() || info.Size() > limit {
		return nil, fmt.Errorf("INGOT-COLLECTION-SIZE: %s must be a regular file no larger than %d bytes", path, limit)
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("INGOT-COLLECTION-SOURCE: %s: %w", path, err)
	}
	defer file.Close()
	return readBounded(file, limit)
}

func (loader Loader) loadHTTPS(ctx context.Context, source string, limit int64) ([]byte, error) {
	parsed, err := url.Parse(source)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil {
		return nil, fmt.Errorf("INGOT-COLLECTION-SOURCE: invalid HTTPS URL")
	}
	base := loader.Client
	if base == nil {
		base = &http.Client{}
	}
	client := *base
	if client.Timeout == 0 {
		client.Timeout = 30 * time.Second
	}
	previousRedirect := client.CheckRedirect
	client.CheckRedirect = func(request *http.Request, via []*http.Request) error {
		if len(via) > 5 {
			return fmt.Errorf("too many redirects")
		}
		if request.URL.Scheme != "https" || request.URL.User != nil {
			return fmt.Errorf("redirect must remain HTTPS and contain no credentials")
		}
		if previousRedirect != nil {
			return previousRedirect(request, via)
		}
		return nil
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, source, nil)
	if err != nil {
		return nil, fmt.Errorf("INGOT-COLLECTION-FETCH: %w", err)
	}
	response, err := client.Do(request)
	if err != nil {
		return nil, fmt.Errorf("INGOT-COLLECTION-FETCH: %s: %w", source, err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, fmt.Errorf("INGOT-COLLECTION-FETCH: %s: HTTP %s", source, response.Status)
	}
	if response.ContentLength > limit {
		return nil, fmt.Errorf("INGOT-COLLECTION-SIZE: response exceeds %d bytes", limit)
	}
	data, err := readBounded(response.Body, limit)
	if err != nil {
		return nil, err
	}
	return data, nil
}

func readBounded(reader io.Reader, limit int64) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(reader, limit+1))
	if err != nil {
		return nil, fmt.Errorf("INGOT-COLLECTION-SOURCE: %w", err)
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("INGOT-COLLECTION-SIZE: document exceeds %d bytes", limit)
	}
	return data, nil
}
