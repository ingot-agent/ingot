package builder

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func writeTestFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestDesiredCanonicalProjectionAndStrictSchema(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	file := filepath.Join(directory, "plugins.toml")
	writeTestFile(t, file, `
# formatting does not enter semantic identity
plugins_version = 1

[[plugins]]
module = "github.com/example/remote"
version = "v1.2.3"

[[plugins]]
module = "github.com/example/local"
path = "plugins/../local"
`)
	desired, err := ParseDesired(file)
	if err != nil {
		t.Fatal(err)
	}
	canonical, err := desired.CanonicalJSON()
	if err != nil {
		t.Fatal(err)
	}
	want := `{"plugins":[{"module":"github.com/example/remote","source":{"kind":"module","version":"v1.2.3"}},{"module":"github.com/example/local","source":{"kind":"path","path":"local"}}],"schema_version":1}`
	if string(canonical) != want {
		t.Fatalf("canonical JSON\n got: %s\nwant: %s", canonical, want)
	}
	firstDigest, _ := desired.Digest()
	writeTestFile(t, file, `plugins_version=1
[[plugins]]
version='v1.2.3'
module='github.com/example/remote'
[[plugins]]
path='local'
module='github.com/example/local'
`)
	second, err := ParseDesired(file)
	if err != nil {
		t.Fatal(err)
	}
	secondDigest, _ := second.Digest()
	if firstDigest != secondDigest {
		t.Fatalf("formatting changed digest: %s != %s", firstDigest, secondDigest)
	}
	writeTestFile(t, file, "plugins_version=1\nunknown=true\n[[plugins]]\nmodule='github.com/example/a'\nversion='v1.0.0'\n")
	if _, err := ParseDesired(file); err == nil {
		t.Fatal("unknown field was accepted")
	}
}

func TestManifestProjectionAndCompatibility(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	file := filepath.Join(directory, "ingot.plugin.toml")
	writeTestFile(t, file, `
manifest_version = 1
name = "app.cli"
ingot = "<0.4.0 >=0.3.0 >=0.3.0"
config_package = "."

[[components]]
name = "interaction"
package = "./interaction"

[[components]]
name = "app"
package = "./app"

[meta]
description = "does not enter the projection"
`)
	manifest, err := ParseManifest(file)
	if err != nil {
		t.Fatal(err)
	}
	if manifest.Ingot != ">=0.3.0 <0.4.0" {
		t.Fatalf("range = %q", manifest.Ingot)
	}
	rangeValue, _ := ParseVersionRange(manifest.Ingot)
	if !rangeValue.Contains("0.3.9") || rangeValue.Contains("0.4.0") {
		t.Fatal("compatibility evaluation is wrong")
	}
	canonical, err := manifest.CanonicalJSON()
	if err != nil {
		t.Fatal(err)
	}
	want := `{"components":[{"name":"interaction","package":"./interaction"},{"name":"app","package":"./app"}],"config_package":".","ingot":">=0.3.0 <0.4.0","manifest_version":1,"name":"app.cli","schema_version":1,"state":{"min_reader_version":0,"present":false,"schema_version":0}}`
	if string(canonical) != want {
		t.Fatalf("canonical JSON\n got: %s\nwant: %s", canonical, want)
	}
}

func TestDevSourceDigestAndSyntheticVersion(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	writeTestFile(t, filepath.Join(directory, "go.mod"), "module github.com/example/local/v2\n\ngo 1.24.0\n")
	writeTestFile(t, filepath.Join(directory, "ingot.plugin.toml"), "manifest_version=1\nname='local'\ningot='0.3.0'\nconfig_package='.'\n[[components]]\nname='default'\npackage='.'\n")
	writeTestFile(t, filepath.Join(directory, "source.go"), "package local\n")
	writeTestFile(t, filepath.Join(directory, ".git", "ignored"), "one")
	first, err := DevSourceDigest(directory)
	if err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, filepath.Join(directory, ".git", "ignored"), "two")
	second, err := DevSourceDigest(directory)
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatal("VCS metadata entered source digest")
	}
	if runtime.GOOS != "windows" {
		link := filepath.Join(t.TempDir(), "source-link")
		if err := os.Symlink(directory, link); err != nil {
			t.Fatal(err)
		}
		linked, err := DevSourceDigest(link)
		if err != nil {
			t.Fatal(err)
		}
		if linked != first {
			t.Fatal("module-root symlink changed source digest")
		}
	}
	writeTestFile(t, filepath.Join(directory, "source.go"), "package local\n// changed\n")
	third, err := DevSourceDigest(directory)
	if err != nil {
		t.Fatal(err)
	}
	if first == third {
		t.Fatal("source change did not update digest")
	}
	version, err := SyntheticVersion("github.com/example/local/v2")
	if err != nil || version != "v2.0.0" {
		t.Fatalf("synthetic version = %q, %v", version, err)
	}
	version, err = SyntheticVersion("github.com/example/local")
	if err != nil || version != "v0.0.0" {
		t.Fatalf("synthetic version = %q, %v", version, err)
	}
}

func TestLockRequiresMaterializedFalseFields(t *testing.T) {
	t.Parallel()
	file := filepath.Join(t.TempDir(), "plugins.lock")
	writeTestFile(t, file, strings.TrimSpace(`
lock_version=3
plugins_digest="sha256:0000000000000000000000000000000000000000000000000000000000000000"
ingot_version="0.3.0"
builder_version="0.3.0"
replacements=[]
plugins=[]
modules=[]
[runtime]
module_path="github.com/ingot-agent/ingot-abi"
version="v0.1.0"
sum="h1:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="
[toolchain]
version="go1.26.2"
[target]
goos="linux"
goarch="amd64"
goexperiment=[]
tuning=[{key="GOAMD64",value="v1"}]
[environment]
gowork="off"
gotoolchain="local"
goproxy="off"
mod="readonly"
[build]
trimpath=true
buildvcs=false
tags=[]
ldflags=[]
gcflags=[]
asmflags=[]
`)+"\n")
	_, err := ParseLock(file)
	if err == nil || !strings.Contains(err.Error(), "target.cgo_enabled") {
		t.Fatalf("missing cgo field error = %v", err)
	}
}

func TestImageIDUsesContentIdentityAndDirectOrder(t *testing.T) {
	t.Parallel()
	first := fixtureGraphLock("/machine-one/provider-a", "/machine-one/provider-b", "/machine-one/consumer")
	second := fixtureGraphLock("/machine-two/provider-a", "/machine-two/provider-b", "/machine-two/consumer")
	firstID, err := first.ImageID()
	if err != nil {
		t.Fatal(err)
	}
	secondID, err := second.ImageID()
	if err != nil {
		t.Fatal(err)
	}
	if firstID != secondID {
		t.Fatalf("machine dev path entered ImageID: %s != %s", firstID, secondID)
	}
	second.Plugins[0], second.Plugins[1] = second.Plugins[1], second.Plugins[0]
	reorderedID, err := second.ImageID()
	if err != nil {
		t.Fatal(err)
	}
	if reorderedID == firstID {
		t.Fatal("direct Plugin order did not enter ImageID")
	}
	lockData, err := first.MarshalTOML()
	if err != nil {
		t.Fatal(err)
	}
	lockPath := filepath.Join(t.TempDir(), "plugins.lock")
	if err := os.WriteFile(lockPath, lockData, 0o600); err != nil {
		t.Fatal(err)
	}
	roundTrip, err := ParseLock(lockPath)
	if err != nil {
		t.Fatalf("written lock does not strict-parse: %v\n%s", err, lockData)
	}
	roundTripID, _ := roundTrip.ImageID()
	if roundTripID != firstID {
		t.Fatalf("lock round trip changed ImageID: %s != %s", roundTripID, firstID)
	}
}

func TestImageIDIncludesRuntimeABI(t *testing.T) {
	t.Parallel()
	first := fixtureGraphLock("/machine/provider-a", "/machine/provider-b", "/machine/consumer")
	first.Modules[1] = LockedModule{Path: RuntimeSupportTOMLModule, Version: RuntimeSupportTOMLVersion, Sum: "h1:QkJCQkJCQkJCQkJCQkJCQkJCQkJCQkJCQkJCQkJCQkI=", GoModSum: "h1:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="}
	second := *first
	second.Modules = append([]LockedModule(nil), first.Modules...)
	second.Modules[1] = LockedModule{Path: RuntimeSupportTOMLModule, Version: RuntimeSupportTOMLVersion, Sum: "h1:Q0NDQ0NDQ0NDQ0NDQ0NDQ0NDQ0NDQ0NDQ0NDQ0NDQ0M=", GoModSum: "h1:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="}
	firstID, err := first.ImageID()
	if err != nil {
		t.Fatal(err)
	}
	secondID, err := second.ImageID()
	if err != nil {
		t.Fatal(err)
	}
	if firstID == secondID {
		t.Fatal("Runtime support module identity did not enter ImageID")
	}
}

func TestLockAcceptsMVSUpgradeOfGeneratedConfigDecoder(t *testing.T) {
	t.Parallel()
	lock := fixtureGraphLock("/machine/provider-a", "/machine/provider-b", "/machine/consumer")
	lock.Modules = append([]LockedModule(nil), lock.Modules...)
	lock.Modules[1].Version = "v2.2.5"
	if err := lock.Validate(); err != nil {
		t.Fatalf("newer generated config decoder was rejected: %v", err)
	}
	root := t.TempDir()
	if err := lock.RestoreRootModule(root, nil); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(root, "go.mod"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), RuntimeSupportTOMLModule+" v2.2.5") {
		t.Fatalf("restored go.mod did not retain the MVS-selected decoder:\n%s", data)
	}
}

func TestImageIDIncludesRuntimeSum(t *testing.T) {
	t.Parallel()
	first := fixtureGraphLock("/machine/provider-a", "/machine/provider-b", "/machine/consumer")
	second := *first
	second.Runtime.Sum = "h1:QkJCQkJCQkJCQkJCQkJCQkJCQkJCQkJCQkJCQkJCQkI="
	second.Modules = append([]LockedModule(nil), first.Modules...)
	second.Modules[0] = LockedModule{Path: IngotABIModulePath, Version: IngotABIVersion, Sum: "h1:QkJCQkJCQkJCQkJCQkJCQkJCQkJCQkJCQkJCQkJCQkI=", GoModSum: "h1:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="}
	firstID, err := first.ImageID()
	if err != nil {
		t.Fatal(err)
	}
	secondID, err := second.ImageID()
	if err != nil {
		t.Fatal(err)
	}
	if firstID == secondID {
		t.Fatal("Runtime ABI sum did not enter ImageID")
	}
}

func TestLockRejectsUnpinnedRuntimeABI(t *testing.T) {
	t.Parallel()
	lock := fixtureGraphLock("/machine/provider-a", "/machine/provider-b", "/machine/consumer")
	lock.Runtime.Version = "v0.2.0"
	if _, err := lock.MarshalTOML(); err == nil || !strings.Contains(err.Error(), "INGOT-LOCK-RUNTIME-VERSION") {
		t.Fatalf("unpinned Runtime version error = %v", err)
	}
}
