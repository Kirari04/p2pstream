package cmd

import (
	"strings"
	"testing"

	"p2pstream/internal/serverupdate"
)

func TestValidateServerUpdaterStatus(t *testing.T) {
	tests := []struct {
		name   string
		status serverupdate.RuntimeStatus
		want   string
	}{
		{
			name:   "healthy",
			status: serverupdate.RuntimeStatus{Ready: true, InstanceID: "expected"},
		},
		{
			name:   "not ready",
			status: serverupdate.RuntimeStatus{InstanceID: "expected"},
			want:   "server is not healthy: readiness is false",
		},
		{
			name:   "wrong instance",
			status: serverupdate.RuntimeStatus{Ready: true, InstanceID: "other"},
			want:   "server is not healthy: server instance mismatch",
		},
		{
			name:   "missing instance",
			status: serverupdate.RuntimeStatus{Ready: true},
			want:   "server is not healthy: server instance mismatch",
		},
		{
			name:   "maintenance",
			status: serverupdate.RuntimeStatus{Ready: true, InstanceID: "expected", Maintenance: true},
			want:   "server is not healthy: maintenance is active",
		},
		{
			name:   "unknown blocker still refuses",
			status: serverupdate.RuntimeStatus{Ready: true, InstanceID: "expected", Blocked: []string{"future blocker"}},
			want:   "server is not healthy: blocked: unrecognized runtime blocker (details omitted)",
		},
		{
			name:   "empty blocker still refuses",
			status: serverupdate.RuntimeStatus{Ready: true, InstanceID: "expected", Blocked: []string{""}},
			want:   "server is not healthy: blocked: unrecognized runtime blocker (details omitted)",
		},
		{
			name:   "all failed conditions reported",
			status: serverupdate.RuntimeStatus{InstanceID: "other", Maintenance: true, Blocked: []string{"Database schema is unavailable"}},
			want:   "server is not healthy: readiness is false; server instance mismatch; maintenance is active; blocked: Database schema is unavailable",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateServerUpdaterStatus(tt.status, "expected")
			if tt.want == "" {
				if err != nil {
					t.Fatalf("healthy status refused: %v", err)
				}
				return
			}
			if err == nil || err.Error() != tt.want {
				t.Fatalf("diagnostic = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestValidateServerUpdaterStatusKnownBlockers(t *testing.T) {
	for _, reason := range []string{
		"Database schema is unavailable",
		"Finish or cancel active agent updates before updating the server",
		"Finish management certificate rotation before updating the server",
		"An enabled public listener is not running",
		"Automatic backups require the standard /data database; use a manual deployment for custom database paths",
		"Custom state paths outside /data require a manual deployment",
	} {
		t.Run(reason, func(t *testing.T) {
			status := serverupdate.RuntimeStatus{Ready: true, InstanceID: "expected", Blocked: []string{reason}}
			err := validateServerUpdaterStatus(status, "expected")
			want := "server is not healthy: blocked: " + reason
			if err == nil || err.Error() != want {
				t.Fatalf("diagnostic = %v, want %q", err, want)
			}
		})
	}
}

func TestValidateServerUpdaterStatusDiagnosticIsBoundedAndSafe(t *testing.T) {
	const secret = "diagnostic-secret-sentinel"
	t.Setenv("DATABASE_URL", "postgres://user:"+secret+"@private/db")
	t.Setenv("SERVER_UPDATE_TOKEN", secret)
	const known = "An enabled public listener is not running"
	status := serverupdate.RuntimeStatus{
		InstanceID: secret, Version: secret, Commit: secret, Agents: []string{secret}, Maintenance: true,
	}
	for range 1000 {
		status.Blocked = append(status.Blocked, known, known+"\n"+secret, strings.Repeat(secret, 100))
	}
	err := validateServerUpdaterStatus(status, "expected-"+secret)
	if err == nil {
		t.Fatal("unsafe status accepted")
	}
	want := "server is not healthy: readiness is false; server instance mismatch; maintenance is active; blocked: " + known + "; blocked: unrecognized runtime blocker (details omitted)"
	if got := err.Error(); got != want || strings.Contains(got, secret) || len(got) > 1024 {
		t.Fatal("diagnostic was not bounded to fixed, deduplicated messages")
	}
}
