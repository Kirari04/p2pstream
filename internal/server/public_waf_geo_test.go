package server

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"testing"
	"time"
)

func TestPublicWafGeoRestrictionMatches(t *testing.T) {
	tests := []struct {
		name    string
		config  publicWafGeoRestrictionConfig
		country string
		known   bool
		want    bool
	}{
		{name: "disabled matches", config: publicWafGeoRestrictionConfig{Mode: publicWafGeoModeDisabled}, want: true},
		{name: "selected country", config: publicWafGeoRestrictionConfig{Mode: publicWafGeoModeSelected, CountryCodes: []string{"CH"}}, country: "ch", known: true, want: true},
		{name: "unselected country", config: publicWafGeoRestrictionConfig{Mode: publicWafGeoModeSelected, CountryCodes: []string{"CH"}}, country: "DE", known: true, want: false},
		{name: "outside selected", config: publicWafGeoRestrictionConfig{Mode: publicWafGeoModeOutsideSelected, CountryCodes: []string{"CH"}}, country: "DE", known: true, want: true},
		{name: "inside allow only", config: publicWafGeoRestrictionConfig{Mode: publicWafGeoModeOutsideSelected, CountryCodes: []string{"CH"}}, country: "CH", known: true, want: false},
		{name: "unknown applies by default", config: publicWafGeoRestrictionConfig{Mode: publicWafGeoModeSelected, CountryCodes: []string{"CH"}}, known: false, want: true},
		{name: "unknown bypasses explicitly", config: publicWafGeoRestrictionConfig{Mode: publicWafGeoModeOutsideSelected, CountryCodes: []string{"CH"}, UnknownBehavior: publicWafGeoUnknownBypassRule}, known: false, want: false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.config.matches(tc.country, tc.known); got != tc.want {
				t.Fatalf("matches(%q, %t) = %t, want %t", tc.country, tc.known, got, tc.want)
			}
		})
	}
}

func TestPublicWafEvaluateGeoRestriction(t *testing.T) {
	tests := []struct {
		name             string
		restriction      publicWafGeoRestrictionConfig
		country          string
		known            bool
		lookupErr        error
		wantAllowed      bool
		wantCountry      string
		wantCountryKnown bool
	}{
		{
			name:        "selected country applies rule",
			restriction: publicWafGeoRestrictionConfig{Mode: publicWafGeoModeSelected, CountryCodes: []string{"CH"}, UnknownBehavior: publicWafGeoUnknownApplyRule},
			country:     "ch", known: true, wantAllowed: false, wantCountry: "CH", wantCountryKnown: true,
		},
		{
			name:        "unselected country bypasses rule",
			restriction: publicWafGeoRestrictionConfig{Mode: publicWafGeoModeSelected, CountryCodes: []string{"CH"}, UnknownBehavior: publicWafGeoUnknownApplyRule},
			country:     "US", known: true, wantAllowed: true,
		},
		{
			name:        "outside selected applies away from selection",
			restriction: publicWafGeoRestrictionConfig{Mode: publicWafGeoModeOutsideSelected, CountryCodes: []string{"CH"}, UnknownBehavior: publicWafGeoUnknownApplyRule},
			country:     "US", known: true, wantAllowed: false, wantCountry: "US", wantCountryKnown: true,
		},
		{
			name:        "unknown lookup applies fail closed",
			restriction: publicWafGeoRestrictionConfig{Mode: publicWafGeoModeSelected, CountryCodes: []string{"CH"}, UnknownBehavior: publicWafGeoUnknownApplyRule},
			lookupErr:   errors.New("database unavailable"), wantAllowed: false,
		},
		{
			name:        "unknown lookup bypasses explicitly",
			restriction: publicWafGeoRestrictionConfig{Mode: publicWafGeoModeSelected, CountryCodes: []string{"CH"}, UnknownBehavior: publicWafGeoUnknownBypassRule},
			lookupErr:   errors.New("database unavailable"), wantAllowed: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rule := testWafRule(1, publicWafActionBlock)
			rule.GeoRestriction = tc.restriction
			snap := testWafSnapshot(rule, nil)
			lookup := &testWafGeoLookup{country: tc.country, known: tc.known, err: tc.lookupErr}
			app := &App{GeoConfigRefresher: lookup}
			req := httptest.NewRequest(http.MethodGet, "http://example.test/private", nil)
			req = req.WithContext(WithClientIdentity(req.Context(), ClientIdentity{
				Peer:     netip.MustParseAddr("192.0.2.10"),
				Resolved: netip.MustParseAddr("198.51.100.23"),
			}))

			decision, allowed := newPublicWAF().evaluate(snap, snap.Listeners[1], req, time.Now(), app)
			if allowed != tc.wantAllowed {
				t.Fatalf("allowed = %t, want %t", allowed, tc.wantAllowed)
			}
			if tc.wantAllowed {
				if decision.Rule.ID != 0 {
					t.Fatalf("bypassed decision rule id = %d, want 0", decision.Rule.ID)
				}
				return
			}
			if !decision.GeoRestrictionApplied {
				t.Fatal("geo restriction was not attached to WAF decision")
			}
			if decision.GeoCountryCode != tc.wantCountry || decision.GeoCountryKnown != tc.wantCountryKnown {
				t.Fatalf("geo decision = (%q, %t), want (%q, %t)", decision.GeoCountryCode, decision.GeoCountryKnown, tc.wantCountry, tc.wantCountryKnown)
			}
		})
	}
}

func TestPublicWafGeoUsesResolvedDirectAndTrustedVisitorAddresses(t *testing.T) {
	lookup := &testWafGeoLookup{country: "CH", known: true}
	app := &App{GeoConfigRefresher: lookup}
	restriction := publicWafGeoRestrictionConfig{
		Mode:            publicWafGeoModeSelected,
		CountryCodes:    []string{"CH"},
		UnknownBehavior: publicWafGeoUnknownApplyRule,
	}

	direct := httptest.NewRequest(http.MethodGet, "http://example.test/", nil)
	direct.RemoteAddr = "81.2.69.142:443"
	if matched, _, known := evaluatePublicWafGeoRestriction(app, direct, restriction); !matched || !known {
		t.Fatalf("direct geo result = matched %t, known %t; want true, true", matched, known)
	}
	trusted := httptest.NewRequest(http.MethodGet, "http://example.test/", nil)
	trusted.RemoteAddr = "104.16.0.10:443"
	trusted = trusted.WithContext(WithClientIdentity(trusted.Context(), ClientIdentity{
		Peer:     netip.MustParseAddr("104.16.0.10"),
		Resolved: netip.MustParseAddr("8.8.8.8"),
		Source:   "cloudflare",
	}))
	if matched, _, known := evaluatePublicWafGeoRestriction(app, trusted, restriction); !matched || !known {
		t.Fatalf("trusted geo result = matched %t, known %t; want true, true", matched, known)
	}
	if len(lookup.addresses) != 2 || lookup.addresses[0] != netip.MustParseAddr("81.2.69.142") || lookup.addresses[1] != netip.MustParseAddr("8.8.8.8") {
		t.Fatalf("lookup addresses = %v, want direct visitor then trusted resolved visitor", lookup.addresses)
	}
}

func TestPublicWafGeoUnknownIdentityDoesNotLookUpTrustedPeer(t *testing.T) {
	lookup := &testWafGeoLookup{country: "CH", known: true}
	app := &App{GeoConfigRefresher: lookup}
	req := httptest.NewRequest(http.MethodGet, "http://example.test/", nil)
	req.RemoteAddr = "104.16.0.10:443"
	req = req.WithContext(WithClientIdentity(req.Context(), ClientIdentity{
		Peer:          netip.MustParseAddr("104.16.0.10"),
		Unknown:       true,
		Source:        "cloudflare",
		UnknownReason: ClientIdentityUnknownMalformedHeader,
	}))

	apply := publicWafGeoRestrictionConfig{Mode: publicWafGeoModeSelected, CountryCodes: []string{"CH"}, UnknownBehavior: publicWafGeoUnknownApplyRule}
	if matched, _, known := evaluatePublicWafGeoRestriction(app, req, apply); !matched || known {
		t.Fatalf("fail-closed unknown result = matched %t, known %t; want true, false", matched, known)
	}
	bypass := apply
	bypass.UnknownBehavior = publicWafGeoUnknownBypassRule
	if matched, _, known := evaluatePublicWafGeoRestriction(app, req, bypass); matched || known {
		t.Fatalf("fail-open unknown result = matched %t, known %t; want false, false", matched, known)
	}
	if len(lookup.addresses) != 0 {
		t.Fatalf("unresolved trusted identity looked up peer addresses: %v", lookup.addresses)
	}
}

func TestPublicWafGeoComposesAfterOrdinaryMatchAndBeforeAutomaticActivation(t *testing.T) {
	lookup := &testWafGeoLookup{country: "US", known: true}
	app := &App{GeoConfigRefresher: lookup, PublicWAF: newPublicWAF()}
	rule := testWafRule(1, publicWafActionBlock)
	rule.Match = mustPublicPolicyMatchCEL(t, `path.startsWith("/admin")`)
	rule.GeoRestriction = publicWafGeoRestrictionConfig{
		Mode:            publicWafGeoModeSelected,
		CountryCodes:    []string{"CH"},
		UnknownBehavior: publicWafGeoUnknownApplyRule,
	}
	rule.ActivationMode = publicWafActivationAutomatic
	rule.Triggers.MinimumRequestRate = 1
	rule.Fingerprint = publicWafRuleFingerprint(rule)
	snap := testWafSnapshot(rule, nil)
	app.PublicWAF.reconcile(snap)

	ordinaryMiss := httptest.NewRequest(http.MethodGet, "http://example.test/public", nil)
	ordinaryMiss.RemoteAddr = "81.2.69.142:443"
	if decision, allowed := app.PublicWAF.evaluate(snap, snap.Listeners[1], ordinaryMiss, time.Unix(100, 0), app); !allowed || decision.Rule.ID != 0 {
		t.Fatalf("ordinary miss decision = %#v, allowed %t; want bypass", decision, allowed)
	}
	if len(lookup.addresses) != 0 {
		t.Fatalf("ordinary match miss still performed GeoIP lookup: %v", lookup.addresses)
	}

	geoMiss := httptest.NewRequest(http.MethodGet, "http://example.test/admin", nil)
	geoMiss.RemoteAddr = "81.2.69.142:443"
	if decision, allowed := app.PublicWAF.evaluate(snap, snap.Listeners[1], geoMiss, time.Unix(101, 0), app); !allowed || decision.Rule.ID != 0 {
		t.Fatalf("geo miss decision = %#v, allowed %t; want bypass", decision, allowed)
	}
	if len(lookup.addresses) != 1 {
		t.Fatalf("geo-targeted request lookup count = %d, want 1", len(lookup.addresses))
	}
	app.PublicWAF.mu.Lock()
	runtime := app.PublicWAF.rules[rule.ID]
	hits := append([]time.Time(nil), runtime.requestHits...)
	app.PublicWAF.mu.Unlock()
	if len(hits) != 0 {
		t.Fatalf("non-targeted geo request incremented automatic activation hits: %v", hits)
	}
}

func TestPublicWafGeoPrivateAddressBecomesUnknown(t *testing.T) {
	lookup := &testWafGeoLookup{lookup: func(addr netip.Addr) (string, bool, error) {
		if !isPublicGeoIPAddress(addr) {
			return "", false, nil
		}
		return "CH", true, nil
	}}
	app := &App{GeoConfigRefresher: lookup}
	req := httptest.NewRequest(http.MethodGet, "http://example.test/", nil)
	req = req.WithContext(WithClientIdentity(req.Context(), ClientIdentity{
		Peer:     netip.MustParseAddr("10.0.0.10"),
		Resolved: netip.MustParseAddr("10.0.0.20"),
		Direct:   true,
	}))
	restriction := publicWafGeoRestrictionConfig{Mode: publicWafGeoModeSelected, CountryCodes: []string{"CH"}, UnknownBehavior: publicWafGeoUnknownBypassRule}
	if matched, code, known := evaluatePublicWafGeoRestriction(app, req, restriction); matched || known || code != "" {
		t.Fatalf("private geo result = matched %t, code %q, known %t; want false, empty, false", matched, code, known)
	}
}

type testWafGeoLookup struct {
	country   string
	known     bool
	err       error
	addresses []netip.Addr
	lookup    func(netip.Addr) (string, bool, error)
}

func (l *testWafGeoLookup) RefreshGeoIPDatabase(context.Context, string, string) (PublicGeoIPDatabaseInfo, error) {
	return PublicGeoIPDatabaseInfo{}, nil
}

func (l *testWafGeoLookup) RefreshTrustedProxySource(context.Context, TrustedProxyProvider) ([]netip.Prefix, error) {
	return nil, nil
}

func (l *testWafGeoLookup) LookupCountry(addr netip.Addr) (string, bool, error) {
	l.addresses = append(l.addresses, addr)
	if l.lookup != nil {
		return l.lookup(addr)
	}
	return l.country, l.known, l.err
}
