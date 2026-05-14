package server

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/google/uuid"
	"p2pstream/msg"
)

var (
	errAgentDisconnected = errors.New("agent disconnected")
	errAgentTokenRotated = errors.New("agent token rotated")
)

const (
	lateAgentResponseTTL        = 2 * time.Minute
	lateAgentResponseMaxEntries = 4096
)

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

type lateAgentResponseTracker struct {
	mu      sync.Mutex
	entries map[uuid.UUID]lateAgentResponse
}

type lateAgentResponse struct {
	reason    string
	expiresAt time.Time
}

func newLateAgentResponseTracker() *lateAgentResponseTracker {
	return &lateAgentResponseTracker{
		entries: make(map[uuid.UUID]lateAgentResponse),
	}
}

func (t *lateAgentResponseTracker) record(id uuid.UUID, reason string) {
	if t == nil || id == uuid.Nil {
		return
	}
	if reason == "" {
		reason = "completed"
	}
	now := time.Now()
	t.mu.Lock()
	defer t.mu.Unlock()
	t.cleanupLocked(now)
	for len(t.entries) >= lateAgentResponseMaxEntries {
		for existingID := range t.entries {
			delete(t.entries, existingID)
			break
		}
	}
	t.entries[id] = lateAgentResponse{
		reason:    reason,
		expiresAt: now.Add(lateAgentResponseTTL),
	}
}

func (t *lateAgentResponseTracker) lookup(id uuid.UUID) (string, bool) {
	if t == nil || id == uuid.Nil {
		return "", false
	}
	now := time.Now()
	t.mu.Lock()
	defer t.mu.Unlock()
	entry, ok := t.entries[id]
	if !ok {
		return "", false
	}
	if now.After(entry.expiresAt) {
		delete(t.entries, id)
		return "", false
	}
	return entry.reason, true
}

func (t *lateAgentResponseTracker) cleanupLocked(now time.Time) {
	for id, entry := range t.entries {
		if now.After(entry.expiresAt) {
			delete(t.entries, id)
		}
	}
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

func (a *App) revokeAgentConnection(agentID int64, err error) bool {
	if a == nil {
		return false
	}
	disconnected := false
	if a.AgentHub != nil {
		disconnected = a.AgentHub.disconnectByID(agentID) != nil
	}
	a.failPendingRequestsForAgent(agentID, err)
	return disconnected
}

func (a *App) finishPendingAgentRequest(id uuid.UUID, reason string) {
	a.PendingRequests.Delete(id)
	if a.LateAgentResponses != nil {
		a.LateAgentResponses.record(id, reason)
	}
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
