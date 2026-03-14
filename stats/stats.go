package stats

import "time"

type AgentStats struct {
	Timestamp      time.Time `json:"timestamp"`
	NumGoroutine   int       `json:"num_goroutine"`
	AllocAllocated uint64    `json:"memory_allocated_mb"`
	ActiveRequests int32     `json:"active_requests"`

	// Traffic & Request metrics (since last report)
	ReqSuccess       int32  `json:"req_success_2xx_3xx"`
	ReqClientError   int32  `json:"req_client_error_4xx"`
	ReqServerError   int32  `json:"req_server_error_5xx"`
	ReqInternalError int32  `json:"req_internal_error_other"`
	BytesReceived    uint64 `json:"bytes_received"`
	BytesSent        uint64 `json:"bytes_sent"`
}
