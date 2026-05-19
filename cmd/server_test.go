package cmd

import (
	"testing"

	"p2pstream/internal/config"
)

func TestManagementListenAddressDefaultsToLoopback(t *testing.T) {
	got := managementListenAddress(&config.Config{})
	if got != "127.0.0.1:8081" {
		t.Fatalf("managementListenAddress = %q, want 127.0.0.1:8081", got)
	}
}

func TestManagementListenAddressAllowsAllInterfaces(t *testing.T) {
	got := managementListenAddress(&config.Config{ManagementBindAddress: "0.0.0.0", ManagementPort: "9443"})
	if got != "0.0.0.0:9443" {
		t.Fatalf("managementListenAddress = %q, want 0.0.0.0:9443", got)
	}
}
