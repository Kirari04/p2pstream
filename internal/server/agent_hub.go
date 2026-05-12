package server

import (
	"context"
	"errors"
	"sync"

	"p2pstream/msg"
)

var errAgentDisconnected = errors.New("agent disconnected")

type pendingAgentRequest struct {
	AgentID       int64
	AgentPublicID string
	ResponseCh    chan *msg.Request
	ErrorCh       chan error
	ctx           context.Context
	cancel        context.CancelFunc
	closeOnce     sync.Once
}

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
	h.mu.Lock()
	defer h.mu.Unlock()
	if current := h.byPublicID[conn.PublicID]; current == conn {
		delete(h.byPublicID, conn.PublicID)
	}
	if current := h.byID[conn.AgentID]; current == conn {
		delete(h.byID, conn.AgentID)
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

func (a *App) failPendingRequestsForAgent(agentID int64, err error) {
	a.PendingRequests.Range(func(key, value any) bool {
		pending, ok := value.(*pendingAgentRequest)
		if !ok || pending.AgentID != agentID {
			return true
		}
		pending.fail(err)
		return true
	})
}

func (p *pendingAgentRequest) fail(err error) {
	select {
	case p.ErrorCh <- err:
	default:
	}
	p.closeOnce.Do(func() {
		if p.cancel != nil {
			p.cancel()
		}
	})
}
