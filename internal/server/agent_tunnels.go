package server

import (
	"context"
	"sync"

	"github.com/hashicorp/yamux"

	"p2pstream/internal/tunnel"
)

// All lanes belong to one authenticated agent generation and share its stream
// admission, update drain and revocation. Losing a lane does not mark the
// agent offline while another lane can still carry requests.
type agentTunnelOwnedResource struct {
	AggregateInitialPoolBytes    int64
	SessionMetadataBytes         int64
	LegacyControlSupplementBytes int64
	AggregateGrowth              *tunnel.AggregateReceiveWindowGrowth
}

func (a *AgentConn) installTunnel(lane int, session *yamux.Session, resources ...agentTunnelOwnedResource) *yamux.Session {
	a.tunnelMu.Lock()
	defer a.tunnelMu.Unlock()
	old := a.tunnels[lane]
	a.tunnels[lane] = session
	a.tunnelResources[lane] = agentTunnelOwnedResource{}
	if len(resources) > 0 {
		a.tunnelResources[lane] = resources[0]
	}
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
	a.tunnelResources[lane] = agentTunnelOwnedResource{}
	for _, other := range a.tunnels {
		if other != nil && !other.IsClosed() {
			return true, false
		}
	}
	return true, true
}

type agentTunnelResourceSnapshot struct {
	AggregateFlowControl         bool
	ControlConnected             bool
	BulkSessions                 int
	AggregateInitialPoolBytes    int64
	AggregateCurrentPoolBytes    int64
	SessionMetadataBytes         int64
	LegacyControlSupplementBytes int64
	SessionResourceBytes         int64
}

func (a *AgentConn) tunnelResourceSnapshot() agentTunnelResourceSnapshot {
	if a == nil {
		return agentTunnelResourceSnapshot{}
	}
	a.tunnelMu.RLock()
	defer a.tunnelMu.RUnlock()
	snapshot := agentTunnelResourceSnapshot{AggregateFlowControl: a.AggregateFlowControl}
	for lane, session := range a.tunnels {
		if session == nil || session.IsClosed() {
			continue
		}
		if lane == 0 {
			snapshot.ControlConnected = true
		} else {
			snapshot.BulkSessions++
		}
		resource := a.tunnelResources[lane]
		snapshot.AggregateInitialPoolBytes += resource.AggregateInitialPoolBytes
		snapshot.SessionMetadataBytes += resource.SessionMetadataBytes
		snapshot.LegacyControlSupplementBytes += resource.LegacyControlSupplementBytes
		if stats := session.AggregateReceiveWindowStats(); stats.Enabled {
			snapshot.AggregateCurrentPoolBytes += int64(yamux.AggregateReceiveWindowMemory(stats.ReceiveLimit))
		}
	}
	snapshot.SessionResourceBytes = snapshot.AggregateCurrentPoolBytes + snapshot.SessionMetadataBytes + snapshot.LegacyControlSupplementBytes
	return snapshot
}

func (a *AgentConn) transportLane() int {
	if a == nil {
		return -1
	}
	a.tunnelMu.Lock()
	defer a.tunnelMu.Unlock()
	if !a.tunnelsInitialized {
		if !a.AggregateFlowControl && a.Session != nil && !a.Session.IsClosed() {
			return 0
		}
		return -1
	}
	first := 0
	count := len(a.tunnels)
	if a.AggregateFlowControl {
		first = 1
		count--
	}
	if a.nextTunnel < first || a.nextTunnel >= len(a.tunnels) {
		a.nextTunnel = first
	}
	for offset := range count {
		lane := first + (a.nextTunnel-first+offset)%count
		if session := a.tunnels[lane]; session != nil && !session.IsClosed() {
			a.nextTunnel = first + (lane-first+1)%count
			return lane
		}
	}
	return -1
}

func (a *AgentConn) tunnelSession(lane int) *yamux.Session {
	if a == nil {
		return nil
	}
	a.tunnelMu.RLock()
	defer a.tunnelMu.RUnlock()
	if !a.tunnelsInitialized {
		if !a.AggregateFlowControl && a.Session != nil && !a.Session.IsClosed() {
			return a.Session
		}
		return nil
	}
	if lane >= 0 && lane < len(a.tunnels) {
		if session := a.tunnels[lane]; session != nil && !session.IsClosed() {
			return session
		}
	}
	if a.AggregateFlowControl {
		// Lane zero carries trusted control traffic only, and bulk lanes never
		// substitute for it. Falling across this boundary would defeat both the
		// independent control budget and aggregate memory accounting.
		return nil
	}
	for _, session := range a.tunnels {
		if session != nil && !session.IsClosed() {
			return session
		}
	}
	return nil
}

func (a *AgentConn) tunnelAggregateGrowth(lane int, session *yamux.Session) *tunnel.AggregateReceiveWindowGrowth {
	if a == nil || lane < 0 || lane >= len(a.tunnels) {
		return nil
	}
	a.tunnelMu.RLock()
	defer a.tunnelMu.RUnlock()
	if a.tunnels[lane] != session {
		return nil
	}
	return a.tunnelResources[lane].AggregateGrowth
}

// acquireControlStream bounds negotiated control work independently from the
// generation's bulk quota. The lease spans opening, live and closing states so
// local FIN cannot admit a fifth receive-window promise before peer cleanup.
func (a *AgentConn) acquireControlStream(ctx context.Context) (func(), bool) {
	if a == nil {
		return nil, false
	}
	if ctx == nil {
		ctx = context.Background()
	}
	a.controlGateMu.Lock()
	if a.controlGate == nil {
		a.controlGate = make(chan struct{}, tunnel.NegotiatedControlStreamLimit)
	}
	gate := a.controlGate
	a.controlGateMu.Unlock()
	select {
	case gate <- struct{}{}:
	case <-ctx.Done():
		return nil, false
	case <-a.Done:
		return nil, false
	}
	select {
	case <-ctx.Done():
		<-gate
		return nil, false
	case <-a.Done:
		<-gate
		return nil, false
	default:
	}
	var once sync.Once
	return func() { once.Do(func() { <-gate }) }, true
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
