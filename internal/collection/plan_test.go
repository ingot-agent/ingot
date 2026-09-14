package collection

import (
	"math/bits"
	"slices"
	"strconv"
	"testing"

	"github.com/ingot-agent/ingot/internal/builder"
)

func TestPlanReportsOrderConflictAndAcceptedMinimumReorder(t *testing.T) {
	t.Parallel()
	current := desiredModules("1", "3", "2", "4", "5")
	loaded := loadedModules("3", "9", "1", "6", "2", "7")
	strict, err := BuildPlan(loaded, current, PlanOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if strict.Applicable || strict.Order.Status != "order_conflict" || strict.Order.Accepted {
		t.Fatalf("strict plan = %#v", strict)
	}
	if len(strict.Order.Inversions) != 1 || strict.Order.Inversions[0].CurrentBefore != moduleID("1") || strict.Order.Inversions[0].RequiredBefore != moduleID("3") {
		t.Fatalf("inversions = %#v", strict.Order.Inversions)
	}
	accepted, err := BuildPlan(loaded, current, PlanOptions{AcceptOrder: true})
	if err != nil {
		t.Fatal(err)
	}
	want := moduleIDs("3", "9", "1", "6", "2", "4", "5", "7")
	if !accepted.Applicable || !accepted.Changed || !accepted.Order.Accepted || accepted.Order.ReversalCount != 1 || !slices.Equal(planModules(accepted), want) {
		t.Fatalf("accepted modules=%v order=%#v", planModules(accepted), accepted.Order)
	}
}

func TestPlanStableMergePreservesBothSequences(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		current    []string
		collection []string
		want       []string
	}{
		{name: "between and trailing", current: []string{"a", "x", "b", "y"}, collection: []string{"a", "n", "b", "t"}, want: []string{"a", "x", "n", "b", "y", "t"}},
		{name: "no overlap appends", current: []string{"x", "y"}, collection: []string{"a", "b"}, want: []string{"x", "y", "a", "b"}},
		{name: "leading", current: []string{"x", "a", "y"}, collection: []string{"n", "a"}, want: []string{"x", "n", "a", "y"}},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			plan, err := BuildPlan(loadedModules(test.collection...), desiredModules(test.current...), PlanOptions{})
			if err != nil {
				t.Fatal(err)
			}
			if !plan.Applicable || !slices.Equal(planModules(plan), moduleIDs(test.want...)) {
				t.Fatalf("modules = %v, want %v", planModules(plan), moduleIDs(test.want...))
			}
		})
	}
}

func TestPlanClassifiesVersionAndSourceConflicts(t *testing.T) {
	t.Parallel()
	current := builder.NewDesired("plugins.toml", []builder.DesiredPlugin{
		{Module: moduleID("a"), Version: "v1.0.0"},
		{Module: moduleID("b"), Path: "../b"},
	})
	loaded := loadedModules("a", "b", "c")
	loaded.Collection.Plugins[0].Version = "v1.1.0"
	loaded.Digest, _ = loaded.Collection.Digest()
	plan, err := BuildPlan(loaded, current, PlanOptions{AcceptOrder: true})
	if err != nil {
		t.Fatal(err)
	}
	if plan.Applicable || plan.Entries[0].Status != StatusVersionConflict || plan.Entries[1].Status != StatusSourceConflict || plan.Entries[2].Status != StatusAdd {
		t.Fatalf("entries = %#v", plan.Entries)
	}
}

func TestMinimumReorderUsesGlobalMinimumNotGreedyMerge(t *testing.T) {
	t.Parallel()
	current := desiredModules("0", "1", "2", "3")
	loaded := loadedModules("3", "1", "0")
	plan, err := BuildPlan(loaded, current, PlanOptions{AcceptOrder: true})
	if err != nil {
		t.Fatal(err)
	}
	want := moduleIDs("3", "1", "0", "2")
	if plan.Order.ReversalCount != 4 || !slices.Equal(planModules(plan), want) {
		t.Fatalf("modules=%v reversals=%d", planModules(plan), plan.Order.ReversalCount)
	}
}

func TestMinimumReorderExhaustive(t *testing.T) {
	t.Parallel()
	for size := 2; size <= 6; size++ {
		current := make([]builder.DesiredPlugin, size)
		for index := range current {
			current[index] = builder.DesiredPlugin{Module: moduleID(strconv.Itoa(index)), Version: "v1.0.0"}
		}
		for mask := 1; mask < 1<<size; mask++ {
			if bits.OnesCount(uint(mask)) < 2 {
				continue
			}
			selected := make([]string, 0)
			for index := 0; index < size; index++ {
				if mask&(1<<index) != 0 {
					selected = append(selected, current[index].Module)
				}
			}
			for _, required := range permutations(selected) {
				collectionIndex := make(map[string]int, len(required))
				for index, modulePath := range required {
					collectionIndex[modulePath] = index
				}
				got, cost := minimumReorder(current, required, collectionIndex)
				wantIndices, wantCost := bruteMinimumReorder(current, required, collectionIndex)
				gotIndices := originalIndices(current, got)
				if cost != wantCost || !slices.Equal(gotIndices, wantIndices) {
					t.Fatalf("size=%d mask=%b required=%v got=%v/%d want=%v/%d", size, mask, required, gotIndices, cost, wantIndices, wantCost)
				}
			}
		}
	}
}

func bruteMinimumReorder(current []builder.DesiredPlugin, required []string, collectionIndex map[string]int) ([]int, int) {
	indexByModule := make(map[string]int, len(current))
	selectedIndices := make([]int, len(required))
	for index, plugin := range current {
		indexByModule[plugin.Module] = index
	}
	for index, modulePath := range required {
		selectedIndices[index] = indexByModule[modulePath]
	}
	unselectedIndices := make([]int, 0)
	for index, plugin := range current {
		if _, selected := collectionIndex[plugin.Module]; !selected {
			unselectedIndices = append(unselectedIndices, index)
		}
	}
	bestCost := int(^uint(0) >> 1)
	var best []int
	var visit func(int, int, []int)
	visit = func(selectedCount, unselectedCount int, candidate []int) {
		if selectedCount == len(selectedIndices) && unselectedCount == len(unselectedIndices) {
			cost := inversionCount(candidate)
			if cost < bestCost || (cost == bestCost && (best == nil || lexicographicallyLess(candidate, best))) {
				bestCost = cost
				best = append([]int(nil), candidate...)
			}
			return
		}
		if selectedCount < len(selectedIndices) {
			visit(selectedCount+1, unselectedCount, append(candidate, selectedIndices[selectedCount]))
		}
		if unselectedCount < len(unselectedIndices) {
			visit(selectedCount, unselectedCount+1, append(candidate, unselectedIndices[unselectedCount]))
		}
	}
	visit(0, 0, nil)
	return best, bestCost
}

func permutations(values []string) [][]string {
	working := append([]string(nil), values...)
	result := make([][]string, 0)
	var generate func(int)
	generate = func(index int) {
		if index == len(working) {
			result = append(result, append([]string(nil), working...))
			return
		}
		for candidate := index; candidate < len(working); candidate++ {
			working[index], working[candidate] = working[candidate], working[index]
			generate(index + 1)
			working[index], working[candidate] = working[candidate], working[index]
		}
	}
	generate(0)
	return result
}

func originalIndices(current, reordered []builder.DesiredPlugin) []int {
	indexByModule := make(map[string]int, len(current))
	for index, plugin := range current {
		indexByModule[plugin.Module] = index
	}
	result := make([]int, len(reordered))
	for index, plugin := range reordered {
		result[index] = indexByModule[plugin.Module]
	}
	return result
}

func inversionCount(indices []int) int {
	result := 0
	for left := 0; left < len(indices); left++ {
		for right := left + 1; right < len(indices); right++ {
			if indices[left] > indices[right] {
				result++
			}
		}
	}
	return result
}

func desiredModules(names ...string) *builder.DesiredPlugins {
	plugins := make([]builder.DesiredPlugin, len(names))
	for index, name := range names {
		plugins[index] = builder.DesiredPlugin{Module: moduleID(name), Version: "v1.0.0"}
	}
	return builder.NewDesired("plugins.toml", plugins)
}

func loadedModules(names ...string) Loaded {
	plugins := make([]Plugin, len(names))
	for index, name := range names {
		plugins[index] = Plugin{Module: moduleID(name), Version: "v1.0.0"}
	}
	value := Collection{
		CollectionSchema: 1, ID: "example.com/collections/test", Version: "v1.0.0",
		Metadata: Metadata{Name: "Test"}, Plugins: plugins,
	}
	digest, _ := value.Digest()
	return Loaded{Source: "fixture.toml", Digest: digest, Collection: value}
}

func moduleID(name string) string { return "example.com/plugins/" + name }

func moduleIDs(names ...string) []string {
	result := make([]string, len(names))
	for index, name := range names {
		result[index] = moduleID(name)
	}
	return result
}

func planModules(plan *Plan) []string {
	result := make([]string, len(plan.CandidatePlugins))
	for index, plugin := range plan.CandidatePlugins {
		result[index] = plugin.Module
	}
	return result
}
