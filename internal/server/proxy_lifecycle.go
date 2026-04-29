package server

import (
	"context"
	"net"
	"net/http"
	"time"

	"connectrpc.com/connect"
	"github.com/rs/zerolog/log"

	p2pstreamv1 "p2pstream/gen/proto/p2pstream/v1"
)

func (a *App) StartProxy(ctx context.Context, req *connect.Request[p2pstreamv1.StartProxyRequest]) (*connect.Response[p2pstreamv1.StartProxyResponse], error) {
	if _, err := a.requireAdmin(ctx, req.Header()); err != nil {
		return nil, err
	}

	status, err := a.startProxy(ctx)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&p2pstreamv1.StartProxyResponse{Proxy: status}), nil
}

func (a *App) StopProxy(ctx context.Context, req *connect.Request[p2pstreamv1.StopProxyRequest]) (*connect.Response[p2pstreamv1.StopProxyResponse], error) {
	if _, err := a.requireAdmin(ctx, req.Header()); err != nil {
		return nil, err
	}

	shutdownCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	status, err := a.stopProxy(shutdownCtx)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&p2pstreamv1.StopProxyResponse{Proxy: status}), nil
}

func (a *App) StartProxyListener(ctx context.Context) (*p2pstreamv1.ProxyStatus, error) {
	return a.startProxy(ctx)
}

func (a *App) StopProxyListener(ctx context.Context) (*p2pstreamv1.ProxyStatus, error) {
	return a.stopProxy(ctx)
}

func (a *App) startProxy(ctx context.Context) (*p2pstreamv1.ProxyStatus, error) {
	_ = ctx

	a.proxyMu.Lock()
	defer a.proxyMu.Unlock()

	if a.proxyState == p2pstreamv1.ProxyState_PROXY_STATE_RUNNING ||
		a.proxyState == p2pstreamv1.ProxyState_PROXY_STATE_STARTING {
		return a.proxyStatusLocked(), nil
	}

	a.proxyState = p2pstreamv1.ProxyState_PROXY_STATE_STARTING
	proxyAddr := ":" + a.Config.Port
	mux := http.NewServeMux()
	a.RegisterProxyRoutes(mux)

	ln, err := net.Listen("tcp", proxyAddr)
	if err != nil {
		errMsg := err.Error()
		a.proxyState = p2pstreamv1.ProxyState_PROXY_STATE_ERROR
		a.proxyLastError = errMsg
		a.ProxyLastError.Store(&errMsg)
		a.ProxyIsRunning.Store(false)
		log.Error().Err(err).Str("addr", proxyAddr).Msg("Proxy server failed to listen")
		return a.proxyStatusLocked(), nil
	}

	srv := &http.Server{
		Addr:    proxyAddr,
		Handler: mux,
	}
	a.proxySrv = srv
	a.proxyState = p2pstreamv1.ProxyState_PROXY_STATE_RUNNING
	a.proxyLastError = ""
	a.proxyStartedAt = time.Now()
	a.ProxyIsRunning.Store(true)

	log.Info().
		Str("url", "http://localhost"+proxyAddr).
		Str("target", a.Config.TargetOrigin).
		Msg("Proxy server listening")

	go func() {
		err := srv.Serve(ln)
		if err != nil && err != http.ErrServerClosed {
			errMsg := err.Error()
			a.proxyMu.Lock()
			a.proxyState = p2pstreamv1.ProxyState_PROXY_STATE_ERROR
			a.proxyLastError = errMsg
			a.proxySrv = nil
			a.proxyStoppedAt = time.Now()
			a.proxyMu.Unlock()
			a.ProxyLastError.Store(&errMsg)
			a.ProxyIsRunning.Store(false)
			log.Error().Err(err).Msg("Proxy server failed")
		}
	}()

	return a.proxyStatusLocked(), nil
}

func (a *App) stopProxy(ctx context.Context) (*p2pstreamv1.ProxyStatus, error) {
	a.proxyMu.Lock()
	if a.proxySrv == nil || a.proxyState == p2pstreamv1.ProxyState_PROXY_STATE_STOPPED {
		a.proxyState = p2pstreamv1.ProxyState_PROXY_STATE_STOPPED
		a.ProxyIsRunning.Store(false)
		status := a.proxyStatusLocked()
		a.proxyMu.Unlock()
		return status, nil
	}

	srv := a.proxySrv
	a.proxyState = p2pstreamv1.ProxyState_PROXY_STATE_STOPPING
	a.proxyMu.Unlock()

	err := srv.Shutdown(ctx)

	a.proxyMu.Lock()
	defer a.proxyMu.Unlock()
	a.proxySrv = nil
	a.proxyStoppedAt = time.Now()
	a.ProxyIsRunning.Store(false)
	if err != nil {
		errMsg := err.Error()
		a.proxyState = p2pstreamv1.ProxyState_PROXY_STATE_ERROR
		a.proxyLastError = errMsg
		a.ProxyLastError.Store(&errMsg)
		return a.proxyStatusLocked(), connect.NewError(connect.CodeInternal, err)
	}

	a.proxyState = p2pstreamv1.ProxyState_PROXY_STATE_STOPPED
	return a.proxyStatusLocked(), nil
}

func (a *App) proxyStatus() *p2pstreamv1.ProxyStatus {
	a.proxyMu.Lock()
	defer a.proxyMu.Unlock()
	return a.proxyStatusLocked()
}

func (a *App) proxyStatusLocked() *p2pstreamv1.ProxyStatus {
	status := &p2pstreamv1.ProxyStatus{
		State:     a.proxyState,
		LastError: a.proxyLastError,
	}
	if !a.proxyStartedAt.IsZero() {
		status.StartedAtUnixMillis = a.proxyStartedAt.UnixMilli()
	}
	if !a.proxyStoppedAt.IsZero() {
		status.StoppedAtUnixMillis = a.proxyStoppedAt.UnixMilli()
	}
	return status
}
