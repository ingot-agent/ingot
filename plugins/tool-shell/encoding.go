package toolshell

import "unicode/utf8"

type textDecoder interface {
	Feed([]byte) []byte
	Flush() []byte
}

type utf8TextDecoder struct {
	pending []byte
}

func newUTF8Decoder() *utf8TextDecoder {
	return &utf8TextDecoder{}
}

func (d *utf8TextDecoder) Feed(p []byte) []byte {
	if len(p) == 0 && len(d.pending) == 0 {
		return nil
	}
	data := make([]byte, 0, len(d.pending)+len(p))
	data = append(data, d.pending...)
	data = append(data, p...)
	d.pending = nil
	output, pending := normalizeUTF8(data, false)
	d.pending = pending
	return output
}

func (d *utf8TextDecoder) Flush() []byte {
	if len(d.pending) == 0 {
		return nil
	}
	output, _ := normalizeUTF8(d.pending, true)
	d.pending = nil
	return output
}

func normalizeUTF8(data []byte, final bool) (output, pending []byte) {
	for len(data) > 0 {
		runeValue, size := utf8.DecodeRune(data)
		if runeValue == utf8.RuneError && size == 1 {
			if !utf8.FullRune(data) && !final {
				return output, append([]byte(nil), data...)
			}
			output = append(output, string(utf8.RuneError)...)
			if !utf8.FullRune(data) {
				return output, nil
			}
			data = data[1:]
			continue
		}
		output = append(output, data[:size]...)
		data = data[size:]
	}
	return output, pending
}

type utf8ProbeResult uint8

const (
	utf8ProbeComplete utf8ProbeResult = iota
	utf8ProbeIncomplete
	utf8ProbeInvalid
)

func probeUTF8(data []byte) utf8ProbeResult {
	for len(data) > 0 {
		runeValue, size := utf8.DecodeRune(data)
		if runeValue == utf8.RuneError && size == 1 {
			if !utf8.FullRune(data) {
				return utf8ProbeIncomplete
			}
			return utf8ProbeInvalid
		}
		data = data[size:]
	}
	return utf8ProbeComplete
}
