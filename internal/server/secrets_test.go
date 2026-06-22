package server

import (
	"context"
	"strings"
	"testing"

	"connectrpc.com/connect"

	p2pstreamv1 "p2pstream/gen/proto/p2pstream/v1"
	"p2pstream/internal/config"
	"p2pstream/internal/db"
	secretspkg "p2pstream/internal/secrets"
)

func TestInitializeSecretStorageMigratesDatabaseSecrets(t *testing.T) {
	ctx := context.Background()
	database := newServerTestDB(t)
	listener := seedPublicConfigTestListener(t, database)
	route, err := database.CreatePublicRoute(ctx, db.CreatePublicRouteParams{
		ListenerID:          listener.ID,
		Priority:            10,
		PathPrefix:          "/legacy",
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
		Name:                                "legacy-target",
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
		UpstreamBasicAuthPassword:           "legacy-password",
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
	header, err := database.CreatePublicRouteTargetUpstreamHeader(ctx, db.CreatePublicRouteTargetUpstreamHeaderParams{
		TargetID:  target.ID,
		Position:  0,
		Name:      "Cookie",
		Value:     "session=legacy",
		Sensitive: 0,
	})
	if err != nil {
		t.Fatalf("create header: %v", err)
	}
	credential, err := database.CreatePublicTlsDnsCredential(ctx, db.CreatePublicTlsDnsCredentialParams{
		Name:             "cloudflare",
		Provider:         "cloudflare",
		CloudflareZoneID: "zone",
		ApiToken:         "cf-token",
		Enabled:          1,
	})
	if err != nil {
		t.Fatalf("create DNS credential: %v", err)
	}
	provider, err := database.CreatePublicWafCaptchaProvider(ctx, db.CreatePublicWafCaptchaProviderParams{
		Name:         "captcha",
		ProviderType: publicWafCaptchaProviderTurnstile,
		SiteKey:      "site",
		SecretKey:    "captcha-secret",
		Enabled:      1,
	})
	if err != nil {
		t.Fatalf("create captcha provider: %v", err)
	}
	if _, err := database.UpsertPublicWafSettings(ctx, "cookie-secret"); err != nil {
		t.Fatalf("upsert WAF settings: %v", err)
	}
	environment, err := database.CreateEnvironment(ctx, db.CreateEnvironmentParams{
		Name:                        "remote",
		ManagementUrl:               "https://remote.example:8081",
		Transport:                   environmentTransportDirect,
		AccessToken:                 "p2pat_remote-token",
		ResponseHeaderTimeoutMillis: defaultEnvironmentResponseHeaderTimeoutMillis,
		Enabled:                     1,
	})
	if err != nil {
		t.Fatalf("create environment: %v", err)
	}

	app := NewApp(&config.Config{
		SecretsEncryptionKey:   testSecretsEncryptionKey(),
		SecretsEncryptionKeyID: "test-key",
	}, database)
	if err := app.InitializeSecretStorage(ctx); err != nil {
		t.Fatalf("initialize secret storage: %v", err)
	}

	assertMigratedSecret(t, app, secretspkg.PurposePublicRouteTargetBasicAuthPassword, target.ID, rawTargetPassword(t, database, target.ID), "legacy-password")
	assertMigratedSecret(t, app, secretspkg.PurposePublicRouteTargetSensitiveHeader, header.ID, rawHeaderValue(t, database, header.ID), "session=legacy")
	assertMigratedSecret(t, app, secretspkg.PurposePublicTLSDNSCredentialAPIToken, credential.ID, rawDNSCredentialToken(t, database, credential.ID), "cf-token")
	assertMigratedSecret(t, app, secretspkg.PurposePublicWAFCaptchaProviderSecretKey, provider.ID, rawCaptchaProviderSecret(t, database, provider.ID), "captcha-secret")
	assertMigratedSecret(t, app, secretspkg.PurposePublicWAFCookieSigningSecret, publicWAFSettingsSingletonID, rawWAFSigningSecret(t, database), "cookie-secret")
	assertMigratedSecret(t, app, secretspkg.PurposeEnvironmentAccessToken, environment.ID, rawEnvironmentAccessToken(t, database, environment.ID), "p2pat_remote-token")
}

func TestInitializeSecretStorageRequiredRejectsPlaintextWithKey(t *testing.T) {
	ctx := context.Background()
	database := newServerTestDB(t)
	if _, err := database.UpsertPublicWafSettings(ctx, "cookie-secret"); err != nil {
		t.Fatalf("upsert WAF settings: %v", err)
	}
	app := NewApp(&config.Config{
		SecretsEncryptionKey:      testSecretsEncryptionKey(),
		SecretsEncryptionKeyID:    "test-key",
		SecretsEncryptionRequired: true,
	}, database)

	err := app.InitializeSecretStorage(ctx)
	if err == nil || !strings.Contains(err.Error(), "plaintext but secrets encryption is required") {
		t.Fatalf("InitializeSecretStorage() error = %v, want required plaintext failure", err)
	}
}

func TestInitializeSecretStorageAllowsNilDatabase(t *testing.T) {
	app := NewApp(&config.Config{
		SecretsEncryptionKey:   testSecretsEncryptionKey(),
		SecretsEncryptionKeyID: "test-key",
	}, nil)
	if err := app.InitializeSecretStorage(context.Background()); err != nil {
		t.Fatalf("InitializeSecretStorage() error = %v, want nil", err)
	}
}

func TestInitializeSecretStorageRewrapsLegacyTargetScopedHeaderEnvelope(t *testing.T) {
	ctx := context.Background()
	database := newServerTestDB(t)
	listener := seedPublicConfigTestListener(t, database)
	route, err := database.CreatePublicRoute(ctx, db.CreatePublicRouteParams{
		ListenerID:          listener.ID,
		Priority:            10,
		PathPrefix:          "/legacy-header",
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
		Name:                                "legacy-target",
		Position:                            0,
		PriorityGroup:                       0,
		Weight:                              100,
		Enabled:                             1,
		TargetType:                          publicRouteTargetTypeProxy,
		Url:                                 "http://127.0.0.1:9000",
		Transport:                           publicRouteTargetTransportDirect,
		AgentSelectorJson:                   "{}",
		AgentLoadBalancing:                  publicRouteTargetLoadBalancingRoundRobin,
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
	if _, err := database.CreatePublicRouteTargetUpstreamHeader(ctx, db.CreatePublicRouteTargetUpstreamHeaderParams{
		TargetID:  target.ID,
		Position:  0,
		Name:      "X-Plain",
		Value:     "plain",
		Sensitive: 0,
	}); err != nil {
		t.Fatalf("create plain header: %v", err)
	}
	service, err := secretspkg.NewService(secretspkg.KeyConfig{
		CurrentKey:     testSecretsEncryptionKey(),
		CurrentKeyID:   "test-key",
		AllowPlaintext: true,
	})
	if err != nil {
		t.Fatalf("new secret service: %v", err)
	}
	legacyEncrypted, err := service.Encrypt(secretspkg.PurposePublicRouteTargetSensitiveHeader, target.ID, "session=legacy")
	if err != nil {
		t.Fatalf("encrypt legacy header: %v", err)
	}
	header, err := database.CreatePublicRouteTargetUpstreamHeader(ctx, db.CreatePublicRouteTargetUpstreamHeaderParams{
		TargetID:  target.ID,
		Position:  1,
		Name:      "Cookie",
		Value:     legacyEncrypted,
		Sensitive: 1,
	})
	if err != nil {
		t.Fatalf("create legacy sensitive header: %v", err)
	}
	if header.ID == target.ID {
		t.Fatalf("test requires different header and target IDs, both were %d", header.ID)
	}

	app := NewApp(&config.Config{
		SecretsEncryptionKey:   testSecretsEncryptionKey(),
		SecretsEncryptionKeyID: "test-key",
	}, database)
	if err := app.InitializeSecretStorage(ctx); err != nil {
		t.Fatalf("initialize secret storage: %v", err)
	}

	stored := rawHeaderValue(t, database, header.ID)
	assertMigratedSecret(t, app, secretspkg.PurposePublicRouteTargetSensitiveHeader, header.ID, stored, "session=legacy")
	if _, _, err := app.decryptSecret(secretspkg.PurposePublicRouteTargetSensitiveHeader, target.ID, stored); err == nil {
		t.Fatal("expected rewrapped header secret to reject legacy target-scoped AAD")
	}
}

func TestInitializeSecretStorageFailsEncryptedRowsWithoutKey(t *testing.T) {
	ctx := context.Background()
	database := newServerTestDB(t)
	service, err := secretspkg.NewService(secretspkg.KeyConfig{
		CurrentKey:     testSecretsEncryptionKey(),
		CurrentKeyID:   "test-key",
		AllowPlaintext: true,
	})
	if err != nil {
		t.Fatalf("new secret service: %v", err)
	}
	encrypted, err := service.Encrypt(secretspkg.PurposePublicWAFCookieSigningSecret, publicWAFSettingsSingletonID, "cookie-secret")
	if err != nil {
		t.Fatalf("encrypt secret: %v", err)
	}
	if _, err := database.UpsertPublicWafSettings(ctx, encrypted); err != nil {
		t.Fatalf("upsert WAF settings: %v", err)
	}

	app := NewApp(nil, database)
	err = app.InitializeSecretStorage(ctx)
	if err == nil || !strings.Contains(err.Error(), "encrypted stored secrets require a current secrets encryption key") {
		t.Fatalf("InitializeSecretStorage() error = %v, want missing key failure", err)
	}
}

func TestManagementSecretWritesEncryptAtRest(t *testing.T) {
	ctx := context.Background()
	app := NewApp(&config.Config{
		SecretsEncryptionKey:   testSecretsEncryptionKey(),
		SecretsEncryptionKeyID: "test-key",
	}, newServerTestDB(t))
	if err := app.InitializeSecretStorage(ctx); err != nil {
		t.Fatalf("initialize secret storage: %v", err)
	}
	header := createTestAdminSession(t, app)

	dnsReq := connect.NewRequest(&p2pstreamv1.CreatePublicTlsDnsCredentialRequest{
		Name:             "cloudflare",
		Provider:         p2pstreamv1.PublicDnsProvider_PUBLIC_DNS_PROVIDER_CLOUDFLARE,
		CloudflareZoneId: "zone",
		ApiToken:         "cf-token",
		Enabled:          true,
	})
	dnsReq.Header().Set("Cookie", header.Get("Cookie"))
	dnsResp, err := app.CreatePublicTlsDnsCredential(ctx, dnsReq)
	if err != nil {
		t.Fatalf("create DNS credential: %v", err)
	}
	assertMigratedSecret(t, app, secretspkg.PurposePublicTLSDNSCredentialAPIToken, dnsResp.Msg.Credential.Id, rawDNSCredentialToken(t, app.DB, dnsResp.Msg.Credential.Id), "cf-token")

	captchaReq := connect.NewRequest(&p2pstreamv1.CreatePublicWafCaptchaProviderRequest{
		Name:         "captcha",
		ProviderType: p2pstreamv1.PublicWafCaptchaProviderType_PUBLIC_WAF_CAPTCHA_PROVIDER_TYPE_TURNSTILE,
		SiteKey:      "site",
		SecretKey:    "captcha-secret",
		Enabled:      true,
	})
	captchaReq.Header().Set("Cookie", header.Get("Cookie"))
	captchaResp, err := app.CreatePublicWafCaptchaProvider(ctx, captchaReq)
	if err != nil {
		t.Fatalf("create captcha provider: %v", err)
	}
	assertMigratedSecret(t, app, secretspkg.PurposePublicWAFCaptchaProviderSecretKey, captchaResp.Msg.Provider.Id, rawCaptchaProviderSecret(t, app.DB, captchaResp.Msg.Provider.Id), "captcha-secret")

	envReq := connect.NewRequest(&p2pstreamv1.CreateEnvironmentRequest{
		Name:                        "remote",
		ManagementUrl:               "https://remote.example:8081",
		Transport:                   p2pstreamv1.EnvironmentTransport_ENVIRONMENT_TRANSPORT_DIRECT,
		AccessToken:                 "p2pat_remote-token",
		ResponseHeaderTimeoutMillis: defaultEnvironmentResponseHeaderTimeoutMillis,
		Enabled:                     true,
	})
	envReq.Header().Set("Cookie", header.Get("Cookie"))
	envResp, err := app.CreateEnvironment(ctx, envReq)
	if err != nil {
		t.Fatalf("create environment: %v", err)
	}
	assertMigratedSecret(t, app, secretspkg.PurposeEnvironmentAccessToken, envResp.Msg.Environment.Id, rawEnvironmentAccessToken(t, app.DB, envResp.Msg.Environment.Id), "p2pat_remote-token")

	wafSettings, err := app.ensurePublicWafSettings(ctx)
	if err != nil {
		t.Fatalf("ensure WAF settings: %v", err)
	}
	assertMigratedSecret(t, app, secretspkg.PurposePublicWAFCookieSigningSecret, publicWAFSettingsSingletonID, rawWAFSigningSecret(t, app.DB), decryptStoredSecret(t, app, secretspkg.PurposePublicWAFCookieSigningSecret, publicWAFSettingsSingletonID, wafSettings.CookieSigningSecret))
}

func assertMigratedSecret(t *testing.T, app *App, purpose secretspkg.Purpose, ownerID int64, stored string, want string) {
	t.Helper()
	if !secretspkg.IsEncrypted(stored) {
		t.Fatalf("stored %s for owner %d = %q, want encrypted", purpose, ownerID, stored)
	}
	if strings.Contains(stored, want) {
		t.Fatalf("stored %s for owner %d leaked plaintext %q", purpose, ownerID, want)
	}
	got, _, err := app.decryptSecret(purpose, ownerID, stored)
	if err != nil {
		t.Fatalf("decrypt %s for owner %d: %v", purpose, ownerID, err)
	}
	if got != want {
		t.Fatalf("decrypt %s for owner %d = %q, want %q", purpose, ownerID, got, want)
	}
}

func decryptStoredSecret(t *testing.T, app *App, purpose secretspkg.Purpose, ownerID int64, stored string) string {
	t.Helper()
	got, _, err := app.decryptSecret(purpose, ownerID, stored)
	if err != nil {
		t.Fatalf("decrypt stored secret: %v", err)
	}
	return got
}

func rawTargetPassword(t *testing.T, database *db.DB, id int64) string {
	t.Helper()
	row, err := database.GetPublicRouteTarget(context.Background(), id)
	if err != nil {
		t.Fatalf("get target: %v", err)
	}
	return row.UpstreamBasicAuthPassword
}

func rawHeaderValue(t *testing.T, database *db.DB, id int64) string {
	t.Helper()
	var value string
	if err := database.QueryRowContext(context.Background(), `SELECT value FROM public_route_target_upstream_headers WHERE id = ?`, id).Scan(&value); err != nil {
		t.Fatalf("get header value: %v", err)
	}
	return value
}

func rawDNSCredentialToken(t *testing.T, database *db.DB, id int64) string {
	t.Helper()
	row, err := database.GetPublicTlsDnsCredential(context.Background(), id)
	if err != nil {
		t.Fatalf("get DNS credential: %v", err)
	}
	return row.ApiToken
}

func rawCaptchaProviderSecret(t *testing.T, database *db.DB, id int64) string {
	t.Helper()
	row, err := database.GetPublicWafCaptchaProvider(context.Background(), id)
	if err != nil {
		t.Fatalf("get captcha provider: %v", err)
	}
	return row.SecretKey
}

func rawWAFSigningSecret(t *testing.T, database *db.DB) string {
	t.Helper()
	row, err := database.GetPublicWafSettings(context.Background())
	if err != nil {
		t.Fatalf("get WAF settings: %v", err)
	}
	return row.CookieSigningSecret
}

func rawEnvironmentAccessToken(t *testing.T, database *db.DB, id int64) string {
	t.Helper()
	row, err := database.GetEnvironment(context.Background(), id)
	if err != nil {
		t.Fatalf("get environment: %v", err)
	}
	return row.AccessToken
}
