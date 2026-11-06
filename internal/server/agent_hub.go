package server

import (
	"errors"
	"sync"
)

var errAgentDisconnected = errors.New("agent disconnected")

type agentHub struct {
	mu           sync.RWMutex
	byID         map[int64]*AgentConn
	byPublicID   map[string]*AgentConn
	onDisconnect func(*AgentConn)
}

func newAgentHub() *agentHub {
	return &agentHub{
		byID:       make(map[int64]*AgentConn),
		byPublicID: make(map[string]*AgentConn),
	}
}

func (h *agentHub) connect(conn *AgentConn) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if _, ok := h.byID[conn.AgentID]; ok {
		return errors.New("agent is already connected")
	}
	if _, ok := h.byPublicID[conn.PublicID]; ok {
		return errors.New("agent is already connected")
	}
	h.byPublicID[conn.PublicID] = conn
	h.byID[conn.AgentID] = conn
	return nil
}

func (h *agentHub) replace(conn *AgentConn) ([]*AgentConn, error) {
	if h == nil || conn == nil {
		return nil, errors.New("agent connection is required")
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	displacedByID := h.byID[conn.AgentID]
	displacedByPublicID := h.byPublicID[conn.PublicID]
	if displacedByID == conn || displacedByPublicID == conn {
		return nil, errors.New("agent connection is already registered")
	}
	displaced := make([]*AgentConn, 0, 2)
	if displacedByID != nil {
		displaced = append(displaced, displacedByID)
	}
	if displacedByPublicID != nil && displacedByPublicID != displacedByID {
		displaced = append(displaced, displacedByPublicID)
	}
	for _, old := range displaced {
		delete(h.byID, old.AgentID)
		delete(h.byPublicID, old.PublicID)
	}
	h.byPublicID[conn.PublicID] = conn
	h.byID[conn.AgentID] = conn
	return displaced, nil
}

func (h *agentHub) disconnect(conn *AgentConn) bool {
	if h == nil || conn == nil {
		return false
	}
	h.mu.Lock()
	disconnected := h.disconnectLocked(conn)
	onDisconnect := h.onDisconnect
	h.mu.Unlock()
	if disconnected && onDisconnect != nil {
		onDisconnect(conn)
	}
	return disconnected
}

func (h *agentHub) disconnectByID(agentID int64) *AgentConn {
	if h == nil {
		return nil
	}
	h.mu.Lock()
	conn := h.byID[agentID]
	if conn == nil {
		h.mu.Unlock()
		return nil
	}
	disconnected := h.disconnectLocked(conn)
	onDisconnect := h.onDisconnect
	h.mu.Unlock()
	if disconnected && onDisconnect != nil {
		onDisconnect(conn)
	}
	return conn
}

func (h *agentHub) disconnectLocked(conn *AgentConn) bool {
	if conn == nil {
		return false
	}
	disconnected := false
	if current := h.byPublicID[conn.PublicID]; current == conn {
		delete(h.byPublicID, conn.PublicID)
		disconnected = true
	}
	if current := h.byID[conn.AgentID]; current == conn {
		delete(h.byID, conn.AgentID)
		disconnected = true
	}
	conn.signalDone()
	return disconnected
}

func (h *agentHub) connectedByID(agentID int64) *AgentConn {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.byID[agentID]
}

// lockCurrentConnection keeps an exact tunnel registered while a caller makes
// a decisive state transition. Callers must release before invoking code that
// may acquire another AgentHub read lock.
func (h *agentHub) lockCurrentConnection(agentID int64, expected *AgentConn) (func(), bool) {
	if h == nil || expected == nil {
		return func() {}, false
	}
	h.mu.RLock()
	if h.byID[agentID] != expected {
		h.mu.RUnlock()
		return func() {}, false
	}
	return h.mu.RUnlock, true
}

func (h *agentHub) connectedCount() int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.byID)
}

func (h *agentHub) connectedIDs() map[int64]*AgentConn {
	h.mu.RLock()
	defer h.mu.RUnlock()
	resp := make(map[int64]*AgentConn, len(h.byID))
	for id, conn := range h.byID {
		resp[id] = conn
	}
	return resp
}

func (a *App) revokeAgentConnection(agentID int64) bool {
	if a == nil {
		return false
	}
	disconnected := false
	if a.AgentHub != nil {
		disconnected = a.AgentHub.disconnectByID(agentID) != nil
	}
	if disconnected && a.TargetHealth != nil {
		a.TargetHealth.recordAgentDisconnectedForAll(agentID)
	}
	return disconnected
}
