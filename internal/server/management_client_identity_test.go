package server

import (
	"net/http"
	"net/http/httptest"
	"net/netip"
	"testing"

	"p2pstream/internal/config"
)

func TestManagementClientIdentityMiddlewareTrustsOnlyConfiguredProxy(t *testing.T) {
	app := NewApp(&config.Config{
		ManagementTrustedProxyCIDRs: "192.0.2.0/24",
		ManagementClientIPHeader:    "X-Forwarded-For",
		ManagementClientIPMode:      "trusted_chain",
	}, nil)
	var got ClientIdentity
	handler := app.ManagementClientIdentityMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got, _ = ClientIdentityFromContext(r.Context())
		w.WriteHeader(http.StatusNoContent)
	}))

	trusted := httptest.NewRequest(http.MethodGet, "http://management.test/", nil)
	trusted.RemoteAddr = "192.0.2.10:443"
	trusted.Header.Set("X-Forwarded-For", "203.0.113.9")
	handler.ServeHTTP(httptest.NewRecorder(), trusted)
	if got.Resolved != netip.MustParseAddr("203.0.113.9") || got.Direct || got.Unknown {
		t.Fatalf("trusted proxy identity = %#v", got)
	}

	untrusted := httptest.NewRequest(http.MethodGet, "http://management.test/", nil)
	untrusted.RemoteAddr = "198.51.100.7:443"
	untrusted.Header.Set("X-Forwarded-For", "203.0.113.99")
	handler.ServeHTTP(httptest.NewRecorder(), untrusted)
	if got.Resolved != netip.MustParseAddr("198.51.100.7") || !got.Direct {
		t.Fatalf("untrusted proxy spoof was accepted: %#v", got)
	}
}

func TestManagementLoginClientKeyRejectsUnknownTrustedIdentity(t *testing.T) {
	if _, err := managementLoginClientKey(ClientIdentity{Unknown: true}, true, "192.0.2.1:443"); err == nil {
		t.Fatal("unknown trusted-proxy identity was accepted")
	}
	got, err := managementLoginClientKey(ClientIdentity{}, false, "198.51.100.7:1234")
	if err != nil || got != "198.51.100.7" {
		t.Fatalf("direct login key = %q, %v", got, err)
	}
}
