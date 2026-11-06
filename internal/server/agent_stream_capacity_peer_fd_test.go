package server

import (
	"net"
	"testing"

	"p2pstream/internal/sysmetrics"
	"p2pstream/internal/tunnel"
)

func TestServerExternalPeerFDShareUsesGrossHeadroom(t *testing.T) {
	usage := sysmetrics.MemoryUsage{UsedBytes: 64 << 20, LimitBytes: 4 << 30, Source: "test"}
	manager := newAdaptiveServerCapacityForTest(t, 64, &usage)
	model, err := tunnel.ServerStreamResourceModel(0)
	if err != nil {
		t.Fatal(err)
	}
	manager.publishAdaptiveCapacity(sysmetrics.AdaptiveMemorySnapshot{
		Generation:           100,
		Level:                sysmetrics.MemoryPressureSoft,
		AdmissionLimit:       32,
		MemoryAdmissionLimit: 32,
		FDAdmissionLimit:     32,
		Maximum:              64,
		Usage:                usage,
		StreamChargeByte:     model.MemoryBytes,
		AdmissionRole:        sysmetrics.AdaptiveAdmissionRoleServerStream,
		FDUsed:               85,
		FDLimit:              100,
		FDHeadroomToHard:     5,
	})

	limiter := newPublicConnectionCapacityLimiter(0, 64)
	peerA := &net.TCPAddr{IP: net.ParseIP("192.0.2.50"), Port: 1000}
	peerB := &net.TCPAddr{IP: net.ParseIP("192.0.2.51"), Port: 1000}
	releases := make([]func(), 0, 5)
	reserve := func(peer net.Addr) bool {
		memoryLimit, fdLimit, adaptive := manager.adaptiveExternalPeerLimits()
		if !adaptive || fdLimit != 4 {
			t.Fatalf("peer limits = %d bytes/%d FDs adaptive=%t, want four-FD share", memoryLimit, fdLimit, adaptive)
		}
		peerRelease, ok := limiter.tryAcquire(peer, 0, memoryLimit, fdLimit)
		if !ok {
			return false
		}
		resourceRelease, ok, constrained := manager.tryReserveAdaptiveResource(adaptiveResourceOwnerPublicConnection, 0, 1)
		if !ok {
			peerRelease()
			if !constrained {
				t.Fatal("server FD reservation was not adaptively constrained")
			}
			return false
		}
		releases = append(releases, func() {
			resourceRelease()
			peerRelease()
		})
		return true
	}

	for index := range 4 {
		if !reserve(peerA) {
			t.Fatalf("peer A reservation %d of 4 rejected", index+1)
		}
	}
	if reserve(peerA) {
		t.Fatal("peer A consumed the descriptor reserved for another peer")
	}
	if !reserve(peerB) {
		t.Fatal("peer B could not use the descriptor preserved by peer fairness")
	}
	if snapshot := manager.snapshot(); snapshot.AdaptiveExternalFDs != 5 {
		t.Fatalf("external descriptors = %d, want full five-descriptor envelope", snapshot.AdaptiveExternalFDs)
	}
	if reserve(peerB) {
		t.Fatal("global descriptor ledger exceeded measured headroom")
	}

	for _, release := range releases {
		release()
	}
	if snapshot := manager.snapshot(); snapshot.AdaptiveExternalFDs != 0 {
		t.Fatalf("released descriptors = %d, want zero", snapshot.AdaptiveExternalFDs)
	}
	if err := manager.validateInvariants(); err != nil {
		t.Fatal(err)
	}
}
