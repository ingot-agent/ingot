package home

import (
	"context"
	"path/filepath"
	"strings"

	"github.com/ingot-agent/ingot/internal/builder"
	"github.com/ingot-agent/ingot/internal/collection"
)

// CollectionApplyResult reports the exact plan and committed resolution.
type CollectionApplyResult struct {
	ProjectPaths
	Plan          *collection.Plan `json:"plan"`
	PluginsDigest string           `json:"plugins_digest,omitempty"`
	ImageID       string           `json:"image_id,omitempty"`
}

// CollectionConflictError means planning succeeded but the Collection cannot
// be applied without an unaccepted or manually resolved conflict.
type CollectionConflictError struct {
	Plan *collection.Plan
}

func (conflict *CollectionConflictError) Error() string {
	var kinds []string
	for _, entry := range conflict.Plan.Entries {
		switch entry.Status {
		case collection.StatusVersionConflict:
			kinds = append(kinds, entry.Module+":version")
		case collection.StatusSourceConflict:
			kinds = append(kinds, entry.Module+":source")
		}
	}
	if conflict.Plan.Order.Status == "order_conflict" && !conflict.Plan.Order.Accepted {
		kinds = append(kinds, "order")
	}
	return "INGOT-COLLECTION-CONFLICT: " + strings.Join(kinds, ", ") + "; run `ingot collection plan` for details"
}

// PlanCollection reads the current project under its writer lock and returns
// a deterministic, side-effect-free Collection plan.
func (home *Home) PlanCollection(ctx context.Context, options RecipeOptions, loaded collection.Loaded, planOptions collection.PlanOptions) (*collection.Plan, ProjectPaths, error) {
	paths, release, err := prepareProject(ctx, options)
	if err != nil {
		return nil, ProjectPaths{}, err
	}
	defer release()
	desired, err := builder.ParseDesired(paths.Recipe)
	if err != nil {
		return nil, paths, err
	}
	plan, err := collection.BuildPlan(loaded, desired, planOptions)
	return plan, paths, err
}

// ApplyCollection replans under the project lock, preflights the complete
// candidate through the Builder, and atomically commits recipe and lock.
func (home *Home) ApplyCollection(ctx context.Context, options RecipeOptions, loaded collection.Loaded, planOptions collection.PlanOptions, resolveOptions builder.ResolveOptions) (CollectionApplyResult, error) {
	paths, release, err := prepareProject(ctx, options)
	if err != nil {
		return CollectionApplyResult{}, err
	}
	defer release()
	result := CollectionApplyResult{ProjectPaths: paths}
	desired, err := builder.ParseDesired(paths.Recipe)
	if err != nil {
		return result, err
	}
	plan, err := collection.BuildPlan(loaded, desired, planOptions)
	result.Plan = plan
	if err != nil {
		return result, err
	}
	if !plan.Applicable {
		return result, &CollectionConflictError{Plan: plan}
	}
	result.PluginsDigest = plan.CandidateDigest
	if !plan.Changed {
		if lock, lockErr := builder.ParseLock(paths.Lock); lockErr == nil && lock.PluginsDigest == plan.CandidateDigest {
			result.ImageID, _ = lock.ImageID()
		}
		return result, nil
	}
	candidate := builder.NewDesired(paths.Recipe, plan.Candidate())
	if err := candidate.Validate(); err != nil {
		return result, err
	}
	if _, err := builder.LoadBuilderConfig(home.BuilderConfigPath()); err != nil {
		return result, err
	}
	if resolveOptions.GOMODCACHE == "" {
		resolveOptions.GOMODCACHE = filepath.Join(home.Root, "cache", "gomod")
	}
	lock, err := builder.Resolve(ctx, candidate, resolveOptions)
	if err != nil {
		return result, err
	}
	if err := commitProjectPair(paths, candidate, lock); err != nil {
		return result, err
	}
	result.PluginsDigest = lock.PluginsDigest
	result.ImageID, _ = lock.ImageID()
	return result, nil
}
