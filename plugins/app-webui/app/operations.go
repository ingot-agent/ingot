package appcomponent

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"sync"
	"unicode/utf8"

	appbackend "github.com/ingot-agent/app-webui"
	"github.com/ingot-agent/sdk/operation"
	"github.com/ingot-agent/sdk/session"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

var operationName = regexp.MustCompile(`^[a-z][a-z0-9]*([._-][a-z0-9]+)*$`)

type registeredOperation struct {
	operation     operation.Operation
	input, output *jsonschema.Schema
}

type operationController struct {
	// entries is keyed by the host-generated internal ID, never by Name:
	// two Plugins may export the same display name without colliding.
	entries     map[string]registeredOperation
	definitions []appbackend.OperationDefinition
}

// operationGroupName validates the optional presentation group hint.
var operationGroupName = regexp.MustCompile(`^[a-z][a-z0-9]*([._-][a-z0-9]+)*$`)

// newOperationController snapshots every operation definition, assigning a
// stable internal ID to each. Same-name operations coexist: identity is the
// internal ID, and Name is display-only.
func newOperationController(operations []operation.Operation) (*operationController, error) {
	c := &operationController{entries: make(map[string]registeredOperation), definitions: make([]appbackend.OperationDefinition, 0, len(operations))}
	for i, candidate := range operations {
		if isNil(candidate) {
			return nil, fmt.Errorf("operation %d is nil: %w", i, appbackend.ErrInvalidOperationDefinition)
		}
		definition := candidate.Definition()
		if !operationName.MatchString(definition.Name) || definition.Description == "" || !utf8.ValidString(definition.Description) {
			return nil, fmt.Errorf("operation %d has invalid name or description: %w", i, appbackend.ErrInvalidOperationDefinition)
		}
		if definition.Group != "" && (!operationGroupName.MatchString(definition.Group) || !utf8.ValidString(definition.Group)) {
			return nil, fmt.Errorf("operation %q has invalid group %q: %w", definition.Name, definition.Group, appbackend.ErrInvalidOperationDefinition)
		}
		id := operationInternalID(definition.Name, i)
		if _, exists := c.entries[id]; exists {
			return nil, fmt.Errorf("duplicate operation internal id %q: %w", id, appbackend.ErrInvalidOperationDefinition)
		}
		input, err := compileOperationSchema(id+":input", definition.InputSchema)
		if err != nil {
			return nil, fmt.Errorf("operation %q input schema: %w: %w", definition.Name, appbackend.ErrInvalidOperationDefinition, err)
		}
		output, err := compileOperationSchema(id+":output", definition.OutputSchema)
		if err != nil {
			return nil, fmt.Errorf("operation %q output schema: %w: %w", definition.Name, appbackend.ErrInvalidOperationDefinition, err)
		}
		c.entries[id] = registeredOperation{operation: candidate, input: input, output: output}
		c.definitions = append(c.definitions, appbackend.OperationDefinition{ID: id, Name: definition.Name, Description: definition.Description, Group: definition.Group,
			InputSchema: bytes.Clone(definition.InputSchema), OutputSchema: bytes.Clone(definition.OutputSchema)})
	}
	return c, nil
}

// operationInternalID derives the stable internal identity of one operation.
// The index makes the ID unique even when two Plugins export the same name,
// while remaining deterministic for a fixed graph composition so a client can
// address the same operation across restarts.
func operationInternalID(name string, index int) string {
	digest := sha256.Sum256([]byte(fmt.Sprintf("%d\x00%s", index, name)))
	return name + "-" + hex.EncodeToString(digest[:8])
}

func decodeObject(raw json.RawMessage) (map[string]any, error) {
	if !json.Valid(raw) {
		return nil, fmt.Errorf("must be valid JSON")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value map[string]any
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	if value == nil {
		return nil, fmt.Errorf("must be a JSON object")
	}
	return value, nil
}

func compileOperationSchema(name string, raw json.RawMessage) (*jsonschema.Schema, error) {
	document, err := decodeObject(raw)
	if err != nil {
		return nil, err
	}
	if declaration, exists := document["$schema"]; exists {
		declared, ok := declaration.(string)
		declared = strings.TrimSuffix(declared, "#")
		if !ok || (declared != "https://json-schema.org/draft/2020-12/schema" && declared != "http://json-schema.org/draft/2020-12/schema") {
			return nil, fmt.Errorf("schema must use Draft 2020-12")
		}
	}
	if kind, exists := document["type"]; exists {
		if kinds, ok := kind.([]any); ok && len(kinds) == 1 {
			kind = kinds[0]
		}
		if kind != "object" {
			return nil, fmt.Errorf("schema must describe an object")
		}
	}
	compiler := jsonschema.NewCompiler()
	compiler.DefaultDraft(jsonschema.Draft2020)
	// Definitions must be self-contained; construction never fetches references.
	compiler.UseLoader(nil)
	location := "urn:ingot:operation:" + name
	if err := compiler.AddResource(location, document); err != nil {
		return nil, err
	}
	return compiler.Compile(location)
}

func (c *operationController) List() []appbackend.OperationDefinition {
	result := make([]appbackend.OperationDefinition, len(c.definitions))
	for i, definition := range c.definitions {
		result[i] = definition
		result[i].InputSchema = bytes.Clone(definition.InputSchema)
		result[i].OutputSchema = bytes.Clone(definition.OutputSchema)
	}
	return result
}

// definition returns the public definition of one operation by internal ID.
func (c *operationController) definition(id string) (appbackend.OperationDefinition, bool) {
	for _, item := range c.definitions {
		if item.ID == id {
			return item, true
		}
	}
	return appbackend.OperationDefinition{}, false
}

func (c *operationController) validateInput(id string, raw json.RawMessage) error {
	entry, ok := c.entries[id]
	if !ok {
		return appbackend.ErrOperationNotFound
	}
	value, err := decodeObject(raw)
	if err == nil {
		err = entry.input.Validate(value)
	}
	if err != nil {
		return fmt.Errorf("operation %q input: %w: %w", id, appbackend.ErrInvalidOperationInput, err)
	}
	return nil
}

func (c *operationController) Invoke(ctx context.Context, id string, request operation.Request) (operation.Result, error) {
	if ctx == nil {
		return operation.Result{}, fmt.Errorf("nil operation context")
	}
	if err := ctx.Err(); err != nil {
		return operation.Result{}, err
	}
	if err := c.validateInput(id, request.Input); err != nil {
		return operation.Result{}, err
	}
	if isNil(request.Interaction) {
		return operation.Result{}, fmt.Errorf("operation interaction channel is required")
	}
	entry := c.entries[id]
	request.Input = bytes.Clone(request.Input)
	result, err := entry.operation.Invoke(ctx, request)
	if err != nil {
		return operation.Result{}, err
	}
	value, err := decodeObject(result.Output)
	if err == nil {
		err = entry.output.Validate(value)
	}
	if err != nil {
		return operation.Result{}, fmt.Errorf("operation %q output: %w: %w", id, appbackend.ErrInvalidOperationOutput, err)
	}
	result.Output = bytes.Clone(result.Output)
	return result, nil
}

type operationInvocation struct {
	snapshot appbackend.OperationSnapshot
	cancel   context.CancelFunc
}

type operationRegistry struct {
	mu                 sync.Mutex
	appCtx             context.Context
	controller         *operationController
	host               appbackend.InteractionHost
	events             appbackend.EventSink
	nextID             uint64
	entries            map[string]*operationInvocation
	terminal           []string
	retention, running int
	closed             bool
	done               chan struct{}
}

func newOperationRegistry(ctx context.Context, controller *operationController, host appbackend.InteractionHost, events appbackend.EventSink, retention int) *operationRegistry {
	return &operationRegistry{appCtx: ctx, controller: controller, host: host, events: events, retention: retention,
		entries: make(map[string]*operationInvocation), done: make(chan struct{})}
}

// Start begins one invocation addressed by the operation's internal ID. The
// snapshot carries the display Name separately so a same-name operation is
// still distinguishable in the UI.
func (r *operationRegistry) Start(operationID string, sessionID session.ID, input json.RawMessage) (string, error) {
	input = bytes.Clone(input)
	if err := r.controller.validateInput(operationID, input); err != nil {
		return "", err
	}
	definition, ok := r.controller.definition(operationID)
	if !ok {
		return "", appbackend.ErrOperationNotFound
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed || r.appCtx.Err() != nil {
		return "", appbackend.ErrApplicationClosed
	}
	r.nextID++
	id := fmt.Sprintf("operation-inv-%d", r.nextID)
	ctx, cancel := context.WithCancel(r.appCtx)
	entry := &operationInvocation{snapshot: appbackend.OperationSnapshot{ID: id, OperationID: operationID, Name: definition.Name, SessionID: string(sessionID), Status: "running"}, cancel: cancel}
	scope := appbackend.Scope{Operation: &appbackend.OperationScope{InvocationID: id}}
	if err := r.events.Publish(appbackend.Event{Type: "operation.started", Scope: &scope, Data: entry.snapshot}); err != nil {
		cancel()
		return "", err
	}
	r.entries[id] = entry
	r.running++
	request := operation.Request{SessionID: sessionID, Input: input, Interaction: r.host.Scoped(scope)}
	go r.run(ctx, entry, request)
	return id, nil
}

func (r *operationRegistry) run(ctx context.Context, entry *operationInvocation, request operation.Request) {
	result, err := r.controller.Invoke(ctx, entry.snapshot.OperationID, request)
	entry.cancel()
	r.mu.Lock()
	defer r.mu.Unlock()
	entry.snapshot.Status = executionStatus(err)
	eventType := "operation.completed"
	if err == nil {
		entry.snapshot.Result = &appbackend.OperationResult{Output: bytes.Clone(result.Output)}
	} else {
		_, detail := apiError(err)
		entry.snapshot.Error = &detail
		eventType = "operation." + entry.snapshot.Status
	}
	_ = r.events.Publish(appbackend.Event{Type: eventType, Scope: &appbackend.Scope{Operation: &appbackend.OperationScope{InvocationID: entry.snapshot.ID}}, Data: entry.snapshot})
	r.terminal = append(r.terminal, entry.snapshot.ID)
	for len(r.terminal) > r.retention {
		delete(r.entries, r.terminal[0])
		r.terminal = r.terminal[1:]
	}
	r.running--
	if r.closed && r.running == 0 {
		close(r.done)
	}
}

func (r *operationRegistry) Cancel(id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	entry, exists := r.entries[id]
	if !exists {
		return appbackend.ErrOperationInvocationNotFound
	}
	if entry.snapshot.Status != "running" {
		return appbackend.ErrOperationSettled
	}
	entry.cancel()
	return nil
}

func (r *operationRegistry) Snapshots() []appbackend.OperationSnapshot {
	r.mu.Lock()
	defer r.mu.Unlock()
	result := make([]appbackend.OperationSnapshot, 0, len(r.entries))
	for _, entry := range r.entries {
		snapshot := entry.snapshot
		if snapshot.Result != nil {
			snapshot.Result = &appbackend.OperationResult{Output: bytes.Clone(snapshot.Result.Output)}
		}
		if snapshot.Error != nil {
			detail := *snapshot.Error
			snapshot.Error = &detail
		}
		result = append(result, snapshot)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].ID < result[j].ID })
	return result
}

func (r *operationRegistry) stop() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return
	}
	r.closed = true
	for _, entry := range r.entries {
		if entry.snapshot.Status == "running" {
			entry.cancel()
		}
	}
	if r.running == 0 {
		close(r.done)
	}
}
