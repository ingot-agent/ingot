package hostcomponent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"sync"
	"sync/atomic"

	appbackend "github.com/ingot-agent/app-webui"
	"github.com/ingot-agent/sdk/interaction"
	"github.com/ingot-agent/sdk/observation"
)

type pendingResult struct {
	response interaction.Response
	err      error
}

type pendingRequest struct {
	scope   *appbackend.Scope
	id      string
	request interaction.Request
	result  chan pendingResult
}

type interactionHost struct {
	mu      sync.Mutex
	nextID  atomic.Uint64
	pending map[string]*pendingRequest
	states  map[string]appbackend.InteractionState
	events  appbackend.EventSink
}

func newInteractionHost(events appbackend.EventSink) *interactionHost {
	return &interactionHost{
		pending: make(map[string]*pendingRequest),
		states:  make(map[string]appbackend.InteractionState),
		events:  events,
	}
}

func (h *interactionHost) Request(ctx context.Context, request interaction.Request) (interaction.Response, error) {
	return h.request(ctx, request, contextScope(ctx))
}

func (h *interactionHost) request(ctx context.Context, request interaction.Request, scope *appbackend.Scope) (interaction.Response, error) {
	if ctx == nil {
		return interaction.Response{}, interaction.ErrUnavailable
	}
	if err := ctx.Err(); err != nil {
		return interaction.Response{}, err
	}
	if err := validateRequest(request); err != nil {
		return interaction.Response{}, err
	}
	id := fmt.Sprintf("interaction-%d", h.nextID.Add(1))
	pending := &pendingRequest{id: id, scope: appbackend.CloneScope(scope), request: cloneRequest(request), result: make(chan pendingResult, 1)}
	h.mu.Lock()
	h.pending[id] = pending
	if err := h.events.Publish(appbackend.Event{Type: "interaction.requested", Scope: pending.scope, Data: projectPending(pending)}); err != nil {
		delete(h.pending, id)
		h.mu.Unlock()
		return interaction.Response{}, err
	}
	h.mu.Unlock()
	select {
	case result := <-pending.result:
		return result.response, result.err
	case <-ctx.Done():
		h.mu.Lock()
		if h.pending[id] == pending {
			delete(h.pending, id)
			_ = h.events.Publish(appbackend.Event{Type: "interaction.canceled", Scope: pending.scope, Data: map[string]string{"id": id}})
			h.mu.Unlock()
			return interaction.Response{}, ctx.Err()
		}
		h.mu.Unlock()
		result := <-pending.result
		return result.response, result.err
	}
}

func (h *interactionHost) Respond(id string, submission appbackend.InteractionSubmission) error {
	h.mu.Lock()
	pending, ok := h.pending[id]
	h.mu.Unlock()
	if !ok {
		return appbackend.ErrInteractionNotFound
	}
	response, err := decodeSubmission(pending.request, submission)
	if err != nil {
		return err
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.pending[id] != pending {
		return appbackend.ErrInteractionNotFound
	}
	if err := h.events.Publish(appbackend.Event{Type: "interaction.resolved", Scope: pending.scope, Data: map[string]string{"id": id}}); err != nil {
		return err
	}
	delete(h.pending, id)
	pending.result <- pendingResult{response: response}
	return nil
}

func (h *interactionHost) Emit(ctx context.Context, event interaction.Event) error {
	return h.emit(ctx, event, contextScope(ctx))
}

func (h *interactionHost) emit(ctx context.Context, event interaction.Event, scope *appbackend.Scope) error {
	if ctx == nil {
		return interaction.ErrUnavailable
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if event.Name == "" {
		return errors.New("interaction event name is required")
	}
	if err := validateLevel(event.Level); err != nil {
		return err
	}
	return h.events.Publish(appbackend.Event{Type: "interaction.event", Scope: scope, Data: map[string]any{
		"name": event.Name, "level": levelName(event.Level), "message": event.Message,
	}})
}

func (h *interactionHost) Set(ctx context.Context, state interaction.State) error {
	return h.set(ctx, state, contextScope(ctx))
}

func (h *interactionHost) set(ctx context.Context, state interaction.State, scope *appbackend.Scope) error {
	if ctx == nil {
		return interaction.ErrUnavailable
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	projected, err := projectState(state)
	if err != nil {
		return err
	}
	projected.Scope = appbackend.CloneScope(scope)
	h.mu.Lock()
	defer h.mu.Unlock()
	// Keep the authoritative mutation and its event ordered under one lock.
	if err := h.events.Publish(appbackend.Event{Type: "interaction.state.set", Scope: scope, Data: projected}); err != nil {
		return err
	}
	h.states[projected.ID] = cloneState(projected)
	return nil
}

func (h *interactionHost) Clear(ctx context.Context, name string) error {
	return h.clear(ctx, name, contextScope(ctx))
}

// contextScope projects only correlation supplied by the execution context.
// A round index alone has no presence marker in the SDK; include it when a
// tool scope makes its meaning unambiguous.
func contextScope(ctx context.Context) *appbackend.Scope {
	correlation, ok := observation.CorrelationFromContext(ctx)
	if !ok || (correlation.SessionID == "" && correlation.TurnID == "") {
		return nil
	}
	agent := &appbackend.AgentScope{
		SessionID: string(correlation.SessionID), TurnID: string(correlation.TurnID),
		ToolCallID: correlation.ToolCallID,
	}
	if correlation.ToolCallID != "" {
		index := correlation.RoundIndex
		agent.RoundIndex = &index
	}
	return &appbackend.Scope{Agent: agent}
}

func (h *interactionHost) clear(ctx context.Context, name string, scope *appbackend.Scope) error {
	if ctx == nil {
		return interaction.ErrUnavailable
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if name == "" {
		return errors.New("interaction state name is required")
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if err := h.events.Publish(appbackend.Event{Type: "interaction.state.clear", Scope: scope, Data: map[string]string{"id": name}}); err != nil {
		return err
	}
	delete(h.states, name)
	return nil
}

func (h *interactionHost) Pending() []appbackend.PendingInteraction {
	h.mu.Lock()
	result := make([]appbackend.PendingInteraction, 0, len(h.pending))
	for _, pending := range h.pending {
		result = append(result, projectPending(pending))
	}
	h.mu.Unlock()
	sort.Slice(result, func(i, j int) bool { return result[i].ID < result[j].ID })
	return result
}

func (h *interactionHost) States() []appbackend.InteractionState {
	h.mu.Lock()
	result := make([]appbackend.InteractionState, 0, len(h.states))
	for _, state := range h.states {
		result = append(result, cloneState(state))
	}
	h.mu.Unlock()
	sort.Slice(result, func(i, j int) bool { return result[i].ID < result[j].ID })
	return result
}

func validateRequest(request interaction.Request) error {
	if request.Name == "" {
		return errors.New("interaction request name is required")
	}
	if err := validateLevel(request.Level); err != nil {
		return err
	}
	seen := make(map[string]struct{}, len(request.Fields))
	for _, field := range request.Fields {
		if field.Name == "" {
			return errors.New("interaction field name is required")
		}
		if _, ok := seen[field.Name]; ok {
			return fmt.Errorf("duplicate interaction field %q", field.Name)
		}
		seen[field.Name] = struct{}{}
		if _, err := fieldKindName(field.Kind); err != nil {
			return err
		}
		if (field.Kind == interaction.FieldChoice || field.Kind == interaction.FieldMultiChoice) && len(field.Options) == 0 {
			return fmt.Errorf("field %q requires choices", field.Name)
		}
		if len(field.Options) > 0 && field.Kind != interaction.FieldString && field.Kind != interaction.FieldChoice && field.Kind != interaction.FieldMultiChoice {
			return fmt.Errorf("field %q does not support options", field.Name)
		}
		options := make(map[string]struct{}, len(field.Options))
		for _, option := range field.Options {
			if _, exists := options[option.Value]; exists {
				return fmt.Errorf("field %q has duplicate option %q", field.Name, option.Value)
			}
			options[option.Value] = struct{}{}
		}
		if field.Default != nil {
			if err := validateDefault(field); err != nil {
				return fmt.Errorf("field %q default: %w", field.Name, err)
			}
		}
	}
	return nil
}

func validateDefault(field interaction.Field) error {
	value := *field.Default
	if err := validateValue(value); err != nil {
		return err
	}
	var kind interaction.ValueKind
	switch field.Kind {
	case interaction.FieldString, interaction.FieldChoice:
		kind = interaction.ValueString
	case interaction.FieldInteger:
		kind = interaction.ValueInteger
	case interaction.FieldNumber:
		kind = interaction.ValueNumber
	case interaction.FieldBoolean:
		kind = interaction.ValueBoolean
	case interaction.FieldMultiChoice:
		kind = interaction.ValueStrings
	}
	if value.Kind != kind {
		return errors.New("value kind does not match field kind")
	}
	raw, err := json.Marshal(projectValue(value))
	if err != nil {
		return err
	}
	_, err = decodeField(field, raw)
	return err
}

func validateLevel(level interaction.Level) error {
	if level > interaction.LevelError {
		return fmt.Errorf("unsupported interaction level %d", level)
	}
	return nil
}

func validateValue(value interaction.Value) error {
	switch value.Kind {
	case interaction.ValueString, interaction.ValueInteger, interaction.ValueBoolean, interaction.ValueStrings:
		return nil
	case interaction.ValueNumber:
		if !math.IsNaN(value.Number) && !math.IsInf(value.Number, 0) {
			return nil
		}
		return errors.New("number must be finite")
	default:
		return fmt.Errorf("unsupported interaction value kind %d", value.Kind)
	}
}

func decodeSubmission(request interaction.Request, submission appbackend.InteractionSubmission) (interaction.Response, error) {
	values := submission.Values
	if values == nil {
		values = map[string]json.RawMessage{}
	}
	known := make(map[string]struct{}, len(request.Fields))
	answers := make([]interaction.Answer, 0, len(request.Fields))
	for _, field := range request.Fields {
		known[field.Name] = struct{}{}
		raw, present := values[field.Name]
		if !present {
			if field.Default != nil {
				answers = append(answers, interaction.Answer{Name: field.Name, Value: cloneValue(*field.Default)})
				continue
			}
			if field.Required {
				return interaction.Response{}, fmt.Errorf("field %q is required: %w", field.Name, appbackend.ErrInvalidInteractionResponse)
			}
			continue
		}
		value, err := decodeField(field, raw)
		if err != nil {
			return interaction.Response{}, fmt.Errorf("field %q: %v: %w", field.Name, err, appbackend.ErrInvalidInteractionResponse)
		}
		answers = append(answers, interaction.Answer{Name: field.Name, Value: value})
	}
	for name := range values {
		if _, ok := known[name]; !ok {
			return interaction.Response{}, fmt.Errorf("unknown field %q: %w", name, appbackend.ErrInvalidInteractionResponse)
		}
	}
	return interaction.Response{Values: answers}, nil
}

func decodeField(field interaction.Field, raw json.RawMessage) (interaction.Value, error) {
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return interaction.Value{}, errors.New("must not be null")
	}
	switch field.Kind {
	case interaction.FieldString, interaction.FieldChoice:
		var value string
		if err := json.Unmarshal(raw, &value); err != nil {
			return interaction.Value{}, errors.New("must be a string")
		}
		if field.Kind == interaction.FieldChoice && !containsOption(field.Options, value) {
			return interaction.Value{}, errors.New("must be one of the allowed choices")
		}
		return interaction.StringValue(value), nil
	case interaction.FieldInteger:
		var value int64
		if err := json.Unmarshal(raw, &value); err != nil {
			return interaction.Value{}, errors.New("must be an integer")
		}
		return interaction.IntegerValue(value), nil
	case interaction.FieldNumber:
		var value float64
		if err := json.Unmarshal(raw, &value); err != nil {
			return interaction.Value{}, errors.New("must be a number")
		}
		return interaction.NumberValue(value), nil
	case interaction.FieldBoolean:
		var value bool
		if err := json.Unmarshal(raw, &value); err != nil {
			return interaction.Value{}, errors.New("must be a boolean")
		}
		return interaction.BooleanValue(value), nil
	case interaction.FieldMultiChoice:
		var decoded []*string
		if err := json.Unmarshal(raw, &decoded); err != nil {
			return interaction.Value{}, errors.New("must be an array of strings")
		}
		values := make([]string, len(decoded))
		for i, value := range decoded {
			if value == nil {
				return interaction.Value{}, errors.New("choices must not contain null")
			}
			if !containsOption(field.Options, *value) {
				return interaction.Value{}, fmt.Errorf("value %q is not an allowed choice", *value)
			}
			values[i] = *value
		}
		return interaction.StringsValue(values), nil
	default:
		return interaction.Value{}, fmt.Errorf("unsupported field kind %d", field.Kind)
	}
}

func containsOption(options []interaction.Option, value string) bool {
	for _, option := range options {
		if option.Value == value {
			return true
		}
	}
	return false
}

func projectPending(pending *pendingRequest) appbackend.PendingInteraction {
	request := pending.request
	result := appbackend.PendingInteraction{
		Scope: appbackend.CloneScope(pending.scope),
		ID:    pending.id, Name: request.Name, Level: levelName(request.Level), Message: request.Description,
		Fields: make([]appbackend.InteractionField, 0, len(request.Fields)),
	}
	for _, field := range request.Fields {
		kind, _ := fieldKindName(field.Kind)
		projected := appbackend.InteractionField{
			Name: field.Name, Label: field.Label, Description: field.Description,
			Kind: kind, Required: field.Required, Sensitive: field.Sensitive, HasDefault: field.Default != nil,
			Options: make([]appbackend.InteractionOption, len(field.Options)),
		}
		if field.Default != nil && !field.Sensitive {
			projected.Default = projectValue(*field.Default)
		}
		for i, option := range field.Options {
			projected.Options[i] = appbackend.InteractionOption{Value: option.Value, Label: option.Label, Description: option.Description}
		}
		result.Fields = append(result.Fields, projected)
	}
	return result
}

func projectState(state interaction.State) (appbackend.InteractionState, error) {
	if state.Name == "" {
		return appbackend.InteractionState{}, errors.New("interaction state name is required")
	}
	if err := validateLevel(state.Level); err != nil {
		return appbackend.InteractionState{}, err
	}
	result := appbackend.InteractionState{
		ID: state.Name, Name: state.Name, Level: levelName(state.Level), Description: state.Description,
		Values: make([]appbackend.InteractionStateEntry, len(state.Values)),
	}
	seen := make(map[string]struct{}, len(state.Values))
	for i, entry := range state.Values {
		if entry.Name == "" {
			return appbackend.InteractionState{}, errors.New("interaction state entry name is required")
		}
		if _, exists := seen[entry.Name]; exists {
			return appbackend.InteractionState{}, fmt.Errorf("duplicate interaction state entry %q", entry.Name)
		}
		seen[entry.Name] = struct{}{}
		if err := validateValue(entry.Value); err != nil {
			return appbackend.InteractionState{}, fmt.Errorf("state entry %q: %w", entry.Name, err)
		}
		result.Values[i] = appbackend.InteractionStateEntry{
			Name: entry.Name, Label: entry.Label, Description: entry.Description, Value: projectValue(entry.Value),
		}
	}
	return result, nil
}

func levelName(level interaction.Level) string {
	switch level {
	case interaction.LevelInfo:
		return "info"
	case interaction.LevelWarning:
		return "warning"
	case interaction.LevelError:
		return "error"
	default:
		return ""
	}
}

func fieldKindName(kind interaction.FieldKind) (string, error) {
	switch kind {
	case interaction.FieldString:
		return "string", nil
	case interaction.FieldInteger:
		return "integer", nil
	case interaction.FieldNumber:
		return "number", nil
	case interaction.FieldBoolean:
		return "boolean", nil
	case interaction.FieldChoice:
		return "choice", nil
	case interaction.FieldMultiChoice:
		return "multichoice", nil
	default:
		return "", fmt.Errorf("unsupported interaction field kind %d", kind)
	}
}

func projectValue(value interaction.Value) any {
	switch value.Kind {
	case interaction.ValueString:
		return value.String
	case interaction.ValueInteger:
		return value.Integer
	case interaction.ValueNumber:
		return value.Number
	case interaction.ValueBoolean:
		return value.Boolean
	case interaction.ValueStrings:
		return append([]string{}, value.Strings...)
	default:
		return nil
	}
}

func cloneRequest(request interaction.Request) interaction.Request {
	result := request
	result.Fields = make([]interaction.Field, len(request.Fields))
	for i, field := range request.Fields {
		result.Fields[i] = field
		result.Fields[i].Options = append([]interaction.Option(nil), field.Options...)
		if field.Default != nil {
			value := cloneValue(*field.Default)
			result.Fields[i].Default = &value
		}
	}
	return result
}

func cloneValue(value interaction.Value) interaction.Value {
	value.Strings = append([]string(nil), value.Strings...)
	return value
}

func cloneState(state appbackend.InteractionState) appbackend.InteractionState {
	result := state
	result.Scope = appbackend.CloneScope(state.Scope)
	result.Values = append([]appbackend.InteractionStateEntry{}, state.Values...)
	for i := range result.Values {
		if strings, ok := result.Values[i].Value.([]string); ok {
			result.Values[i].Value = append([]string{}, strings...)
		}
	}
	return result
}

var _ appbackend.InteractionHost = (*interactionHost)(nil)

func (h *interactionHost) Scoped(scope appbackend.Scope) interaction.Channel {
	return &scopedChannel{host: h, scope: appbackend.CloneScope(&scope)}
}

type scopedChannel struct {
	host  *interactionHost
	scope *appbackend.Scope
}

func (c *scopedChannel) Request(ctx context.Context, request interaction.Request) (interaction.Response, error) {
	return c.host.request(ctx, request, c.scope)
}
func (c *scopedChannel) Emit(ctx context.Context, event interaction.Event) error {
	return c.host.emit(ctx, event, c.scope)
}
func (c *scopedChannel) Set(ctx context.Context, state interaction.State) error {
	return c.host.set(ctx, state, c.scope)
}
func (c *scopedChannel) Clear(ctx context.Context, name string) error {
	return c.host.clear(ctx, name, c.scope)
}
