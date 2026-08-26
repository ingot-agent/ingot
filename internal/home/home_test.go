package home

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/ingot-agent/ingot/internal/builder"
	"github.com/ingot-agent/ingot/internal/layout"
)

func TestCurrentSwitchRollbackAndGC(t *testing.T) {
	t.Parallel()
	home, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	first := writeImageFixture(t, home, `{"generation":1}`, "first")
	second := writeImageFixture(t, home, `{"generation":2}`, "second")
	third := writeImageFixture(t, home, `{"generation":3}`, "third")
	if err := home.switchCurrent(first); err != nil {
		t.Fatal(err)
	}
	if err := home.switchCurrent(second); err != nil {
		t.Fatal(err)
	}
	current, err := home.Current()
	if err != nil || current != second {
		t.Fatalf("current = %q, %v", current, err)
	}
	if err := home.Rollback(context.Background(), ""); err != nil {
		t.Fatal(err)
	}
	current, _ = home.Current()
	if current != first {
		t.Fatalf("rollback current = %q", current)
	}
	removed, err := home.GC(context.Background(), 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(removed) != 1 || removed[0] != third {
		t.Fatalf("GC removed %#v", removed)
	}
	if _, err := os.Stat(home.imageDirectory(first)); err != nil {
		t.Fatal("GC removed current", err)
	}
	if _, err := os.Stat(home.imageDirectory(second)); err != nil {
		t.Fatal("GC removed previous", err)
	}
}

func TestSwitchRejectsTraversalAndCorruptArtifact(t *testing.T) {
	t.Parallel()
	home, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := home.switchCurrent("sha256:../../outside"); err == nil {
		t.Fatal("path traversal image id was accepted")
	}
	imageID := writeImageFixture(t, home, `{"generation":1}`, "binary")
	if err := os.WriteFile(filepath.Join(home.imageDirectory(imageID), layout.RuntimeExecutableName(runtime.GOOS)), []byte("corrupt"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := home.switchCurrent(imageID); err == nil {
		t.Fatal("corrupt artifact was accepted")
	}
}

func TestOpenRecoversPluginPairTransaction(t *testing.T) {
	t.Parallel()
	home, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	desired, lock := []byte("desired candidate"), []byte("lock candidate")
	marker, err := json.Marshal(transaction{Desired: base64.StdEncoding.EncodeToString(desired), Lock: base64.StdEncoding.EncodeToString(lock)})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(home.transactionPath(), marker, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(home.DesiredPath(), []byte("partial old value"), 0o600); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(home.Root)
	if err != nil {
		t.Fatal(err)
	}
	gotDesired, err := os.ReadFile(reopened.DesiredPath())
	if err != nil {
		t.Fatal(err)
	}
	gotLock, err := os.ReadFile(reopened.LockPath())
	if err != nil {
		t.Fatal(err)
	}
	if string(gotDesired) != string(desired) || string(gotLock) != string(lock) {
		t.Fatalf("recovered pair = %q / %q", gotDesired, gotLock)
	}
	if _, err := os.Stat(home.transactionPath()); !os.IsNotExist(err) {
		t.Fatalf("transaction marker still exists: %v", err)
	}
}

func TestDesiredWriterPreservesCommentsAcrossUpdateAndReorder(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "plugins.toml")
	existing := `# top-level note
plugins_version = 1

[[plugins]]
# A detail
module = "example.com/a"
version = "v1.0.0" # A pin

[[plugins]]
module = "example.com/b" # B identity
version = "v1.0.0"
`
	if err := os.WriteFile(path, []byte(existing), 0o600); err != nil {
		t.Fatal(err)
	}
	desired := builder.NewDesired(path, []builder.DesiredPlugin{
		{Module: "example.com/b", Version: "v1.0.0"},
		{Module: "example.com/a", Version: "v1.1.0"},
		{Module: "example.com/c", Version: "v1.0.0"},
	})
	data, err := marshalDesiredPreservingComments(path, desired)
	if err != nil {
		t.Fatal(err)
	}
	for _, comment := range []string{"# top-level note", "# A detail", "# A pin", "# B identity"} {
		if !strings.Contains(string(data), comment) {
			t.Fatalf("comment %q was lost:\n%s", comment, data)
		}
	}
	written := filepath.Join(t.TempDir(), "plugins.toml")
	if err := os.WriteFile(written, data, 0o600); err != nil {
		t.Fatal(err)
	}
	parsed, err := builder.ParseDesired(written)
	if err != nil {
		t.Fatalf("preserved output is invalid: %v\n%s", err, data)
	}
	if len(parsed.Plugins) != 3 || parsed.Plugins[0].Module != "example.com/b" || parsed.Plugins[1].Version != "v1.1.0" || parsed.Plugins[2].Module != "example.com/c" {
		t.Fatalf("preserved semantics = %#v", parsed.Plugins)
	}
}

func writeImageFixture(t *testing.T, home *Home, buildManifest, binary string) string {
	t.Helper()
	manifestDigest := sha256.Sum256([]byte(buildManifest))
	imageID := "sha256:" + hex.EncodeToString(manifestDigest[:])
	directory := home.imageDirectory(imageID)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	binaryDigest := sha256.Sum256([]byte(binary))
	artifact := "sha256:" + hex.EncodeToString(binaryDigest[:])
	if err := os.WriteFile(filepath.Join(directory, layout.RuntimeExecutableName(runtime.GOOS)), []byte(binary), 0o755); err != nil {
		t.Fatal(err)
	}
	manifest := builder.ImageManifest{SchemaVersion: 1, ImageID: imageID, ArtifactDigest: artifact, BuildManifest: json.RawMessage(buildManifest), DirectPlugins: []string{}, ComponentCreationOrder: []string{}, ManyOrder: map[string][]string{}}
	data, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "manifest.json"), data, 0o644); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(imageID, "sha256:") {
		t.Fatal("bad fixture")
	}
	return imageID
}
