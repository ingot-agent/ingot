package appcomponent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"strconv"
	"strings"
	"time"

	appbackend "github.com/ingot-agent/app-webui"
	"github.com/ingot-agent/app-webui/internal/projection"
	"github.com/ingot-agent/sdk/agent"
	"github.com/ingot-agent/sdk/asset"
	"github.com/ingot-agent/sdk/content"
	"github.com/ingot-agent/sdk/operation"
	"github.com/ingot-agent/sdk/session"
)

const maxJSONBody = 1 << 20

func (a *application) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/state", a.handleState)
	mux.HandleFunc("GET /api/events", a.handleEvents)
	mux.HandleFunc("POST /api/turns", a.handleCreateTurn)
	mux.HandleFunc("DELETE /api/turns/{id}", a.handleCancelTurn)
	mux.HandleFunc("GET /api/sessions", a.handleListSessions)
	mux.HandleFunc("POST /api/sessions", a.handleCreateSession)
	mux.HandleFunc("GET /api/sessions/{id}", a.handleGetSession)
	mux.HandleFunc("PATCH /api/sessions/{id}", a.handleRenameSession)
	mux.HandleFunc("GET /api/sessions/{id}/history", a.handleHistory)
	mux.HandleFunc("POST /api/interactions/{id}/response", a.handleInteractionResponse)

	mux.HandleFunc("POST /api/assets", a.handleUploadAsset)
	mux.HandleFunc("GET /api/assets/{id}", a.handleReadAsset)
	mux.HandleFunc("GET /api/operations", func(w http.ResponseWriter, _ *http.Request) { writeJSON(w, http.StatusOK, a.operations.List()) })
	mux.HandleFunc("POST /api/operations/{name}", a.handleInvokeOperation)
	mux.HandleFunc("DELETE /api/operation-invocations/{id}", a.handleCancelOperation)
	mux.HandleFunc("DELETE /api/sessions/{id}", a.handleDeleteSession)
	mux.HandleFunc("POST /api/sessions/{id}/archive", a.handleSessionLifecycle("session.archived", a.sessions.Archive))
	mux.HandleFunc("POST /api/sessions/{id}/restore", a.handleSessionLifecycle("session.restored", a.sessions.Restore))
	mux.HandleFunc("POST /api/sessions/{id}/fork", a.handleForkSession)
	mux.HandleFunc("/api/", func(w http.ResponseWriter, _ *http.Request) {
		writeAPIError(w, http.StatusNotFound, "not_found", "API endpoint not found")
	})
	mux.Handle("/", webHandler())
	return mux
}

func (a *application) handleState(w http.ResponseWriter, r *http.Request) {
	noCache(w)
	cursor := a.backend.Events().Cursor()
	sessions, err := a.sessions.List(r.Context())
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, appbackend.StateSnapshot{
		Cursor:               cursor,
		Agent:                appbackend.AgentState{Capabilities: a.agent.Capabilities()},
		Assets:               appbackend.AssetState{Available: a.assets != nil, MaxBytes: a.config.MaxAssetBytes},
		Sessions:             sessions,
		Operations:           a.operations.List(),
		Turns:                a.turns.Snapshots(),
		OperationInvocations: a.operationInvocations.Snapshots(),
		Interactions:         a.backend.Interactions().Pending(),
		InteractionStates:    a.backend.Interactions().States(),
	})
}

func (a *application) handleEvents(w http.ResponseWriter, r *http.Request) {
	noCache(w)
	query := r.URL.Query()
	afterText := query.Get("after")
	if !query.Has("after") {
		afterText = r.Header.Get("Last-Event-ID")
		if afterText == "" {
			writeAPIError(w, http.StatusBadRequest, "event_cursor_required", "bootstrap with GET /api/state before subscribing")
			return
		}
	}
	after, err := strconv.ParseUint(afterText, 10, 64)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_event_cursor", "event cursor must be an unsigned integer")
		return
	}
	subscription, err := a.backend.Events().Subscribe(after)
	if err != nil {
		writeError(w, err)
		return
	}
	defer subscription.Close()
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeAPIError(w, http.StatusInternalServerError, "sse_unsupported", "streaming response is unavailable")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()
	for _, record := range subscription.Replay() {
		if err := writeSSERecord(w, flusher, record); err != nil {
			return
		}
	}
	var heartbeat <-chan time.Time
	var ticker *time.Ticker
	if a.config.Heartbeat > 0 {
		ticker = time.NewTicker(a.config.Heartbeat)
		heartbeat = ticker.C
		defer ticker.Stop()
	}
	for {
		select {
		case record, ok := <-subscription.Events():
			if !ok {
				return
			}
			if err := writeSSERecord(w, flusher, record); err != nil {
				return
			}
		case <-heartbeat:
			if _, err := io.WriteString(w, ": ping\n\n"); err != nil {
				return
			}
			flusher.Flush()
		case <-r.Context().Done():
			return
		}
	}
}

type createTurnRequest struct {
	SessionID   string                  `json:"sessionId"`
	Input       string                  `json:"input"`
	Attachments []appbackend.Attachment `json:"attachments,omitempty"`
}

func (a *application) handleCreateTurn(w http.ResponseWriter, r *http.Request) {
	var request createTurnRequest
	if err := decodeJSON(w, r, &request); err != nil {
		return
	}
	if request.SessionID == "" {
		writeAPIError(w, http.StatusBadRequest, "session_id_required", "sessionId is required")
		return
	}
	attachments := make([]content.Attachment, len(request.Attachments))
	for i, attachment := range request.Attachments {
		kind := map[string]content.Kind{"image": content.KindImage, "audio": content.KindAudio, "video": content.KindVideo, "file": content.KindFile}[attachment.Kind]
		attachments[i] = content.Attachment{Kind: kind, Media: content.Media{MIMEType: attachment.MIMEType, Name: attachment.Name, Source: content.Source{Kind: content.SourceAsset, Asset: asset.Reference{ID: attachment.AssetID}}}}
	}
	if err := content.ValidateAttachments(attachments); err != nil {
		writeError(w, err)
		return
	}
	id, err := a.turns.Start(agent.Turn{SessionID: session.ID(request.SessionID), Input: request.Input, Attachments: attachments})
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"id": id})
}

func (a *application) handleCancelTurn(w http.ResponseWriter, r *http.Request) {
	if err := a.turns.Cancel(r.PathValue("id")); err != nil {
		writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *application) handleListSessions(w http.ResponseWriter, r *http.Request) {
	items, err := a.sessions.List(r.Context())
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, items)
}

func (a *application) handleGetSession(w http.ResponseWriter, r *http.Request) {
	item, err := a.sessions.Get(r.Context(), session.ID(r.PathValue("id")))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, item)
}

func (a *application) handleCreateSession(w http.ResponseWriter, r *http.Request) {
	var request struct {
		Title string `json:"title"`
	}
	if err := decodeJSON(w, r, &request); err != nil {
		return
	}
	a.sessionMu.Lock()
	item, err := a.sessions.Create(r.Context(), request.Title)
	if err == nil {
		_ = a.backend.Events().Publish(appbackend.Event{Type: "session.created", Data: item})
	}
	a.sessionMu.Unlock()
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, item)
}

func (a *application) handleRenameSession(w http.ResponseWriter, r *http.Request) {
	var request struct {
		Title string `json:"title"`
	}
	if err := decodeJSON(w, r, &request); err != nil {
		return
	}
	if strings.TrimSpace(request.Title) == "" {
		writeAPIError(w, http.StatusBadRequest, "session_title_required", "title is required")
		return
	}
	a.sessionMu.Lock()
	item, err := a.sessions.Rename(r.Context(), session.ID(r.PathValue("id")), request.Title)
	if err == nil {
		_ = a.backend.Events().Publish(appbackend.Event{Type: "session.renamed", Data: item})
	}
	a.sessionMu.Unlock()
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, item)
}

func (a *application) handleHistory(w http.ResponseWriter, r *http.Request) {
	messages, err := a.agent.History(r.Context(), session.ID(r.PathValue("id")))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, projection.Messages(messages))
}

func (a *application) handleInteractionResponse(w http.ResponseWriter, r *http.Request) {
	var submission appbackend.InteractionSubmission
	if err := decodeJSON(w, r, &submission); err != nil {
		return
	}
	if err := a.backend.Interactions().Respond(r.PathValue("id"), submission); err != nil {
		writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *application) handleUploadAsset(w http.ResponseWriter, r *http.Request) {
	if a.assets == nil {
		writeAPIError(w, http.StatusNotImplemented, "asset_store_unavailable", "no asset store is configured")
		return
	}
	if r.ContentLength < 0 {
		writeAPIError(w, http.StatusLengthRequired, "asset_size_required", "Content-Length is required")
		return
	}
	if r.ContentLength > a.config.MaxAssetBytes {
		writeAPIError(w, http.StatusRequestEntityTooLarge, "asset_too_large", "asset exceeds max_asset_bytes")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, a.config.MaxAssetBytes)
	reference, info, err := a.assets.Put(r.Context(), asset.PutRequest{Body: r.Body, Size: uint64(r.ContentLength)})
	if err != nil {
		if errors.Is(err, io.ErrUnexpectedEOF) {
			writeAPIError(w, http.StatusBadRequest, "asset_size_mismatch", err.Error())
			return
		}
		writeError(w, err)
		return
	}
	if reference.ID == "" || info.Size != uint64(r.ContentLength) {
		writeAPIError(w, http.StatusInternalServerError, "invalid_asset_result", "asset store returned an invalid reference or size")
		return
	}
	writeJSON(w, http.StatusCreated, struct {
		ID   string `json:"id"`
		Size uint64 `json:"size"`
	}{ID: reference.ID, Size: info.Size})
}

func (a *application) handleReadAsset(w http.ResponseWriter, r *http.Request) {
	noCache(w)
	if a.assets == nil {
		writeAPIError(w, http.StatusNotImplemented, "asset_store_unavailable", "no asset store is configured")
		return
	}
	reference := asset.Reference{ID: r.PathValue("id")}
	info, err := a.assets.Stat(r.Context(), reference)
	if err != nil {
		writeError(w, err)
		return
	}
	reader, err := a.assets.Open(r.Context(), reference)
	if err != nil {
		writeError(w, err)
		return
	}
	defer reader.Close()
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", "attachment")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Length", strconv.FormatUint(info.Size, 10))
	if _, err := io.Copy(w, reader); err != nil {
		// A truncated successful response must not look like a complete asset.
		panic(http.ErrAbortHandler)
	}
}

func (a *application) handleInvokeOperation(w http.ResponseWriter, r *http.Request) {
	var request struct {
		SessionID string          `json:"sessionId"`
		Input     json.RawMessage `json:"input"`
	}
	if err := decodeJSON(w, r, &request); err != nil {
		return
	}
	id, err := a.operationInvocations.Start(r.PathValue("name"), session.ID(request.SessionID), request.Input)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"id": id})
}

func (a *application) handleCancelOperation(w http.ResponseWriter, r *http.Request) {
	if err := a.operationInvocations.Cancel(r.PathValue("id")); err != nil {
		writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *application) handleSessionLifecycle(eventType string, mutate func(context.Context, session.ID) (appbackend.Session, error)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		a.sessionMu.Lock()
		item, err := mutate(r.Context(), session.ID(r.PathValue("id")))
		if err == nil {
			_ = a.backend.Events().Publish(appbackend.Event{Type: eventType, Data: item})
		}
		a.sessionMu.Unlock()
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, item)
	}
}

func (a *application) handleDeleteSession(w http.ResponseWriter, r *http.Request) {
	id := session.ID(r.PathValue("id"))
	a.sessionMu.Lock()
	err := a.sessions.Delete(r.Context(), id)
	if err == nil {
		_ = a.backend.Events().Publish(appbackend.Event{Type: "session.deleted", Data: map[string]string{"id": string(id)}})
	}
	a.sessionMu.Unlock()
	if err != nil {
		writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *application) handleForkSession(w http.ResponseWriter, r *http.Request) {
	var request struct {
		Title string `json:"title"`
	}
	if err := decodeJSON(w, r, &request); err != nil {
		return
	}
	a.sessionMu.Lock()
	item, err := a.sessions.Fork(r.Context(), session.ID(r.PathValue("id")), request.Title)
	if err == nil {
		_ = a.backend.Events().Publish(appbackend.Event{Type: "session.created", Data: item})
	}
	a.sessionMu.Unlock()
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, item)
}

func decodeJSON(w http.ResponseWriter, r *http.Request, destination any) error {
	r.Body = http.MaxBytesReader(w, r.Body, maxJSONBody)
	decoder := json.NewDecoder(r.Body)
	var raw json.RawMessage
	if err := decoder.Decode(&raw); err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_json", err.Error())
		return err
	}
	if trimmed := bytes.TrimSpace(raw); len(trimmed) == 0 || trimmed[0] != '{' {
		err := errors.New("request body must be a JSON object")
		writeAPIError(w, http.StatusBadRequest, "invalid_json", err.Error())
		return err
	}
	objectDecoder := json.NewDecoder(bytes.NewReader(raw))
	objectDecoder.DisallowUnknownFields()
	if err := objectDecoder.Decode(destination); err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_json", err.Error())
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		if err == nil {
			err = errors.New("request body must contain exactly one JSON value")
		}
		writeAPIError(w, http.StatusBadRequest, "invalid_json", err.Error())
		return err
	}
	return nil
}

func writeSSERecord(w io.Writer, flusher http.Flusher, record appbackend.EventRecord) error {
	if _, err := fmt.Fprintf(w, "id: %d\ndata: %s\n\n", record.ID, record.Data); err != nil {
		return err
	}
	flusher.Flush()
	return nil
}

func noCache(w http.ResponseWriter) {
	w.Header().Set("Cache-Control", "no-store")
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	data, err := json.Marshal(value)
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "internal_error", "failed to encode response")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(append(data, '\n'))
}

func writeError(w http.ResponseWriter, err error) {
	status, detail := apiError(err)
	writeJSON(w, status, appbackend.ErrorResponse{Error: detail})
}

func apiError(err error) (int, appbackend.ErrorDetail) {
	status, code := http.StatusInternalServerError, "internal_error"
	switch {
	case errors.Is(err, appbackend.ErrApplicationClosed):
		status, code = http.StatusServiceUnavailable, "application_closed"
	case errors.Is(err, appbackend.ErrInvalidOperationInput):
		status, code = http.StatusBadRequest, "operation_invalid_input"
	case errors.Is(err, appbackend.ErrInvalidOperationOutput):
		status, code = http.StatusInternalServerError, "operation_invalid_output"
	case errors.Is(err, appbackend.ErrOperationSettled):
		status, code = http.StatusConflict, "operation_already_settled"
	case errors.Is(err, operation.ErrUnavailable):
		status, code = http.StatusConflict, "operation_unavailable"
	case errors.Is(err, appbackend.ErrCapabilityUnavailable):
		status, code = http.StatusNotImplemented, "capability_unavailable"
	case errors.Is(err, content.ErrInvalidContent):
		status, code = http.StatusBadRequest, "invalid_content"
	case errors.Is(err, content.ErrUnsupportedContent):
		status, code = http.StatusUnprocessableEntity, "unsupported_content"
	case errors.Is(err, session.ErrArchived):
		status, code = http.StatusConflict, "session_archived"
	case errors.Is(err, context.Canceled):
		status, code = http.StatusRequestTimeout, "canceled"
	case errors.Is(err, context.DeadlineExceeded):
		status, code = http.StatusRequestTimeout, "deadline_exceeded"
	case errors.Is(err, appbackend.ErrInvalidInteractionResponse):
		status, code = http.StatusBadRequest, "interaction_invalid_response"
	case errors.Is(err, appbackend.ErrInteractionNotFound):
		status, code = http.StatusConflict, "interaction_already_settled"
	case errors.Is(err, appbackend.ErrCursorExpired):
		status, code = http.StatusConflict, "event_cursor_expired"
	case errors.Is(err, appbackend.ErrCursorAhead):
		status, code = http.StatusConflict, "event_cursor_ahead"
	case errors.Is(err, appbackend.ErrTurnNotFound), errors.Is(err, session.ErrNotFound), errors.Is(err, fs.ErrNotExist), errors.Is(err, appbackend.ErrOperationNotFound), errors.Is(err, appbackend.ErrOperationInvocationNotFound):
		status, code = http.StatusNotFound, "not_found"
	}
	return status, appbackend.ErrorDetail{Code: code, Message: err.Error()}
}

func writeAPIError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, appbackend.ErrorResponse{Error: appbackend.ErrorDetail{Code: code, Message: message}})
}
