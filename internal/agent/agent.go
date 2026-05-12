package agent

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"connectrpc.com/connect"
	"github.com/coder/websocket"
	"github.com/google/uuid"
	"github.com/rs/zerolog/log"

	p2pstreamv1 "p2pstream/gen/proto/p2pstream/v1"
	"p2pstream/gen/proto/p2pstream/v1/p2pstreamv1connect"
	"p2pstream/httpmsg"
	"p2pstream/msg"
)

var (
	incomingRequests sync.Map // map[uuid.UUID]chan *msg.Request
	writeCh          chan *msg.Request

	activeRequests   atomic.Int32
	reqSuccess       atomic.Int32
	reqClientError   atomic.Int32
	reqServerError   atomic.Int32
	reqInternalError atomic.Int32
	bytesReceived    atomic.Uint64
	bytesSent        atomic.Uint64

	defaultForwardClient       = &http.Client{}
	tlsSkipVerifyForwardClient = &http.Client{Transport: tlsSkipVerifyTransport()}
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
	wsURL, err := managementWebSocketURL(opts.ManagementURL)
	if err != nil {
		return err
	}

	go startStatsReporter(managementClient, opts.ManagementURL, opts.PublicID, opts.Token)

	for {
		log.Info().Str("ws_url", wsURL).Msg("Attempting to connect to management server...")

		err := connectAndServe(managementClient, wsURL, opts.PublicID, opts.Name, opts.Token)
		if err != nil {
			log.Warn().Err(err).Msg("Disconnected")
		}

		time.Sleep(2 * time.Second)
	}
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

func managementWebSocketURL(mgmtURL string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(mgmtURL))
	if err != nil {
		return "", fmt.Errorf("invalid management URL: %w", err)
	}
	switch parsed.Scheme {
	case "http":
		parsed.Scheme = "ws"
	case "https":
		parsed.Scheme = "wss"
	default:
		return "", fmt.Errorf("unsupported management URL scheme %q", parsed.Scheme)
	}
	parsed.Path = strings.TrimRight(parsed.Path, "/") + "/ws"
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

func connectAndServe(client *http.Client, wsURL string, agentPublicID string, agentName string, agentToken string) error {
	ctx := context.Background()
	opts := &websocket.DialOptions{
		HTTPClient: client,
		HTTPHeader: http.Header{
			"Authorization":          []string{"Bearer " + agentToken},
			"X-P2PStream-Agent-ID":   []string{agentPublicID},
			"X-P2PStream-Agent-Name": []string{agentName},
		},
	}

	c, _, err := websocket.Dial(ctx, wsURL, opts)
	if err != nil {
		return fmt.Errorf("failed to dial: %w", err)
	}
	defer c.Close(websocket.StatusInternalError, "agent shutting down")

	c.SetReadLimit(128 * 1024)

	log.Info().Msg("Connected successfully!")

	writeCh = make(chan *msg.Request, 100)

	go func() {
		for req := range writeCh {
			cw, err := c.Writer(ctx, websocket.MessageBinary)
			if err != nil {
				log.Error().Err(err).Msg("ws write error")
				return
			}
			n, err := req.WriteTo(cw)
			if err != nil {
				log.Error().Err(err).Msg("msg WriteTo error")
				cw.Close()
				return
			}
			bytesSent.Add(uint64(n))
			cw.Close()
		}
	}()

	for {
		_, reader, err := c.Reader(ctx)
		if err != nil {
			return fmt.Errorf("failed to get reader: %w", err)
		}

		b, err := io.ReadAll(reader)
		if err != nil {
			return fmt.Errorf("failed to read: %w", err)
		}

		bytesReceived.Add(uint64(len(b)))

		m, err := msg.ParseRequest(bytes.NewReader(b))
		if err != nil {
			log.Error().Err(err).Msg("Failed to parse request")
			continue
		}

		if m.Type == msg.RequestTypeHeader || m.Type == msg.RequestTypeHeaderAndBody {
			reqCh := make(chan *msg.Request, 100)
			incomingRequests.Store(m.ID, reqCh)
			reqCh <- m

			go handleRequest(m.ID, reqCh)
		} else {
			if ch, ok := incomingRequests.Load(m.ID); ok {
				ch.(chan *msg.Request) <- m
			} else {
				log.Warn().Str("req_id", m.ID.String()).Msg("Received chunk for unknown request")
			}
		}
	}
}

func handleRequest(id uuid.UUID, reqCh chan *msg.Request) {
	activeRequests.Add(1)
	defer activeRequests.Add(-1)
	defer incomingRequests.Delete(id)

	stream := &httpmsg.ChannelStream{Ch: reqCh}
	firstMsg, err := stream.Next()
	if err != nil {
		log.Error().Err(err).Str("req_id", id.String()).Msg("Failed to get first chunk")
		reqInternalError.Add(1)
		return
	}
	tlsSkipVerify := strings.EqualFold(firstMsg.Headers[httpmsg.MetadataTLSSkipVerify], "true")

	req, err := httpmsg.DecodeRequest(firstMsg, stream)
	if err != nil {
		log.Error().Err(err).Str("req_id", id.String()).Msg("Failed to decode request")
		reqInternalError.Add(1)
		return
	}

	req.RequestURI = ""

	log.Info().Str("req_id", id.String()).Str("method", req.Method).Str("url", req.URL.String()).Msg("Forwarding request")

	client := forwardHTTPClient(tlsSkipVerify)
	resp, err := client.Do(req)
	if err != nil {
		log.Error().Err(err).Str("req_id", id.String()).Msg("Failed to execute request")
		reqInternalError.Add(1)

		resp = &http.Response{
			StatusCode:    http.StatusBadGateway,
			Status:        http.StatusText(http.StatusBadGateway),
			Header:        make(http.Header),
			Body:          io.NopCloser(bytes.NewReader([]byte(err.Error()))),
			ContentLength: int64(len(err.Error())),
		}
	} else {
		if resp.StatusCode >= 200 && resp.StatusCode < 400 {
			reqSuccess.Add(1)
		} else if resp.StatusCode >= 400 && resp.StatusCode < 500 {
			reqClientError.Add(1)
		} else {
			reqServerError.Add(1)
		}
	}

	enc := httpmsg.NewResponseEncoder(id, resp)
	for {
		m, err := enc.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			log.Error().Err(err).Str("req_id", id.String()).Msg("Failed to encode response")
			return
		}
		writeCh <- m
	}

	log.Info().Str("req_id", id.String()).Msg("Finished successfully")
}

func forwardHTTPClient(tlsSkipVerify bool) *http.Client {
	if tlsSkipVerify {
		return tlsSkipVerifyForwardClient
	}
	return defaultForwardClient
}

func tlsSkipVerifyTransport() http.RoundTripper {
	base, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		return http.DefaultTransport
	}
	transport := base.Clone()
	if transport.TLSClientConfig == nil {
		transport.TLSClientConfig = &tls.Config{}
	} else {
		transport.TLSClientConfig = transport.TLSClientConfig.Clone()
	}
	transport.TLSClientConfig.InsecureSkipVerify = true
	return transport
}

func startStatsReporter(httpClient *http.Client, mgmtURL string, agentPublicID string, agentToken string) {
	client := p2pstreamv1connect.NewAgentManagementServiceClient(
		httpClient,
		mgmtURL,
		connect.WithGRPC(), // We can use gRPC or Connect protocol, let's use default Connect or GRPC
	)

	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	for range ticker.C {
		var mem runtime.MemStats
		runtime.ReadMemStats(&mem)

		req := &p2pstreamv1.AgentStatsRequest{
			MemorySysMb:      int64(mem.Alloc / 1024 / 1024),
			NumGoroutine:     int64(runtime.NumGoroutine()),
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
