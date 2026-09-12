package home

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/ingot-agent/ingot/internal/image"
	"github.com/ingot-agent/ingot/internal/layout"
	processmgr "github.com/ingot-agent/ingot/internal/process"
)

type ImageView struct {
	ImageID        string         `json:"image_id"`
	ArtifactDigest string         `json:"artifact_digest"`
	Target         image.Target   `json:"target"`
	Tags           []image.Source `json:"tags"`
	Pinned         bool           `json:"pinned"`
}

func (home *Home) ImageInspect(ctx context.Context, value string) ([]ImageView, error) {
	release, err := home.acquire(ctx)
	if err != nil {
		return nil, err
	}
	defer release()
	ref, err := image.ParseReference(value)
	if err != nil {
		return nil, err
	}
	if ref.Digest != "" || ref.GOOS != "" {
		binding, verified, err := home.resolveImageUnlocked(value, runtime.GOOS, runtime.GOARCH)
		if err != nil {
			return nil, err
		}
		catalog, err := image.LoadCatalog(home.CatalogPath())
		if err != nil {
			return nil, err
		}
		view := ImageView{ImageID: binding.ImageID, ArtifactDigest: binding.ArtifactDigest, Target: verified.Manifest.Target, Tags: []image.Source{}}
		if err := decorateImageView(catalog, &view); err != nil {
			return nil, err
		}
		return []ImageView{view}, nil
	}
	catalog, err := image.LoadCatalog(home.CatalogPath())
	if err != nil {
		return nil, err
	}
	result := []ImageView{}
	for _, entry := range catalog.Tags {
		if entry.Name != ref.Name || entry.Tag != ref.Tag {
			continue
		}
		for _, variant := range entry.Variants {
			verified, err := image.Verify(home.imageDirectory(variant.ImageID), variant.ImageID, nil)
			if err != nil {
				return nil, err
			}
			if verified.Manifest.ArtifactDigest != variant.ArtifactDigest || !verified.Manifest.Target.Equal(variant.Target) {
				return nil, fmt.Errorf("INGOT-IMAGE-CATALOG-MISMATCH: %s", variant.ImageID)
			}
			view := ImageView{ImageID: variant.ImageID, ArtifactDigest: variant.ArtifactDigest, Target: variant.Target, Tags: []image.Source{}}
			if err := decorateImageView(catalog, &view); err != nil {
				return nil, err
			}
			result = append(result, view)
		}
		return result, nil
	}
	return nil, fmt.Errorf("INGOT-IMAGE-REF-NOT-FOUND: %s", value)
}

func decorateImageView(catalog image.Catalog, view *ImageView) error {
	for _, entry := range catalog.Tags {
		for _, variant := range entry.Variants {
			if variant.ImageID == view.ImageID {
				if variant.ArtifactDigest != view.ArtifactDigest || !variant.Target.Equal(view.Target) {
					return fmt.Errorf("INGOT-IMAGE-CATALOG-MISMATCH: %s", view.ImageID)
				}
				view.Tags = append(view.Tags, image.Source{Name: entry.Name, Tag: entry.Tag})
				break
			}
		}
	}
	for _, pin := range catalog.Pins {
		if pin.ImageID == view.ImageID {
			view.Pinned = true
			break
		}
	}
	sort.Slice(view.Tags, func(i, j int) bool {
		if view.Tags[i].Name != view.Tags[j].Name {
			return view.Tags[i].Name < view.Tags[j].Name
		}
		return view.Tags[i].Tag < view.Tags[j].Tag
	})
	return nil
}

func (home *Home) resolveImageUnlocked(value, defaultGOOS, defaultGOARCH string) (image.Binding, image.Verified, error) {
	ref, err := image.ParseReference(value)
	if err != nil {
		return image.Binding{}, image.Verified{}, err
	}
	catalog, err := image.LoadCatalog(home.CatalogPath())
	if err != nil {
		return image.Binding{}, image.Verified{}, err
	}
	binding, err := catalog.Resolve(ref, defaultGOOS, defaultGOARCH)
	if err != nil {
		return image.Binding{}, image.Verified{}, err
	}
	verified, err := image.Verify(home.imageDirectory(binding.ImageID), binding.ImageID, nil)
	if err != nil {
		return image.Binding{}, image.Verified{}, err
	}
	if binding.Target.GOOS == "" {
		binding.Target, binding.ArtifactDigest = verified.Manifest.Target, verified.Manifest.ArtifactDigest
	}
	if binding.ArtifactDigest != verified.Manifest.ArtifactDigest || !binding.Target.Equal(verified.Manifest.Target) {
		return image.Binding{}, image.Verified{}, fmt.Errorf("INGOT-IMAGE-CATALOG-MISMATCH: catalog binding differs from image manifest")
	}
	return binding, verified, nil
}

func (home *Home) ResolveImage(ctx context.Context, value string) (image.Binding, image.Verified, error) {
	release, err := home.acquire(ctx)
	if err != nil {
		return image.Binding{}, image.Verified{}, err
	}
	defer release()
	return home.resolveImageUnlocked(value, runtime.GOOS, runtime.GOARCH)
}

func (home *Home) ImageList(ctx context.Context) ([]ImageView, error) {
	release, err := home.acquire(ctx)
	if err != nil {
		return nil, err
	}
	defer release()
	catalog, err := image.LoadCatalog(home.CatalogPath())
	if err != nil {
		return nil, err
	}
	views := map[string]*ImageView{}
	verifiedImages := map[string]image.Verified{}
	for _, tag := range catalog.Tags {
		for _, variant := range tag.Variants {
			verified, ok := verifiedImages[variant.ImageID]
			if !ok {
				verified, err = image.Verify(home.imageDirectory(variant.ImageID), variant.ImageID, nil)
				if err != nil {
					return nil, err
				}
				verifiedImages[variant.ImageID] = verified
			}
			if verified.Manifest.ArtifactDigest != variant.ArtifactDigest || !verified.Manifest.Target.Equal(variant.Target) {
				return nil, fmt.Errorf("INGOT-IMAGE-CATALOG-MISMATCH: %s", variant.ImageID)
			}
			view := views[variant.ImageID]
			if view == nil {
				view = &ImageView{ImageID: variant.ImageID, ArtifactDigest: variant.ArtifactDigest, Target: variant.Target, Tags: []image.Source{}}
				views[variant.ImageID] = view
			}
			view.Tags = append(view.Tags, image.Source{Name: tag.Name, Tag: tag.Tag})
		}
	}
	for _, pin := range catalog.Pins {
		view := views[pin.ImageID]
		if view == nil {
			verified, err := image.Verify(home.imageDirectory(pin.ImageID), pin.ImageID, nil)
			if err != nil {
				return nil, err
			}
			view = &ImageView{ImageID: pin.ImageID, ArtifactDigest: verified.Manifest.ArtifactDigest, Target: verified.Manifest.Target, Tags: []image.Source{}}
			views[pin.ImageID] = view
		}
		view.Pinned = true
	}
	entries, err := os.ReadDir(filepath.Join(home.Root, "images"))
	if err != nil {
		return nil, err
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		if strings.HasPrefix(entry.Name(), ".staging-") {
			continue
		}
		id := layout.ImageIDFromDirectoryName(entry.Name(), runtime.GOOS)
		if !image.ValidDigest(id) {
			return nil, fmt.Errorf("INGOT-IMAGE-STORE-ENTRY: invalid image directory %s", entry.Name())
		}
		if views[id] != nil {
			continue
		}
		verified, err := image.Verify(home.imageDirectory(id), id, nil)
		if err != nil {
			return nil, err
		}
		views[id] = &ImageView{ImageID: id, ArtifactDigest: verified.Manifest.ArtifactDigest, Target: verified.Manifest.Target, Tags: []image.Source{}}
	}
	result := make([]ImageView, 0, len(views))
	for _, view := range views {
		sort.Slice(view.Tags, func(i, j int) bool {
			if view.Tags[i].Name != view.Tags[j].Name {
				return view.Tags[i].Name < view.Tags[j].Name
			}
			return view.Tags[i].Tag < view.Tags[j].Tag
		})
		result = append(result, *view)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].ImageID < result[j].ImageID })
	return result, nil
}

func (home *Home) ImageTag(ctx context.Context, sourceValue, destination string) (image.Binding, error) {
	release, err := home.acquire(ctx)
	if err != nil {
		return image.Binding{}, err
	}
	defer release()
	binding, _, err := home.resolveImageUnlocked(sourceValue, runtime.GOOS, runtime.GOARCH)
	if err != nil {
		return image.Binding{}, err
	}
	source, err := image.ParseNamedReference(destination)
	if err != nil {
		return image.Binding{}, err
	}
	binding.Source = &source
	catalog, err := image.LoadCatalog(home.CatalogPath())
	if err != nil {
		return image.Binding{}, err
	}
	if err := catalog.Set(source, binding); err != nil {
		return image.Binding{}, err
	}
	return binding, image.WriteCatalog(home.CatalogPath(), catalog)
}

func (home *Home) ImageUntag(ctx context.Context, value string) (bool, error) {
	release, err := home.acquire(ctx)
	if err != nil {
		return false, err
	}
	defer release()
	source, err := image.ParseNamedReference(value)
	if err != nil {
		return false, err
	}
	catalog, err := image.LoadCatalog(home.CatalogPath())
	if err != nil {
		return false, err
	}
	changed := catalog.Untag(source)
	if !changed {
		return false, nil
	}
	return true, image.WriteCatalog(home.CatalogPath(), catalog)
}

func (home *Home) ImagePin(ctx context.Context, value string, pin bool) (string, error) {
	release, err := home.acquire(ctx)
	if err != nil {
		return "", err
	}
	defer release()
	binding, _, err := home.resolveImageUnlocked(value, runtime.GOOS, runtime.GOARCH)
	if err != nil {
		return "", err
	}
	catalog, err := image.LoadCatalog(home.CatalogPath())
	if err != nil {
		return "", err
	}
	if pin {
		err = catalog.Pin(binding.ImageID)
	} else {
		catalog.Unpin(binding.ImageID)
	}
	if err == nil {
		err = image.WriteCatalog(home.CatalogPath(), catalog)
	}
	return binding.ImageID, err
}

func (home *Home) ImageExport(ctx context.Context, value, target, output string) error {
	release, err := home.acquire(ctx)
	if err != nil {
		return err
	}
	defer release()
	goos, goarch := runtime.GOOS, runtime.GOARCH
	if target != "" {
		ref, parseErr := image.ParseReference("x:t@" + target)
		if parseErr != nil {
			return parseErr
		}
		goos, goarch = ref.GOOS, ref.GOARCH
	}
	binding, verified, err := home.resolveImageUnlocked(value, goos, goarch)
	if err != nil {
		return err
	}
	return image.ExportBundle(verified.Directory, binding.Source, output)
}

func (home *Home) ImageImport(ctx context.Context, path string, noTag bool) (image.ImportResult, error) {
	release, err := home.acquire(ctx)
	if err != nil {
		return image.ImportResult{}, err
	}
	defer release()
	journalPath := ""
	result, err := image.ImportBundleWithPrepare(filepath.Join(home.Root, "images"), path, func(candidate image.ImportResult) error {
		if noTag || candidate.Tag == nil {
			return nil
		}
		journal := homeTransaction{Kind: "image_import", ImageImport: &imageImportTransaction{Binding: candidate.Binding, Tag: *candidate.Tag}}
		var err error
		journalPath, err = home.writeHomeTransaction(journal)
		return err
	})
	if err != nil {
		if journalPath != "" {
			if recoverErr := home.recoverHomeTransaction(journalPath); recoverErr != nil {
				return image.ImportResult{}, fmt.Errorf("%w; transaction recovery failed: %v", err, recoverErr)
			}
		}
		return image.ImportResult{}, err
	}
	if result.Tag != nil && !noTag {
		catalog, err := image.LoadCatalog(home.CatalogPath())
		if err != nil {
			return image.ImportResult{}, err
		}
		if err := catalog.Set(*result.Tag, result.Binding); err != nil {
			return image.ImportResult{}, err
		}
		if err := image.WriteCatalog(home.CatalogPath(), catalog); err != nil {
			return image.ImportResult{}, err
		}
	}
	if journalPath != "" {
		if err := home.removeHomeTransaction(journalPath); err != nil {
			return image.ImportResult{}, err
		}
	}
	return result, nil
}

func (home *Home) imageReferencesUnlocked(ctx context.Context) (map[string][]string, error) {
	catalog, err := image.LoadCatalog(home.CatalogPath())
	if err != nil {
		return nil, err
	}
	references := map[string][]string{}
	for _, tag := range catalog.Tags {
		for _, variant := range tag.Variants {
			if err := home.verifyConcreteImage(variant.ImageID, variant.ArtifactDigest, variant.Target); err != nil {
				return nil, err
			}
			references[variant.ImageID] = append(references[variant.ImageID], "tag:"+tag.Name+":"+tag.Tag+"@"+variant.Target.Platform())
		}
	}
	for _, pin := range catalog.Pins {
		references[pin.ImageID] = append(references[pin.ImageID], "pin")
	}
	runtimes, err := home.registry().List()
	if err != nil {
		return nil, err
	}
	for _, entry := range runtimes {
		if err := home.verifyConcreteImage(entry.DesiredImage.ImageID, entry.DesiredImage.ArtifactDigest, entry.DesiredImage.Target); err != nil {
			return nil, err
		}
		references[entry.DesiredImage.ImageID] = append(references[entry.DesiredImage.ImageID], "runtime:"+entry.Name+":desired")
		if entry.RollbackImage != nil {
			if err := home.verifyConcreteImage(entry.RollbackImage.ImageID, entry.RollbackImage.ArtifactDigest, entry.RollbackImage.Target); err != nil {
				return nil, err
			}
			references[entry.RollbackImage.ImageID] = append(references[entry.RollbackImage.ImageID], "runtime:"+entry.Name+":rollback")
		}
		observation, err := processmgr.Reconcile(ctx, home.registry().RuntimeHome(entry.Name))
		if err != nil {
			return nil, err
		}
		if observation.State == "external" {
			return nil, fmt.Errorf("INGOT-GC-REFERENCE-EXTERNAL: runtime %s has an unknown external process", entry.Name)
		}
		if observation.Process != nil {
			if err := home.verifyConcreteImage(observation.Process.ImageID, observation.Process.ArtifactDigest, observation.Process.Target); err != nil {
				return nil, err
			}
			references[observation.Process.ImageID] = append(references[observation.Process.ImageID], "process:"+observation.Process.ProcessID)
		}
	}
	for id := range references {
		if _, err := image.Verify(home.imageDirectory(id), id, nil); err != nil {
			return nil, fmt.Errorf("INGOT-GC-REFERENCE-CORRUPT: %s: %w", id, err)
		}
		sort.Strings(references[id])
	}
	return references, nil
}

func (home *Home) verifyConcreteImage(imageID, artifactDigest string, target image.Target) error {
	verified, err := image.Verify(home.imageDirectory(imageID), imageID, nil)
	if err != nil {
		return fmt.Errorf("INGOT-GC-REFERENCE-CORRUPT: %s: %w", imageID, err)
	}
	if verified.Manifest.ArtifactDigest != artifactDigest || !verified.Manifest.Target.Equal(target) {
		return fmt.Errorf("INGOT-GC-REFERENCE-CORRUPT: binding for %s differs from manifest", imageID)
	}
	return nil
}

func (home *Home) ImageRemove(ctx context.Context, imageID string) (bool, []string, error) {
	if !image.ValidDigest(imageID) {
		return false, nil, fmt.Errorf("INGOT-IMAGE-REF-DIGEST: remove requires a digest")
	}
	release, err := home.acquire(ctx)
	if err != nil {
		return false, nil, err
	}
	defer release()
	references, err := home.imageReferencesUnlocked(ctx)
	if err != nil {
		return false, nil, err
	}
	if roots := references[imageID]; len(roots) > 0 {
		return false, roots, fmt.Errorf("INGOT-GC-REFERENCE-IN-USE: %s is referenced by %v", imageID, roots)
	}
	directory := home.imageDirectory(imageID)
	if _, err := image.Verify(directory, imageID, nil); err != nil {
		return false, nil, err
	}
	if err := os.RemoveAll(directory); err != nil {
		return false, nil, err
	}
	if err := syncDirectory(filepath.Join(home.Root, "images")); err != nil {
		return false, nil, err
	}
	return true, nil, nil
}

func (home *Home) GC(ctx context.Context, keepRecent int) ([]string, error) {
	if keepRecent < 0 {
		return nil, fmt.Errorf("keep-recent must be non-negative")
	}
	release, err := home.acquire(ctx)
	if err != nil {
		return nil, err
	}
	defer release()
	references, err := home.imageReferencesUnlocked(ctx)
	if err != nil {
		return nil, err
	}
	type candidate struct {
		id, path string
		modified time.Time
	}
	candidates := []candidate{}
	entries, err := os.ReadDir(filepath.Join(home.Root, "images"))
	if err != nil {
		return nil, err
	}
	for _, entry := range entries {
		path := filepath.Join(home.Root, "images", entry.Name())
		if entry.IsDir() && strings.HasPrefix(entry.Name(), ".staging-") {
			if err := os.RemoveAll(path); err != nil {
				return nil, err
			}
			continue
		}
		if !entry.IsDir() {
			continue
		}
		id := layout.ImageIDFromDirectoryName(entry.Name(), runtime.GOOS)
		if !image.ValidDigest(id) {
			continue
		}
		if _, err := image.Verify(path, id, nil); err != nil {
			return nil, fmt.Errorf("INGOT-GC-REFERENCE-CORRUPT: %s: %w", path, err)
		}
		info, err := entry.Info()
		if err != nil {
			return nil, err
		}
		candidates = append(candidates, candidate{id: id, path: path, modified: info.ModTime()})
	}
	sort.Slice(candidates, func(i, j int) bool { return candidates[i].modified.After(candidates[j].modified) })
	for i := 0; i < keepRecent && i < len(candidates); i++ {
		references[candidates[i].id] = append(references[candidates[i].id], "recent")
	}
	removed := []string{}
	for _, candidate := range candidates {
		if len(references[candidate.id]) > 0 {
			continue
		}
		if err := os.RemoveAll(candidate.path); err != nil {
			return removed, err
		}
		removed = append(removed, candidate.id)
	}
	if err := syncDirectory(filepath.Join(home.Root, "images")); err != nil {
		return removed, err
	}
	sort.Strings(removed)
	return removed, nil
}
