package server

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/netip"
	"sort"
	"strings"
)

const (
	DefaultTrustedProxyHeaderBytes = 8 << 10
	DefaultTrustedProxyMaxHops     = 32

	ClientIdentitySourceDirect = "direct"
)

// TrustedProxyHeaderMode controls how a trusted proxy's client address header
// is interpreted. Untrusted peers never reach either parser.
type TrustedProxyHeaderMode string

const (
	TrustedProxyHeaderSingleIP     TrustedProxyHeaderMode = "single_ip"
	TrustedProxyHeaderTrustedChain TrustedProxyHeaderMode = "trusted_chain"
)

// TrustedProxyProvider identifies immutable built-in provider behavior. Custom
// sources use TrustedProxyProviderCustom.
type TrustedProxyProvider string

const (
	TrustedProxyProviderCustom     TrustedProxyProvider = "custom"
	TrustedProxyProviderCloudflare TrustedProxyProvider = "cloudflare"
	TrustedProxyProviderBunny      TrustedProxyProvider = "bunny"
	TrustedProxyProviderCloudFront TrustedProxyProvider = "cloudfront"
)

// TrustedProxySource is a DB-neutral snapshot of a configured source. Disabled
// sources and their prefixes are ignored completely.
type TrustedProxySource struct {
	Name       string
	Provider   TrustedProxyProvider
	Enabled    bool
	Prefixes   []netip.Prefix
	HeaderName string
	HeaderMode TrustedProxyHeaderMode
}

type ClientIdentityResolverOptions struct {
	MaxHeaderBytes int
	MaxHops        int
}

// ClientIdentityUnknownReason is deliberately coarse: it can be logged or
// surfaced without reflecting attacker-controlled header contents.
type ClientIdentityUnknownReason string

const (
	ClientIdentityUnknownInvalidPeer       ClientIdentityUnknownReason = "invalid_peer"
	ClientIdentityUnknownMissingHeader     ClientIdentityUnknownReason = "missing_header"
	ClientIdentityUnknownMalformedHeader   ClientIdentityUnknownReason = "malformed_header"
	ClientIdentityUnknownNoUntrustedHop    ClientIdentityUnknownReason = "no_untrusted_hop"
	ClientIdentityUnknownConflictingSource ClientIdentityUnknownReason = "conflicting_sources"
)

// ClientIdentity preserves the network peer independently from the resolved
// visitor. Resolved is invalid when Unknown is true.
type ClientIdentity struct {
	Peer           netip.Addr
	Resolved       netip.Addr
	Unknown        bool
	Direct         bool
	Source         string
	MatchedSources []string
	UnknownReason  ClientIdentityUnknownReason
}

type trustedProxyResolverSource struct {
	name         string
	prefixes     []netip.Prefix
	headerName   string
	headerMode   TrustedProxyHeaderMode
	chainTrusted []netip.Prefix
}

// ClientIdentityResolver is an immutable trusted-proxy snapshot and is safe to
// share between requests.
type ClientIdentityResolver struct {
	sources        []trustedProxyResolverSource
	headerNames    []string
	maxHeaderBytes int
	maxHops        int
}

func NewClientIdentityResolver(sources []TrustedProxySource, options ClientIdentityResolverOptions) (*ClientIdentityResolver, error) {
	maxHeaderBytes := options.MaxHeaderBytes
	if maxHeaderBytes == 0 {
		maxHeaderBytes = DefaultTrustedProxyHeaderBytes
	}
	maxHops := options.MaxHops
	if maxHops == 0 {
		maxHops = DefaultTrustedProxyMaxHops
	}
	if maxHeaderBytes < 1 {
		return nil, errors.New("trusted proxy header byte limit must be positive")
	}
	if maxHops < 1 {
		return nil, errors.New("trusted proxy hop limit must be positive")
	}

	resolver := &ClientIdentityResolver{maxHeaderBytes: maxHeaderBytes, maxHops: maxHops}
	seenNames := make(map[string]struct{})
	headerNames := make(map[string]struct{})
	for _, source := range sources {
		headerName := strings.TrimSpace(source.HeaderName)
		if validHTTPFieldName(headerName) {
			headerNames[http.CanonicalHeaderKey(headerName)] = struct{}{}
		}
		if !source.Enabled {
			continue
		}
		name := strings.TrimSpace(source.Name)
		if name == "" {
			return nil, errors.New("enabled trusted proxy source has no name")
		}
		if _, ok := seenNames[name]; ok {
			return nil, fmt.Errorf("duplicate enabled trusted proxy source %q", name)
		}
		seenNames[name] = struct{}{}
		if !validHTTPFieldName(headerName) {
			return nil, fmt.Errorf("trusted proxy source %q has an invalid header name", name)
		}
		if source.HeaderMode != TrustedProxyHeaderSingleIP && source.HeaderMode != TrustedProxyHeaderTrustedChain {
			return nil, fmt.Errorf("trusted proxy source %q has an invalid header mode", name)
		}
		if len(source.Prefixes) == 0 {
			return nil, fmt.Errorf("trusted proxy source %q has no prefixes", name)
		}
		prefixes, err := normalizeTrustedProxyPrefixes(source.Prefixes)
		if err != nil {
			return nil, fmt.Errorf("trusted proxy source %q: %w", name, err)
		}
		resolver.sources = append(resolver.sources, trustedProxyResolverSource{
			name:       name,
			prefixes:   prefixes,
			headerName: http.CanonicalHeaderKey(headerName),
			headerMode: source.HeaderMode,
		})
	}
	chainTrustDomains := make(map[string][]netip.Prefix)
	for _, source := range resolver.sources {
		if source.headerMode != TrustedProxyHeaderTrustedChain {
			continue
		}
		chainTrustDomains[source.headerName] = append(chainTrustDomains[source.headerName], source.prefixes...)
	}
	for i := range resolver.sources {
		source := &resolver.sources[i]
		if source.headerMode == TrustedProxyHeaderTrustedChain {
			source.chainTrusted = deduplicatePrefixes(chainTrustDomains[source.headerName])
		}
	}
	resolver.headerNames = make([]string, 0, len(headerNames))
	for headerName := range headerNames {
		resolver.headerNames = append(resolver.headerNames, headerName)
	}
	sort.Strings(resolver.headerNames)
	return resolver, nil
}

// Resolve returns the direct peer for untrusted connections and ignores every
// client-IP header in that case. Once a peer is trusted, any missing, malformed,
// or discrepant source result fails closed to an unresolved identity.
func (r *ClientIdentityResolver) Resolve(req *http.Request) ClientIdentity {
	peer, ok := peerAddrFromRequest(req)
	if !ok {
		return ClientIdentity{Unknown: true, UnknownReason: ClientIdentityUnknownInvalidPeer}
	}
	if r == nil || len(r.sources) == 0 {
		return directClientIdentity(peer)
	}

	matching := make([]trustedProxyResolverSource, 0, 1)
	for _, source := range r.sources {
		if prefixesContain(source.prefixes, peer) {
			matching = append(matching, source)
		}
	}
	if len(matching) == 0 {
		return directClientIdentity(peer)
	}

	names := make([]string, 0, len(matching))
	for _, source := range matching {
		names = append(names, source.name)
	}
	sort.Strings(names)
	var resolved netip.Addr
	for _, source := range matching {
		candidate, reason := r.resolveSource(req.Header, source)
		if reason != "" {
			return unresolvedTrustedIdentity(peer, names, reason)
		}
		if resolved.IsValid() && resolved != candidate {
			return unresolvedTrustedIdentity(peer, names, ClientIdentityUnknownConflictingSource)
		}
		resolved = candidate
	}
	return ClientIdentity{
		Peer:           peer,
		Resolved:       resolved,
		Source:         strings.Join(names, "+"),
		MatchedSources: names,
	}
}

// ResolveRequest attaches the resolved identity to a shallow copy of req.
func (r *ClientIdentityResolver) ResolveRequest(req *http.Request) *http.Request {
	if req == nil {
		return nil
	}
	identity := r.Resolve(req)
	ctx := WithClientIdentity(req.Context(), identity)
	ctx = withClientIdentityHeaderNames(ctx, r.ConfiguredHeaderNames())
	return req.WithContext(ctx)
}

// ConfiguredHeaderNames returns every syntactically valid configured client-IP
// header, including disabled sources. The list is canonicalized, deduplicated,
// sorted, and copied for safe request-time sanitization.
func (r *ClientIdentityResolver) ConfiguredHeaderNames() []string {
	if r == nil {
		return []string{}
	}
	return append([]string(nil), r.headerNames...)
}

func (r *ClientIdentityResolver) resolveSource(header http.Header, source trustedProxyResolverSource) (netip.Addr, ClientIdentityUnknownReason) {
	values := header.Values(source.headerName)
	if len(values) == 0 {
		return netip.Addr{}, ClientIdentityUnknownMissingHeader
	}
	bytes := 0
	for _, value := range values {
		bytes += len(value)
	}
	bytes += len(values) - 1
	if bytes > r.maxHeaderBytes {
		return netip.Addr{}, ClientIdentityUnknownMalformedHeader
	}

	switch source.headerMode {
	case TrustedProxyHeaderSingleIP:
		if len(values) != 1 || strings.Contains(values[0], ",") {
			return netip.Addr{}, ClientIdentityUnknownMalformedHeader
		}
		addr, ok := parseBareIP(values[0])
		if !ok {
			return netip.Addr{}, ClientIdentityUnknownMalformedHeader
		}
		return addr, ""
	case TrustedProxyHeaderTrustedChain:
		hops := make([]netip.Addr, 0, min(r.maxHops, len(values)+1))
		for _, value := range values {
			for _, rawHop := range strings.Split(value, ",") {
				if len(hops) == r.maxHops {
					return netip.Addr{}, ClientIdentityUnknownMalformedHeader
				}
				hop, ok := parseBareIP(rawHop)
				if !ok {
					return netip.Addr{}, ClientIdentityUnknownMalformedHeader
				}
				hops = append(hops, hop)
			}
		}
		for i := len(hops) - 1; i >= 0; i-- {
			if !prefixesContain(source.chainTrusted, hops[i]) {
				return hops[i], ""
			}
		}
		return netip.Addr{}, ClientIdentityUnknownNoUntrustedHop
	default:
		return netip.Addr{}, ClientIdentityUnknownMalformedHeader
	}
}

func directClientIdentity(peer netip.Addr) ClientIdentity {
	return ClientIdentity{
		Peer:     peer,
		Resolved: peer,
		Direct:   true,
		Source:   ClientIdentitySourceDirect,
	}
}

func unresolvedTrustedIdentity(peer netip.Addr, names []string, reason ClientIdentityUnknownReason) ClientIdentity {
	names = append([]string(nil), names...)
	sort.Strings(names)
	return ClientIdentity{
		Peer:           peer,
		Unknown:        true,
		Source:         strings.Join(names, "+"),
		MatchedSources: names,
		UnknownReason:  reason,
	}
}

func peerAddrFromRequest(req *http.Request) (netip.Addr, bool) {
	if req == nil {
		return netip.Addr{}, false
	}
	value := strings.TrimSpace(req.RemoteAddr)
	if value == "" {
		return netip.Addr{}, false
	}
	if addrPort, err := netip.ParseAddrPort(value); err == nil {
		addr := addrPort.Addr()
		if addr.Zone() == "" {
			return addr.Unmap(), true
		}
		return netip.Addr{}, false
	}
	addr, err := netip.ParseAddr(value)
	if err != nil || addr.Zone() != "" {
		return netip.Addr{}, false
	}
	return addr.Unmap(), true
}

func parseBareIP(value string) (netip.Addr, bool) {
	value = strings.Trim(value, " \t")
	if value == "" || strings.ContainsAny(value, "[]") {
		return netip.Addr{}, false
	}
	addr, err := netip.ParseAddr(value)
	if err != nil || addr.Zone() != "" {
		return netip.Addr{}, false
	}
	return addr.Unmap(), true
}

func normalizeTrustedProxyPrefixes(input []netip.Prefix) ([]netip.Prefix, error) {
	prefixes := make([]netip.Prefix, 0, len(input))
	for _, prefix := range input {
		if !prefix.IsValid() || prefix.Addr().Zone() != "" {
			return nil, errors.New("invalid trusted proxy prefix")
		}
		addr := prefix.Addr().Unmap()
		bits := prefix.Bits()
		if prefix.Addr().Is4In6() {
			bits -= 96
		}
		if bits < 0 || bits > addr.BitLen() {
			return nil, errors.New("invalid trusted proxy prefix length")
		}
		prefixes = append(prefixes, netip.PrefixFrom(addr, bits).Masked())
	}
	return deduplicatePrefixes(prefixes), nil
}

func deduplicatePrefixes(input []netip.Prefix) []netip.Prefix {
	seen := make(map[netip.Prefix]struct{}, len(input))
	output := make([]netip.Prefix, 0, len(input))
	for _, prefix := range input {
		prefix = prefix.Masked()
		if _, ok := seen[prefix]; ok {
			continue
		}
		seen[prefix] = struct{}{}
		output = append(output, prefix)
	}
	sort.Slice(output, func(i, j int) bool {
		if output[i].Addr().Compare(output[j].Addr()) != 0 {
			return output[i].Addr().Less(output[j].Addr())
		}
		return output[i].Bits() < output[j].Bits()
	})
	return output
}

func prefixesContain(prefixes []netip.Prefix, addr netip.Addr) bool {
	addr = addr.Unmap()
	for _, prefix := range prefixes {
		if prefix.Contains(addr) {
			return true
		}
	}
	return false
}

func validHTTPFieldName(value string) bool {
	if value == "" {
		return false
	}
	for i := 0; i < len(value); i++ {
		c := value[i]
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') {
			continue
		}
		switch c {
		case '!', '#', '$', '%', '&', '\'', '*', '+', '-', '.', '^', '_', '`', '|', '~':
			continue
		default:
			return false
		}
	}
	return true
}

type clientIdentityContextKey struct{}
type clientIdentityHeaderNamesContextKey struct{}

func WithClientIdentity(ctx context.Context, identity ClientIdentity) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	identity.MatchedSources = append([]string(nil), identity.MatchedSources...)
	return context.WithValue(ctx, clientIdentityContextKey{}, identity)
}

func ClientIdentityFromContext(ctx context.Context) (ClientIdentity, bool) {
	if ctx == nil {
		return ClientIdentity{}, false
	}
	identity, ok := ctx.Value(clientIdentityContextKey{}).(ClientIdentity)
	identity.MatchedSources = append([]string(nil), identity.MatchedSources...)
	return identity, ok
}

func ClientIdentityPeer(ctx context.Context) (netip.Addr, bool) {
	identity, ok := ClientIdentityFromContext(ctx)
	return identity.Peer, ok && identity.Peer.IsValid()
}

func ClientIdentityResolved(ctx context.Context) (netip.Addr, bool) {
	identity, ok := ClientIdentityFromContext(ctx)
	return identity.Resolved, ok && !identity.Unknown && identity.Resolved.IsValid()
}

func ClientIdentityIsUnknown(ctx context.Context) bool {
	identity, ok := ClientIdentityFromContext(ctx)
	return !ok || identity.Unknown
}

func ClientIdentitySource(ctx context.Context) string {
	identity, ok := ClientIdentityFromContext(ctx)
	if !ok {
		return ""
	}
	return identity.Source
}

func withClientIdentityHeaderNames(ctx context.Context, names []string) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, clientIdentityHeaderNamesContextKey{}, append([]string(nil), names...))
}

// ClientIdentityHeaderNames returns a copy of all configured client-IP header
// names that must be stripped before constructing an upstream request.
func ClientIdentityHeaderNames(ctx context.Context) []string {
	if ctx == nil {
		return []string{}
	}
	names, _ := ctx.Value(clientIdentityHeaderNamesContextKey{}).([]string)
	return append([]string(nil), names...)
}
