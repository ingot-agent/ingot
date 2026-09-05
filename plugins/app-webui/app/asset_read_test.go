package appcomponent

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	appbackend "github.com/ingot-agent/app-webui"
	"github.com/ingot-agent/sdk/asset"
)

type readAssetStore struct {
	asset.Store
	body      string
	err       error
	closed    bool
	reference asset.Reference
}

func (s *readAssetStore) Stat(ctx context.Context, reference asset.Reference) (asset.Info, error) {
	s.reference = reference
	if ctx.Err() != nil {
		return asset.Info{}, ctx.Err()
	}
	return asset.Info{Size: uint64(len(s.body))}, s.err
}
func (s *readAssetStore) Open(context.Context, asset.Reference) (io.ReadCloser, error) {
	return &trackedAssetReader{Reader: strings.NewReader(s.body), close: func() { s.closed = true }}, nil
}

type trackedAssetReader struct {
	io.Reader
	close func()
}

func (r *trackedAssetReader) Close() error { r.close(); return nil }

func TestReadAssetPreservesOpaqueIDAndDownloadHeaders(t *testing.T) {
	a := testApplication(t)
	store := &readAssetStore{body: "<script>never execute</script>"}
	a.assets = store
	a.config.MaxAssetBytes = 4096
	w := httptest.NewRecorder()
	a.routes().ServeHTTP(w, httptest.NewRequest("GET", "/api/assets/opaque%2Fasset%3Aid", nil))
	if w.Code != 200 || w.Body.String() != store.body || !store.closed {
		t.Fatalf("%d %s closed=%v", w.Code, w.Body, store.closed)
	}
	if store.reference.ID != "opaque/asset:id" {
		t.Fatalf("ID = %q", store.reference.ID)
	}
	if w.Header().Get("Content-Type") != "application/octet-stream" || w.Header().Get("Content-Disposition") != "attachment" || w.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("headers = %v", w.Header())
	}
	w = httptest.NewRecorder()
	a.routes().ServeHTTP(w, httptest.NewRequest("GET", "/api/state", nil))
	var state appbackend.StateSnapshot
	if err := json.Unmarshal(w.Body.Bytes(), &state); err != nil {
		t.Fatal(err)
	}
	if !state.Assets.Available || state.Assets.MaxBytes != 4096 {
		t.Fatalf("assets = %#v", state.Assets)
	}
}

func TestReadAssetUnavailableMissingEmptyAndCancellation(t *testing.T) {
	a := testApplication(t)
	for _, test := range []struct {
		name     string
		store    *readAssetStore
		canceled bool
		status   int
	}{
		{"unavailable", nil, false, 501},
		{"missing", &readAssetStore{err: fs.ErrNotExist}, false, 404},
		{"empty", &readAssetStore{}, false, 200},
		{"canceled", &readAssetStore{}, true, 408},
	} {
		t.Run(test.name, func(t *testing.T) {
			a.assets = nil
			if test.store != nil {
				a.assets = test.store
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if test.canceled {
				cancel()
			}
			w := httptest.NewRecorder()
			a.routes().ServeHTTP(w, httptest.NewRequest("GET", "/api/assets/test", nil).WithContext(ctx))
			if w.Code != test.status {
				t.Fatalf("%d %s", w.Code, w.Body)
			}
			if test.status == 200 && (!test.store.closed || w.Header().Get("Content-Length") != "0") {
				t.Fatal("empty asset was not closed or sized")
			}
		})
	}
}

type brokenAssetStore struct{ readAssetStore }

func (s *brokenAssetStore) Open(context.Context, asset.Reference) (io.ReadCloser, error) {
	return &trackedAssetReader{Reader: brokenAssetReader{}, close: func() { s.closed = true }}, nil
}

type brokenAssetReader struct{}

func (brokenAssetReader) Read([]byte) (int, error) { return 0, errors.New("read failed") }
func TestAssetReadFailureAbortsResponseAndClosesReader(t *testing.T) {
	a := testApplication(t)
	store := &brokenAssetStore{readAssetStore: readAssetStore{body: "expected"}}
	a.assets = store
	defer func() {
		if recover() != http.ErrAbortHandler || !store.closed {
			t.Fatalf("read failure was not aborted/closed: closed=%v", store.closed)
		}
	}()
	a.routes().ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/api/assets/test", nil))
	t.Fatal("read failure returned successfully")
}
