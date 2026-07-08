package secrets

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
)

const (
	EnvelopePrefix = "p2penc:v1:"

	envelopeVersion = 1
	envelopeAlg     = "AES-256-GCM"
	keySize         = 32
	nonceSize       = 12
)

type Purpose string

const (
	PurposePublicRouteTargetBasicAuthPassword Purpose = "public_route_target.basic_auth_password"
	PurposePublicRouteTargetSensitiveHeader   Purpose = "public_route_target_upstream_header.value"
	PurposePublicTLSDNSCredentialAPIToken     Purpose = "public_tls_dns_credential.api_token"
	PurposePublicWAFCaptchaProviderSecretKey  Purpose = "public_waf_captcha_provider.secret_key"
	PurposePublicWAFCookieSigningSecret       Purpose = "public_waf_settings.cookie_signing_secret"
	PurposeEnvironmentAccessToken             Purpose = "environment.access_token"
)

type State int

const (
	StatePlaintext State = iota
	StateEncrypted
)

type KeyConfig struct {
	CurrentKey     string
	CurrentKeyID   string
	PreviousKeys   string
	Required       bool
	AllowPlaintext bool
}

type Keyring struct {
	currentID string
	current   []byte
	keys      map[string][]byte
	required  bool
}

type Service struct {
	keyring        *Keyring
	allowPlaintext bool
}

type envelope struct {
	Version    int    `json:"v"`
	Algorithm  string `json:"alg"`
	KeyID      string `json:"kid"`
	Nonce      string `json:"n"`
	Ciphertext string `json:"ct"`
}

func NewService(cfg KeyConfig) (*Service, error) {
	keyring, err := NewKeyring(cfg)
	if err != nil {
		return nil, err
	}
	return &Service{
		keyring:        keyring,
		allowPlaintext: cfg.AllowPlaintext,
	}, nil
}

func NewDisabledService() *Service {
	return &Service{allowPlaintext: true}
}

func NewKeyring(cfg KeyConfig) (*Keyring, error) {
	currentKeyText := strings.TrimSpace(cfg.CurrentKey)
	previousKeysText := strings.TrimSpace(cfg.PreviousKeys)
	if currentKeyText == "" {
		if cfg.Required {
			return nil, errors.New("SECRETS_ENCRYPTION_REQUIRED=true requires SECRETS_ENCRYPTION_KEY")
		}
		if previousKeysText != "" {
			return nil, errors.New("SECRETS_ENCRYPTION_PREVIOUS_KEYS requires SECRETS_ENCRYPTION_KEY")
		}
		return nil, nil
	}

	current, err := ParseKey(currentKeyText)
	if err != nil {
		return nil, fmt.Errorf("parse SECRETS_ENCRYPTION_KEY: %w", err)
	}
	currentID := strings.TrimSpace(cfg.CurrentKeyID)
	if currentID == "" {
		currentID = DefaultKeyID(current)
	} else if err := validateKeyID(currentID); err != nil {
		return nil, fmt.Errorf("invalid SECRETS_ENCRYPTION_KEY_ID: %w", err)
	}

	keys := map[string][]byte{currentID: cloneBytes(current)}
	if previousKeysText != "" {
		for _, part := range strings.Split(previousKeysText, ",") {
			part = strings.TrimSpace(part)
			if part == "" {
				continue
			}
			separator := strings.LastIndex(part, ":")
			if separator <= 0 || separator == len(part)-1 {
				return nil, errors.New("SECRETS_ENCRYPTION_PREVIOUS_KEYS entries must use key_id:key")
			}
			keyID, keyText := part[:separator], part[separator+1:]
			keyID = strings.TrimSpace(keyID)
			if err := validateKeyID(keyID); err != nil {
				return nil, fmt.Errorf("invalid previous key id %q: %w", keyID, err)
			}
			if _, exists := keys[keyID]; exists {
				return nil, fmt.Errorf("duplicate secrets encryption key id %q", keyID)
			}
			key, err := ParseKey(strings.TrimSpace(keyText))
			if err != nil {
				return nil, fmt.Errorf("parse previous key %q: %w", keyID, err)
			}
			keys[keyID] = cloneBytes(key)
		}
	}

	return &Keyring{
		currentID: currentID,
		current:   cloneBytes(current),
		keys:      keys,
		required:  cfg.Required,
	}, nil
}

func ParseKey(text string) ([]byte, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil, errors.New("key is empty")
	}
	encodings := []*base64.Encoding{
		base64.RawURLEncoding,
		base64.URLEncoding,
		base64.RawStdEncoding,
		base64.StdEncoding,
	}
	for _, enc := range encodings {
		decoded, err := enc.DecodeString(text)
		if err == nil && len(decoded) == keySize {
			return decoded, nil
		}
	}
	return nil, fmt.Errorf("key must be %d bytes encoded as base64 or base64url", keySize)
}

func DefaultKeyID(key []byte) string {
	sum := sha256.Sum256(key)
	return "sha256:" + base64.RawURLEncoding.EncodeToString(sum[:12])
}

func IsEncrypted(stored string) bool {
	return strings.HasPrefix(strings.TrimSpace(stored), EnvelopePrefix)
}

func (s *Service) Enabled() bool {
	return s != nil && s.keyring != nil && len(s.keyring.current) == keySize
}

func (s *Service) Required() bool {
	return s != nil && s.keyring != nil && s.keyring.required
}

func (s *Service) Encrypt(purpose Purpose, ownerID int64, plaintext string) (string, error) {
	if plaintext == "" {
		return "", nil
	}
	if s == nil || s.keyring == nil {
		if s != nil && s.allowPlaintext {
			return plaintext, nil
		}
		return "", errors.New("secrets encryption key is not configured")
	}

	block, err := aes.NewCipher(s.keyring.current)
	if err != nil {
		return "", err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, nonceSize)
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", fmt.Errorf("generate secret nonce: %w", err)
	}
	ciphertext := aead.Seal(nil, nonce, []byte(plaintext), associatedData(purpose, ownerID))
	env := envelope{
		Version:    envelopeVersion,
		Algorithm:  envelopeAlg,
		KeyID:      s.keyring.currentID,
		Nonce:      base64.RawURLEncoding.EncodeToString(nonce),
		Ciphertext: base64.RawURLEncoding.EncodeToString(ciphertext),
	}
	payload, err := json.Marshal(env)
	if err != nil {
		return "", err
	}
	return EnvelopePrefix + base64.RawURLEncoding.EncodeToString(payload), nil
}

func (s *Service) Decrypt(purpose Purpose, ownerID int64, stored string) (string, State, error) {
	if stored == "" {
		return "", StatePlaintext, nil
	}
	if !IsEncrypted(stored) {
		if s == nil || s.allowPlaintext {
			return stored, StatePlaintext, nil
		}
		return "", StatePlaintext, errors.New("plaintext secret is not allowed when secrets encryption is required")
	}
	if s == nil || s.keyring == nil {
		return "", StateEncrypted, errors.New("encrypted secret cannot be decrypted without SECRETS_ENCRYPTION_KEY")
	}
	env, err := parseEnvelope(stored)
	if err != nil {
		return "", StateEncrypted, err
	}
	key := s.keyring.keys[env.KeyID]
	if len(key) != keySize {
		return "", StateEncrypted, fmt.Errorf("secrets encryption key %q is not configured", env.KeyID)
	}
	nonce, err := base64.RawURLEncoding.DecodeString(env.Nonce)
	if err != nil || len(nonce) != nonceSize {
		return "", StateEncrypted, errors.New("encrypted secret nonce is invalid")
	}
	ciphertext, err := base64.RawURLEncoding.DecodeString(env.Ciphertext)
	if err != nil {
		return "", StateEncrypted, errors.New("encrypted secret ciphertext is invalid")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", StateEncrypted, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return "", StateEncrypted, err
	}
	plaintext, err := aead.Open(nil, nonce, ciphertext, associatedData(purpose, ownerID))
	if err != nil {
		return "", StateEncrypted, errors.New("encrypted secret authentication failed")
	}
	return string(plaintext), StateEncrypted, nil
}

func (s *Service) NeedsRewrap(stored string) bool {
	if s == nil || s.keyring == nil || !IsEncrypted(stored) {
		return false
	}
	env, err := parseEnvelope(stored)
	if err != nil {
		return false
	}
	return env.KeyID != s.keyring.currentID || env.Algorithm != envelopeAlg || env.Version != envelopeVersion
}

func parseEnvelope(stored string) (envelope, error) {
	payload := strings.TrimPrefix(strings.TrimSpace(stored), EnvelopePrefix)
	if payload == stored {
		return envelope{}, errors.New("encrypted secret envelope prefix is invalid")
	}
	raw, err := base64.RawURLEncoding.DecodeString(payload)
	if err != nil {
		return envelope{}, errors.New("encrypted secret envelope payload is invalid")
	}
	var env envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return envelope{}, errors.New("encrypted secret envelope JSON is invalid")
	}
	if env.Version != envelopeVersion {
		return envelope{}, fmt.Errorf("unsupported encrypted secret version %d", env.Version)
	}
	if env.Algorithm != envelopeAlg {
		return envelope{}, fmt.Errorf("unsupported encrypted secret algorithm %q", env.Algorithm)
	}
	if err := validateKeyID(env.KeyID); err != nil {
		return envelope{}, fmt.Errorf("encrypted secret key id is invalid: %w", err)
	}
	if env.Nonce == "" || env.Ciphertext == "" {
		return envelope{}, errors.New("encrypted secret envelope is incomplete")
	}
	return env, nil
}

func associatedData(purpose Purpose, ownerID int64) []byte {
	return []byte(fmt.Sprintf("p2pstream:secret:v1:%s:%d", purpose, ownerID))
}

func validateKeyID(keyID string) error {
	if keyID == "" {
		return errors.New("key id is empty")
	}
	if len(keyID) > 128 {
		return errors.New("key id is too long")
	}
	if strings.ContainsAny(keyID, ",\r\n\t ") {
		return errors.New("key id must not contain whitespace, comma, CR, or LF")
	}
	return nil
}

func cloneBytes(value []byte) []byte {
	if value == nil {
		return nil
	}
	clone := make([]byte, len(value))
	copy(clone, value)
	return clone
}
