package agent

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"strconv"
	"strings"
)

var (
	errAgentDestinationForbidden = errors.New("agent destination forbidden by allowlist")
	agentDestinationLookupIP     = lookupAgentDestinationIP
	defaultAgentDestinationRules = []string{"127.0.0.0/8", "::1/128"}
)

type agentDestinationPolicy struct {
	rules          []agentDestinationRule
	hasPrefixRules bool
	allowAny       bool
}

type agentDestinationRule struct {
	hostname  string
	prefix    netip.Prefix
	hasPrefix bool
	ports     agentDestinationPortRange
}

type agentDestinationPortRange struct {
	any bool
	min int
	max int
}

type agentDestinationForbiddenError struct {
	reason string
	cause  error
}

func (e *agentDestinationForbiddenError) Error() string {
	if e.reason == "" {
		return errAgentDestinationForbidden.Error()
	}
	return errAgentDestinationForbidden.Error() + ": " + e.reason
}

func (e *agentDestinationForbiddenError) Is(target error) bool {
	return target == errAgentDestinationForbidden
}

func (e *agentDestinationForbiddenError) Unwrap() error {
	return e.cause
}

func newAgentDestinationPolicy(rawRules []string, allowAny ...bool) (*agentDestinationPolicy, error) {
	policy := &agentDestinationPolicy{allowAny: len(allowAny) > 0 && allowAny[0]}
	if !policy.allowAny && !hasNonEmptyAgentDestinationRule(rawRules) {
		rawRules = defaultAgentDestinationRules
	}
	for _, raw := range rawRules {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		rule, err := parseAgentAllowTarget(raw)
		if err != nil {
			return nil, fmt.Errorf("invalid agent allow target %q: %w", raw, err)
		}
		if rule.hasPrefix {
			policy.hasPrefixRules = true
		}
		policy.rules = append(policy.rules, rule)
	}
	if policy.allowAny && len(policy.rules) > 0 {
		return nil, errors.New("AGENT_ALLOW_ANY_TARGET=true cannot be combined with AGENT_ALLOW_TARGETS or --allow-target")
	}
	return policy, nil
}

func hasNonEmptyAgentDestinationRule(rawRules []string) bool {
	for _, raw := range rawRules {
		if strings.TrimSpace(raw) != "" {
			return true
		}
	}
	return false
}

func parseAgentAllowTarget(raw string) (agentDestinationRule, error) {
	host, portSpec, hasPort, err := splitAgentAllowTarget(raw)
	if err != nil {
		return agentDestinationRule{}, err
	}
	ports := agentDestinationPortRange{any: true}
	if hasPort {
		ports, err = parseAgentDestinationPortRange(portSpec)
		if err != nil {
			return agentDestinationRule{}, err
		}
	}

	host = strings.TrimSpace(host)
	if host == "" {
		return agentDestinationRule{}, errors.New("host is required")
	}
	if strings.ContainsAny(host, " \t\r\n") {
		return agentDestinationRule{}, errors.New("host must not contain whitespace")
	}

	rule := agentDestinationRule{ports: ports}
	if prefix, err := netip.ParsePrefix(host); err == nil {
		rule.prefix = normalizeAgentDestinationPrefix(prefix)
		rule.hasPrefix = true
		return rule, nil
	}
	if strings.Contains(host, "/") {
		return agentDestinationRule{}, errors.New("CIDR prefix is invalid")
	}
	if addr, err := netip.ParseAddr(host); err == nil {
		rule.prefix = prefixForAgentDestinationAddr(addr)
		rule.hasPrefix = true
		return rule, nil
	}
	hostname := normalizeAgentDestinationHost(host)
	if hostname == "" {
		return agentDestinationRule{}, errors.New("hostname is required")
	}
	if strings.Contains(hostname, "*") {
		return agentDestinationRule{}, errors.New("wildcard hostnames are not supported")
	}
	if strings.ContainsAny(hostname, "[]/:") {
		return agentDestinationRule{}, errors.New("hostname is invalid")
	}
	rule.hostname = hostname
	return rule, nil
}

func splitAgentAllowTarget(raw string) (host string, portSpec string, hasPort bool, err error) {
	value := strings.TrimSpace(raw)
	if value == "" {
		return "", "", false, errors.New("target is required")
	}
	if strings.HasPrefix(value, "[") {
		end := strings.LastIndex(value, "]")
		if end < 0 {
			return "", "", false, errors.New("bracketed host is missing closing bracket")
		}
		host = value[1:end]
		rest := value[end+1:]
		if rest == "" {
			return host, "", false, nil
		}
		if strings.HasPrefix(rest, ":") && len(rest) > 1 {
			return host, rest[1:], true, nil
		}
		return "", "", false, errors.New("bracketed host must be followed by an optional port")
	}
	if strings.Count(value, ":") == 1 {
		before, after, _ := strings.Cut(value, ":")
		if before == "" {
			return "", "", false, errors.New("host is required")
		}
		if after == "" {
			return "", "", false, errors.New("port is required")
		}
		return before, after, true, nil
	}
	return value, "", false, nil
}

func parseAgentDestinationPortRange(spec string) (agentDestinationPortRange, error) {
	spec = strings.TrimSpace(spec)
	if spec == "" {
		return agentDestinationPortRange{}, errors.New("port is required")
	}
	if startText, endText, ok := strings.Cut(spec, "-"); ok {
		start, err := parseAgentDestinationPort(startText)
		if err != nil {
			return agentDestinationPortRange{}, err
		}
		end, err := parseAgentDestinationPort(endText)
		if err != nil {
			return agentDestinationPortRange{}, err
		}
		if start > end {
			return agentDestinationPortRange{}, errors.New("port range start must be less than or equal to end")
		}
		return agentDestinationPortRange{min: start, max: end}, nil
	}
	port, err := parseAgentDestinationPort(spec)
	if err != nil {
		return agentDestinationPortRange{}, err
	}
	return agentDestinationPortRange{min: port, max: port}, nil
}

func parseAgentDestinationPort(spec string) (int, error) {
	spec = strings.TrimSpace(spec)
	if spec == "" {
		return 0, errors.New("port is required")
	}
	for _, r := range spec {
		if r < '0' || r > '9' {
			return 0, fmt.Errorf("port %q is invalid", spec)
		}
	}
	port, err := strconv.Atoi(spec)
	if err != nil {
		return 0, fmt.Errorf("port %q is invalid", spec)
	}
	if port < 1 || port > 65535 {
		return 0, fmt.Errorf("port %d is outside 1-65535", port)
	}
	return port, nil
}

func (p *agentDestinationPolicy) dialAddress(ctx context.Context, network string, address string) (string, error) {
	if p != nil && p.allowAny {
		return address, nil
	}
	if p == nil || len(p.rules) == 0 {
		return "", agentDestinationForbidden("no destinations are allowed by the local agent policy", nil)
	}
	host, portText, err := net.SplitHostPort(strings.TrimSpace(address))
	if err != nil {
		return "", agentDestinationForbidden("requested address must be host:port", err)
	}
	port, err := parseAgentDestinationPort(portText)
	if err != nil {
		return "", agentDestinationForbidden("requested address port is invalid", err)
	}
	host = strings.TrimSpace(host)
	if host == "" {
		return "", agentDestinationForbidden("requested address host is required", nil)
	}

	if addr, err := netip.ParseAddr(host); err == nil {
		addr = normalizeAgentDestinationAddr(addr)
		if p.matchesAddr(addr, port) {
			return address, nil
		}
		return "", agentDestinationForbidden("requested IP address is not allowed", nil)
	}

	hostname := normalizeAgentDestinationHost(host)
	if hostname == "" || strings.ContainsAny(hostname, "[]/:") {
		return "", agentDestinationForbidden("requested hostname is invalid", nil)
	}
	if p.matchesHostname(hostname, port) {
		return address, nil
	}
	if !p.hasPrefixRules {
		return "", agentDestinationForbidden("requested hostname is not allowed", nil)
	}

	addrs, err := agentDestinationLookupIP(ctx, host)
	if err != nil {
		if isTimeoutError(err) || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) ||
			(ctx != nil && (errors.Is(ctx.Err(), context.Canceled) || errors.Is(ctx.Err(), context.DeadlineExceeded))) {
			return "", err
		}
		return "", agentDestinationForbidden("requested hostname did not resolve to an allowed address", err)
	}
	for _, addr := range addrs {
		addr = normalizeAgentDestinationAddr(addr)
		if !agentDestinationAddrMatchesNetwork(addr, network) {
			continue
		}
		if p.matchesAddr(addr, port) {
			return net.JoinHostPort(addr.String(), strconv.Itoa(port)), nil
		}
	}
	return "", agentDestinationForbidden("requested hostname resolved outside the allowlist", nil)
}

func (p *agentDestinationPolicy) matchesHostname(hostname string, port int) bool {
	for _, rule := range p.rules {
		if rule.hostname == "" || rule.hostname != hostname {
			continue
		}
		if rule.ports.matches(port) {
			return true
		}
	}
	return false
}

func (p *agentDestinationPolicy) matchesAddr(addr netip.Addr, port int) bool {
	for _, rule := range p.rules {
		if !rule.hasPrefix {
			continue
		}
		if rule.prefix.Contains(addr) && rule.ports.matches(port) {
			return true
		}
	}
	return false
}

func (r agentDestinationPortRange) matches(port int) bool {
	if r.any {
		return true
	}
	return port >= r.min && port <= r.max
}

func normalizeAgentDestinationHost(host string) string {
	host = strings.ToLower(strings.TrimSpace(host))
	host = strings.TrimSuffix(host, ".")
	return host
}

func normalizeAgentDestinationAddr(addr netip.Addr) netip.Addr {
	return addr.Unmap().WithZone("")
}

func normalizeAgentDestinationPrefix(prefix netip.Prefix) netip.Prefix {
	addr := normalizeAgentDestinationAddr(prefix.Addr())
	bits := prefix.Bits()
	if prefix.Addr().Is4In6() && addr.Is4() {
		bits -= 96
	}
	if addr.Is4() && bits > 32 {
		bits = 32
	}
	if bits < 0 {
		bits = 0
	}
	return netip.PrefixFrom(addr, bits).Masked()
}

func prefixForAgentDestinationAddr(addr netip.Addr) netip.Prefix {
	addr = normalizeAgentDestinationAddr(addr)
	if addr.Is4() {
		return netip.PrefixFrom(addr, 32)
	}
	return netip.PrefixFrom(addr, 128)
}

func agentDestinationForbidden(reason string, cause error) error {
	return &agentDestinationForbiddenError{reason: reason, cause: cause}
}

func agentDestinationAddrMatchesNetwork(addr netip.Addr, network string) bool {
	switch strings.ToLower(strings.TrimSpace(network)) {
	case "tcp4":
		return addr.Is4()
	case "tcp6":
		return addr.Is6()
	default:
		return true
	}
}

func lookupAgentDestinationIP(ctx context.Context, host string) ([]netip.Addr, error) {
	ips, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil {
		return nil, err
	}
	addrs := make([]netip.Addr, 0, len(ips))
	for _, ip := range ips {
		if ip.IP == nil {
			continue
		}
		addr, ok := netip.AddrFromSlice(ip.IP)
		if !ok {
			continue
		}
		addrs = append(addrs, normalizeAgentDestinationAddr(addr))
	}
	if len(addrs) == 0 {
		return nil, errors.New("hostname did not resolve to IP addresses")
	}
	return addrs, nil
}
