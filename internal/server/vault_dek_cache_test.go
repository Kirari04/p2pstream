package server

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"p2pstream/internal/config"
	"p2pstream/internal/db"
	secretspkg "p2pstream/internal/secrets"
)

func TestPublicProxySnapshotRefreshUsesVaultDEKCache(t *testing.T) {
	ctx := context.Background()
	vault := newServerFakeVaultTransit(t)
	database := newServerTestDB(t)
	app := NewApp(&config.Config{
		SecretsEncryptionProvider:      secretspkg.ProviderVaultTransit,
		SecretsVaultAddress:            vault.server.URL,
		SecretsVaultToken:              vault.token,
		SecretsVaultMount:              "transit",
		SecretsVaultKey:                "p2pstream",
		SecretsVaultDEKCacheMaxEntries: 8,
		SecretsVaultDEKCacheTTL:        time.Minute,
	}, database)
	listener := seedPublicConfigTestListener(t, database)
	route, err := database.CreatePublicRoute(ctx, db.CreatePublicRouteParams{
		ListenerID:          listener.ID,
		Priority:            10,
		PathPrefix:          "/vault-cache",
		Action:              publicRouteActionForward,
		PathSecurityMode:    publicRoutePathSecurityModeStrict,
		TargetLoadBalancing: publicRouteTargetLoadBalancingRoundRobin,
		Enabled:             1,
	})
	if err != nil {
		t.Fatalf("create route: %v", err)
	}
	target, err := database.CreatePublicRouteTarget(ctx, db.CreatePublicRouteTargetParams{
		RouteID:                             route.ID,
		Name:                                "vault-target",
		Position:                            0,
		PriorityGroup:                       0,
		Weight:                              100,
		Enabled:                             1,
		TargetType:                          publicRouteTargetTypeProxy,
		Url:                                 "http://127.0.0.1:9000",
		Transport:                           publicRouteTargetTransportDirect,
		AgentSelectorJson:                   "{}",
		AgentLoadBalancing:                  publicRouteTargetLoadBalancingRoundRobin,
		UpstreamBasicAuthEnabled:            1,
		UpstreamBasicAuthUsername:           "origin",
		UpstreamResponseHeaderTimeoutMillis: defaultTargetUpstreamResponseHeaderTimeoutMillis,
		HealthCheckMethod:                   defaultTargetHealthCheckMethod,
		HealthCheckPath:                     defaultTargetHealthCheckPath,
		HealthCheckIntervalMillis:           defaultTargetHealthCheckIntervalMillis,
		HealthCheckTimeoutMillis:            defaultTargetHealthCheckTimeoutMillis,
		HealthCheckHealthyThreshold:         defaultTargetHealthCheckHealthyThreshold,
		HealthCheckUnhealthyThreshold:       defaultTargetHealthCheckUnhealthyThreshold,
		HealthCheckExpectedStatusMin:        defaultTargetHealthCheckExpectedStatusMin,
		HealthCheckExpectedStatusMax:        defaultTargetHealthCheckExpectedStatusMax,
		StaticStatusCode:                    defaultStaticStatusCode,
		StaticResponseBodyMode:              publicResponseBodyModeInline,
	})
	if err != nil {
		t.Fatalf("create target: %v", err)
	}
	storeVaultBackedTargetPassword(t, app, target.ID, "origin-password")

	vault.resetDecryptCount()
	if err := app.refreshPublicProxySnapshot(ctx); err != nil {
		t.Fatalf("refresh public proxy snapshot: %v", err)
	}
	if got := vault.decryptCount(); got != 2 {
		t.Fatalf("Vault decrypt count after first refresh = %d, want 2", got)
	}
	if err := app.refreshPublicProxySnapshot(ctx); err != nil {
		t.Fatalf("refresh public proxy snapshot again: %v", err)
	}
	if got := vault.decryptCount(); got != 2 {
		t.Fatalf("Vault decrypt count after second refresh = %d, want 2 cache hits", got)
	}

	storeVaultBackedTargetPassword(t, app, target.ID, "rotated-password")
	if err := app.refreshPublicProxySnapshot(ctx); err != nil {
		t.Fatalf("refresh public proxy snapshot after changing envelope: %v", err)
	}
	if got := vault.decryptCount(); got != 3 {
		t.Fatalf("Vault decrypt count after envelope change = %d, want 3", got)
	}
}

func storeVaultBackedTargetPassword(t *testing.T, app *App, targetID int64, plaintext string) {
	t.Helper()
	stored, err := app.Secrets.EncryptContext(context.Background(), secretspkg.PurposePublicRouteTargetBasicAuthPassword, targetID, plaintext)
	if err != nil {
		t.Fatalf("encrypt target password: %v", err)
	}
	if _, err := app.DB.ExecContext(context.Background(), `UPDATE public_route_targets SET upstream_basic_auth_password = ? WHERE id = ?`, stored, targetID); err != nil {
		t.Fatalf("store encrypted target password: %v", err)
	}
}

type serverFakeVaultTransit struct {
	server         *httptest.Server
	token          string
	mu             sync.Mutex
	latest         int
	dataKeyCounter int
	decryptCounter int
	keys           map[string]serverFakeVaultTransitKey
}

type serverFakeVaultTransitKey struct {
	value   []byte
	context string
}

func newServerFakeVaultTransit(t *testing.T) *serverFakeVaultTransit {
	t.Helper()
	fake := &serverFakeVaultTransit{
		token:  "test-token",
		latest: 1,
		keys:   make(map[string]serverFakeVaultTransitKey),
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/transit/keys/p2pstream", fake.handleKey)
	mux.HandleFunc("/v1/transit/datakey/plaintext/p2pstream", fake.handleDataKey)
	mux.HandleFunc("/v1/transit/decrypt/p2pstream", fake.handleDecrypt)
	fake.server = httptest.NewServer(mux)
	t.Cleanup(fake.server.Close)
	return fake
}

func (f *serverFakeVaultTransit) resetDecryptCount() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.decryptCounter = 0
}

func (f *serverFakeVaultTransit) decryptCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.decryptCounter
}

func (f *serverFakeVaultTransit) handleKey(w http.ResponseWriter, r *http.Request) {
	if !f.authorized(w, r) {
		return
	}
	f.mu.Lock()
	latest := f.latest
	f.mu.Unlock()
	writeServerVaultJSON(w, map[string]interface{}{
		"data": map[string]interface{}{
			"latest_version": latest,
			"derived":        true,
		},
	})
}

func (f *serverFakeVaultTransit) handleDataKey(w http.ResponseWriter, r *http.Request) {
	if !f.authorized(w, r) {
		return
	}
	var body map[string]interface{}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid json", http.StatusBadRequest)
		return
	}
	contextValue := fmt.Sprint(body["context"])
	if contextValue == "" || fmt.Sprint(body["bits"]) != "256" {
		http.Error(w, "invalid datakey request", http.StatusBadRequest)
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.dataKeyCounter++
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(f.dataKeyCounter + i)
	}
	wrapped := fmt.Sprintf("vault:v%d:%s", f.latest, base64.RawURLEncoding.EncodeToString(key))
	f.keys[wrapped] = serverFakeVaultTransitKey{value: cloneServerVaultBytes(key), context: contextValue}
	writeServerVaultJSON(w, map[string]interface{}{
		"data": map[string]string{
			"plaintext":  base64.StdEncoding.EncodeToString(key),
			"ciphertext": wrapped,
		},
	})
}

func (f *serverFakeVaultTransit) handleDecrypt(w http.ResponseWriter, r *http.Request) {
	if !f.authorized(w, r) {
		return
	}
	var body struct {
		Ciphertext string `json:"ciphertext"`
		Context    string `json:"context"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid json", http.StatusBadRequest)
		return
	}
	f.mu.Lock()
	f.decryptCounter++
	record := f.keys[body.Ciphertext]
	f.mu.Unlock()
	if len(record.value) != 32 || record.context != body.Context {
		writeServerVaultError(w, http.StatusBadRequest, "ciphertext is invalid")
		return
	}
	writeServerVaultJSON(w, map[string]interface{}{
		"data": map[string]string{"plaintext": base64.StdEncoding.EncodeToString(record.value)},
	})
}

func (f *serverFakeVaultTransit) authorized(w http.ResponseWriter, r *http.Request) bool {
	if r.Header.Get("X-Vault-Token") != f.token {
		writeServerVaultError(w, http.StatusForbidden, "permission denied")
		return false
	}
	return true
}

func writeServerVaultJSON(w http.ResponseWriter, value interface{}) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(value)
}

func writeServerVaultError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string][]string{"errors": []string{message}})
}

func cloneServerVaultBytes(value []byte) []byte {
	clone := make([]byte, len(value))
	copy(clone, value)
	return clone
}
