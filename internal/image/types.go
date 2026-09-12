package image

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

var (
	digestPattern   = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
	segmentPattern  = regexp.MustCompile(`^[a-z][a-z0-9]*(?:[._-][a-z0-9]+)*$`)
	tagPattern      = regexp.MustCompile(`^[a-z0-9_][a-z0-9_.-]{0,127}$`)
	platformPattern = regexp.MustCompile(`^[a-z0-9]+$`)
)

type TargetKey struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

type Target struct {
	GOOS         string      `json:"goos"`
	GOARCH       string      `json:"goarch"`
	CGOEnabled   bool        `json:"cgo_enabled"`
	GOExperiment []string    `json:"goexperiment"`
	Tuning       []TargetKey `json:"tuning"`
}

func (target Target) Validate() error {
	if !platformPattern.MatchString(target.GOOS) || !platformPattern.MatchString(target.GOARCH) {
		return fmt.Errorf("INGOT-IMAGE-TARGET-GRAMMAR: invalid target %q", target.Platform())
	}
	if target.GOExperiment == nil || target.Tuning == nil {
		return fmt.Errorf("INGOT-IMAGE-TARGET-SCHEMA: goexperiment and tuning must be present")
	}
	if !sort.StringsAreSorted(target.GOExperiment) {
		return fmt.Errorf("INGOT-IMAGE-TARGET-ORDER: goexperiment must be sorted")
	}
	for index := 1; index < len(target.GOExperiment); index++ {
		if target.GOExperiment[index] == target.GOExperiment[index-1] {
			return fmt.Errorf("INGOT-IMAGE-TARGET-ORDER: goexperiment must be unique")
		}
	}
	previous := ""
	for _, item := range target.Tuning {
		if item.Key == "" || item.Value == "" || (previous != "" && item.Key <= previous) {
			return fmt.Errorf("INGOT-IMAGE-TARGET-ORDER: tuning must contain sorted unique non-empty keys")
		}
		previous = item.Key
	}
	return nil
}

func (target Target) Platform() string { return target.GOOS + "/" + target.GOARCH }
func (target Target) Equal(other Target) bool {
	if target.GOOS != other.GOOS || target.GOARCH != other.GOARCH || target.CGOEnabled != other.CGOEnabled || len(target.GOExperiment) != len(other.GOExperiment) || len(target.Tuning) != len(other.Tuning) {
		return false
	}
	for index := range target.GOExperiment {
		if target.GOExperiment[index] != other.GOExperiment[index] {
			return false
		}
	}
	for index := range target.Tuning {
		if target.Tuning[index] != other.Tuning[index] {
			return false
		}
	}
	return true
}
func (target Target) SamePlatform(goos, goarch string) bool {
	return target.GOOS == goos && target.GOARCH == goarch
}

type Source struct {
	Name string `json:"name"`
	Tag  string `json:"tag"`
}

type Binding struct {
	Source         *Source `json:"source"`
	Target         Target  `json:"target"`
	ImageID        string  `json:"image_id"`
	ArtifactDigest string  `json:"artifact_digest"`
}

func (binding Binding) Validate() error {
	if binding.Source != nil {
		if err := ValidateName(binding.Source.Name); err != nil {
			return err
		}
		if err := ValidateTag(binding.Source.Tag); err != nil {
			return err
		}
	}
	if err := binding.Target.Validate(); err != nil {
		return err
	}
	if !ValidDigest(binding.ImageID) || !ValidDigest(binding.ArtifactDigest) {
		return fmt.Errorf("INGOT-IMAGE-REF-DIGEST: invalid image or artifact digest")
	}
	return nil
}

func ValidDigest(value string) bool { return digestPattern.MatchString(value) }

func ValidateName(value string) error {
	if len(value) == 0 || len(value) > 128 {
		return fmt.Errorf("INGOT-IMAGE-REF-NAME: image name length must be 1..128 bytes")
	}
	segments := strings.Split(value, "/")
	if len(segments) < 1 || len(segments) > 4 {
		return fmt.Errorf("INGOT-IMAGE-REF-NAME: image name must contain 1..4 segments")
	}
	for _, segment := range segments {
		if len(segment) > 64 || !segmentPattern.MatchString(segment) {
			return fmt.Errorf("INGOT-IMAGE-REF-NAME: invalid image name %q", value)
		}
	}
	return nil
}

func ValidateRuntimeName(value string) error {
	if len(value) == 0 || len(value) > 64 || !segmentPattern.MatchString(value) {
		return fmt.Errorf("INGOT-RUNTIME-REGISTRY-NAME: invalid runtime name %q", value)
	}
	return nil
}

func ValidateTag(value string) error {
	if !tagPattern.MatchString(value) {
		return fmt.Errorf("INGOT-IMAGE-REF-TAG: invalid image tag %q", value)
	}
	return nil
}

type Reference struct {
	Digest string
	Name   string
	Tag    string
	GOOS   string
	GOARCH string
}

func ParseReference(value string) (Reference, error) {
	if ValidDigest(value) {
		return Reference{Digest: value}, nil
	}
	base, suffix, hasSuffix := value, "", false
	if index := strings.LastIndex(value, "@"); index >= 0 {
		base, suffix, hasSuffix = value[:index], value[index+1:], true
	}
	colon := strings.LastIndex(base, ":")
	if colon <= 0 || colon == len(base)-1 {
		return Reference{}, fmt.Errorf("INGOT-IMAGE-REF-GRAMMAR: named image reference requires explicit name:tag")
	}
	ref := Reference{Name: base[:colon], Tag: base[colon+1:]}
	if err := ValidateName(ref.Name); err != nil {
		return Reference{}, err
	}
	if err := ValidateTag(ref.Tag); err != nil {
		return Reference{}, err
	}
	if hasSuffix {
		parts := strings.Split(suffix, "/")
		if len(parts) != 2 || !platformPattern.MatchString(parts[0]) || !platformPattern.MatchString(parts[1]) {
			return Reference{}, fmt.Errorf("INGOT-IMAGE-REF-TARGET: invalid target suffix %q", suffix)
		}
		ref.GOOS, ref.GOARCH = parts[0], parts[1]
	}
	return ref, nil
}

func ParseNamedReference(value string) (Source, error) {
	ref, err := ParseReference(value)
	if err != nil {
		return Source{}, err
	}
	if ref.Digest != "" || ref.GOOS != "" {
		return Source{}, fmt.Errorf("INGOT-IMAGE-REF-GRAMMAR: expected name:tag without target")
	}
	return Source{Name: ref.Name, Tag: ref.Tag}, nil
}
