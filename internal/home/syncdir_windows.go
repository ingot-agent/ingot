//go:build windows

package home

// Directory handles cannot be flushed with File.Sync on Windows.
func syncDirectory(string) error { return nil }
