package server

import "p2pstream/internal/tunnel"

func newAgentRequestLimiter(limit int64) *tunnel.StreamLimiter {
	limiter, err := tunnel.NewStreamLimiter(limit)
	if err != nil {
		limiter, _ = tunnel.NewStreamLimiter(tunnel.DefaultMaxConcurrentAgentRequests)
	}
	return limiter
}
