//go:build !windows

package home

import "os"

func atomicReplace(source, destination string) error { return os.Rename(source, destination) }
