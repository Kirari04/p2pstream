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

	defaultMaxHeaderBytes = 1 << 20
)

func ConfigureManagementHTTPServer(srv *http.Server) {
	if srv == nil {
		return
	}
	srv.ReadHeaderTimeout = managementReadHeaderTimeout
	srv.ReadTimeout = managementReadTimeout
	srv.WriteTimeout = managementWriteTimeout
	srv.IdleTimeout = managementIdleTimeout
	srv.MaxHeaderBytes = defaultMaxHeaderBytes
}

func configurePublicHTTPServer(srv *http.Server) {
	if srv == nil {
		return
	}
	srv.ReadHeaderTimeout = publicReadHeaderTimeout
	srv.IdleTimeout = publicIdleTimeout
	srv.MaxHeaderBytes = defaultMaxHeaderBytes
}
