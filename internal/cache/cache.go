package cache

import (
	"container/list"
	"context"
	"errors"
	"sync"
	"time"
)

// Config controls cache capacity and maintenance behavior.
//
// Correctness-first defaults:
//   - MaxEntries <= 0 means "unbounded" (no LRU eviction)
//   - CleanupInterval <= 0 disables background cleanup (lazy expiration still works)
//
// Background cleanup exists to prevent memory growth when keys are written once and never read again.
// Lazy expiration alone can leave dead entries in memory indefinitely.
type Config struct {
	MaxEntries      int
	CleanupInterval time.Duration
}

// Cache is a concurrency-safe in-memory key–value cache with TTL and LRU eviction.
//
// The core design is intentionally explicit and "mechanical":
// a map gives O(1) key lookup, and a doubly-linked list maintains recency ordering.
//
// Ownership model:
// Cache owns its internal goroutines. Call Close to stop them.
type Cache struct {
	mu sync.RWMutex

	maxEntries int
	items      map[string]*list.Element
	lru        *list.List // Front = most recently used (MRU), Back = least recently used (LRU)

	// Goroutine ownership.
	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup

	cleanupEvery time.Duration
	closed       bool
}

// entry is the value stored in the LRU list elements.
// We keep the key here because eviction starts from list nodes.
//
// ExpiresAt is optional: hasExpiry=false means "never expires".
type entry struct {
	key       string
	value     []byte
	expiresAt time.Time
	hasExpiry bool
}

var ErrClosed = errors.New("cache is closed")

// New constructs a cache and starts background maintenance (if enabled).
//
// New never returns a nil Cache.
func New(cfg Config) *Cache {
	ctx, cancel := context.WithCancel(context.Background())

	c := &Cache{
		maxEntries:   cfg.MaxEntries,
		items:        make(map[string]*list.Element),
		lru:          list.New(),
		ctx:          ctx,
		cancel:       cancel,
		cleanupEvery: cfg.CleanupInterval,
	}

	if c.cleanupEvery > 0 {
		c.wg.Add(1)
		go c.expiryLoop()
	}

	return c
}

// Close stops background goroutines and prevents further mutation.
//
// Close is safe to call multiple times.
func (c *Cache) Close() error {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil
	}
	c.closed = true
	cancel := c.cancel
	c.mu.Unlock()

	// Cancel outside the lock so shutdown doesn't block readers/writers.
	cancel()
	c.wg.Wait()
	return nil
}

// Set writes/overwrites a key.
//
// ttl semantics:
//   - ttl <= 0 means "no expiration" (common cache API convention)
//
// Complexity:
//   - O(1) to locate/insert
//   - O(1) eviction per removed entry
func (c *Cache) Set(key string, value []byte, ttl time.Duration) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.closed {
		return ErrClosed
	}

	now := time.Now()

	// Compute expiry once. Using hasExpiry avoids comparing against the zero time.
	var expiresAt time.Time
	hasExpiry := ttl > 0
	if hasExpiry {
		expiresAt = now.Add(ttl)
	}

	if el, ok := c.items[key]; ok {
		e := el.Value.(*entry)
		e.value = cloneBytes(value)
		e.hasExpiry = hasExpiry
		e.expiresAt = expiresAt

		// Updating counts as use; move to MRU.
		c.lru.MoveToFront(el)
		c.evictIfNeededLocked(now)
		return nil
	}

	e := &entry{
		key:       key,
		value:     cloneBytes(value),
		hasExpiry: hasExpiry,
		expiresAt: expiresAt,
	}

	el := c.lru.PushFront(e)
	c.items[key] = el

	c.evictIfNeededLocked(now)
	return nil
}

// Get reads a key.
//
// It performs lazy TTL expiration: expired keys are removed on access.
//
// Concurrency note:
// Reads ideally take an RLock, but LRU updates are writes.
// We use an "optimistic read then confirm under write lock" pattern:
//  1. RLock to find entry and check expiry.
//  2. If present and not expired, release RLock.
//  3. Lock and re-check, then move node to front and copy value.
//
// This keeps the uncontended fast-path mostly read-locked, while still being correct.
func (c *Cache) Get(key string) ([]byte, bool) {
	now := time.Now()

	c.mu.RLock()
	el, ok := c.items[key]
	if !ok {
		c.mu.RUnlock()
		return nil, false
	}

	e := el.Value.(*entry)
	if e.hasExpiry && !e.expiresAt.After(now) {
		// Expired: must upgrade to write lock to delete.
		c.mu.RUnlock()
		c.mu.Lock()
		defer c.mu.Unlock()
		c.deleteIfExpiredLocked(key, now)
		return nil, false
	}

	// Snapshot what we can under RLock.
	// We must NOT return e.value directly because callers could mutate it.
	// Also, we still need to move the LRU node, which requires a write lock.
	c.mu.RUnlock()

	c.mu.Lock()
	defer c.mu.Unlock()

	// Re-check because the key could have been deleted/evicted between locks.
	el2, ok := c.items[key]
	if !ok {
		return nil, false
	}
	e2 := el2.Value.(*entry)
	if e2.hasExpiry && !e2.expiresAt.After(now) {
		c.deleteLocked(key)
		return nil, false
	}

	c.lru.MoveToFront(el2)
	return cloneBytes(e2.value), true
}

// Delete removes a key if present.
func (c *Cache) Delete(key string) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.closed {
		return ErrClosed
	}

	c.deleteLocked(key)
	return nil
}

// Len returns the number of currently stored entries.
//
// Note: Len includes entries that have expired but haven't been cleaned up yet.
// Lazy expiration removes them when accessed; the cleanup loop removes them over time.
func (c *Cache) Len() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return len(c.items)
}

// Keys returns keys in MRU -> LRU order.
//
// This is a debug/teaching helper used by the demo.
func (c *Cache) Keys() []string {
	c.mu.RLock()
	defer c.mu.RUnlock()

	out := make([]string, 0, c.lru.Len())
	for el := c.lru.Front(); el != nil; el = el.Next() {
		out = append(out, el.Value.(*entry).key)
	}
	return out
}

func (c *Cache) evictIfNeededLocked(now time.Time) {
	if c.maxEntries <= 0 {
		return
	}

	// Prefer to reclaim expired entries first if we're under pressure.
	// This keeps LRU semantics for live keys while treating expired keys as already dead.
	c.deleteExpiredLocked(now)

	for len(c.items) > c.maxEntries {
		el := c.lru.Back()
		if el == nil {
			return
		}
		e := el.Value.(*entry)
		c.deleteLocked(e.key)
	}
}

func (c *Cache) deleteLocked(key string) {
	el, ok := c.items[key]
	if !ok {
		return
	}
	delete(c.items, key)
	c.lru.Remove(el)
}

func (c *Cache) deleteIfExpiredLocked(key string, now time.Time) bool {
	el, ok := c.items[key]
	if !ok {
		return false
	}
	e := el.Value.(*entry)
	if e.hasExpiry && !e.expiresAt.After(now) {
		c.deleteLocked(key)
		return true
	}
	return false
}

// deleteExpiredLocked removes all expired keys.
//
// This is O(n) and intentionally simple. More complex designs can track expirations
// in a min-heap or timing wheel, but that trades simplicity for performance.
func (c *Cache) deleteExpiredLocked(now time.Time) int {
	removed := 0
	for key, el := range c.items {
		e := el.Value.(*entry)
		if e.hasExpiry && !e.expiresAt.After(now) {
			delete(c.items, key)
			c.lru.Remove(el)
			removed++
		}
	}
	return removed
}

func cloneBytes(b []byte) []byte {
	if b == nil {
		return nil
	}
	out := make([]byte, len(b))
	copy(out, b)
	return out
}
