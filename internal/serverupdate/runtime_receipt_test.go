package serverupdate

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func receiptFixture(t *testing.T) ([]byte, map[string]any) {
	t.Helper()
	model := []byte(`{"name":"project","services":{"p2pstream":{"image":"ghcr.io/test/repo@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","restart":"unless-stopped"}}}`)
	var inspection map[string]any
	if err := json.Unmarshal([]byte(`{
"Id":"aaaaaaaaaaaa1111111111111111111111111111111111111111111111111111","Image":"sha256:image","Name":"/project-p2pstream-1","Path":"/app/p2pstream","Args":["server"],"AppArmorProfile":"docker-default",
"Config":{"Hostname":"aaaaaaaaaaaa","Env":["CONFIG_DIR=/data","SECRET=value"],"Cmd":["/app/p2pstream","server"],"User":"999:999","Labels":{"com.docker.compose.project":"project","com.docker.compose.service":"p2pstream","com.docker.compose.config-hash":"managed-hash","com.docker.compose.version":"2.39.2","p2pstream.server-update.instance":"instance"}},
"HostConfig":{"RestartPolicy":{"Name":"unless-stopped"},"ReadonlyRootfs":true,"Privileged":false,"PortBindings":{"8081/tcp":[{"HostIp":"127.0.0.1","HostPort":"9443"}]},"Binds":["data:/data:rw","/private/control:/run/p2pstream-server-update:ro"],"CapDrop":["ALL"]},
"Mounts":[{"Type":"volume","Name":"data","Source":"/var/lib/docker/volumes/data/_data","Destination":"/data","RW":true},{"Type":"bind","Source":"/private/control","Destination":"/run/p2pstream-server-update","RW":false}],
"NetworkSettings":{"Networks":{"project_default":{"NetworkID":"old-network","EndpointID":"old-endpoint","IPAddress":"172.18.0.2","IPPrefixLen":16,"Gateway":"172.18.0.1","MacAddress":"02:42:ac:12:00:02","IPAMConfig":null,"Aliases":["project-p2pstream-1","p2pstream","aaaaaaaaaaaa"],"DNSNames":["project-p2pstream-1","aaaaaaaaaaaa"]}}}
}`), &inspection); err != nil {
		t.Fatal(err)
	}
	return model, inspection
}

func TestRuntimeReceiptAllowsOnlyKnownRecreationDifferences(t *testing.T) {
	model, original := receiptFixture(t)
	before, err := runtimeFingerprint(model, original)
	if err != nil {
		t.Fatal(err)
	}
	_, next := receiptFixture(t)
	next["Id"] = strings.Repeat("b", 64)
	config := receiptMap(next["Config"])
	config["Hostname"] = strings.Repeat("b", 12)
	config["Env"] = []any{"SECRET=value", "CONFIG_DIR=/data"}
	labels := receiptMap(config["Labels"])
	labels["com.docker.compose.config-hash"] = "new-native-hash"
	labels["com.docker.compose.version"] = "5.0.0"
	labels["com.docker.compose.project.config_files"] = "/deployment/compose.yaml"
	network := receiptMap(receiptMap(receiptMap(next["NetworkSettings"])["Networks"])["project_default"])
	network["NetworkID"], network["EndpointID"] = "new-network", "new-endpoint"
	network["IPAddress"], network["MacAddress"] = "172.18.0.9", "02:42:ac:12:00:09"
	network["Aliases"] = []any{strings.Repeat("b", 12), "p2pstream", "project-p2pstream-1"}
	after, err := runtimeFingerprint(model, next)
	if err != nil || string(before) != string(after) {
		t.Fatalf("equivalent recreation rejected: %v\nbefore=%s\nafter=%s", err, before, after)
	}
	if receiptMap(original["Config"])["Hostname"] != "aaaaaaaaaaaa" {
		t.Fatal("fingerprint mutated original inspection")
	}
}

func TestRuntimeReceiptNormalizesDuplicateNetworkAliases(t *testing.T) {
	model, inspection := receiptFixture(t)
	network := receiptMap(receiptMap(receiptMap(inspection["NetworkSettings"])["Networks"])["project_default"])
	id := inspection["Id"].(string)
	network["Aliases"] = []any{"p2pstream", "p2pstream", "custom", id[:12], id}
	original, err := json.Marshal(inspection)
	if err != nil {
		t.Fatal(err)
	}
	before, err := runtimeFingerprint(model, inspection)
	if err != nil {
		t.Fatal(err)
	}
	after, err := json.Marshal(inspection)
	if err != nil || string(after) != string(original) {
		t.Fatalf("fingerprint mutated original inspection: %v", err)
	}
	for _, aliases := range [][]any{
		{"custom", "p2pstream"},
		{"custom", "custom", "p2pstream", "p2pstream", "p2pstream"},
		{id, "p2pstream", id[:12], "custom", id},
	} {
		network["Aliases"] = aliases
		after, err := runtimeFingerprint(model, inspection)
		if err != nil || string(before) != string(after) {
			t.Fatalf("equivalent alias membership rejected: %v\nbefore=%s\nafter=%s", err, before, after)
		}
	}
	for _, aliases := range [][]any{{"p2pstream"}, {"p2pstream", "custom", "added"}, {"p2pstream", "changed"}} {
		network["Aliases"] = aliases
		after, err := runtimeFingerprint(model, inspection)
		if err != nil || string(before) == string(after) {
			t.Fatalf("alias membership drift was not distinguished: %v", err)
		}
	}
}

func TestRuntimeReceiptRejectsMalformedNetworkAliases(t *testing.T) {
	for name, aliases := range map[string]any{
		"string list":  "p2pstream",
		"number list":  42,
		"object list":  map[string]any{"alias": "p2pstream"},
		"number entry": []any{"p2pstream", 42},
		"null entry":   []any{"p2pstream", nil},
		"object entry": []any{map[string]any{"alias": "p2pstream"}},
		"list entry":   []any{[]any{"p2pstream"}},
	} {
		t.Run(name, func(t *testing.T) {
			model, inspection := receiptFixture(t)
			receiptMap(receiptMap(receiptMap(inspection["NetworkSettings"])["Networks"])["project_default"])["Aliases"] = aliases
			if _, err := runtimeFingerprint(model, inspection); err == nil {
				t.Fatal("accepted malformed aliases")
			}
		})
	}
}

func TestRuntimeReceiptRejectsRuntimeDrift(t *testing.T) {
	changes := map[string]func(map[string]any){
		"environment": func(m map[string]any) { receiptMap(m["Config"])["Env"] = []any{"CONFIG_DIR=/other", "SECRET=value"} },
		"command":     func(m map[string]any) { receiptMap(m["Config"])["Cmd"] = []any{"/bin/sh"} },
		"user":        func(m map[string]any) { receiptMap(m["Config"])["User"] = "0:0" },
		"hostname":    func(m map[string]any) { receiptMap(m["Config"])["Hostname"] = "changed" },
		"privilege":   func(m map[string]any) { receiptMap(m["HostConfig"])["Privileged"] = true },
		"readonly":    func(m map[string]any) { receiptMap(m["HostConfig"])["ReadonlyRootfs"] = false },
		"ports": func(m map[string]any) {
			receiptMap(m["HostConfig"])["PortBindings"] = map[string]any{"8081/tcp": []any{map[string]any{"HostIp": "0.0.0.0", "HostPort": "9443"}}}
		},
		"restart":        func(m map[string]any) { receiptMap(m["HostConfig"])["RestartPolicy"] = map[string]any{"Name": "no"} },
		"mount":          func(m map[string]any) { receiptMap(m["Mounts"].([]any)[1])["RW"] = true },
		"data":           func(m map[string]any) { receiptMap(m["Mounts"].([]any)[0])["Name"] = "other-data" },
		"apparmor":       func(m map[string]any) { m["AppArmorProfile"] = "unconfined" },
		"unknown config": func(m map[string]any) { receiptMap(m["Config"])["FutureSecuritySetting"] = "changed" },
		"unknown host":   func(m map[string]any) { receiptMap(m["HostConfig"])["FutureSecuritySetting"] = "changed" },
		"alias": func(m map[string]any) {
			receiptMap(receiptMap(receiptMap(m["NetworkSettings"])["Networks"])["project_default"])["Aliases"] = []any{"changed"}
		},
		"static address": func(m map[string]any) {
			receiptMap(receiptMap(receiptMap(m["NetworkSettings"])["Networks"])["project_default"])["IPAMConfig"] = map[string]any{"IPv4Address": "172.18.0.2"}
		},
	}
	for name, change := range changes {
		t.Run(name, func(t *testing.T) {
			model, original := receiptFixture(t)
			before, err := runtimeFingerprint(model, original)
			if err != nil {
				t.Fatal(err)
			}
			change(original)
			after, err := runtimeFingerprint(model, original)
			if err == nil && string(before) == string(after) {
				t.Fatal("accepted runtime drift")
			}
		})
	}
}

func TestRuntimeReceiptRejectsAmbiguousEnvironmentAndMounts(t *testing.T) {
	for _, kind := range []string{"environment", "identical environment", "mount", "host mount", "bind"} {
		model, inspection := receiptFixture(t)
		switch kind {
		case "environment":
			receiptMap(inspection["Config"])["Env"] = []any{"CONFIG_DIR=/data", "CONFIG_DIR=/other"}
		case "identical environment":
			receiptMap(inspection["Config"])["Env"] = []any{"CONFIG_DIR=/data", "CONFIG_DIR=/data"}
		case "mount":
			mounts := inspection["Mounts"].([]any)
			inspection["Mounts"] = append(mounts, mounts[0])
		case "host mount":
			receiptMap(inspection["HostConfig"])["Mounts"] = []any{map[string]any{"Target": "/data"}, map[string]any{"Target": "/data"}}
		case "bind":
			receiptMap(inspection["HostConfig"])["Binds"] = []any{"data:/data:rw", "data:/data:rw"}
		}
		if _, err := runtimeFingerprint(model, inspection); err == nil {
			t.Fatalf("accepted ambiguous %s", kind)
		}
	}
}

func TestRuntimeReceiptBindsCanonicalModelAndImmutableImage(t *testing.T) {
	model, inspection := receiptFixture(t)
	network := receiptMap(receiptMap(receiptMap(inspection["NetworkSettings"])["Networks"])["project_default"])
	network["Aliases"] = []any{"p2pstream", "p2pstream"}
	directory := t.TempDir()
	canonical := filepath.Join(directory, "compose.json")
	inspectPath := filepath.Join(directory, "inspect.json")
	docker := filepath.Join(directory, "docker")
	write := func(path string, value []byte, mode os.FileMode) {
		t.Helper()
		if err := os.WriteFile(path, value, mode); err != nil {
			t.Fatal(err)
		}
	}
	write(canonical, model, 0600)
	publishInspection := func() { raw, _ := json.Marshal([]any{inspection}); write(inspectPath, raw, 0600) }
	publishInspection()
	// Paths are controlled temporary test paths; the executable uses its own
	// directory rather than interpolating deployment strings into shell code.
	write(docker, []byte("#!/bin/sh\ncase \"$1\" in\ninspect) cat \"${0%/*}/inspect.json\" ;;\ncompose) printf 'p2pstream managed-hash\\n' ;;\nimage) printf '[{\"Id\":\"sha256:image\"}]' ;;\n*) exit 1 ;;\nesac\n"), 0700)
	driver := &ComposeDriver{Config: ComposeConfig{DeploymentProtocol: 2, StateDir: directory, DeploymentFile: canonical, Docker: docker, Project: "project", InstanceID: "instance"}}
	id := inspection["Id"].(string)
	if err := driver.RecordRuntimeReceipt(context.Background(), model, id); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(directory, runtimeReceiptFile)
	saved, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	receiptMap(receiptMap(inspection["Config"])["Labels"])["com.docker.compose.config-hash"] = "native-new-hash"
	id = strings.Repeat("b", 64)
	inspection["Id"] = id
	receiptMap(inspection["Config"])["Hostname"] = id[:12]
	network["Aliases"] = []any{id[:12], "p2pstream"}
	publishInspection()
	if err := driver.VerifyRuntimeReceipt(context.Background(), model, id); err != nil {
		t.Fatal(err)
	}
	if err := driver.RecordRuntimeReceipt(context.Background(), model, id); err == nil {
		t.Fatal("adopted mismatching native hash")
	}
	network["Aliases"] = []any{"p2pstream", "changed"}
	publishInspection()
	if err := driver.VerifyRuntimeReceipt(context.Background(), model, id); err == nil {
		t.Fatal("accepted changed network aliases")
	}
	network["Aliases"] = []any{"p2pstream"}
	publishInspection()
	if err := driver.VerifyRuntimeReceipt(context.Background(), append(model, ' '), id); err == nil {
		t.Fatal("accepted changed canonical bytes")
	}
	inspection["Image"] = "sha256:other-image"
	publishInspection()
	if err := driver.VerifyRuntimeReceipt(context.Background(), model, id); err == nil {
		t.Fatal("accepted changed immutable image")
	}
	after, _ := os.ReadFile(path)
	if string(after) != string(saved) {
		t.Fatal("mismatch changed trusted receipt")
	}
}
