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
	if _, ok := h.byPublicID[conn.PublicID]; ok {
		return errors.New("agent is already connected")
	}
	h.byPublicID[conn.PublicID] = conn
	h.byID[conn.AgentID] = conn
	return nil
}

func (h *agentHub) disconnect(conn *AgentConn) {
	if h == nil || conn == nil {
		return
	}
	h.mu.Lock()
	disconnected := h.disconnectLocked(conn)
	onDisconnect := h.onDisconnect
	h.mu.Unlock()
	if disconnected && onDisconnect != nil {
		onDisconnect(conn)
	}
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
	if conn.Done == nil {
		return disconnected
	}
	select {
	case <-conn.Done:
	default:
		close(conn.Done)
	}
	return disconnected
}

func (h *agentHub) connectedByID(agentID int64) *AgentConn {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.byID[agentID]
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
