package builder

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strconv"
)

// canonicalJSON implements the RFC 8785 subset used by the v1 schemas. The
// schemas contain strings, booleans, integral numbers, arrays, and objects.
func canonicalJSON(value any) ([]byte, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var generic any
	if err := decoder.Decode(&generic); err != nil {
		return nil, err
	}
	var out bytes.Buffer
	if err := writeCanonicalJSON(&out, generic); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

func writeCanonicalJSON(out *bytes.Buffer, value any) error {
	switch value := value.(type) {
	case nil:
		out.WriteString("null")
	case bool:
		out.WriteString(strconv.FormatBool(value))
	case string:
		writeCanonicalString(out, value)
	case json.Number:
		if _, err := strconv.ParseInt(string(value), 10, 64); err != nil {
			return fmt.Errorf("canonical JSON only supports integral schema numbers: %q", value)
		}
		out.WriteString(string(value))
	case []any:
		out.WriteByte('[')
		for i, item := range value {
			if i > 0 {
				out.WriteByte(',')
			}
			if err := writeCanonicalJSON(out, item); err != nil {
				return err
			}
		}
		out.WriteByte(']')
	case map[string]any:
		keys := make([]string, 0, len(value))
		for key := range value {
			keys = append(keys, key)
		}
		sort.Slice(keys, func(i, j int) bool {
			// All v1 schema keys are ASCII, for which byte and UTF-16 order are
			// identical. Values never participate in object member ordering.
			return keys[i] < keys[j]
		})
		out.WriteByte('{')
		for i, key := range keys {
			if i > 0 {
				out.WriteByte(',')
			}
			writeCanonicalString(out, key)
			out.WriteByte(':')
			if err := writeCanonicalJSON(out, value[key]); err != nil {
				return err
			}
		}
		out.WriteByte('}')
	default:
		return fmt.Errorf("unsupported canonical JSON value %s", reflect.TypeOf(value))
	}
	return nil
}

func writeCanonicalString(out *bytes.Buffer, value string) {
	out.WriteByte('"')
	for _, character := range value {
		switch character {
		case '"':
			out.WriteString(`\"`)
		case '\\':
			out.WriteString(`\\`)
		case '\b':
			out.WriteString(`\b`)
		case '\t':
			out.WriteString(`\t`)
		case '\n':
			out.WriteString(`\n`)
		case '\f':
			out.WriteString(`\f`)
		case '\r':
			out.WriteString(`\r`)
		default:
			if character < 0x20 {
				_, _ = fmt.Fprintf(out, `\u%04x`, character)
			} else {
				out.WriteRune(character)
			}
		}
	}
	out.WriteByte('"')
}

func digestBytes(data []byte) string {
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func sortedUnique(values []string) ([]string, error) {
	copyOf := append([]string(nil), values...)
	sort.Strings(copyOf)
	result := copyOf[:0]
	for _, value := range copyOf {
		if len(result) == 0 || result[len(result)-1] != value {
			result = append(result, value)
		}
	}
	return result, nil
}
