package agentupdateauth

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"
	"time"
)

func TestCanonicalAuthorizationPayloadGoldenDigests(t *testing.T) {
	authorityPublic, _, authorityKeyID := deterministicKey(0x11)
	_, _, activatorKeyID := deterministicKey(0x22)
	authorization := validTestAssignmentAuthorization(authorityKeyID)
	enrollment := validTestEnrollmentReceipt(authorityKeyID, authorityPublic, activatorKeyID)
	receipt := validTestRootActionReceipt(authorityKeyID, activatorKeyID)
	tests := []struct {
		name    string
		payload func() ([]byte, error)
		want    string
	}{
		{"assignment", func() ([]byte, error) { return AssignmentAuthorizationPayload(authorization) }, "d34d351857c23ae1fea300af6d5a643006dac9819d8b15357db7e6beef6db4c9"},
		{"enrollment", func() ([]byte, error) { return EnrollmentReceiptPayload(enrollment) }, "557f64e04d4a21e0aba7cd843e5fa0806b7414e329b3b334cf9fb94d3330cc0c"},
		{"root-action", func() ([]byte, error) { return RootActionReceiptPayload(receipt) }, "f84d10ce594a3154611e59933d89e125fb8e7f0c0e6aac8ad184098a6221087b"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			payload, err := test.payload()
			if err != nil {
				t.Fatal(err)
			}
			digest := sha256.Sum256(payload)
			if got := hex.EncodeToString(digest[:]); got != test.want {
				t.Fatalf("canonical payload digest = %s, want %s", got, test.want)
			}
		})
	}
}

func TestAssignmentAuthorizationSignatureBindsEveryField(t *testing.T) {
	publicKey, privateKey, keyID := deterministicKey(0x11)
	base := validTestAssignmentAuthorization(keyID)
	signature, err := SignAssignmentAuthorization(privateKey, base)
	if err != nil {
		t.Fatal(err)
	}
	policy := AssignmentAuthorizationVerifyPolicy{
		Now: time.UnixMilli(base.IssuedAtUnixMillis).Add(time.Minute), ExpectedAgentPublicID: base.AgentPublicID,
		ExpectedAction: base.Action, ExpectedAuthorityEpoch: base.AuthorityEpoch,
	}
	if err := VerifyAssignmentAuthorization(publicKey, base, signature, policy); err != nil {
		t.Fatal(err)
	}

	mutations := []struct {
		name   string
		mutate func(*AssignmentAuthorization)
	}{
		{"agent", func(v *AssignmentAuthorization) { v.AgentPublicID = "agent:configured-b" }},
		{"assignment", func(v *AssignmentAuthorization) { v.AssignmentID++ }},
		{"campaign", func(v *AssignmentAuthorization) { v.CampaignID++ }},
		{"generation", func(v *AssignmentAuthorization) { v.Generation++ }},
		{"action", func(v *AssignmentAuthorization) { v.Action = AssignmentActionRollback }},
		{"sequence", func(v *AssignmentAuthorization) { v.CommandSequence++ }},
		{"nonce", func(v *AssignmentAuthorization) { v.Nonce[0] ^= 1 }},
		{"issued", func(v *AssignmentAuthorization) { v.IssuedAtUnixMillis++ }},
		{"expires", func(v *AssignmentAuthorization) { v.ExpiresAtUnixMillis-- }},
		{"authority-key", func(v *AssignmentAuthorization) { v.AuthorityKeyID = strings.Repeat("9", 64) }},
		{"authority-epoch", func(v *AssignmentAuthorization) { v.AuthorityEpoch++ }},
		{"server-version", func(v *AssignmentAuthorization) { v.ServerVersion = "v1.9.1" }},
		{"root-version", func(v *AssignmentAuthorization) { v.RootVersion++ }},
		{"manifest", func(v *AssignmentAuthorization) { v.ManifestSHA256 = strings.Repeat("8", 64) }},
		{"target-version", func(v *AssignmentAuthorization) { v.TargetVersion = "v2.4.1" }},
		{"target-commit", func(v *AssignmentAuthorization) { v.TargetCommit = strings.Repeat("7", 40) }},
		{"release-sequence", func(v *AssignmentAuthorization) { v.ReleaseSequence++ }},
		{"security-epoch", func(v *AssignmentAuthorization) { v.SecurityEpoch++ }},
		{"os", func(v *AssignmentAuthorization) { v.OS = "freebsd" }},
		{"arch", func(v *AssignmentAuthorization) { v.Arch = "arm64" }},
		{"artifact-name", func(v *AssignmentAuthorization) { v.ArtifactName = "p2pstream_v1.2.3_linux_arm64" }},
		{"artifact-size", func(v *AssignmentAuthorization) { v.ArtifactSize++ }},
		{"artifact-digest", func(v *AssignmentAuthorization) { v.ArtifactSHA256 = strings.Repeat("6", 64) }},
	}
	for _, mutation := range mutations {
		t.Run(mutation.name, func(t *testing.T) {
			changed := cloneAssignmentAuthorization(base)
			mutation.mutate(&changed)
			mutationPolicy := policy
			mutationPolicy.ExpectedAgentPublicID = changed.AgentPublicID
			mutationPolicy.ExpectedAction = changed.Action
			mutationPolicy.ExpectedAuthorityEpoch = changed.AuthorityEpoch
			if err := VerifyAssignmentAuthorization(publicKey, changed, signature, mutationPolicy); err == nil {
				t.Fatal("original signature accepted mutated assignment authorization")
			}
		})
	}

	replayPolicy := policy
	replayPolicy.LastCommandSequence = base.CommandSequence
	if err := VerifyAssignmentAuthorization(publicKey, base, signature, replayPolicy); err == nil {
		t.Fatal("replayed command sequence was accepted")
	}
	expiredPolicy := policy
	expiredPolicy.Now = time.UnixMilli(base.ExpiresAtUnixMillis)
	if err := VerifyAssignmentAuthorization(publicKey, base, signature, expiredPolicy); err == nil {
		t.Fatal("expired authorization was accepted")
	}
}

func TestEnrollmentReceiptSignatureBindsEveryField(t *testing.T) {
	authorityPublic, authorityPrivate, authorityKeyID := deterministicKey(0x11)
	updaterPublic, _, updaterKeyID := deterministicKey(0x33)
	activatorPublic, _, activatorKeyID := deterministicKey(0x22)
	base := validTestEnrollmentReceipt(authorityKeyID, updaterPublic, activatorKeyID)
	base.ActivatorPublicKeySHA256, _ = KeyID(activatorPublic)
	signature, err := SignEnrollmentReceipt(authorityPrivate, base)
	if err != nil {
		t.Fatal(err)
	}
	policy := EnrollmentReceiptVerifyPolicy{
		Now: time.UnixMilli(base.EnrolledAtUnixMillis).Add(time.Minute), ExpectedAgentPublicID: base.AgentPublicID,
		ExpectedAuthorityEpoch: base.AuthorityEpoch,
	}
	if err := VerifyEnrollmentReceipt(authorityPublic, base, signature, policy); err != nil {
		t.Fatal(err)
	}
	_ = updaterKeyID

	mutations := []struct {
		name   string
		mutate func(*EnrollmentReceipt)
	}{
		{"agent", func(v *EnrollmentReceipt) { v.AgentPublicID = "agent:configured-b" }},
		{"updater-key", func(v *EnrollmentReceipt) { v.UpdaterKeyID = strings.Repeat("9", 64) }},
		{"updater-digest", func(v *EnrollmentReceipt) { v.UpdaterPublicKeySHA256 = strings.Repeat("9", 64) }},
		{"activator-key", func(v *EnrollmentReceipt) { v.ActivatorKeyID = strings.Repeat("8", 64) }},
		{"activator-digest", func(v *EnrollmentReceipt) { v.ActivatorPublicKeySHA256 = strings.Repeat("8", 64) }},
		{"os", func(v *EnrollmentReceipt) { v.OS = "freebsd" }},
		{"arch", func(v *EnrollmentReceipt) { v.Arch = "arm64" }},
		{"updater-version", func(v *EnrollmentReceipt) { v.UpdaterVersion = "v1.1.1" }},
		{"root-digest", func(v *EnrollmentReceipt) { v.TrustedRootSHA256 = strings.Repeat("7", 64) }},
		{"root-version", func(v *EnrollmentReceipt) { v.TrustedRootVersion++ }},
		{"repository", func(v *EnrollmentReceipt) { v.PinnedRepository = "owner/other" }},
		{"authority-key", func(v *EnrollmentReceipt) { v.AuthorityKeyID = strings.Repeat("6", 64) }},
		{"authority-epoch", func(v *EnrollmentReceipt) { v.AuthorityEpoch++ }},
		{"enrolled", func(v *EnrollmentReceipt) { v.EnrolledAtUnixMillis++ }},
		{"expires", func(v *EnrollmentReceipt) { v.ExpiresAtUnixMillis-- }},
		{"generation", func(v *EnrollmentReceipt) { v.Generation++ }},
	}
	for _, mutation := range mutations {
		t.Run(mutation.name, func(t *testing.T) {
			changed := base
			mutation.mutate(&changed)
			mutationPolicy := policy
			mutationPolicy.ExpectedAgentPublicID = changed.AgentPublicID
			mutationPolicy.ExpectedAuthorityEpoch = changed.AuthorityEpoch
			if err := VerifyEnrollmentReceipt(authorityPublic, changed, signature, mutationPolicy); err == nil {
				t.Fatal("original signature accepted mutated enrollment receipt")
			}
		})
	}
	replay := policy
	replay.LastGeneration = base.Generation
	if err := VerifyEnrollmentReceipt(authorityPublic, base, signature, replay); err == nil {
		t.Fatal("replayed enrollment generation was accepted")
	}
}

func TestRootActionReceiptSignatureBindsEveryFieldAndSupportsBootstrapRollback(t *testing.T) {
	_, _, authorityKeyID := deterministicKey(0x11)
	activatorPublic, activatorPrivate, activatorKeyID := deterministicKey(0x22)
	base := validTestRootActionReceipt(authorityKeyID, activatorKeyID)
	signature, err := SignRootActionReceipt(activatorPrivate, base)
	if err != nil {
		t.Fatal(err)
	}
	policy := RootActionReceiptVerifyPolicy{
		Now: time.UnixMilli(base.CompletedAtUnixMillis).Add(time.Minute), ExpectedAgentPublicID: base.AgentPublicID,
		ExpectedAction: base.Action, ExpectedAuthorizationSHA256: base.AuthorizationSHA256,
		ExpectedAuthorityKeyID: base.AuthorityKeyID, ExpectedAuthorityEpoch: base.AuthorityEpoch,
	}
	if err := VerifyRootActionReceipt(activatorPublic, base, signature, policy); err != nil {
		t.Fatal(err)
	}

	mutations := []struct {
		name   string
		mutate func(*RootActionReceipt)
	}{
		{"agent", func(v *RootActionReceipt) { v.AgentPublicID = "agent:configured-b" }},
		{"assignment", func(v *RootActionReceipt) { v.AssignmentID++ }},
		{"campaign", func(v *RootActionReceipt) { v.CampaignID++ }},
		{"generation", func(v *RootActionReceipt) { v.Generation++ }},
		{"action", func(v *RootActionReceipt) { v.Action = AssignmentActionRollback }},
		{"sequence", func(v *RootActionReceipt) { v.CommandSequence++ }},
		{"authorization-digest", func(v *RootActionReceipt) { v.AuthorizationSHA256 = strings.Repeat("9", 64) }},
		{"authorization-nonce", func(v *RootActionReceipt) { v.AuthorizationNonce[0] ^= 1 }},
		{"authority-key", func(v *RootActionReceipt) { v.AuthorityKeyID = strings.Repeat("8", 64) }},
		{"authority-epoch", func(v *RootActionReceipt) { v.AuthorityEpoch++ }},
		{"activator-key", func(v *RootActionReceipt) { v.ActivatorKeyID = strings.Repeat("7", 64) }},
		{"root-counter", func(v *RootActionReceipt) { v.RootActionCounter++ }},
		{"completed", func(v *RootActionReceipt) { v.CompletedAtUnixMillis++ }},
		{"result-kind", func(v *RootActionReceipt) { v.ResultKind = RootActionResultBootstrap }},
		{"result-root", func(v *RootActionReceipt) { v.ResultRootVersion++ }},
		{"result-manifest", func(v *RootActionReceipt) { v.ResultManifestSHA256 = strings.Repeat("6", 64) }},
		{"result-version", func(v *RootActionReceipt) { v.ResultVersion = "v2.4.1" }},
		{"result-commit", func(v *RootActionReceipt) { v.ResultCommit = strings.Repeat("5", 40) }},
		{"result-sequence", func(v *RootActionReceipt) { v.ResultReleaseSequence++ }},
		{"result-epoch", func(v *RootActionReceipt) { v.ResultSecurityEpoch++ }},
		{"result-os", func(v *RootActionReceipt) { v.ResultOS = "freebsd" }},
		{"result-arch", func(v *RootActionReceipt) { v.ResultArch = "arm64" }},
		{"result-artifact", func(v *RootActionReceipt) { v.ResultArtifactName = "p2pstream_v1.2.3_linux_arm64" }},
		{"result-size", func(v *RootActionReceipt) { v.ResultArtifactSize++ }},
		{"result-digest", func(v *RootActionReceipt) { v.ResultArtifactSHA256 = strings.Repeat("4", 64) }},
	}
	for _, mutation := range mutations {
		t.Run(mutation.name, func(t *testing.T) {
			changed := cloneRootActionReceipt(base)
			mutation.mutate(&changed)
			mutationPolicy := policy
			mutationPolicy.ExpectedAgentPublicID = changed.AgentPublicID
			mutationPolicy.ExpectedAction = changed.Action
			mutationPolicy.ExpectedAuthorizationSHA256 = changed.AuthorizationSHA256
			mutationPolicy.ExpectedAuthorityKeyID = changed.AuthorityKeyID
			mutationPolicy.ExpectedAuthorityEpoch = changed.AuthorityEpoch
			if err := VerifyRootActionReceipt(activatorPublic, changed, signature, mutationPolicy); err == nil {
				t.Fatal("original signature accepted mutated root action receipt")
			}
		})
	}

	bootstrap := cloneRootActionReceipt(base)
	bootstrap.Action = AssignmentActionRollback
	bootstrap.ResultKind = RootActionResultBootstrap
	bootstrap.ResultRootVersion = 0
	bootstrap.ResultManifestSHA256 = ""
	bootstrap.ResultVersion = "v1.9.0"
	bootstrap.ResultCommit = strings.Repeat("d", 40)
	bootstrap.ResultReleaseSequence = 0
	bootstrap.ResultSecurityEpoch = 0
	bootstrap.ResultArtifactName = "p2pstream"
	bootstrapSignature, err := SignRootActionReceipt(activatorPrivate, bootstrap)
	if err != nil {
		t.Fatal(err)
	}
	bootstrapPolicy := policy
	bootstrapPolicy.ExpectedAction = AssignmentActionRollback
	if err := VerifyRootActionReceipt(activatorPublic, bootstrap, bootstrapSignature, bootstrapPolicy); err != nil {
		t.Fatalf("verify exact bootstrap rollback receipt: %v", err)
	}
}

func deterministicKey(fill byte) (ed25519.PublicKey, ed25519.PrivateKey, string) {
	privateKey := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{fill}, ed25519.SeedSize))
	publicKey := privateKey.Public().(ed25519.PublicKey)
	keyID, _ := KeyID(publicKey)
	return publicKey, privateKey, keyID
}

func validTestAssignmentAuthorization(authorityKeyID string) AssignmentAuthorization {
	issuedAt := time.Date(2026, time.September, 2, 20, 0, 0, 0, time.UTC)
	return AssignmentAuthorization{
		AgentPublicID: "agent:configured-a", AssignmentID: 17, CampaignID: 9, Generation: 3,
		Action: AssignmentActionActivate, CommandSequence: 12, Nonce: bytes.Repeat([]byte{0xa5}, 32),
		IssuedAtUnixMillis: issuedAt.UnixMilli(), ExpiresAtUnixMillis: issuedAt.Add(time.Hour).UnixMilli(),
		AuthorityKeyID: authorityKeyID, AuthorityEpoch: 2, ServerVersion: "v1.9.0", RootVersion: 4,
		ManifestSHA256: strings.Repeat("a", 64), TargetVersion: "v2.4.0", TargetCommit: strings.Repeat("b", 40),
		ReleaseSequence: 31, SecurityEpoch: 7, OS: "linux", Arch: "amd64",
		ArtifactName: "p2pstream_v2.4.0_linux_amd64", ArtifactSize: 1234567, ArtifactSHA256: strings.Repeat("c", 64),
	}
}

func validTestEnrollmentReceipt(authorityKeyID string, updaterPublic ed25519.PublicKey, activatorKeyID string) EnrollmentReceipt {
	updaterKeyID, _ := KeyID(updaterPublic)
	enrolledAt := time.Date(2026, time.September, 2, 19, 0, 0, 0, time.UTC)
	return EnrollmentReceipt{
		AgentPublicID: "agent:configured-a", UpdaterKeyID: updaterKeyID, UpdaterPublicKeySHA256: updaterKeyID,
		ActivatorKeyID: activatorKeyID, ActivatorPublicKeySHA256: activatorKeyID, OS: "linux", Arch: "amd64",
		UpdaterVersion: "v1.9.0", TrustedRootSHA256: strings.Repeat("d", 64), TrustedRootVersion: 4,
		PinnedRepository: "owner/repository", AuthorityKeyID: authorityKeyID, AuthorityEpoch: 2,
		EnrolledAtUnixMillis: enrolledAt.UnixMilli(), ExpiresAtUnixMillis: enrolledAt.Add(24 * time.Hour).UnixMilli(), Generation: 5,
	}
}

func validTestRootActionReceipt(authorityKeyID, activatorKeyID string) RootActionReceipt {
	return RootActionReceipt{
		AgentPublicID: "agent:configured-a", AssignmentID: 17, CampaignID: 9, Generation: 3,
		Action: AssignmentActionActivate, CommandSequence: 12, AuthorizationSHA256: strings.Repeat("e", 64),
		AuthorizationNonce: bytes.Repeat([]byte{0xa5}, 32), AuthorityKeyID: authorityKeyID, AuthorityEpoch: 2,
		ActivatorKeyID: activatorKeyID, RootActionCounter: 8,
		CompletedAtUnixMillis: time.Date(2026, time.September, 2, 20, 5, 0, 0, time.UTC).UnixMilli(),
		ResultKind:            RootActionResultSignedRelease, ResultRootVersion: 4, ResultManifestSHA256: strings.Repeat("a", 64),
		ResultVersion: "v2.4.0", ResultCommit: strings.Repeat("b", 40), ResultReleaseSequence: 31, ResultSecurityEpoch: 7,
		ResultOS: "linux", ResultArch: "amd64", ResultArtifactName: "p2pstream_v2.4.0_linux_amd64",
		ResultArtifactSize: 1234567, ResultArtifactSHA256: strings.Repeat("c", 64),
	}
}

func cloneAssignmentAuthorization(value AssignmentAuthorization) AssignmentAuthorization {
	value.Nonce = append([]byte(nil), value.Nonce...)
	return value
}

func cloneRootActionReceipt(value RootActionReceipt) RootActionReceipt {
	value.AuthorizationNonce = append([]byte(nil), value.AuthorizationNonce...)
	return value
}
