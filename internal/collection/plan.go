package collection

import (
	"fmt"
	"sort"

	"github.com/ingot-agent/ingot/internal/builder"
)

// EntryStatus classifies one requested Plugin against the current recipe.
type EntryStatus string

const (
	StatusAdd             EntryStatus = "add"
	StatusSatisfied       EntryStatus = "satisfied"
	StatusVersionConflict EntryStatus = "version_conflict"
	StatusSourceConflict  EntryStatus = "source_conflict"
)

// PluginState is the machine-readable desired-state form used in plans.
type PluginState struct {
	Index      int    `json:"index"`
	Module     string `json:"module"`
	SourceKind string `json:"source_kind"`
	Version    string `json:"version,omitempty"`
	Path       string `json:"path,omitempty"`
}

// PlanEntry describes the disposition of one Collection Plugin.
type PlanEntry struct {
	CollectionIndex  int          `json:"collection_index"`
	Module           string       `json:"module"`
	RequestedVersion string       `json:"requested_version"`
	Status           EntryStatus  `json:"status"`
	Current          *PluginState `json:"current,omitempty"`
}

// OrderInversion is one pair whose relative order differs.
type OrderInversion struct {
	CurrentBefore  string `json:"current_before"`
	RequiredBefore string `json:"required_before"`
}

// OrderAssessment records Collection order compatibility and authorization.
type OrderAssessment struct {
	Status        string           `json:"status"`
	Current       []string         `json:"current"`
	Required      []string         `json:"required"`
	Inversions    []OrderInversion `json:"inversions"`
	Accepted      bool             `json:"accepted"`
	ReversalCount int              `json:"reversal_count,omitempty"`
}

// PlanOptions controls explicit conflict handling.
type PlanOptions struct {
	AcceptOrder bool
}

// Plan is a complete, deterministic Collection transformation result.
type Plan struct {
	CollectionID      string          `json:"collection_id"`
	CollectionVersion string          `json:"collection_version"`
	CollectionDigest  string          `json:"collection_digest"`
	CollectionSource  string          `json:"collection_source"`
	Entries           []PlanEntry     `json:"entries"`
	Order             OrderAssessment `json:"order"`
	Applicable        bool            `json:"applicable"`
	Changed           bool            `json:"changed"`
	CandidatePlugins  []PluginState   `json:"candidate_plugins,omitempty"`
	CandidateDigest   string          `json:"candidate_digest,omitempty"`

	candidate []builder.DesiredPlugin
}

// Candidate returns a defensive copy of the applicable desired Plugin set.
func (plan *Plan) Candidate() []builder.DesiredPlugin {
	return append([]builder.DesiredPlugin(nil), plan.candidate...)
}

// BuildPlan plans one Collection against a validated current desired state.
func BuildPlan(loaded Loaded, current *builder.DesiredPlugins, options PlanOptions) (*Plan, error) {
	if err := loaded.Collection.Validate(); err != nil {
		return nil, err
	}
	digest, err := loaded.Collection.Digest()
	if err != nil {
		return nil, err
	}
	if loaded.Digest != "" && loaded.Digest != digest {
		return nil, fmt.Errorf("INGOT-COLLECTION-DIGEST: loaded digest %s does not match semantic digest %s", loaded.Digest, digest)
	}
	loaded.Digest = digest
	if err := current.Validate(); err != nil {
		return nil, err
	}
	plan := &Plan{
		CollectionID: loaded.Collection.ID, CollectionVersion: loaded.Collection.Version,
		CollectionDigest: loaded.Digest, CollectionSource: loaded.Source,
		Entries: make([]PlanEntry, len(loaded.Collection.Plugins)),
	}
	currentByModule := make(map[string]int, len(current.Plugins))
	for index, plugin := range current.Plugins {
		currentByModule[plugin.Module] = index
	}
	collectionIndex := make(map[string]int, len(loaded.Collection.Plugins))
	blockingConflict := false
	for index, requested := range loaded.Collection.Plugins {
		collectionIndex[requested.Module] = index
		entry := PlanEntry{CollectionIndex: index, Module: requested.Module, RequestedVersion: requested.Version, Status: StatusAdd}
		if currentIndex, exists := currentByModule[requested.Module]; exists {
			state := pluginState(currentIndex, current.Plugins[currentIndex])
			entry.Current = &state
			switch currentPlugin := current.Plugins[currentIndex]; {
			case currentPlugin.Path != "":
				entry.Status = StatusSourceConflict
				blockingConflict = true
			case currentPlugin.Version != requested.Version:
				entry.Status = StatusVersionConflict
				blockingConflict = true
			default:
				entry.Status = StatusSatisfied
			}
		}
		plan.Entries[index] = entry
	}

	currentShared := make([]string, 0)
	for _, plugin := range current.Plugins {
		if _, exists := collectionIndex[plugin.Module]; exists {
			currentShared = append(currentShared, plugin.Module)
		}
	}
	requiredShared := make([]string, 0, len(currentShared))
	for _, plugin := range loaded.Collection.Plugins {
		if _, exists := currentByModule[plugin.Module]; exists {
			requiredShared = append(requiredShared, plugin.Module)
		}
	}
	inversions := orderInversions(currentShared, collectionIndex)
	plan.Order = OrderAssessment{Status: "satisfied", Current: currentShared, Required: requiredShared, Inversions: inversions}
	if len(inversions) > 0 {
		plan.Order.Status = "order_conflict"
		plan.Order.Accepted = options.AcceptOrder
	}
	plan.Applicable = !blockingConflict && (len(inversions) == 0 || options.AcceptOrder)
	if !plan.Applicable {
		return plan, nil
	}

	baseline := append([]builder.DesiredPlugin(nil), current.Plugins...)
	if len(inversions) > 0 {
		var reversals int
		baseline, reversals = minimumReorder(current.Plugins, requiredShared, collectionIndex)
		plan.Order.ReversalCount = reversals
	}
	candidate, err := stableMerge(baseline, loaded.Collection.Plugins)
	if err != nil {
		return nil, err
	}
	desired := builder.NewDesired("", candidate)
	if err := desired.Validate(); err != nil {
		return nil, err
	}
	candidateDigest, err := desired.Digest()
	if err != nil {
		return nil, err
	}
	plan.candidate = candidate
	plan.CandidatePlugins = make([]PluginState, len(candidate))
	for index, plugin := range candidate {
		plan.CandidatePlugins[index] = pluginState(index, plugin)
	}
	plan.CandidateDigest = candidateDigest
	plan.Changed = !samePlugins(current.Plugins, candidate)
	return plan, nil
}

func pluginState(index int, plugin builder.DesiredPlugin) PluginState {
	state := PluginState{Index: index, Module: plugin.Module, SourceKind: "module", Version: plugin.Version}
	if plugin.Path != "" {
		state.SourceKind, state.Version, state.Path = "path", "", plugin.Path
	}
	return state
}

func orderInversions(current []string, collectionIndex map[string]int) []OrderInversion {
	result := make([]OrderInversion, 0)
	for left := 0; left < len(current); left++ {
		for right := left + 1; right < len(current); right++ {
			if collectionIndex[current[left]] > collectionIndex[current[right]] {
				result = append(result, OrderInversion{CurrentBefore: current[left], RequiredBefore: current[right]})
			}
		}
	}
	return result
}

type reorderState struct {
	valid   bool
	cost    int
	indices []int
	plugins []builder.DesiredPlugin
}

// minimumReorder computes the minimum-Kendall interleaving of Collection
// members in required order and all other current Plugins in current order.
func minimumReorder(current []builder.DesiredPlugin, required []string, collectionIndex map[string]int) ([]builder.DesiredPlugin, int) {
	byModule := make(map[string]builder.DesiredPlugin, len(current))
	originalIndex := make(map[string]int, len(current))
	unselected := make([]builder.DesiredPlugin, 0)
	for index, plugin := range current {
		byModule[plugin.Module], originalIndex[plugin.Module] = plugin, index
		if _, selected := collectionIndex[plugin.Module]; !selected {
			unselected = append(unselected, plugin)
		}
	}
	selected := make([]builder.DesiredPlugin, len(required))
	for index, modulePath := range required {
		selected[index] = byModule[modulePath]
	}
	dp := make([][]reorderState, len(selected)+1)
	for index := range dp {
		dp[index] = make([]reorderState, len(unselected)+1)
	}
	dp[0][0] = reorderState{valid: true, indices: []int{}, plugins: []builder.DesiredPlugin{}}
	for selectedCount := 0; selectedCount <= len(selected); selectedCount++ {
		for unselectedCount := 0; unselectedCount <= len(unselected); unselectedCount++ {
			state := dp[selectedCount][unselectedCount]
			if !state.valid {
				continue
			}
			if selectedCount < len(selected) {
				next := selected[selectedCount]
				updateReorder(&dp[selectedCount+1][unselectedCount], state, next, originalIndex[next.Module])
			}
			if unselectedCount < len(unselected) {
				next := unselected[unselectedCount]
				updateReorder(&dp[selectedCount][unselectedCount+1], state, next, originalIndex[next.Module])
			}
		}
	}
	best := dp[len(selected)][len(unselected)]
	return best.plugins, best.cost
}

func updateReorder(destination *reorderState, previous reorderState, plugin builder.DesiredPlugin, originalIndex int) {
	cost := previous.cost
	for _, index := range previous.indices {
		if index > originalIndex {
			cost++
		}
	}
	indices := append(append([]int(nil), previous.indices...), originalIndex)
	plugins := append(append([]builder.DesiredPlugin(nil), previous.plugins...), plugin)
	candidate := reorderState{valid: true, cost: cost, indices: indices, plugins: plugins}
	if !destination.valid || candidate.cost < destination.cost || (candidate.cost == destination.cost && lexicographicallyLess(candidate.indices, destination.indices)) {
		*destination = candidate
	}
}

func lexicographicallyLess(left, right []int) bool {
	for index := range left {
		if left[index] != right[index] {
			return left[index] < right[index]
		}
	}
	return false
}

func stableMerge(existing []builder.DesiredPlugin, requested []Plugin) ([]builder.DesiredPlugin, error) {
	type node struct {
		plugin          builder.DesiredPlugin
		existingIndex   int
		collectionIndex int
	}
	nodes := make(map[string]node, len(existing)+len(requested))
	for index, plugin := range existing {
		nodes[plugin.Module] = node{plugin: plugin, existingIndex: index, collectionIndex: -1}
	}
	for index, plugin := range requested {
		current, exists := nodes[plugin.Module]
		if exists {
			current.collectionIndex = index
			nodes[plugin.Module] = current
			continue
		}
		nodes[plugin.Module] = node{plugin: builder.DesiredPlugin{Module: plugin.Module, Version: plugin.Version}, existingIndex: -1, collectionIndex: index}
	}
	adjacency := make(map[string]map[string]bool, len(nodes))
	indegree := make(map[string]int, len(nodes))
	for modulePath := range nodes {
		adjacency[modulePath] = map[string]bool{}
	}
	addSequenceEdges := func(sequence []string) {
		for index := 1; index < len(sequence); index++ {
			from, to := sequence[index-1], sequence[index]
			if !adjacency[from][to] {
				adjacency[from][to] = true
				indegree[to]++
			}
		}
	}
	existingModules := make([]string, len(existing))
	for index, plugin := range existing {
		existingModules[index] = plugin.Module
	}
	requestedModules := make([]string, len(requested))
	for index, plugin := range requested {
		requestedModules[index] = plugin.Module
	}
	addSequenceEdges(existingModules)
	addSequenceEdges(requestedModules)
	result := make([]builder.DesiredPlugin, 0, len(nodes))
	used := make(map[string]bool, len(nodes))
	for len(result) < len(nodes) {
		available := make([]string, 0)
		for modulePath := range nodes {
			if !used[modulePath] && indegree[modulePath] == 0 {
				available = append(available, modulePath)
			}
		}
		if len(available) == 0 {
			return nil, fmt.Errorf("INGOT-COLLECTION-ORDER: candidate constraints contain a cycle")
		}
		sort.Slice(available, func(left, right int) bool {
			leftNode, rightNode := nodes[available[left]], nodes[available[right]]
			if (leftNode.existingIndex >= 0) != (rightNode.existingIndex >= 0) {
				return leftNode.existingIndex >= 0
			}
			if leftNode.existingIndex >= 0 && leftNode.existingIndex != rightNode.existingIndex {
				return leftNode.existingIndex < rightNode.existingIndex
			}
			if leftNode.collectionIndex != rightNode.collectionIndex {
				return leftNode.collectionIndex < rightNode.collectionIndex
			}
			return available[left] < available[right]
		})
		selected := available[0]
		used[selected] = true
		result = append(result, nodes[selected].plugin)
		for target := range adjacency[selected] {
			indegree[target]--
		}
	}
	return result, nil
}

func samePlugins(left, right []builder.DesiredPlugin) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}
