package agent

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"testing"
)

func TestAgentDestinationPolicyAllowsWhenUnset(t *testing.T) {
	policy, err := newAgentDestinationPolicy(nil)
	if err != nil {
		t.Fatalf("newAgentDestinationPolicy() error = %v", err)
	}
	if policy != nil {
		t.Fatalf("policy = %#v, want nil", policy)
	}
}

func TestAgentDestinationPolicyExactHostnameRules(t *testing.T) {
	policy, err := newAgentDestinationPolicy([]string{"App.Internal.:443", "metrics.internal:9000-9002"})
	if err != nil {
		t.Fatalf("newAgentDestinationPolicy() error = %v", err)
	}

	tests := []struct {
		name        string
		address     string
		wantAddress string
		wantAllowed bool
	}{
		{name: "normalizes case and trailing dot", address: "app.internal:443", wantAddress: "app.internal:443", wantAllowed: true},
		{name: "allows port range", address: "metrics.internal:9001", wantAddress: "metrics.internal:9001", wantAllowed: true},
		{name: "rejects wrong port", address: "app.internal:444", wantAllowed: false},
		{name: "rejects wrong host", address: "other.internal:443", wantAllowed: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := policy.dialAddress(context.Background(), "tcp", tt.address)
			if tt.wantAllowed {
				if err != nil {
					t.Fatalf("dialAddress() error = %v", err)
				}
				if got != tt.wantAddress {
					t.Fatalf("dialAddress() = %q, want %q", got, tt.wantAddress)
				}
				return
			}
			if !errors.Is(err, errAgentDestinationForbidden) {
				t.Fatalf("dialAddress() error = %T %[1]v, want forbidden", err)
			}
		})
	}
}

func TestAgentDestinationPolicyCIDRRulesResolveAndDialCheckedIP(t *testing.T) {
	policy, err := newAgentDestinationPolicy([]string{"10.0.5.0/24:8080", "[2001:db8::/64]:8443"})
	if err != nil {
		t.Fatalf("newAgentDestinationPolicy() error = %v", err)
	}
	restoreLookup := replaceAgentDestinationLookup(func(ctx context.Context, host string) ([]netip.Addr, error) {
		switch host {
		case "svc.internal":
			return []netip.Addr{netip.MustParseAddr("10.0.5.24"), netip.MustParseAddr("198.51.100.7")}, nil
		case "v6.internal":
			return []netip.Addr{netip.MustParseAddr("2001:db8::25")}, nil
		case "outside.internal":
			return []netip.Addr{netip.MustParseAddr("10.0.6.24")}, nil
		default:
			return nil, fmt.Errorf("unexpected lookup host %q", host)
		}
	})
	t.Cleanup(restoreLookup)

	tests := []struct {
		name        string
		network     string
		address     string
		wantAddress string
		wantAllowed bool
	}{
		{name: "resolved IPv4 CIDR", network: "tcp", address: "svc.internal:8080", wantAddress: "10.0.5.24:8080", wantAllowed: true},
		{name: "honors tcp6 network", network: "tcp6", address: "v6.internal:8443", wantAddress: "[2001:db8::25]:8443", wantAllowed: true},
		{name: "rejects resolved IP outside CIDR", network: "tcp", address: "outside.internal:8080", wantAllowed: false},
		{name: "rejects wrong port", network: "tcp", address: "svc.internal:8081", wantAllowed: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := policy.dialAddress(context.Background(), tt.network, tt.address)
			if tt.wantAllowed {
				if err != nil {
					t.Fatalf("dialAddress() error = %v", err)
				}
				if got != tt.wantAddress {
					t.Fatalf("dialAddress() = %q, want %q", got, tt.wantAddress)
				}
				return
			}
			if !errors.Is(err, errAgentDestinationForbidden) {
				t.Fatalf("dialAddress() error = %T %[1]v, want forbidden", err)
			}
		})
	}
}

func TestAgentDestinationPolicyPropagatesLookupCancellation(t *testing.T) {
	policy, err := newAgentDestinationPolicy([]string{"10.0.5.0/24:8080"})
	if err != nil {
		t.Fatalf("newAgentDestinationPolicy() error = %v", err)
	}
	restoreLookup := replaceAgentDestinationLookup(func(ctx context.Context, host string) ([]netip.Addr, error) {
		return nil, context.Canceled
	})
	t.Cleanup(restoreLookup)

	_, err = policy.dialAddress(context.Background(), "tcp", "svc.internal:8080")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("dialAddress() error = %T %[1]v, want context.Canceled", err)
	}
	if errors.Is(err, errAgentDestinationForbidden) {
		t.Fatalf("dialAddress() error = %T %[1]v, should not be forbidden", err)
	}
}

func TestAgentDestinationPolicyIPLiteralRules(t *testing.T) {
	policy, err := newAgentDestinationPolicy([]string{"127.0.0.1:8080", "[::1]:8443", "[fe80::1]:443"})
	if err != nil {
		t.Fatalf("newAgentDestinationPolicy() error = %v", err)
	}
	tests := []struct {
		name        string
		address     string
		wantAllowed bool
	}{
		{name: "IPv4 literal", address: "127.0.0.1:8080", wantAllowed: true},
		{name: "IPv6 literal", address: "[::1]:8443", wantAllowed: true},
		{name: "IPv6 scoped literal", address: "[fe80::1%eth0]:443", wantAllowed: true},
		{name: "wrong IPv4 port", address: "127.0.0.1:8081", wantAllowed: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := policy.dialAddress(context.Background(), "tcp", tt.address)
			if tt.wantAllowed && err != nil {
				t.Fatalf("dialAddress() error = %v", err)
			}
			if !tt.wantAllowed && !errors.Is(err, errAgentDestinationForbidden) {
				t.Fatalf("dialAddress() error = %T %[1]v, want forbidden", err)
			}
		})
	}
}

func TestNewAgentDestinationPolicyRejectsInvalidRules(t *testing.T) {
	tests := []string{
		":443",
		"app.internal:",
		"app.internal:0",
		"app.internal:65536",
		"app.internal:9001-9000",
		"10.0.0.0/33",
		"[2001:db8::1",
		"*.internal:443",
	}
	for _, raw := range tests {
		t.Run(raw, func(t *testing.T) {
			if _, err := newAgentDestinationPolicy([]string{raw}); err == nil {
				t.Fatalf("newAgentDestinationPolicy(%q) error = nil, want error", raw)
			}
		})
	}
}

func replaceAgentDestinationLookup(fn func(context.Context, string) ([]netip.Addr, error)) func() {
	previous := agentDestinationLookupIP
	restored := false
	agentDestinationLookupIP = fn
	return func() {
		if restored {
			return
		}
		restored = true
		agentDestinationLookupIP = previous
	}
}
