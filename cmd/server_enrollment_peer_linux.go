//go:build linux

package cmd

import (
	"net"
	"os"

	"golang.org/x/sys/unix"
)

func serverEnrollmentPreflightPeerVerifier() (func(net.Conn) error, error) {
	// The supported Docker image executes /app/p2pstream server directly as
	// PID 1. docker exec uses that image's configured server user for this CLI.
	// Do not infer authority from the pathname, file ownership, or response JSON.
	uid := uint32(os.Geteuid())
	return func(connection net.Conn) error {
		return verifyServerEnrollmentPeer(connection, 1, uid)
	}, nil
}

func verifyServerEnrollmentPeer(connection net.Conn, expectedPID int32, expectedUID uint32) error {
	socket, ok := connection.(*net.UnixConn)
	if !ok {
		return errServerEnrollmentPreflightPeer
	}
	raw, err := socket.SyscallConn()
	if err != nil {
		return errServerEnrollmentPreflightPeer
	}
	var peer *unix.Ucred
	var credentialErr error
	err = raw.Control(func(fd uintptr) {
		peer, credentialErr = unix.GetsockoptUcred(int(fd), unix.SOL_SOCKET, unix.SO_PEERCRED)
	})
	if err != nil || credentialErr != nil || peer == nil || peer.Pid != expectedPID || peer.Uid != expectedUID {
		return errServerEnrollmentPreflightPeer
	}
	return nil
}
