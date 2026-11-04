package yamux

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"testing"
)

// A close-timeout callback can already be running when application disposal
// stops its timer. Its late forced close must not turn an explicit reset into
// successful EOF after unread FIN bytes were discarded.
func TestRetirementDisposalSurvivesLateCloseTimeout(t *testing.T) {
	session := newAggregateUnitSession(bytes.Repeat([]byte{1}, 5000))
	defer session.closeAggregatePool()
	stream := newStream(session, 1, streamLocalClose)
	session.streams[1] = stream
	hdr := header(make([]byte, headerSize))
	hdr.encode(typeData, flagFIN, 1, 5000)
	if err := session.handleAggregateData(hdr); err != nil {
		t.Fatal(err)
	}
	if session.NumStreams() != 0 {
		t.Fatal("FIN fixture remained mapped")
	}
	if err := stream.Reset(); err != nil {
		t.Fatal(err)
	}
	close(session.shutdownCh)
	stream.closeTimeout()
	if _, err := stream.Read(make([]byte, 1)); !errors.Is(err, ErrConnectionReset) {
		t.Fatalf("late cleanup replaced reset with EOF: %v", err)
	}
	if buffered, allocated := stream.ReceiveBufferStats(); buffered != 0 || allocated != 0 {
		t.Fatal("reset retained storage")
	}
}

// Timer.Stop may lose to a callback already waiting for stateLock. A completed
// FIN handshake must still leave its final unread bytes owned by the reader.
func TestRetirementLateCloseTimeoutPreservesGracefulFINBuffer(t *testing.T) {
	for _, aggregate := range []bool{false, true} {
		t.Run(fmt.Sprint(aggregate), func(t *testing.T) {
			session, stream, payload := retirementTimeoutBuffer(t, aggregate, flagFIN)
			defer session.closeAggregatePool()
			stream.closeTimeout()
			got, err := io.ReadAll(stream)
			if err != nil || !bytes.Equal(got, payload) {
				t.Fatalf("late timeout truncated graceful FIN: bytes=%d err=%v", len(got), err)
			}
		})
	}
}

func TestRetirementCloseTimeoutReportsDiscardedUnreadBytes(t *testing.T) {
	for _, aggregate := range []bool{false, true} {
		t.Run(fmt.Sprint(aggregate), func(t *testing.T) {
			session, stream, _ := retirementTimeoutBuffer(t, aggregate, 0)
			defer session.closeAggregatePool()
			stream.closeTimeout()
			if _, err := stream.Read(make([]byte, 1)); !errors.Is(err, ErrConnectionReset) {
				t.Fatalf("timeout hid truncation: %v", err)
			}
			if buffered, allocated := stream.ReceiveBufferStats(); buffered != 0 || allocated != 0 {
				t.Fatal("timeout retained abandoned storage")
			}
		})
	}
}

func retirementTimeoutBuffer(t *testing.T, aggregate bool, flags uint16) (*Session, *Stream, []byte) {
	t.Helper()
	payload := bytes.Repeat([]byte{7}, 5000)
	var session *Session
	if aggregate {
		session = newAggregateUnitSession(payload)
	} else {
		session = newLegacyUnitSession(bytes.NewReader(payload))
	}
	stream := newStream(session, 1, streamLocalClose)
	session.streams[1] = stream
	hdr := header(make([]byte, headerSize))
	hdr.encode(typeData, flags, 1, uint32(len(payload)))
	var err error
	if aggregate {
		err = session.handleAggregateData(hdr)
	} else {
		err = session.handleStreamMessage(hdr)
	}
	if err != nil {
		t.Fatal(err)
	}
	close(session.shutdownCh) // no physical sender is needed for this fixture
	return session, stream, payload
}
