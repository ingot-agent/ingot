package prompts

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestCodingAgentPrompt(t *testing.T) {
	prompt := CodingAgent()
	if prompt == "" {
		t.Fatal("coding agent prompt must not be empty")
	}
	if !utf8.ValidString(prompt) {
		t.Fatal("coding agent prompt must be valid UTF-8")
	}
	if len([]byte(prompt)) > 256*1024 {
		t.Fatal("coding agent prompt exceeds system prompt limit")
	}
	if prompt != strings.TrimSpace(prompt) {
		t.Fatal("coding agent prompt must not have leading or trailing whitespace")
	}
}
