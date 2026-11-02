//go:build !linux

package cmd

import "net"

func serverEnrollmentPreflightPeerVerifier() (func(net.Conn) error, error) {
	return nil, errServerEnrollmentPreflightPlatform
}
