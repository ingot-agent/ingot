package toolshell

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestUTF8DecoderPreservesTextAcrossChunks(t *testing.T) {
	decoder := newUTF8Decoder()
	if got := decoder.Feed([]byte{0xE4, 0xB8}); len(got) != 0 {
		t.Fatalf("incomplete UTF-8 chunk = %q", got)
	}
	got := append(decoder.Feed([]byte{0xAD}), decoder.Flush()...)
	if string(got) != "中" {
		t.Fatalf("decoded text = %q, want 中", got)
	}
}

func TestUTF8DecoderReplacesInvalidBytes(t *testing.T) {
	decoder := newUTF8Decoder()
	got := append(decoder.Feed([]byte{'a', 0xFF, 'b', 0xE4, 0xB8}), decoder.Flush()...)
	if string(got) != "a�b�" {
		t.Fatalf("invalid UTF-8 normalization = %q", got)
	}
	if !utf8.Valid(got) {
		t.Fatalf("normalized output is not valid UTF-8: %x", got)
	}
}

func TestOutputWriterFlushesPendingText(t *testing.T) {
	collector := newOutputCollector(32)
	writer := newOutputWriter(nil, collector, nil, "stdout", false)
	if _, err := writer.Write([]byte{0xE4, 0xB8}); err != nil {
		t.Fatal(err)
	}
	if got := collector.format(0); strings.Contains(got, "中") {
		t.Fatalf("pending text emitted before flush: %q", got)
	}
	writer.Flush()
	if got := collector.format(0); !strings.Contains(got, "�") {
		t.Fatalf("flush did not replace incomplete text: %q", got)
	}
}
