package builder

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestBuilderConfigSupportsMultipleSDKs(t *testing.T) {
	t.Parallel()
	config, err := parseBuilderConfig([]byte(`
builder_config_version = 1

[[sdks]]
module = "example.com/sdk-one"
version = "v1.2.3"

[[sdks]]
module = "example.com/sdk-two/v2"
version = "v2.0.1"
path = "../sdk-two"
`), "builder.toml")
	if err != nil {
		t.Fatal(err)
	}
	want := []SDKConfig{
		{Module: "example.com/sdk-one", Version: "v1.2.3"},
		{Module: "example.com/sdk-two/v2", Version: "v2.0.1", Path: "../sdk-two"},
	}
	if !reflect.DeepEqual(config.SDKs, want) {
		t.Fatalf("SDKs = %#v, want %#v", config.SDKs, want)
	}
}

func TestBuilderConfigEnvironmentOverrides(t *testing.T) {
	t.Parallel()
	base := BuilderConfig{BuilderConfigVersion: 1, SDKs: []SDKConfig{
		{Module: "example.com/original", Version: "v1.0.0", Path: "/local/original"},
		{Module: "example.com/kept", Version: "v1.1.0"},
	}}
	first := base
	first.SDKs = append([]SDKConfig(nil), base.SDKs...)
	firstEnvironment := map[string]string{
		BuilderSDKModuleEnv:  "example.com/replaced",
		BuilderSDKVersionEnv: "v1.4.0",
	}
	if err := first.ApplyEnvironment(mapLookup(firstEnvironment)); err != nil {
		t.Fatal(err)
	}
	wantFirst := []SDKConfig{
		{Module: "example.com/replaced", Version: "v1.4.0", Path: "/local/original"},
		{Module: "example.com/kept", Version: "v1.1.0"},
	}
	if !reflect.DeepEqual(first.SDKs, wantFirst) {
		t.Fatalf("first SDK override = %#v, want %#v", first.SDKs, wantFirst)
	}

	list := base
	list.SDKs = append([]SDKConfig(nil), base.SDKs...)
	if err := list.ApplyEnvironment(mapLookup(map[string]string{
		BuilderSDKsEnv: "example.com/one@v1.2.3, example.com/two/v2@v2.0.1",
	})); err != nil {
		t.Fatal(err)
	}
	wantList := []SDKConfig{
		{Module: "example.com/one", Version: "v1.2.3"},
		{Module: "example.com/two/v2", Version: "v2.0.1"},
	}
	if !reflect.DeepEqual(list.SDKs, wantList) {
		t.Fatalf("list override = %#v, want %#v", list.SDKs, wantList)
	}
}

func TestBuilderConfigRejectsInvalidEnvironment(t *testing.T) {
	t.Parallel()
	base := BuilderConfig{BuilderConfigVersion: 1, SDKs: []SDKConfig{{Module: "example.com/original", Version: "v1.0.0"}}}
	tests := []map[string]string{
		{BuilderSDKsEnv: "example.com/missing-version"},
		{BuilderSDKsEnv: "example.com/one@v1.0.0", BuilderSDKVersionEnv: "v1.1.0"},
		{BuilderSDKsEnv: "example.com/duplicate@v1.0.0,example.com/duplicate@v1.1.0"},
	}
	for _, environment := range tests {
		config := base
		config.SDKs = append([]SDKConfig(nil), base.SDKs...)
		if err := config.ApplyEnvironment(mapLookup(environment)); err == nil {
			t.Fatalf("environment %#v was accepted", environment)
		}
	}
}

func TestLoadBuilderConfigUsesEnvironmentAndResolvesPath(t *testing.T) {
	for _, key := range []string{BuilderSDKsEnv, BuilderSDKModuleEnv, BuilderSDKVersionEnv} {
		value, exists := os.LookupEnv(key)
		t.Cleanup(func() {
			if exists {
				_ = os.Setenv(key, value)
			} else {
				_ = os.Unsetenv(key)
			}
		})
		if err := os.Unsetenv(key); err != nil {
			t.Fatal(err)
		}
	}
	directory := t.TempDir()
	path := filepath.Join(directory, "builder.toml")
	writeTestFile(t, path, `
builder_config_version = 1
[[sdks]]
module = "example.com/original"
version = "v1.0.0"
path = "../sdk"
`)
	config, err := LoadBuilderConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if config.SDKs[0].Path != filepath.Clean(filepath.Join(directory, "../sdk")) {
		t.Fatalf("relative SDK path = %q", config.SDKs[0].Path)
	}
	t.Setenv(BuilderSDKsEnv, "example.com/override@v1.5.0,example.com/extra@v1.0.0")
	config, err = LoadBuilderConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := config.SDKs; len(got) != 2 || got[0].Module != "example.com/override" || got[1].Module != "example.com/extra" || strings.Contains(got[0].Path, "sdk") {
		t.Fatalf("environment-loaded SDKs = %#v", got)
	}
}

func mapLookup(values map[string]string) func(string) (string, bool) {
	return func(key string) (string, bool) {
		value, ok := values[key]
		return value, ok
	}
}
