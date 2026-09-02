package stats

import "time"

type AgentStats struct {
	Timestamp                time.Time `json:"timestamp"`
	NumGoroutine             int       `json:"num_goroutine"`
	MemorySysMB              uint64    `json:"memory_sys_mb"`
	ActiveRequests           int32     `json:"active_requests"`
	CPUPercent               float64   `json:"cpu_percent"`
	TunnelCapacityAdaptive   bool      `json:"tunnel_capacity_adaptive"`
	TunnelAdmissionLimit     int64     `json:"tunnel_admission_limit"`
	TunnelStreamsInUse       int64     `json:"tunnel_streams_in_use"`
	MemoryPressure           string    `json:"memory_pressure"`
	MemoryUsageBytes         int64     `json:"memory_usage_bytes"`
	MemoryLimitBytes         int64     `json:"memory_limit_bytes"`
	MemorySource             string    `json:"memory_source"`
	TunnelPressureRejections uint64    `json:"tunnel_pressure_rejections"`
	TunnelLimitRejections    uint64    `json:"tunnel_limit_rejections"`
	FileDescriptorsUsed      int64     `json:"file_descriptors_used"`
	FileDescriptorsLimit     int64     `json:"file_descriptors_limit"`
	ResourcePressureReason   string    `json:"resource_pressure_reason"`
	ResourceSampleError      string    `json:"resource_sample_error"`
	ResourceLastGoodAt       time.Time `json:"resource_last_good_at"`

	// Traffic & Request metrics (since last report)
	ReqSuccess       int32  `json:"req_success_2xx_3xx"`
	ReqClientError   int32  `json:"req_client_error_4xx"`
	ReqServerError   int32  `json:"req_server_error_5xx"`
	ReqInternalError int32  `json:"req_internal_error_other"`
	BytesReceived    uint64 `json:"bytes_received"`
	BytesSent        uint64 `json:"bytes_sent"`
}
