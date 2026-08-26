//go:build windows

package builder

// Windows does not support flushing a directory handle. File contents are
// synced before rename and the home package uses a write-through replacement
// for pointer files.
func syncDirectory(string) error { return nil }
