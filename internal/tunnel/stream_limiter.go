package tunnel

import "sync"

type StreamLimiter struct {
	slots chan struct{}
}

func NewStreamLimiter(limit int64) (*StreamLimiter, error) {
	limit, err := NormalizeMaxConcurrentAgentRequests(limit)
	if err != nil {
		return nil, err
	}
	return &StreamLimiter{slots: make(chan struct{}, int(limit))}, nil
}

func (l *StreamLimiter) TryAcquire() (func(), bool) {
	if l == nil || l.slots == nil {
		return func() {}, true
	}
	select {
	case l.slots <- struct{}{}:
		var once sync.Once
		return func() {
			once.Do(func() { <-l.slots })
		}, true
	default:
		return nil, false
	}
}

func (l *StreamLimiter) InUse() int {
	if l == nil {
		return 0
	}
	return len(l.slots)
}

func (l *StreamLimiter) Capacity() int {
	if l == nil {
		return 0
	}
	return cap(l.slots)
}
