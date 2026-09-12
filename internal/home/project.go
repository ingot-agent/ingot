package home

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/ingot-agent/ingot/internal/builder"
	"github.com/ingot-agent/ingot/internal/image"
)

type RecipeOptions struct {
	CWD    string
	Use    string
	Lock   string
	Locked bool
	Tag    string
}

type ProjectPaths struct {
	Recipe string `json:"recipe"`
	Lock   string `json:"lock"`
}

type ProjectStatus struct {
	ProjectPaths
	DesiredDigest string `json:"desired_digest,omitempty"`
	LockedDigest  string `json:"locked_digest,omitempty"`
	LockedImageID string `json:"locked_image_id,omitempty"`
	DesiredLocked bool   `json:"desired_locked"`
	LockedSources bool   `json:"locked_sources"`
	Built         bool   `json:"built"`
}

type ProjectLookup struct {
	Index  int
	Plugin builder.DesiredPlugin
}

type mutation func(*builder.DesiredPlugins, *builder.Lock) error

func (home *Home) LookupProjectPlugin(options RecipeOptions, reference string) (ProjectLookup, error) {
	paths, release, err := prepareProject(context.Background(), options)
	if err != nil {
		return ProjectLookup{}, err
	}
	defer release()
	desired, err := builder.ParseDesired(paths.Recipe)
	if err != nil {
		return ProjectLookup{}, err
	}
	lock, _ := builder.ParseLock(paths.Lock)
	index, err := findPlugin(desired, lock, reference)
	if err != nil {
		return ProjectLookup{}, err
	}
	return ProjectLookup{Index: index, Plugin: desired.Plugins[index]}, nil
}

func (home *Home) AddProject(ctx context.Context, options RecipeOptions, plugin builder.DesiredPlugin, resolveOptions builder.ResolveOptions) (ProjectPaths, error) {
	return home.mutateProject(ctx, options, resolveOptions, func(desired *builder.DesiredPlugins, lock *builder.Lock) error {
		for _, existing := range desired.Plugins {
			if existing.Module == plugin.Module {
				return fmt.Errorf("plugin %s is already present", plugin.Module)
			}
		}
		desired.Plugins = append(desired.Plugins, plugin)
		return nil
	})
}
func (home *Home) RemoveProject(ctx context.Context, options RecipeOptions, reference string, resolveOptions builder.ResolveOptions) (ProjectPaths, error) {
	return home.mutateProject(ctx, options, resolveOptions, func(desired *builder.DesiredPlugins, lock *builder.Lock) error {
		index, err := findPlugin(desired, lock, reference)
		if err != nil {
			return err
		}
		desired.Plugins = append(desired.Plugins[:index], desired.Plugins[index+1:]...)
		return nil
	})
}
func (home *Home) UpdateProject(ctx context.Context, options RecipeOptions, reference string, replacement builder.DesiredPlugin, resolveOptions builder.ResolveOptions) (ProjectPaths, error) {
	return home.mutateProject(ctx, options, resolveOptions, func(desired *builder.DesiredPlugins, lock *builder.Lock) error {
		index, err := findPlugin(desired, lock, reference)
		if err != nil {
			return err
		}
		if replacement.Module != "" && replacement.Module != desired.Plugins[index].Module {
			return fmt.Errorf("update cannot change canonical module path")
		}
		replacement.Module = desired.Plugins[index].Module
		desired.Plugins[index] = replacement
		return nil
	})
}
func (home *Home) ReorderProject(ctx context.Context, options RecipeOptions, reference, anchor string, before bool, resolveOptions builder.ResolveOptions) (ProjectPaths, error) {
	return home.mutateProject(ctx, options, resolveOptions, func(desired *builder.DesiredPlugins, lock *builder.Lock) error {
		from, err := findPlugin(desired, lock, reference)
		if err != nil {
			return err
		}
		to, err := findPlugin(desired, lock, anchor)
		if err != nil {
			return err
		}
		if from == to {
			return fmt.Errorf("plugin and anchor are identical")
		}
		entry := desired.Plugins[from]
		desired.Plugins = append(desired.Plugins[:from], desired.Plugins[from+1:]...)
		if from < to {
			to--
		}
		if !before {
			to++
		}
		desired.Plugins = append(desired.Plugins, builder.DesiredPlugin{})
		copy(desired.Plugins[to+1:], desired.Plugins[to:])
		desired.Plugins[to] = entry
		return nil
	})
}

func (home *Home) mutateProject(ctx context.Context, options RecipeOptions, resolveOptions builder.ResolveOptions, change mutation) (ProjectPaths, error) {
	paths, release, err := prepareProject(ctx, options)
	if err != nil {
		return ProjectPaths{}, err
	}
	defer release()
	desired, err := builder.ParseDesired(paths.Recipe)
	if err != nil {
		return paths, err
	}
	lock, lockErr := builder.ParseLock(paths.Lock)
	if lockErr != nil && !isMissing(lockErr) {
		return paths, lockErr
	}
	if err := change(desired, lock); err != nil {
		return paths, err
	}
	if err := desired.Validate(); err != nil {
		return paths, err
	}
	if _, err := builder.LoadBuilderConfig(home.BuilderConfigPath()); err != nil {
		return paths, err
	}
	if resolveOptions.GOMODCACHE == "" {
		resolveOptions.GOMODCACHE = filepath.Join(home.Root, "cache", "gomod")
	}
	candidate, err := builder.Resolve(ctx, desired, resolveOptions)
	if err != nil {
		return paths, err
	}
	if err := commitProjectPair(paths, desired, candidate); err != nil {
		return paths, err
	}
	return paths, nil
}

func commitProjectPair(paths ProjectPaths, desired *builder.DesiredPlugins, lock *builder.Lock) error {
	desiredData, err := marshalDesiredPreservingComments(paths.Recipe, desired)
	if err != nil {
		return err
	}
	lockData, err := lock.MarshalTOML()
	if err != nil {
		return err
	}
	markerPath := projectTransactionPath(paths)
	markerData, err := json.Marshal(transaction{Desired: base64.StdEncoding.EncodeToString(desiredData), Lock: base64.StdEncoding.EncodeToString(lockData)})
	if err != nil {
		return err
	}
	if err := image.AtomicWrite(markerPath, markerData, 0o600); err != nil {
		return err
	}
	if err := image.AtomicWriteUserFile(paths.Recipe, desiredData, 0o644); err != nil {
		return err
	}
	if err := image.AtomicWriteUserFile(paths.Lock, lockData, 0o644); err != nil {
		return err
	}
	return os.Remove(markerPath)
}

func (home *Home) ProjectStatus(options RecipeOptions) (ProjectStatus, error) {
	paths, release, err := prepareProject(context.Background(), options)
	if err != nil {
		return ProjectStatus{}, err
	}
	defer release()
	return home.projectStatusUnlocked(paths)
}

func (home *Home) projectStatusUnlocked(paths ProjectPaths) (ProjectStatus, error) {
	status := ProjectStatus{ProjectPaths: paths}
	desired, err := builder.ParseDesired(paths.Recipe)
	if err != nil {
		return status, err
	}
	status.DesiredDigest, _ = desired.Digest()
	lock, err := builder.ParseLock(paths.Lock)
	if err != nil {
		if isMissing(err) {
			return status, nil
		}
		return status, err
	}
	status.LockedDigest = lock.PluginsDigest
	status.LockedImageID, _ = lock.ImageID()
	status.DesiredLocked = status.DesiredDigest == status.LockedDigest
	status.LockedSources = true
	for _, replacement := range lock.Replacements {
		calculated, digestErr := builder.ModuleSourceDigest(replacement.DevPath)
		if pluginIsDev(lock, replacement.ModulePath) {
			calculated, digestErr = builder.DevSourceDigest(replacement.DevPath)
		}
		if digestErr != nil || calculated != replacement.ContentSHA256 {
			status.LockedSources = false
			break
		}
	}
	if status.LockedImageID != "" {
		expected, _ := lock.CanonicalBuildManifest()
		_, verifyErr := image.Verify(home.imageDirectory(status.LockedImageID), status.LockedImageID, expected)
		status.Built = verifyErr == nil
	}
	return status, nil
}

func (home *Home) ProjectInspect(options RecipeOptions, reference string) (Inspection, error) {
	paths, release, err := prepareProject(context.Background(), options)
	if err != nil {
		return Inspection{}, err
	}
	defer release()
	status, err := home.projectStatusUnlocked(paths)
	if err != nil {
		return Inspection{}, err
	}
	lock, err := builder.ParseLock(status.Lock)
	if err != nil {
		return Inspection{}, err
	}
	legacyStatus := Status{DesiredDigest: status.DesiredDigest, LockedDigest: status.LockedDigest, LockedImageID: status.LockedImageID, DesiredLocked: status.DesiredLocked, LockedSources: status.LockedSources, Built: status.Built}
	inspection := Inspection{Status: legacyStatus, ManyOrder: map[string][]string{}}
	for i, plugin := range lock.Plugins {
		if reference != "" && reference != plugin.ID && reference != plugin.Name {
			continue
		}
		inspection.DirectPlugins = append(inspection.DirectPlugins, PluginInspection{DirectPluginIndex: i, ID: plugin.ID, Name: plugin.Name, SourceKind: plugin.SourceKind, Version: plugin.Version, ManifestDigest: plugin.ManifestDigest, Components: plugin.Components})
	}
	if reference != "" && len(inspection.DirectPlugins) == 0 {
		return Inspection{}, fmt.Errorf("plugin %q not found", reference)
	}
	if status.Built {
		verified, verifyErr := image.Verify(home.imageDirectory(status.LockedImageID), status.LockedImageID, nil)
		if verifyErr == nil {
			inspection.ComponentCreationOrder = verified.Manifest.ComponentCreationOrder
			inspection.ManyOrder = verified.Manifest.ManyOrder
			inspection.HostDependencies = verified.Manifest.HostDependencies
		}
	}
	return inspection, nil
}

func resolveProjectPaths(options RecipeOptions) (ProjectPaths, error) {
	cwd := options.CWD
	if cwd == "" {
		var err error
		cwd, err = os.Getwd()
		if err != nil {
			return ProjectPaths{}, err
		}
	}
	absCWD, err := filepath.Abs(cwd)
	if err != nil {
		return ProjectPaths{}, err
	}
	recipe := options.Use
	if recipe == "" {
		recipe = "plugins.toml"
	}
	if !filepath.IsAbs(recipe) {
		recipe = filepath.Join(absCWD, recipe)
	}
	recipe = filepath.Clean(recipe)
	lock := options.Lock
	if lock == "" {
		if strings.HasSuffix(strings.ToLower(recipe), ".toml") {
			lock = strings.TrimSuffix(recipe, filepath.Ext(recipe)) + ".lock"
		} else {
			lock = recipe + ".lock"
		}
	} else if !filepath.IsAbs(lock) {
		lock = filepath.Join(absCWD, lock)
	}
	lock = filepath.Clean(lock)
	if recipe == lock {
		return ProjectPaths{}, fmt.Errorf("INGOT-BUILD-LOCK-PATH: recipe and lock must differ")
	}
	return ProjectPaths{Recipe: recipe, Lock: lock}, nil
}

func prepareProject(ctx context.Context, options RecipeOptions) (ProjectPaths, func(), error) {
	paths, err := resolveProjectPaths(options)
	if err != nil {
		return ProjectPaths{}, nil, err
	}
	release, err := acquireProjectLock(ctx, projectWriterLockPath(paths))
	if err != nil {
		return ProjectPaths{}, nil, err
	}
	if err := recoverProjectTransaction(paths); err != nil {
		release()
		return ProjectPaths{}, nil, err
	}
	info, err := os.Stat(paths.Recipe)
	if err != nil {
		release()
		return ProjectPaths{}, nil, fmt.Errorf("INGOT-BUILD-INPUT-RECIPE: %w", err)
	}
	if !info.Mode().IsRegular() {
		release()
		return ProjectPaths{}, nil, fmt.Errorf("INGOT-BUILD-INPUT-RECIPE: %s is not a regular file", paths.Recipe)
	}
	return paths, release, nil
}

func DiscoverProject(options RecipeOptions) (ProjectPaths, error) {
	paths, release, err := prepareProject(context.Background(), options)
	if err != nil {
		return ProjectPaths{}, err
	}
	release()
	return paths, nil
}

func projectTransactionPath(paths ProjectPaths) string {
	return filepath.Join(filepath.Dir(paths.Lock), "."+filepath.Base(paths.Lock)+".transaction")
}

func projectWriterLockPath(paths ProjectPaths) string {
	return filepath.Join(filepath.Dir(paths.Lock), "."+filepath.Base(paths.Lock)+".writer.lock")
}

func recoverProjectTransaction(paths ProjectPaths) error {
	data, err := os.ReadFile(projectTransactionPath(paths))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	var marker transaction
	if err := image.StrictDecode(data, &marker); err != nil {
		return fmt.Errorf("INGOT-BUILD-LOCK-TRANSACTION: %w", err)
	}
	desired, err := decodeTransactionValue(marker.Desired)
	if err != nil {
		return err
	}
	lock, err := decodeTransactionValue(marker.Lock)
	if err != nil {
		return err
	}
	if err := image.AtomicWriteUserFile(paths.Recipe, desired, 0o644); err != nil {
		return err
	}
	if err := image.AtomicWriteUserFile(paths.Lock, lock, 0o644); err != nil {
		return err
	}
	return os.Remove(projectTransactionPath(paths))
}

func (home *Home) ResolveProject(ctx context.Context, options RecipeOptions, resolveOptions builder.ResolveOptions) (*builder.Lock, ProjectPaths, error) {
	paths, release, err := prepareProject(ctx, options)
	if err != nil {
		return nil, ProjectPaths{}, err
	}
	defer release()
	lock, err := home.resolveProjectUnlocked(ctx, paths, resolveOptions)
	return lock, paths, err
}

func (home *Home) resolveProjectUnlocked(ctx context.Context, paths ProjectPaths, resolveOptions builder.ResolveOptions) (*builder.Lock, error) {
	desired, err := builder.ParseDesired(paths.Recipe)
	if err != nil {
		return nil, err
	}
	if _, err := builder.LoadBuilderConfig(home.BuilderConfigPath()); err != nil {
		return nil, err
	}
	if resolveOptions.GOMODCACHE == "" {
		resolveOptions.GOMODCACHE = filepath.Join(home.Root, "cache", "gomod")
	}
	lock, err := builder.Resolve(ctx, desired, resolveOptions)
	if err != nil {
		return nil, err
	}
	data, err := lock.MarshalTOML()
	if err != nil {
		return nil, err
	}
	if err := image.AtomicWriteUserFile(paths.Lock, data, 0o644); err != nil {
		return nil, err
	}
	return lock, nil
}

func (home *Home) BuildProject(ctx context.Context, options RecipeOptions, resolveOptions builder.ResolveOptions) (*builder.BuildResult, ProjectPaths, error) {
	paths, releaseProject, err := prepareProject(ctx, options)
	if err != nil {
		return nil, ProjectPaths{}, err
	}
	defer releaseProject()
	desired, err := builder.ParseDesired(paths.Recipe)
	if err != nil {
		return nil, paths, err
	}
	lock, lockErr := builder.ParseLock(paths.Lock)
	digest, digestErr := desired.Digest()
	if digestErr != nil {
		return nil, paths, digestErr
	}
	stale := lockErr != nil || lock.PluginsDigest != digest
	if !stale {
		for _, replacement := range lock.Replacements {
			calculated, sourceErr := builder.ModuleSourceDigest(replacement.DevPath)
			if pluginIsDev(lock, replacement.ModulePath) {
				calculated, sourceErr = builder.DevSourceDigest(replacement.DevPath)
			}
			if sourceErr != nil || calculated != replacement.ContentSHA256 {
				stale = true
				break
			}
		}
	}
	if stale {
		if options.Locked {
			if lockErr != nil {
				return nil, paths, fmt.Errorf("INGOT-BUILD-LOCK-REQUIRED: %w", lockErr)
			}
			return nil, paths, fmt.Errorf("INGOT-BUILD-LOCK-STALE: recipe digest changed")
		}
		lock, err = home.resolveProjectUnlocked(ctx, paths, resolveOptions)
		if err != nil {
			return nil, paths, err
		}
	} else if options.Locked {
		for _, replacement := range lock.Replacements {
			calculated, err := builder.ModuleSourceDigest(replacement.DevPath)
			if pluginIsDev(lock, replacement.ModulePath) {
				calculated, err = builder.DevSourceDigest(replacement.DevPath)
			}
			if err != nil || calculated != replacement.ContentSHA256 {
				return nil, paths, fmt.Errorf("INGOT-BUILD-LOCK-STALE: source %s changed", replacement.ModulePath)
			}
		}
	}
	release, err := home.acquire(ctx)
	if err != nil {
		return nil, paths, err
	}
	defer release()
	result, err := builder.Build(ctx, desired, lock, builder.BuildOptions{Home: home.Root, GOMODCACHE: filepath.Join(home.Root, "cache", "gomod")})
	if err != nil {
		return nil, paths, err
	}
	if options.Tag != "" {
		source, err := image.ParseNamedReference(options.Tag)
		if err != nil {
			return nil, paths, err
		}
		catalog, err := image.LoadCatalog(home.CatalogPath())
		if err != nil {
			return nil, paths, err
		}
		binding := image.Binding{Source: &source, Target: result.Target, ImageID: result.ImageID, ArtifactDigest: result.ArtifactDigest}
		if err := catalog.Set(source, binding); err != nil {
			return nil, paths, err
		}
		if err := image.WriteCatalog(home.CatalogPath(), catalog); err != nil {
			return nil, paths, err
		}
	}
	return result, paths, nil
}

func pluginIsDev(lock *builder.Lock, id string) bool {
	for _, plugin := range lock.Plugins {
		if plugin.ID == id {
			return plugin.SourceKind == "dev"
		}
	}
	return false
}

func isMissing(err error) bool { return os.IsNotExist(err) || os.IsNotExist(errors.Unwrap(err)) }
