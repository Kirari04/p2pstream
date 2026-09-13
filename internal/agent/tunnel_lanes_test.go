package agent

import (
	"errors"
	"net/http"
	"testing"

	"p2pstream/internal/tunnel"
)

type laneTestRoundTripper func(*http.Request) (*http.Response, error)

func (f laneTestRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestExtraTunnelRejectsMissingAcknowledgmentAfterServerDowngrade(t *testing.T) {
	client := &http.Client{Transport: laneTestRoundTripper(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusSwitchingProtocols, Header: http.Header{"Upgrade": {tunnel.UpgradeToken}}, Body: http.NoBody}, nil
	})}
	err := connectAndServe(t.Context(), client, "https://management.test/agent/tunnel", "test-agent", "test-agent", "test-token", nil, 2<<20, 64, false, nil,
		tunnelLaneRegistration{group: "a123456789012345", lane: 1})
	if !errors.Is(err, errTunnelLaneUnsupported) {
		t.Fatalf("extra lane did not stop after lost acknowledgment: %v", err)
	}
}
