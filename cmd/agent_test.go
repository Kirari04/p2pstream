package cmd

import (
	"reflect"
	"testing"

	"github.com/spf13/cobra"
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
