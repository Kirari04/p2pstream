package server

import (
	"fmt"
	"net/http"
	"net/netip"
	"strings"

	"p2pstream/internal/config"
)

func newManagementClientIdentityResolver(cfg *config.Config) (*ClientIdentityResolver, error) {
	if cfg == nil {
		return NewClientIdentityResolver(nil, ClientIdentityResolverOptions{})
	}
	rawCIDRs := strings.FieldsFunc(cfg.ManagementTrustedProxyCIDRs, func(r rune) bool {
		return r == ',' || r == ' ' || r == '\t' || r == '\r' || r == '\n'
	})
	if len(rawCIDRs) == 0 {
		return NewClientIdentityResolver(nil, ClientIdentityResolverOptions{})
	}
	prefixes := make([]netip.Prefix, 0, len(rawCIDRs))
	for _, raw := range rawCIDRs {
		prefix, err := netip.ParsePrefix(raw)
		if err != nil {
			return nil, fmt.Errorf("parse management trusted proxy CIDR %q: %w", raw, err)
		}
		prefixes = append(prefixes, prefix)
	}
	headerName := strings.TrimSpace(cfg.ManagementClientIPHeader)
	if headerName == "" {
		headerName = "X-Forwarded-For"
	}
	headerMode := TrustedProxyHeaderMode(strings.ToLower(strings.TrimSpace(cfg.ManagementClientIPMode)))
	if headerMode == "" {
		headerMode = TrustedProxyHeaderTrustedChain
	}
	return NewClientIdentityResolver([]TrustedProxySource{{
		Name:       "management-edge",
		Provider:   TrustedProxyProviderCustom,
		Enabled:    true,
		Prefixes:   prefixes,
		HeaderName: headerName,
		HeaderMode: headerMode,
	}}, ClientIdentityResolverOptions{})
}

func (a *App) ManagementClientIdentityMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if a == nil || a.managementClientIdentityErr != nil {
			http.Error(w, "Management client identity is unavailable", http.StatusServiceUnavailable)
			return
		}
		if a.managementClientIdentity == nil {
			next.ServeHTTP(w, r)
			return
		}
		next.ServeHTTP(w, a.managementClientIdentity.ResolveRequest(r))
	})
}

func managementLoginClientKey(ctxIdentity ClientIdentity, hasIdentity bool, peerAddr string) (string, error) {
	if !hasIdentity {
		return loginThrottlePeer(peerAddr), nil
	}
	if ctxIdentity.Unknown || !ctxIdentity.Resolved.IsValid() {
		return "", fmt.Errorf("management client identity is unavailable")
	}
	return ctxIdentity.Resolved.String(), nil
}
