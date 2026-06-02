package cmd

import (
	"testing"

	"p2pstream/internal/config"
)

func TestManagementListenAddressDefaultsToAllInterfaces(t *testing.T) {
	got := managementListenAddress(&config.Config{})
	if got != "0.0.0.0:8081" {
		t.Fatalf("managementListenAddress = %q, want 0.0.0.0:8081", got)
	}
}

func TestManagementListenAddressAllowsLoopback(t *testing.T) {
	got := managementListenAddress(&config.Config{ManagementBindAddress: "127.0.0.1", ManagementPort: "9443"})
	if got != "127.0.0.1:9443" {
		t.Fatalf("managementListenAddress = %q, want 127.0.0.1:9443", got)
	}
}

func TestManagementDisplayAddressUsesEffectivePort(t *testing.T) {
	got := managementDisplayAddress(managementListenAddress(&config.Config{}))
	if got != "localhost:8081" {
		t.Fatalf("managementDisplayAddress = %q, want localhost:8081", got)
	}
}
