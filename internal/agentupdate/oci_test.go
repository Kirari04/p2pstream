package agentupdate

import (
	"strings"
	"testing"
)

func TestParseAndVerifyOCIImageIndexBindsExactBytesAndPlatforms(t *testing.T) {
	raw := []byte(`{"schemaVersion":2,"mediaType":"application/vnd.oci.image.index.v1+json","manifests":[{"mediaType":"application/vnd.oci.image.manifest.v1+json","digest":"sha256:` + strings.Repeat("2", 64) + `","size":22,"platform":{"architecture":"arm64","os":"linux"}},{"mediaType":"application/vnd.oci.image.manifest.v1+json","digest":"sha256:` + strings.Repeat("1", 64) + `","size":11,"platform":{"architecture":"amd64","os":"linux"}}],"annotations":{"org.opencontainers.image.version":"v1.2.3"}}`)
	image, err := ParseOCIImageIndex("ghcr.io/example/p2pstream", raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(image.Platforms) != 2 || image.Platforms[0].Arch != "amd64" || image.Platforms[1].Arch != "arm64" {
		t.Fatalf("platforms not normalized: %+v", image.Platforms)
	}
	if err := VerifyOCIImageIndex(raw, image); err != nil {
		t.Fatalf("VerifyOCIImageIndex: %v", err)
	}
	tampered := []byte(strings.Replace(string(raw), strings.Repeat("1", 64), strings.Repeat("3", 64), 1))
	if err := VerifyOCIImageIndex(tampered, image); err == nil {
		t.Fatal("VerifyOCIImageIndex accepted changed exact index bytes")
	}
}

func TestParseOCIImageIndexRejectsUnsafeOrAmbiguousDescriptors(t *testing.T) {
	tests := []struct {
		name       string
		repository string
		raw        string
	}{
		{"tagged repository", "ghcr.io/example/p2pstream:latest", `{}`},
		{"duplicate field", "ghcr.io/example/p2pstream", `{"schemaVersion":2,"schemaVersion":2,"mediaType":"application/vnd.oci.image.index.v1+json","manifests":[]}`},
		{"platform variant", "ghcr.io/example/p2pstream", `{"schemaVersion":2,"mediaType":"application/vnd.oci.image.index.v1+json","manifests":[{"mediaType":"application/vnd.oci.image.manifest.v1+json","digest":"sha256:` + strings.Repeat("1", 64) + `","size":11,"platform":{"architecture":"arm64","os":"linux","variant":"v8"}}]}`},
		{"duplicate platform", "ghcr.io/example/p2pstream", `{"schemaVersion":2,"mediaType":"application/vnd.oci.image.index.v1+json","manifests":[{"mediaType":"application/vnd.oci.image.manifest.v1+json","digest":"sha256:` + strings.Repeat("1", 64) + `","size":11,"platform":{"architecture":"amd64","os":"linux"}},{"mediaType":"application/vnd.oci.image.manifest.v1+json","digest":"sha256:` + strings.Repeat("2", 64) + `","size":12,"platform":{"architecture":"amd64","os":"linux"}}]}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := ParseOCIImageIndex(test.repository, []byte(test.raw)); err == nil {
				t.Fatal("accepted invalid OCI image index")
			}
		})
	}
}
