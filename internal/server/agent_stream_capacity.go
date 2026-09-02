package server

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"
)

type agentStreamCapacityClass uint8

const (
	agentStreamCapacityPublicPooled agentStreamCapacityClass = iota
	agentStreamCapacityPublicOneShot
	agentStreamCapacityTrustedHealth
	agentStreamCapacityClassCount
)

type agentStreamLeaseState uint8

const (
	agentStreamLeaseOpening agentStreamLeaseState = iota
	agentStreamLeaseLive
	agentStreamLeaseClosing
	agentStreamLeaseReleased
	agentStreamLeaseStateCount
)

var (
	errAgentStreamCapacityInvalidConfig       = errors.New("invalid agent stream capacity configuration")
	errAgentStreamCapacityInvalidRequest      = errors.New("invalid agent stream capacity request")
	errAgentStreamCapacityClassDisabled       = errors.New("agent stream capacity class is disabled")
	errAgentStreamCapacityTotalBudget         = errors.New("agent stream total budget is full")
	errAgentStreamCapacityPublicBudget        = errors.New("agent stream public budget is full")
	errAgentStreamCapacityPooledBudget        = errors.New("agent stream pooled budget is full")
	errAgentStreamCapacityControlBudget       = errors.New("agent stream control budget is full")
	errAgentStreamCapacitySessionBudget       = errors.New("agent session stream budget is full")
	errAgentStreamCapacitySessionOpeningLimit = errors.New("agent session stream-opening limit is full")
	errAgentStreamCapacityWaitTurn            = errors.New("agent stream capacity waiter is waiting for its fair turn")
	errAgentStreamCapacityQueueFull           = errors.New("agent stream capacity waiter queue is full")
	errAgentStreamCapacityKeyQueueFull        = errors.New("agent stream capacity per-key waiter queue is full")
)

type agentStreamCapacityAcquireError struct {
	Cause      error
	Constraint error
	QueueKey   string
	SessionKey string
}

func (e *agentStreamCapacityAcquireError) Error() string {
	if e == nil {
		return "agent stream capacity acquisition failed"
	}
	switch {
	case e.Cause != nil && e.Constraint != nil:
		return fmt.Sprintf("agent stream capacity acquisition failed: %v: %v", e.Cause, e.Constraint)
	case e.Cause != nil:
		return fmt.Sprintf("agent stream capacity acquisition failed: %v", e.Cause)
	case e.Constraint != nil:
		return fmt.Sprintf("agent stream capacity acquisition failed: %v", e.Constraint)
	default:
		return "agent stream capacity acquisition failed"
	}
}

func (e *agentStreamCapacityAcquireError) Unwrap() []error {
	if e == nil {
		return nil
	}
	causes := make([]error, 0, 2)
	if e.Cause != nil {
		causes = append(causes, e.Cause)
	}
	if e.Constraint != nil {
		causes = append(causes, e.Constraint)
	}
	return causes
}

type agentStreamCapacityConfig struct {
	Total                          int
	Public                         int
	Pooled                         int
	Control                        int
	MaxWaiters                     int
	MaxWaitersPerKey               int
	MaxOpeningPerSession           int
	ReservedPublicForOtherSessions int
}

type agentStreamCapacityManager struct {
	mu sync.Mutex

	config agentStreamCapacityConfig

	usedTotal   int
	usedPublic  int
	usedPooled  int
	usedControl int

	stateByClass                [agentStreamCapacityClassCount][agentStreamLeaseReleased]int
	openingBySession            map[string]int
	publicOpeningBySession      map[string]int
	publicBySession             map[string]int
	totalBySession              map[string]int
	registeredSessions          map[string]struct{}
	sessionLimits               map[string]int
	activeLeases                map[uint64]*agentStreamCapacityLease
	nextLeaseID                 uint64
	granted                     uint64
	released                    uint64
	admissionMissesByConstraint map[string]uint64

	queues       map[string][]*agentStreamCapacityWaiter
	queueKeys    []string
	nextQueueKey int
	waiters      int
}

type agentStreamCapacityWaiter struct {
	class      agentStreamCapacityClass
	queueKey   string
	sessionKey string
	ctx        context.Context
	ready      chan struct{}
	lease      *agentStreamCapacityLease
	err        error
	status     agentStreamCapacityWaiterStatus
}

type agentStreamCapacityWaiterStatus uint8

const (
	agentStreamCapacityWaiterQueued agentStreamCapacityWaiterStatus = iota
	agentStreamCapacityWaiterGranted
	agentStreamCapacityWaiterCanceled
)

type agentStreamCapacityLease struct {
	manager        *agentStreamCapacityManager
	id             uint64
	class          agentStreamCapacityClass
	queueKey       string
	sessionKey     string
	state          agentStreamLeaseState
	stateChangedAt time.Time
}

type agentStreamCapacityBudgetSnapshot struct {
	Capacity int
	InUse    int
}

type agentStreamCapacityStateSnapshot struct {
	Opening int
	Live    int
	Closing int
}

type agentStreamCapacitySnapshot struct {
	Total   agentStreamCapacityBudgetSnapshot
	Public  agentStreamCapacityBudgetSnapshot
	Pooled  agentStreamCapacityBudgetSnapshot
	Control agentStreamCapacityBudgetSnapshot

	States                      agentStreamCapacityStateSnapshot
	StatesByClass               map[agentStreamCapacityClass]agentStreamCapacityStateSnapshot
	WaitersByClass              map[agentStreamCapacityClass]int
	WaitersByConstraint         map[string]int
	OpeningBySession            map[string]int
	PublicOpeningBySession      map[string]int
	PublicBySession             map[string]int
	TotalBySession              map[string]int
	SessionLimits               map[string]int
	RegisteredSessions          int
	WaitersByKey                map[string]int
	Waiters                     int
	ActiveLeases                int
	Granted                     uint64
	Released                    uint64
	AdmissionMissesByConstraint map[string]uint64
	OldestClosingAgeMillis      int64
	MaxSessionPublicInUse       int
	ContendedSessionPublicLimit int
}

func (c agentStreamCapacityClass) String() string {
	switch c {
	case agentStreamCapacityPublicPooled:
		return "public_pooled"
	case agentStreamCapacityPublicOneShot:
		return "public_one_shot"
	case agentStreamCapacityTrustedHealth:
		return "trusted_health"
	default:
		return fmt.Sprintf("unknown_%d", uint8(c))
	}
}

func (s agentStreamLeaseState) String() string {
	switch s {
	case agentStreamLeaseOpening:
		return "opening"
	case agentStreamLeaseLive:
		return "live"
	case agentStreamLeaseClosing:
		return "closing"
	case agentStreamLeaseReleased:
		return "released"
	default:
		return fmt.Sprintf("unknown_%d", uint8(s))
	}
}

func agentStreamCapacityConstraintName(err error) string {
	switch {
	case errors.Is(err, errAgentStreamCapacityTotalBudget):
		return "total_budget"
	case errors.Is(err, errAgentStreamCapacityPublicBudget):
		return "public_budget"
	case errors.Is(err, errAgentStreamCapacityPooledBudget):
		return "pooled_budget"
	case errors.Is(err, errAgentStreamCapacityControlBudget):
		return "control_budget"
	case errors.Is(err, errAgentStreamCapacitySessionBudget):
		return "session_budget"
	case errors.Is(err, errAgentStreamCapacitySessionOpeningLimit):
		return "session_opening_limit"
	case errors.Is(err, errAgentStreamCapacityWaitTurn):
		return "fair_turn"
	case errors.Is(err, errAgentStreamCapacityQueueFull):
		return "queue_full"
	case errors.Is(err, errAgentStreamCapacityKeyQueueFull):
		return "key_queue_full"
	case errors.Is(err, errAgentStreamCapacityClassDisabled):
		return "class_disabled"
	default:
		return "unknown"
	}
}

func newAgentStreamCapacityManager(config agentStreamCapacityConfig) (*agentStreamCapacityManager, error) {
	if err := validateAgentStreamCapacityConfig(config); err != nil {
		return nil, err
	}
	return &agentStreamCapacityManager{
		config:                      config,
		openingBySession:            make(map[string]int),
		publicOpeningBySession:      make(map[string]int),
		publicBySession:             make(map[string]int),
		totalBySession:              make(map[string]int),
		registeredSessions:          make(map[string]struct{}),
		sessionLimits:               make(map[string]int),
		activeLeases:                make(map[uint64]*agentStreamCapacityLease),
		admissionMissesByConstraint: make(map[string]uint64),
		queues:                      make(map[string][]*agentStreamCapacityWaiter),
	}, nil
}

func validateAgentStreamCapacityConfig(config agentStreamCapacityConfig) error {
	switch {
	case config.Total < 1:
		return fmt.Errorf("%w: total must be at least 1", errAgentStreamCapacityInvalidConfig)
	case config.Public < 0 || config.Public > config.Total:
		return fmt.Errorf("%w: public must be between 0 and total", errAgentStreamCapacityInvalidConfig)
	case config.Pooled < 0 || config.Pooled > config.Public:
		return fmt.Errorf("%w: pooled must be between 0 and public", errAgentStreamCapacityInvalidConfig)
	case config.Control < 0 || config.Control > config.Total:
		return fmt.Errorf("%w: control must be between 0 and total", errAgentStreamCapacityInvalidConfig)
	case config.Public+config.Control > config.Total:
		return fmt.Errorf("%w: public plus control must not exceed total", errAgentStreamCapacityInvalidConfig)
	case config.MaxWaiters < 0:
		return fmt.Errorf("%w: max waiters must not be negative", errAgentStreamCapacityInvalidConfig)
	case config.MaxWaitersPerKey < 0 || config.MaxWaitersPerKey > config.MaxWaiters:
		return fmt.Errorf("%w: max waiters per key must be between 0 and max waiters", errAgentStreamCapacityInvalidConfig)
	case config.MaxWaiters > 0 && config.MaxWaitersPerKey == 0:
		return fmt.Errorf("%w: max waiters per key must be positive when waiting is enabled", errAgentStreamCapacityInvalidConfig)
	case config.MaxOpeningPerSession < 1:
		return fmt.Errorf("%w: max opening per session must be at least 1", errAgentStreamCapacityInvalidConfig)
	case config.ReservedPublicForOtherSessions < 0 || (config.Public > 0 && config.ReservedPublicForOtherSessions >= config.Public):
		return fmt.Errorf("%w: public reserve for other sessions must be between 0 and public-1", errAgentStreamCapacityInvalidConfig)
	default:
		return nil
	}
}

func (m *agentStreamCapacityManager) acquire(
	ctx context.Context,
	class agentStreamCapacityClass,
	queueKey string,
	sessionKey string,
) (*agentStreamCapacityLease, error) {
	if m == nil {
		return nil, fmt.Errorf("%w: manager is nil", errAgentStreamCapacityInvalidRequest)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if class >= agentStreamCapacityClassCount {
		return nil, fmt.Errorf("%w: unknown class %d", errAgentStreamCapacityInvalidRequest, class)
	}
	if queueKey == "" {
		return nil, fmt.Errorf("%w: queue key is empty", errAgentStreamCapacityInvalidRequest)
	}
	if sessionKey == "" {
		return nil, fmt.Errorf("%w: session key is empty", errAgentStreamCapacityInvalidRequest)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	m.mu.Lock()
	if !m.classEnabledLocked(class) {
		m.recordRejectionLocked(errAgentStreamCapacityClassDisabled)
		m.mu.Unlock()
		return nil, errAgentStreamCapacityClassDisabled
	}
	if !m.hasCompetingWaiterLocked(class) && m.canGrantLocked(class, sessionKey) {
		lease := m.grantLocked(class, queueKey, sessionKey)
		m.mu.Unlock()
		return lease, nil
	}
	classWaiters := m.waiterCountForClassLocked(class)
	queueLimitReached := false
	if class == agentStreamCapacityTrustedHealth {
		controlWaiterLimit := m.config.Control
		if controlWaiterLimit < 1 {
			controlWaiterLimit = 1
		}
		queueLimitReached = classWaiters >= controlWaiterLimit
	} else {
		publicWaiters := m.waiters - m.waiterCountForClassLocked(agentStreamCapacityTrustedHealth)
		queueLimitReached = publicWaiters >= m.config.MaxWaiters
	}
	if queueLimitReached {
		m.recordRejectionLocked(errAgentStreamCapacityQueueFull)
		m.mu.Unlock()
		return nil, &agentStreamCapacityAcquireError{
			Constraint: errAgentStreamCapacityQueueFull,
			QueueKey:   queueKey,
			SessionKey: sessionKey,
		}
	}
	if len(m.queues[queueKey]) >= m.config.MaxWaitersPerKey {
		m.recordRejectionLocked(errAgentStreamCapacityKeyQueueFull)
		m.mu.Unlock()
		return nil, &agentStreamCapacityAcquireError{
			Constraint: errAgentStreamCapacityKeyQueueFull,
			QueueKey:   queueKey,
			SessionKey: sessionKey,
		}
	}
	waiter := &agentStreamCapacityWaiter{
		class:      class,
		queueKey:   queueKey,
		sessionKey: sessionKey,
		ctx:        ctx,
		ready:      make(chan struct{}),
		status:     agentStreamCapacityWaiterQueued,
	}
	m.enqueueLocked(waiter)
	m.dispatchLocked()
	m.mu.Unlock()

	select {
	case <-waiter.ready:
		m.mu.Lock()
		lease, err := waiter.lease, waiter.err
		if lease != nil && ctx.Err() != nil {
			waiter.lease = nil
			m.mu.Unlock()
			lease.release()
			return nil, ctx.Err()
		}
		m.mu.Unlock()
		return lease, err
	case <-ctx.Done():
		m.mu.Lock()
		switch waiter.status {
		case agentStreamCapacityWaiterQueued:
			constraint := m.blockingConstraintLocked(waiter.class, waiter.sessionKey)
			m.recordRejectionLocked(constraint)
			m.removeWaiterLocked(waiter)
			waiter.status = agentStreamCapacityWaiterCanceled
			waiter.err = newAgentStreamCapacityAcquireError(ctx.Err(), constraint, waiter.queueKey, waiter.sessionKey)
			m.dispatchLocked()
			m.mu.Unlock()
			return nil, waiter.err
		case agentStreamCapacityWaiterGranted:
			lease := waiter.lease
			waiter.lease = nil
			m.mu.Unlock()
			if lease != nil {
				lease.release()
			}
			return nil, ctx.Err()
		default:
			err := waiter.err
			m.mu.Unlock()
			if err == nil {
				err = ctx.Err()
			}
			return nil, err
		}
	}
}

// tryAcquire performs admission without joining the bounded waiter queue. It
// still honors queued work, so callers cannot use the fast path to barge ahead
// of an older request. In particular, pooled HTTP dials can use the pooled
// budget sentinel to select a safe one-shot path without occupying a waiter.
func (m *agentStreamCapacityManager) tryAcquire(
	class agentStreamCapacityClass,
	queueKey string,
	sessionKey string,
) (*agentStreamCapacityLease, error) {
	if m == nil {
		return nil, fmt.Errorf("%w: manager is nil", errAgentStreamCapacityInvalidRequest)
	}
	if class >= agentStreamCapacityClassCount {
		return nil, fmt.Errorf("%w: unknown class %d", errAgentStreamCapacityInvalidRequest, class)
	}
	if queueKey == "" {
		return nil, fmt.Errorf("%w: queue key is empty", errAgentStreamCapacityInvalidRequest)
	}
	if sessionKey == "" {
		return nil, fmt.Errorf("%w: session key is empty", errAgentStreamCapacityInvalidRequest)
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.classEnabledLocked(class) {
		m.recordRejectionLocked(errAgentStreamCapacityClassDisabled)
		return nil, errAgentStreamCapacityClassDisabled
	}
	if m.hasCompetingWaiterLocked(class) {
		m.recordRejectionLocked(errAgentStreamCapacityWaitTurn)
		return nil, newAgentStreamCapacityAcquireError(
			nil,
			errAgentStreamCapacityWaitTurn,
			queueKey,
			sessionKey,
		)
	}
	if constraint := m.blockingConstraintLocked(class, sessionKey); constraint != errAgentStreamCapacityWaitTurn {
		m.recordRejectionLocked(constraint)
		return nil, newAgentStreamCapacityAcquireError(nil, constraint, queueKey, sessionKey)
	}
	return m.grantLocked(class, queueKey, sessionKey), nil
}

func (m *agentStreamCapacityManager) waiterCountForClassLocked(class agentStreamCapacityClass) int {
	count := 0
	for _, queue := range m.queues {
		for _, waiter := range queue {
			if waiter != nil && waiter.class == class {
				count++
			}
		}
	}
	return count
}

func (m *agentStreamCapacityManager) hasCompetingWaiterLocked(class agentStreamCapacityClass) bool {
	for _, queue := range m.queues {
		for _, waiter := range queue {
			if waiter == nil {
				continue
			}
			if class == agentStreamCapacityTrustedHealth {
				if waiter.class == agentStreamCapacityTrustedHealth && m.canGrantLocked(waiter.class, waiter.sessionKey) {
					return true
				}
				continue
			}
			if waiter.class != agentStreamCapacityTrustedHealth && m.canGrantLocked(waiter.class, waiter.sessionKey) {
				return true
			}
		}
	}
	return false
}

func (m *agentStreamCapacityManager) classEnabledLocked(class agentStreamCapacityClass) bool {
	switch class {
	case agentStreamCapacityPublicPooled:
		return m.config.Public > 0 && m.config.Pooled > 0
	case agentStreamCapacityPublicOneShot:
		return m.config.Public > 0
	case agentStreamCapacityTrustedHealth:
		return m.config.Control > 0
	default:
		return false
	}
}

func (m *agentStreamCapacityManager) canGrantLocked(class agentStreamCapacityClass, sessionKey string) bool {
	if m.usedTotal >= m.config.Total ||
		m.totalBySession[sessionKey] >= m.sessionLimitLocked(sessionKey) ||
		m.openingBySession[sessionKey] >= m.config.MaxOpeningPerSession {
		return false
	}
	switch class {
	case agentStreamCapacityPublicPooled:
		return m.usedPublic < m.config.Public &&
			m.usedPooled < m.config.Pooled &&
			m.publicBySession[sessionKey] < m.publicSessionLimitLocked(sessionKey) &&
			m.publicOpeningBySession[sessionKey] < m.publicOpeningLimitPerSessionLocked()
	case agentStreamCapacityPublicOneShot:
		return m.usedPublic < m.config.Public &&
			m.publicBySession[sessionKey] < m.publicSessionLimitLocked(sessionKey) &&
			m.publicOpeningBySession[sessionKey] < m.publicOpeningLimitPerSessionLocked()
	case agentStreamCapacityTrustedHealth:
		return m.usedControl < m.config.Control
	default:
		return false
	}
}

func (m *agentStreamCapacityManager) blockingConstraintLocked(class agentStreamCapacityClass, sessionKey string) error {
	if m.usedTotal >= m.config.Total {
		return errAgentStreamCapacityTotalBudget
	}
	if m.totalBySession[sessionKey] >= m.sessionLimitLocked(sessionKey) {
		return errAgentStreamCapacitySessionBudget
	}
	switch class {
	case agentStreamCapacityPublicPooled:
		if m.usedPublic >= m.config.Public {
			return errAgentStreamCapacityPublicBudget
		}
		if m.publicBySession[sessionKey] >= m.publicSessionLimitLocked(sessionKey) {
			return errAgentStreamCapacitySessionBudget
		}
		if m.openingBySession[sessionKey] >= m.config.MaxOpeningPerSession ||
			m.publicOpeningBySession[sessionKey] >= m.publicOpeningLimitPerSessionLocked() {
			return errAgentStreamCapacitySessionOpeningLimit
		}
		if m.usedPooled >= m.config.Pooled {
			return errAgentStreamCapacityPooledBudget
		}
	case agentStreamCapacityPublicOneShot:
		if m.usedPublic >= m.config.Public {
			return errAgentStreamCapacityPublicBudget
		}
		if m.publicBySession[sessionKey] >= m.publicSessionLimitLocked(sessionKey) {
			return errAgentStreamCapacitySessionBudget
		}
		if m.openingBySession[sessionKey] >= m.config.MaxOpeningPerSession ||
			m.publicOpeningBySession[sessionKey] >= m.publicOpeningLimitPerSessionLocked() {
			return errAgentStreamCapacitySessionOpeningLimit
		}
	case agentStreamCapacityTrustedHealth:
		if m.usedControl >= m.config.Control {
			return errAgentStreamCapacityControlBudget
		}
		if m.openingBySession[sessionKey] >= m.config.MaxOpeningPerSession {
			return errAgentStreamCapacitySessionOpeningLimit
		}
	}
	return errAgentStreamCapacityWaitTurn
}

func newAgentStreamCapacityAcquireError(cause, constraint error, queueKey, sessionKey string) error {
	return &agentStreamCapacityAcquireError{
		Cause:      cause,
		Constraint: constraint,
		QueueKey:   queueKey,
		SessionKey: sessionKey,
	}
}

func (m *agentStreamCapacityManager) grantLocked(
	class agentStreamCapacityClass,
	queueKey string,
	sessionKey string,
) *agentStreamCapacityLease {
	m.nextLeaseID++
	lease := &agentStreamCapacityLease{
		manager:        m,
		id:             m.nextLeaseID,
		class:          class,
		queueKey:       queueKey,
		sessionKey:     sessionKey,
		state:          agentStreamLeaseOpening,
		stateChangedAt: time.Now(),
	}
	m.usedTotal++
	m.totalBySession[sessionKey]++
	switch class {
	case agentStreamCapacityPublicPooled:
		m.usedPublic++
		m.usedPooled++
		m.publicBySession[sessionKey]++
		m.publicOpeningBySession[sessionKey]++
	case agentStreamCapacityPublicOneShot:
		m.usedPublic++
		m.publicBySession[sessionKey]++
		m.publicOpeningBySession[sessionKey]++
	case agentStreamCapacityTrustedHealth:
		m.usedControl++
	}
	m.stateByClass[class][agentStreamLeaseOpening]++
	m.openingBySession[sessionKey]++
	m.activeLeases[lease.id] = lease
	m.granted++
	return lease
}

func (m *agentStreamCapacityManager) enqueueLocked(waiter *agentStreamCapacityWaiter) {
	if len(m.queues[waiter.queueKey]) == 0 {
		m.queueKeys = append(m.queueKeys, waiter.queueKey)
	}
	m.queues[waiter.queueKey] = append(m.queues[waiter.queueKey], waiter)
	m.waiters++
}

func (m *agentStreamCapacityManager) dispatchLocked() {
	for m.waiters > 0 && len(m.queueKeys) > 0 {
		scanned := 0
		progress := false
		for scanned < len(m.queueKeys) {
			if m.nextQueueKey >= len(m.queueKeys) {
				m.nextQueueKey = 0
			}
			index := m.nextQueueKey
			queueKey := m.queueKeys[index]
			queue := m.queues[queueKey]
			if len(queue) == 0 {
				m.removeQueueKeyAtLocked(index)
				progress = true
				break
			}
			for waiterIndex, waiter := range queue {
				if err := waiter.ctx.Err(); err != nil {
					constraint := m.blockingConstraintLocked(waiter.class, waiter.sessionKey)
					m.recordRejectionLocked(constraint)
					m.popQueueEntryLocked(index, waiterIndex, false)
					waiter.status = agentStreamCapacityWaiterCanceled
					waiter.err = newAgentStreamCapacityAcquireError(
						err,
						constraint,
						waiter.queueKey,
						waiter.sessionKey,
					)
					close(waiter.ready)
					progress = true
					break
				}
				if m.canGrantLocked(waiter.class, waiter.sessionKey) {
					m.popQueueEntryLocked(index, waiterIndex, true)
					waiter.lease = m.grantLocked(waiter.class, waiter.queueKey, waiter.sessionKey)
					waiter.status = agentStreamCapacityWaiterGranted
					close(waiter.ready)
					progress = true
					break
				}
			}
			if progress {
				break
			}
			m.nextQueueKey = (index + 1) % len(m.queueKeys)
			scanned++
		}
		if !progress {
			return
		}
	}
}

func (m *agentStreamCapacityManager) popQueueEntryLocked(index int, waiterIndex int, advance bool) {
	queueKey := m.queueKeys[index]
	queue := m.queues[queueKey]
	copy(queue[waiterIndex:], queue[waiterIndex+1:])
	queue[len(queue)-1] = nil
	queue = queue[:len(queue)-1]
	m.waiters--
	if len(queue) == 0 {
		delete(m.queues, queueKey)
		m.removeQueueKeyAtLocked(index)
		return
	}
	m.queues[queueKey] = queue
	if advance {
		m.nextQueueKey = (index + 1) % len(m.queueKeys)
	} else {
		m.nextQueueKey = index
	}
}

func (m *agentStreamCapacityManager) removeQueueKeyAtLocked(index int) {
	copy(m.queueKeys[index:], m.queueKeys[index+1:])
	m.queueKeys[len(m.queueKeys)-1] = ""
	m.queueKeys = m.queueKeys[:len(m.queueKeys)-1]
	if len(m.queueKeys) == 0 {
		m.nextQueueKey = 0
		return
	}
	if index < m.nextQueueKey {
		m.nextQueueKey--
	}
	if m.nextQueueKey >= len(m.queueKeys) {
		m.nextQueueKey = 0
	}
}

func (m *agentStreamCapacityManager) removeWaiterLocked(waiter *agentStreamCapacityWaiter) {
	queue := m.queues[waiter.queueKey]
	for index, candidate := range queue {
		if candidate != waiter {
			continue
		}
		copy(queue[index:], queue[index+1:])
		queue[len(queue)-1] = nil
		queue = queue[:len(queue)-1]
		m.waiters--
		if len(queue) > 0 {
			m.queues[waiter.queueKey] = queue
			return
		}
		delete(m.queues, waiter.queueKey)
		for keyIndex, queueKey := range m.queueKeys {
			if queueKey != waiter.queueKey {
				continue
			}
			m.removeQueueKeyAtLocked(keyIndex)
			return
		}
		return
	}
}

func (l *agentStreamCapacityLease) markLive() bool {
	if l == nil || l.manager == nil {
		return false
	}
	m := l.manager
	m.mu.Lock()
	defer m.mu.Unlock()
	if l.state != agentStreamLeaseOpening || m.activeLeases[l.id] != l {
		return false
	}
	m.stateByClass[l.class][agentStreamLeaseOpening]--
	m.stateByClass[l.class][agentStreamLeaseLive]++
	m.releaseSessionOpeningLocked(l.class, l.sessionKey)
	l.state = agentStreamLeaseLive
	l.stateChangedAt = time.Now()
	m.dispatchLocked()
	return true
}

func (l *agentStreamCapacityLease) markClosing() bool {
	if l == nil || l.manager == nil {
		return false
	}
	m := l.manager
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.activeLeases[l.id] != l {
		return false
	}
	switch l.state {
	case agentStreamLeaseOpening:
		m.stateByClass[l.class][agentStreamLeaseOpening]--
		m.releaseSessionOpeningLocked(l.class, l.sessionKey)
	case agentStreamLeaseLive:
		m.stateByClass[l.class][agentStreamLeaseLive]--
	default:
		return false
	}
	m.stateByClass[l.class][agentStreamLeaseClosing]++
	l.state = agentStreamLeaseClosing
	l.stateChangedAt = time.Now()
	m.dispatchLocked()
	return true
}

func (l *agentStreamCapacityLease) release() bool {
	if l == nil || l.manager == nil {
		return false
	}
	m := l.manager
	m.mu.Lock()
	defer m.mu.Unlock()
	if l.state == agentStreamLeaseReleased || m.activeLeases[l.id] != l {
		return false
	}
	if l.state == agentStreamLeaseOpening {
		m.releaseSessionOpeningLocked(l.class, l.sessionKey)
	}
	m.stateByClass[l.class][l.state]--
	m.usedTotal--
	m.releaseSessionTotalLocked(l.sessionKey)
	switch l.class {
	case agentStreamCapacityPublicPooled:
		m.usedPublic--
		m.usedPooled--
		m.releaseSessionPublicLocked(l.sessionKey)
	case agentStreamCapacityPublicOneShot:
		m.usedPublic--
		m.releaseSessionPublicLocked(l.sessionKey)
	case agentStreamCapacityTrustedHealth:
		m.usedControl--
	}
	delete(m.activeLeases, l.id)
	l.state = agentStreamLeaseReleased
	m.released++
	m.dispatchLocked()
	return true
}

func (l *agentStreamCapacityLease) currentState() agentStreamLeaseState {
	if l == nil || l.manager == nil {
		return agentStreamLeaseReleased
	}
	l.manager.mu.Lock()
	defer l.manager.mu.Unlock()
	return l.state
}

func (m *agentStreamCapacityManager) releaseSessionOpeningLocked(class agentStreamCapacityClass, sessionKey string) {
	openings := m.openingBySession[sessionKey]
	if openings <= 1 {
		delete(m.openingBySession, sessionKey)
	} else {
		m.openingBySession[sessionKey] = openings - 1
	}
	if class == agentStreamCapacityTrustedHealth {
		return
	}
	publicOpenings := m.publicOpeningBySession[sessionKey]
	if publicOpenings <= 1 {
		delete(m.publicOpeningBySession, sessionKey)
		return
	}
	m.publicOpeningBySession[sessionKey] = publicOpenings - 1
}

func (m *agentStreamCapacityManager) recordRejectionLocked(constraint error) {
	if constraint == nil {
		return
	}
	m.admissionMissesByConstraint[agentStreamCapacityConstraintName(constraint)]++
}

func (m *agentStreamCapacityManager) releaseSessionPublicLocked(sessionKey string) {
	usage := m.publicBySession[sessionKey]
	if usage <= 1 {
		delete(m.publicBySession, sessionKey)
		return
	}
	m.publicBySession[sessionKey] = usage - 1
}

func (m *agentStreamCapacityManager) releaseSessionTotalLocked(sessionKey string) {
	usage := m.totalBySession[sessionKey]
	if usage <= 1 {
		delete(m.totalBySession, sessionKey)
		return
	}
	m.totalBySession[sessionKey] = usage - 1
}

func (m *agentStreamCapacityManager) sessionLimitLocked(sessionKey string) int {
	if limit := m.sessionLimits[sessionKey]; limit > 0 && limit < m.config.Total {
		return limit
	}
	return m.config.Total
}

func (m *agentStreamCapacityManager) publicSessionLimitLocked(sessionKey string) int {
	limit := m.config.Public
	if m.config.ReservedPublicForOtherSessions <= 0 || len(m.registeredSessions) <= 1 {
		return limit
	}
	if _, registered := m.registeredSessions[sessionKey]; !registered {
		return limit
	}
	return limit - m.config.ReservedPublicForOtherSessions
}

// publicOpeningLimitPerSessionLocked protects trusted health from a public
// opening burst when the configured stream total exceeds Yamux's finite open
// backlog. At or below that backlog the lifetime public/control partition
// already provides the same guarantee.
func (m *agentStreamCapacityManager) publicOpeningLimitPerSessionLocked() int {
	limit := m.config.MaxOpeningPerSession
	if limit >= m.config.Total || m.config.Control <= 0 || limit <= 1 {
		return limit
	}
	reserve := m.config.Control
	if reserve >= limit {
		reserve = limit - 1
	}
	return limit - reserve
}

func (m *agentStreamCapacityManager) registerSession(sessionKey string) {
	if m == nil {
		return
	}
	m.registerSessionWithLimit(sessionKey, m.config.Total)
}

func (m *agentStreamCapacityManager) registerSessionWithLimit(sessionKey string, limit int) {
	if m == nil || sessionKey == "" {
		return
	}
	if limit < 1 {
		limit = 1
	}
	if limit > m.config.Total {
		limit = m.config.Total
	}
	m.mu.Lock()
	m.registeredSessions[sessionKey] = struct{}{}
	m.sessionLimits[sessionKey] = limit
	m.dispatchLocked()
	m.mu.Unlock()
}

func (m *agentStreamCapacityManager) unregisterSession(sessionKey string) {
	if m == nil || sessionKey == "" {
		return
	}
	m.mu.Lock()
	delete(m.registeredSessions, sessionKey)
	delete(m.sessionLimits, sessionKey)
	m.dispatchLocked()
	m.mu.Unlock()
}

func (m *agentStreamCapacityManager) snapshot() agentStreamCapacitySnapshot {
	if m == nil {
		return agentStreamCapacitySnapshot{}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.snapshotLocked()
}

func (m *agentStreamCapacityManager) snapshotLocked() agentStreamCapacitySnapshot {
	snapshot := agentStreamCapacitySnapshot{
		Total:                       agentStreamCapacityBudgetSnapshot{Capacity: m.config.Total, InUse: m.usedTotal},
		Public:                      agentStreamCapacityBudgetSnapshot{Capacity: m.config.Public, InUse: m.usedPublic},
		Pooled:                      agentStreamCapacityBudgetSnapshot{Capacity: m.config.Pooled, InUse: m.usedPooled},
		Control:                     agentStreamCapacityBudgetSnapshot{Capacity: m.config.Control, InUse: m.usedControl},
		StatesByClass:               make(map[agentStreamCapacityClass]agentStreamCapacityStateSnapshot, agentStreamCapacityClassCount),
		WaitersByClass:              make(map[agentStreamCapacityClass]int, agentStreamCapacityClassCount),
		WaitersByConstraint:         make(map[string]int),
		OpeningBySession:            make(map[string]int, len(m.openingBySession)),
		PublicOpeningBySession:      make(map[string]int, len(m.publicOpeningBySession)),
		PublicBySession:             make(map[string]int, len(m.publicBySession)),
		TotalBySession:              make(map[string]int, len(m.totalBySession)),
		SessionLimits:               make(map[string]int, len(m.sessionLimits)),
		RegisteredSessions:          len(m.registeredSessions),
		WaitersByKey:                make(map[string]int, len(m.queues)),
		Waiters:                     m.waiters,
		ActiveLeases:                len(m.activeLeases),
		Granted:                     m.granted,
		Released:                    m.released,
		AdmissionMissesByConstraint: make(map[string]uint64, len(m.admissionMissesByConstraint)),
	}
	if m.config.Public > 0 {
		snapshot.ContendedSessionPublicLimit = m.config.Public - m.config.ReservedPublicForOtherSessions
	}
	for class := agentStreamCapacityClass(0); class < agentStreamCapacityClassCount; class++ {
		states := agentStreamCapacityStateSnapshot{
			Opening: m.stateByClass[class][agentStreamLeaseOpening],
			Live:    m.stateByClass[class][agentStreamLeaseLive],
			Closing: m.stateByClass[class][agentStreamLeaseClosing],
		}
		snapshot.StatesByClass[class] = states
		snapshot.States.Opening += states.Opening
		snapshot.States.Live += states.Live
		snapshot.States.Closing += states.Closing
	}
	for sessionKey, openings := range m.openingBySession {
		snapshot.OpeningBySession[sessionKey] = openings
	}
	for sessionKey, openings := range m.publicOpeningBySession {
		snapshot.PublicOpeningBySession[sessionKey] = openings
	}
	for sessionKey, usage := range m.publicBySession {
		snapshot.PublicBySession[sessionKey] = usage
		if usage > snapshot.MaxSessionPublicInUse {
			snapshot.MaxSessionPublicInUse = usage
		}
	}
	for sessionKey, usage := range m.totalBySession {
		snapshot.TotalBySession[sessionKey] = usage
	}
	for sessionKey, limit := range m.sessionLimits {
		snapshot.SessionLimits[sessionKey] = limit
	}
	for constraint, count := range m.admissionMissesByConstraint {
		snapshot.AdmissionMissesByConstraint[constraint] = count
	}
	now := time.Now()
	for _, lease := range m.activeLeases {
		if lease == nil || lease.state != agentStreamLeaseClosing || lease.stateChangedAt.IsZero() {
			continue
		}
		age := now.Sub(lease.stateChangedAt).Milliseconds()
		if age > snapshot.OldestClosingAgeMillis {
			snapshot.OldestClosingAgeMillis = age
		}
	}
	for queueKey, queue := range m.queues {
		snapshot.WaitersByKey[queueKey] = len(queue)
		for _, waiter := range queue {
			snapshot.WaitersByClass[waiter.class]++
			constraint := m.blockingConstraintLocked(waiter.class, waiter.sessionKey)
			snapshot.WaitersByConstraint[agentStreamCapacityConstraintName(constraint)]++
		}
	}
	return snapshot
}

func (m *agentStreamCapacityManager) validateInvariants() error {
	if m == nil {
		return errors.New("agent stream capacity manager is nil")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.validateInvariantsLocked()
}

func (m *agentStreamCapacityManager) validateInvariantsLocked() error {
	if err := validateAgentStreamCapacityConfig(m.config); err != nil {
		return err
	}
	if m.usedTotal < 0 || m.usedTotal > m.config.Total {
		return fmt.Errorf("total usage %d exceeds capacity %d", m.usedTotal, m.config.Total)
	}
	if m.usedPublic < 0 || m.usedPublic > m.config.Public {
		return fmt.Errorf("public usage %d exceeds capacity %d", m.usedPublic, m.config.Public)
	}
	if m.usedPooled < 0 || m.usedPooled > m.config.Pooled {
		return fmt.Errorf("pooled usage %d exceeds capacity %d", m.usedPooled, m.config.Pooled)
	}
	if m.usedControl < 0 || m.usedControl > m.config.Control {
		return fmt.Errorf("control usage %d exceeds capacity %d", m.usedControl, m.config.Control)
	}
	if m.usedTotal != m.usedPublic+m.usedControl {
		return fmt.Errorf("total usage %d != public %d + control %d", m.usedTotal, m.usedPublic, m.usedControl)
	}

	var statesByClass [agentStreamCapacityClassCount]int
	var totalStates int
	var totalOpenings int
	for class := agentStreamCapacityClass(0); class < agentStreamCapacityClassCount; class++ {
		for state := agentStreamLeaseState(0); state < agentStreamLeaseReleased; state++ {
			count := m.stateByClass[class][state]
			if count < 0 {
				return fmt.Errorf("negative state count for class %d state %d", class, state)
			}
			statesByClass[class] += count
			totalStates += count
			if state == agentStreamLeaseOpening {
				totalOpenings += count
			}
		}
	}
	if totalStates != m.usedTotal || totalStates != len(m.activeLeases) {
		return fmt.Errorf("active state count %d, usage %d, leases %d differ", totalStates, m.usedTotal, len(m.activeLeases))
	}
	if statesByClass[agentStreamCapacityPublicPooled] != m.usedPooled {
		return fmt.Errorf("pooled state count %d != usage %d", statesByClass[agentStreamCapacityPublicPooled], m.usedPooled)
	}
	if statesByClass[agentStreamCapacityPublicPooled]+statesByClass[agentStreamCapacityPublicOneShot] != m.usedPublic {
		return fmt.Errorf("public class state counts do not equal usage %d", m.usedPublic)
	}
	if statesByClass[agentStreamCapacityTrustedHealth] != m.usedControl {
		return fmt.Errorf("control class state count %d != usage %d", statesByClass[agentStreamCapacityTrustedHealth], m.usedControl)
	}

	openingSum := 0
	for sessionKey, openings := range m.openingBySession {
		if sessionKey == "" || openings < 1 || openings > m.config.MaxOpeningPerSession {
			return fmt.Errorf("invalid opening count %d for session %q", openings, sessionKey)
		}
		openingSum += openings
	}
	if openingSum != totalOpenings {
		return fmt.Errorf("session opening count %d != state count %d", openingSum, totalOpenings)
	}
	publicOpeningSum := 0
	publicOpeningLimit := m.publicOpeningLimitPerSessionLocked()
	for sessionKey, openings := range m.publicOpeningBySession {
		if sessionKey == "" || openings < 1 || openings > publicOpeningLimit || openings > m.openingBySession[sessionKey] {
			return fmt.Errorf("invalid public opening count %d for session %q", openings, sessionKey)
		}
		publicOpeningSum += openings
	}
	publicOpeningStates := m.stateByClass[agentStreamCapacityPublicPooled][agentStreamLeaseOpening] +
		m.stateByClass[agentStreamCapacityPublicOneShot][agentStreamLeaseOpening]
	if publicOpeningSum != publicOpeningStates {
		return fmt.Errorf("session public opening count %d != state count %d", publicOpeningSum, publicOpeningStates)
	}
	publicBySessionSum := 0
	for sessionKey, usage := range m.publicBySession {
		if sessionKey == "" || usage < 1 || usage > m.config.Public {
			return fmt.Errorf("invalid public count %d for session %q", usage, sessionKey)
		}
		publicBySessionSum += usage
	}
	if publicBySessionSum != m.usedPublic {
		return fmt.Errorf("session public count %d != public usage %d", publicBySessionSum, m.usedPublic)
	}
	totalBySessionSum := 0
	for sessionKey, usage := range m.totalBySession {
		if sessionKey == "" || usage < 1 || usage > m.config.Total {
			return fmt.Errorf("invalid total count %d for session %q", usage, sessionKey)
		}
		if _, registered := m.registeredSessions[sessionKey]; registered && usage > m.sessionLimitLocked(sessionKey) {
			return fmt.Errorf("session %q usage %d exceeds negotiated limit %d", sessionKey, usage, m.sessionLimitLocked(sessionKey))
		}
		totalBySessionSum += usage
	}
	if totalBySessionSum != m.usedTotal {
		return fmt.Errorf("session total count %d != total usage %d", totalBySessionSum, m.usedTotal)
	}
	for sessionKey, limit := range m.sessionLimits {
		if sessionKey == "" || limit < 1 || limit > m.config.Total {
			return fmt.Errorf("invalid negotiated limit %d for session %q", limit, sessionKey)
		}
		if _, registered := m.registeredSessions[sessionKey]; !registered {
			return fmt.Errorf("negotiated limit exists for unregistered session %q", sessionKey)
		}
	}

	var leaseStates [agentStreamCapacityClassCount][agentStreamLeaseReleased]int
	for id, lease := range m.activeLeases {
		if lease == nil || lease.manager != m || lease.id != id {
			return fmt.Errorf("invalid active lease %d", id)
		}
		if lease.class >= agentStreamCapacityClassCount || lease.state >= agentStreamLeaseReleased {
			return fmt.Errorf("invalid active lease %d class/state", id)
		}
		leaseStates[lease.class][lease.state]++
	}
	if leaseStates != m.stateByClass {
		return errors.New("active lease states differ from state counters")
	}
	if m.granted-m.released != uint64(len(m.activeLeases)) {
		return fmt.Errorf("granted %d - released %d != active leases %d", m.granted, m.released, len(m.activeLeases))
	}

	queued := 0
	queuedControl := 0
	seenKeys := make(map[string]struct{}, len(m.queueKeys))
	for _, queueKey := range m.queueKeys {
		if queueKey == "" {
			return errors.New("empty queue key in round-robin order")
		}
		if _, duplicate := seenKeys[queueKey]; duplicate {
			return fmt.Errorf("duplicate queue key %q", queueKey)
		}
		seenKeys[queueKey] = struct{}{}
		queue := m.queues[queueKey]
		if len(queue) == 0 || len(queue) > m.config.MaxWaitersPerKey {
			return fmt.Errorf("invalid queue length %d for key %q", len(queue), queueKey)
		}
		for _, waiter := range queue {
			if waiter == nil || waiter.queueKey != queueKey || waiter.status != agentStreamCapacityWaiterQueued {
				return fmt.Errorf("invalid waiter in queue %q", queueKey)
			}
			if waiter.class == agentStreamCapacityTrustedHealth {
				queuedControl++
			}
		}
		queued += len(queue)
	}
	if len(seenKeys) != len(m.queues) {
		return errors.New("queue map and round-robin keys differ")
	}
	controlWaiterLimit := m.config.Control
	if controlWaiterLimit < 1 {
		controlWaiterLimit = 1
	}
	queuedPublic := queued - queuedControl
	if queued != m.waiters || queuedPublic > m.config.MaxWaiters || queuedControl > controlWaiterLimit {
		return fmt.Errorf("queued waiter count %d != tracked %d or exceeds max", queued, m.waiters)
	}
	if len(m.queueKeys) == 0 && m.nextQueueKey != 0 {
		return fmt.Errorf("queue cursor %d is nonzero with no queues", m.nextQueueKey)
	}
	if len(m.queueKeys) > 0 && (m.nextQueueKey < 0 || m.nextQueueKey >= len(m.queueKeys)) {
		return fmt.Errorf("queue cursor %d outside %d keys", m.nextQueueKey, len(m.queueKeys))
	}
	return nil
}
