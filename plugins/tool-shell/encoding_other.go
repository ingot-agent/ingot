//go:build !windows

package toolshell

func newTextDecoder() textDecoder {
	return newUTF8Decoder()
}
