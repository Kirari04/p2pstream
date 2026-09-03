package updater

import (
	"context"
	"errors"
	"os/exec"
	"time"
)

type SystemdService struct {
	Unit      string
	Timeout   time.Duration
	Stability time.Duration
}

func DefaultSystemdService() SystemdService {
	return SystemdService{Unit: DefaultAgentUnit, Timeout: defaultHealthTimeout, Stability: 5 * time.Second}
}

func (s SystemdService) validate() error {
	if s.Unit != DefaultAgentUnit {
		return errors.New("updater may only control the p2pstream agent service")
	}
	return nil
}

func (s SystemdService) Restart(ctx context.Context) error {
	if err := s.validate(); err != nil {
		return err
	}
	return exec.CommandContext(ctx, "/usr/bin/systemctl", "restart", DefaultAgentUnit).Run()
}

func (s SystemdService) Healthy(ctx context.Context) error {
	if err := s.validate(); err != nil {
		return err
	}
	timeout := s.Timeout
	if timeout <= 0 {
		timeout = defaultHealthTimeout
	}
	deadline := time.Now().Add(timeout)
	stability := s.Stability
	if stability <= 0 {
		stability = 5 * time.Second
	}
	var activeSince time.Time
	for {
		if err := exec.CommandContext(ctx, "/usr/bin/systemctl", "is-active", "--quiet", DefaultAgentUnit).Run(); err == nil {
			if activeSince.IsZero() {
				activeSince = time.Now()
			}
			if time.Since(activeSince) >= stability {
				return nil
			}
		} else {
			activeSince = time.Time{}
		}
		if time.Now().After(deadline) {
			return errors.New("agent service did not become active before health timeout")
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}
	}
}
