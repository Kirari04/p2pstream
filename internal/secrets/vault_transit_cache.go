package secrets

import (
	"container/list"
	"context"
	"sync"
	"time"
)

type unwrappedDEKCacheKey struct {
	keyID   string
	wrapped string
	aadHash [32]byte
}

type unwrappedDEKCacheEntry struct {
	key       unwrappedDEKCacheKey
	value     []byte
	expiresAt time.Time
}

type unwrapCall struct {
	done chan struct{}
	err  error
}

type vaultTransitDEKCache struct {
	mu         sync.Mutex
	maxEntries int
	ttl        time.Duration
	now        func() time.Time
	entries    map[unwrappedDEKCacheKey]*list.Element
	order      *list.List
	inflight   map[unwrappedDEKCacheKey]*unwrapCall
}

func newVaultTransitDEKCache(maxEntries int, ttl time.Duration, now func() time.Time) *vaultTransitDEKCache {
	if maxEntries <= 0 || ttl <= 0 {
		return nil
	}
	if now == nil {
		now = time.Now
	}
	return &vaultTransitDEKCache{
		maxEntries: maxEntries,
		ttl:        ttl,
		now:        now,
		entries:    make(map[unwrappedDEKCacheKey]*list.Element),
		order:      list.New(),
		inflight:   make(map[unwrappedDEKCacheKey]*unwrapCall),
	}
}

func (c *vaultTransitDEKCache) getOrUnwrap(ctx context.Context, key unwrappedDEKCacheKey, unwrap func() ([]byte, error)) ([]byte, error) {
	if c == nil {
		return unwrap()
	}
	if ctx == nil {
		ctx = context.Background()
	}
	for {
		c.mu.Lock()
		if value, ok := c.getLocked(key, c.now()); ok {
			c.mu.Unlock()
			return value, nil
		}
		if call := c.inflight[key]; call != nil {
			c.mu.Unlock()
			select {
			case <-call.done:
				if call.err != nil {
					return nil, call.err
				}
				continue
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		call := &unwrapCall{done: make(chan struct{})}
		c.inflight[key] = call
		c.mu.Unlock()

		value, err := unwrap()

		c.mu.Lock()
		delete(c.inflight, key)
		call.err = err
		if err == nil {
			c.setLocked(key, value, c.now())
		}
		close(call.done)
		c.mu.Unlock()

		if err != nil {
			return nil, err
		}
		out := cloneBytes(value)
		zeroBytes(value)
		return out, nil
	}
}

func (c *vaultTransitDEKCache) invalidateWrapped(wrapped string) {
	if c == nil || wrapped == "" {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	for key, elem := range c.entries {
		if key.wrapped == wrapped {
			c.removeElementLocked(elem)
		}
	}
}

func (c *vaultTransitDEKCache) getLocked(key unwrappedDEKCacheKey, now time.Time) ([]byte, bool) {
	elem := c.entries[key]
	if elem == nil {
		return nil, false
	}
	entry := elem.Value.(*unwrappedDEKCacheEntry)
	if !now.Before(entry.expiresAt) {
		c.removeElementLocked(elem)
		return nil, false
	}
	c.order.MoveToFront(elem)
	return cloneBytes(entry.value), true
}

func (c *vaultTransitDEKCache) setLocked(key unwrappedDEKCacheKey, value []byte, now time.Time) {
	c.removeExpiredLocked(now)
	if elem := c.entries[key]; elem != nil {
		entry := elem.Value.(*unwrappedDEKCacheEntry)
		zeroBytes(entry.value)
		entry.value = cloneBytes(value)
		entry.expiresAt = now.Add(c.ttl)
		c.order.MoveToFront(elem)
		return
	}
	entry := &unwrappedDEKCacheEntry{
		key:       key,
		value:     cloneBytes(value),
		expiresAt: now.Add(c.ttl),
	}
	elem := c.order.PushFront(entry)
	c.entries[key] = elem
	for len(c.entries) > c.maxEntries {
		c.removeElementLocked(c.order.Back())
	}
}

func (c *vaultTransitDEKCache) removeExpiredLocked(now time.Time) {
	for elem := c.order.Back(); elem != nil; {
		prev := elem.Prev()
		entry := elem.Value.(*unwrappedDEKCacheEntry)
		if !now.Before(entry.expiresAt) {
			c.removeElementLocked(elem)
		}
		elem = prev
	}
}

func (c *vaultTransitDEKCache) removeElementLocked(elem *list.Element) {
	if elem == nil {
		return
	}
	entry := elem.Value.(*unwrappedDEKCacheEntry)
	delete(c.entries, entry.key)
	zeroBytes(entry.value)
	c.order.Remove(elem)
}
