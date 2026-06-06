package server

import (
	"errors"
	"sync"
)

var errAgentDisconnected = errors.New("agent disconnected")

type agentHub struct {
	mu         sync.RWMutex
	byID       map[int64]*AgentConn
	byPublicID map[string]*AgentConn
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
	defer h.mu.Unlock()
	h.disconnectLocked(conn)
}

func (h *agentHub) disconnectByID(agentID int64) *AgentConn {
	if h == nil {
		return nil
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	conn := h.byID[agentID]
	if conn == nil {
		return nil
	}
	h.disconnectLocked(conn)
	return conn
}

func (h *agentHub) disconnectLocked(conn *AgentConn) {
	if conn == nil {
		return
	}
	if current := h.byPublicID[conn.PublicID]; current == conn {
		delete(h.byPublicID, conn.PublicID)
	}
	if current := h.byID[conn.AgentID]; current == conn {
		delete(h.byID, conn.AgentID)
	}
	if conn.Done == nil {
		return
	}
	select {
	case <-conn.Done:
	default:
		close(conn.Done)
	}
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
	if disconnected && a.BackendHealth != nil {
		a.BackendHealth.recordAgentDisconnectedForAll(agentID)
	}
	return disconnected
}
