package agent

import (
	"bytes"
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net/http"
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

// Run is the main entry point to start the agent loop
func Run(mgmtURL string, agentToken string) {
	go startStatsReporter(mgmtURL, agentToken)

	wsURL := strings.Replace(mgmtURL, "http", "ws", 1) + "/ws"

	for {
		log.Info().Str("ws_url", wsURL).Msg("Attempting to connect to management server...")

		err := connectAndServe(wsURL, agentToken)
		if err != nil {
			log.Warn().Err(err).Msg("Disconnected")
		}

		time.Sleep(2 * time.Second)
	}
}

func connectAndServe(wsURL string, agentToken string) error {
	ctx := context.Background()
	var opts *websocket.DialOptions
	if agentToken != "" {
		opts = &websocket.DialOptions{
			HTTPHeader: http.Header{
				"Authorization": []string{"Bearer " + agentToken},
			},
		}
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

func startStatsReporter(mgmtURL string, agentToken string) {
	client := p2pstreamv1connect.NewAgentManagementServiceClient(
		http.DefaultClient,
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
		}

		connectReq := connect.NewRequest(req)
		if agentToken != "" {
			connectReq.Header().Set("Authorization", "Bearer "+agentToken)
		}

		_, err := client.ReportStats(context.Background(), connectReq)
		if err != nil {
			log.Debug().Err(err).Msg("Failed to report stats")
		}
	}
}
