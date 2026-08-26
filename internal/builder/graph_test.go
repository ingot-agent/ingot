package builder

import (
	"go/token"
	"go/types"
	"os"
	"strings"
	"testing"
)

func TestBaseBuildEnvironmentUsesNativeTemporaryVariables(t *testing.T) {
	t.Parallel()
	tests := []struct {
		goos string
		want map[string]string
	}{
		{goos: "linux", want: map[string]string{"TMPDIR": `/tmp/ingot`}},
		{goos: "windows", want: map[string]string{"TEMP": `C:\Temp\ingot`, "TMP": `C:\Temp\ingot`}},
	}
	for _, test := range tests {
		t.Run(test.goos, func(t *testing.T) {
			t.Parallel()
			temporary := `/tmp/ingot`
			if test.goos == "windows" {
				temporary = `C:\Temp\ingot`
			}
			environment := baseBuildEnvironment(test.goos, "home", "cache", temporary)
			values := map[string]string{}
			for _, item := range environment {
				key, value, _ := strings.Cut(item, "=")
				values[key] = value
			}
			for key, want := range test.want {
				if got := values[key]; got != want {
					t.Fatalf("%s = %q, want %q; environment = %#v", key, got, want, environment)
				}
			}
		})
	}
}

func TestLockedEnvironmentPinsGoWorkDirectoryToSystemTemp(t *testing.T) {
	t.Parallel()
	environment := lockedEnvironment(&Lock{Target: TargetLock{GOOS: "windows", GOARCH: "amd64"}}, t.TempDir())
	for _, item := range environment {
		key, value, _ := strings.Cut(item, "=")
		if key == "GOTMPDIR" {
			if value != os.TempDir() {
				t.Fatalf("GOTMPDIR = %q, want %q", value, os.TempDir())
			}
			return
		}
	}
	t.Fatalf("GOTMPDIR missing from %#v", environment)
}

func TestGraphReportsCompleteCyclePath(t *testing.T) {
	t.Parallel()
	contracts := types.NewPackage("example.com/contracts", "contracts")
	capabilityA := testInterface(contracts, "CapabilityA", "A")
	capabilityB := testInterface(contracts, "CapabilityB", "B")
	first := &Component{ID: "example.com/first/default", DirectIndex: 0, ExportList: []*Export{{Name: "A", Type: capabilityA}}, DependencyList: []*Dependency{{Name: "B", Type: capabilityB, Target: capabilityB, Cardinality: CardinalityOne}}}
	second := &Component{ID: "example.com/second/default", DirectIndex: 1, ExportList: []*Export{{Name: "B", Type: capabilityB}}, DependencyList: []*Dependency{{Name: "A", Type: capabilityA, Target: capabilityA, Cardinality: CardinalityOne}}}
	graph := &Graph{Components: []*Component{first, second}}
	err := graph.resolve()
	if err == nil || !strings.Contains(err.Error(), "INGOT-GRAPH-CYCLE") || !strings.Contains(err.Error(), first.ID+" -> "+second.ID+" -> "+first.ID) {
		t.Fatalf("cycle error = %v", err)
	}
}

func testInterface(pkg *types.Package, name, methodName string) *types.Named {
	signature := types.NewSignatureType(nil, nil, nil, types.NewTuple(), types.NewTuple(), false)
	method := types.NewFunc(token.NoPos, pkg, methodName, signature)
	interfaceType := types.NewInterfaceType([]*types.Func{method}, nil).Complete()
	return types.NewNamed(types.NewTypeName(token.NoPos, pkg, name, nil), interfaceType, nil)
}
