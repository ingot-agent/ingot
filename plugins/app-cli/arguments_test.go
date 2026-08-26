package appcli

import (
	"errors"
	"testing"
)

func TestParseArguments(t *testing.T) {
	tests := []struct {
		name      string
		arguments []string
		want      Mode
		wantErr   bool
	}{
		{name: "tui", arguments: []string{"chat"}, want: ModeTUI},
		{name: "plain", arguments: []string{"chat", "--plain"}, want: ModePlain},
		{name: "missing command", wantErr: true},
		{name: "unknown flag", arguments: []string{"chat", "--color"}, wantErr: true},
		{name: "extra positional", arguments: []string{"chat", "now"}, wantErr: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := ParseArguments(test.arguments)
			if got != test.want || errors.Is(err, ErrInvalidArguments) != test.wantErr {
				t.Fatalf("ParseArguments(%q)=(%v, %v), want (%v, invalid=%v)", test.arguments, got, err, test.want, test.wantErr)
			}
		})
	}
}
