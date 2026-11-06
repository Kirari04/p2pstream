package agentupdate

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"errors"
)

// These helpers encode the local worker and activator identities used to
// authorize privileged host actions. They are unrelated to release artifacts.
func EncodePublicKey(publicKey ed25519.PublicKey) (string, error) {
	if len(publicKey) != ed25519.PublicKeySize {
		return "", errors.New("invalid Ed25519 public key length")
	}
	return base64.StdEncoding.EncodeToString(publicKey), nil
}

func ParsePublicKey(encoded string) (ed25519.PublicKey, error) {
	decoded, err := decodeCanonicalKey(encoded, ed25519.PublicKeySize)
	if err != nil {
		return nil, err
	}
	return ed25519.PublicKey(decoded), nil
}

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
	seed, err := decodeCanonicalKey(encoded, ed25519.SeedSize)
	if err != nil {
		return nil, err
	}
	return ed25519.NewKeyFromSeed(seed), nil
}

func decodeCanonicalKey(value string, expectedBytes int) ([]byte, error) {
	decoded, err := base64.StdEncoding.DecodeString(value)
	if err != nil {
		return nil, err
	}
	if len(decoded) != expectedBytes || base64.StdEncoding.EncodeToString(decoded) != value {
		return nil, errors.New("invalid length or non-canonical base64")
	}
	return decoded, nil
}
