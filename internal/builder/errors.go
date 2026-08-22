// Package builder resolves ingot plugin declarations into a statically wired
// Go runtime image.
package builder

import "fmt"

// Error is a stable, machine-readable validation or build diagnostic.
type Error struct {
	Code   string
	Path   string
	Field  string
	Plugin string
	Actual string
	Want   string
	Err    error
}

func (e *Error) Error() string {
	message := e.Code
	if e.Path != "" {
		message += ": " + e.Path
	}
	if e.Field != "" {
		message += ": " + e.Field
	}
	if e.Plugin != "" {
		message += " (plugin " + e.Plugin + ")"
	}
	if e.Want != "" && e.Actual != "" {
		message += fmt.Sprintf(": expected %s, got %s", e.Want, e.Actual)
	} else if e.Want != "" {
		message += ": expected " + e.Want
	} else if e.Actual != "" {
		message += ": got " + e.Actual
	}
	if e.Err != nil {
		message += ": " + e.Err.Error()
	}
	return message
}

func (e *Error) Unwrap() error { return e.Err }

func diagnostic(code, path, field string, err error) error {
	return &Error{Code: code, Path: path, Field: field, Err: err}
}
