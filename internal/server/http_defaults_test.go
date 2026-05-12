package server

import (
	"net/http"
	"testing"
)

func TestManagementHTTPServerDefaults(t *testing.T) {
	srv := &http.Server{}
	ConfigureManagementHTTPServer(srv)
	if srv.ReadHeaderTimeout != managementReadHeaderTimeout {
		t.Fatalf("ReadHeaderTimeout = %s, want %s", srv.ReadHeaderTimeout, managementReadHeaderTimeout)
	}
	if srv.ReadTimeout != managementReadTimeout {
		t.Fatalf("ReadTimeout = %s, want %s", srv.ReadTimeout, managementReadTimeout)
	}
	if srv.WriteTimeout != managementWriteTimeout {
		t.Fatalf("WriteTimeout = %s, want %s", srv.WriteTimeout, managementWriteTimeout)
	}
	if srv.IdleTimeout != managementIdleTimeout {
		t.Fatalf("IdleTimeout = %s, want %s", srv.IdleTimeout, managementIdleTimeout)
	}
	if srv.MaxHeaderBytes != defaultMaxHeaderBytes {
		t.Fatalf("MaxHeaderBytes = %d, want %d", srv.MaxHeaderBytes, defaultMaxHeaderBytes)
	}
}

func TestPublicHTTPServerDefaultsPreserveStreaming(t *testing.T) {
	srv := &http.Server{}
	configurePublicHTTPServer(srv)
	if srv.ReadHeaderTimeout != publicReadHeaderTimeout {
		t.Fatalf("ReadHeaderTimeout = %s, want %s", srv.ReadHeaderTimeout, publicReadHeaderTimeout)
	}
	if srv.ReadTimeout != 0 {
		t.Fatalf("ReadTimeout = %s, want 0 for streaming", srv.ReadTimeout)
	}
	if srv.WriteTimeout != 0 {
		t.Fatalf("WriteTimeout = %s, want 0 for streaming", srv.WriteTimeout)
	}
	if srv.IdleTimeout != publicIdleTimeout {
		t.Fatalf("IdleTimeout = %s, want %s", srv.IdleTimeout, publicIdleTimeout)
	}
	if srv.MaxHeaderBytes != defaultMaxHeaderBytes {
		t.Fatalf("MaxHeaderBytes = %d, want %d", srv.MaxHeaderBytes, defaultMaxHeaderBytes)
	}
}
