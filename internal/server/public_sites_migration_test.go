package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"

	p2pstreamv1 "p2pstream/gen/proto/p2pstream/v1"
	"p2pstream/internal/db"
)

func TestPlanPublicSiteMigrationCopiesListenerFallbacksAndRequiresHardeningAcknowledgement(t *testing.T) {
	certPEM, keyPEM, _, err := generatePublicSelfSignedCertificatePEM("zzz.example.com", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	certPath, keyPath := filepath.Join(t.TempDir(), "site.crt"), filepath.Join(t.TempDir(), "site.key")
	if err := os.WriteFile(certPath, certPEM, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyPath, keyPEM, 0o600); err != nil {
		t.Fatal(err)
	}
	state := publicSiteMigrationState{
		Revision:  "reviewed",
		Listeners: []publicSiteMigrationListener{{ID: 7, Name: "https", Protocol: publicListenerProtocolHTTPS}},
		Certs:     []db.PublicTlsCertificate{{ID: 1, ListenerID: 7, HostnamePattern: "zzz.example.com", CertPath: certPath, KeyPath: keyPath, Enabled: 1, Source: publicTLSCertificateSourceManual}},
		Routes: []publicSiteMigrationRoute{
			{ID: 10, ListenerID: 7, Priority: 10, HostPattern: "zzz.example.com", PathPrefix: "/api", Enabled: true},
			{ID: 20, ListenerID: 7, Priority: 20, PathPrefix: "/", Enabled: true},
			{ID: 30, ListenerID: 7, Priority: 100, IsDefault: true, Enabled: true},
		},
	}
	plan := planPublicSiteMigration(state)
	preview := publicSiteMigrationPreviewProto(plan)
	if !preview.CanApply {
		t.Fatalf("preview unexpectedly blocked: %+v", preview.Issues)
	}
	if len(preview.Groups) != 2 {
		t.Fatalf("groups = %+v, want named and default", preview.Groups)
	}
	if preview.Groups[0].HostnameMode != p2pstreamv1.PublicSiteMigrationHostnameMode_PUBLIC_SITE_MIGRATION_HOSTNAME_MODE_SPECIFIC {
		t.Fatalf("group order = %+v, copies must be created before the standalone fallback is moved", preview.Groups)
	}
	var named, fallback *p2pstreamv1.PublicSiteMigrationGroup
	for _, group := range preview.Groups {
		if group.HostnameMode == p2pstreamv1.PublicSiteMigrationHostnameMode_PUBLIC_SITE_MIGRATION_HOSTNAME_MODE_DEFAULT {
			fallback = group
		} else {
			named = group
		}
	}
	if named == nil || fallback == nil {
		t.Fatalf("groups = %+v", preview.Groups)
	}
	if len(named.SourceRouteIds) != 1 || named.SourceRouteIds[0] != 10 || len(named.RouteCopies) != 2 {
		t.Fatalf("named group = %+v", named)
	}
	if named.PreservesRouteIds {
		t.Fatal("named group claims to preserve every route ID despite fallback copies")
	}
	if len(fallback.SourceRouteIds) != 2 || fallback.SourceRouteIds[0] != 20 || fallback.SourceRouteIds[1] != 30 || !fallback.PreservesRouteIds {
		t.Fatalf("default group = %+v", fallback)
	}
	warnings := make(map[string]bool)
	for _, issue := range preview.Issues {
		warnings[issue.Code] = issue.RequiresAcknowledgement
		if issue.Summary == "" || issue.Detail == "" {
			t.Fatalf("issue lacks UI text: %+v", issue)
		}
	}
	if !warnings[publicSiteMigrationWarningAuthority] || !warnings[publicSiteMigrationWarningSNI] {
		t.Fatalf("warning acknowledgements = %+v", warnings)
	}
}

func TestPlanPublicSiteMigrationBlocksUnmodeledSemantics(t *testing.T) {
	tests := []struct {
		name  string
		state publicSiteMigrationState
		code  string
	}{
		{
			name:  "legacy wildcard depth",
			state: publicSiteMigrationState{Listeners: []publicSiteMigrationListener{{ID: 1, Protocol: publicListenerProtocolHTTP}}, Routes: []publicSiteMigrationRoute{{ID: 1, ListenerID: 1, HostPattern: "*.example.com", PathPrefix: "/", Enabled: true}}},
			code:  "legacy_wildcard_semantics",
		},
		{
			name:  "copied route priority tie",
			state: publicSiteMigrationState{Listeners: []publicSiteMigrationListener{{ID: 1, Protocol: publicListenerProtocolHTTP}}, Routes: []publicSiteMigrationRoute{{ID: 1, ListenerID: 1, Priority: 10, HostPattern: "app.example.com", PathPrefix: "/api", Enabled: true}, {ID: 2, ListenerID: 1, Priority: 10, PathPrefix: "/", Enabled: true}}},
			code:  "copy_priority_tie",
		},
		{
			name:  "existing wildcard Site ownership",
			state: publicSiteMigrationState{Listeners: []publicSiteMigrationListener{{ID: 1, Protocol: publicListenerProtocolHTTP}}, Routes: []publicSiteMigrationRoute{{ID: 1, ListenerID: 1, HostPattern: "app.example.com", PathPrefix: "/", Enabled: true}}, Claims: []publicSiteMigrationClaim{{SiteID: 9, ListenerID: 1, HostnamePattern: "*.example.com"}}},
			code:  "existing_site_hostname_overlap",
		},
		{
			name:  "missing HTTPS certificate",
			state: publicSiteMigrationState{Listeners: []publicSiteMigrationListener{{ID: 1, Protocol: publicListenerProtocolHTTPS}}, Routes: []publicSiteMigrationRoute{{ID: 1, ListenerID: 1, HostPattern: "app.example.com", PathPrefix: "/", Enabled: true}}},
			code:  "https_certificate_coverage",
		},
		{
			name:  "enabled forward route without target",
			state: publicSiteMigrationState{Listeners: []publicSiteMigrationListener{{ID: 1, Protocol: publicListenerProtocolHTTP}}, Routes: []publicSiteMigrationRoute{{ID: 1, ListenerID: 1, HostPattern: "app.example.com", PathPrefix: "/", Action: publicRouteActionForward, Enabled: true}}},
			code:  "enabled_forward_without_target",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			preview := publicSiteMigrationPreviewProto(planPublicSiteMigration(test.state))
			if preview.CanApply {
				t.Fatalf("preview unexpectedly applyable: %+v", preview)
			}
			for _, issue := range preview.Issues {
				if issue.Code == test.code && issue.Severity == p2pstreamv1.PublicSiteMigrationSeverity_PUBLIC_SITE_MIGRATION_SEVERITY_BLOCKER {
					return
				}
			}
			t.Fatalf("missing blocker %q in %+v", test.code, preview.Issues)
		})
	}
}

func TestPlanPublicSiteMigrationExplainsIDNAAndFollowOnTLSBlockers(t *testing.T) {
	preview := publicSiteMigrationPreviewProto(planPublicSiteMigration(publicSiteMigrationState{
		Listeners: []publicSiteMigrationListener{{ID: 1, Name: "https", Protocol: publicListenerProtocolHTTPS}},
		Routes:    []publicSiteMigrationRoute{{ID: 44, ListenerID: 1, HostPattern: "züribadi.ch", PathPrefix: "/", Enabled: true}},
		Certs: []db.PublicTlsCertificate{{
			ID: 1, ListenerID: 1, HostnamePattern: "züribadi.ch", Enabled: 1, Source: publicTLSCertificateSourceACME,
		}},
	}))
	if preview.CanApply {
		t.Fatal("internationalized hostname migration unexpectedly applyable")
	}
	var canonical, coverage bool
	for _, issue := range preview.Issues {
		switch issue.Code {
		case "hostname_canonicalization_change":
			canonical = strings.Contains(issue.Detail, "züribadi.ch") && strings.Contains(issue.Detail, "xn--zribadi-n2a.ch")
		case "https_certificate_coverage":
			coverage = strings.Contains(issue.Detail, "xn--zribadi-n2a.ch") && strings.Contains(issue.Detail, "issuance has not produced")
		}
	}
	if !canonical || !coverage {
		t.Fatalf("migration issues do not explain canonicalization and TLS follow-up: %+v", preview.Issues)
	}
}

func TestPlanPublicSiteMigrationDefaultHTTPSRequiresSNIWarningAcknowledgement(t *testing.T) {
	preview := publicSiteMigrationPreviewProto(planPublicSiteMigration(publicSiteMigrationState{
		Listeners: []publicSiteMigrationListener{{ID: 4, Protocol: publicListenerProtocolHTTPS}},
		Routes:    []publicSiteMigrationRoute{{ID: 8, ListenerID: 4, PathPrefix: "/", Enabled: true}},
	}))
	if !preview.CanApply {
		t.Fatalf("default-only HTTPS migration unexpectedly blocked: %+v", preview.Issues)
	}
	for _, issue := range preview.Issues {
		if issue.Code == publicSiteMigrationWarningSNI && issue.RequiresAcknowledgement {
			return
		}
	}
	t.Fatalf("missing Default Site SNI acknowledgement warning: %+v", preview.Issues)
}

func TestExpandPublicSiteMigrationIDJSONPreservesOrder(t *testing.T) {
	got, changed, err := expandPublicSiteMigrationIDJSON(`[4,2]`, map[int64][]int64{4: {9, 10}, 2: {11}, 8: {12}})
	if err != nil || !changed || got != `[4,2,9,10,11]` {
		t.Fatalf("expanded JSON = %q, changed=%v, err=%v", got, changed, err)
	}
	got, changed, err = expandPublicSiteMigrationIDJSON(`[4,9]`, map[int64][]int64{4: {9}})
	if err != nil || changed || got != `[4,9]` {
		t.Fatalf("deduplicated JSON = %q, changed=%v, err=%v", got, changed, err)
	}
}

func TestPublicSiteMigrationApplyIsRevisionBoundAndCopiesProtectedConfiguration(t *testing.T) {
	app, database, cookie, listenerID := newPublicSiteMigrationTestApp(t, publicListenerProtocolHTTP)
	ctx := context.Background()
	exactResult, err := database.ExecContext(ctx, `INSERT INTO public_routes (listener_id, priority, host_pattern, path_prefix, action, redirect_target_mode, redirect_target, enabled) VALUES (?, 10, 'app.example.com', '/api', 'redirect', 'same_host_path', '/v2', 1)`, listenerID)
	if err != nil {
		t.Fatal(err)
	}
	exactID, _ := exactResult.LastInsertId()
	fallbackResult, err := database.ExecContext(ctx, `INSERT INTO public_routes (listener_id, priority, path_prefix, action, enabled, access_policy_id) VALUES (?, 20, '/', 'forward', 1, NULL)`, listenerID)
	if err != nil {
		t.Fatal(err)
	}
	fallbackID, _ := fallbackResult.LastInsertId()
	targetResult, err := database.ExecContext(ctx, `INSERT INTO public_route_targets (route_id, name, position, target_type, static_status_code, static_response_body, upstream_basic_auth_enabled, upstream_basic_auth_username, upstream_basic_auth_password) VALUES (?, 'fallback', 0, 'static', 200, 'ok', 1, 'service', 'secret-value')`, fallbackID)
	if err != nil {
		t.Fatal(err)
	}
	targetID, _ := targetResult.LastInsertId()
	if _, err := database.ExecContext(ctx, `INSERT INTO public_route_target_upstream_headers (target_id, position, name, value, sensitive) VALUES (?, 0, 'Authorization', 'Bearer secret', 1)`, targetID); err != nil {
		t.Fatal(err)
	}
	if _, err := database.ExecContext(ctx, `INSERT INTO public_route_target_response_headers (target_id, position, name, value) VALUES (?, 0, 'X-Migrated', 'yes')`, targetID); err != nil {
		t.Fatal(err)
	}
	routeScope, _ := json.Marshal([]int64{fallbackID})
	targetScope, _ := json.Marshal([]int64{targetID})
	if _, err := database.ExecContext(ctx, `INSERT INTO public_cache_rules (name, route_ids_json, target_ids_json) VALUES ('migration-cache', ?, ?)`, string(routeScope), string(targetScope)); err != nil {
		t.Fatal(err)
	}
	if _, err := database.ExecContext(ctx, `INSERT INTO public_retry_rules (name, route_ids_json, target_ids_json) VALUES ('migration-retry', ?, ?)`, string(routeScope), string(targetScope)); err != nil {
		t.Fatal(err)
	}
	before, err := app.loadPublicProxySnapshot(ctx)
	if err != nil {
		t.Fatalf("load pre-migration snapshot: %v", err)
	}
	assertPublicSiteMigrationRouteWinner(t, app, before, listenerID, "app.example.com", "/api/users", exactID)
	assertPublicSiteMigrationRouteWinner(t, app, before, listenerID, "app.example.com", "/other", fallbackID)
	assertPublicSiteMigrationRouteWinner(t, app, before, listenerID, "other.example.com", "/other", fallbackID)

	previewReq := connect.NewRequest(&p2pstreamv1.PreviewPublicSiteMigrationRequest{ListenerIds: []int64{listenerID}})
	previewReq.Header().Set("Cookie", cookie)
	preview, err := app.PreviewPublicSiteMigration(ctx, previewReq)
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	applyWithoutAcknowledgement := connect.NewRequest(&p2pstreamv1.ApplyPublicSiteMigrationRequest{Revision: preview.Msg.Revision, ListenerIds: []int64{listenerID}})
	applyWithoutAcknowledgement.Header().Set("Cookie", cookie)
	if _, err := app.ApplyPublicSiteMigration(ctx, applyWithoutAcknowledgement); connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("unacknowledged apply code = %v, err=%v", connect.CodeOf(err), err)
	}
	if got := countPublicSiteMigrationRows(t, database, `SELECT COUNT(*) FROM public_sites`); got != 0 {
		t.Fatalf("unacknowledged apply created %d Sites", got)
	}
	if got := countPublicSiteMigrationRows(t, database, `SELECT COUNT(*) FROM public_site_migrated_listeners`); got != 0 {
		t.Fatalf("unacknowledged apply marked %d listeners", got)
	}

	applyReq := connect.NewRequest(&p2pstreamv1.ApplyPublicSiteMigrationRequest{Revision: preview.Msg.Revision, ListenerIds: []int64{listenerID}, AcceptedWarningCodes: []string{publicSiteMigrationWarningAuthority}})
	applyReq.Header().Set("Cookie", cookie)
	applied, err := app.ApplyPublicSiteMigration(ctx, applyReq)
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if len(applied.Msg.CreatedSites) != 2 || len(applied.Msg.Mappings) != 3 {
		t.Fatalf("apply response = %+v", applied.Msg)
	}
	var copiedRouteID int64
	for _, mapping := range applied.Msg.Mappings {
		if mapping.SourceRouteId == fallbackID && mapping.Copied {
			copiedRouteID = mapping.DestinationRouteId
		}
	}
	if copiedRouteID == 0 {
		t.Fatalf("missing copied fallback mapping: %+v", applied.Msg.Mappings)
	}
	after := app.currentPublicSnapshot()
	assertPublicSiteMigrationRouteWinner(t, app, after, listenerID, "app.example.com", "/api/users", exactID)
	assertPublicSiteMigrationRouteWinner(t, app, after, listenerID, "app.example.com", "/other", copiedRouteID)
	assertPublicSiteMigrationRouteWinner(t, app, after, listenerID, "other.example.com", "/other", fallbackID)
	if got := countPublicSiteMigrationRows(t, database, `SELECT COUNT(*) FROM public_routes WHERE listener_id > 0 AND site_id IS NULL`); got != 0 {
		t.Fatalf("standalone route count = %d", got)
	}
	if got := countPublicSiteMigrationRows(t, database, `SELECT COUNT(*) FROM public_routes WHERE id IN (?, ?) AND listener_id = 0 AND site_id IS NOT NULL`, exactID, fallbackID); got != 2 {
		t.Fatalf("identity-preserved routes = %d", got)
	}
	var copiedTargetID int64
	var password string
	if err := database.QueryRowContext(ctx, `SELECT id, upstream_basic_auth_password FROM public_route_targets WHERE route_id = ?`, copiedRouteID).Scan(&copiedTargetID, &password); err != nil {
		t.Fatal(err)
	}
	if password != "secret-value" {
		t.Fatalf("copied password = %q", password)
	}
	if got := countPublicSiteMigrationRows(t, database, `SELECT COUNT(*) FROM public_route_target_upstream_headers WHERE target_id = ? AND value = 'Bearer secret' AND sensitive = 1`, copiedTargetID); got != 1 {
		t.Fatalf("copied sensitive headers = %d", got)
	}
	if got := countPublicSiteMigrationRows(t, database, `SELECT COUNT(*) FROM public_route_target_response_headers WHERE target_id = ? AND name = 'X-Migrated'`, copiedTargetID); got != 1 {
		t.Fatalf("copied response headers = %d", got)
	}
	for _, table := range []string{"public_cache_rules", "public_retry_rules"} {
		var routesJSON, targetsJSON string
		if err := database.QueryRowContext(ctx, `SELECT route_ids_json, target_ids_json FROM `+table+` WHERE name LIKE 'migration-%'`).Scan(&routesJSON, &targetsJSON); err != nil {
			t.Fatal(err)
		}
		assertPublicSiteMigrationJSONIDs(t, routesJSON, fallbackID, copiedRouteID)
		assertPublicSiteMigrationJSONIDs(t, targetsJSON, targetID, copiedTargetID)
	}
	if got := countPublicSiteMigrationRows(t, database, `SELECT COUNT(*) FROM public_site_migration_route_mappings`); got != 3 {
		t.Fatalf("durable route mappings = %d", got)
	}
	if got := countPublicSiteMigrationRows(t, database, `SELECT COUNT(*) FROM public_site_migration_target_mappings`); got != 2 {
		t.Fatalf("durable target mappings = %d", got)
	}
	if got := countPublicSiteMigrationRows(t, database, `SELECT COUNT(*) FROM public_site_migrated_listeners WHERE listener_id = ?`, listenerID); got != 1 {
		t.Fatalf("migration marker count = %d", got)
	}
}

func TestPublicSiteMigrationRejectsStalePreviewWithoutPartialMutation(t *testing.T) {
	app, database, cookie, listenerID := newPublicSiteMigrationTestApp(t, publicListenerProtocolHTTP)
	ctx := context.Background()
	result, err := database.ExecContext(ctx, `INSERT INTO public_routes (listener_id, priority, host_pattern, path_prefix, action, redirect_target_mode, redirect_target, enabled) VALUES (?, 10, 'app.example.com', '/', 'redirect', 'same_host_path', '/v1', 1)`, listenerID)
	if err != nil {
		t.Fatal(err)
	}
	routeID, _ := result.LastInsertId()
	previewReq := connect.NewRequest(&p2pstreamv1.PreviewPublicSiteMigrationRequest{ListenerIds: []int64{listenerID}})
	previewReq.Header().Set("Cookie", cookie)
	preview, err := app.PreviewPublicSiteMigration(ctx, previewReq)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.ExecContext(ctx, `UPDATE public_routes SET redirect_target = '/changed' WHERE id = ?`, routeID); err != nil {
		t.Fatal(err)
	}
	applyReq := connect.NewRequest(&p2pstreamv1.ApplyPublicSiteMigrationRequest{Revision: preview.Msg.Revision, ListenerIds: []int64{listenerID}, AcceptedWarningCodes: []string{publicSiteMigrationWarningAuthority}})
	applyReq.Header().Set("Cookie", cookie)
	if _, err := app.ApplyPublicSiteMigration(ctx, applyReq); connect.CodeOf(err) != connect.CodeAborted {
		t.Fatalf("stale apply code = %v, err=%v", connect.CodeOf(err), err)
	}
	if got := countPublicSiteMigrationRows(t, database, `SELECT COUNT(*) FROM public_sites`); got != 0 {
		t.Fatalf("stale apply created %d Sites", got)
	}
	if got := countPublicSiteMigrationRows(t, database, `SELECT COUNT(*) FROM public_routes WHERE id = ? AND listener_id = ? AND site_id IS NULL`, routeID, listenerID); got != 1 {
		t.Fatalf("stale apply changed standalone route: %d", got)
	}
	if got := countPublicSiteMigrationRows(t, database, `SELECT COUNT(*) FROM public_site_migrated_listeners`); got != 0 {
		t.Fatalf("stale apply marked %d listeners", got)
	}
}

func TestPublicSiteMigrationCandidateFailureRollsBackAndKeepsRuntime(t *testing.T) {
	app, database, cookie, listenerID := newPublicSiteMigrationTestApp(t, publicListenerProtocolHTTP)
	ctx := context.Background()
	result, err := database.ExecContext(ctx, `INSERT INTO public_routes (listener_id, priority, host_pattern, path_prefix, action, redirect_target_mode, redirect_target, enabled) VALUES (?, 10, 'app.example.com', '/', 'redirect', 'same_host_path', '/v1', 1)`, listenerID)
	if err != nil {
		t.Fatal(err)
	}
	routeID, _ := result.LastInsertId()
	if _, err := database.ExecContext(ctx, `INSERT INTO public_retry_rules (name, methods_json) VALUES ('invalid-candidate', 'not-json')`); err != nil {
		t.Fatal(err)
	}
	original := &publicProxySnapshot{Listeners: map[int64]publicListenerConfig{listenerID: {ID: listenerID, Protocol: publicListenerProtocolHTTP}}}
	setPublicSnapshotForTest(t, app, original)

	previewReq := connect.NewRequest(&p2pstreamv1.PreviewPublicSiteMigrationRequest{ListenerIds: []int64{listenerID}})
	previewReq.Header().Set("Cookie", cookie)
	preview, err := app.PreviewPublicSiteMigration(ctx, previewReq)
	if err != nil {
		t.Fatal(err)
	}
	applyReq := connect.NewRequest(&p2pstreamv1.ApplyPublicSiteMigrationRequest{Revision: preview.Msg.Revision, ListenerIds: []int64{listenerID}, AcceptedWarningCodes: []string{publicSiteMigrationWarningAuthority}})
	applyReq.Header().Set("Cookie", cookie)
	if _, err := app.ApplyPublicSiteMigration(ctx, applyReq); err == nil {
		t.Fatal("migration applied despite invalid runtime candidate")
	}
	if got := countPublicSiteMigrationRows(t, database, `SELECT COUNT(*) FROM public_sites`); got != 0 {
		t.Fatalf("candidate failure created %d Sites", got)
	}
	if got := countPublicSiteMigrationRows(t, database, `SELECT COUNT(*) FROM public_routes WHERE id = ? AND listener_id = ? AND site_id IS NULL`, routeID, listenerID); got != 1 {
		t.Fatalf("candidate failure changed standalone route: %d", got)
	}
	if app.currentPublicSnapshot() != original {
		t.Fatal("candidate failure replaced the live runtime snapshot")
	}
	if got := countPublicSiteMigrationRows(t, database, `SELECT COUNT(*) FROM public_site_migrated_listeners`); got != 0 {
		t.Fatalf("candidate failure marked %d listeners", got)
	}
}

func TestPublicSiteMigrationMarksSelectedEmptyListener(t *testing.T) {
	app, database, cookie, listenerID := newPublicSiteMigrationTestApp(t, publicListenerProtocolHTTP)
	ctx := context.Background()
	previewReq := connect.NewRequest(&p2pstreamv1.PreviewPublicSiteMigrationRequest{ListenerIds: []int64{listenerID}})
	previewReq.Header().Set("Cookie", cookie)
	preview, err := app.PreviewPublicSiteMigration(ctx, previewReq)
	if err != nil {
		t.Fatal(err)
	}
	if !preview.Msg.CanApply || len(preview.Msg.Groups) != 0 {
		t.Fatalf("empty listener preview = %+v", preview.Msg)
	}
	applyReq := connect.NewRequest(&p2pstreamv1.ApplyPublicSiteMigrationRequest{Revision: preview.Msg.Revision, ListenerIds: []int64{listenerID}})
	applyReq.Header().Set("Cookie", cookie)
	if _, err := app.ApplyPublicSiteMigration(ctx, applyReq); err != nil {
		t.Fatal(err)
	}
	if got := countPublicSiteMigrationRows(t, database, `SELECT COUNT(*) FROM public_site_migrated_listeners WHERE listener_id = ?`, listenerID); got != 1 {
		t.Fatalf("empty listener migration marker = %d", got)
	}
	legacyCreate := connect.NewRequest(&p2pstreamv1.CreatePublicRouteRequest{
		ListenerId: listenerID, Priority: 10, PathPrefix: "/legacy", Enabled: true,
		Action:             p2pstreamv1.PublicRouteAction_PUBLIC_ROUTE_ACTION_REDIRECT,
		RedirectTargetMode: p2pstreamv1.PublicRouteRedirectTargetMode_PUBLIC_ROUTE_REDIRECT_TARGET_MODE_SAME_HOST_PATH,
		RedirectTarget:     "/replacement",
	})
	legacyCreate.Header().Set("Cookie", cookie)
	if _, err := app.CreatePublicRoute(ctx, legacyCreate); connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("standalone create after migration code = %v, err=%v", connect.CodeOf(err), err)
	}
}

func newPublicSiteMigrationTestApp(t *testing.T, protocol string) (*App, *db.DB, string, int64) {
	t.Helper()
	database, err := db.Open(filepath.Join(t.TempDir(), "migration.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	app := NewApp(nil, database)
	cookie := createTestAdminSession(t, app).Get("Cookie")
	result, err := database.ExecContext(context.Background(), `INSERT INTO public_listeners (name, bind_address, port, protocol, enabled) VALUES ('migration-listener', '127.0.0.1', 18443, ?, 1)`, protocol)
	if err != nil {
		t.Fatal(err)
	}
	listenerID, err := result.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	return app, database, cookie, listenerID
}

func countPublicSiteMigrationRows(t *testing.T, database *db.DB, query string, args ...any) int {
	t.Helper()
	var count int
	if err := database.QueryRowContext(context.Background(), query, args...).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

func assertPublicSiteMigrationJSONIDs(t *testing.T, raw string, want ...int64) {
	t.Helper()
	var ids []int64
	if err := json.Unmarshal([]byte(raw), &ids); err != nil {
		t.Fatal(err)
	}
	seen := make(map[int64]bool, len(ids))
	for _, id := range ids {
		seen[id] = true
	}
	for _, id := range want {
		if !seen[id] {
			t.Fatalf("JSON IDs %v do not contain %d", ids, id)
		}
	}
}

func assertPublicSiteMigrationRouteWinner(t *testing.T, app *App, snapshot *publicProxySnapshot, listenerID int64, host, path string, want int64) {
	t.Helper()
	request := httptest.NewRequest(http.MethodGet, "http://"+host+path, nil)
	match, err := app.matchPublicRouteInSnapshot(snapshot, listenerID, request)
	if err != nil || match.Route.ID != want {
		t.Fatalf("route winner for %s%s = %d, err=%v, want %d", host, path, match.Route.ID, err, want)
	}
}
