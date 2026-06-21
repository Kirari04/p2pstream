package db

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
)

func TestMigrationUpgradesLegacyTLSCertificateSchema(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "legacy-tls.db")
	raw, err := sql.Open("sqlite3", dbPath)
	if err != nil {
		t.Fatalf("open raw legacy db: %v", err)
	}
	legacySchema := `
	CREATE TABLE public_tls_certificates (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		listener_id INTEGER NOT NULL,
		hostname_pattern TEXT NOT NULL,
		cert_path TEXT NOT NULL,
		key_path TEXT NOT NULL,
		enabled INTEGER NOT NULL DEFAULT 1,
		created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
	);
	INSERT INTO public_tls_certificates (listener_id, hostname_pattern, cert_path, key_path, enabled)
	VALUES (1, 'example.com', '/tmp/cert.pem', '/tmp/key.pem', 1);
	`
	if _, err := raw.ExecContext(context.Background(), legacySchema); err != nil {
		t.Fatalf("create legacy TLS schema: %v", err)
	}
	if err := raw.Close(); err != nil {
		t.Fatalf("close raw legacy db: %v", err)
	}

	database, err := Open(dbPath)
	if err != nil {
		t.Fatalf("open migrated legacy db: %v", err)
	}
	defer func() { _ = database.Close() }()

	tlsColumns := tableColumns(t, database, "public_tls_certificates")
	for _, column := range []string{"source", "acme_challenge_type", "acme_ca", "acme_email", "dns_credential_id", "status", "last_error", "issued_at", "expires_at", "next_renewal_at", "last_renewal_attempt_at"} {
		if !containsString(tlsColumns, column) {
			t.Fatalf("public_tls_certificates missing column %s after migration; columns=%v", column, tlsColumns)
		}
	}
	if !indexExists(t, database, "idx_public_tls_certificates_dns_credential_id") {
		t.Fatal("expected idx_public_tls_certificates_dns_credential_id after migration")
	}
	cert, err := database.GetPublicTlsCertificate(context.Background(), 1)
	if err != nil {
		t.Fatalf("get migrated cert: %v", err)
	}
	if cert.Source != "manual" || cert.Status != "ready" {
		t.Fatalf("migrated cert source/status = %q/%q, want manual/ready", cert.Source, cert.Status)
	}
}

func TestMigrationUpgradesLegacyPublicRoutesForRedirects(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "legacy-routes.db")
	raw, err := sql.Open("sqlite3", dbPath)
	if err != nil {
		t.Fatalf("open raw legacy db: %v", err)
	}
	legacySchema := `
	CREATE TABLE public_backends (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		name TEXT NOT NULL UNIQUE,
		target_origin TEXT NOT NULL,
		enabled INTEGER NOT NULL DEFAULT 1,
		created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
	);
	CREATE TABLE public_listeners (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		name TEXT NOT NULL UNIQUE,
		bind_address TEXT NOT NULL DEFAULT '',
		port INTEGER NOT NULL,
		protocol TEXT NOT NULL,
		enabled INTEGER NOT NULL DEFAULT 1,
		default_backend_id INTEGER NOT NULL REFERENCES public_backends(id),
		created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		UNIQUE(bind_address, port)
	);
	CREATE TABLE public_routes (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		listener_id INTEGER NOT NULL REFERENCES public_listeners(id) ON DELETE CASCADE,
		priority INTEGER NOT NULL,
		host_pattern TEXT NOT NULL DEFAULT '',
		path_prefix TEXT NOT NULL DEFAULT '',
		backend_id INTEGER NOT NULL REFERENCES public_backends(id),
		enabled INTEGER NOT NULL DEFAULT 1,
		created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
	);
	CREATE INDEX idx_public_routes_listener_priority
	ON public_routes (listener_id, priority, id);
	INSERT INTO public_backends (name, target_origin) VALUES ('legacy-backend', 'https://example.com');
	INSERT INTO public_listeners (name, port, protocol, default_backend_id) VALUES ('legacy-listener', 8080, 'http', 1);
	INSERT INTO public_routes (listener_id, priority, path_prefix, backend_id, enabled) VALUES (1, 10, '/legacy', 1, 1);
	`
	if _, err := raw.ExecContext(context.Background(), legacySchema); err != nil {
		t.Fatalf("create legacy route schema: %v", err)
	}
	if err := raw.Close(); err != nil {
		t.Fatalf("close raw legacy db: %v", err)
	}

	database, err := Open(dbPath)
	if err != nil {
		t.Fatalf("open migrated legacy db: %v", err)
	}
	defer func() { _ = database.Close() }()

	routes, err := database.ListPublicRoutes(context.Background())
	if err != nil {
		t.Fatalf("list migrated routes: %v", err)
	}
	if len(routes) != 2 {
		t.Fatalf("got %d routes, want explicit route plus generated default route", len(routes))
	}
	var route PublicRoute
	var defaultRoute PublicRoute
	for _, candidate := range routes {
		if candidate.IsDefault != 0 {
			defaultRoute = candidate
		} else {
			route = candidate
		}
	}
	if route.ID == 0 || defaultRoute.ID == 0 {
		t.Fatalf("expected one explicit route and one default route, got %+v", routes)
	}
	if route.Action != "forward" || route.RedirectStatusCode != 302 || route.RedirectPreservePathSuffix != 1 || route.RedirectPreserveQuery != 1 {
		t.Fatalf("unexpected migrated redirect defaults: %+v", route)
	}
	if route.PathSecurityMode != "strict" || defaultRoute.PathSecurityMode != "strict" {
		t.Fatalf("unexpected migrated path security modes: route=%q default=%q", route.PathSecurityMode, defaultRoute.PathSecurityMode)
	}
	routeColumns := tableColumns(t, database, "public_routes")
	if !containsString(routeColumns, "path_security_mode") {
		t.Fatalf("public_routes missing path_security_mode after migration: %v", routeColumns)
	}
	for _, column := range []string{"backend_id", "fallback_backend_id", "load_balancing"} {
		if containsString(routeColumns, column) {
			t.Fatalf("public_routes still has legacy column %s after migration: %v", column, routeColumns)
		}
	}
	listenerColumns := tableColumns(t, database, "public_listeners")
	if containsString(listenerColumns, "default_backend_id") {
		t.Fatalf("public_listeners still has default_backend_id after migration: %v", listenerColumns)
	}
	if !indexExists(t, database, "idx_public_routes_listener_priority") {
		t.Fatal("expected idx_public_routes_listener_priority after route migration")
	}
	if route.TargetLoadBalancing != "round_robin" || route.IsDefault != 0 {
		t.Fatalf("unexpected migrated target fields: %+v", route)
	}
	if defaultRoute.IsDefault != 1 {
		t.Fatalf("unexpected generated default route: %+v", defaultRoute)
	}
	targets, err := database.ListPublicRouteTargets(context.Background())
	if err != nil {
		t.Fatalf("list migrated route targets: %v", err)
	}
	if len(targets) != 2 {
		t.Fatalf("got %d route targets, want explicit and default targets: %+v", len(targets), targets)
	}
	targetsByRoute := make(map[int64]PublicRouteTarget, len(targets))
	for _, target := range targets {
		targetsByRoute[target.RouteID] = target
	}
	explicitTarget, ok := targetsByRoute[route.ID]
	if !ok {
		t.Fatalf("missing explicit route target for route %d: %+v", route.ID, targets)
	}
	if explicitTarget.Url != "https://example.com" || explicitTarget.Transport != "direct" || explicitTarget.TargetType != "proxy" {
		t.Fatalf("unexpected migrated explicit target: %+v", explicitTarget)
	}
	defaultTarget, ok := targetsByRoute[defaultRoute.ID]
	if !ok {
		t.Fatalf("missing default route target for route %d: %+v", defaultRoute.ID, targets)
	}
	if defaultTarget.Url != "https://example.com" || defaultTarget.Transport != "direct" || defaultTarget.TargetType != "proxy" {
		t.Fatalf("unexpected generated default target: %+v", defaultTarget)
	}
	for _, table := range []string{"public_backends", "public_backend_agents", "public_backend_headers", "public_backend_upstream_headers", "public_route_backends"} {
		if tableExists(t, database, table) {
			t.Fatalf("legacy table %s still exists after route-target migration", table)
		}
	}
}

func TestMigrationPreservesDefaultBackendWhenRoutesAlreadyRebuilt(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "legacy-default-rebuilt-routes.db")
	raw, err := sql.Open("sqlite3", dbPath)
	if err != nil {
		t.Fatalf("open raw legacy db: %v", err)
	}
	legacySchema := `
	CREATE TABLE public_backends (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		name TEXT NOT NULL UNIQUE,
		target_origin TEXT NOT NULL,
		enabled INTEGER NOT NULL DEFAULT 1,
		created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
	);
	CREATE TABLE public_listeners (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		name TEXT NOT NULL UNIQUE,
		bind_address TEXT NOT NULL DEFAULT '',
		port INTEGER NOT NULL,
		protocol TEXT NOT NULL,
		enabled INTEGER NOT NULL DEFAULT 1,
		default_backend_id INTEGER NOT NULL REFERENCES public_backends(id),
		created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		UNIQUE(bind_address, port)
	);
	CREATE TABLE public_routes (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		listener_id INTEGER NOT NULL REFERENCES public_listeners(id) ON DELETE CASCADE,
		priority INTEGER NOT NULL,
		host_pattern TEXT NOT NULL DEFAULT '',
		path_prefix TEXT NOT NULL DEFAULT '',
		target_load_balancing TEXT NOT NULL DEFAULT 'round_robin',
		is_default INTEGER NOT NULL DEFAULT 0,
		action TEXT NOT NULL DEFAULT 'forward',
		redirect_target_mode TEXT NOT NULL DEFAULT '',
		redirect_target TEXT NOT NULL DEFAULT '',
		redirect_status_code INTEGER NOT NULL DEFAULT 302,
		redirect_preserve_path_suffix INTEGER NOT NULL DEFAULT 1,
		redirect_preserve_query INTEGER NOT NULL DEFAULT 1,
		enabled INTEGER NOT NULL DEFAULT 1,
		created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
	);
	INSERT INTO public_backends (name, target_origin) VALUES ('legacy-default', 'https://default.example.com');
	INSERT INTO public_listeners (name, port, protocol, default_backend_id) VALUES ('legacy-listener', 8080, 'http', 1);
	`
	if _, err := raw.ExecContext(context.Background(), legacySchema); err != nil {
		t.Fatalf("create partially migrated legacy schema: %v", err)
	}
	if err := raw.Close(); err != nil {
		t.Fatalf("close raw legacy db: %v", err)
	}

	database, err := Open(dbPath)
	if err != nil {
		t.Fatalf("open migrated legacy db: %v", err)
	}
	defer func() { _ = database.Close() }()

	routes, err := database.ListPublicRoutes(context.Background())
	if err != nil {
		t.Fatalf("list migrated routes: %v", err)
	}
	if len(routes) != 1 {
		t.Fatalf("got %d routes, want generated default route: %+v", len(routes), routes)
	}
	if routes[0].IsDefault != 1 {
		t.Fatalf("generated route is_default = %d, want 1: %+v", routes[0].IsDefault, routes[0])
	}
	targets, err := database.ListPublicRouteTargets(context.Background())
	if err != nil {
		t.Fatalf("list migrated route targets: %v", err)
	}
	if len(targets) != 1 {
		t.Fatalf("got %d targets, want generated default target: %+v", len(targets), targets)
	}
	if targets[0].RouteID != routes[0].ID {
		t.Fatalf("target route_id = %d, want generated route %d", targets[0].RouteID, routes[0].ID)
	}
	if targets[0].Url != "https://default.example.com" || targets[0].Transport != "direct" || targets[0].TargetType != "proxy" {
		t.Fatalf("unexpected generated default target: %+v", targets[0])
	}
	if containsString(tableColumns(t, database, "public_listeners"), "default_backend_id") {
		t.Fatal("public_listeners still has default_backend_id after migration")
	}
	for _, column := range []string{"backend_id", "fallback_backend_id", "load_balancing"} {
		if containsString(tableColumns(t, database, "public_routes"), column) {
			t.Fatalf("public_routes still has legacy column %s after migration", column)
		}
	}
	for _, table := range []string{"public_backends", "public_backend_agents", "public_backend_headers", "public_backend_upstream_headers", "public_route_backends"} {
		if tableExists(t, database, table) {
			t.Fatalf("legacy table %s still exists after route-target migration", table)
		}
	}
	assertForeignKeyCheck(t, database)
}

func TestPublicRouteTargetUpstreamHeadersRoundTrip(t *testing.T) {
	database, err := Open(filepath.Join(t.TempDir(), "p2pstream-test.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer func() { _ = database.Close() }()

	listener, err := database.CreatePublicListener(context.Background(), CreatePublicListenerParams{
		Name:        "headers-listener",
		BindAddress: "",
		Port:        18080,
		Protocol:    "http",
		Enabled:     1,
	})
	if err != nil {
		t.Fatalf("create listener: %v", err)
	}
	route, err := database.CreatePublicRoute(context.Background(), CreatePublicRouteParams{
		ListenerID:                 listener.ID,
		Priority:                   10,
		HostPattern:                "",
		PathPrefix:                 "/",
		TargetLoadBalancing:        "round_robin",
		IsDefault:                  1,
		Action:                     "forward",
		RedirectTargetMode:         "",
		RedirectTarget:             "",
		RedirectStatusCode:         302,
		RedirectPreservePathSuffix: 1,
		RedirectPreserveQuery:      1,
		PathSecurityMode:           "strict",
		Enabled:                    1,
	})
	if err != nil {
		t.Fatalf("create route: %v", err)
	}
	target, err := database.CreatePublicRouteTarget(context.Background(), CreatePublicRouteTargetParams{
		RouteID:                             route.ID,
		Name:                                "upstream-headers",
		Position:                            0,
		PriorityGroup:                       0,
		Weight:                              100,
		Enabled:                             1,
		TargetType:                          "proxy",
		Url:                                 "http://example.com",
		Transport:                           "direct",
		AgentSelectorJson:                   "{}",
		AgentLoadBalancing:                  "round_robin",
		TlsSkipVerify:                       0,
		UpstreamBasicAuthEnabled:            0,
		UpstreamBasicAuthUsername:           "",
		UpstreamBasicAuthPassword:           "",
		UpstreamResponseHeaderTimeoutMillis: 60000,
		HealthCheckEnabled:                  0,
		HealthCheckMethod:                   "GET",
		HealthCheckPath:                     "/",
		HealthCheckIntervalMillis:           10000,
		HealthCheckTimeoutMillis:            2000,
		HealthCheckHealthyThreshold:         2,
		HealthCheckUnhealthyThreshold:       2,
		HealthCheckExpectedStatusMin:        200,
		HealthCheckExpectedStatusMax:        399,
		StaticStatusCode:                    200,
		StaticResponseBody:                  "",
		StaticResponseBodyMode:              "inline",
		StaticResponseTemplateID:            sql.NullInt64{},
	})
	if err != nil {
		t.Fatalf("create target: %v", err)
	}
	otherTarget, err := database.CreatePublicRouteTarget(context.Background(), CreatePublicRouteTargetParams{
		RouteID:                             route.ID,
		Name:                                "other-upstream-headers",
		Position:                            1,
		PriorityGroup:                       0,
		Weight:                              100,
		Enabled:                             1,
		TargetType:                          "proxy",
		Url:                                 "http://other.example.com",
		Transport:                           "direct",
		AgentSelectorJson:                   "{}",
		AgentLoadBalancing:                  "round_robin",
		TlsSkipVerify:                       0,
		UpstreamBasicAuthEnabled:            0,
		UpstreamBasicAuthUsername:           "",
		UpstreamBasicAuthPassword:           "",
		UpstreamResponseHeaderTimeoutMillis: 60000,
		HealthCheckEnabled:                  0,
		HealthCheckMethod:                   "GET",
		HealthCheckPath:                     "/",
		HealthCheckIntervalMillis:           10000,
		HealthCheckTimeoutMillis:            2000,
		HealthCheckHealthyThreshold:         2,
		HealthCheckUnhealthyThreshold:       2,
		HealthCheckExpectedStatusMin:        200,
		HealthCheckExpectedStatusMax:        399,
		StaticStatusCode:                    200,
		StaticResponseBody:                  "",
		StaticResponseBodyMode:              "inline",
		StaticResponseTemplateID:            sql.NullInt64{},
	})
	if err != nil {
		t.Fatalf("create other target: %v", err)
	}
	first, err := database.CreatePublicRouteTargetUpstreamHeader(context.Background(), CreatePublicRouteTargetUpstreamHeaderParams{
		TargetID:  target.ID,
		Position:  0,
		Name:      "X-Upstream-One",
		Value:     "one",
		Sensitive: 0,
	})
	if err != nil {
		t.Fatalf("create upstream header: %v", err)
	}
	second, err := database.CreatePublicRouteTargetUpstreamHeader(context.Background(), CreatePublicRouteTargetUpstreamHeaderParams{
		TargetID:  target.ID,
		Position:  1,
		Name:      "Authorization",
		Value:     "Bearer secret",
		Sensitive: 1,
	})
	if err != nil {
		t.Fatalf("create sensitive upstream header: %v", err)
	}
	otherHeader, err := database.CreatePublicRouteTargetUpstreamHeader(context.Background(), CreatePublicRouteTargetUpstreamHeaderParams{
		TargetID:  otherTarget.ID,
		Position:  0,
		Name:      "X-Other",
		Value:     "other",
		Sensitive: 0,
	})
	if err != nil {
		t.Fatalf("create other upstream header: %v", err)
	}

	byTarget, err := database.ListPublicRouteTargetUpstreamHeadersByTarget(context.Background(), target.ID)
	if err != nil {
		t.Fatalf("list upstream headers by target: %v", err)
	}
	if len(byTarget) != 2 || byTarget[0].ID != first.ID || byTarget[1].ID != second.ID {
		t.Fatalf("unexpected upstream headers by target: %+v", byTarget)
	}
	all, err := database.ListPublicRouteTargetUpstreamHeaders(context.Background())
	if err != nil {
		t.Fatalf("list upstream headers: %v", err)
	}
	if len(all) != 3 || all[0].Name != "X-Upstream-One" || all[1].Sensitive != 1 || all[2].ID != otherHeader.ID {
		t.Fatalf("unexpected upstream headers: %+v", all)
	}
	if err := database.DeletePublicRouteTargetUpstreamHeaders(context.Background(), target.ID); err != nil {
		t.Fatalf("delete upstream headers: %v", err)
	}
	empty, err := database.ListPublicRouteTargetUpstreamHeadersByTarget(context.Background(), target.ID)
	if err != nil {
		t.Fatalf("list deleted upstream headers: %v", err)
	}
	if len(empty) != 0 {
		t.Fatalf("expected deleted upstream headers, got %+v", empty)
	}
	remaining, err := database.ListPublicRouteTargetUpstreamHeadersByTarget(context.Background(), otherTarget.ID)
	if err != nil {
		t.Fatalf("list other upstream headers: %v", err)
	}
	if len(remaining) != 1 || remaining[0].ID != otherHeader.ID || remaining[0].Name != "X-Other" || remaining[0].Value != "other" {
		t.Fatalf("other target upstream headers were not preserved: %+v", remaining)
	}
}

func TestPublicRoutePathSecurityModeRoundTrip(t *testing.T) {
	database, err := Open(filepath.Join(t.TempDir(), "p2pstream-route-mode-test.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer func() { _ = database.Close() }()

	listener, err := database.CreatePublicListener(context.Background(), CreatePublicListenerParams{
		Name:        "route-mode-listener",
		BindAddress: "",
		Port:        18081,
		Protocol:    "http",
		Enabled:     1,
	})
	if err != nil {
		t.Fatalf("create listener: %v", err)
	}
	route, err := database.CreatePublicRoute(context.Background(), CreatePublicRouteParams{
		ListenerID:                 listener.ID,
		Priority:                   10,
		HostPattern:                "",
		PathPrefix:                 "/git",
		TargetLoadBalancing:        "round_robin",
		IsDefault:                  0,
		Action:                     "forward",
		RedirectTargetMode:         "",
		RedirectTarget:             "",
		RedirectStatusCode:         302,
		RedirectPreservePathSuffix: 1,
		RedirectPreserveQuery:      1,
		PathSecurityMode:           "allow_encoded_separators",
		Enabled:                    1,
	})
	if err != nil {
		t.Fatalf("create route: %v", err)
	}
	if route.PathSecurityMode != "allow_encoded_separators" {
		t.Fatalf("created route path security mode = %q, want allow_encoded_separators", route.PathSecurityMode)
	}
	got, err := database.GetPublicRoute(context.Background(), route.ID)
	if err != nil {
		t.Fatalf("get route: %v", err)
	}
	if got.PathSecurityMode != "allow_encoded_separators" {
		t.Fatalf("got route path security mode = %q, want allow_encoded_separators", got.PathSecurityMode)
	}
	updated, err := database.UpdatePublicRoute(context.Background(), UpdatePublicRouteParams{
		ID:                         route.ID,
		ListenerID:                 listener.ID,
		Priority:                   10,
		HostPattern:                "",
		PathPrefix:                 "/git",
		TargetLoadBalancing:        "round_robin",
		IsDefault:                  0,
		Action:                     "forward",
		RedirectTargetMode:         "",
		RedirectTarget:             "",
		RedirectStatusCode:         302,
		RedirectPreservePathSuffix: 1,
		RedirectPreserveQuery:      1,
		PathSecurityMode:           "strict",
		Enabled:                    1,
	})
	if err != nil {
		t.Fatalf("update route: %v", err)
	}
	if updated.PathSecurityMode != "strict" {
		t.Fatalf("updated route path security mode = %q, want strict", updated.PathSecurityMode)
	}
}
