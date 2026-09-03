package agentupdate

import (
	"bytes"
	"crypto/ed25519"
	"strings"
	"testing"
	"time"
)

func TestCanonicalKeyEncoding(t *testing.T) {
	privateKey := deterministicPrivateKey(5)
	encodedPrivate, err := EncodePrivateKey(privateKey)
	if err != nil {
		t.Fatal(err)
	}
	parsedPrivate, err := ParsePrivateKey(encodedPrivate)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(privateKey, parsedPrivate) {
		t.Fatal("private key round trip changed bytes")
	}
	publicKey := privateKey.Public().(ed25519.PublicKey)
	encodedPublic, err := EncodePublicKey(publicKey)
	if err != nil {
		t.Fatal(err)
	}
	parsedPublic, err := ParsePublicKey(encodedPublic)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(publicKey, parsedPublic) {
		t.Fatal("public key round trip changed bytes")
	}
}

func TestRootRotationRequiresCurrentThresholdAndNextVersion(t *testing.T) {
	oldKeys := []ed25519.PrivateKey{deterministicPrivateKey(1), deterministicPrivateKey(2)}
	currentRoot, err := NewRootMetadata(1, "2027-01-01T00:00:00Z", 2, []ed25519.PublicKey{
		oldKeys[0].Public().(ed25519.PublicKey), oldKeys[1].Public().(ed25519.PublicKey),
	})
	if err != nil {
		t.Fatal(err)
	}
	newKey := deterministicPrivateKey(3)
	nextRoot, err := NewRootMetadata(2, "2028-01-01T00:00:00Z", 1, []ed25519.PublicKey{newKey.Public().(ed25519.PublicKey)})
	if err != nil {
		t.Fatal(err)
	}
	nextRootJSON, err := CanonicalRoot(nextRoot)
	if err != nil {
		t.Fatal(err)
	}
	envelope, err := SignRootRotation(nextRootJSON, currentRoot, oldKeys)
	if err != nil {
		t.Fatal(err)
	}
	signaturesJSON, err := CanonicalSignatures(envelope)
	if err != nil {
		t.Fatal(err)
	}
	got, err := VerifyRootRotation(nextRootJSON, signaturesJSON, currentRoot, RootRotationPolicy{
		Now:                time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		MinimumRootVersion: 1,
	})
	if err != nil {
		t.Fatalf("VerifyRootRotation: %v", err)
	}
	if got.Version != 2 {
		t.Fatalf("root version = %d", got.Version)
	}

	currentRoot.Threshold = 1
	oneSignature, err := SignRootRotation(nextRootJSON, currentRoot, oldKeys[:1])
	if err != nil {
		t.Fatal(err)
	}
	currentRoot.Threshold = 2
	oneSignatureJSON, err := CanonicalSignatures(oneSignature)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := VerifyRootRotation(nextRootJSON, oneSignatureJSON, currentRoot, RootRotationPolicy{Now: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}); err == nil || !strings.Contains(err.Error(), "threshold") {
		t.Fatalf("threshold error = %v", err)
	}
}
