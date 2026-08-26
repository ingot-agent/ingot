package appcomponent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/ingot-agent/sdk/model"
)

const (
	automaticTitleTimeout   = 10 * time.Second
	automaticTitleMaxRunes  = 32
	automaticTitleMaxTokens = 1024
	temporaryTitleMaxRunes  = 48
	manualTitleMaxRunes     = 80
	titleInputMaxBytes      = 4 * 1024
)

const titleSystemPrompt = `Generate a short title that makes this conversation easy to find later.
Return only the title: one line, no quotation marks, Markdown, sentence-ending punctuation, or "Title:" prefix.
Use the conversation's language. Prefer 4-24 Chinese characters or at most 8 concise words.`

type titleConversation struct {
	User      string `json:"user"`
	Assistant string `json:"assistant"`
}

func temporarySessionTitle(input, fallback string) string {
	title := normalizeTitleText(input)
	if title == "" {
		title = normalizeTitleText(fallback)
	}
	if title == "" {
		title = "New Session"
	}
	return truncateRunes(title, temporaryTitleMaxRunes)
}

func manualSessionTitle(input string) (string, error) {
	if !utf8.ValidString(input) {
		return "", errors.New("session title must be valid UTF-8")
	}
	title := normalizeTitleText(input)
	if title == "" {
		return "", errors.New("session title must not be empty")
	}
	if utf8.RuneCountInString(title) > manualTitleMaxRunes {
		return "", fmt.Errorf("session title exceeds %d characters", manualTitleMaxRunes)
	}
	return title, nil
}

func (a *application) generateAndRenameTitle(ctx context.Context, user, assistant string) {
	title, err := a.generateTitle(ctx, user, assistant)
	if err != nil {
		return
	}
	_ = a.store.Rename(ctx, a.current, title)
}

func (a *application) generateTitle(ctx context.Context, user, assistant string) (string, error) {
	conversation, err := json.Marshal(titleConversation{
		User:      truncateUTF8Bytes(user, titleInputMaxBytes),
		Assistant: truncateUTF8Bytes(assistant, titleInputMaxBytes),
	})
	if err != nil {
		return "", err
	}
	temperature, maxTokens := 0.2, automaticTitleMaxTokens
	titleCtx, cancel := context.WithTimeout(ctx, automaticTitleTimeout)
	defer cancel()
	response, err := a.model.Complete(titleCtx, model.Request{
		Provider: a.titleProvider,
		Model:    a.titleModel,
		Messages: []model.Message{
			{Role: model.RoleSystem, Content: titleSystemPrompt},
			{Role: model.RoleUser, Content: string(conversation)},
		},
		Temperature: &temperature,
		MaxTokens:   &maxTokens,
	})
	if err != nil {
		return "", err
	}
	if response.Message.Role != model.RoleAssistant || len(response.Message.ToolCalls) != 0 {
		return "", errors.New("title model returned an invalid message")
	}
	return generatedSessionTitle(response.Message.Content)
}

func generatedSessionTitle(input string) (string, error) {
	if !utf8.ValidString(input) || strings.ContainsAny(input, "\r\n") {
		return "", errors.New("generated title must be one line of valid UTF-8")
	}
	title := strings.TrimSpace(input)
	title = strings.TrimSpace(strings.Trim(title, "`#*_'\"“”‘’"))
	title = strings.TrimPrefix(title, "标题：")
	title = strings.TrimPrefix(title, "标题:")
	title = strings.TrimPrefix(title, "Title:")
	title = strings.TrimSpace(strings.Trim(title, "`#*_'\"“”‘’"))
	title = strings.TrimRightFunc(title, func(r rune) bool {
		return strings.ContainsRune("。.!！?？;；", r)
	})
	title = normalizeTitleText(title)
	if title == "" {
		return "", errors.New("generated title is empty")
	}
	if utf8.RuneCountInString(title) > automaticTitleMaxRunes {
		return "", fmt.Errorf("generated title exceeds %d characters", automaticTitleMaxRunes)
	}
	return title, nil
}

func normalizeTitleText(input string) string {
	input = strings.TrimSpace(input)
	input = strings.TrimLeftFunc(input, func(r rune) bool {
		return unicode.IsSpace(r) || strings.ContainsRune("#>*-", r)
	})
	return strings.Join(strings.Fields(input), " ")
}

func truncateRunes(input string, maximum int) string {
	runes := []rune(input)
	if len(runes) <= maximum {
		return input
	}
	return string(runes[:maximum-1]) + "…"
}

func truncateUTF8Bytes(input string, maximum int) string {
	if len(input) <= maximum {
		return input
	}
	end := maximum
	for end > 0 && !utf8.RuneStart(input[end]) {
		end--
	}
	return input[:end]
}
