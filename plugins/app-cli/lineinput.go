package appcli

import "context"

// LineInput is the line-oriented terminal input primitive used by the app.cli
// CLI loop.
//
// It is a frontend-local capability and deliberately not part of
// sdk interaction.Channel: Channel is the plugin-facing semantic interaction
// surface (Ask/Render), while reading raw lines is a terminal transport detail
// that non-line-oriented frontends (GUI, Web, IDE) do not implement.
type LineInput interface {
	ReadLine(context.Context, string) (string, error)
}
