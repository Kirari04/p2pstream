package tunnel

import (
	"io"
	"time"

	"github.com/hashicorp/yamux"
)

func DefaultYamuxConfig(logger yamux.Logger) *yamux.Config {
	cfg := yamux.DefaultConfig()
	cfg.AcceptBacklog = 256
	cfg.EnableKeepAlive = true
	cfg.KeepAliveInterval = 20 * time.Second
	cfg.ConnectionWriteTimeout = 10 * time.Second
	cfg.StreamOpenTimeout = 10 * time.Second
	cfg.StreamCloseTimeout = 30 * time.Second
	if logger != nil {
		cfg.Logger = logger
		cfg.LogOutput = nil
	} else {
		cfg.Logger = nil
		cfg.LogOutput = io.Discard
	}
	return cfg
}
