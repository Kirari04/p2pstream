package db

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"

	"github.com/pressly/goose/v3"

	"p2pstream/internal/db/migrations"
)

func seedCutoverVersion17(t *testing.T) *sql.DB {
	t.Helper()
	database := openSchemaParityDB(t, "version17.db")
	database.SetMaxOpenConns(1)
	if _, err := database.Exec(`PRAGMA foreign_keys=ON`); err != nil {
		t.Fatal(err)
	}
	provider, err := goose.NewProvider(goose.DialectSQLite3, database, migrations.FS,
		goose.WithGoMigrations(goose.NewGoMigration(17, &goose.GoFunc{RunTx: repairEmptyManagedUpdateSchema}, nil)))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := provider.UpTo(context.Background(), 17); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`
 INSERT INTO public_cache_rules (id,name,allow_cookie_requests) VALUES (1,'assets',1);
 INSERT INTO public_cache_entries (key_digest,rule_id,scope,listener_protocol,host,path,query_key,status_code,body_path,size_bytes,stored_at,expires_at,last_accessed_at,hit_count)
 VALUES ('existing',1,'selected_backend','http','example.test','/asset','',200,'/cache/existing.body',123,'2026-09-01 12:34:56','2026-09-30 12:34:56','2026-09-02 12:34:56',7);
 `); err != nil {
		t.Fatal(err)
	}
	return database
}

func TestSchemaCutoverPreservesCacheAndNormalizesTimestamps(t *testing.T) {
	database := seedCutoverVersion17(t)
	if _, err := database.Exec(`
 UPDATE observability_rollup_state SET proxy_backfill_upper_id=10, proxy_backfilled_through_id=10, agent_backfill_upper_id=20, agent_backfilled_through_id=20 WHERE id=1;
 INSERT INTO proxy_request_rollup_minutes (bucket_unix_millis,requests,response_bytes) VALUES (1000,42,12345);
 `); err != nil {
		t.Fatal(err)
	}
	if err := runEmbeddedMigrations(database); err != nil {
		t.Fatal(err)
	}
	var requests, responseBytes int64
	if err := database.QueryRow(`SELECT requests,response_bytes FROM proxy_request_rollup_minutes WHERE bucket_unix_millis=1000`).Scan(&requests, &responseBytes); err != nil || requests != 42 || responseBytes != 12345 {
		t.Fatalf("historical rollup changed: %d/%d, %v", requests, responseBytes, err)
	}
	q := New(database)
	entry, err := q.GetPublicCacheEntry(context.Background(), "existing")
	if err != nil {
		t.Fatal(err)
	}
	if entry.BodyPath != "/cache/existing.body" || entry.SizeBytes != 123 || entry.HitCount != 7 || entry.RuleID != 1 {
		t.Fatalf("cache changed: %+v", entry)
	}
	var stored, expires, accessed string
	if err := database.QueryRow(`SELECT CAST(stored_at AS TEXT),CAST(expires_at AS TEXT),CAST(last_accessed_at AS TEXT) FROM public_cache_entries WHERE key_digest='existing'`).Scan(&stored, &expires, &accessed); err != nil {
		t.Fatal(err)
	}
	for _, value := range []string{stored, expires, accessed} {
		if !strings.HasSuffix(value, "+00:00") {
			t.Fatalf("timestamp not normalized: %q", value)
		}
	}
	if err := q.TouchPublicCacheEntry(context.Background(), TouchPublicCacheEntryParams{KeyDigest: entry.KeyDigest, StoredAt: entry.StoredAt.UTC(), LastAccessedAt: time.Now().UTC(), HitCount: 2}); err != nil {
		t.Fatal(err)
	}
	touched, err := q.GetPublicCacheEntry(context.Background(), entry.KeyDigest)
	if err != nil || touched.HitCount != 9 {
		t.Fatalf("normalized generation touch: %+v, %v", touched, err)
	}
	stale, err := q.DeletePublicCacheEntryGeneration(context.Background(), DeletePublicCacheEntryGenerationParams{KeyDigest: entry.KeyDigest, StoredAt: entry.StoredAt.Add(time.Nanosecond)})
	if err != nil || stale != 0 {
		t.Fatalf("stale generation delete: %d, %v", stale, err)
	}
	deleted, err := q.DeletePublicCacheEntryGeneration(context.Background(), DeletePublicCacheEntryGenerationParams{KeyDigest: entry.KeyDigest, StoredAt: entry.StoredAt.UTC()})
	if err != nil || deleted != 1 {
		t.Fatalf("normalized generation delete: %d, %v", deleted, err)
	}
	// SQL defaults must round-trip through the Go driver's generation comparison too.
	if _, err := database.Exec(`INSERT INTO public_cache_entries (key_digest,rule_id,scope,listener_protocol,host,path,query_key,status_code,body_path,size_bytes,expires_at) VALUES ('default',1,'selected_backend','http','example.test','/default','',200,'/cache/default.body',5,'2026-09-30 12:34:56+00:00')`); err != nil {
		t.Fatal(err)
	}
	current, err := q.GetPublicCacheEntry(context.Background(), "default")
	if err != nil {
		t.Fatal(err)
	}
	deleted, err = q.DeletePublicCacheEntryGeneration(context.Background(), DeletePublicCacheEntryGenerationParams{KeyDigest: current.KeyDigest, StoredAt: current.StoredAt.UTC()})
	if err != nil || deleted != 1 {
		t.Fatalf("default generation delete: %d, %v", deleted, err)
	}
	want := openSchemaParityDB(t, "schema.db")
	applySchemaSQL(t, want)
	assertSchemaParity(t, "cutover", readNormalizedSchema(t, database), readNormalizedSchema(t, want))
	rows, err := database.Query(`PRAGMA foreign_key_check`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	if rows.Next() || rows.Err() != nil {
		t.Fatalf("foreign key check: %v", rows.Err())
	}
}

func TestSchemaCutoverRejectsUnfinishedBackfillWithoutMutation(t *testing.T) {
	for _, column := range []string{"proxy_backfill_upper_id", "agent_backfill_upper_id"} {
		t.Run(column, func(t *testing.T) {
			database := seedCutoverVersion17(t)
			if _, err := database.Exec(`UPDATE observability_rollup_state SET ` + column + `=10 WHERE id=1`); err != nil {
				t.Fatal(err)
			}
			before := readNormalizedSchema(t, database)
			err := runEmbeddedMigrations(database)
			if err == nil || !strings.Contains(err.Error(), "unfinished observability backfill") {
				t.Fatalf("cutover error: %v", err)
			}
			assertSchemaParity(t, "rejected cutover", readNormalizedSchema(t, database), before)
			var version, allowed, hits int64
			var stored string
			if err := database.QueryRow(`SELECT MAX(version_id) FROM goose_db_version WHERE is_applied=1`).Scan(&version); err != nil || version != 17 {
				t.Fatalf("version=%d, %v", version, err)
			}
			if err := database.QueryRow(`SELECT allow_cookie_requests FROM public_cache_rules WHERE id=1`).Scan(&allowed); err != nil || allowed != 1 {
				t.Fatalf("cookie field=%d, %v", allowed, err)
			}
			if err := database.QueryRow(`SELECT CAST(stored_at AS TEXT),hit_count FROM public_cache_entries WHERE key_digest='existing'`).Scan(&stored, &hits); err != nil || stored != "2026-09-01 12:34:56" || hits != 7 {
				t.Fatalf("cache changed: %s, %d, %v", stored, hits, err)
			}
		})
	}
}

func TestSchemaCutoverRejectsRetiredPolicyBeforeChangingSchema(t *testing.T) {
	for _, table := range []string{"public_rate_limit_rules", "public_traffic_shaper_rules", "public_waf_rules", "public_cache_rules", "public_retry_rules"} {
		t.Run(table, func(t *testing.T) {
			database := seedCutoverVersion17(t)
			columns, values := "name", "'old-policy'"
			if table == "public_rate_limit_rules" {
				columns += ",algorithm,limit_count,window_millis"
				values += ",'fixed_window',10,1000"
			}
			if _, err := database.Exec(`INSERT INTO ` + table + ` (` + columns + `) VALUES (` + values + `)`); err != nil {
				t.Fatal(err)
			}
			oldMatch := `{"cel_expression":"","builder":{"root":{"operator":"and","groups":[{"operator":"and","conditions":[{"field":"header","name":"x-mode","operator":"equals","values":["on"],"legacy_first_value":true}]}]}}}`
			if _, err := database.Exec(`UPDATE `+table+` SET match_json=? WHERE name='old-policy'`, oldMatch); err != nil {
				t.Fatal(err)
			}
			before := readNormalizedSchema(t, database)
			err := runEmbeddedMigrations(database)
			if err == nil || !strings.Contains(err.Error(), "legacy_first_value") {
				t.Fatalf("retired policy error: %v", err)
			}
			assertSchemaParity(t, "rejected policy cutover", readNormalizedSchema(t, database), before)
			var version int64
			if err := database.QueryRow(`SELECT MAX(version_id) FROM goose_db_version WHERE is_applied=1`).Scan(&version); err != nil || version != 17 {
				t.Fatalf("version=%d: %v", version, err)
			}
			var match string
			if err := database.QueryRow(`SELECT match_json FROM ` + table + ` WHERE name='old-policy'`).Scan(&match); err != nil || match != oldMatch {
				t.Fatalf("policy changed: %q, %v", match, err)
			}
			// Explicit CEL keeps first-value semantics without the retired builder flag.
			if _, err := database.Exec(`UPDATE `+table+` SET match_json=? WHERE name='old-policy'`, `{"cel_expression":"\"x-mode\" in headers && headers[\"x-mode\"].size() > 0 && headers[\"x-mode\"][0] == \"on\""}`); err != nil {
				t.Fatal(err)
			}
			if err := runEmbeddedMigrations(database); err != nil {
				t.Fatalf("CEL-only cutover: %v", err)
			}
		})
	}
}

func TestSchemaCutoverRejectsRetiredTargetBeforeChangingSchema(t *testing.T) {
	for _, targetType := range []string{"proxy", " Proxy ", "", "unknown"} {
		t.Run(targetType, func(t *testing.T) {
			database := seedCutoverVersion17(t)
			if _, err := database.Exec(`INSERT INTO public_listeners (id,name,protocol,bind_address,port) VALUES (100,'cutover','http','127.0.0.1',18080);
 INSERT INTO public_routes (id,listener_id,priority) VALUES (100,100,10);
 INSERT INTO public_route_targets (id,route_id,position,url) VALUES (100,100,0,'https://user:private-secret@example.test/ignored?key=private-secret');`); err != nil {
				t.Fatal(err)
			}
			if _, err := database.Exec(`UPDATE public_route_targets SET target_type=? WHERE id=100`, targetType); err != nil {
				t.Fatal(err)
			}
			before := readNormalizedSchema(t, database)
			err := runEmbeddedMigrations(database)
			if err == nil || !strings.Contains(err.Error(), "unsupported proxy target 100") || strings.Contains(err.Error(), "private-secret") {
				t.Fatalf("retired URL error: %v", err)
			}
			assertSchemaParity(t, "rejected target cutover", readNormalizedSchema(t, database), before)
			var version int64
			if err := database.QueryRow(`SELECT MAX(version_id) FROM goose_db_version WHERE is_applied=1`).Scan(&version); err != nil || version != 17 {
				t.Fatalf("version=%d: %v", version, err)
			}
			if _, err := database.Exec(`UPDATE public_route_targets SET url='https://example.test:8443/' WHERE id=100`); err != nil {
				t.Fatal(err)
			}
			if err := runEmbeddedMigrations(database); err != nil {
				t.Fatalf("current origin cutover: %v", err)
			}
		})
	}
}
