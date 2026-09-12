package home

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/ingot-agent/ingot/internal/builder"
	"github.com/ingot-agent/ingot/internal/image"
	"github.com/ingot-agent/ingot/internal/layout"
)

func newM2Home(t *testing.T) *Home {
	t.Helper()
	root := t.TempDir()
	home, err := OpenForInit(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := home.initializeSchema(); err != nil {
		t.Fatal(err)
	}
	home, err = Open(root)
	if err != nil {
		t.Fatal(err)
	}
	return home
}

func TestOpenRejectsUninitializedAndLegacyHome(t *testing.T) {
	if _, err := Open(t.TempDir()); err == nil || !strings.Contains(err.Error(), "INGOT-HOME-SCHEMA-MISSING") {
		t.Fatalf("empty home error = %v", err)
	}
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "current"), []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenForInit(root); err == nil {
		t.Fatal("legacy non-empty home was accepted")
	}
}

func TestSupervisorOpenDoesNotAcquireHomeWriterLock(t *testing.T) {
	home := newM2Home(t)
	release, err := home.acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if _, err := OpenForSupervisor(home.Root); err != nil {
		t.Fatalf("supervisor open while writer lock held: %v", err)
	}
}

func TestTagRuntimeBindingRollbackAndGC(t *testing.T) {
	home := newM2Home(t)
	first := writeM2ImageFixture(t, home, "first")
	second := writeM2ImageFixture(t, home, "second")
	third := writeM2ImageFixture(t, home, "third")
	if _, err := home.ImageTag(context.Background(), first, "acme/app:stable"); err != nil {
		t.Fatal(err)
	}
	work, err := home.RuntimeCreate(context.Background(), "work", "acme/app:stable", []string{"serve"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := home.ImageTag(context.Background(), second, "acme/app:stable"); err != nil {
		t.Fatal(err)
	}
	if work.DesiredImage.ImageID != first {
		t.Fatalf("runtime followed moved tag: %s", work.DesiredImage.ImageID)
	}
	personal, err := home.RuntimeCreate(context.Background(), "personal", "acme/app:stable", nil)
	if err != nil {
		t.Fatal(err)
	}
	if personal.DesiredImage.ImageID != second {
		t.Fatalf("new runtime resolved %s, want %s", personal.DesiredImage.ImageID, second)
	}
	switched, err := home.RuntimeSwitch(context.Background(), "work", "acme/app:stable")
	if err != nil {
		t.Fatal(err)
	}
	if switched.DesiredImage.ImageID != second || switched.RollbackImage == nil || switched.RollbackImage.ImageID != first {
		t.Fatalf("switch = %#v", switched.Runtime)
	}
	rolled, err := home.RuntimeRollback(context.Background(), "work")
	if err != nil {
		t.Fatal(err)
	}
	if rolled.DesiredImage.ImageID != first || rolled.RollbackImage == nil || rolled.RollbackImage.ImageID != second {
		t.Fatalf("rollback = %#v", rolled.Runtime)
	}
	removed, err := home.GC(context.Background(), 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(removed) != 1 || removed[0] != third {
		t.Fatalf("removed = %#v", removed)
	}
}

func TestRuntimeStateIsolation(t *testing.T) {
	home := newM2Home(t)
	id := writeM2ImageFixture(t, home, "image")
	if _, err := home.RuntimeCreate(context.Background(), "one", id, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := home.RuntimeCreate(context.Background(), "two", id, nil); err != nil {
		t.Fatal(err)
	}
	one := filepath.Join(home.Root, "runtimes", "one", "state", "plugin", "value")
	two := filepath.Join(home.Root, "runtimes", "two", "state", "plugin", "value")
	if err := os.MkdirAll(filepath.Dir(one), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(two), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(one, []byte("one"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(two, []byte("two"), 0o600); err != nil {
		t.Fatal(err)
	}
	left, _ := os.ReadFile(one)
	right, _ := os.ReadFile(two)
	if string(left) == string(right) {
		t.Fatal("runtime state was not isolated")
	}
}

func TestForegroundRunInjectsRuntimeHomeAndRecordsExit(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX script fixture")
	}
	home := newM2Home(t)
	recorded := filepath.Join(t.TempDir(), "runtime-home")
	script := "#!/bin/sh\nprintf '%s' \"$INGOT_RUNTIME_HOME\" > " + strconvQuote(recorded) + "\n"
	id := writeM2ImageFixture(t, home, script)
	if _, err := home.RuntimeCreate(context.Background(), "work", id, nil); err != nil {
		t.Fatal(err)
	}
	code, err := home.RuntimeRun(context.Background(), "work", nil, nil, &bytes.Buffer{}, &bytes.Buffer{})
	if err != nil && strings.Contains(err.Error(), "operation not permitted") {
		t.Skip("sandbox forbids loopback control listener")
	}
	if err != nil || code != 0 {
		t.Fatalf("run = %d, %v", code, err)
	}
	data, err := os.ReadFile(recorded)
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(home.Root, "runtimes", "work")
	if string(data) != want {
		t.Fatalf("runtime home = %q, want %q", data, want)
	}
	view, err := home.RuntimeInspect(context.Background(), "work")
	if err != nil {
		t.Fatal(err)
	}
	if view.State != "stopped" || view.LastExit == nil || view.LastExit.ExitCode != 0 {
		t.Fatalf("view = %#v", view)
	}
}

func TestImageBundleRoundTripDeterministic(t *testing.T) {
	home := newM2Home(t)
	id := writeM2ImageFixture(t, home, "bundle-binary")
	if _, err := home.ImageTag(context.Background(), id, "acme/app:one"); err != nil {
		t.Fatal(err)
	}
	first := filepath.Join(t.TempDir(), "first.ingot-image")
	second := filepath.Join(t.TempDir(), "second.ingot-image")
	if err := home.ImageExport(context.Background(), "acme/app:one", "", first); err != nil {
		t.Fatal(err)
	}
	if err := home.ImageExport(context.Background(), "acme/app:one", "", second); err != nil {
		t.Fatal(err)
	}
	a, _ := os.ReadFile(first)
	b, _ := os.ReadFile(second)
	if !bytes.Equal(a, b) {
		t.Fatal("bundle bytes are not deterministic")
	}
	other := newM2Home(t)
	result, err := other.ImageImport(context.Background(), first, false)
	if err != nil {
		t.Fatal(err)
	}
	if result.Binding.ImageID != id || result.Tag == nil {
		t.Fatalf("import = %#v", result)
	}
	if _, _, err := other.ResolveImage(context.Background(), "acme/app:one"); err != nil {
		t.Fatal(err)
	}
}

func TestOpenRecoversImageImportAndRuntimeDeleteTransactions(t *testing.T) {
	home := newM2Home(t)
	id := writeM2ImageFixture(t, home, "transaction-image")
	binding, _, err := home.resolveImageUnlocked(id, runtime.GOOS, runtime.GOARCH)
	if err != nil {
		t.Fatal(err)
	}
	tag := image.Source{Name: "acme/recovered", Tag: "latest"}
	binding.Source = &tag
	if _, err := home.writeHomeTransaction(homeTransaction{Kind: "image_import", ImageImport: &imageImportTransaction{Binding: binding, Tag: tag}}); err != nil {
		t.Fatal(err)
	}
	if _, err := home.RuntimeCreate(context.Background(), "discard", id, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := home.writeHomeTransaction(homeTransaction{Kind: "runtime_delete", RuntimeDelete: &runtimeDeleteTransaction{Name: "discard", Purge: true}}); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(home.Root)
	if err != nil {
		t.Fatal(err)
	}
	resolved, _, err := reopened.ResolveImage(context.Background(), "acme/recovered:latest")
	if err != nil || resolved.ImageID != id {
		t.Fatalf("recovered binding = %#v, %v", resolved, err)
	}
	if _, err := os.Stat(reopened.registry().RuntimeHome("discard")); !os.IsNotExist(err) {
		t.Fatalf("runtime delete transaction was not completed: %v", err)
	}
	entries, err := os.ReadDir(reopened.transactionDirectory())
	if err != nil || len(entries) != 0 {
		t.Fatalf("transactions after recovery = %#v, %v", entries, err)
	}
}

func TestProjectWriterLockHonorsContext(t *testing.T) {
	project := t.TempDir()
	recipe := filepath.Join(project, "plugins.toml")
	if err := os.WriteFile(recipe, []byte("plugins_version = 1\nplugins = []\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	options := RecipeOptions{CWD: project}
	paths, err := resolveProjectPaths(options)
	if err != nil {
		t.Fatal(err)
	}
	release, err := acquireProjectLock(context.Background(), projectWriterLockPath(paths))
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, blockedRelease, err := prepareProject(ctx, options); err == nil {
		blockedRelease()
		t.Fatal("second project writer acquired an already-held lock")
	} else if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("lock error = %v", err)
	}
}

func TestDesiredWriterPreservesCommentsAcrossUpdateAndReorder(t *testing.T) {
	path := filepath.Join(t.TempDir(), "plugins.toml")
	existing := "# top-level note\nplugins_version = 1\n\n[[plugins]]\n# A detail\nmodule = \"example.com/a\"\nversion = \"v1.0.0\" # A pin\n"
	if err := os.WriteFile(path, []byte(existing), 0o644); err != nil {
		t.Fatal(err)
	}
	desired := builder.NewDesired(path, []builder.DesiredPlugin{{Module: "example.com/a", Version: "v1.1.0"}})
	data, err := marshalDesiredPreservingComments(path, desired)
	if err != nil {
		t.Fatal(err)
	}
	for _, comment := range []string{"# top-level note", "# A detail", "# A pin"} {
		if !strings.Contains(string(data), comment) {
			t.Fatalf("lost %s", comment)
		}
	}
}

func writeM2ImageFixture(t *testing.T, home *Home, binary string) string {
	t.Helper()
	target := image.Target{GOOS: runtime.GOOS, GOARCH: runtime.GOARCH, CGOEnabled: false, GOExperiment: []string{}, Tuning: []image.TargetKey{}}
	buildManifestValue := map[string]any{"schema_version": 3, "target": map[string]any{"goos": target.GOOS, "goarch": target.GOARCH, "cgo_enabled": false, "goexperiment": []string{}, "tuning": map[string]string{}}}
	buildManifest, err := image.CanonicalJSON(buildManifestValue)
	if err != nil {
		t.Fatal(err)
	}
	buildManifest = bytes.Replace(buildManifest, []byte(`"schema_version":3`), []byte(`"fixture":`+strconvQuote(binary)+`,"schema_version":3`), 1)
	buildManifest, err = image.CanonicalJSON(json.RawMessage(buildManifest))
	if err != nil {
		t.Fatal(err)
	}
	id := image.Digest(buildManifest)
	directory := home.imageDirectory(id)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	binaryPath := filepath.Join(directory, layout.RuntimeExecutableName(runtime.GOOS))
	if err := os.WriteFile(binaryPath, []byte(binary), 0o755); err != nil {
		t.Fatal(err)
	}
	artifact, err := image.FileDigest(binaryPath)
	if err != nil {
		t.Fatal(err)
	}
	manifest := image.Manifest{SchemaVersion: 3, ImageID: id, ArtifactDigest: artifact, Target: target, BuildManifest: buildManifest, DirectPlugins: []string{}, ComponentCreationOrder: []string{}, ManyOrder: map[string][]string{}, HostDependencies: map[string][]string{}}
	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "manifest.json"), append(data, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
	return id
}

func strconvQuote(value string) string { data, _ := json.Marshal(value); return string(data) }
