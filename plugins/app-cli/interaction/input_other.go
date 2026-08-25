//go:build !unix && !windows

package interactioncomponent

import (
	"context"
	"errors"
	"io"
	"os"
)

type unsupportedInput struct{}

func newTerminalInput(*os.File, io.Writer) (inputDriver, error) {
	return nil, errors.New("cancelable terminal input is unsupported on this platform")
}

func supportsColor(*os.File) bool { return false }

func (unsupportedInput) ReadLine(context.Context, int) (string, error) { return "", io.EOF }
func (unsupportedInput) Close() error                                  { return nil }
