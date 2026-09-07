//go:build windows

package toolshell

import (
	"unicode/utf16"
	"unicode/utf8"

	"golang.org/x/sys/windows"
)

type decoderMode uint8

const (
	decoderUndecided decoderMode = iota
	decoderUTF8
	decoderNative
)

const (
	mbErrInvalidChars  = 0x00000008
	nativePendingLimit = 8
)

var procGetOEMCP = windows.NewLazySystemDLL("kernel32.dll").NewProc("GetOEMCP")

type windowsTextDecoder struct {
	mode     decoderMode
	codePage uint32
	utf8     *utf8TextDecoder
	pending  []byte
}

func newTextDecoder() textDecoder {
	return newWindowsTextDecoder(windowsNativeOutputCodePage())
}

func newWindowsTextDecoder(codePage uint32) *windowsTextDecoder {
	if codePage == 0 {
		codePage = windows.GetACP()
	}
	return &windowsTextDecoder{codePage: codePage}
}

func windowsNativeOutputCodePage() uint32 {
	if codePage, err := windows.GetConsoleOutputCP(); err == nil && codePage != 0 {
		return codePage
	}
	if codePage, _, _ := procGetOEMCP.Call(); codePage != 0 {
		return uint32(codePage)
	}
	if codePage := windows.GetACP(); codePage != 0 {
		return codePage
	}
	return 1252
}

func (d *windowsTextDecoder) Feed(p []byte) []byte {
	if len(p) == 0 {
		return nil
	}
	switch d.mode {
	case decoderUTF8:
		return d.utf8.Feed(p)
	case decoderNative:
		return d.feedNative(p)
	default:
		return d.feedUndecided(p)
	}
}

func (d *windowsTextDecoder) Flush() []byte {
	switch d.mode {
	case decoderUTF8:
		return d.utf8.Flush()
	case decoderNative:
		output, _ := decodeWindowsNative(d.pending, d.codePage, true)
		d.pending = nil
		return output
	default:
		if len(d.pending) == 0 {
			return nil
		}
		switch probe := probeUTF8(d.pending); probe {
		case utf8ProbeComplete:
			d.mode = decoderUTF8
			d.utf8 = newUTF8Decoder()
			output := d.utf8.Feed(d.pending)
			d.pending = nil
			return append(output, d.utf8.Flush()...)
		case utf8ProbeIncomplete:
			output, _ := normalizeUTF8(d.pending, true)
			d.pending = nil
			return output
		default:
			d.mode = decoderNative
			output, _ := decodeWindowsNative(d.pending, d.codePage, true)
			d.pending = nil
			return output
		}
	}
}

func (d *windowsTextDecoder) feedUndecided(p []byte) []byte {
	data := make([]byte, 0, len(d.pending)+len(p))
	data = append(data, d.pending...)
	data = append(data, p...)
	d.pending = nil

	firstNonASCII := 0
	for firstNonASCII < len(data) && data[firstNonASCII] < utf8.RuneSelf {
		firstNonASCII++
	}
	if firstNonASCII == len(data) {
		return data
	}

	output := append([]byte(nil), data[:firstNonASCII]...)
	nonASCII := data[firstNonASCII:]
	switch probe := probeUTF8(nonASCII); probe {
	case utf8ProbeComplete:
		d.mode = decoderUTF8
		d.utf8 = newUTF8Decoder()
		return append(output, d.utf8.Feed(nonASCII)...)
	case utf8ProbeIncomplete:
		d.pending = append([]byte(nil), nonASCII...)
		return output
	default:
		d.mode = decoderNative
		d.pending = append([]byte(nil), nonASCII...)
		return append(output, d.feedNative(nil)...)
	}
}

func (d *windowsTextDecoder) feedNative(p []byte) []byte {
	data := make([]byte, 0, len(d.pending)+len(p))
	data = append(data, d.pending...)
	data = append(data, p...)
	d.pending = nil

	output, pending := decodeWindowsNative(data, d.codePage, false)
	d.pending = pending
	return output
}

func decodeWindowsNative(data []byte, codePage uint32, final bool) (output, pending []byte) {
	for len(data) > 0 {
		asciiEnd := 0
		for asciiEnd < len(data) && data[asciiEnd] < utf8.RuneSelf {
			asciiEnd++
		}
		if asciiEnd > 0 {
			output = append(output, data[:asciiEnd]...)
			data = data[asciiEnd:]
			continue
		}
		if len(data) == 0 {
			break
		}

		// A native Windows code-page character occupies only a bounded number of
		// bytes. Probe that bounded prefix instead of retrying every possible
		// prefix of an untrusted pipe chunk.
		probeLimit := min(len(data), nativePendingLimit)
		decoded := false
		for size := 1; size <= probeLimit; size++ {
			converted, ok := convertWindowsCodePage(data[:size], codePage, true)
			if !ok {
				continue
			}
			output = append(output, converted...)
			data = data[size:]
			decoded = true
			break
		}
		if decoded {
			continue
		}
		if !final && len(data) < nativePendingLimit {
			return output, append([]byte(nil), data...)
		}
		output = append(output, string(utf8.RuneError)...)
		data = data[1:]
	}
	return output, nil
}

func convertWindowsCodePage(data []byte, codePage uint32, strict bool) ([]byte, bool) {
	if len(data) == 0 {
		return nil, true
	}
	flags := uint32(0)
	if strict {
		flags = mbErrInvalidChars
	}
	wideLength, err := windows.MultiByteToWideChar(codePage, flags, &data[0], int32(len(data)), nil, 0)
	if err != nil || wideLength <= 0 {
		return nil, false
	}
	wide := make([]uint16, wideLength)
	converted, err := windows.MultiByteToWideChar(codePage, flags, &data[0], int32(len(data)), &wide[0], wideLength)
	if err != nil || converted != wideLength {
		return nil, false
	}
	return []byte(string(utf16.Decode(wide))), true
}
