package server

import (
	"context"
	"errors"
	"net"
	"net/http"
	"sort"
	"strconv"
	"sync"
	"syscall"
	"time"

	"connectrpc.com/connect"
	"github.com/rs/zerolog/log"

	p2pstreamv1 "p2pstream/gen/proto/p2pstream/v1"
)

const (
	publicListenerProtocolHTTP    = "http"
	publicListenerProtocolHTTPS   = "https"
	publicListenerShutdownTimeout = 10 * time.Second
)

type publicListenerRuntime struct {
	Server       *http.Server
	TLSSelector  *publicTLSSelectorStore
	State        p2pstreamv1.ProxyState
	LastError    string
	StartedAt    time.Time
	StoppedAt    time.Time
	BoundAddress string
}

type resourceBoundedPublicListener struct {
	net.Listener
	acquire func(net.Conn) (func(), bool)
}

func (l resourceBoundedPublicListener) Accept() (net.Conn, error) {
	for {
		conn, err := l.Listener.Accept()
		if err != nil {
			return nil, err
		}
		tcpConn, ok := conn.(*net.TCPConn)
		if !ok {
			if release, ok := l.acquireConnection(conn); ok {
				return &resourceBoundedPublicConn{Conn: conn, release: release}, nil
			}
			_ = conn.Close()
			time.Sleep(10 * time.Millisecond)
			continue
		}
		// Bound attacker-controlled public socket queues so the adaptive
		// per-stream overhead reservation remains meaningful for slow uploads
		// and downloads. Linux can double the requested values; the reservation
		// includes both directions plus relay/allocator slack.
		const socketBufferBytes = 64 * 1024
		if err := tcpConn.SetReadBuffer(socketBufferBytes); err == nil {
			if err = tcpConn.SetWriteBuffer(socketBufferBytes); err == nil {
				if release, ok := l.acquireConnection(conn); ok {
					tracked := &resourceBoundedPublicConn{Conn: conn, release: release}
					return &resourceBoundedPublicTCPConn{resourceBoundedPublicConn: tracked, tcp: tcpConn}, nil
				}
			}
		}
		_ = conn.Close()
		time.Sleep(10 * time.Millisecond)
	}
}

func (l resourceBoundedPublicListener) acquireConnection(conn net.Conn) (func(), bool) {
	if l.acquire == nil {
		return func() {}, true
	}
	return l.acquire(conn)
}

// publicConnectionCapacityLimiter is an implementation guard, not a normal
// operating ceiling. Adaptive memory/FD accounting remains the binding global
// limit; the per-client guard prevents one direct peer from monopolizing every
// accepted keep-alive connection while doing no concurrent work.
type publicConnectionCapacityLimiter struct {
	mu          sync.Mutex
	global      int64
	perClient   int64
	total       int64
	clients     map[string]int64
	clientBytes map[string]int64
}

func newPublicConnectionCapacityLimiter(global, perClient int64) *publicConnectionCapacityLimiter {
	if global < 0 {
		global = 0
	}
	if perClient < 0 {
		perClient = 0
	}
	return &publicConnectionCapacityLimiter{
		global: global, perClient: perClient, clients: make(map[string]int64), clientBytes: make(map[string]int64),
	}
}

func (l *publicConnectionCapacityLimiter) tryAcquire(remote net.Addr, memoryBytes, peerMemoryLimit, peerFDLimit int64) (func(), bool) {
	if l == nil {
		return func() {}, true
	}
	client := publicConnectionClientKey(remote)
	l.mu.Lock()
	peerMemoryExceeded := l.perClient > 0 && peerMemoryLimit >= 0 &&
		(memoryBytes > peerMemoryLimit-l.clientBytes[client])
	peerFDExceeded := l.perClient > 0 && peerFDLimit >= 0 &&
		(1 > peerFDLimit-l.clients[client])
	if (l.global > 0 && l.total >= l.global) || (l.perClient > 0 && l.clients[client] >= l.perClient) || peerMemoryExceeded || peerFDExceeded {
		l.mu.Unlock()
		return nil, false
	}
	l.total++
	l.clients[client]++
	l.clientBytes[client] = saturatingAddInt64(l.clientBytes[client], memoryBytes)
	l.mu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			l.mu.Lock()
			l.total--
			l.clients[client]--
			l.clientBytes[client] -= memoryBytes
			if l.clients[client] == 0 {
				delete(l.clients, client)
				delete(l.clientBytes, client)
			}
			l.mu.Unlock()
		})
	}, true
}

func (l *publicConnectionCapacityLimiter) inUse() int64 {
	if l == nil {
		return 0
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.total
}

func (l *publicConnectionCapacityLimiter) peerGuardEnabled() bool {
	return l != nil && l.perClient > 0
}

func publicConnectionClientKey(remote net.Addr) string {
	if remote == nil {
		return "unknown"
	}
	host, _, err := net.SplitHostPort(remote.String())
	if err == nil && host != "" {
		return host
	}
	return remote.String()
}

type resourceBoundedPublicConn struct {
	net.Conn
	release func()
	once    sync.Once
}

func (c *resourceBoundedPublicConn) Close() error {
	defer c.once.Do(func() {
		if c.release != nil {
			c.release()
		}
	})
	return c.Conn.Close()
}

type resourceBoundedPublicTCPConn struct {
	*resourceBoundedPublicConn
	tcp *net.TCPConn
}

func (c *resourceBoundedPublicTCPConn) CloseRead() error {
	return c.tcp.CloseRead()
}

func (c *resourceBoundedPublicTCPConn) CloseWrite() error {
	return c.tcp.CloseWrite()
}

func (c *resourceBoundedPublicTCPConn) SyscallConn() (syscall.RawConn, error) {
	return c.tcp.SyscallConn()
}

func (a *App) tryReservePublicConnection(conn net.Conn) (func(), bool) {
	if a == nil {
		return nil, false
	}
	var remote net.Addr
	if conn != nil {
		remote = conn.RemoteAddr()
	}
	maxHeaderBytes := defaultPublicMaxHeaderBytes
	if a.Config != nil && a.Config.PublicMaxHeaderBytes > 0 {
		maxHeaderBytes = a.Config.PublicMaxHeaderBytes
	}
	// TCP queues, TLS/parser state, and the configured maximum attacker-owned
	// header all exist before request admission. Header parsing can transiently
	// hold input plus parsed strings, so charge it twice on top of 512 KiB of
	// socket/runtime slack for the connection lifetime.
	resourceBytes := int64(512*1024) + int64(maxHeaderBytes)*2
	peerMemoryLimit := int64(-1)
	peerFDLimit := int64(-1)
	if a.publicConnections.peerGuardEnabled() {
		if memoryLimit, fdLimit, adaptive := a.agentStreamCapacity.adaptiveExternalPeerLimits(); adaptive {
			peerMemoryLimit = memoryLimit
			peerFDLimit = fdLimit
		}
	}
	connectionRelease, ok := a.publicConnections.tryAcquire(remote, resourceBytes, peerMemoryLimit, peerFDLimit)
	if !ok {
		a.publicConnectionLimitRejected.Add(1)
		return nil, false
	}
	resourceRelease, resourceOK, constrained := a.agentStreamCapacity.tryReserveAdaptiveExternal(resourceBytes, 1)
	if constrained && !resourceOK {
		a.publicConnectionResourceReject.Add(1)
		connectionRelease()
		return nil, false
	}
	if resourceRelease == nil {
		resourceRelease = func() {}
	}
	var once sync.Once
	return func() {
		once.Do(func() {
			resourceRelease()
			connectionRelease()
		})
	}, true
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
	a.setPublicSnapshotLocked(snap)
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
		err := shutdownPublicHTTPServer(ctx, stop.Server, publicListenerShutdownTimeout)
		if err != nil {
			shutdownErr = errors.Join(shutdownErr, err)
		}
		a.proxyMu.Lock()
		if runtime := a.publicListenerState[stop.ID]; runtime != nil && runtime.Server == stop.Server {
			runtime.Server = nil
			runtime.TLSSelector = nil
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
	if a.DirectTransports != nil {
		a.DirectTransports.closeAll()
	}
	closePublicAccessProviderIdleConnections(a.currentPublicSnapshot())
	if err := a.FlushObservabilityRecorder(ctx); err != nil {
		log.Warn().Err(err).Msg("Failed to flush observability recorder after stopping proxy")
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

func shutdownPublicHTTPServer(ctx context.Context, srv *http.Server, timeout time.Duration) error {
	if srv == nil {
		return nil
	}
	shutdownCtx, cancel := publicListenerShutdownContext(ctx, timeout)
	defer cancel()
	err := srv.Shutdown(shutdownCtx)
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		log.Warn().Err(err).Dur("timeout", timeout).Msg("Public listener graceful shutdown interrupted; forcing close")
		if closeErr := srv.Close(); closeErr != nil {
			return errors.Join(err, closeErr)
		}
		return nil
	}
	return err
}

func publicListenerShutdownContext(ctx context.Context, timeout time.Duration) (context.Context, context.CancelFunc) {
	if ctx == nil {
		ctx = context.Background()
	}
	if timeout <= 0 {
		return context.WithCancel(ctx)
	}
	return context.WithTimeout(ctx, timeout)
}

type publicTLSSelectorRefresh struct {
	listenerID   int64
	listenerName string
	generation   uint64
	snapshot     *publicProxySnapshot
	server       *http.Server
	selector     *publicTLSSelectorStore
}

func (a *App) refreshRunningPublicTLSSelectors(snap *publicProxySnapshot, generation uint64) {
	if a == nil || snap == nil {
		return
	}
	updates := make([]publicTLSSelectorRefresh, 0)
	a.proxyMu.Lock()
	if a.publicSnapshot != snap || a.publicSnapshotGeneration != generation {
		a.proxyMu.Unlock()
		return
	}
	for listenerID, listener := range snap.Listeners {
		if listener.Protocol != publicListenerProtocolHTTPS {
			continue
		}
		runtime := a.publicListenerState[listenerID]
		if runtime == nil || runtime.Server == nil || runtime.TLSSelector == nil || runtime.State == p2pstreamv1.ProxyState_PROXY_STATE_STOPPING {
			continue
		}
		updates = append(updates, publicTLSSelectorRefresh{
			listenerID:   listenerID,
			listenerName: listener.Name,
			generation:   generation,
			snapshot:     snap,
			server:       runtime.Server,
			selector:     runtime.TLSSelector,
		})
	}
	a.proxyMu.Unlock()

	for _, update := range updates {
		selector, err := update.selector.buildRefresh(update.listenerID, snap, a.PublicACME)
		if hook := a.publicTLSSelectorRefreshBeforePublish; hook != nil {
			hook(update.generation)
		}
		if err != nil {
			log.Warn().
				Err(err).
				Int64("listener_id", update.listenerID).
				Str("listener", update.listenerName).
				Msg("Failed to refresh public TLS certificate selector")
			a.markPublicTLSSelectorRefreshError(update, err)
			continue
		}
		a.publishPublicTLSSelectorRefresh(update, selector)
	}
}

func (a *App) publicTLSSelectorRefreshCurrentLocked(update publicTLSSelectorRefresh) (*publicListenerRuntime, bool) {
	if a.publicSnapshot != update.snapshot || a.publicSnapshotGeneration != update.generation {
		return nil, false
	}
	runtime := a.publicListenerState[update.listenerID]
	if runtime == nil || runtime.Server != update.server || runtime.TLSSelector != update.selector || runtime.Server == nil || runtime.State == p2pstreamv1.ProxyState_PROXY_STATE_STOPPING {
		return nil, false
	}
	return runtime, true
}

func (a *App) markPublicTLSSelectorRefreshError(update publicTLSSelectorRefresh, err error) {
	a.proxyMu.Lock()
	defer a.proxyMu.Unlock()
	runtime, ok := a.publicTLSSelectorRefreshCurrentLocked(update)
	if !ok {
		return
	}
	// The listener is still serving with its last known-good selector. Keep it
	// addressable so a retry cannot orphan the live server by starting a second
	// listener on the same address.
	runtime.State = p2pstreamv1.ProxyState_PROXY_STATE_RUNNING
	runtime.LastError = "refresh TLS certificates: " + err.Error()
	a.proxyStatusLocked()
}

func (a *App) publishPublicTLSSelectorRefresh(update publicTLSSelectorRefresh, selector *publicTLSSelector) {
	a.proxyMu.Lock()
	defer a.proxyMu.Unlock()
	runtime, ok := a.publicTLSSelectorRefreshCurrentLocked(update)
	if !ok {
		return
	}
	update.selector.publish(selector)
	runtime.State = p2pstreamv1.ProxyState_PROXY_STATE_RUNNING
	runtime.LastError = ""
	a.proxyStatusLocked()
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
	if runtime.Server != nil {
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
	var tlsSelector *publicTLSSelectorStore
	if listener.Protocol == publicListenerProtocolHTTPS {
		tlsConfig, selector, err := newPublicTLSConfigWithSelectorStore(listener.ID, snap, a.PublicACME)
		if err != nil {
			a.setPublicListenerError(listener.ID, err)
			return a.getPublicListenerStatus(listener.ID), nil
		}
		tlsSelector = selector
		srv = &http.Server{Addr: addr, Handler: mux, TLSConfig: tlsConfig}
		configurePublicHTTPServer(srv, a.Config.PublicMaxHeaderBytes)
		serve = func(ln net.Listener) error {
			return srv.ServeTLS(ln, "", "")
		}
	} else {
		srv = &http.Server{Addr: addr, Handler: mux}
		configurePublicHTTPServer(srv, a.Config.PublicMaxHeaderBytes)
		serve = srv.Serve
	}

	ln, err := net.Listen("tcp", addr)
	if err != nil {
		a.setPublicListenerError(listener.ID, err)
		log.Error().Err(err).Str("addr", addr).Str("listener", listener.Name).Msg("Public listener failed to listen")
		return a.getPublicListenerStatus(listener.ID), nil
	}
	ln = resourceBoundedPublicListener{Listener: ln, acquire: a.tryReservePublicConnection}

	a.proxyMu.Lock()
	runtime := a.ensureListenerStateLocked(listener.ID)
	runtime.Server = srv
	runtime.TLSSelector = tlsSelector
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
				runtime.TLSSelector = nil
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
		runtime.TLSSelector = nil
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

	err := shutdownPublicHTTPServer(ctx, srv, publicListenerShutdownTimeout)

	a.proxyMu.Lock()
	runtime = a.ensureListenerStateLocked(listenerID)
	if runtime.Server == srv {
		runtime.Server = nil
		runtime.TLSSelector = nil
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
	runtime.TLSSelector = nil
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
