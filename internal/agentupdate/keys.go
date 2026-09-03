package agentupdate

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"errors"
)

func EncodePublicKey(publicKey ed25519.PublicKey) (string, error) {
	if len(publicKey) != ed25519.PublicKeySize {
		return "", errors.New("invalid Ed25519 public key length")
	}
	return base64.StdEncoding.EncodeToString(publicKey), nil
}

func ParsePublicKey(encoded string) (ed25519.PublicKey, error) {
	decoded, err := decodeCanonicalBase64(encoded, ed25519.PublicKeySize)
	if err != nil {
		return nil, err
	}
	return ed25519.PublicKey(decoded), nil
}

// EncodePrivateKey encodes only the 32-byte seed; the public suffix is derived
// again on parse instead of trusting duplicated key material.
func EncodePrivateKey(privateKey ed25519.PrivateKey) (string, error) {
	if len(privateKey) != ed25519.PrivateKeySize {
		return "", errors.New("invalid Ed25519 private key length")
	}
	derived := ed25519.NewKeyFromSeed(privateKey.Seed())
	if !bytes.Equal(privateKey, derived) {
		return "", errors.New("Ed25519 private key has an inconsistent public suffix")
	}
	return base64.StdEncoding.EncodeToString(privateKey.Seed()), nil
}

func ParsePrivateKey(encoded string) (ed25519.PrivateKey, error) {
	seed, err := decodeCanonicalBase64(encoded, ed25519.SeedSize)
	if err != nil {
		return nil, err
	}
	return ed25519.NewKeyFromSeed(seed), nil
}
