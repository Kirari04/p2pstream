package agent

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"net"
	"net/http"
	"net/url"
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"connectrpc.com/connect"
	"github.com/hashicorp/yamux"
	"github.com/rs/zerolog/log"

	p2pstreamv1 "p2pstream/gen/proto/p2pstream/v1"
	"p2pstream/gen/proto/p2pstream/v1/p2pstreamv1connect"
	"p2pstream/internal/sysmetrics"
	"p2pstream/internal/tunnel"
)

var (
	activeRequests   atomic.Int32
	reqSuccess       atomic.Int32
	reqClientError   atomic.Int32
	reqServerError   atomic.Int32
	reqInternalError atomic.Int32
	bytesReceived    atomic.Uint64
	bytesSent        atomic.Uint64
)

var (
	agentStableConnectionInterval = 20 * time.Second
	agentReconnectBackoffMin      = time.Second
	agentReconnectBackoffMax      = 30 * time.Second
)

type Options struct {
	ManagementURL           string
	PublicID                string
	Name                    string
	Token                   string
	ManagementCAFile        string
	ManagementCAPEMBase64   string
	TLSCertFile             string
	TLSKeyFile              string
	AllowInsecureManagement bool
}

// Run is the main entry point to start the agent loop
func Run(opts Options) error {
	if err := validateOptions(opts); err != nil {
		return err
	}
	managementClient, err := managementHTTPClient(opts)
	if err != nil {
		return err
	}
	tunnelURL, err := managementTunnelURL(opts.ManagementURL)
	if err != nil {
		return err
	}
	tunnelClient, err := managementTunnelHTTPClient(managementClient)
	if err != nil {
		return err
	}

	go startStatsReporter(managementClient, opts.ManagementURL, opts.PublicID, opts.Token)

	backoff := agentReconnectBackoffMin
	for {
		log.Info().Str("tunnel_url", tunnelURL).Msg("Attempting to connect to management server...")

		connectedAt := time.Now()
		err := connectAndServe(tunnelClient, tunnelURL, opts.PublicID, opts.Name, opts.Token)
		if err != nil {
			log.Warn().Err(err).Msg("Disconnected")
		}
		if time.Since(connectedAt) >= agentStableConnectionInterval {
			backoff = agentReconnectBackoffMin
		}

		sleep := jitterAgentReconnectBackoff(backoff)
		log.Info().Dur("retry_in", sleep).Msg("Waiting before reconnect")
		time.Sleep(sleep)
		backoff = nextAgentReconnectBackoff(backoff)
	}
}

func nextAgentReconnectBackoff(current time.Duration) time.Duration {
	if current <= 0 {
		return agentReconnectBackoffMin
	}
	next := current * 2
	if next > agentReconnectBackoffMax {
		return agentReconnectBackoffMax
	}
	return next
}

func jitterAgentReconnectBackoff(base time.Duration) time.Duration {
	if base <= 0 {
		return 0
	}
	jitter := int64(float64(base) * 0.2)
	if jitter <= 0 {
		return base
	}
	delta := rand.Int63n(jitter*2+1) - jitter
	return base + time.Duration(delta)
}

func validateOptions(opts Options) error {
	if strings.TrimSpace(opts.ManagementURL) == "" {
		return fmt.Errorf("management URL is required")
	}
	parsed, err := url.Parse(strings.TrimSpace(opts.ManagementURL))
	if err != nil {
		return fmt.Errorf("invalid management URL: %w", err)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return fmt.Errorf("unsupported management URL scheme %q", parsed.Scheme)
	}
	if parsed.Scheme == "http" && !opts.AllowInsecureManagement {
		return fmt.Errorf("insecure HTTP management URL rejected; use https or set AGENT_ALLOW_INSECURE_MANAGEMENT=true")
	}
	hasClientCert := strings.TrimSpace(opts.TLSCertFile) != ""
	hasClientKey := strings.TrimSpace(opts.TLSKeyFile) != ""
	if hasClientCert != hasClientKey {
		return fmt.Errorf("AGENT_TLS_CERT_FILE and AGENT_TLS_KEY_FILE must be set together")
	}
	if parsed.Scheme != "https" && (hasClientCert || strings.TrimSpace(opts.ManagementCAFile) != "" || strings.TrimSpace(opts.ManagementCAPEMBase64) != "") {
		return fmt.Errorf("agent TLS files require an https management URL")
	}
	return nil
}

func managementTunnelURL(mgmtURL string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(mgmtURL))
	if err != nil {
		return "", fmt.Errorf("invalid management URL: %w", err)
	}
	switch parsed.Scheme {
	case "http", "https":
	default:
		return "", fmt.Errorf("unsupported management URL scheme %q", parsed.Scheme)
	}
	parsed.Path = strings.TrimRight(parsed.Path, "/") + tunnel.BootstrapPath
	parsed.RawQuery = ""
	parsed.Fragment = ""
	return parsed.String(), nil
}

func managementHTTPClient(opts Options) (*http.Client, error) {
	if err := validateOptions(opts); err != nil {
		return nil, err
	}
	if strings.TrimSpace(opts.ManagementCAFile) == "" &&
		strings.TrimSpace(opts.ManagementCAPEMBase64) == "" &&
		strings.TrimSpace(opts.TLSCertFile) == "" &&
		strings.TrimSpace(opts.TLSKeyFile) == "" {
		return http.DefaultClient, nil
	}

	baseTransport, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		return nil, fmt.Errorf("default transport is %T, want *http.Transport", http.DefaultTransport)
	}
	transport := baseTransport.Clone()
	tlsConfig := &tls.Config{MinVersion: tls.VersionTLS12}

	if caFile := strings.TrimSpace(opts.ManagementCAFile); caFile != "" {
		rootCAs, err := x509.SystemCertPool()
		if err != nil {
			rootCAs = x509.NewCertPool()
		}
		caPEM, err := os.ReadFile(caFile)
		if err != nil {
			return nil, fmt.Errorf("read management CA file: %w", err)
		}
		if !rootCAs.AppendCertsFromPEM(caPEM) {
			return nil, fmt.Errorf("no PEM certificates found in management CA file %q", caFile)
		}
		tlsConfig.RootCAs = rootCAs
	}

	if caBase64 := strings.TrimSpace(opts.ManagementCAPEMBase64); caBase64 != "" {
		rootCAs := tlsConfig.RootCAs
		if rootCAs == nil {
			var err error
			rootCAs, err = x509.SystemCertPool()
			if err != nil {
				rootCAs = x509.NewCertPool()
			}
		}
		caPEM, err := base64.StdEncoding.DecodeString(caBase64)
		if err != nil {
			caPEM, err = base64.RawStdEncoding.DecodeString(caBase64)
		}
		if err != nil {
			return nil, fmt.Errorf("decode MANAGEMENT_CA_PEM_BASE64: %w", err)
		}
		if !rootCAs.AppendCertsFromPEM(caPEM) {
			return nil, fmt.Errorf("MANAGEMENT_CA_PEM_BASE64 did not contain PEM certificates")
		}
		tlsConfig.RootCAs = rootCAs
	}

	if certFile := strings.TrimSpace(opts.TLSCertFile); certFile != "" {
		cert, err := tls.LoadX509KeyPair(certFile, strings.TrimSpace(opts.TLSKeyFile))
		if err != nil {
			return nil, fmt.Errorf("load agent TLS certificate: %w", err)
		}
		tlsConfig.Certificates = []tls.Certificate{cert}
	}

	transport.TLSClientConfig = tlsConfig
	return &http.Client{Transport: transport}, nil
}

func managementTunnelHTTPClient(base *http.Client) (*http.Client, error) {
	if base == nil {
		base = http.DefaultClient
	}
	var transport *http.Transport
	if base.Transport == nil {
		defaultTransport, ok := http.DefaultTransport.(*http.Transport)
		if !ok {
			return nil, fmt.Errorf("default transport is %T, want *http.Transport", http.DefaultTransport)
		}
		transport = defaultTransport.Clone()
	} else {
		baseTransport, ok := base.Transport.(*http.Transport)
		if !ok {
			return nil, fmt.Errorf("management tunnel transport is %T, want *http.Transport", base.Transport)
		}
		transport = baseTransport.Clone()
	}
	transport.ForceAttemptHTTP2 = false
	transport.TLSNextProto = map[string]func(string, *tls.Conn) http.RoundTripper{}
	protocols := new(http.Protocols)
	protocols.SetHTTP1(true)
	transport.Protocols = protocols
	if transport.TLSClientConfig == nil {
		transport.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12}
	} else {
		transport.TLSClientConfig = transport.TLSClientConfig.Clone()
	}
	transport.TLSClientConfig.NextProtos = []string{"http/1.1"}
	return &http.Client{
		Transport:     transport,
		CheckRedirect: base.CheckRedirect,
		Jar:           base.Jar,
	}, nil
}

func connectAndServe(client *http.Client, tunnelURL string, agentPublicID string, agentName string, agentToken string) error {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, tunnelURL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+agentToken)
	req.Header.Set("X-P2PStream-Agent-ID", agentPublicID)
	req.Header.Set("X-P2PStream-Agent-Name", agentName)
	req.Header.Set("Connection", "Upgrade")
	req.Header.Set("Upgrade", tunnel.UpgradeToken)
	req.Header.Set(tunnel.TunnelVersionHeader, strconv.Itoa(tunnel.ProtocolVersion))

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("failed to dial tunnel: %w", err)
	}
	if resp.StatusCode != http.StatusSwitchingProtocols {
		body := ""
		if resp.Body != nil {
			data, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
			body = strings.TrimSpace(string(data))
			resp.Body.Close()
		}
		if body != "" {
			return fmt.Errorf("agent tunnel upgrade failed: status %d: %s", resp.StatusCode, body)
		}
		return fmt.Errorf("agent tunnel upgrade failed: status %d", resp.StatusCode)
	}
	if got := resp.Header.Get("Upgrade"); !strings.EqualFold(got, tunnel.UpgradeToken) {
		resp.Body.Close()
		return fmt.Errorf("agent tunnel upgrade response header = %q", got)
	}
	rwc, ok := resp.Body.(io.ReadWriteCloser)
	if !ok {
		resp.Body.Close()
		return fmt.Errorf("agent tunnel response body is %T, want io.ReadWriteCloser", resp.Body)
	}
	session, err := yamux.Client(rwc, tunnel.DefaultYamuxConfig(nil))
	if err != nil {
		rwc.Close()
		return fmt.Errorf("failed to initialize tunnel session: %w", err)
	}
	defer session.Close()

	log.Info().Msg("Connected tunnel successfully")

	go func() {
		<-ctx.Done()
		_ = session.Close()
	}()

	return serveTunnelSession(ctx, session)
}

func serveTunnelSession(ctx context.Context, session *yamux.Session) error {
	for {
		stream, err := session.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return fmt.Errorf("accept tunnel stream: %w", err)
		}
		go handleTunnelStream(ctx, stream)
	}
}

func handleTunnelStream(ctx context.Context, stream net.Conn) {
	defer stream.Close()

	openReq, err := tunnel.ReadOpenRequest(stream)
	if err != nil {
		reqInternalError.Add(1)
		_ = tunnel.WriteOpenResponse(stream, tunnel.OpenResponse{
			OK:        false,
			ErrorKind: "invalid_open_request",
			Error:     err.Error(),
		})
		return
	}

	activeRequests.Add(1)
	defer activeRequests.Add(-1)

	dialer := net.Dialer{}
	upstream, err := dialer.DialContext(ctx, openReq.Network, openReq.Address)
	if err != nil {
		kind := tunnelDialErrorKind(err)
		reqInternalError.Add(1)
		_ = tunnel.WriteOpenResponse(stream, tunnel.OpenResponse{
			OK:        false,
			ErrorKind: kind,
			Error:     err.Error(),
		})
		return
	}
	defer upstream.Close()

	if err := tunnel.WriteOpenResponse(stream, tunnel.OpenResponse{OK: true}); err != nil {
		reqInternalError.Add(1)
		return
	}

	if err := relayTunnelStream(ctx, stream, upstream); err != nil && ctx.Err() == nil {
		reqInternalError.Add(1)
		log.Debug().Err(err).Str("request_id", openReq.RequestID).Msg("Tunnel stream relay failed")
		return
	}
	reqSuccess.Add(1)
}

func tunnelDialErrorKind(err error) string {
	if isTimeoutError(err) {
		return "dial_timeout"
	}
	return "dial_failed"
}

func relayTunnelStream(ctx context.Context, stream net.Conn, upstream net.Conn) error {
	errCh := make(chan error, 2)
	go func() {
		n, err := io.Copy(upstream, stream)
		bytesReceived.Add(uint64(n))
		errCh <- err
	}()
	go func() {
		n, err := io.Copy(stream, upstream)
		bytesSent.Add(uint64(n))
		errCh <- err
	}()
	select {
	case err := <-errCh:
		_ = stream.Close()
		_ = upstream.Close()
		if errors.Is(err, net.ErrClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
		_ = stream.Close()
		_ = upstream.Close()
		return ctx.Err()
	}
}

func isTimeoutError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var netErr net.Error
	return errors.As(err, &netErr) && netErr.Timeout()
}

func startStatsReporter(httpClient *http.Client, mgmtURL string, agentPublicID string, agentToken string) {
	client := p2pstreamv1connect.NewAgentManagementServiceClient(
		httpClient,
		mgmtURL,
		connect.WithGRPC(), // We can use gRPC or Connect protocol, let's use default Connect or GRPC
	)
	cpuSampler := sysmetrics.NewProcessCPUSampler()

	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	for range ticker.C {
		var mem runtime.MemStats
		runtime.ReadMemStats(&mem)
		cpuPercent := 0.0
		if cpuSampler != nil {
			if sampled, ok, err := cpuSampler.Sample(); err == nil && ok {
				cpuPercent = sampled
			}
		}

		req := &p2pstreamv1.AgentStatsRequest{
			MemorySysMb:      int64(mem.Alloc / 1024 / 1024),
			NumGoroutine:     int64(runtime.NumGoroutine()),
			CpuPercent:       cpuPercent,
			ActiveRequests:   activeRequests.Load(),
			ReqSuccess:       int64(reqSuccess.Swap(0)),
			ReqClientError:   int64(reqClientError.Swap(0)),
			ReqServerError:   int64(reqServerError.Swap(0)),
			ReqInternalError: int64(reqInternalError.Swap(0)),
			BytesReceived:    bytesReceived.Swap(0),
			BytesSent:        bytesSent.Swap(0),
			AgentPublicId:    agentPublicID,
		}

		connectReq := connect.NewRequest(req)
		connectReq.Header().Set("Authorization", "Bearer "+agentToken)

		_, err := client.ReportStats(context.Background(), connectReq)
		if err != nil {
			log.Debug().Err(err).Msg("Failed to report stats")
		}
	}
}
