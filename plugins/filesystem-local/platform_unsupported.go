//go:build !unix && !windows

package filesystemlocal

import "errors"

func securePlatform() error { return errors.ErrUnsupported }
