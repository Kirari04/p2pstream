package cmd

import (
	"reflect"
	"testing"
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
