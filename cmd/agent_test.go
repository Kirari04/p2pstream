package cmd

import (
	"reflect"
	"testing"

	"github.com/spf13/cobra"

	"p2pstream/internal/tunnel"
)

func TestSplitAgentAllowTargets(t *testing.T) {
	got := splitAgentAllowTargets("10.0.0.0/24:8080, app.internal:443\nmetrics.internal:9000-9010\t[2001:db8::/64]:8443")
	want := []string{
		"10.0.0.0/24:8080",
		"app.internal:443",
		"metrics.internal:9000-9010",
		"[2001:db8::/64]:8443",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("splitAgentAllowTargets() = %#v, want %#v", got, want)
	}
}

func TestAgentTunnelCapacityDefaultsToAdaptiveWhenLimitIsAbsent(t *testing.T) {
	t.Setenv("TUNNEL_MAX_CONCURRENT_REQUESTS", "")
	command := &cobra.Command{}
	command.Flags().Int64("tunnel-max-concurrent-requests", tunnel.DefaultMaxConcurrentAgentRequests, "")

	got, err := agentTunnelMaxConcurrentRequests(command)
	if err != nil {
		t.Fatal(err)
	}
	if got != tunnel.MaxConcurrentAgentRequestsLimit || !agentTunnelCapacityAdaptive(command) {
		t.Fatalf("default capacity = %d adaptive=%t, want old-peer-safe %d adaptive", got, agentTunnelCapacityAdaptive(command), tunnel.MaxConcurrentAgentRequestsLimit)
	}
}

func TestAgentTunnelCapacityExplicitValueRemainsFixed(t *testing.T) {
	t.Setenv("TUNNEL_MAX_CONCURRENT_REQUESTS", "512")
	command := &cobra.Command{}
	command.Flags().Int64("tunnel-max-concurrent-requests", tunnel.DefaultMaxConcurrentAgentRequests, "")

	got, err := agentTunnelMaxConcurrentRequests(command)
	if err != nil {
		t.Fatal(err)
	}
	if got != 512 || agentTunnelCapacityAdaptive(command) {
		t.Fatalf("explicit capacity = %d adaptive=%t, want 512 fixed", got, agentTunnelCapacityAdaptive(command))
	}
}

func TestResolvedAgentAllowAnyTargetHonorsExplicitFalse(t *testing.T) {
	t.Setenv("AGENT_ALLOW_ANY_TARGET", "true")
	command := &cobra.Command{}
	command.Flags().Bool("allow-any-target", false, "")

	got, err := resolvedAgentAllowAnyTarget(command)
	if err != nil {
		t.Fatalf("resolve inherited policy: %v", err)
	}
	if !got {
		t.Fatal("environment policy was not used when the flag was absent")
	}
	if err := command.Flags().Set("allow-any-target", "false"); err != nil {
		t.Fatalf("set explicit false: %v", err)
	}
	got, err = resolvedAgentAllowAnyTarget(command)
	if err != nil {
		t.Fatalf("resolve explicit policy: %v", err)
	}
	if got {
		t.Fatal("explicit --allow-any-target=false did not override the environment")
	}
}
