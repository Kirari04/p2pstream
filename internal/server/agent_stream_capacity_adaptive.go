package server

import (
	"context"
	"time"

	"github.com/rs/zerolog/log"

	"p2pstream/internal/config"
	"p2pstream/internal/sysmetrics"
)

func newServerAdaptiveMemoryController(cfg *config.Config) *sysmetrics.AdaptiveMemoryController {
	adaptive := sysmetrics.DefaultAdaptiveMemoryConfig()
	if cfg != nil {
		if cfg.ServerTunnelMemorySoftPercent > 0 {
			adaptive.SoftLimitPercent = cfg.ServerTunnelMemorySoftPercent
		}
		if cfg.ServerTunnelMemoryHardPercent > 0 {
			adaptive.HardLimitPercent = cfg.ServerTunnelMemoryHardPercent
		}
		if cfg.ServerTunnelMemoryRecoveryPercent > 0 {
			adaptive.RecoveryPercent = cfg.ServerTunnelMemoryRecoveryPercent
		}
		if cfg.ServerTunnelMemorySampleMillis > 0 {
			adaptive.SampleInterval = time.Duration(cfg.ServerTunnelMemorySampleMillis) * time.Millisecond
		}
		if cfg.ServerTunnelEstimatedStreamBytes > 0 {
			adaptive.EstimatedBytesPerAdmission = cfg.ServerTunnelEstimatedStreamBytes
		}
	}
	controller, err := sysmetrics.NewAdaptiveMemoryController(adaptive, nil)
	if err != nil {
		return nil
	}
	return controller
}

// StartAdaptiveTunnelCapacity keeps queued admissions and idle-pool reclaim in
// sync with resource recovery even when no new request arrives to refresh the
// lazy fast path. The caller owns ctx; no independent lifecycle is introduced.
func (a *App) StartAdaptiveTunnelCapacity(ctx context.Context) {
	if a == nil || a.agentStreamCapacity == nil || a.Config == nil || (!a.Config.ServerTunnelCapacityAuto && a.Config.ServerTunnelMaxConcurrentStreams <= 0) {
		return
	}
	interval := time.Duration(a.Config.ServerTunnelMemorySampleMillis) * time.Millisecond
	if interval <= 0 {
		interval = sysmetrics.DefaultAdaptiveMemoryConfig().SampleInterval
	}
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		previous := sysmetrics.MemoryPressureUnknown
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				snapshot := a.agentStreamCapacity.refreshAdaptiveCapacity(true)
				// A transition-triggered sweep frees reclaimable idle stream
				// reservations without continuously destroying newly warmed pools
				// while unrelated memory pressure remains elevated. Admission misses
				// retain their demand-driven, targeted reclaim path.
				if maximum := adaptivePressureTransitionReclaim(previous, snapshot.Level); maximum > 0 {
					a.reclaimIdleAgentTransports(maximum)
				}
				if snapshot.Level != previous {
					log.Info().
						Str("memory_pressure", snapshot.Level.String()).
						Str("resource_pressure_reason", snapshot.PressureReason).
						Str("memory_source", snapshot.Usage.Source).
						Int64("memory_used_bytes", snapshot.Usage.UsedBytes).
						Int64("memory_limit_bytes", snapshot.Usage.LimitBytes).
						Int64("file_descriptors_used", snapshot.FDUsed).
						Int64("file_descriptors_limit", snapshot.FDLimit).
						Bool("resource_sensor_degraded", snapshot.SampleError != "").
						Int("stream_admission_limit", snapshot.AdmissionLimit).
						Msg("Server tunnel adaptive capacity changed")
					previous = snapshot.Level
				}
			}
		}
	}()
}

func adaptivePressureTransitionReclaim(previous, current sysmetrics.MemoryPressureLevel) int {
	if previous == current {
		return 0
	}
	switch current {
	case sysmetrics.MemoryPressureSoft:
		return 8
	case sysmetrics.MemoryPressureCritical:
		return 32
	default:
		return 0
	}
}

func (a *App) reclaimIdleAgentTransports(maximum int) int {
	if a == nil || a.AgentTransports == nil || maximum <= 0 {
		return 0
	}
	reclaimed := 0
	for reclaimed < maximum && a.AgentTransports.reclaimOldestIdle(nil, true) {
		reclaimed++
	}
	return reclaimed
}
