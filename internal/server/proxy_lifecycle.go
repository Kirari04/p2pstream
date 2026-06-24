package server

import (
	"context"
	"errors"
	"net"
	"net/http"
	"sort"
	"strconv"
	"time"

	"connectrpc.com/connect"
	"github.com/rs/zerolog/log"

	p2pstreamv1 "p2pstream/gen/proto/p2pstream/v1"
)

const (
	publicListenerProtocolHTTP  = "http"
	publicListenerProtocolHTTPS = "https"
)

type publicListenerRuntime struct {
	Server       *http.Server
	State        p2pstreamv1.ProxyState
	LastError    string
	StartedAt    time.Time
	StoppedAt    time.Time
	BoundAddress string
}

func (a *App) StartProxy(ctx context.Context, req *connect.Request[p2pstreamv1.StartProxyRequest]) (*connect.Response[p2pstreamv1.StartProxyResponse], error) {
	if _, err := a.requireAdmin(ctx, req.Header()); err != nil {
		return nil, err
	}

	status, err := a.proxyRuntimeService().start(ctx)
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

	status, err := a.proxyRuntimeService().stop(shutdownCtx)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&p2pstreamv1.StopProxyResponse{Proxy: status}), nil
}

func (a *App) StartProxyListener(ctx context.Context) (*p2pstreamv1.ProxyStatus, error) {
	return a.proxyRuntimeService().start(ctx)
}

func (a *App) StopProxyListener(ctx context.Context) (*p2pstreamv1.ProxyStatus, error) {
	return a.proxyRuntimeService().stop(ctx)
}

func (a *App) startProxy(ctx context.Context) (*p2pstreamv1.ProxyStatus, error) {
	snap, err := a.loadPublicProxySnapshot(ctx)
	if err != nil {
		return nil, err
	}

	a.proxyMu.Lock()
	a.publicSnapshot = snap
	a.proxyServiceActive = true
	a.ensureListenerStatesLocked(snap)
	a.proxyState = p2pstreamv1.ProxyState_PROXY_STATE_STARTING
	a.proxyMu.Unlock()
	if a.LoadBalancers != nil {
		a.LoadBalancers.reconcile(snap)
	}
	if a.TargetHealth != nil {
		a.TargetHealth.reconcile(a, snap, true)
	}

	for _, listener := range snap.Listeners {
		if !listener.Enabled {
			continue
		}
		_, _ = a.startPublicListenerRuntime(ctx, listener.ID, false)
	}

	a.proxyMu.Lock()
	status := a.proxyStatusLocked()
	a.proxyMu.Unlock()
	return status, nil
}

func (a *App) stopProxy(ctx context.Context) (*p2pstreamv1.ProxyStatus, error) {
	a.proxyMu.Lock()
	a.proxyServiceActive = false
	var stops []publicListenerStop
	for id, runtime := range a.publicListenerState {
		if runtime.Server == nil {
			continue
		}
		runtime.State = p2pstreamv1.ProxyState_PROXY_STATE_STOPPING
		stops = append(stops, publicListenerStop{ID: id, Server: runtime.Server})
	}
	a.proxyMu.Unlock()

	var shutdownErr error
	for _, stop := range stops {
		err := stop.Server.Shutdown(ctx)
		if err != nil {
			shutdownErr = errors.Join(shutdownErr, err)
		}
		a.proxyMu.Lock()
		if runtime := a.publicListenerState[stop.ID]; runtime != nil && runtime.Server == stop.Server {
			runtime.Server = nil
			runtime.StoppedAt = time.Now()
			if err != nil {
				runtime.State = p2pstreamv1.ProxyState_PROXY_STATE_ERROR
				runtime.LastError = err.Error()
			} else {
				runtime.State = p2pstreamv1.ProxyState_PROXY_STATE_STOPPED
				runtime.LastError = ""
			}
		}
		a.proxyMu.Unlock()
	}

	a.proxyMu.Lock()
	status := a.proxyStatusLocked()
	a.proxyMu.Unlock()
	if a.TargetHealth != nil {
		a.TargetHealth.reconcile(a, nil, false)
	}
	if shutdownErr != nil {
		return status, connect.NewError(connect.CodeInternal, shutdownErr)
	}
	return status, nil
}

type publicListenerStop struct {
	ID     int64
	Server *http.Server
}

func (a *App) startPublicListenerRuntime(ctx context.Context, listenerID int64, activateService bool) (*p2pstreamv1.PublicListenerStatus, error) {
	_ = ctx

	a.proxyMu.Lock()
	if activateService {
		a.proxyServiceActive = true
	}
	snap := a.publicSnapshot
	if snap == nil {
		a.proxyMu.Unlock()
		if err := a.refreshPublicProxySnapshot(ctx); err != nil {
			return nil, err
		}
		a.proxyMu.Lock()
		snap = a.publicSnapshot
	}
	listener, ok := snap.Listeners[listenerID]
	if !ok {
		a.proxyMu.Unlock()
		return nil, connect.NewError(connect.CodeNotFound, errors.New("listener not found"))
	}
	runtime := a.ensureListenerStateLocked(listenerID)
	if !listener.Enabled {
		runtime.State = p2pstreamv1.ProxyState_PROXY_STATE_STOPPED
		runtime.LastError = ""
		status := a.publicListenerStatusLocked(listenerID)
		a.proxyStatusLocked()
		a.proxyMu.Unlock()
		return status, connect.NewError(connect.CodeFailedPrecondition, errors.New("listener is disabled"))
	}
	if runtime.Server != nil && runtime.State == p2pstreamv1.ProxyState_PROXY_STATE_RUNNING {
		status := a.publicListenerStatusLocked(listenerID)
		a.proxyStatusLocked()
		a.proxyMu.Unlock()
		return status, nil
	}

	runtime.State = p2pstreamv1.ProxyState_PROXY_STATE_STARTING
	runtime.LastError = ""
	a.proxyStatusLocked()
	a.proxyMu.Unlock()

	status, err := a.startPublicListenerFromSnapshot(listener, snap)
	return status, err
}

func (a *App) startPublicListenerFromSnapshot(listener publicListenerConfig, snap *publicProxySnapshot) (*p2pstreamv1.PublicListenerStatus, error) {
	addr := listenerAddress(listener)
	mux := http.NewServeMux()
	mux.HandleFunc("/", a.publicProxyHandler(listener.ID))

	var srv *http.Server
	var serve func(net.Listener) error
	if listener.Protocol == publicListenerProtocolHTTPS {
		tlsConfig, err := newPublicTLSConfigWithApp(context.Background(), a, listener.ID, snap, a.PublicACME)
		if err != nil {
			a.setPublicListenerError(listener.ID, err)
			return a.getPublicListenerStatus(listener.ID), nil
		}
		srv = &http.Server{Addr: addr, Handler: mux, TLSConfig: tlsConfig}
		configurePublicHTTPServer(srv)
		serve = func(ln net.Listener) error {
			return srv.ServeTLS(ln, "", "")
		}
	} else {
		srv = &http.Server{Addr: addr, Handler: mux}
		configurePublicHTTPServer(srv)
		serve = srv.Serve
	}

	ln, err := net.Listen("tcp", addr)
	if err != nil {
		a.setPublicListenerError(listener.ID, err)
		log.Error().Err(err).Str("addr", addr).Str("listener", listener.Name).Msg("Public listener failed to listen")
		return a.getPublicListenerStatus(listener.ID), nil
	}

	a.proxyMu.Lock()
	runtime := a.ensureListenerStateLocked(listener.ID)
	runtime.Server = srv
	runtime.State = p2pstreamv1.ProxyState_PROXY_STATE_RUNNING
	runtime.LastError = ""
	runtime.StartedAt = time.Now()
	runtime.BoundAddress = ln.Addr().String()
	status := a.publicListenerStatusLocked(listener.ID)
	a.proxyStatusLocked()
	a.proxyMu.Unlock()

	log.Info().
		Str("addr", ln.Addr().String()).
		Str("protocol", listener.Protocol).
		Str("listener", listener.Name).
		Msg("Public listener started")

	go func() {
		err := serve(ln)
		if err != nil && err != http.ErrServerClosed {
			errMsg := err.Error()
			a.proxyMu.Lock()
			if runtime := a.publicListenerState[listener.ID]; runtime != nil && runtime.Server == srv {
				runtime.Server = nil
				runtime.State = p2pstreamv1.ProxyState_PROXY_STATE_ERROR
				runtime.LastError = errMsg
				runtime.StoppedAt = time.Now()
			}
			a.proxyStatusLocked()
			a.proxyMu.Unlock()
			log.Error().Err(err).Str("listener", listener.Name).Msg("Public listener failed")
		}
	}()

	return status, nil
}

func (a *App) stopPublicListenerRuntime(ctx context.Context, listenerID int64) (*p2pstreamv1.PublicListenerStatus, error) {
	a.proxyMu.Lock()
	if a.publicSnapshot != nil {
		if _, ok := a.publicSnapshot.Listeners[listenerID]; !ok {
			a.proxyMu.Unlock()
			return nil, connect.NewError(connect.CodeNotFound, errors.New("listener not found"))
		}
	}
	runtime := a.ensureListenerStateLocked(listenerID)
	if runtime.Server == nil {
		runtime.State = p2pstreamv1.ProxyState_PROXY_STATE_STOPPED
		status := a.publicListenerStatusLocked(listenerID)
		a.proxyStatusLocked()
		a.proxyMu.Unlock()
		return status, nil
	}
	srv := runtime.Server
	runtime.State = p2pstreamv1.ProxyState_PROXY_STATE_STOPPING
	a.proxyStatusLocked()
	a.proxyMu.Unlock()

	err := srv.Shutdown(ctx)

	a.proxyMu.Lock()
	runtime = a.ensureListenerStateLocked(listenerID)
	if runtime.Server == srv {
		runtime.Server = nil
		runtime.StoppedAt = time.Now()
		if err != nil {
			runtime.State = p2pstreamv1.ProxyState_PROXY_STATE_ERROR
			runtime.LastError = err.Error()
		} else {
			runtime.State = p2pstreamv1.ProxyState_PROXY_STATE_STOPPED
			runtime.LastError = ""
		}
	}
	status := a.publicListenerStatusLocked(listenerID)
	a.proxyStatusLocked()
	a.proxyMu.Unlock()

	if err != nil {
		return status, connect.NewError(connect.CodeInternal, err)
	}
	return status, nil
}

func (a *App) restartPublicListenerRuntime(ctx context.Context, listenerID int64) (*p2pstreamv1.PublicListenerStatus, error) {
	if _, err := a.stopPublicListenerRuntime(ctx, listenerID); err != nil {
		return a.getPublicListenerStatus(listenerID), err
	}
	return a.startPublicListenerRuntime(ctx, listenerID, false)
}

func (a *App) setPublicListenerError(listenerID int64, err error) {
	a.proxyMu.Lock()
	runtime := a.ensureListenerStateLocked(listenerID)
	runtime.Server = nil
	runtime.State = p2pstreamv1.ProxyState_PROXY_STATE_ERROR
	runtime.LastError = err.Error()
	runtime.StoppedAt = time.Now()
	a.proxyStatusLocked()
	a.proxyMu.Unlock()
}

func (a *App) getPublicListenerStatus(listenerID int64) *p2pstreamv1.PublicListenerStatus {
	a.proxyMu.Lock()
	defer a.proxyMu.Unlock()
	return a.publicListenerStatusLocked(listenerID)
}

func (a *App) proxyStatus() *p2pstreamv1.ProxyStatus {
	a.proxyMu.Lock()
	defer a.proxyMu.Unlock()
	return a.proxyStatusLocked()
}

func (a *App) ensureListenerStatesLocked(snap *publicProxySnapshot) {
	for id := range snap.Listeners {
		a.ensureListenerStateLocked(id)
	}
	for id, runtime := range a.publicListenerState {
		if _, ok := snap.Listeners[id]; !ok && runtime.Server == nil {
			delete(a.publicListenerState, id)
		}
	}
}

func (a *App) ensureListenerStateLocked(listenerID int64) *publicListenerRuntime {
	if a.publicListenerState == nil {
		a.publicListenerState = make(map[int64]*publicListenerRuntime)
	}
	runtime := a.publicListenerState[listenerID]
	if runtime == nil {
		runtime = &publicListenerRuntime{State: p2pstreamv1.ProxyState_PROXY_STATE_STOPPED}
		a.publicListenerState[listenerID] = runtime
	}
	return runtime
}

func (a *App) proxyStatusLocked() *p2pstreamv1.ProxyStatus {
	statuses := make([]*p2pstreamv1.PublicListenerStatus, 0)
	enabledCount := 0
	runningCount := 0
	hasError := false
	hasStarting := false
	hasStopping := false
	var lastError string
	var startedAt int64
	var stoppedAt int64

	if a.publicSnapshot != nil {
		for id, listener := range a.publicSnapshot.Listeners {
			runtime := a.ensureListenerStateLocked(id)
			listenerStatus := a.publicListenerStatusFromRuntimeLocked(id, runtime)
			if !listener.Enabled {
				listenerStatus.Disabled = true
				if runtime.Server == nil {
					listenerStatus.State = p2pstreamv1.ProxyState_PROXY_STATE_STOPPED
				}
			} else {
				enabledCount++
				switch runtime.State {
				case p2pstreamv1.ProxyState_PROXY_STATE_RUNNING:
					runningCount++
				case p2pstreamv1.ProxyState_PROXY_STATE_ERROR:
					hasError = true
					if lastError == "" {
						lastError = runtime.LastError
					}
				case p2pstreamv1.ProxyState_PROXY_STATE_STARTING:
					hasStarting = true
				case p2pstreamv1.ProxyState_PROXY_STATE_STOPPING:
					hasStopping = true
				}
			}
			if listenerStatus.StartedAtUnixMillis > 0 && (startedAt == 0 || listenerStatus.StartedAtUnixMillis < startedAt) {
				startedAt = listenerStatus.StartedAtUnixMillis
			}
			if listenerStatus.StoppedAtUnixMillis > stoppedAt {
				stoppedAt = listenerStatus.StoppedAtUnixMillis
			}
			statuses = append(statuses, listenerStatus)
		}
	}
	sort.SliceStable(statuses, func(i, j int) bool {
		return statuses[i].ListenerId < statuses[j].ListenerId
	})

	state := p2pstreamv1.ProxyState_PROXY_STATE_STOPPED
	switch {
	case hasError:
		state = p2pstreamv1.ProxyState_PROXY_STATE_ERROR
	case hasStarting:
		state = p2pstreamv1.ProxyState_PROXY_STATE_STARTING
	case hasStopping:
		state = p2pstreamv1.ProxyState_PROXY_STATE_STOPPING
	case runningCount > 0:
		state = p2pstreamv1.ProxyState_PROXY_STATE_RUNNING
	case enabledCount == 0 && a.proxyServiceActive:
		state = p2pstreamv1.ProxyState_PROXY_STATE_STOPPED
	}

	a.proxyState = state
	a.proxyLastError = lastError
	a.ProxyIsRunning.Store(runningCount > 0)
	if lastError != "" {
		a.ProxyLastError.Store(&lastError)
	} else {
		a.ProxyLastError.Store(nil)
	}

	return &p2pstreamv1.ProxyStatus{
		State:               state,
		LastError:           lastError,
		StartedAtUnixMillis: startedAt,
		StoppedAtUnixMillis: stoppedAt,
		Listeners:           statuses,
	}
}

func (a *App) publicListenerStatusLocked(listenerID int64) *p2pstreamv1.PublicListenerStatus {
	return a.publicListenerStatusFromRuntimeLocked(listenerID, a.ensureListenerStateLocked(listenerID))
}

func (a *App) publicListenerStatusFromRuntimeLocked(listenerID int64, runtime *publicListenerRuntime) *p2pstreamv1.PublicListenerStatus {
	disabled := false
	if a.publicSnapshot != nil {
		if listener, ok := a.publicSnapshot.Listeners[listenerID]; ok {
			disabled = !listener.Enabled
		}
	}
	status := &p2pstreamv1.PublicListenerStatus{
		ListenerId:   listenerID,
		State:        runtime.State,
		LastError:    runtime.LastError,
		BoundAddress: runtime.BoundAddress,
		Running:      runtime.Server != nil && runtime.State == p2pstreamv1.ProxyState_PROXY_STATE_RUNNING,
		Disabled:     disabled,
	}
	if !runtime.StartedAt.IsZero() {
		status.StartedAtUnixMillis = runtime.StartedAt.UnixMilli()
	}
	if !runtime.StoppedAt.IsZero() {
		status.StoppedAtUnixMillis = runtime.StoppedAt.UnixMilli()
	}
	return status
}

func listenerAddress(listener publicListenerConfig) string {
	port := strconv.FormatInt(listener.Port, 10)
	if listener.BindAddress == "" {
		return ":" + port
	}
	return net.JoinHostPort(listener.BindAddress, port)
}
