package builder

import (
	"go/token"
	"go/types"
	"strings"
	"testing"
)

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
