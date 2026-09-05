package appbackend

import (
	"encoding/json"
	"time"

	"github.com/ingot-agent/sdk/interaction"
)

// Attachment is the asset-first browser attachment DTO.
type Attachment struct {
	Kind     string `json:"kind"`
	MIMEType string `json:"mimeType,omitempty"`
	Name     string `json:"name,omitempty"`
	AssetID  string `json:"assetId"`
}

// ErrorResponse is the stable HTTP error envelope.
type ErrorResponse struct {
	Error ErrorDetail `json:"error"`
}

// ErrorDetail describes one HTTP API error.
type ErrorDetail struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// AgentCapabilities reports independently available execution modes.
type AgentCapabilities struct {
	Run    bool `json:"run"`
	Stream bool `json:"stream"`
}

// AgentState is the agent portion of the bootstrap snapshot.
type AgentState struct {
	Capabilities AgentCapabilities `json:"capabilities"`
}

// AssetState describes the optional browser asset transport.
type AssetState struct {
	Available bool  `json:"available"`
	MaxBytes  int64 `json:"maxBytes"`
}

// Session is the Web projection of session metadata.
type Session struct {
	ID         string     `json:"id"`
	Title      string     `json:"title"`
	CreatedAt  time.Time  `json:"createdAt"`
	UpdatedAt  time.Time  `json:"updatedAt"`
	ArchivedAt *time.Time `json:"archivedAt,omitempty"`
}

// TurnSnapshot is the recoverable projection of a running turn.
type TurnSnapshot struct {
	ID        string `json:"id"`
	SessionID string `json:"sessionId"`
	Revision  uint64 `json:"revision"`
	Reasoning string `json:"reasoning"`
	Output    string `json:"output"`
}

// StateSnapshot bootstraps all process-local Web state. Session history is
// intentionally fetched separately.
type StateSnapshot struct {
	Cursor               uint64                `json:"cursor"`
	Agent                AgentState            `json:"agent"`
	Assets               AssetState            `json:"assets"`
	Sessions             []Session             `json:"sessions"`
	Operations           []OperationDefinition `json:"operations"`
	Turns                []TurnSnapshot        `json:"turns"`
	OperationInvocations []OperationSnapshot   `json:"operationInvocations"`
	Interactions         []PendingInteraction  `json:"interactions"`
	InteractionStates    []InteractionState    `json:"interactionStates"`
}

// InteractionSubmission carries raw browser values for host-side validation.
type InteractionSubmission struct {
	Values map[string]json.RawMessage `json:"values"`
}

// PendingInteraction is a caller-owned pending request snapshot.
type PendingInteraction struct {
	Scope   *Scope             `json:"scope,omitempty"`
	ID      string             `json:"id"`
	Name    string             `json:"name"`
	Level   string             `json:"level,omitempty"`
	Message string             `json:"description,omitempty"`
	Fields  []InteractionField `json:"fields"`
}

// InteractionField is the browser representation of an SDK request field.
type InteractionField struct {
	Name        string              `json:"name"`
	Label       string              `json:"label,omitempty"`
	Description string              `json:"description,omitempty"`
	Kind        string              `json:"kind"`
	Required    bool                `json:"required"`
	Sensitive   bool                `json:"sensitive"`
	HasDefault  bool                `json:"hasDefault"`
	Default     any                 `json:"default,omitempty"`
	Options     []InteractionOption `json:"options,omitempty"`
}

// InteractionOption is one ordered choice.
type InteractionOption struct {
	Value       string `json:"value"`
	Label       string `json:"label,omitempty"`
	Description string `json:"description,omitempty"`
}

// InteractionState is the current replaceable state projection.
type InteractionState struct {
	Scope       *Scope                  `json:"scope,omitempty"`
	ID          string                  `json:"id"`
	Name        string                  `json:"name"`
	Level       string                  `json:"level,omitempty"`
	Description string                  `json:"description,omitempty"`
	Values      []InteractionStateEntry `json:"values"`
}

// InteractionStateEntry is one projected state value.
type InteractionStateEntry struct {
	Name        string `json:"name"`
	Label       string `json:"label,omitempty"`
	Description string `json:"description,omitempty"`
	Value       any    `json:"value"`
}

// InteractionHost implements the SDK channel and exposes Web settlement and
// authoritative snapshots.
type InteractionHost interface {
	interaction.Channel
	Pending() []PendingInteraction
	States() []InteractionState
	Respond(string, InteractionSubmission) error
	Scoped(Scope) interaction.Channel
}

// OperationDefinition is an immutable operation's public JSON contract.
type OperationDefinition struct {
	Name         string          `json:"name"`
	Description  string          `json:"description"`
	InputSchema  json.RawMessage `json:"inputSchema"`
	OutputSchema json.RawMessage `json:"outputSchema"`
}

// OperationSnapshot preserves running calls and a bounded set of final results.
type OperationSnapshot struct {
	ID        string           `json:"id"`
	Name      string           `json:"name"`
	SessionID string           `json:"sessionId,omitempty"`
	Status    string           `json:"status"`
	Result    *OperationResult `json:"result,omitempty"`
	Error     *ErrorDetail     `json:"error,omitempty"`
}

// OperationResult is the validated machine-readable output of an operation.
type OperationResult struct {
	Output json.RawMessage `json:"output"`
}

// Runtime contains the host-side process state shared with the HTTP component.
type Runtime interface {
	Events() EventHub
	Interactions() InteractionHost
}
