package yamux

import (
	"bytes"
	"errors"
	"io"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type shutdownWaitConn struct{}

func (shutdownWaitConn) Read([]byte) (int, error)    { return 0, io.EOF }
func (shutdownWaitConn) Write(p []byte) (int, error) { return len(p), nil }
func (shutdownWaitConn) Close() error                { return nil }

func closedShutdownWaitChannel() chan struct{} {
	ch := make(chan struct{})
	close(ch)
	return ch
}

// newBlockedShutdownSession returns a session whose physical reader is joined,
// but whose Close remains blocked on a later internal cleanup stage. This is
// the interval in which an empty established stream used to spin on shutdownCh.
func newBlockedShutdownSession(t *testing.T, aggregate bool) (*Session, *Stream, *Stream, func()) {
	t.Helper()
	config := DefaultConfig()
	if aggregate {
		config = smallAggregateTestConfig()
	}
	session := &Session{
		config:          config,
		conn:            shutdownWaitConn{},
		streams:         make(map[uint32]*Stream),
		shutdownCh:      make(chan struct{}),
		recvDoneCh:      closedShutdownWaitChannel(),
		sendDoneCh:      closedShutdownWaitChannel(),
		pingReplyDoneCh: closedShutdownWaitChannel(),
	}
	readStream := newStream(session, 1, streamEstablished)
	writeStream := newStream(session, 3, streamEstablished)
	atomic.StoreUint32(&writeStream.sendWindow, 0)
	session.streams[readStream.id] = readStream
	session.streams[writeStream.id] = writeStream

	var releaseOnce sync.Once
	if aggregate {
		session.aggregate = newAggregateWindow(config)
		release := session.aggregate.publishDone
		return session, readStream, writeStream, func() {
			releaseOnce.Do(func() { close(release) })
		}
	}

	// Legacy Close reaches streamLock after closing shutdownCh and joining the
	// reader, so holding it gives the equivalent deterministic cleanup gate.
	session.streamLock.Lock()
	return session, readStream, writeStream, func() {
		releaseOnce.Do(session.streamLock.Unlock)
	}
}

func TestShutdownWakesEmptyReadAndWriteBeforeSessionCleanup(t *testing.T) {
	for _, aggregate := range []bool{false, true} {
		name := "legacy"
		if aggregate {
			name = "aggregate"
		}
		t.Run(name, func(t *testing.T) {
			session, readStream, writeStream, release := newBlockedShutdownSession(t, aggregate)
			defer release()

			closeDone := make(chan error, 1)
			go func() { closeDone <- session.Close() }()
			select {
			case <-session.shutdownCh:
			case <-time.After(time.Second):
				t.Fatal("session shutdown did not begin")
			}

			readDone := make(chan error, 1)
			go func() {
				_, err := readStream.Read(make([]byte, 1))
				readDone <- err
			}()
			writeDone := make(chan error, 1)
			go func() {
				_, err := writeStream.Write([]byte("x"))
				writeDone <- err
			}()

			for operation, done := range map[string]<-chan error{
				"read":  readDone,
				"write": writeDone,
			} {
				select {
				case err := <-done:
					if !errors.Is(err, ErrSessionShutdown) {
						t.Fatalf("%s error=%v, want ErrSessionShutdown", operation, err)
					}
				case <-time.After(time.Second):
					t.Fatalf("%s remained blocked during session cleanup", operation)
				}
			}

			select {
			case err := <-closeDone:
				t.Fatalf("Session.Close returned before cleanup release: %v", err)
			default:
			}
			release()
			select {
			case err := <-closeDone:
				if err != nil {
					t.Fatalf("Session.Close: %v", err)
				}
			case <-time.After(time.Second):
				t.Fatal("Session.Close remained blocked after cleanup release")
			}
		})
	}
}

func newShutdownReadSession(t *testing.T, aggregate bool, payload []byte) (*Session, *Stream) {
	t.Helper()
	config := DefaultConfig()
	if aggregate {
		config = smallAggregateTestConfig()
	}
	session := &Session{
		config:     config,
		shutdownCh: closedShutdownWaitChannel(),
		recvDoneCh: closedShutdownWaitChannel(),
	}
	if aggregate {
		session.aggregate = newAggregateWindow(config)
	}
	stream := newStream(session, 1, streamEstablished)
	if !aggregate {
		stream.recvBuf = &recvBuffer{}
		if n, err := stream.recvBuf.readFrom(bytes.NewReader(payload), int64(len(payload))); err != nil || n != int64(len(payload)) {
			t.Fatalf("seed legacy receive buffer: n=%d err=%v", n, err)
		}
		stream.recvWindow -= uint32(len(payload))
		return session, stream
	}

	charge := aggregateDataCharge(uint32(len(payload)))
	head, tail, units, err := session.borrowAggregateChunks(charge)
	if err != nil {
		t.Fatalf("borrow aggregate chunks: %v", err)
	}
	remaining := payload
	for chunk := head; chunk != nil && len(remaining) != 0; chunk = chunk.next {
		n := copy(chunk.buf, remaining)
		chunk.write = uint16(n)
		remaining = remaining[n:]
	}
	if len(remaining) != 0 {
		t.Fatalf("aggregate seed retained %d payload bytes", len(remaining))
	}
	stream.aggregateHead, stream.aggregateTail = head, tail
	stream.aggregateBuffered = len(payload)
	stream.aggregateUnits = units
	stream.recvWindow -= uint32(len(payload))
	session.aggregate.mu.Lock()
	session.aggregate.recvWindow -= charge
	stream.allocationRecvWindow -= charge
	session.aggregate.mu.Unlock()
	session.trackAggregateBuffer(stream)
	return session, stream
}

func TestShutdownReadDrainsBufferedPayloadBeforeTerminalError(t *testing.T) {
	payload := []byte("final payload")
	for _, aggregate := range []bool{false, true} {
		name := "legacy"
		if aggregate {
			name = "aggregate"
		}
		t.Run(name, func(t *testing.T) {
			_, stream := newShutdownReadSession(t, aggregate, payload)
			got := make([]byte, len(payload))
			if n, err := io.ReadFull(stream, got); err != nil || n != len(payload) {
				t.Fatalf("drain payload: n=%d err=%v", n, err)
			}
			if !bytes.Equal(got, payload) {
				t.Fatalf("payload=%q want=%q", got, payload)
			}
			if n, err := stream.Read(make([]byte, 1)); n != 0 || !errors.Is(err, ErrSessionShutdown) {
				t.Fatalf("empty read after drain: n=%d err=%v", n, err)
			}
		})
	}
}

func TestExpiredDeadlinePrecedesSessionShutdown(t *testing.T) {
	for _, aggregate := range []bool{false, true} {
		name := "legacy"
		if aggregate {
			name = "aggregate"
		}
		t.Run(name, func(t *testing.T) {
			session, readStream := newShutdownReadSession(t, aggregate, nil)
			writeStream := newStream(session, 3, streamEstablished)
			atomic.StoreUint32(&writeStream.sendWindow, 0)
			deadline := time.Now().Add(-time.Second)
			if err := readStream.SetReadDeadline(deadline); err != nil {
				t.Fatal(err)
			}
			if err := writeStream.SetWriteDeadline(deadline); err != nil {
				t.Fatal(err)
			}
			if _, err := readStream.Read(make([]byte, 1)); !errors.Is(err, ErrTimeout) {
				t.Fatalf("read error=%v, want ErrTimeout", err)
			}
			if _, err := writeStream.Write([]byte("x")); !errors.Is(err, ErrTimeout) {
				t.Fatalf("write error=%v, want ErrTimeout", err)
			}
		})
	}
}

func TestShutdownReadDeadlineChangesInterruptReaderJoin(t *testing.T) {
	newWaitingRead := func(t *testing.T, deadline time.Time) (*Stream, chan struct{}, <-chan error) {
		t.Helper()
		recvDone := make(chan struct{})
		session := &Session{
			config:     DefaultConfig(),
			shutdownCh: closedShutdownWaitChannel(),
			recvDoneCh: recvDone,
		}
		stream := newStream(session, 1, streamEstablished)
		if err := stream.SetReadDeadline(deadline); err != nil {
			t.Fatal(err)
		}
		result := make(chan error, 1)
		go func() {
			_, err := stream.Read(make([]byte, 1))
			result <- err
		}()
		select {
		case err := <-result:
			t.Fatalf("read returned before deadline change: %v", err)
		case <-time.After(20 * time.Millisecond):
		}
		return stream, recvDone, result
	}

	t.Run("none to expired", func(t *testing.T) {
		stream, recvDone, result := newWaitingRead(t, time.Time{})
		defer close(recvDone)
		if err := stream.SetReadDeadline(time.Now().Add(-time.Second)); err != nil {
			t.Fatal(err)
		}
		select {
		case err := <-result:
			if !errors.Is(err, ErrTimeout) {
				t.Fatalf("read error=%v, want ErrTimeout", err)
			}
		case <-time.After(time.Second):
			t.Fatal("expired deadline did not interrupt reader join")
		}
	})

	t.Run("future to cleared", func(t *testing.T) {
		stream, recvDone, result := newWaitingRead(t, time.Now().Add(100*time.Millisecond))
		if err := stream.SetReadDeadline(time.Time{}); err != nil {
			t.Fatal(err)
		}
		select {
		case err := <-result:
			t.Fatalf("cleared deadline still terminated read: %v", err)
		case <-time.After(150 * time.Millisecond):
		}
		close(recvDone)
		select {
		case err := <-result:
			if !errors.Is(err, ErrSessionShutdown) {
				t.Fatalf("read error=%v, want ErrSessionShutdown", err)
			}
		case <-time.After(time.Second):
			t.Fatal("reader join did not release shutdown read")
		}
	})
}
