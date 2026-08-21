package server

import (
	"encoding/json"
	"fmt"
	"net/netip"
	"strings"

	"p2pstream/internal/db"
)

func trustedProxyResolverFromRows(rows []db.PublicTrustedProxySource) (*ClientIdentityResolver, error) {
	sources := make([]TrustedProxySource, 0, len(rows))
	for _, row := range rows {
		var rawCIDRs []string
		if err := json.Unmarshal([]byte(row.CidrsJson), &rawCIDRs); err != nil {
			return nil, fmt.Errorf("trusted proxy source %q has invalid CIDR JSON: %w", row.Name, err)
		}
		prefixes := make([]netip.Prefix, 0, len(rawCIDRs))
		for _, rawCIDR := range rawCIDRs {
			prefix, err := netip.ParsePrefix(strings.TrimSpace(rawCIDR))
			if err != nil || prefix.Addr().Zone() != "" {
				return nil, fmt.Errorf("trusted proxy source %q has invalid CIDR %q", row.Name, rawCIDR)
			}
			prefix = prefix.Masked()
			if prefix.Bits() == 0 {
				return nil, fmt.Errorf("trusted proxy source %q cannot trust a default route", row.Name)
			}
			prefixes = append(prefixes, prefix)
		}

		provider := TrustedProxyProvider(row.Provider)
		source := TrustedProxySource{
			Name:       row.Name,
			Provider:   provider,
			Enabled:    row.Enabled != 0,
			Prefixes:   prefixes,
			HeaderName: row.HeaderName,
			HeaderMode: TrustedProxyHeaderMode(row.HeaderMode),
		}
		if row.BuiltIn != 0 {
			if provider != TrustedProxyProviderCloudflare && provider != TrustedProxyProviderBunny && provider != TrustedProxyProviderCloudFront {
				return nil, fmt.Errorf("trusted proxy source %q has unsupported built-in provider %q", row.Name, row.Provider)
			}
			if source.Enabled {
				expected, err := BuiltinTrustedProxySource(provider, prefixes)
				if err != nil {
					return nil, fmt.Errorf("trusted proxy source %q: %w", row.Name, err)
				}
				if !strings.EqualFold(source.HeaderName, expected.HeaderName) || source.HeaderMode != expected.HeaderMode {
					return nil, fmt.Errorf("trusted proxy source %q differs from its built-in header contract", row.Name)
				}
			}
		} else if provider != TrustedProxyProviderCustom {
			return nil, fmt.Errorf("trusted proxy source %q has invalid custom provider %q", row.Name, row.Provider)
		}
		sources = append(sources, source)
	}
	return NewClientIdentityResolver(sources, ClientIdentityResolverOptions{})
}
