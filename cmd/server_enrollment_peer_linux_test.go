//go:build linux

package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
	"p2pstream/internal/buildinfo"
	"p2pstream/internal/serverupdate"
)

func TestServerEnrollmentPeerKernelCredentials(t *testing.T) {
	path := filepath.Join(t.TempDir(), "peer.sock")
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	connection, err := net.Dial("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	pid, uid := int32(os.Getpid()), uint32(os.Geteuid())
	for _, test := range []struct {
		name    string
		pid     int32
		uid     uint32
		allowed bool
	}{
		{"own peer", pid, uid, true},
		{"same UID foreign PID", pid + 1, uid, false},
		{"matching PID foreign UID", pid, uid + 1, false},
		{"foreign PID and UID", pid + 1, uid + 1, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := verifyServerEnrollmentPeer(connection, test.pid, test.uid)
			if (err == nil) != test.allowed || err != nil && !errors.Is(err, errServerEnrollmentPreflightPeer) {
				t.Fatalf("kernel peer check = %v, allowed = %v", err, test.allowed)
			}
		})
	}
	verifier, err := serverEnrollmentPreflightPeerVerifier()
	if err != nil {
		t.Fatal(err)
	}
	if os.Getpid() != 1 && !errors.Is(verifier(connection), errServerEnrollmentPreflightPeer) {
		t.Fatal("public verifier did not pin PID 1")
	}
	_ = connection.Close()
	if err := verifyServerEnrollmentPeer(connection, pid, uid); !errors.Is(err, errServerEnrollmentPreflightPeer) {
		t.Fatalf("closed socket accepted: %v", err)
	}
	left, right := net.Pipe()
	defer left.Close()
	defer right.Close()
	if err := verifyServerEnrollmentPeer(left, pid, uid); !errors.Is(err, errServerEnrollmentPreflightPeer) {
		t.Fatalf("non-Unix connection accepted: %v", err)
	}
}

func TestServerEnrollmentPreflightTrustedKernelPeerPrintsReadyAndBlocked(t *testing.T) {
	for _, ready := range []bool{true, false} {
		id := uuid.NewString()
		path := filepath.Join(t.TempDir(), "trusted.sock")
		listener, err := net.Listen("unix", path)
		if err != nil {
			t.Fatal(err)
		}
		status := serverupdate.RuntimeStatus{Ready: ready, Version: buildinfo.Version, Commit: buildinfo.Commit}
		preflight := serverupdate.NewEnrollmentPreflight(id, status)
		srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _ = json.NewEncoder(w).Encode(preflight) })}
		go func() { _ = srv.Serve(listener) }()
		t.Cleanup(func() { _ = srv.Close() })
		var out bytes.Buffer
		err = printServerEnrollmentPreflightWithPeer(context.Background(), &out, path, id, func(connection net.Conn) error {
			return verifyServerEnrollmentPeer(connection, int32(os.Getpid()), uint32(os.Geteuid()))
		})
		got, decodeErr := serverupdate.DecodeEnrollmentPreflight(&out)
		if err != nil || decodeErr != nil || got.InstallationID != id || got.Ready != ready {
			t.Fatalf("trusted peer response = %+v error=%v decode=%v", got, err, decodeErr)
		}
	}
}

func TestServerEnrollmentPreflightRejectsAndClosesPeerBeforeHTTP(t *testing.T) {
	for _, verifier := range []func(net.Conn) error{nil, func(connection net.Conn) error {
		return verifyServerEnrollmentPeer(connection, int32(os.Getpid()), uint32(os.Geteuid())+1)
	}} {
		path := filepath.Join(t.TempDir(), "rejected.sock")
		listener, err := net.Listen("unix", path)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = listener.Close() })
		closed := make(chan error, 1)
		go func() {
			connection, err := listener.Accept()
			if err != nil {
				closed <- err
				return
			}
			defer connection.Close()
			_ = connection.SetReadDeadline(time.Now().Add(time.Second))
			var request [1]byte
			count, err := connection.Read(request[:])
			if count != 0 {
				err = errors.New("HTTP bytes sent before peer authentication")
			}
			closed <- err
		}()
		var out bytes.Buffer
		err = printServerEnrollmentPreflightWithPeer(context.Background(), &out, path, uuid.NewString(), verifier)
		if !errors.Is(err, errServerEnrollmentPreflightPeer) || out.Len() != 0 {
			t.Fatalf("untrusted peer result = %q %v", out.Bytes(), err)
		}
		select {
		case err := <-closed:
			if !errors.Is(err, io.EOF) {
				t.Fatalf("untrusted connection not closed before HTTP: %v", err)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("untrusted connection survived rejection")
		}
	}
}

func TestServerEnrollmentPreflightCancellationAndTimeoutCloseConnection(t *testing.T) {
	for _, timeout := range []bool{false, true} {
		path := filepath.Join(t.TempDir(), "silent.sock")
		listener, err := net.Listen("unix", path)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = listener.Close() })
		accepted, closed := make(chan struct{}), make(chan error, 1)
		go func() {
			connection, err := listener.Accept()
			if err != nil {
				closed <- err
				return
			}
			defer connection.Close()
			close(accepted)
			_ = connection.SetReadDeadline(time.Now().Add(2 * time.Second))
			_, err = io.Copy(io.Discard, connection)
			closed <- err
		}()
		ctx, cancel := context.WithCancel(context.Background())
		if timeout {
			cancel()
			ctx, cancel = context.WithTimeout(context.Background(), 100*time.Millisecond)
		}
		defer cancel()
		result := make(chan error, 1)
		var out bytes.Buffer
		go func() {
			result <- printServerEnrollmentPreflightWithPeer(ctx, &out, path, uuid.NewString(), func(connection net.Conn) error {
				return verifyServerEnrollmentPeer(connection, int32(os.Getpid()), uint32(os.Geteuid()))
			})
		}()
		select {
		case <-accepted:
			if !timeout {
				cancel()
			}
		case <-time.After(time.Second):
			t.Fatal("preflight did not connect")
		}
		select {
		case err := <-result:
			if err == nil || out.Len() != 0 {
				t.Fatalf("cancellation/timeout failed open: %q %v", out.Bytes(), err)
			}
		case <-time.After(time.Second):
			t.Fatal("preflight ignored cancellation/timeout")
		}
		select {
		case err := <-closed:
			if err != nil {
				t.Fatalf("cancelled connection not closed: %v", err)
			}
		case <-time.After(time.Second):
			t.Fatal("cancelled connection survived")
		}
	}
}
