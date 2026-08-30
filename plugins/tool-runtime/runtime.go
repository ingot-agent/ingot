// Package toolruntime implements the standard tool lookup and invocation chokepoint.
package toolruntime

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"regexp"
	"unicode/utf8"

	"github.com/ingot-agent/ingot-abi"
	"github.com/ingot-agent/sdk/pipeline"
	"github.com/ingot-agent/sdk/tool"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

const (
	defaultMaxArgumentsBytes = 1024 * 1024
	defaultMaxResultBytes    = 4 * 1024 * 1024
)

var (
	// ErrInvalidConfig indicates invalid runtime limits or dependencies.
	ErrInvalidConfig = errors.New("invalid tool.runtime config")
	// ErrInvalidDefinition indicates a malformed or duplicate tool definition.
	ErrInvalidDefinition = errors.New("invalid tool definition")
	// ErrInvalidResult indicates invalid UTF-8 or an oversized result.
	ErrInvalidResult = errors.New("invalid tool result")
	// ErrCallMutation indicates that an interceptor changed a validated Call.
	ErrCallMutation = errors.New("tool call mutation is not allowed")
	toolNamePattern = regexp.MustCompile("^[a-z][a-z0-9]*(?:[._-][a-z0-9]+)*$")
)

// Config bounds argument and result payloads.
type Config struct {
	MaxArgumentsBytes int `toml:"max_arguments_bytes"`
	MaxResultBytes    int `toml:"max_result_bytes"`
}

// Dependencies are the tools and interceptors assembled by the host.
type Dependencies struct {
	Tools        []tool.Tool
	Interceptors []tool.Interceptor
}

// Exports contains the runtime capability.
type Exports struct {
	Runtime tool.Runtime
}

type runtime struct {
	definitions  []tool.Definition
	entries      map[string]registeredTool
	interceptors []tool.Interceptor
	maxArguments int
	maxResult    int
}

type registeredTool struct {
	tool   tool.Tool
	schema *jsonschema.Schema
}

// New snapshots and validates all tool definitions, then composes the immutable runtime.
func New(ctx context.Context, cfg Config, deps Dependencies) (Exports, ingotabi.Cleanup, error) {
	if ctx == nil {
		return Exports{}, nil, fmt.Errorf("construct tool.runtime: %w", ErrInvalidConfig)
	}
	if err := ctx.Err(); err != nil {
		return Exports{}, nil, err
	}
	maxArguments := cfg.MaxArgumentsBytes
	if maxArguments == 0 {
		maxArguments = defaultMaxArgumentsBytes
	}
	if maxArguments < 1 {
		return Exports{}, nil, fmt.Errorf("max_arguments_bytes must be positive: %w", ErrInvalidConfig)
	}
	maxResult := cfg.MaxResultBytes
	if maxResult == 0 {
		maxResult = defaultMaxResultBytes
	}
	if maxResult < 1 {
		return Exports{}, nil, fmt.Errorf("max_result_bytes must be positive: %w", ErrInvalidConfig)
	}
	entries := make(map[string]registeredTool, len(deps.Tools))
	definitions := make([]tool.Definition, 0, len(deps.Tools))
	for i, candidate := range deps.Tools {
		if isNil(candidate) {
			return Exports{}, nil, fmt.Errorf("tools[%d] is nil: %w", i, ErrInvalidDefinition)
		}
		definition := candidate.Definition()
		if err := validateDefinition(definition); err != nil {
			return Exports{}, nil, fmt.Errorf("tools[%d]: %w", i, err)
		}
		if _, duplicate := entries[definition.Name]; duplicate {
			return Exports{}, nil, fmt.Errorf("duplicate tool %q: %w", definition.Name, ErrInvalidDefinition)
		}
		schema, err := compileSchema(definition.Name, definition.InputSchema)
		if err != nil {
			return Exports{}, nil, fmt.Errorf("tool %q schema: %w: %w", definition.Name, ErrInvalidDefinition, err)
		}
		snapshot := tool.Definition{
			Name:        definition.Name,
			Description: definition.Description,
			InputSchema: append(json.RawMessage(nil), definition.InputSchema...),
		}
		definitions = append(definitions, snapshot)
		entries[definition.Name] = registeredTool{tool: candidate, schema: schema}
	}
	interceptors := make([]tool.Interceptor, len(deps.Interceptors))
	for i, interceptor := range deps.Interceptors {
		if isNil(interceptor) {
			return Exports{}, nil, fmt.Errorf("interceptors[%d] is nil: %w", i, ErrInvalidConfig)
		}
		interceptors[i] = interceptor
	}
	return Exports{
		Runtime: &runtime{
			definitions:  definitions,
			entries:      entries,
			interceptors: interceptors,
			maxArguments: maxArguments,
			maxResult:    maxResult,
		},
	}, nil, nil
}

func validateDefinition(definition tool.Definition) error {
	if !toolNamePattern.MatchString(definition.Name) {
		return fmt.Errorf("name %q does not match required pattern: %w", definition.Name, ErrInvalidDefinition)
	}
	if definition.Description == "" || !utf8.ValidString(definition.Description) {
		return fmt.Errorf("description must be non-empty UTF-8: %w", ErrInvalidDefinition)
	}
	if len(bytes.TrimSpace(definition.InputSchema)) == 0 || !json.Valid(definition.InputSchema) {
		return fmt.Errorf("input schema must be non-empty valid JSON: %w", ErrInvalidDefinition)
	}
	var schemaObject map[string]any
	if err := json.Unmarshal(definition.InputSchema, &schemaObject); err != nil || schemaObject == nil {
		return fmt.Errorf("input schema must be a JSON object: %w", ErrInvalidDefinition)
	}
	if declared, ok := schemaObject["$schema"].(string); ok &&
		declared != "https://json-schema.org/draft/2020-12/schema" &&
		declared != "http://json-schema.org/draft/2020-12/schema" {
		return fmt.Errorf("input schema must use Draft 2020-12: %w", ErrInvalidDefinition)
	}
	return nil
}

func compileSchema(name string, raw json.RawMessage) (*jsonschema.Schema, error) {
	var document map[string]any
	if err := json.Unmarshal(raw, &document); err != nil {
		return nil, err
	}
	compiler := jsonschema.NewCompiler()
	compiler.DefaultDraft(jsonschema.Draft2020)
	location := "urn:ingot:tool-schema:" + name
	if err := compiler.AddResource(location, document); err != nil {
		return nil, err
	}
	return compiler.Compile(location)
}

func (r *runtime) Definitions() []tool.Definition {
	result := make([]tool.Definition, len(r.definitions))
	for i, definition := range r.definitions {
		result[i] = tool.Definition{
			Name:        definition.Name,
			Description: definition.Description,
			InputSchema: append(json.RawMessage(nil), definition.InputSchema...),
		}
	}
	return result
}

func (r *runtime) Call(ctx context.Context, call tool.Call) (tool.Result, error) {
	if ctx == nil {
		return tool.Result{}, fmt.Errorf("tool runtime: nil context")
	}
	if err := ctx.Err(); err != nil {
		return tool.Result{}, err
	}
	if len(call.Arguments) > r.maxArguments {
		return tool.Result{}, fmt.Errorf("tool %q arguments exceed limit: %w", call.Name, tool.ErrInvalidArguments)
	}
	if !json.Valid(call.Arguments) {
		return tool.Result{}, fmt.Errorf("tool %q arguments are not valid JSON: %w", call.Name, tool.ErrInvalidArguments)
	}
	entry, ok := r.entries[call.Name]
	if !ok {
		return tool.Result{}, fmt.Errorf("tool %q: %w", call.Name, tool.ErrNotFound)
	}
	request := tool.Call{
		ID:        call.ID,
		Name:      call.Name,
		Arguments: append(json.RawMessage(nil), call.Arguments...),
	}
	var value any
	decoder := json.NewDecoder(bytes.NewReader(request.Arguments))
	decoder.UseNumber()
	if err := decoder.Decode(&value); err != nil {
		return tool.Result{}, fmt.Errorf("tool %q arguments: %w: %w", call.Name, tool.ErrInvalidArguments, err)
	}
	if err := entry.schema.Validate(value); err != nil {
		return tool.Result{}, fmt.Errorf("tool %q arguments do not satisfy schema: %w: %w", call.Name, tool.ErrInvalidArguments, err)
	}
	original := cloneCall(request)
	terminal := func(invokeCtx context.Context, selected tool.Call) (tool.Result, error) {
		if !sameCall(selected, original) {
			return tool.Result{}, fmt.Errorf("tool %q: %w", original.Name, ErrCallMutation)
		}
		if err := validateCallArguments(selected.Name, entry.schema, selected.Arguments); err != nil {
			return tool.Result{}, err
		}
		return entry.tool.Invoke(invokeCtx, cloneCall(selected))
	}
	next := pipeline.Compose[tool.Call, tool.Result](terminal, r.interceptors...)
	result, err := next(ctx, request)
	if err != nil {
		return tool.Result{}, err
	}
	if !sameCall(request, original) {
		return tool.Result{}, fmt.Errorf("tool %q: %w", original.Name, ErrCallMutation)
	}
	if !utf8.ValidString(result.Content) {
		return tool.Result{}, fmt.Errorf("tool %q returned invalid UTF-8: %w", call.Name, ErrInvalidResult)
	}
	if len([]byte(result.Content)) > r.maxResult {
		return tool.Result{}, fmt.Errorf("tool %q result exceeds limit: %w", call.Name, ErrInvalidResult)
	}
	return result, nil
}

func validateCallArguments(name string, schema *jsonschema.Schema, raw json.RawMessage) error {
	if !json.Valid(raw) {
		return fmt.Errorf("tool %q arguments are not valid JSON: %w", name, tool.ErrInvalidArguments)
	}
	var value any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&value); err != nil {
		return fmt.Errorf("tool %q arguments: %w: %w", name, tool.ErrInvalidArguments, err)
	}
	if err := schema.Validate(value); err != nil {
		return fmt.Errorf("tool %q arguments do not satisfy schema: %w: %w", name, tool.ErrInvalidArguments, err)
	}
	return nil
}

func cloneCall(call tool.Call) tool.Call {
	call.Arguments = append(json.RawMessage(nil), call.Arguments...)
	return call
}

func sameCall(left, right tool.Call) bool {
	return left.ID == right.ID && left.Name == right.Name && bytes.Equal(left.Arguments, right.Arguments)
}
func isNil(value any) bool {
	if value == nil {
		return true
	}
	v := reflect.ValueOf(value)
	switch v.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return v.IsNil()
	}
	return false
}

var _ tool.Runtime = (*runtime)(nil)
