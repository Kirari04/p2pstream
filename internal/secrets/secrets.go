package secrets

import (
	"context"
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

	ProviderDirect       = "direct"
	ProviderVaultTransit = "vault-transit"

	envelopeVersionV1         = 1
	envelopeVersionV2         = 2
	envelopeAlg               = "AES-256-GCM"
	wrapAlgVaultTransitDEK    = "vault-transit-datakey"
	encryptedDataKeySize      = 32
	keySize                   = 32
	nonceSize                 = 12
	defaultEncryptionProvider = ProviderDirect
)

type Purpose string

const (
	PurposePublicRouteTargetBasicAuthPassword Purpose = "public_route_target.basic_auth_password"
	PurposePublicRouteTargetSensitiveHeader   Purpose = "public_route_target_upstream_header.value"
	PurposePublicTLSDNSCredentialAPIToken     Purpose = "public_tls_dns_credential.api_token"
	PurposePublicWAFCaptchaProviderSecretKey  Purpose = "public_waf_captcha_provider.secret_key"
	PurposePublicWAFCookieSigningSecret       Purpose = "public_waf_settings.cookie_signing_secret"
	PurposeEnvironmentAccessToken             Purpose = "environment.access_token"
	PurposeFileManagementTLSCAKey             Purpose = "file.management_tls.ca_key"
	PurposeFileManagementTLSServerKey         Purpose = "file.management_tls.server_key"
	PurposeFilePublicTLSPrivateKey            Purpose = "file.public_tls.private_key"
	PurposeFileACMEAccountKey                 Purpose = "file.acme.account_key"
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
	Provider       string
	VaultTransit   VaultTransitConfig
}

type Keyring struct {
	currentID string
	current   []byte
	keys      map[string][]byte
	required  bool
}

type Service struct {
	keyring         *Keyring
	dataKeyProvider DataKeyProvider
	allowPlaintext  bool
}

type envelope struct {
	Version          int    `json:"v"`
	Algorithm        string `json:"alg"`
	KeyID            string `json:"kid"`
	Nonce            string `json:"n"`
	Ciphertext       string `json:"ct"`
	Provider         string `json:"provider,omitempty"`
	WrapAlgorithm    string `json:"wrap_alg,omitempty"`
	EncryptedDataKey string `json:"edk,omitempty"`
}

type Metadata struct {
	State         State
	Version       int
	Algorithm     string
	KeyID         string
	Provider      string
	WrapAlgorithm string
}

type DataKey struct {
	Plaintext []byte
	Wrapped   string
}

type DataKeyProvider interface {
	Provider() string
	CurrentKeyID() string
	GenerateDataKey(context.Context, []byte) (DataKey, error)
	UnwrapDataKey(context.Context, string, []byte) ([]byte, error)
	NeedsRewrap(context.Context, string) (bool, error)
	RewrapDataKey(context.Context, string, []byte) (string, error)
	Check(context.Context) error
}

func GenerateKey() (string, string, error) {
	key := make([]byte, keySize)
	if _, err := io.ReadFull(rand.Reader, key); err != nil {
		return "", "", fmt.Errorf("generate secrets encryption key: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(key), DefaultKeyID(key), nil
}

func NewService(cfg KeyConfig) (*Service, error) {
	provider := normalizeProvider(cfg.Provider)
	var (
		keyring         *Keyring
		dataKeyProvider DataKeyProvider
		err             error
	)
	switch provider {
	case ProviderDirect:
		keyring, err = NewKeyring(cfg)
		if err != nil {
			return nil, err
		}
	case ProviderVaultTransit:
		if strings.TrimSpace(cfg.CurrentKey) != "" {
			return nil, errors.New("SECRETS_ENCRYPTION_KEY is not used with SECRETS_ENCRYPTION_PROVIDER=vault-transit")
		}
		keyring, err = NewPreviousKeyring(cfg.PreviousKeys)
		if err != nil {
			return nil, err
		}
		dataKeyProvider, err = NewVaultTransitProvider(cfg.VaultTransit)
		if err != nil {
			return nil, err
		}
	default:
		return nil, fmt.Errorf("unsupported SECRETS_ENCRYPTION_PROVIDER %q", cfg.Provider)
	}
	return &Service{
		keyring:         keyring,
		dataKeyProvider: dataKeyProvider,
		allowPlaintext:  cfg.AllowPlaintext,
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
			return nil, errors.New("SECRETS_ENCRYPTION_REQUIRED=true requires a secrets encryption key")
		}
		if previousKeysText != "" {
			return nil, errors.New("SECRETS_ENCRYPTION_PREVIOUS_KEYS requires a current secrets encryption key")
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
	if err := parsePreviousKeys(previousKeysText, keys); err != nil {
		return nil, err
	}

	return &Keyring{
		currentID: currentID,
		current:   cloneBytes(current),
		keys:      keys,
		required:  cfg.Required,
	}, nil
}

func NewPreviousKeyring(previousKeys string) (*Keyring, error) {
	keys := make(map[string][]byte)
	if err := parsePreviousKeys(strings.TrimSpace(previousKeys), keys); err != nil {
		return nil, err
	}
	if len(keys) == 0 {
		return nil, nil
	}
	return &Keyring{keys: keys}, nil
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
	return s != nil && ((s.keyring != nil && len(s.keyring.current) == keySize) || s.dataKeyProvider != nil)
}

func (s *Service) CurrentKeyID() string {
	if !s.Enabled() {
		return ""
	}
	if s.dataKeyProvider != nil {
		return s.dataKeyProvider.CurrentKeyID()
	}
	return s.keyring.currentID
}

func (s *Service) Provider() string {
	if !s.Enabled() {
		return ""
	}
	if s.dataKeyProvider != nil {
		return s.dataKeyProvider.Provider()
	}
	return ProviderDirect
}

func (s *Service) HasKeyID(keyID string) bool {
	if s == nil || s.keyring == nil {
		return false
	}
	key := s.keyring.keys[keyID]
	return len(key) == keySize
}

func (s *Service) CanDecrypt(meta Metadata) bool {
	if meta.State != StateEncrypted {
		return true
	}
	switch meta.Version {
	case envelopeVersionV1:
		return s.HasKeyID(meta.KeyID)
	case envelopeVersionV2:
		return s != nil &&
			s.dataKeyProvider != nil &&
			meta.Provider == s.dataKeyProvider.Provider() &&
			meta.KeyID == s.dataKeyProvider.CurrentKeyID()
	default:
		return false
	}
}

func (s *Service) Required() bool {
	return s != nil && ((s.keyring != nil && s.keyring.required) || (s.dataKeyProvider != nil && !s.allowPlaintext))
}

func (s *Service) Encrypt(purpose Purpose, ownerID int64, plaintext string) (string, error) {
	return s.EncryptContext(context.Background(), purpose, ownerID, plaintext)
}

func (s *Service) EncryptContext(ctx context.Context, purpose Purpose, ownerID int64, plaintext string) (string, error) {
	if plaintext == "" {
		return "", nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if s == nil || (s.keyring == nil && s.dataKeyProvider == nil) {
		if s != nil && s.allowPlaintext {
			return plaintext, nil
		}
		return "", errors.New("secrets encryption key is not configured")
	}
	if s.dataKeyProvider != nil {
		return s.encryptEnvelopeV2(ctx, purpose, ownerID, plaintext)
	}
	return s.encryptEnvelopeV1(purpose, ownerID, plaintext)
}

func (s *Service) encryptEnvelopeV1(purpose Purpose, ownerID int64, plaintext string) (string, error) {
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
		Version:    envelopeVersionV1,
		Algorithm:  envelopeAlg,
		KeyID:      s.keyring.currentID,
		Nonce:      base64.RawURLEncoding.EncodeToString(nonce),
		Ciphertext: base64.RawURLEncoding.EncodeToString(ciphertext),
	}
	return marshalEnvelope(env)
}

func (s *Service) encryptEnvelopeV2(ctx context.Context, purpose Purpose, ownerID int64, plaintext string) (string, error) {
	aad := associatedData(purpose, ownerID)
	dataKey, err := s.dataKeyProvider.GenerateDataKey(ctx, aad)
	if err != nil {
		return "", fmt.Errorf("generate data key: %w", err)
	}
	defer zeroBytes(dataKey.Plaintext)
	if len(dataKey.Plaintext) != encryptedDataKeySize {
		return "", fmt.Errorf("data key length = %d, want %d", len(dataKey.Plaintext), encryptedDataKeySize)
	}
	if strings.TrimSpace(dataKey.Wrapped) == "" {
		return "", errors.New("wrapped data key is empty")
	}
	block, err := aes.NewCipher(dataKey.Plaintext)
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
	ciphertext := aead.Seal(nil, nonce, []byte(plaintext), aad)
	env := envelope{
		Version:          envelopeVersionV2,
		Algorithm:        envelopeAlg,
		Provider:         s.dataKeyProvider.Provider(),
		KeyID:            s.dataKeyProvider.CurrentKeyID(),
		WrapAlgorithm:    wrapAlgVaultTransitDEK,
		EncryptedDataKey: dataKey.Wrapped,
		Nonce:            base64.RawURLEncoding.EncodeToString(nonce),
		Ciphertext:       base64.RawURLEncoding.EncodeToString(ciphertext),
	}
	return marshalEnvelope(env)
}

func (s *Service) Decrypt(purpose Purpose, ownerID int64, stored string) (string, State, error) {
	return s.DecryptContext(context.Background(), purpose, ownerID, stored)
}

func (s *Service) DecryptContext(ctx context.Context, purpose Purpose, ownerID int64, stored string) (string, State, error) {
	if stored == "" {
		return "", StatePlaintext, nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if !IsEncrypted(stored) {
		if s == nil || s.allowPlaintext {
			return stored, StatePlaintext, nil
		}
		return "", StatePlaintext, errors.New("plaintext secret is not allowed when secrets encryption is required")
	}
	if s == nil || (s.keyring == nil && s.dataKeyProvider == nil) {
		return "", StateEncrypted, errors.New("encrypted secret cannot be decrypted without a secrets encryption key")
	}
	env, err := parseEnvelope(stored)
	if err != nil {
		return "", StateEncrypted, err
	}
	switch env.Version {
	case envelopeVersionV1:
		return s.decryptEnvelopeV1(purpose, ownerID, env)
	case envelopeVersionV2:
		return s.decryptEnvelopeV2(ctx, purpose, ownerID, env)
	default:
		return "", StateEncrypted, fmt.Errorf("unsupported encrypted secret version %d", env.Version)
	}
}

func (s *Service) decryptEnvelopeV1(purpose Purpose, ownerID int64, env envelope) (string, State, error) {
	if s == nil || s.keyring == nil {
		return "", StateEncrypted, errors.New("direct encrypted secret cannot be decrypted without a direct secrets key")
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

func (s *Service) decryptEnvelopeV2(ctx context.Context, purpose Purpose, ownerID int64, env envelope) (string, State, error) {
	if s == nil || s.dataKeyProvider == nil || env.Provider != s.dataKeyProvider.Provider() || env.KeyID != s.dataKeyProvider.CurrentKeyID() {
		return "", StateEncrypted, fmt.Errorf("secrets encryption provider %q key %q is not configured", env.Provider, env.KeyID)
	}
	dataKey, err := s.dataKeyProvider.UnwrapDataKey(ctx, env.EncryptedDataKey, associatedData(purpose, ownerID))
	if err != nil {
		return "", StateEncrypted, fmt.Errorf("unwrap data key: %w", err)
	}
	defer zeroBytes(dataKey)
	if len(dataKey) != encryptedDataKeySize {
		return "", StateEncrypted, fmt.Errorf("data key length = %d, want %d", len(dataKey), encryptedDataKeySize)
	}
	nonce, err := base64.RawURLEncoding.DecodeString(env.Nonce)
	if err != nil || len(nonce) != nonceSize {
		return "", StateEncrypted, errors.New("encrypted secret nonce is invalid")
	}
	ciphertext, err := base64.RawURLEncoding.DecodeString(env.Ciphertext)
	if err != nil {
		return "", StateEncrypted, errors.New("encrypted secret ciphertext is invalid")
	}
	block, err := aes.NewCipher(dataKey)
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
	needsRewrap, _ := s.NeedsRewrapContext(context.Background(), stored)
	return needsRewrap
}

func (s *Service) NeedsRewrapContext(ctx context.Context, stored string) (bool, error) {
	if s == nil || !IsEncrypted(stored) {
		return false, nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	env, err := parseEnvelope(stored)
	if err != nil {
		return false, nil
	}
	switch env.Version {
	case envelopeVersionV1:
		if s.dataKeyProvider != nil {
			return true, nil
		}
		if s.keyring == nil {
			return false, nil
		}
		return env.KeyID != s.keyring.currentID || env.Algorithm != envelopeAlg, nil
	case envelopeVersionV2:
		if s.dataKeyProvider == nil ||
			env.Provider != s.dataKeyProvider.Provider() ||
			env.KeyID != s.dataKeyProvider.CurrentKeyID() ||
			env.Algorithm != envelopeAlg ||
			env.WrapAlgorithm != wrapAlgVaultTransitDEK {
			return true, nil
		}
		return s.dataKeyProvider.NeedsRewrap(ctx, env.EncryptedDataKey)
	default:
		return false, nil
	}
}

var ErrPlaintextRewrapRequired = errors.New("secret rewrap requires decrypting plaintext")

func (s *Service) RewrapContext(ctx context.Context, purpose Purpose, ownerID int64, stored string) (string, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	env, err := parseEnvelope(stored)
	if err != nil {
		return "", err
	}
	if env.Version == envelopeVersionV2 &&
		s != nil &&
		s.dataKeyProvider != nil &&
		env.Provider == s.dataKeyProvider.Provider() &&
		env.KeyID == s.dataKeyProvider.CurrentKeyID() &&
		env.Algorithm == envelopeAlg &&
		env.WrapAlgorithm == wrapAlgVaultTransitDEK {
		rewrapped, err := s.dataKeyProvider.RewrapDataKey(ctx, env.EncryptedDataKey, associatedData(purpose, ownerID))
		if err != nil {
			return "", fmt.Errorf("rewrap data key: %w", err)
		}
		env.EncryptedDataKey = rewrapped
		return marshalEnvelope(env)
	}
	return "", ErrPlaintextRewrapRequired
}

func (s *Service) Check(ctx context.Context) error {
	if s == nil || s.dataKeyProvider == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	return s.dataKeyProvider.Check(ctx)
}

func Inspect(stored string) (Metadata, error) {
	if stored == "" || !IsEncrypted(stored) {
		return Metadata{State: StatePlaintext}, nil
	}
	env, err := parseEnvelope(stored)
	if err != nil {
		return Metadata{State: StateEncrypted}, err
	}
	return Metadata{
		State:         StateEncrypted,
		Version:       env.Version,
		Algorithm:     env.Algorithm,
		KeyID:         env.KeyID,
		Provider:      envelopeProvider(env),
		WrapAlgorithm: env.WrapAlgorithm,
	}, nil
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
	switch env.Version {
	case envelopeVersionV1:
		return validateEnvelopeV1(env)
	case envelopeVersionV2:
		return validateEnvelopeV2(env)
	default:
		return envelope{}, fmt.Errorf("unsupported encrypted secret version %d", env.Version)
	}
}

func validateEnvelopeV1(env envelope) (envelope, error) {
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

func validateEnvelopeV2(env envelope) (envelope, error) {
	if env.Algorithm != envelopeAlg {
		return envelope{}, fmt.Errorf("unsupported encrypted secret algorithm %q", env.Algorithm)
	}
	if env.Provider != ProviderVaultTransit {
		return envelope{}, fmt.Errorf("unsupported encrypted secret provider %q", env.Provider)
	}
	if env.WrapAlgorithm != wrapAlgVaultTransitDEK {
		return envelope{}, fmt.Errorf("unsupported encrypted secret wrap algorithm %q", env.WrapAlgorithm)
	}
	if err := validateKeyID(env.KeyID); err != nil {
		return envelope{}, fmt.Errorf("encrypted secret key id is invalid: %w", err)
	}
	if strings.TrimSpace(env.EncryptedDataKey) == "" || env.Nonce == "" || env.Ciphertext == "" {
		return envelope{}, errors.New("encrypted secret envelope is incomplete")
	}
	return env, nil
}

func marshalEnvelope(env envelope) (string, error) {
	payload, err := json.Marshal(env)
	if err != nil {
		return "", err
	}
	return EnvelopePrefix + base64.RawURLEncoding.EncodeToString(payload), nil
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

func parsePreviousKeys(previousKeysText string, keys map[string][]byte) error {
	if previousKeysText == "" {
		return nil
	}
	for _, part := range strings.Split(previousKeysText, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		separator := strings.LastIndex(part, ":")
		if separator <= 0 || separator == len(part)-1 {
			return errors.New("SECRETS_ENCRYPTION_PREVIOUS_KEYS entries must use key_id:key")
		}
		keyID, keyText := part[:separator], part[separator+1:]
		keyID = strings.TrimSpace(keyID)
		if err := validateKeyID(keyID); err != nil {
			return fmt.Errorf("invalid previous key id %q: %w", keyID, err)
		}
		if _, exists := keys[keyID]; exists {
			return fmt.Errorf("duplicate secrets encryption key id %q", keyID)
		}
		key, err := ParseKey(strings.TrimSpace(keyText))
		if err != nil {
			return fmt.Errorf("parse previous key %q: %w", keyID, err)
		}
		keys[keyID] = cloneBytes(key)
	}
	return nil
}

func normalizeProvider(provider string) string {
	provider = strings.ToLower(strings.TrimSpace(provider))
	if provider == "" {
		return defaultEncryptionProvider
	}
	return provider
}

func envelopeProvider(env envelope) string {
	if env.Provider != "" {
		return env.Provider
	}
	return ProviderDirect
}

func cloneBytes(value []byte) []byte {
	if value == nil {
		return nil
	}
	clone := make([]byte, len(value))
	copy(clone, value)
	return clone
}

func zeroBytes(value []byte) {
	for i := range value {
		value[i] = 0
	}
}
