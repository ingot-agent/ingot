package appcomponent

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	appbackend "github.com/ingot-agent/app-webui"
	ingotabi "github.com/ingot-agent/ingot-abi"
	"github.com/ingot-agent/sdk/agent"
	"github.com/ingot-agent/sdk/asset"
	"github.com/ingot-agent/sdk/content"
)

type uploadStore struct {
	asset.Store
	size  uint64
	body  string
	calls int
}

func (s *uploadStore) Put(ctx context.Context, request asset.PutRequest) (asset.Reference, asset.Info, error) {
	s.calls++
	s.size = request.Size
	data, err := io.ReadAll(request.Body)
	if err != nil {
		return asset.Reference{}, asset.Info{}, err
	}
	if uint64(len(data)) != request.Size {
		return asset.Reference{}, asset.Info{}, io.ErrUnexpectedEOF
	}
	s.body = string(data)
	return asset.Reference{ID: "asset-one"}, asset.Info{Size: request.Size}, nil
}

func TestAssetUploadStreamingSizeAndValidation(t *testing.T) {
	a := testApplication(t)
	store := &uploadStore{}
	a.assets = store
	a.config.MaxAssetBytes = 8
	for _, test := range []struct {
		body   string
		length int64
		status int
	}{
		{"file", 4, http.StatusCreated},
		{"", 0, http.StatusCreated},
		{"file", -1, http.StatusLengthRequired},
		{"too-large", 9, http.StatusRequestEntityTooLarge},
		{"file", 5, http.StatusBadRequest},
	} {
		request := httptest.NewRequest(http.MethodPost, "/api/assets", strings.NewReader(test.body))
		request.ContentLength = test.length
		w := httptest.NewRecorder()
		a.routes().ServeHTTP(w, request)
		if w.Code != test.status {
			t.Fatalf("upload length %d: %d %s", test.length, w.Code, w.Body.String())
		}
		if w.Code == http.StatusCreated {
			var body struct {
				ID   string
				Size uint64
			}
			if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if body.ID != "asset-one" || body.Size != uint64(test.length) || store.body != test.body || store.size != body.Size {
				t.Fatalf("upload = %#v, store=%#v", body, store)
			}
		}
	}
	if store.calls != 3 {
		t.Fatalf("invalid upload reached store, calls=%d", store.calls)
	}
	a.assets = nil
	w := httptest.NewRecorder()
	a.routes().ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/assets", strings.NewReader("file")))
	if w.Code != http.StatusNotImplemented {
		t.Fatalf("unconfigured asset store = %d", w.Code)
	}
}

func TestAttachmentOnlyTurnUsesAssetReferences(t *testing.T) {
	a := testApplication(t)
	received := make(chan agent.Turn, 1)
	runtime := &testAgent{run: func(_ context.Context, turn agent.Turn) (agent.Execution, error) {
		received <- turn
		return agent.Execution{}, nil
	}}
	controller, err := newAgentController(ingotabi.Some[agent.Runtime](runtime), ingotabi.None[agent.StreamingRuntime](), runtime)
	if err != nil {
		t.Fatal(err)
	}
	a.turns.agent = controller
	w := httptest.NewRecorder()
	a.routes().ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/turns", strings.NewReader(`{"sessionId":"session","attachments":[{"kind":"image","assetId":"asset-one","mimeType":"image/png","name":"photo.png"}]}`)))
	if w.Code != http.StatusAccepted {
		t.Fatalf("attachment-only turn = %d %s", w.Code, w.Body.String())
	}
	select {
	case turn := <-received:
		if turn.Input != "" || len(turn.Attachments) != 1 || turn.Attachments[0].Kind != content.KindImage || turn.Attachments[0].Media.Source.Kind != content.SourceAsset || turn.Attachments[0].Media.Source.Asset.ID != "asset-one" || turn.Attachments[0].Media.Name != "photo.png" {
			t.Fatalf("SDK turn = %#v", turn)
		}
	case <-time.After(time.Second):
		t.Fatal("turn did not reach runtime")
	}
	for _, body := range []string{`{"sessionId":"session","attachments":[{"kind":"image","assetId":""}]}`, `{"sessionId":"session","attachments":[{"kind":"image","assetId":"one","uri":"https://example.com"}]}`} {
		w := httptest.NewRecorder()
		a.routes().ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/turns", strings.NewReader(body)))
		if w.Code != http.StatusBadRequest {
			t.Fatalf("invalid attachment = %d %s", w.Code, w.Body.String())
		}
	}
}

func TestSessionLifecycleHTTPDelegatesMetadata(t *testing.T) {
	a := testApplication(t)
	item, err := a.sessions.Create(context.Background(), "original")
	if err != nil {
		t.Fatal(err)
	}
	for _, action := range []string{"archive", "archive", "restore"} {
		w := httptest.NewRecorder()
		a.routes().ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/sessions/"+item.ID+"/"+action, nil))
		var metadata appbackend.Session
		if err := json.Unmarshal(w.Body.Bytes(), &metadata); err != nil {
			t.Fatal(err)
		}
		if w.Code != http.StatusOK || !metadata.UpdatedAt.Equal(item.UpdatedAt) || (metadata.ArchivedAt != nil) != (action == "archive") {
			t.Fatalf("%s = %d %#v", action, w.Code, metadata)
		}
	}
	w := httptest.NewRecorder()
	a.routes().ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/sessions/"+item.ID+"/fork", strings.NewReader(`{}`)))
	var fork appbackend.Session
	if err := json.Unmarshal(w.Body.Bytes(), &fork); err != nil {
		t.Fatal(err)
	}
	if w.Code != http.StatusCreated || fork.ID == item.ID || fork.Title != item.Title || fork.ArchivedAt != nil {
		t.Fatalf("fork = %d %#v", w.Code, fork)
	}
	w = httptest.NewRecorder()
	a.routes().ServeHTTP(w, httptest.NewRequest(http.MethodDelete, "/api/sessions/"+item.ID, nil))
	if w.Code != http.StatusNoContent {
		t.Fatalf("delete = %d %s", w.Code, w.Body.String())
	}
	w = httptest.NewRecorder()
	a.routes().ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/sessions/"+item.ID, nil))
	if w.Code != http.StatusNotFound {
		t.Fatalf("deleted session = %d", w.Code)
	}
}
