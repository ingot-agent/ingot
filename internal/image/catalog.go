package image

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
)

type Variant struct {
	Target         Target `json:"target"`
	ImageID        string `json:"image_id"`
	ArtifactDigest string `json:"artifact_digest"`
}

type TagEntry struct {
	Name     string    `json:"name"`
	Tag      string    `json:"tag"`
	Variants []Variant `json:"variants"`
}

type Pin struct {
	ImageID string `json:"image_id"`
}

type Catalog struct {
	CatalogVersion int        `json:"catalog_version"`
	Tags           []TagEntry `json:"tags"`
	Pins           []Pin      `json:"pins"`
}

func NewCatalog() Catalog { return Catalog{CatalogVersion: 1, Tags: []TagEntry{}, Pins: []Pin{}} }

func LoadCatalog(path string) (Catalog, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Catalog{}, err
	}
	var catalog Catalog
	if err := StrictDecode(data, &catalog); err != nil {
		return Catalog{}, fmt.Errorf("INGOT-IMAGE-CATALOG-SCHEMA: %w", err)
	}
	if err := catalog.Validate(); err != nil {
		return Catalog{}, err
	}
	return catalog, nil
}

func (catalog *Catalog) Validate() error {
	if catalog.CatalogVersion != 1 || catalog.Tags == nil || catalog.Pins == nil {
		return fmt.Errorf("INGOT-IMAGE-CATALOG-VERSION: invalid catalog")
	}
	previousTag := ""
	for _, entry := range catalog.Tags {
		if err := ValidateName(entry.Name); err != nil {
			return err
		}
		if err := ValidateTag(entry.Tag); err != nil {
			return err
		}
		key := entry.Name + ":" + entry.Tag
		if previousTag != "" && key <= previousTag {
			return fmt.Errorf("INGOT-IMAGE-CATALOG-ORDER: tags are not sorted and unique")
		}
		previousTag = key
		if entry.Variants == nil || len(entry.Variants) == 0 {
			return fmt.Errorf("INGOT-IMAGE-CATALOG-SCHEMA: tag has no variants")
		}
		previousPlatform := ""
		for _, variant := range entry.Variants {
			if err := variant.Target.Validate(); err != nil {
				return err
			}
			platform := variant.Target.Platform()
			if previousPlatform != "" && platform <= previousPlatform {
				return fmt.Errorf("INGOT-IMAGE-CATALOG-ORDER: variants are not sorted and unique")
			}
			previousPlatform = platform
			if !ValidDigest(variant.ImageID) || !ValidDigest(variant.ArtifactDigest) {
				return fmt.Errorf("INGOT-IMAGE-CATALOG-DIGEST: invalid digest")
			}
		}
	}
	previousPin := ""
	for _, pin := range catalog.Pins {
		if !ValidDigest(pin.ImageID) || (previousPin != "" && pin.ImageID <= previousPin) {
			return fmt.Errorf("INGOT-IMAGE-CATALOG-ORDER: pins are invalid or unsorted")
		}
		previousPin = pin.ImageID
	}
	return nil
}

func (catalog *Catalog) Normalize() {
	for i := range catalog.Tags {
		sort.Slice(catalog.Tags[i].Variants, func(a, b int) bool {
			return catalog.Tags[i].Variants[a].Target.Platform() < catalog.Tags[i].Variants[b].Target.Platform()
		})
	}
	sort.Slice(catalog.Tags, func(i, j int) bool {
		if catalog.Tags[i].Name != catalog.Tags[j].Name {
			return catalog.Tags[i].Name < catalog.Tags[j].Name
		}
		return catalog.Tags[i].Tag < catalog.Tags[j].Tag
	})
	sort.Slice(catalog.Pins, func(i, j int) bool { return catalog.Pins[i].ImageID < catalog.Pins[j].ImageID })
}

func (catalog *Catalog) Resolve(ref Reference, defaultGOOS, defaultGOARCH string) (Binding, error) {
	if ref.Digest != "" {
		return Binding{ImageID: ref.Digest}, nil
	}
	goos, goarch := ref.GOOS, ref.GOARCH
	if goos == "" {
		goos, goarch = defaultGOOS, defaultGOARCH
	}
	for _, entry := range catalog.Tags {
		if entry.Name == ref.Name && entry.Tag == ref.Tag {
			for _, variant := range entry.Variants {
				if variant.Target.SamePlatform(goos, goarch) {
					source := &Source{Name: ref.Name, Tag: ref.Tag}
					return Binding{Source: source, Target: variant.Target, ImageID: variant.ImageID, ArtifactDigest: variant.ArtifactDigest}, nil
				}
			}
			available := []string{}
			for _, variant := range entry.Variants {
				available = append(available, variant.Target.Platform())
			}
			return Binding{}, fmt.Errorf("INGOT-IMAGE-TARGET-NOT-FOUND: %s/%s; available %v", goos, goarch, available)
		}
	}
	return Binding{}, fmt.Errorf("INGOT-IMAGE-REF-NOT-FOUND: %s:%s", ref.Name, ref.Tag)
}

func (catalog *Catalog) Set(source Source, binding Binding) error {
	if err := ValidateName(source.Name); err != nil {
		return err
	}
	if err := ValidateTag(source.Tag); err != nil {
		return err
	}
	if err := binding.Validate(); err != nil {
		return err
	}
	for i := range catalog.Tags {
		if catalog.Tags[i].Name == source.Name && catalog.Tags[i].Tag == source.Tag {
			for j := range catalog.Tags[i].Variants {
				if catalog.Tags[i].Variants[j].Target.Platform() == binding.Target.Platform() {
					catalog.Tags[i].Variants[j] = Variant{Target: binding.Target, ImageID: binding.ImageID, ArtifactDigest: binding.ArtifactDigest}
					catalog.Normalize()
					return nil
				}
			}
			catalog.Tags[i].Variants = append(catalog.Tags[i].Variants, Variant{Target: binding.Target, ImageID: binding.ImageID, ArtifactDigest: binding.ArtifactDigest})
			catalog.Normalize()
			return nil
		}
	}
	catalog.Tags = append(catalog.Tags, TagEntry{Name: source.Name, Tag: source.Tag, Variants: []Variant{{Target: binding.Target, ImageID: binding.ImageID, ArtifactDigest: binding.ArtifactDigest}}})
	catalog.Normalize()
	return nil
}

func (catalog *Catalog) Untag(source Source) bool {
	for i, entry := range catalog.Tags {
		if entry.Name == source.Name && entry.Tag == source.Tag {
			catalog.Tags = append(catalog.Tags[:i], catalog.Tags[i+1:]...)
			return true
		}
	}
	return false
}
func (catalog *Catalog) Pin(imageID string) error {
	if !ValidDigest(imageID) {
		return fmt.Errorf("invalid image digest")
	}
	for _, pin := range catalog.Pins {
		if pin.ImageID == imageID {
			return nil
		}
	}
	catalog.Pins = append(catalog.Pins, Pin{ImageID: imageID})
	catalog.Normalize()
	return nil
}
func (catalog *Catalog) Unpin(imageID string) {
	for i, pin := range catalog.Pins {
		if pin.ImageID == imageID {
			catalog.Pins = append(catalog.Pins[:i], catalog.Pins[i+1:]...)
			return
		}
	}
}

func WriteCatalog(path string, catalog Catalog) error {
	catalog.Normalize()
	if err := catalog.Validate(); err != nil {
		return err
	}
	data, err := json.MarshalIndent(catalog, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return AtomicWrite(path, data, 0o600)
}

func AtomicWrite(path string, data []byte, mode os.FileMode) error {
	directory := filepath.Dir(path)
	temporary, err := os.CreateTemp(directory, ".tmp-")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(mode); err != nil {
		temporary.Close()
		return err
	}
	if _, err := temporary.Write(data); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := replaceFile(temporaryPath, path); err != nil {
		return err
	}
	return syncDirectory(directory)
}

// AtomicWriteUserFile preserves an existing file's permissions and lets the
// process umask restrict the requested mode when the destination is new.
func AtomicWriteUserFile(path string, data []byte, mode os.FileMode) error {
	directory := filepath.Dir(path)
	existingMode := os.FileMode(0)
	if info, err := os.Lstat(path); err == nil {
		if !info.Mode().IsRegular() {
			return fmt.Errorf("destination %s is not a regular file", path)
		}
		existingMode = info.Mode().Perm()
	} else if !os.IsNotExist(err) {
		return err
	}
	var temporary *os.File
	for attempts := 0; attempts < 16; attempts++ {
		var suffix [8]byte
		if _, err := rand.Read(suffix[:]); err != nil {
			return err
		}
		temporaryPath := filepath.Join(directory, ".tmp-"+hex.EncodeToString(suffix[:]))
		file, err := os.OpenFile(temporaryPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
		if os.IsExist(err) {
			continue
		}
		if err != nil {
			return err
		}
		temporary = file
		break
	}
	if temporary == nil {
		return fmt.Errorf("create temporary file in %s: too many collisions", directory)
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if existingMode != 0 {
		if err := temporary.Chmod(existingMode); err != nil {
			temporary.Close()
			return err
		}
	}
	if _, err := temporary.Write(data); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := replaceFile(temporaryPath, path); err != nil {
		return err
	}
	return syncDirectory(directory)
}
