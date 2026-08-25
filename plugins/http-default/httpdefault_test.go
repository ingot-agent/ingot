package httpdefault_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/ingot-agent/http-default"
	"github.com/ingot-agent/sdk"
	"github.com/ingot-agent/sdk/httpx"
)

func TestComponentContract(t *testing.T) {
	t.Parallel()

	var constructor func(context.Context, httpdefault.Config, httpdefault.Dependencies) (httpdefault.Exports, sdk.Cleanup, error) = httpdefault.New
	_ = constructor

	exports, cleanup, err := httpdefault.New(context.Background(), httpdefault.Config{ProxyMode: "direct"}, httpdefault.Dependencies{})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := exports.Client.(httpx.Client); !ok {
		t.Fatal("export does not implement httpx.Client")
	}
	if cleanup == nil {
		t.Fatal("cleanup is nil")
	}
	if err := cleanup(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestDoUsesExplicitContextWithoutMutatingRequest(t *testing.T) {
	t.Parallel()

	started := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		close(started)
		<-request.Context().Done()
	}))
	defer server.Close()

	exports, cleanup, err := httpdefault.New(context.Background(), httpdefault.Config{ProxyMode: "direct"}, httpdefault.Dependencies{})
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup(context.Background())

	type originalKey struct{}
	originalContext := context.WithValue(context.Background(), originalKey{}, "original")
	request, err := http.NewRequestWithContext(originalContext, http.MethodGet, server.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("X-Test", "unchanged")

	callContext, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, callErr := exports.Client.Do(callContext, request)
		done <- callErr
	}()

	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("request did not start")
	}
	cancel()

	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("error = %v, want context.Canceled", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("request did not observe cancellation")
	}

	if request.Context() != originalContext {
		t.Fatal("Do changed the request context")
	}
	if got := request.Header.Get("X-Test"); got != "unchanged" {
		t.Fatalf("request header = %q", got)
	}
}

func TestDoRoundTrip(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "ok")
	}))
	defer server.Close()

	exports, cleanup, err := httpdefault.New(context.Background(), httpdefault.Config{ProxyMode: "direct"}, httpdefault.Dependencies{})
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup(context.Background())

	request, err := http.NewRequest(http.MethodGet, server.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err := exports.Client.Do(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != "ok" {
		t.Fatalf("body = %q", body)
	}
}

func TestInvalidConfig(t *testing.T) {
	t.Parallel()

	tests := []httpdefault.Config{
		{ProxyMode: "unknown"},
		{ProxyMode: "direct", ProxyURL: "http://proxy.example"},
		{ProxyMode: "url"},
		{ProxyMode: "url", ProxyURL: "ftp://proxy.example"},
		{ProxyMode: "direct", MaxIdleConns: -1},
	}
	for _, cfg := range tests {
		_, _, err := httpdefault.New(context.Background(), cfg, httpdefault.Dependencies{})
		if !errors.Is(err, httpdefault.ErrInvalidConfig) {
			t.Fatalf("config %#v error = %v, want ErrInvalidConfig", cfg, err)
		}
	}
}

func TestNilRequest(t *testing.T) {
	t.Parallel()

	exports, cleanup, err := httpdefault.New(context.Background(), httpdefault.Config{ProxyMode: "direct"}, httpdefault.Dependencies{})
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup(context.Background())

	if _, err := exports.Client.Do(context.Background(), nil); !errors.Is(err, httpdefault.ErrInvalidRequest) {
		t.Fatalf("error = %v, want ErrInvalidRequest", err)
	}
}
