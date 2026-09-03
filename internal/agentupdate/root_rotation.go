package agentupdate

import (
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
)

const rootSignatureDomain = "p2pstream-agent-update-root-v1\x00"

type RootRotationPolicy struct {
	Now                time.Time
	MinimumRootVersion uint64
}

// SignRootRotation signs a canonical next-root document with a threshold
// subset of keys from the current root.
func SignRootRotation(nextRootJSON []byte, currentRoot RootMetadata, privateKeys []ed25519.PrivateKey) (SignatureEnvelope, error) {
	nextRoot, err := ParseRoot(nextRootJSON)
	if err != nil {
		return SignatureEnvelope{}, err
	}
	if err := validateRoot(currentRoot); err != nil {
		return SignatureEnvelope{}, fmt.Errorf("invalid current root: %w", err)
	}
	if currentRoot.Version == ^uint64(0) || nextRoot.Version != currentRoot.Version+1 {
		return SignatureEnvelope{}, errors.New("next root version must advance by exactly one")
	}
	currentExpiry, _ := parseTimestamp(currentRoot.ExpiresAt)
	nextExpiry, _ := parseTimestamp(nextRoot.ExpiresAt)
	if !nextExpiry.After(currentExpiry) {
		return SignatureEnvelope{}, errors.New("next root must extend the root expiry")
	}
	return signWithRoot(rootSignedPayload(nextRootJSON), currentRoot, privateKeys)
}

// VerifyRootRotation authenticates an exact canonical next-root document under
// the current root threshold. An expired root cannot authorize online rotation;
// recovery then requires a separately distributed out-of-band trust anchor.
func VerifyRootRotation(nextRootJSON, signatureJSON []byte, currentRoot RootMetadata, policy RootRotationPolicy) (RootMetadata, error) {
	if err := validateRoot(currentRoot); err != nil {
		return RootMetadata{}, fmt.Errorf("invalid current root: %w", err)
	}
	if policy.Now.IsZero() {
		return RootMetadata{}, errors.New("verification time is required")
	}
	if currentRoot.Version < policy.MinimumRootVersion {
		return RootMetadata{}, errors.New("current root is below the persisted root version floor")
	}
	currentExpiry, _ := parseTimestamp(currentRoot.ExpiresAt)
	if !policy.Now.Before(currentExpiry) {
		return RootMetadata{}, errors.New("current root is expired")
	}
	nextRoot, err := ParseRoot(nextRootJSON)
	if err != nil {
		return RootMetadata{}, err
	}
	if currentRoot.Version == ^uint64(0) || nextRoot.Version != currentRoot.Version+1 {
		return RootMetadata{}, errors.New("next root version must advance by exactly one")
	}
	nextExpiry, _ := parseTimestamp(nextRoot.ExpiresAt)
	if !nextExpiry.After(currentExpiry) {
		return RootMetadata{}, errors.New("next root must extend the root expiry")
	}
	if !policy.Now.Before(nextExpiry) {
		return RootMetadata{}, errors.New("next root is expired")
	}
	envelope, err := ParseSignatures(signatureJSON)
	if err != nil {
		return RootMetadata{}, err
	}
	if err := verifyThreshold(rootSignedPayload(nextRootJSON), envelope, currentRoot); err != nil {
		return RootMetadata{}, err
	}
	return nextRoot, nil
}

func signWithRoot(payload []byte, root RootMetadata, privateKeys []ed25519.PrivateKey) (SignatureEnvelope, error) {
	allowed := make(map[string]struct{}, len(root.Keys))
	for _, key := range root.Keys {
		allowed[key.ID] = struct{}{}
	}
	envelope := SignatureEnvelope{SchemaVersion: SchemaVersion}
	seen := make(map[string]struct{}, len(privateKeys))
	for _, privateKey := range privateKeys {
		if len(privateKey) != ed25519.PrivateKeySize {
			return SignatureEnvelope{}, errors.New("invalid Ed25519 private key length")
		}
		if _, err := EncodePrivateKey(privateKey); err != nil {
			return SignatureEnvelope{}, err
		}
		publicKey := privateKey.Public().(ed25519.PublicKey)
		id := KeyID(publicKey)
		if _, ok := allowed[id]; !ok {
			return SignatureEnvelope{}, fmt.Errorf("signing key %s is not in root metadata", id)
		}
		if _, ok := seen[id]; ok {
			return SignatureEnvelope{}, fmt.Errorf("duplicate signing key %s", id)
		}
		seen[id] = struct{}{}
		encoded := base64.StdEncoding.EncodeToString(ed25519.Sign(privateKey, payload))
		envelope.Signatures = append(envelope.Signatures, Signature{KeyID: id, Signature: encoded})
	}
	slices.SortFunc(envelope.Signatures, func(a, b Signature) int { return strings.Compare(a.KeyID, b.KeyID) })
	if uint32(len(envelope.Signatures)) < root.Threshold {
		return SignatureEnvelope{}, errors.New("not enough signing keys to satisfy root threshold")
	}
	if err := validateSignatures(envelope); err != nil {
		return SignatureEnvelope{}, err
	}
	return envelope, nil
}

func rootSignedPayload(rootJSON []byte) []byte {
	payload := make([]byte, 0, len(rootSignatureDomain)+len(rootJSON))
	payload = append(payload, rootSignatureDomain...)
	payload = append(payload, rootJSON...)
	return payload
}
