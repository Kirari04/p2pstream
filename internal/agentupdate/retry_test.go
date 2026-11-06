package agentupdate

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"
	"time"
)

func TestVerifyExactActivatedManifestRetry(t *testing.T) {
	for _, channel := range []string{"stable", "staging"} {
		t.Run(channel, func(t *testing.T) {
			manifestJSON, _ := testBundle(t, channel)
			manifest, err := ParseManifest(manifestJSON)
			if err != nil {
				t.Fatal(err)
			}
			policy := testPolicy()
			policy.RequiredChannel = channel
			policy.CurrentSequence = manifest.Sequence
			policy.CurrentVersion = manifest.Version
			digest := sha256.Sum256(manifestJSON)
			policy.CurrentManifestSHA256 = hex.EncodeToString(digest[:])
			if _, err := Verify(manifestJSON, policy); err != nil {
				t.Fatalf("exact activated manifest cannot be retried: %v", err)
			}
		})
	}
}

func TestExactManifestRetryRetainsFloorsAndCompatibility(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*VerifyPolicy)
	}{
		{"empty pin", func(p *VerifyPolicy) { p.CurrentManifestSHA256 = "" }},
		{"different pin", func(p *VerifyPolicy) { p.CurrentManifestSHA256 = strings.Repeat("f", 64) }},
		{"lower sequence", func(p *VerifyPolicy) { p.CurrentSequence++ }},
		{"lower version", func(p *VerifyPolicy) { p.CurrentVersion = "v1.2.4" }},
		{"higher sequence same version", func(p *VerifyPolicy) { p.CurrentSequence-- }},
		{"same sequence higher version", func(p *VerifyPolicy) { p.CurrentVersion = "v1.2.2" }},
		{"epoch floor", func(p *VerifyPolicy) { p.CurrentSecurityEpoch = 3 }},
		{"minimum safe floor", func(p *VerifyPolicy) { p.CurrentMinimumSafeVersion = "v1.1.0" }},
		{"expiry", func(p *VerifyPolicy) { p.Now = time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC) }},
		{"server compatibility", func(p *VerifyPolicy) { p.ServerVersion = "v2.1.0" }},
		{"updater compatibility", func(p *VerifyPolicy) { p.UpdaterVersion = "v0.9.0" }},
		{"protocol compatibility", func(p *VerifyPolicy) { p.ProtocolVersion = 3 }},
		{"channel", func(p *VerifyPolicy) { p.RequiredChannel = "staging" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			data, _ := testBundle(t, "stable")
			policy := testPolicy()
			policy.CurrentSequence = 10203
			policy.CurrentVersion = "v1.2.3"
			digest := sha256.Sum256(data)
			policy.CurrentManifestSHA256 = hex.EncodeToString(digest[:])
			test.mutate(&policy)
			if _, err := Verify(data, policy); err == nil {
				t.Fatal("exact-target retry bypassed a required trust/compatibility check")
			}
		})
	}
}

func TestExactManifestRetryRejectsChangedReleaseIdentity(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*Manifest)
	}{
		{"commit", func(m *Manifest) { m.Commit = strings.Repeat("b", 40) }},
		{"artifact", func(m *Manifest) { m.Artifacts[0].SHA256 = strings.Repeat("e", 64) }},
		{"epoch", func(m *Manifest) { m.SecurityEpoch++ }},
		{"metadata expiry", func(m *Manifest) { m.ExpiresAt = "2026-02-02T00:00:00Z" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			data, _ := testBundle(t, "stable")
			manifest, err := ParseManifest(data)
			if err != nil {
				t.Fatal(err)
			}
			policy := testPolicy()
			policy.CurrentSequence, policy.CurrentVersion = manifest.Sequence, manifest.Version
			digest := sha256.Sum256(data)
			policy.CurrentManifestSHA256 = hex.EncodeToString(digest[:])
			test.mutate(&manifest)
			changed, err := CanonicalManifest(manifest)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := Verify(changed, policy); err == nil {
				t.Fatal("same-target retry accepted altered manifest bytes")
			}
		})
	}
}
