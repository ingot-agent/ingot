// Package prompts contains official prompts shipped with ingot.
package prompts

import (
	_ "embed"
	"strings"
)

//go:embed coding-agent.md
var codingAgent string

// CodingAgent returns the canonical official coding-agent system prompt.
func CodingAgent() string {
	return strings.TrimSpace(codingAgent)
}
