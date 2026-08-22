package builder

import (
	"bytes"
	"fmt"
	"os"

	"github.com/pelletier/go-toml/v2"
)

func decodeExactFile(path string, target any, code string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return diagnostic(code, path, "", err)
	}
	decoder := toml.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return diagnostic(code, path, "", err)
	}
	return nil
}

func writeTOML(value any) ([]byte, error) {
	var out bytes.Buffer
	encoder := toml.NewEncoder(&out)
	if err := encoder.Encode(value); err != nil {
		return nil, fmt.Errorf("encode TOML: %w", err)
	}
	return out.Bytes(), nil
}
