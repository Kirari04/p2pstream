package server

import (
	"net/http"
	"time"
)

const (
	managementReadHeaderTimeout = 5 * time.Second
	managementReadTimeout       = 30 * time.Second
	managementWriteTimeout      = 60 * time.Second
	managementIdleTimeout       = 120 * time.Second

	publicReadHeaderTimeout = 5 * time.Second
	publicIdleTimeout       = 120 * time.Second

	defaultManagementMaxHeaderBytes = 1 << 20
	defaultPublicMaxHeaderBytes     = 64 << 10
)

func ConfigureManagementHTTPServer(srv *http.Server) {
	if srv == nil {
		return
	}
	srv.ReadHeaderTimeout = managementReadHeaderTimeout
	srv.ReadTimeout = managementReadTimeout
	srv.WriteTimeout = managementWriteTimeout
	srv.IdleTimeout = managementIdleTimeout
	srv.MaxHeaderBytes = defaultManagementMaxHeaderBytes
}

func configurePublicHTTPServer(srv *http.Server, configuredMaxHeaderBytes ...int) {
	if srv == nil {
		return
	}
	srv.ReadHeaderTimeout = publicReadHeaderTimeout
	srv.IdleTimeout = publicIdleTimeout
	maxHeaderBytes := defaultPublicMaxHeaderBytes
	if len(configuredMaxHeaderBytes) > 0 && configuredMaxHeaderBytes[0] > 0 {
		maxHeaderBytes = configuredMaxHeaderBytes[0]
	}
	srv.MaxHeaderBytes = maxHeaderBytes
}
