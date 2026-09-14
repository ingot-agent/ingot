package builder

import (
	"sort"

	"github.com/ingot-agent/ingot/internal/canonicaljson"
)

// canonicalJSON implements the RFC 8785 subset used by the v1 schemas. The
// schemas contain strings, booleans, integral numbers, arrays, and objects.
func canonicalJSON(value any) ([]byte, error) {
	return canonicaljson.Marshal(value)
}

func digestBytes(data []byte) string {
	return canonicaljson.Digest(data)
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
