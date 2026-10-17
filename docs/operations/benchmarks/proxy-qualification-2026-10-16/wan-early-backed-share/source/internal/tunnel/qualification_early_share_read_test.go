//go:build linux && p2pstream_qualification

package tunnel

import (
	"io"
	"net"
	"testing"
	"time"
)

type earlyShareReadResult struct {
	n   int
	err error
}
type earlyShareScriptedReadConn struct {
	net.Conn
	reads []earlyShareReadResult
}

func (c *earlyShareScriptedReadConn) Read(p []byte) (int, error) {
	if len(c.reads) == 0 {
		return 0, io.EOF
	}
	next := c.reads[0]
	c.reads = c.reads[1:]
	if next.n > len(p) {
		panic("test Read buffer too small")
	}
	return next.n, next.err
}

func TestQualificationEarlyShareReadGatingAndOnceNoop(t *testing.T) {
	restore := setQualificationEarlyShareTestMode(true, true)
	defer restore()
	_, server, _, reader := earlySharePair(t)
	owner := NewAggregateReceiveWindowGrowth(server, func(int64) (func(), bool) { t.Fatal("initial-pool attempt reserved"); return nil, false })
	conn := NewAggregateGrowingConn(reader, 64<<20, owner)
	// No-op below a grown pool still consumes once. Scripted net.Conn results
	// isolate the public Read contract; the growth owner/stream are real Yamux.
	conn.Conn = &earlyShareScriptedReadConn{Conn: reader, reads: []earlyShareReadResult{
		{0, nil}, {16, io.EOF}, {(512 << 10) - 1, nil}, {1, nil}, {512 << 10, nil},
	}}
	conn.growth.deniedUntil = time.Now().Add(time.Hour)
	buffer := make([]byte, 512<<10)
	for i := 0; i < 3; i++ {
		_, _ = conn.Read(buffer)
	}
	if conn.earlyShareAttempted || conn.earlyShareConsumed != earlyShareThreshold-1 {
		t.Fatalf("zero/error Read counted: attempted=%v consumed=%d", conn.earlyShareAttempted, conn.earlyShareConsumed)
	}
	if n, err := conn.Read(buffer); n != 1 || err != nil {
		t.Fatalf("threshold Read=%d,%v", n, err)
	}
	if !conn.earlyShareAttempted || conn.earlyShareConsumed != earlyShareThreshold {
		t.Fatal("threshold not consumed")
	}
	if _, err := conn.Read(buffer); err != nil {
		t.Fatal(err)
	}
	if conn.earlyShareConsumed != earlyShareThreshold {
		t.Fatal("once counter kept lifetime bytes")
	}
	if got, _ := reader.AggregateReceiveAllocationWindow(); got != 512<<10 {
		t.Fatal("initial backing allowed a larger share")
	}
}

func TestQualificationEarlyShareClosingAndUnconfiguredDoNotCount(t *testing.T) {
	for _, tt := range []struct {
		name                string
		configured, closing bool
	}{{"closing", true, true}, {"unconfigured", false, false}} {
		t.Run(tt.name, func(t *testing.T) {
			restore := setQualificationEarlyShareTestMode(true, tt.configured)
			defer restore()
			_, server, _, reader := earlySharePair(t)
			owner := NewAggregateReceiveWindowGrowth(server, func(int64) (func(), bool) { t.Fatal("unexpected reserve"); return nil, false })
			conn := NewAggregateGrowingConn(reader, 64<<20, owner)
			conn.Conn = &earlyShareScriptedReadConn{Conn: reader, reads: []earlyShareReadResult{{512 << 10, nil}}}
			conn.closing = tt.closing
			conn.growth.deniedUntil = time.Now().Add(time.Hour)
			if _, err := conn.Read(make([]byte, 512<<10)); err != nil {
				t.Fatal(err)
			}
			if conn.earlyShareAttempted || conn.earlyShareConsumed != 0 {
				t.Fatal("ineligible Read counted")
			}
		})
	}
}

func TestQualificationEarlyShareDisabledOwnerErrorDoesNotChangeRead(t *testing.T) {
	restore := setQualificationEarlyShareTestMode(false, true)
	defer restore()
	_, server, _, reader := earlySharePair(t)
	owner := NewAggregateReceiveWindowGrowth(server, func(int64) (func(), bool) { return func() {}, true })
	conn := NewAggregateGrowingConn(reader, 64<<20, owner)
	conn.Conn = &earlyShareScriptedReadConn{Conn: reader, reads: []earlyShareReadResult{{512 << 10, nil}}}
	// Original estimator has not completed a demand epoch, so baseline Read
	// returns success even if this new diagnostic observes owner shutdown.
	owner.mu.Lock()
	owner.closing = true
	owner.mu.Unlock()
	conn.growth.start = time.Now().Add(time.Hour)
	if n, err := conn.Read(make([]byte, 512<<10)); n != 512<<10 || err != nil {
		t.Fatalf("disabled observer changed Read: %d,%v", n, err)
	}
	if !conn.earlyShareAttempted {
		t.Fatal("disabled arm did not record threshold")
	}
}

func TestQualificationEarlyShareClosedSkipDoesNotRetryEstimator(t *testing.T) {
	restore := setQualificationEarlyShareTestMode(true, true)
	defer restore()
	_, server, writer, reader := earlySharePair(t)
	owner := NewAggregateReceiveWindowGrowth(server, func(int64) (func(), bool) { return func() {}, true })
	if ok, err := owner.Grow(reader, 2<<20); !ok || err != nil {
		t.Fatalf("prime: %v %v", ok, err)
	}
	// Use a fresh receive stream so a larger backed target would be available.
	client := writer.Session()
	peer, err := client.OpenStream()
	if err != nil {
		t.Fatal(err)
	}
	stream, err := server.AcceptStream()
	if err != nil {
		t.Fatal(err)
	}
	if err := peer.Close(); err != nil {
		t.Fatal(err)
	}
	if n, err := stream.Read(make([]byte, 1)); n != 0 || err != io.EOF {
		t.Fatalf("join FIN=%d,%v", n, err)
	}
	conn := NewAggregateGrowingConn(stream, 64<<20, owner)
	conn.Conn = &earlyShareScriptedReadConn{Conn: stream, reads: []earlyShareReadResult{{512 << 10, nil}}}
	conn.growth.start = time.Now().Add(-80 * time.Millisecond)
	conn.growth.previous = 4 << 20
	conn.growth.rate = 64 << 20
	before := conn.growth
	if n, err := conn.Read(make([]byte, 512<<10)); n != 512<<10 || err != nil {
		t.Fatalf("closed skip retried growth: %d,%v", n, err)
	}
	if conn.growth != before {
		t.Fatal("closed skip changed/retried ordinary estimator")
	}
}

func TestQualificationEarlyShareReadGrantsOnceWithoutIdleReset(t *testing.T) {
	restore := setQualificationEarlyShareTestMode(true, true)
	defer restore()
	client, server, _, donor := earlySharePair(t)
	owner := NewAggregateReceiveWindowGrowth(server, func(int64) (func(), bool) { return func() {}, true })
	if ok, err := owner.Grow(donor, 2<<20); !ok || err != nil {
		t.Fatalf("prime: %v %v", ok, err)
	}
	_, err := client.OpenStream()
	if err != nil {
		t.Fatal(err)
	}
	stream, err := server.AcceptStream()
	if err != nil {
		t.Fatal(err)
	}
	conn := NewAggregateGrowingConn(stream, 64<<20, owner)
	conn.Conn = &earlyShareScriptedReadConn{Conn: stream, reads: []earlyShareReadResult{{256 << 10, nil}, {256 << 10, nil}, {512 << 10, nil}}}
	denied := time.Now().Add(time.Hour)
	conn.growth.deniedUntil = denied
	buffer := make([]byte, 512<<10)
	if _, err := conn.Read(buffer); err != nil {
		t.Fatal(err)
	}
	if conn.earlyShareAttempted {
		t.Fatal("early attempt before cumulative threshold")
	}
	if _, err := conn.Read(buffer); err != nil {
		t.Fatal(err)
	}
	if got, _ := stream.AggregateReceiveAllocationWindow(); got != 2<<20 || stream.MaxReceiveWindow() != 2<<20 {
		t.Fatal("threshold did not grant from grown backing")
	}
	if conn.growth.deniedUntil != denied {
		t.Fatal("early share changed estimator denial history")
	}
	// More backing and a long pause do not re-arm the lifetime attempt.
	if ok, err := owner.Grow(donor, 4<<20); !ok || err != nil {
		t.Fatalf("second backing growth: %v %v", ok, err)
	}
	conn.growth.start = time.Now().Add(-time.Hour)
	if _, err := conn.Read(buffer); err != nil {
		t.Fatal(err)
	}
	if got, _ := stream.AggregateReceiveAllocationWindow(); got != 2<<20 || stream.MaxReceiveWindow() != 2<<20 {
		t.Fatal("idle pause rearmed early share")
	}
}
