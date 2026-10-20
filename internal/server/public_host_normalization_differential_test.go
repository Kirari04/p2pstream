package server

import (
	"net"
	"strings"
	"testing"
)

// Frozen from 5b7d2f27976f85b2566bb0a5c11c512a0eaeb876. Keep both
// functions independent of the production normalization helpers.
func legacyNormalizeRequestHostForComparison(host string) string {
	host = strings.TrimSpace(strings.ToLower(host))
	if strings.HasPrefix(host, "[") {
		if h, _, err := net.SplitHostPort(host); err == nil {
			return legacyNormalizeBareRequestHostForComparison(h)
		}
		return legacyNormalizeBareRequestHostForComparison(strings.Trim(host, "[]"))
	}
	if h, _, err := net.SplitHostPort(host); err == nil {
		return legacyNormalizeBareRequestHostForComparison(h)
	}
	if idx := strings.LastIndex(host, ":"); idx > -1 && strings.Count(host, ":") == 1 {
		return legacyNormalizeBareRequestHostForComparison(host[:idx])
	}
	return legacyNormalizeBareRequestHostForComparison(host)
}

func legacyNormalizeBareRequestHostForComparison(host string) string {
	host = strings.TrimSpace(strings.ToLower(strings.Trim(host, "[]")))
	if strings.Contains(host, ":") {
		return host
	}
	return strings.TrimSuffix(host, ".")
}

func hostNormalizationLegacyCorpus() []struct{ name, host string } {
	return []struct{ name, host string }{
		{"empty", ""},
		{"whitespace", " \t\r\n"},
		{"dns", "example.com"},
		{"uppercase_whitespace", " \tEXAMPLE.COM.\r\n"},
		{"trailing_dot", "example.com."},
		{"double_trailing_dot", "example.com.."},
		{"dots_only", ".."},
		{"port", "example.com:443"},
		{"empty_port", "example.com:"},
		{"nonnumeric_port", "example.com:not-a-port"},
		{"negative_port", "example.com:-1"},
		{"large_port", "example.com:99999999999999999999"},
		{"empty_host_port", ":443"},
		{"port_whitespace", "example.com: 443 "},
		{"bracket_dns", "[EXAMPLE.COM.]"},
		{"bracket_dns_port", "[EXAMPLE.COM.]:443"},
		{"bracket_spaces", " [ EXAMPLE.COM.. ] "},
		{"bracket_empty", "[]"},
		{"bracket_empty_port", "[]:443"},
		{"bracket_unclosed", "[ EXAMPLE.COM.."},
		{"bracket_extra_close", "[example.com]]:443"},
		{"bracket_embedded", "ex[ample.com"},
		{"bracket_trailing_only", "example.com]"},
		{"ipv6_bracket_port", "[2001:DB8::1]:443"},
		{"ipv6_bracket", "[2001:DB8::1]"},
		{"ipv6_bare", "2001:DB8::1"},
		{"ipv6_zone_bracket", "[FE80::1%eth0]:443"},
		{"ipv6_zone_bare", "FE80::1%eth0"},
		{"multiple_colons", "EXAMPLE.COM:BAD:TAIL"},
		{"colon_only", ":"},
		{"unicode_case", "BÜCHER.İΣ.example."},
		{"unicode_space", "\u2003EXAMPLE.COM.\u00a0"},
		{"unicode_colon", "example.com\uff1a443"},
		{"invalid_utf8_bare", "a\xff\xfe.EXAMPLE."},
		{"invalid_utf8_port", "a\xff.EXAMPLE:443"},
		{"invalid_utf8_bracket", "[a\xff.EXAMPLE]:443"},
		{"nul", "ex\x00ample.com"},
		{"embedded_newline", "ex\nample.com"},
		{"long_bare", strings.Repeat("a", 4096) + ".example"},
		{"long_dns_port", strings.Repeat("a", 4096) + ".example:443"},
		{"long_colon_tail", strings.Repeat("a", 4096) + ":bad:tail"},
		{"long_port_suffix", "example.com:" + strings.Repeat("9", 4096)},
	}
}

func TestNormalizeRequestHostMatchesFrozenLegacy(t *testing.T) {
	for _, tc := range hostNormalizationLegacyCorpus() {
		t.Run(tc.name, func(t *testing.T) {
			if got, want := normalizeRequestHost(tc.host), legacyNormalizeRequestHostForComparison(tc.host); got != want {
				t.Fatalf("normalizeRequestHost(%q) = %q, legacy %q", tc.host, got, want)
			}
		})
	}
}

func FuzzNormalizeRequestHostMatchesFrozenLegacy(f *testing.F) {
	for _, tc := range hostNormalizationLegacyCorpus() {
		f.Add(tc.host)
	}
	f.Fuzz(func(t *testing.T, host string) {
		if got, want := normalizeRequestHost(host), legacyNormalizeRequestHostForComparison(host); got != want {
			t.Fatalf("normalizeRequestHost(%q) = %q, legacy %q", host, got, want)
		}
	})
}
