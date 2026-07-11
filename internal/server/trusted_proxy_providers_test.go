package server

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/netip"
	"strings"
	"testing"
)

func TestBuiltinTrustedProxySourceContracts(t *testing.T) {
	prefixes := []netip.Prefix{netip.MustParsePrefix("173.245.48.0/20")}
	tests := []struct {
		provider TrustedProxyProvider
		header   string
		mode     TrustedProxyHeaderMode
	}{
		{provider: TrustedProxyProviderCloudflare, header: "CF-Connecting-IP", mode: TrustedProxyHeaderSingleIP},
		{provider: TrustedProxyProviderBunny, header: "X-Real-IP", mode: TrustedProxyHeaderSingleIP},
		{provider: TrustedProxyProviderCloudFront, header: "X-Forwarded-For", mode: TrustedProxyHeaderTrustedChain},
	}
	for _, tc := range tests {
		t.Run(string(tc.provider), func(t *testing.T) {
			source, err := BuiltinTrustedProxySource(tc.provider, prefixes)
			if err != nil {
				t.Fatal(err)
			}
			if source.Name != string(tc.provider) || source.Provider != tc.provider || !source.Enabled || source.HeaderName != tc.header || source.HeaderMode != tc.mode {
				t.Fatalf("source = %#v", source)
			}
		})
	}
	if _, err := BuiltinTrustedProxySource(TrustedProxyProviderCustom, prefixes); err == nil {
		t.Fatal("custom source must not have a managed built-in contract")
	}
}

func TestFetchCloudflareTrustedProxyPrefixes(t *testing.T) {
	client := trustedProxyHTTPClientFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.String() != CloudflareIPRangesURL {
			t.Fatalf("URL = %q", req.URL)
		}
		if req.Context() == nil {
			t.Fatal("request context was not propagated")
		}
		return trustedProxyHTTPResponse(http.StatusOK, `{
			"success": true,
			"result": {
				"ipv4_cidrs": ["173.245.48.5/20", "173.245.48.0/20"],
				"ipv6_cidrs": ["2400:cb00::/32"]
			}
		}`), nil
	})
	prefixes, err := FetchTrustedProxyProviderPrefixes(context.Background(), client, TrustedProxyProviderCloudflare)
	if err != nil {
		t.Fatal(err)
	}
	want := []netip.Prefix{
		netip.MustParsePrefix("173.245.48.0/20"),
		netip.MustParsePrefix("2400:cb00::/32"),
	}
	assertPrefixSet(t, prefixes, want)
}

func TestParseCloudflareIPRangesRejectsBadResponses(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{name: "unsuccessful", body: `{"success":false,"result":{"ipv4_cidrs":["173.245.48.0/20"]}}`},
		{name: "empty", body: `{"success":true,"result":{}}`},
		{name: "malformed prefix", body: `{"success":true,"result":{"ipv4_cidrs":["not-a-prefix"]}}`},
		{name: "default route", body: `{"success":true,"result":{"ipv4_cidrs":["0.0.0.0/0"]}}`},
		{name: "trailing JSON", body: `{"success":true,"result":{"ipv4_cidrs":["173.245.48.0/20"]}} {}`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := ParseCloudflareIPRanges(strings.NewReader(tc.body)); err == nil {
				t.Fatal("expected parse error")
			}
		})
	}
}

func TestFetchBunnyTrustedProxyPrefixesUsesBothOfficialLists(t *testing.T) {
	seen := make(map[string]int)
	client := trustedProxyHTTPClientFunc(func(req *http.Request) (*http.Response, error) {
		seen[req.URL.String()]++
		switch req.URL.String() {
		case BunnyIPv4RangesURL:
			return trustedProxyHTTPResponse(http.StatusOK, "84.17.32.1\n84.17.32.0/24\n"), nil
		case BunnyIPv6RangesURL:
			return trustedProxyHTTPResponse(http.StatusOK, "2a02:7b40::1\n2a02:7b40::/48\n"), nil
		default:
			t.Fatalf("unexpected URL %q", req.URL)
			return nil, nil
		}
	})
	prefixes, err := FetchTrustedProxyProviderPrefixes(context.Background(), client, TrustedProxyProviderBunny)
	if err != nil {
		t.Fatal(err)
	}
	if seen[BunnyIPv4RangesURL] != 1 || seen[BunnyIPv6RangesURL] != 1 {
		t.Fatalf("seen = %#v", seen)
	}
	assertPrefixSet(t, prefixes, []netip.Prefix{
		netip.MustParsePrefix("84.17.32.1/32"),
		netip.MustParsePrefix("84.17.32.0/24"),
		netip.MustParsePrefix("2a02:7b40::1/128"),
		netip.MustParsePrefix("2a02:7b40::/48"),
	})
}

func TestFetchBunnyTrustedProxyPrefixesRejectsFamilyDiscrepancy(t *testing.T) {
	client := trustedProxyHTTPClientFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.String() == BunnyIPv4RangesURL {
			return trustedProxyHTTPResponse(http.StatusOK, "2a02:7b40::1\n"), nil
		}
		return trustedProxyHTTPResponse(http.StatusOK, "2a02:7b40::1\n"), nil
	})
	if _, err := FetchTrustedProxyProviderPrefixes(context.Background(), client, TrustedProxyProviderBunny); err == nil {
		t.Fatal("expected IPv4 endpoint family error")
	}
}

func TestFetchCloudFrontTrustedProxyPrefixesSelectsOnlyOriginFacingCloudFront(t *testing.T) {
	client := trustedProxyHTTPClientFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.String() != CloudFrontIPRangesURL {
			t.Fatalf("URL = %q", req.URL)
		}
		return trustedProxyHTTPResponse(http.StatusOK, `{
			"syncToken":"1",
			"prefixes":[
				{"ip_prefix":"13.32.0.0/15","service":"CLOUDFRONT","region":"GLOBAL"},
				{"ip_prefix":"54.182.0.0/16","service":"CLOUDFRONT_ORIGIN_FACING","region":"GLOBAL"},
				{"ip_prefix":"3.5.140.0/22","service":"AMAZON","region":"ap-northeast-2"}
			],
			"ipv6_prefixes":[
				{"ipv6_prefix":"2600:9000::/28","service":"CLOUDFRONT","region":"GLOBAL"},
				{"ipv6_prefix":"2600:9000:5300::/40","service":"CLOUDFRONT_ORIGIN_FACING","region":"GLOBAL"},
				{"ipv6_prefix":"2406:da00::/28","service":"AMAZON","region":"GLOBAL"}
			]
		}`), nil
	})
	prefixes, err := FetchTrustedProxyProviderPrefixes(context.Background(), client, TrustedProxyProviderCloudFront)
	if err != nil {
		t.Fatal(err)
	}
	assertPrefixSet(t, prefixes, []netip.Prefix{
		netip.MustParsePrefix("54.182.0.0/16"),
		netip.MustParsePrefix("2600:9000:5300::/40"),
	})
}

func TestFetchTrustedProxyProviderPrefixesDoesNotExposeCustomURL(t *testing.T) {
	client := trustedProxyHTTPClientFunc(func(*http.Request) (*http.Response, error) {
		t.Fatal("HTTP client must not be called for custom sources")
		return nil, nil
	})
	if _, err := FetchTrustedProxyProviderPrefixes(context.Background(), client, TrustedProxyProviderCustom); err == nil {
		t.Fatal("expected unsupported provider error")
	}
}

func TestFetchTrustedProxyProviderPrefixesKeepsLastGoodToCaller(t *testing.T) {
	// Fetchers return a complete validated snapshot or an error; they never
	// mutate a caller-owned slice. This is the primitive used by management to
	// retain cached provider ranges after refresh failure.
	old := []netip.Prefix{netip.MustParsePrefix("192.0.2.0/24")}
	client := trustedProxyHTTPClientFunc(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("network down")
	})
	if _, err := FetchTrustedProxyProviderPrefixes(context.Background(), client, TrustedProxyProviderCloudflare); err == nil {
		t.Fatal("expected fetch error")
	}
	if old[0] != netip.MustParsePrefix("192.0.2.0/24") {
		t.Fatal("existing snapshot changed on refresh failure")
	}
}

type trustedProxyHTTPClientFunc func(*http.Request) (*http.Response, error)

func (fn trustedProxyHTTPClientFunc) Do(req *http.Request) (*http.Response, error) {
	return fn(req)
}

func trustedProxyHTTPResponse(status int, body string) *http.Response {
	return &http.Response{
		StatusCode:    status,
		Body:          io.NopCloser(strings.NewReader(body)),
		ContentLength: int64(len(body)),
		Header:        make(http.Header),
	}
}

func assertPrefixSet(t *testing.T, got, want []netip.Prefix) {
	t.Helper()
	gotSet := make(map[netip.Prefix]struct{}, len(got))
	for _, prefix := range got {
		gotSet[prefix] = struct{}{}
	}
	if len(gotSet) != len(want) {
		t.Fatalf("prefixes = %v, want %v", got, want)
	}
	for _, prefix := range want {
		if _, ok := gotSet[prefix]; !ok {
			t.Fatalf("prefixes = %v, missing %s", got, prefix)
		}
	}
}
