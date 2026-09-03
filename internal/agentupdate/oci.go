package agentupdate

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
)

type ociIndexDocument struct {
	SchemaVersion int                  `json:"schemaVersion"`
	MediaType     string               `json:"mediaType"`
	Manifests     []ociIndexDescriptor `json:"manifests"`
}

type ociIndexDescriptor struct {
	MediaType string `json:"mediaType"`
	Digest    string `json:"digest"`
	Size      uint64 `json:"size"`
	Platform  struct {
		Architecture string `json:"architecture"`
		OS           string `json:"os"`
		Variant      string `json:"variant"`
	} `json:"platform"`
}

// ParseOCIImageIndex converts the exact raw bytes returned by an OCI registry
// into signed release metadata. Digest binds every byte, including annotations
// not represented in Platforms; Platforms makes the executable child set
// explicit to offline reviewers.
func ParseOCIImageIndex(repository string, raw []byte) (OCIImage, error) {
	if err := validateOCIRepository(repository); err != nil {
		return OCIImage{}, err
	}
	if len(raw) == 0 || len(raw) > MaxManifestBytes {
		return OCIImage{}, fmt.Errorf("OCI image index must be 1..%d bytes", MaxManifestBytes)
	}
	if err := rejectDuplicateJSONKeys(raw); err != nil {
		return OCIImage{}, fmt.Errorf("validate OCI image index JSON: %w", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	var document ociIndexDocument
	if err := decoder.Decode(&document); err != nil {
		return OCIImage{}, fmt.Errorf("parse OCI image index: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err != nil {
			return OCIImage{}, fmt.Errorf("parse trailing OCI image index data: %w", err)
		}
		return OCIImage{}, errors.New("OCI image index contains multiple JSON values")
	}
	if document.SchemaVersion != 2 {
		return OCIImage{}, errors.New("OCI image index schemaVersion must be 2")
	}
	image := OCIImage{
		Repository: repository,
		MediaType:  document.MediaType,
		Size:       uint64(len(raw)),
		Platforms:  make([]OCIPlatform, 0, len(document.Manifests)),
	}
	digest := sha256.Sum256(raw)
	image.Digest = "sha256:" + hex.EncodeToString(digest[:])
	for _, descriptor := range document.Manifests {
		if descriptor.Platform.Variant != "" {
			return OCIImage{}, fmt.Errorf("OCI image platform %s/%s has unsupported variant %q", descriptor.Platform.OS, descriptor.Platform.Architecture, descriptor.Platform.Variant)
		}
		image.Platforms = append(image.Platforms, OCIPlatform{
			OS:        descriptor.Platform.OS,
			Arch:      descriptor.Platform.Architecture,
			Digest:    descriptor.Digest,
			MediaType: descriptor.MediaType,
			Size:      descriptor.Size,
		})
	}
	slices.SortFunc(image.Platforms, func(a, b OCIPlatform) int {
		return strings.Compare(a.OS+"/"+a.Arch, b.OS+"/"+b.Arch)
	})
	if err := validateOCIImage(image); err != nil {
		return OCIImage{}, err
	}
	return image, nil
}

func rejectDuplicateJSONKeys(data []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	var walk func() error
	walk = func() error {
		token, err := decoder.Token()
		if err != nil {
			return err
		}
		delimiter, ok := token.(json.Delim)
		if !ok {
			return nil
		}
		switch delimiter {
		case '{':
			seen := make(map[string]struct{})
			for decoder.More() {
				keyToken, err := decoder.Token()
				if err != nil {
					return err
				}
				key, ok := keyToken.(string)
				if !ok {
					return errors.New("object key is not a string")
				}
				if _, duplicate := seen[key]; duplicate {
					return fmt.Errorf("duplicate object key %q", key)
				}
				seen[key] = struct{}{}
				if err := walk(); err != nil {
					return err
				}
			}
			closing, err := decoder.Token()
			if err != nil || closing != json.Delim('}') {
				return errors.New("unterminated JSON object")
			}
		case '[':
			for decoder.More() {
				if err := walk(); err != nil {
					return err
				}
			}
			closing, err := decoder.Token()
			if err != nil || closing != json.Delim(']') {
				return errors.New("unterminated JSON array")
			}
		default:
			return errors.New("unexpected JSON delimiter")
		}
		return nil
	}
	if err := walk(); err != nil {
		return err
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("multiple JSON values")
		}
		return err
	}
	return nil
}

// VerifyOCIImageIndex requires the registry's current raw index to be exactly
// the signed content address and normalized descriptor set.
func VerifyOCIImageIndex(raw []byte, expected OCIImage) error {
	actual, err := ParseOCIImageIndex(expected.Repository, raw)
	if err != nil {
		return err
	}
	if actual.Digest != expected.Digest || actual.Size != expected.Size || actual.MediaType != expected.MediaType || !slices.Equal(actual.Platforms, expected.Platforms) {
		return errors.New("OCI image index does not match signed release metadata")
	}
	return nil
}

func validateOCIImage(image OCIImage) error {
	// Keep this focused rather than manufacturing otherwise-valid release
	// metadata just to reuse validateManifest.
	if err := validateOCIRepository(image.Repository); err != nil {
		return err
	}
	if !validOCIDigest(image.Digest) {
		return errors.New("OCI image digest must be canonical sha256:<lowercase hexadecimal>")
	}
	if image.MediaType != "application/vnd.oci.image.index.v1+json" && image.MediaType != "application/vnd.docker.distribution.manifest.list.v2+json" {
		return errors.New("unsupported OCI image index media type")
	}
	if image.Size == 0 || image.Size > MaxManifestBytes {
		return errors.New("OCI image index size is outside the bounded metadata limit")
	}
	if len(image.Platforms) != 2 {
		return errors.New("OCI image index must contain exactly linux/amd64 and linux/arm64")
	}
	previous := ""
	for _, platform := range image.Platforms {
		if !tokenRE.MatchString(platform.OS) || !tokenRE.MatchString(platform.Arch) {
			return errors.New("invalid OCI image platform")
		}
		identity := platform.OS + "/" + platform.Arch
		if previous != "" && identity <= previous {
			return errors.New("OCI image platforms are not uniquely sorted by OS/architecture")
		}
		previous = identity
		if !validOCIDigest(platform.Digest) {
			return errors.New("OCI platform digest must be canonical sha256:<lowercase hexadecimal>")
		}
		if platform.MediaType != "application/vnd.oci.image.manifest.v1+json" && platform.MediaType != "application/vnd.docker.distribution.manifest.v2+json" {
			return errors.New("unsupported OCI platform manifest media type")
		}
		if platform.Size == 0 || platform.Size > MaxArtifactSize {
			return errors.New("OCI platform manifest size is outside the bounded limit")
		}
	}
	if image.Platforms[0].OS != "linux" || image.Platforms[0].Arch != "amd64" || image.Platforms[1].OS != "linux" || image.Platforms[1].Arch != "arm64" {
		return errors.New("OCI image index must contain exactly linux/amd64 and linux/arm64")
	}
	return nil
}
