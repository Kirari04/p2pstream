package secrets

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

const defaultVaultTransitTimeout = 5 * time.Second

type VaultTransitConfig struct {
	Address   string
	Token     string
	MountPath string
	KeyName   string
	Namespace string
	Timeout   time.Duration

	HTTPClient *http.Client
}

type VaultTransitProvider struct {
	address   string
	token     string
	mountPath string
	keyName   string
	keyID     string
	namespace string
	timeout   time.Duration
	client    *http.Client

	mu                    sync.Mutex
	latestVersion         int
	latestVersionResolved bool
}

func NewVaultTransitProvider(cfg VaultTransitConfig) (*VaultTransitProvider, error) {
	address := strings.TrimRight(strings.TrimSpace(cfg.Address), "/")
	if address == "" {
		return nil, errors.New("SECRETS_ENCRYPTION_VAULT_ADDR is required")
	}
	parsed, err := url.Parse(address)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return nil, fmt.Errorf("invalid SECRETS_ENCRYPTION_VAULT_ADDR %q", cfg.Address)
	}
	if parsed.Scheme != "https" && !(parsed.Scheme == "http" && isLoopbackHost(parsed.Hostname())) {
		return nil, errors.New("SECRETS_ENCRYPTION_VAULT_ADDR must use https unless it targets loopback")
	}
	token := strings.TrimSpace(cfg.Token)
	if token == "" {
		return nil, errors.New("SECRETS_ENCRYPTION_VAULT_TOKEN or SECRETS_ENCRYPTION_VAULT_TOKEN_FILE is required")
	}
	mountPath, err := normalizeVaultPath(cfg.MountPath, "transit")
	if err != nil {
		return nil, fmt.Errorf("invalid SECRETS_ENCRYPTION_VAULT_MOUNT: %w", err)
	}
	keyName := strings.TrimSpace(cfg.KeyName)
	if keyName == "" {
		return nil, errors.New("SECRETS_ENCRYPTION_VAULT_KEY is required")
	}
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = defaultVaultTransitTimeout
	}
	client := cfg.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: timeout}
	}
	keyID := "vault-transit:" + mountPath + "/" + keyName
	if err := validateKeyID(keyID); err != nil {
		return nil, fmt.Errorf("invalid Vault Transit key reference: %w", err)
	}
	return &VaultTransitProvider{
		address:   address,
		token:     token,
		mountPath: mountPath,
		keyName:   keyName,
		keyID:     keyID,
		namespace: strings.TrimSpace(cfg.Namespace),
		timeout:   timeout,
		client:    client,
	}, nil
}

func (v *VaultTransitProvider) Provider() string {
	return ProviderVaultTransit
}

func (v *VaultTransitProvider) CurrentKeyID() string {
	if v == nil {
		return ""
	}
	return v.keyID
}

func (v *VaultTransitProvider) GenerateDataKey(ctx context.Context, aad []byte) (DataKey, error) {
	var response struct {
		Data struct {
			Plaintext  string `json:"plaintext"`
			Ciphertext string `json:"ciphertext"`
		} `json:"data"`
	}
	if err := v.doJSON(ctx, http.MethodPost, v.transitPath("datakey/plaintext"), map[string]interface{}{
		"bits":    256,
		"context": base64.StdEncoding.EncodeToString(aad),
	}, &response); err != nil {
		return DataKey{}, err
	}
	plaintext, err := decodeBase64(response.Data.Plaintext)
	if err != nil {
		return DataKey{}, errors.New("Vault returned invalid plaintext data key")
	}
	if len(plaintext) != encryptedDataKeySize {
		zeroBytes(plaintext)
		return DataKey{}, fmt.Errorf("Vault returned data key length %d, want %d", len(plaintext), encryptedDataKeySize)
	}
	if strings.TrimSpace(response.Data.Ciphertext) == "" {
		zeroBytes(plaintext)
		return DataKey{}, errors.New("Vault returned empty wrapped data key")
	}
	return DataKey{Plaintext: plaintext, Wrapped: response.Data.Ciphertext}, nil
}

func (v *VaultTransitProvider) UnwrapDataKey(ctx context.Context, wrapped string, aad []byte) ([]byte, error) {
	var response struct {
		Data struct {
			Plaintext string `json:"plaintext"`
		} `json:"data"`
	}
	if err := v.doJSON(ctx, http.MethodPost, v.transitPath("decrypt"), map[string]interface{}{
		"ciphertext": strings.TrimSpace(wrapped),
		"context":    base64.StdEncoding.EncodeToString(aad),
	}, &response); err != nil {
		return nil, err
	}
	plaintext, err := decodeBase64(response.Data.Plaintext)
	if err != nil {
		return nil, errors.New("Vault returned invalid plaintext data key")
	}
	if len(plaintext) != encryptedDataKeySize {
		zeroBytes(plaintext)
		return nil, fmt.Errorf("Vault returned data key length %d, want %d", len(plaintext), encryptedDataKeySize)
	}
	return plaintext, nil
}

func (v *VaultTransitProvider) NeedsRewrap(ctx context.Context, wrapped string) (bool, error) {
	wrappedVersion := vaultCiphertextVersion(wrapped)
	if wrappedVersion <= 0 {
		return false, nil
	}
	latest, err := v.latestKeyVersion(ctx)
	if err != nil {
		return false, err
	}
	return latest > 0 && wrappedVersion < latest, nil
}

func (v *VaultTransitProvider) RewrapDataKey(ctx context.Context, wrapped string, aad []byte) (string, error) {
	var response struct {
		Data struct {
			Ciphertext string `json:"ciphertext"`
		} `json:"data"`
	}
	if err := v.doJSON(ctx, http.MethodPost, v.transitPath("rewrap"), map[string]interface{}{
		"ciphertext": strings.TrimSpace(wrapped),
		"context":    base64.StdEncoding.EncodeToString(aad),
	}, &response); err != nil {
		return "", err
	}
	if strings.TrimSpace(response.Data.Ciphertext) == "" {
		return "", errors.New("Vault returned empty rewrapped data key")
	}
	return response.Data.Ciphertext, nil
}

func (v *VaultTransitProvider) Check(ctx context.Context) error {
	_, err := v.latestKeyVersion(ctx)
	return err
}

func (v *VaultTransitProvider) latestKeyVersion(ctx context.Context) (int, error) {
	v.mu.Lock()
	if v.latestVersionResolved {
		version := v.latestVersion
		v.mu.Unlock()
		return version, nil
	}
	v.mu.Unlock()

	var response struct {
		Data struct {
			LatestVersion int `json:"latest_version"`
		} `json:"data"`
	}
	if err := v.doJSON(ctx, http.MethodGet, v.transitPath("keys"), nil, &response); err != nil {
		return 0, err
	}
	if response.Data.LatestVersion <= 0 {
		return 0, errors.New("Vault Transit key has no latest version")
	}

	v.mu.Lock()
	v.latestVersion = response.Data.LatestVersion
	v.latestVersionResolved = true
	v.mu.Unlock()
	return response.Data.LatestVersion, nil
}

func (v *VaultTransitProvider) doJSON(ctx context.Context, method, path string, requestBody interface{}, responseBody interface{}) error {
	if v == nil {
		return errors.New("Vault Transit provider is not configured")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	var cancel context.CancelFunc
	if v.timeout > 0 {
		ctx, cancel = context.WithTimeout(ctx, v.timeout)
		defer cancel()
	}

	var body io.Reader
	if requestBody != nil {
		payload, err := json.Marshal(requestBody)
		if err != nil {
			return err
		}
		body = bytes.NewReader(payload)
	}
	req, err := http.NewRequestWithContext(ctx, method, v.address+"/v1/"+path, body)
	if err != nil {
		return err
	}
	req.Header.Set("X-Vault-Token", v.token)
	req.Header.Set("Content-Type", "application/json")
	if v.namespace != "" {
		req.Header.Set("X-Vault-Namespace", v.namespace)
	}
	resp, err := v.client.Do(req)
	if err != nil {
		return fmt.Errorf("Vault Transit %s failed: %w", method, err)
	}
	defer resp.Body.Close()

	limitedBody, err := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	if err != nil {
		return fmt.Errorf("read Vault Transit response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("Vault Transit request failed with HTTP %d: %s", resp.StatusCode, vaultErrorSummary(limitedBody))
	}
	if responseBody == nil {
		return nil
	}
	if err := json.Unmarshal(limitedBody, responseBody); err != nil {
		return errors.New("Vault Transit response JSON is invalid")
	}
	return nil
}

func (v *VaultTransitProvider) transitPath(operation string) string {
	key := url.PathEscape(v.keyName)
	switch operation {
	case "keys":
		return v.mountPath + "/keys/" + key
	default:
		return v.mountPath + "/" + operation + "/" + key
	}
}

func normalizeVaultPath(value, fallback string) (string, error) {
	value = strings.Trim(strings.TrimSpace(value), "/")
	if value == "" {
		value = fallback
	}
	if value == "." || strings.Contains(value, "..") {
		return "", errors.New("path must not contain dot segments")
	}
	for _, part := range strings.Split(value, "/") {
		if part == "" || part == "." || part == ".." {
			return "", errors.New("path must not contain empty or dot segments")
		}
	}
	return value, nil
}

func vaultCiphertextVersion(ciphertext string) int {
	ciphertext = strings.TrimSpace(ciphertext)
	if !strings.HasPrefix(ciphertext, "vault:v") {
		return 0
	}
	rest := strings.TrimPrefix(ciphertext, "vault:v")
	separator := strings.IndexByte(rest, ':')
	if separator <= 0 {
		return 0
	}
	version, err := strconv.Atoi(rest[:separator])
	if err != nil {
		return 0
	}
	return version
}

func vaultErrorSummary(body []byte) string {
	var parsed struct {
		Errors []string `json:"errors"`
	}
	if err := json.Unmarshal(body, &parsed); err == nil && len(parsed.Errors) > 0 {
		return sanitizeProviderError(strings.Join(parsed.Errors, "; "))
	}
	return "Vault returned an error"
}

func sanitizeProviderError(message string) string {
	message = strings.NewReplacer("\r", " ", "\n", " ", "\t", " ").Replace(strings.TrimSpace(message))
	if message == "" {
		return "Vault returned an error"
	}
	if len(message) > 512 {
		return message[:512] + "..."
	}
	return message
}

func decodeBase64(value string) ([]byte, error) {
	value = strings.TrimSpace(value)
	encodings := []*base64.Encoding{
		base64.StdEncoding,
		base64.RawStdEncoding,
		base64.URLEncoding,
		base64.RawURLEncoding,
	}
	for _, encoding := range encodings {
		decoded, err := encoding.DecodeString(value)
		if err == nil {
			return decoded, nil
		}
	}
	return nil, errors.New("invalid base64")
}

func isLoopbackHost(host string) bool {
	switch strings.ToLower(strings.Trim(host, "[]")) {
	case "localhost", "127.0.0.1", "::1":
		return true
	default:
		return false
	}
}
