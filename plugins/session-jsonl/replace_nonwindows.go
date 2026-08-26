//go:build !windows

package sessionjsonl

import "os"

func atomicReplace(source, destination string) error {
	return os.Rename(source, destination)
}
