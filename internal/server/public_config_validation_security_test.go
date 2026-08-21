package server

import (
	"context"
	"strings"
	"testing"
)

func TestParsePublicTargetOriginAcceptsOnlyOriginComponents(t *testing.T) {
	tests := []struct {
		name    string
		value   string
		want    string
		wantErr bool
	}{
		{name: "http origin", value: "http://127.0.0.1:9000", want: "http://127.0.0.1:9000"},
		{name: "root slash normalized", value: "https://upstream.example/", want: "https://upstream.example"},
		{name: "path rejected", value: "https://upstream.example/internal", wantErr: true},
		{name: "encoded path rejected", value: "https://upstream.example/%2fsecret", wantErr: true},
		{name: "query rejected", value: "https://upstream.example?token=secret", wantErr: true},
		{name: "empty query rejected", value: "https://upstream.example?", wantErr: true},
		{name: "fragment rejected", value: "https://upstream.example#internal", wantErr: true},
		{name: "userinfo rejected", value: "https://user:pass@upstream.example", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parsePublicTargetOrigin(tt.value)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("parsePublicTargetOrigin(%q) = %v, want error", tt.value, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("parsePublicTargetOrigin(%q) error = %v", tt.value, err)
			}
			if got.String() != tt.want {
				t.Fatalf("parsePublicTargetOrigin(%q) = %q, want %q", tt.value, got, tt.want)
			}
		})
	}
}

func TestNormalizeLegacyPublicTargetOriginPreservesEffectiveUpstream(t *testing.T) {
	got, migrated, err := normalizeLegacyPublicTargetOrigin("https://user:pass@upstream.example:8443/ignored/path?token=secret#fragment")
	if err != nil {
		t.Fatalf("normalizeLegacyPublicTargetOrigin() error = %v", err)
	}
	if !migrated || got != "https://upstream.example:8443" {
		t.Fatalf("normalized legacy origin = %q, migrated=%v", got, migrated)
	}

	got, migrated, err = normalizeLegacyPublicTargetOrigin("https://upstream.example:8443")
	if err != nil || migrated || got != "https://upstream.example:8443" {
		t.Fatalf("strict origin = %q, migrated=%v, err=%v", got, migrated, err)
	}
}

func TestLoadPublicProxySnapshotMigratesLegacyTargetOrigin(t *testing.T) {
	app := NewApp(nil, newServerTestDB(t))
	header := createTestAdminSession(t, app)
	listener := seedPublicConfigTestListener(t, app.DB)
	request := testPublicRouteRequest(listener.ID, "/legacy-origin", nil)
	request.Header().Set("Cookie", header.Get("Cookie"))
	response, err := app.CreatePublicRoute(context.Background(), request)
	if err != nil {
		t.Fatalf("create route: %v", err)
	}
	targetID := response.Msg.Route.Targets[0].Id
	legacyURL := "http://user:password@127.0.0.1:9000/ignored?token=secret#fragment"
	if _, err := app.DB.ExecContext(context.Background(), "UPDATE public_route_targets SET url = ? WHERE id = ?", legacyURL, targetID); err != nil {
		t.Fatalf("seed legacy target URL: %v", err)
	}

	if _, err := app.loadPublicProxySnapshot(context.Background()); err != nil {
		t.Fatalf("loadPublicProxySnapshot() error = %v", err)
	}
	targets, err := app.DB.ListPublicRouteTargetsByRoute(context.Background(), response.Msg.Route.Id)
	if err != nil {
		t.Fatalf("list route targets: %v", err)
	}
	if len(targets) != 1 || targets[0].Url != "http://127.0.0.1:9000" {
		t.Fatalf("migrated targets = %+v", targets)
	}
	if strings.Contains(targets[0].Url, "password") || strings.Contains(targets[0].Url, "secret") {
		t.Fatalf("legacy target secret remained stored: %q", targets[0].Url)
	}
}
