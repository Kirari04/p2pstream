package server

import (
	"net"
	"sync"
	"testing"
)

func TestPublicConnectionCapacityLimiterReturnsTemporaryBytesAtActive(t *testing.T) {
	limiter := newPublicConnectionCapacityLimiter(0, 10)
	client := &net.TCPAddr{IP: net.ParseIP("192.0.2.40"), Port: 1000}
	const (
		lifetimeBytes  = int64(640 << 10)
		temporaryBytes = int64(256 << 10)
		peerBytes      = 2 * lifetimeBytes
	)

	releaseFirst, activeFirst, ok := limiter.tryAcquirePhased(client, lifetimeBytes, temporaryBytes, peerBytes, 10)
	if !ok {
		t.Fatal("first phased connection rejected")
	}
	if got := limiter.clientBytes[client.IP.String()]; got != lifetimeBytes+temporaryBytes {
		t.Fatalf("initial peer bytes = %d, want %d", got, lifetimeBytes+temporaryBytes)
	}
	if _, _, ok := limiter.tryAcquirePhased(client, lifetimeBytes, temporaryBytes, peerBytes, 10); ok {
		t.Fatal("second initial phase exceeded the peer memory share")
	}

	activeFirst()
	if got := limiter.clientBytes[client.IP.String()]; got != lifetimeBytes {
		t.Fatalf("active peer bytes = %d, want %d", got, lifetimeBytes)
	}
	releaseSecond, activeSecond, ok := limiter.tryAcquirePhased(client, lifetimeBytes, 0, peerBytes, 10)
	if !ok {
		t.Fatal("temporary-byte release did not make the peer share reusable")
	}

	var race sync.WaitGroup
	for range 16 {
		race.Go(activeFirst)
		race.Go(releaseFirst)
	}
	race.Wait()
	clientKey := client.IP.String()
	if got := limiter.clients[clientKey]; got != 1 {
		t.Fatalf("connections after active/close race = %d, want 1", got)
	}
	if got := limiter.clientBytes[clientKey]; got != lifetimeBytes {
		t.Fatalf("peer bytes after active/close race = %d, want %d", got, lifetimeBytes)
	}

	activeSecond()
	releaseSecond()
	releaseSecond()
	if limiter.total != 0 || len(limiter.clients) != 0 || len(limiter.clientBytes) != 0 {
		t.Fatalf("released limiter state: total=%d clients=%v bytes=%v", limiter.total, limiter.clients, limiter.clientBytes)
	}
}
