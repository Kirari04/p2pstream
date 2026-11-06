package cmd

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/pressly/goose/v3"

	"p2pstream/internal/db"
	"p2pstream/internal/db/migrations"
	"p2pstream/internal/server"
)

func TestStandaloneServerStartsWithAbandonedLegacyOwnerAndSchema20(t *testing.T) {
	data := setupServerGuardConfig(t)
	// Model a standalone deployment with no updater environment, including any
	// SERVER_UPDATE_* variables inherited from the machine running this test.
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		if strings.HasPrefix(key, "SERVER_UPDATE_") {
			t.Setenv(key, "")
			if err := os.Unsetenv(key); err != nil {
				t.Fatal(err)
			}
		}
	}
	t.Setenv("BOOTSTRAP_AGENT_ID", "")
	t.Setenv("BOOTSTRAP_AGENT_NAME", "")
	t.Setenv("BOOTSTRAP_AGENT_TOKEN", "")
	t.Setenv("PUBLIC_CACHE_DIR", filepath.Join(data, "cache", "public"))

	// These are synthetic IDs in the valid API-2 RuntimeOwner format from
	// v0.1.53-staging.103. allowed:false used to block standalone startup.
	sentinels := map[string][]byte{
		".p2pstream-runtime-owner.json": []byte(`{
  "api": 2,
  "instance_id": "11111111-1111-4111-8111-111111111111",
  "generation": "22222222-2222-4222-8222-222222222222",
  "allowed": false,
  "version": "v0.1.53-staging.103",
  "commit": "879e33936fed954e4611a2850f6b7818fd8a4bba"
}
`),
		".p2pstream-runtime.lock": []byte("existing stable data lock\n"),
		"server-installation-id":  []byte("11111111-1111-4111-8111-111111111111\n"),
		"config-sentinel.env":     []byte("MANAGEMENT_PORT=19443\n"),
		"certs/preserved.crt":     []byte("synthetic certificate preservation sentinel\n"),
		"certs/preserved.key":     []byte("synthetic key preservation sentinel\n"),
	}
	for name, contents := range sentinels {
		path := filepath.Join(data, name)
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, contents, 0600); err != nil {
			t.Fatal(err)
		}
	}
	lockPath := filepath.Join(data, ".p2pstream-runtime.lock")
	lockBefore, err := os.Stat(lockPath)
	if err != nil {
		t.Fatal(err)
	}
	wantRows := legacyOwnerSchema20Fixture(t, data)
	var firstHistory [][]any
	for _, phase := range []string{"upgrade", "restart"} {
		if !t.Run(phase, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			cfg, lease, err := loadServerConfig(ctx)
			if err != nil {
				t.Fatalf("standalone config with abandoned owner: %v", err)
			}
			defer func() {
				if err := lease.Close(); err != nil {
					t.Error(err)
				}
			}()
			database, err := db.Open(cfg.DatabaseURL)
			if err != nil {
				t.Fatalf("normal database open: %v", err)
			}
			defer func() {
				if err := database.Close(); err != nil {
					t.Error(err)
				}
			}()
			app, err := server.NewAppWithError(cfg, database)
			if err != nil {
				t.Fatalf("normal application initialization: %v", err)
			}
			defer app.CloseAgentTransports()
			defer func() {
				if err := app.ClosePublicGeoRuntime(); err != nil {
					t.Error(err)
				}
				closeCtx, closeCancel := context.WithTimeout(context.Background(), time.Second)
				defer closeCancel()
				if err := app.CloseObservabilityRecorder(closeCtx); err != nil {
					t.Error(err)
				}
			}()
			assertLegacyOwnerSchemaVersion(t, database.DB, 21)
			assertLegacyOwnerEventIndexes(t, database.DB, true)
			if rows := legacyOwnerQueryRows(t, database.DB, "PRAGMA foreign_key_check"); len(rows) != 0 {
				t.Fatalf("foreign key violations after startup: %v", rows)
			}
			if got := legacyOwnerPersistentRows(t, database.DB); !reflect.DeepEqual(got, wantRows) {
				t.Fatalf("persistent agent/site/routing/certificate/event data changed:\nbefore=%v\nafter=%v", wantRows, got)
			}
			history := legacyOwnerQueryRows(t, database.DB, "SELECT * FROM goose_db_version ORDER BY id")
			if phase == "upgrade" {
				firstHistory = history
			} else if !reflect.DeepEqual(history, firstHistory) {
				t.Fatal("clean restart changed migration history")
			}
			contenderCtx, contenderCancel := context.WithTimeout(ctx, 30*time.Millisecond)
			defer contenderCancel()
			_, contender, err := loadServerConfig(contenderCtx)
			if contender != nil {
				_ = contender.Close()
			}
			if !errors.Is(err, context.DeadlineExceeded) || contender != nil {
				t.Fatalf("competing startup was not excluded: err=%v lease=%v", err, contender)
			}
		}) {
			return
		}
		// Subtest defers close the application, database, and lease before restart.
		lockAfter, err := os.Stat(lockPath)
		if err != nil || !os.SameFile(lockBefore, lockAfter) || lockAfter.Mode() != lockBefore.Mode() {
			t.Fatalf("%s replaced or changed the stable lock: %v", phase, err)
		}
		for name, want := range sentinels {
			got, err := os.ReadFile(filepath.Join(data, name))
			if err != nil || !bytes.Equal(got, want) {
				t.Fatalf("%s changed sentinel %s: %v", phase, name, err)
			}
		}
	}
}

func legacyOwnerSchema20Fixture(t *testing.T, data string) map[string][][]any {
	t.Helper()
	database, err := db.Open(filepath.Join(data, "p2pstream.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := database.Close(); err != nil {
			t.Error(err)
		}
	}()
	assertLegacyOwnerSchemaVersion(t, database.DB, 21)
	// Use the real reversible migration, including Goose bookkeeping, to build
	// schema 20. This restores the full indexes, rather than falsely stamping a
	// schema-21 database as 20. Only migration 21 is exposed to this provider;
	// cmd tests do not export or replace the private Go migrations 17/18/20.
	// This synthetic fixture is not a restored historical production database.
	const migration = "00021_proxy_request_event_nullable_indexes.sql"
	contents, err := migrations.FS.ReadFile(migration)
	if err != nil {
		t.Fatal(err)
	}
	provider, err := goose.NewProvider(goose.DialectSQLite3, database.DB, fstest.MapFS{
		migration: &fstest.MapFile{Data: contents},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := provider.DownTo(t.Context(), 20); err != nil {
		t.Fatal(err)
	}
	assertLegacyOwnerSchemaVersion(t, database.DB, 20)
	assertLegacyOwnerEventIndexes(t, database.DB, false)
	for _, statement := range []string{
		`INSERT INTO agents (id, public_id, name, token_hash, agent_version, agent_commit) VALUES (101, '33333333-3333-4333-8333-333333333333', 'preserved agent', 'synthetic-token-hash', 'v0.1.53-staging.103', '879e33936fed954e4611a2850f6b7818fd8a4bba')`,
		`INSERT INTO public_agent_labels (agent_id, key, value) VALUES (101, 'fleet', 'preserved')`,
		`INSERT INTO users (id, username, password_hash, role) VALUES (101, 'preserved-admin', 'synthetic-password-hash', 'admin')`,
		`INSERT INTO public_listeners (id, name, port, protocol) VALUES (101, 'preserved listener', 19443, 'https')`,
		`INSERT INTO public_sites (id, name, published, canonical_hostname) VALUES (101, 'preserved site', 1, 'preserved.example')`,
		`INSERT INTO public_site_listener_bindings (site_id, listener_id) VALUES (101, 101)`,
		`INSERT INTO public_site_hosts (id, site_id, hostname_pattern, role, behavior) VALUES (101, 101, 'preserved.example', 'primary', 'serve')`,
		`INSERT INTO public_routes (id, site_id, priority, path_prefix) VALUES (101, 101, 10, '/preserved')`,
		`INSERT INTO public_route_targets (id, route_id, position, name, url, transport, agent_selector_json) VALUES (101, 101, 0, 'preserved target', 'http://upstream.invalid:9000', 'agent', '{"match_labels":{"fleet":"preserved"}}')`,
		`INSERT INTO proxy_request_events (id, occurred_at, status_code, duration_ms, listener_id, route_id, route_target_id, agent_id, method, host, path_prefix) VALUES (101, '2026-10-18 12:00:00+00:00', 200, 5, 101, 101, 101, 101, 'GET', 'preserved.example', '/preserved')`,
	} {
		if _, err := database.ExecContext(t.Context(), statement); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := database.ExecContext(t.Context(), `INSERT INTO public_tls_certificates (id, listener_id, hostname_pattern, cert_path, key_path) VALUES (101, 101, 'preserved.example', ?, ?)`, filepath.Join(data, "certs/preserved.crt"), filepath.Join(data, "certs/preserved.key")); err != nil {
		t.Fatal(err)
	}
	return legacyOwnerPersistentRows(t, database.DB)
}

func assertLegacyOwnerSchemaVersion(t *testing.T, database *sql.DB, want int) {
	t.Helper()
	var version int
	if err := database.QueryRowContext(t.Context(), `SELECT MAX(version_id) FROM goose_db_version WHERE is_applied = 1`).Scan(&version); err != nil || version != want {
		t.Fatalf("schema version = %d, want %d: %v", version, want, err)
	}
}

func assertLegacyOwnerEventIndexes(t *testing.T, database *sql.DB, partial bool) {
	t.Helper()
	for _, column := range []string{"listener_id", "route_id", "route_target_id", "agent_id", "waf_rule_id", "cache_rule_id", "retry_rule_id"} {
		name := "idx_proxy_request_events_" + column
		var ddl string
		if err := database.QueryRowContext(t.Context(), `SELECT sql FROM sqlite_master WHERE type = 'index' AND name = ?`, name).Scan(&ddl); err != nil {
			t.Fatal(err)
		}
		want := "CREATE INDEX " + name + " ON proxy_request_events (" + column + ")"
		if partial {
			want += " WHERE " + column + " IS NOT NULL"
		}
		if got := strings.Join(strings.Fields(ddl), " "); got != want {
			t.Fatalf("index %s = %q, want %q", name, got, want)
		}
	}
}

func legacyOwnerPersistentRows(t *testing.T, database *sql.DB) map[string][][]any {
	t.Helper()
	result := make(map[string][][]any)
	for _, table := range []string{"agents", "public_agent_labels", "users", "public_listeners", "public_sites", "public_site_listener_bindings", "public_site_hosts", "public_routes", "public_route_targets", "public_tls_certificates", "proxy_request_events"} {
		result[table] = legacyOwnerQueryRows(t, database, "SELECT * FROM "+table+" ORDER BY 1")
	}
	return result
}

func legacyOwnerQueryRows(t *testing.T, database *sql.DB, query string) [][]any {
	t.Helper()
	rows, err := database.QueryContext(t.Context(), query)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	columns, err := rows.Columns()
	if err != nil {
		t.Fatal(err)
	}
	var result [][]any
	for rows.Next() {
		values := make([]any, len(columns))
		args := make([]any, len(columns))
		for i := range values {
			args[i] = &values[i]
		}
		if err := rows.Scan(args...); err != nil {
			t.Fatal(err)
		}
		result = append(result, values)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return result
}
