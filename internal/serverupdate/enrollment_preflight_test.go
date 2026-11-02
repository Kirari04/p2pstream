package serverupdate

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestEnrollmentPreflightBoundedAllowlistAndFalseReadiness(t *testing.T) {
	status := RuntimeStatus{Version: "dev", Ready: false}
	for i := 0; i < 10000; i++ {
		status.Blocked = append(status.Blocked, "sensitive runtime detail", "An enabled public listener is not running")
	}
	p := NewEnrollmentPreflight("11111111-1111-4111-8111-111111111111", status)
	data, err := json.Marshal(p)
	if err != nil || p.Ready || len(p.Blockers) != 2 || strings.Contains(string(data), "sensitive") || len(data) > EnrollmentPreflightMaxBytes {
		t.Fatalf("unbounded preflight: %s %v", data, err)
	}
	if _, err := DecodeEnrollmentPreflight(strings.NewReader(string(data))); err != nil {
		t.Fatal(err)
	}
	p = NewEnrollmentPreflight("11111111-1111-4111-8111-111111111111", RuntimeStatus{Version: "dev"})
	if p.Ready || len(p.Blockers) != 1 || p.Blockers[0].Code != "runtime_unavailable" {
		t.Fatalf("false runtime readiness hidden: %+v", p)
	}
}

func TestDeploymentCapabilitiesRemainBackwardCompatible(t *testing.T) {
	var older DeploymentCapabilities
	if err := decode([]byte(`{"runtime_guard":2,"version":"v1.0.0","commit":"`+strings.Repeat("a", 40)+`"}`), &older); err != nil || older.RuntimeGuard != 2 || older.EnrollmentPreflight != 0 {
		t.Fatalf("old capability no longer accepted: %+v %v", older, err)
	}
	data, _ := json.Marshal(older)
	if strings.Contains(string(data), "enrollment_preflight") {
		t.Fatalf("zero capability changed old fixture: %s", data)
	}
	older.EnrollmentPreflight = 1
	data, _ = json.Marshal(older)
	var newer DeploymentCapabilities
	if err := decode(data, &newer); err != nil || newer.EnrollmentPreflight != 1 || newer.RuntimeGuard != 2 {
		t.Fatalf("strict reader rejects new capability: %+v %v", newer, err)
	}
}

func TestEnrollmentPreflightSocketRequiresCanonicalNonzeroIdentity(t *testing.T) {
	id := "12345678-abcd-4123-8123-123456789abc"
	if socket, err := EnrollmentPreflightSocket(id); err != nil || socket != "/tmp/p2pstream-enrollment-"+id+".sock" || socket == RuntimeSocket {
		t.Fatalf("installation socket = %q, %v", socket, err)
	}
	for _, id := range []string{"", "00000000-0000-0000-0000-000000000000", strings.ToUpper(id), "{" + id + "}", "urn:uuid:" + id, strings.ReplaceAll(id, "-", ""), "../" + id} {
		if socket, err := EnrollmentPreflightSocket(id); err == nil || socket != "" {
			t.Fatalf("invalid identity %q acquired socket %q", id, socket)
		}
	}
}

func TestEnrollmentPreflightStrictFieldTypesAndDuplicateBlockers(t *testing.T) {
	p := NewEnrollmentPreflight("11111111-1111-4111-8111-111111111111", RuntimeStatus{Version: "dev", Ready: true})
	data, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	var original map[string]json.RawMessage
	if err := json.Unmarshal(data, &original); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"api", "installation_id", "version", "commit", "ready", "blockers"} {
		for _, value := range []string{"null", "{}", `"wrong-type"`, "1", "false"} {
			// A string version/commit and integer API have independent value checks.
			if field == "version" && value == `"wrong-type"` || field == "api" && value == "1" {
				continue
			}
			t.Run(field+"/"+value, func(t *testing.T) {
				fields := map[string]json.RawMessage{}
				for k, v := range original {
					fields[k] = v
				}
				fields[field] = json.RawMessage(value)
				data, _ := json.Marshal(fields)
				if _, err := DecodeEnrollmentPreflight(strings.NewReader(string(data))); err == nil {
					t.Fatalf("accepted invalid field: %s", data)
				}
			})
		}
	}
	p.Ready = false
	p.Blockers = []EnrollmentBlocker{EnrollmentBlockerForCode("runtime_unavailable"), EnrollmentBlockerForCode("runtime_unavailable")}
	data, _ = json.Marshal(p)
	if _, err := DecodeEnrollmentPreflight(strings.NewReader(string(data))); err == nil {
		t.Fatal("accepted duplicate blockers")
	}
}
