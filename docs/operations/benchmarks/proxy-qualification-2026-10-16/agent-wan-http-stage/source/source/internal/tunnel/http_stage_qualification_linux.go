//go:build linux && p2pstream_transport_ceiling && p2pstream_http_stage

package tunnel

// Temporary, test-build-only HTTP/1 stage diagnostic. This file deliberately
// builds on the immutable transport-ceiling helper while leaving production
// and the accepted bare transport fixture unchanged.

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"time"
)

const (
	HTTPStageSchemaVersion     = 1
	HTTPStageExpectedPhases    = TransportCeilingExpectedPhases
	HTTPStageResponsePumpBytes = TransportCeilingRelayBufferBytes
	HTTPStageConsumerReadBytes = TransportCeilingDownloadReadBytes
	HTTPStageUploadReadBytes   = TransportCeilingDownloadReadBytes
	HTTPStageOriginWriteBytes  = TransportCeilingDownloadWriteBytes
	HTTPStageUploadSourceBytes = TransportCeilingUploadBufferBytes
	HTTPStageFixedCapacity     = 16
	// defaultAgentStreamCapacityConfig(16) reserves one control slot and one
	// quarter of the remaining public slots for one-shot work: 15 - 4 = 11.
	HTTPStagePooledCapacity  = 11
	HTTPStageIdleTimeout     = 30 * time.Second
	HTTPStageResponseTimeout = 90 * time.Second
	HTTPStageVariant         = "aggregate-http1"
	HTTPStageDownloadSHA256  = "e1a511c229b7fdd398a94b74c98b7f16411fa0ec93302744f7e5d959275a907a"
	HTTPStageUploadSHA256    = "a6d72ac7690f53be6ae46ba88506bd97302a093f7108472bd9efc3cefda06484"
)

type HTTPStageConfiguration struct {
	TLSYamux                         TransportCeilingConfiguration `json:"tls_yamux_configuration"`
	FixedCapacityStreams             int                           `json:"fixed_capacity_streams"`
	HTTPIdleTimeoutNanoseconds       int64                         `json:"http_idle_timeout_nanoseconds"`
	ResponseHeaderTimeoutNanoseconds int64                         `json:"response_header_timeout_nanoseconds"`
	MaxIdleConnections               int                           `json:"max_idle_connections"`
	MaxIdleConnectionsPerHost        int                           `json:"max_idle_connections_per_host"`
	MaxConnectionsPerHost            int                           `json:"max_connections_per_host"`
	DisableKeepAlives                bool                          `json:"disable_keep_alives"`
	ResponsePumpBufferBytes          int                           `json:"response_pump_buffer_bytes"`
	FinalConsumerReadBytes           int                           `json:"final_consumer_read_bytes"`
	OriginWriteBytes                 int                           `json:"origin_write_bytes"`
	OriginFlush                      bool                          `json:"origin_flush"`
	UploadOriginIOCopySHA256         bool                          `json:"upload_origin_io_copy_sha256"`
	ConditioningClosesWholePool      bool                          `json:"conditioning_closes_whole_pool"`
}

func httpStageConfiguration(role string) HTTPStageConfiguration {
	return HTTPStageConfiguration{
		TLSYamux:                         transportCeilingConfiguration(role, TransportCeilingVariantYamux),
		FixedCapacityStreams:             HTTPStageFixedCapacity,
		HTTPIdleTimeoutNanoseconds:       int64(HTTPStageIdleTimeout),
		ResponseHeaderTimeoutNanoseconds: int64(HTTPStageResponseTimeout),
		MaxIdleConnections:               HTTPStagePooledCapacity,
		MaxIdleConnectionsPerHost:        HTTPStagePooledCapacity,
		MaxConnectionsPerHost:            0,
		DisableKeepAlives:                false,
		ResponsePumpBufferBytes:          HTTPStageResponsePumpBytes,
		FinalConsumerReadBytes:           HTTPStageConsumerReadBytes,
		OriginWriteBytes:                 HTTPStageOriginWriteBytes,
		OriginFlush:                      false,
		UploadOriginIOCopySHA256:         true,
		ConditioningClosesWholePool:      true,
	}
}

var httpStageDownloadBlock = bytes.Repeat([]byte("p2pstream-aggregate-growth-payload-"), 1024)

type HTTPStageConnectionTrace struct {
	ConnectionIdentity       string `json:"connection_identity"`
	OriginConnectionIdentity string `json:"origin_connection_identity"`
	Reused                   bool   `json:"reused"`
	WasIdle                  bool   `json:"was_idle"`
	IdleNanoseconds          int64  `json:"idle_nanoseconds"`
	PutIdle                  bool   `json:"put_idle"`
	PutIdleError             string `json:"put_idle_error"`
	DialOpened               bool   `json:"dial_opened"`
}

type HTTPStageOwnershipSnapshot struct {
	CapacityTotalInUse         int       `json:"capacity_total_in_use"`
	CapacityPooledInUse        int       `json:"capacity_pooled_in_use"`
	CapacityOpening            int       `json:"capacity_opening"`
	CapacityLive               int       `json:"capacity_live"`
	CapacityClosing            int       `json:"capacity_closing"`
	CapacityRegisteredSessions int       `json:"capacity_registered_sessions"`
	TransportEntries           int       `json:"transport_entries"`
	TransportDraining          int       `json:"transport_draining"`
	LogicalOpens               uint64    `json:"logical_opens"`
	LogicalCloses              uint64    `json:"logical_closes"`
	ConnectionReuses           uint64    `json:"connection_reuses"`
	OriginActiveRequests       int       `json:"origin_active_requests"`
	LaneStreams                [4]int    `json:"lane_streams"`
	LaneReceiveLimits          [4]uint32 `json:"lane_receive_limits"`
}

type HTTPStagePhaseResult struct {
	SchemaVersion           int                        `json:"schema_version"`
	PhaseIndex              int                        `json:"phase_index"`
	PhaseName               string                     `json:"phase_name"`
	Lane                    int                        `json:"lane"`
	Direction               string                     `json:"direction"`
	Stage                   string                     `json:"stage"`
	PlannedReuseKind        string                     `json:"planned_reuse_kind"`
	ConfiguredPayloadBytes  int64                      `json:"configured_payload_bytes"`
	ReceivedPayloadBytes    int64                      `json:"received_payload_bytes"`
	ExpectedSHA256          string                     `json:"expected_sha256"`
	ReceivedSHA256          string                     `json:"received_sha256"`
	HTTPStatus              int                        `json:"http_status"`
	OriginHTTPMajor         int                        `json:"origin_http_major"`
	ResponseContentLength   int64                      `json:"response_content_length"`
	PhaseWallNanoseconds    int64                      `json:"phase_wall_nanoseconds"`
	UsefulBytesPerSecond    float64                    `json:"useful_bytes_per_second"`
	Connection              HTTPStageConnectionTrace   `json:"connection"`
	OwnershipBefore         HTTPStageOwnershipSnapshot `json:"ownership_before"`
	OwnershipAfter          HTTPStageOwnershipSnapshot `json:"ownership_after"`
	ConditioningPoolClosed  bool                       `json:"conditioning_pool_closed"`
	ConditioningQuiescent   bool                       `json:"conditioning_quiescent"`
	ResponsePumpBufferBytes int                        `json:"response_pump_buffer_bytes"`
	FinalConsumerReadBytes  int                        `json:"final_consumer_read_bytes"`
	Error                   string                     `json:"error"`
}

type HTTPStageReport struct {
	SchemaVersion          int                            `json:"schema_version"`
	ProcessRole            string                         `json:"process_role"`
	Variant                string                         `json:"variant"`
	SourceRef              string                         `json:"source_ref"`
	Configuration          HTTPStageConfiguration         `json:"configuration"`
	Ready                  bool                           `json:"ready"`
	GracefulShutdown       bool                           `json:"graceful_shutdown"`
	InvalidReason          string                         `json:"invalid_reason"`
	ExpectedPhases         int                            `json:"expected_phases"`
	CompletedPhases        int                            `json:"completed_phases"`
	PhysicalSocketsInitial []TransportCeilingSocket       `json:"physical_sockets_initial"`
	PhysicalSocketsCurrent []TransportCeilingSocket       `json:"physical_sockets_current"`
	SameSocketIdentities   bool                           `json:"same_socket_identities"`
	ReconnectCount         int                            `json:"reconnect_count"`
	Results                []HTTPStagePhaseResult         `json:"results"`
	Ownership              HTTPStageOwnershipSnapshot     `json:"ownership"`
	PoolLedger             TransportCeilingLedgerSnapshot `json:"pool_ledger"`
	WorkingLedger          TransportCeilingLedgerSnapshot `json:"working_ledger"`
	Errors                 []string                       `json:"errors"`
}

type HTTPStageEndpoint struct {
	mu       sync.Mutex
	role     string
	source   string
	pool     *TransportCeilingLedger
	working  *TransportCeilingLedger
	initial  []TransportCeilingSocket
	current  []TransportCeilingSocket
	results  []HTTPStagePhaseResult
	invalid  error
	transfer func(context.Context, int) (HTTPStagePhaseResult, error)
	shutdown func()
	owner    func() HTTPStageOwnershipSnapshot
	config   HTTPStageConfiguration
	live     bool
}

func NewHTTPStageEndpoint(role, source string, pool, working *TransportCeilingLedger) (*HTTPStageEndpoint, error) {
	if role != "server" && role != "agent" {
		return nil, fmt.Errorf("invalid HTTP stage role %q", role)
	}
	if source == "" || pool == nil || working == nil {
		return nil, errors.New("HTTP stage source and ledgers are required")
	}
	return &HTTPStageEndpoint{
		role: role, source: source, pool: pool, working: working,
		config: httpStageConfiguration(role),
	}, nil
}

func (e *HTTPStageEndpoint) SetCallbacks(transfer func(context.Context, int) (HTTPStagePhaseResult, error), shutdown func(), owner func() HTTPStageOwnershipSnapshot) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.transfer, e.shutdown, e.owner = transfer, shutdown, owner
}

func (e *HTTPStageEndpoint) SetReady(sockets []TransportCeilingSocket) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.initial = append([]TransportCeilingSocket(nil), sockets...)
	e.current = append([]TransportCeilingSocket(nil), sockets...)
}

func (e *HTTPStageEndpoint) SetLive(sockets []TransportCeilingSocket) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if len(e.results) != HTTPStageExpectedPhases {
		return fmt.Errorf("HTTP stage live result count %d", len(e.results))
	}
	if len(sockets) != TransportCeilingPhysicalLanes {
		return fmt.Errorf("HTTP stage live physical socket count %d", len(sockets))
	}
	for _, socket := range sockets {
		if !socket.Connected || !socket.TCPInfoCaptured || socket.TCPInfoError != "" || socket.TCPState != TransportCeilingTCPEstablished {
			return fmt.Errorf("HTTP stage live lane %d is not established", socket.Lane)
		}
	}
	if !sameHTTPStageSockets(e.initial, sockets) {
		return errors.New("HTTP stage live physical socket identities changed")
	}
	e.current = append([]TransportCeilingSocket(nil), sockets...)
	e.live = true
	return nil
}

// Terminal is called only after the retained post16 snapshot has been
// published, the whole HTTP transport pool has been closed and quiesced, and
// every physical session has been closed and joined.
func (e *HTTPStageEndpoint) Terminal(sockets []TransportCeilingSocket) HTTPStageReport {
	e.mu.Lock()
	e.current = append([]TransportCeilingSocket(nil), sockets...)
	e.mu.Unlock()
	return e.snapshot(true)
}

func (e *HTTPStageEndpoint) RecordResult(result HTTPStagePhaseResult) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if result.PhaseIndex != len(e.results) {
		return fmt.Errorf("HTTP stage result index %d, want %d", result.PhaseIndex, len(e.results))
	}
	e.results = append(e.results, result)
	return nil
}

func (e *HTTPStageEndpoint) MarkInvalid(err error) {
	if err == nil {
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.invalid == nil {
		e.invalid = err
	}
}

func (e *HTTPStageEndpoint) IsLive() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.live
}

func (e *HTTPStageEndpoint) snapshot(final bool) HTTPStageReport {
	e.mu.Lock()
	defer e.mu.Unlock()
	report := HTTPStageReport{
		SchemaVersion: HTTPStageSchemaVersion, ProcessRole: e.role, Variant: HTTPStageVariant,
		SourceRef: e.source, Configuration: e.config,
		Ready:            !final && len(e.initial) == TransportCeilingPhysicalLanes,
		GracefulShutdown: final, ExpectedPhases: HTTPStageExpectedPhases,
		CompletedPhases: len(e.results), PhysicalSocketsInitial: append([]TransportCeilingSocket(nil), e.initial...),
		PhysicalSocketsCurrent: append([]TransportCeilingSocket(nil), e.current...),
		Results:                append([]HTTPStagePhaseResult(nil), e.results...), PoolLedger: e.pool.Snapshot(final),
		WorkingLedger: e.working.Snapshot(final), SameSocketIdentities: sameHTTPStageSockets(e.initial, e.current),
		Errors: []string{},
	}
	if e.owner != nil {
		report.Ownership = e.owner()
	}
	if e.invalid != nil {
		report.InvalidReason = e.invalid.Error()
		report.Errors = []string{e.invalid.Error()}
	}
	_ = CaptureTransportCeilingProcessResources(&report.WorkingLedger)
	return report
}

func (e *HTTPStageEndpoint) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/http-stage/readiness":
		writeHTTPStageJSON(w, e.snapshot(false))
	case r.Method == http.MethodGet && r.URL.Path == "/http-stage/live-post16":
		e.mu.Lock()
		live := e.live
		e.mu.Unlock()
		if !live {
			http.Error(w, "HTTP stage live state unavailable", http.StatusConflict)
			return
		}
		writeHTTPStageJSON(w, e.snapshot(false))
	case e.role == "server" && r.Method == http.MethodPost && r.URL.Path == "/http-stage/transfer":
		var request struct {
			SchemaVersion int `json:"schema_version"`
			PhaseIndex    int `json:"phase_index"`
		}
		decoder := json.NewDecoder(io.LimitReader(r.Body, 4096))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&request); err != nil || request.SchemaVersion != HTTPStageSchemaVersion || !httpStageJSONEnds(decoder) {
			http.Error(w, "invalid HTTP stage transfer", http.StatusBadRequest)
			return
		}
		e.mu.Lock()
		transfer := e.transfer
		e.mu.Unlock()
		if transfer == nil {
			http.Error(w, "HTTP stage transfer unavailable", http.StatusServiceUnavailable)
			return
		}
		result, err := transfer(r.Context(), request.PhaseIndex)
		if err != nil {
			e.MarkInvalid(err)
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeHTTPStageJSON(w, result)
	case e.role == "server" && r.Method == http.MethodPost && r.URL.Path == "/http-stage/shutdown":
		var request struct {
			SchemaVersion int `json:"schema_version"`
		}
		decoder := json.NewDecoder(io.LimitReader(r.Body, 4096))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&request); err != nil || request.SchemaVersion != HTTPStageSchemaVersion || !httpStageJSONEnds(decoder) {
			http.Error(w, "invalid HTTP stage shutdown", http.StatusBadRequest)
			return
		}
		e.mu.Lock()
		shutdown := e.shutdown
		live := e.live
		e.mu.Unlock()
		if shutdown == nil || !live {
			http.Error(w, "HTTP stage shutdown unavailable", http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusNoContent)
		go shutdown()
	default:
		http.NotFound(w, r)
	}
}

func httpStageJSONEnds(decoder *json.Decoder) bool {
	var extra any
	return decoder.Decode(&extra) == io.EOF
}

func writeHTTPStageJSON(w http.ResponseWriter, value any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(value)
}

func WriteHTTPStageReport(path string, report HTTPStageReport) error {
	if path == "" {
		return errors.New("HTTP stage report path is required")
	}
	encoded, err := json.Marshal(report)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	temporary := path + ".tmp"
	if err := os.WriteFile(temporary, append(encoded, '\n'), 0600); err != nil {
		return err
	}
	return os.Rename(temporary, path)
}

// ReserveHTTPStageWorkingEnvelope prepays a conservative fixed-capacity
// fixture allowance/promise. It is not a hard bound on TLS buffers, the Go
// heap, RSS, or kernel memory. It is independent of the production fixed-count
// stream manager, whose non-adaptive resource controller deliberately has no
// host sampler and therefore does not publish byte ownership.
func ReserveHTTPStageWorkingEnvelope(ledger *TransportCeilingLedger) ([]func(), error) {
	if ledger == nil {
		return nil, errors.New("HTTP stage working ledger is required")
	}
	reservations := []struct {
		category string
		bytes    int64
	}{
		{"session_metadata", int64(TransportCeilingPhysicalLanes) * AggregateSessionMetadataBytes},
		{"stream_metadata", 16 * AdaptiveServerStreamOverheadBytes},
		{"relay_buffers", 16 * 2 * TransportCeilingRelayBufferBytes},
		{"frame_buffers", 16 * int64(HTTPStageOriginWriteBytes+HTTPStageUploadSourceBytes)},
		{"tls_app_buffers", int64(TransportCeilingPhysicalLanes) * 64 << 10},
	}
	releases := make([]func(), 0, len(reservations))
	for _, reservation := range reservations {
		release, ok := ledger.Reserve(reservation.category, reservation.bytes)
		if !ok {
			for index := len(releases) - 1; index >= 0; index-- {
				releases[index]()
			}
			return nil, fmt.Errorf("reserve HTTP stage %s working envelope", reservation.category)
		}
		releases = append(releases, release)
	}
	return releases, nil
}

func sameHTTPStageSockets(left, right []TransportCeilingSocket) bool {
	if len(left) != len(right) {
		return false
	}
	a, b := append([]TransportCeilingSocket(nil), left...), append([]TransportCeilingSocket(nil), right...)
	slices.SortFunc(a, func(x, y TransportCeilingSocket) int { return x.Lane - y.Lane })
	slices.SortFunc(b, func(x, y TransportCeilingSocket) int { return x.Lane - y.Lane })
	for index := range a {
		if a[index].Lane != b[index].Lane || a[index].LaneRole != b[index].LaneRole || a[index].ConnectionIdentity != b[index].ConnectionIdentity || a[index].LocalAddress != b[index].LocalAddress || a[index].RemoteAddress != b[index].RemoteAddress || a[index].TLSVersion != b[index].TLSVersion || a[index].TLSCipherSuite != b[index].TLSCipherSuite {
			return false
		}
	}
	return true
}

type httpStageReaderOnly struct{ io.Reader }
type httpStageWriterOnly struct{ io.Writer }

// ConsumeHTTPStageResponse keeps the final 8 KiB consumer behind an explicit
// 32 KiB pump. The interface wrappers prevent io.CopyBuffer from bypassing the
// supplied buffer via WriterTo or ReaderFrom. Short reads are preserved.
func ConsumeHTTPStageResponse(ctx context.Context, body io.ReadCloser, release func()) (int64, string, []byte, error) {
	if body == nil {
		return 0, "", nil, errors.New("HTTP stage response body is nil")
	}
	reader, writer := io.Pipe()
	var closeOnce sync.Once
	var closeErr error
	closeBody := func() {
		closeOnce.Do(func() { closeErr = body.Close() })
	}
	pumpDone := make(chan error, 1)
	go func() {
		buffer := make([]byte, HTTPStageResponsePumpBytes)
		_, copyErr := io.CopyBuffer(httpStageWriterOnly{writer}, httpStageReaderOnly{body}, buffer)
		closeBody()
		_ = writer.CloseWithError(copyErr)
		pumpDone <- copyErr
	}()
	consumerDone := make(chan struct{})
	watchDone := make(chan struct{})
	go func() {
		defer close(watchDone)
		select {
		case <-ctx.Done():
			closeBody()
			_ = writer.CloseWithError(ctx.Err())
			_ = reader.CloseWithError(ctx.Err())
		case <-consumerDone:
		}
	}()
	consumerBuffer := make([]byte, HTTPStageConsumerReadBytes)
	digest := sha256.New()
	var prefix httpStagePrefix
	n, readErr := copyHTTPStageShortReads(io.MultiWriter(digest, &prefix), reader, consumerBuffer)
	_ = reader.Close()
	pumpErr := <-pumpDone
	close(consumerDone)
	<-watchDone
	if release != nil {
		release()
	}
	if readErr != nil {
		return n, "", prefix.Bytes(), readErr
	}
	if pumpErr != nil {
		return n, "", prefix.Bytes(), pumpErr
	}
	if closeErr != nil {
		return n, "", prefix.Bytes(), fmt.Errorf("close HTTP stage response body: %w", closeErr)
	}
	return n, hex.EncodeToString(digest.Sum(nil)), prefix.Bytes(), nil
}

type httpStagePrefix struct {
	bytes [sha256.Size]byte
	n     int
}

func (p *httpStagePrefix) Write(data []byte) (int, error) {
	if p.n < len(p.bytes) {
		p.n += copy(p.bytes[p.n:], data)
	}
	return len(data), nil
}

func (p *httpStagePrefix) Bytes() []byte {
	return append([]byte(nil), p.bytes[:p.n]...)
}

func copyHTTPStageShortReads(destination io.Writer, source io.Reader, buffer []byte) (int64, error) {
	var total int64
	for {
		n, err := source.Read(buffer)
		if n > 0 {
			total += int64(n)
			if _, writeErr := destination.Write(buffer[:n]); writeErr != nil {
				return total, writeErr
			}
		}
		if err == io.EOF {
			return total, nil
		}
		if err != nil {
			return total, err
		}
		if n == 0 {
			return total, io.ErrNoProgress
		}
	}
}

type HTTPStagePayloadReader struct {
	remaining int64
	offset    int64
	maxChunk  int
	zero      bool
}

func NewHTTPStageDownloadReader(bytes int64, maxChunk int) *HTTPStagePayloadReader {
	return &HTTPStagePayloadReader{remaining: bytes, maxChunk: maxChunk}
}

func NewHTTPStageUploadReader(bytes int64, maxChunk int) *HTTPStagePayloadReader {
	return &HTTPStagePayloadReader{remaining: bytes, maxChunk: maxChunk, zero: true}
}

func (r *HTTPStagePayloadReader) Read(p []byte) (int, error) {
	if r.remaining == 0 {
		return 0, io.EOF
	}
	limit := len(p)
	if r.maxChunk > 0 && limit > r.maxChunk {
		limit = r.maxChunk
	}
	if int64(limit) > r.remaining {
		limit = int(r.remaining)
	}
	if r.zero {
		clear(p[:limit])
	} else {
		for index := 0; index < limit; index++ {
			p[index] = httpStageDownloadBlock[(r.offset+int64(index))%int64(len(httpStageDownloadBlock))]
		}
	}
	r.offset += int64(limit)
	r.remaining -= int64(limit)
	return limit, nil
}

func WriteHTTPStagePayload(w io.Writer, bytes int64) (string, error) {
	remaining := bytes
	for remaining > 0 {
		n := min(remaining, int64(len(httpStageDownloadBlock)))
		chunk := httpStageDownloadBlock[:n]
		written, err := w.Write(chunk)
		if written > 0 {
			remaining -= int64(written)
		}
		if err != nil {
			return "", err
		}
		if written != len(chunk) {
			return "", io.ErrShortWrite
		}
	}
	// The block algorithm and exact length have an independently pinned digest.
	// Do not add origin-side hashing to the timed path; the receiver hashes the
	// bytes that actually crossed the production transport.
	return HTTPStageDownloadSHA256, nil
}

func HTTPStageSocketIdentity(conn net.Conn) string {
	return fmt.Sprintf("%p", conn)
}
