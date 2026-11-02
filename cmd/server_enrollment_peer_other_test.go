//go:build !linux

package cmd

import (
	"bytes"
	"context"
	"errors"
	"testing"
)

func TestServerEnrollmentPreflightUnsupportedPlatformFailsClosed(t *testing.T) {
	verifier, err := serverEnrollmentPreflightPeerVerifier()
	if verifier != nil || !errors.Is(err, errServerEnrollmentPreflightPlatform) {
		t.Fatalf("unsupported peer verification = %v", err)
	}
	var out bytes.Buffer
	err = printServerEnrollmentPreflight(context.Background(), &out, "unused.sock", "11111111-1111-4111-8111-111111111111")
	if !errors.Is(err, errServerEnrollmentPreflightPlatform) || out.Len() != 0 {
		t.Fatalf("unsupported preflight failed open: %q %v", out.Bytes(), err)
	}
}
