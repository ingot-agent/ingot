// Package appcli defines the shared root configuration for the app.cli
// composite plugin.
package appcli

import "errors"

var (
	// ErrInputLimit indicates that one complete terminal line exceeded its configured bound.
	ErrInputLimit = errors.New("terminal input limit exceeded")
	// ErrInvalidInput indicates invalid UTF-8 terminal input.
	ErrInvalidInput = errors.New("invalid terminal input")
)

// Config is shared by both app.cli components.
type Config struct {
	Interaction InteractionConfig `toml:"interaction"`
	App         AppConfig         `toml:"app"`
}

// InteractionConfig controls terminal input and rendering.
type InteractionConfig struct {
	InputPrompt  string `toml:"input_prompt"`
	AskPrompt    string `toml:"ask_prompt"`
	Color        string `toml:"color"`
	MaxLineBytes int    `toml:"max_line_bytes"`
}

// AppConfig controls the session-aware CLI loop.
type AppConfig struct {
	InitialSessionTitle string `toml:"initial_session_title"`
	ShowBanner          bool   `toml:"show_banner"`
}
