package server

import (
	"github.com/hashicorp/yamux"
)

// All lanes belong to one authenticated agent generation and share its stream
// admission, update drain and revocation. Losing a lane does not mark the
// agent offline while another lane can still carry requests.
func (a *AgentConn) installTunnel(lane int, session *yamux.Session) *yamux.Session {
	a.tunnelMu.Lock()
	defer a.tunnelMu.Unlock()
	old := a.tunnels[lane]
	a.tunnels[lane] = session
	a.tunnelsInitialized = true
	return old
}

func (a *AgentConn) removeTunnel(lane int, session *yamux.Session) (removed, last bool) {
	a.tunnelMu.Lock()
	defer a.tunnelMu.Unlock()
	if a.tunnels[lane] != session {
		return false, false
	}
	a.tunnels[lane] = nil
	for _, other := range a.tunnels {
		if other != nil && !other.IsClosed() {
			return true, false
		}
	}
	return true, true
}

func (a *AgentConn) transportLane() int {
	if a == nil {
		return 0
	}
	a.tunnelMu.Lock()
	defer a.tunnelMu.Unlock()
	for offset := range len(a.tunnels) {
		lane := (a.nextTunnel + offset) % len(a.tunnels)
		if session := a.tunnels[lane]; session != nil && !session.IsClosed() {
			a.nextTunnel = (lane + 1) % len(a.tunnels)
			return lane
		}
	}
	return 0
}

func (a *AgentConn) tunnelSession(lane int) *yamux.Session {
	if a == nil {
		return nil
	}
	a.tunnelMu.RLock()
	defer a.tunnelMu.RUnlock()
	if !a.tunnelsInitialized {
		if a.Session != nil && !a.Session.IsClosed() {
			return a.Session
		}
		return nil
	}
	if lane >= 0 && lane < len(a.tunnels) {
		if session := a.tunnels[lane]; session != nil && !session.IsClosed() {
			return session
		}
	}
	for _, session := range a.tunnels {
		if session != nil && !session.IsClosed() {
			return session
		}
	}
	return nil
}

func (p *agentTransportPool) closeTunnelLane(agent *AgentConn, lane int) {
	if p != nil {
		p.closeEntriesWhere(func(key agentTransportKey, entry *pooledAgentTransport) bool {
			return entry.agent == agent && key.TunnelLane == lane
		})
	}
}

func (a *App) cleanupAgentTunnel(agent *AgentConn, lane int, session *yamux.Session) {
	unlock := a.lockAgentAuth(agent.AgentID)
	defer unlock()
	removed, last := agent.removeTunnel(lane, session)
	if !removed {
		return
	}
	a.AgentTransports.closeTunnelLane(agent, lane)
	if last {
		a.cleanupAgentConnectionLocked(agent)
	}
}
