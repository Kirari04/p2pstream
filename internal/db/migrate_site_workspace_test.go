package db

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"

	"github.com/pressly/goose/v3"

	"p2pstream/internal/db/migrations"
)

func TestSiteWorkspaceMigrationRollsBackAndPreservesReferencedConfiguration(t *testing.T) {
	ctx := context.Background()
	database, err := sql.Open("sqlite3", filepath.Join(t.TempDir(), "workspace-upgrade.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	database.SetMaxOpenConns(4)
	provider, err := goose.NewProvider(goose.DialectSQLite3, database, migrations.FS,
		goose.WithGoMigrations(
			goose.NewGoMigration(17, &goose.GoFunc{RunTx: repairEmptyManagedUpdateSchema}, nil),
			goose.NewGoMigration(18, &goose.GoFunc{RunTx: applySchemaCutover}, nil),
		))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := provider.UpTo(ctx, 19); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`
		INSERT INTO public_listeners (id,name,port,protocol) VALUES (71,'legacy-https',443,'https');
		INSERT INTO public_access_providers (id,name,forward_auth_url) VALUES (72,'auth','https://auth.example.test/check');
		INSERT INTO public_access_policies (id,name,provider_id) VALUES (73,'protected',72);
		INSERT INTO public_sites (id,listener_id,name,enabled) VALUES (74,71,'legacy-site',1);
		INSERT INTO public_site_hosts (id,site_id,listener_id,hostname_pattern,role,behavior) VALUES (75,74,71,'app.example.test','primary','serve');
		INSERT INTO public_routes (id,listener_id,site_id,priority,path_prefix,action,access_policy_id,enabled) VALUES (76,71,74,10,'/','forward',73,1);
		INSERT INTO public_route_targets (id,route_id,name,position,target_type,url) VALUES (77,76,'origin',0,'proxy','https://origin.example.test');
		INSERT INTO public_route_target_upstream_headers (target_id,position,name,value,sensitive) VALUES (77,0,'Authorization','secret',1);
		INSERT INTO public_route_target_response_headers (target_id,position,name,value) VALUES (77,0,'X-Site','legacy');
	`); err != nil {
		t.Fatal(err)
	}

	injected := errors.New("injected pre-commit failure")
	if err := applySiteWorkspaceFoundationWithHook(ctx, database, func() error { return injected }); !errors.Is(err, injected) {
		t.Fatalf("migration failure = %v, want injected failure", err)
	}
	var oldListenerID int64
	if err := database.QueryRow(`SELECT listener_id FROM public_sites WHERE id=74`).Scan(&oldListenerID); err != nil || oldListenerID != 71 {
		t.Fatalf("rollback did not restore old Site schema/data: listener=%d err=%v", oldListenerID, err)
	}
	var publishedColumn int
	if err := database.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('public_sites') WHERE name='published'`).Scan(&publishedColumn); err != nil || publishedColumn != 0 {
		t.Fatalf("rollback left partial schema: published column=%d err=%v", publishedColumn, err)
	}

	if err := applySiteWorkspaceFoundation(ctx, database); err != nil {
		t.Fatalf("retry migration: %v", err)
	}
	var published, routeListener, policyID int64
	var canonical, secret, responseValue string
	if err := database.QueryRow(`SELECT published,canonical_hostname FROM public_sites WHERE id=74`).Scan(&published, &canonical); err != nil {
		t.Fatal(err)
	}
	if err := database.QueryRow(`SELECT listener_id,access_policy_id FROM public_routes WHERE id=76`).Scan(&routeListener, &policyID); err != nil {
		t.Fatal(err)
	}
	if err := database.QueryRow(`SELECT value FROM public_route_target_upstream_headers WHERE target_id=77 AND sensitive=1`).Scan(&secret); err != nil {
		t.Fatal(err)
	}
	if err := database.QueryRow(`SELECT value FROM public_route_target_response_headers WHERE target_id=77`).Scan(&responseValue); err != nil {
		t.Fatal(err)
	}
	if published != 1 || canonical != "app.example.test" || routeListener != 0 || policyID != 73 || secret != "secret" || responseValue != "legacy" {
		t.Fatalf("migrated values: published=%d canonical=%q listener=%d policy=%d secret=%q response=%q", published, canonical, routeListener, policyID, secret, responseValue)
	}
	var bindingCount, targetCount int
	if err := database.QueryRow(`SELECT COUNT(*) FROM public_site_listener_bindings WHERE site_id=74 AND listener_id=71 AND behavior='serve'`).Scan(&bindingCount); err != nil {
		t.Fatal(err)
	}
	if err := database.QueryRow(`SELECT COUNT(*) FROM public_route_targets WHERE id=77 AND route_id=76`).Scan(&targetCount); err != nil {
		t.Fatal(err)
	}
	if bindingCount != 1 || targetCount != 1 {
		t.Fatalf("binding/target counts = %d/%d", bindingCount, targetCount)
	}
	assertForeignKeyCheck(t, &DB{DB: database})
}
