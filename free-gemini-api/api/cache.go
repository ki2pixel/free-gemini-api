package api

import (
	"container/list"
	"sync"
	"time"
)

// LRUCache is a thread-safe, generically typed least-recently-used cache with
// an optional per-entry time-to-live and an eviction callback.
//
// It replaces the unbounded sync.Map instances that previously backed user
// sessions and worker affinity. Both are attacker-influenced keys, so an
// unbounded map was a memory-exhaustion vector (F-13).
type LRUCache[K comparable, V any] struct {
	mu        sync.Mutex
	capacity  int
	ttl       time.Duration
	items     map[K]*list.Element
	order     *list.List
	onEvict   func(key K, value V)
	closed    bool
	stop      chan struct{}
	closeOnce sync.Once
}

type lruEntry[K comparable, V any] struct {
	key       K
	value     V
	expiresAt time.Time
}

// NewLRUCache creates a cache holding at most capacity entries. A ttl <= 0
// disables expiration. onEvict, when non-nil, is invoked outside the cache
// lock for every entry removed by eviction, expiration, Delete or Close.
func NewLRUCache[K comparable, V any](capacity int, ttl time.Duration, onEvict func(key K, value V)) *LRUCache[K, V] {
	if capacity <= 0 {
		capacity = 1
	}
	c := &LRUCache[K, V]{
		capacity: capacity,
		ttl:      ttl,
		items:    make(map[K]*list.Element),
		order:    list.New(),
		onEvict:  onEvict,
		stop:     make(chan struct{}),
	}
	if ttl > 0 {
		go c.janitor()
	}
	return c
}

func (c *LRUCache[K, V]) janitor() {
	interval := c.ttl / 2
	if interval < time.Second {
		interval = time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			c.PurgeExpired()
		case <-c.stop:
			return
		}
	}
}

func (c *LRUCache[K, V]) expired(entry *lruEntry[K, V]) bool {
	return c.ttl > 0 && !entry.expiresAt.IsZero() && time.Now().After(entry.expiresAt)
}

func (c *LRUCache[K, V]) removeElementLocked(el *list.Element) (K, V, bool) {
	entry, ok := el.Value.(*lruEntry[K, V])
	if !ok {
		var zeroK K
		var zeroV V
		c.order.Remove(el)
		return zeroK, zeroV, false
	}
	delete(c.items, entry.key)
	c.order.Remove(el)
	return entry.key, entry.value, true
}

func (c *LRUCache[K, V]) notify(keys []K, values []V) {
	if c.onEvict == nil {
		return
	}
	for i := range keys {
		c.onEvict(keys[i], values[i])
	}
}

// Get returns the value for key and marks it as recently used. Expired
// entries are removed and reported to the eviction callback.
func (c *LRUCache[K, V]) Get(key K) (V, bool) {
	var zero V

	c.mu.Lock()
	el, ok := c.items[key]
	if !ok {
		c.mu.Unlock()
		return zero, false
	}
	entry := el.Value.(*lruEntry[K, V])
	if c.expired(entry) {
		k, v, removed := c.removeElementLocked(el)
		c.mu.Unlock()
		if removed {
			c.notify([]K{k}, []V{v})
		}
		return zero, false
	}
	c.order.MoveToFront(el)
	value := entry.value
	c.mu.Unlock()
	return value, true
}

// Set inserts or updates key. When the cache is at capacity the least recently
// used entry is evicted.
func (c *LRUCache[K, V]) Set(key K, value V) {
	var evictedKeys []K
	var evictedValues []V

	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return
	}
	if el, ok := c.items[key]; ok {
		entry := el.Value.(*lruEntry[K, V])
		entry.value = value
		if c.ttl > 0 {
			entry.expiresAt = time.Now().Add(c.ttl)
		}
		c.order.MoveToFront(el)
		c.mu.Unlock()
		return
	}

	entry := &lruEntry[K, V]{key: key, value: value}
	if c.ttl > 0 {
		entry.expiresAt = time.Now().Add(c.ttl)
	}
	c.items[key] = c.order.PushFront(entry)

	for len(c.items) > c.capacity {
		oldest := c.order.Back()
		if oldest == nil {
			break
		}
		k, v, removed := c.removeElementLocked(oldest)
		if removed {
			evictedKeys = append(evictedKeys, k)
			evictedValues = append(evictedValues, v)
		}
	}
	c.mu.Unlock()

	c.notify(evictedKeys, evictedValues)
}

// Delete removes key and reports the removed value to the callback.
func (c *LRUCache[K, V]) Delete(key K) {
	c.mu.Lock()
	el, ok := c.items[key]
	if !ok {
		c.mu.Unlock()
		return
	}
	k, v, removed := c.removeElementLocked(el)
	c.mu.Unlock()
	if removed {
		c.notify([]K{k}, []V{v})
	}
}

// Len returns the number of entries currently stored (including entries whose
// TTL elapsed but which have not been touched or purged yet).
func (c *LRUCache[K, V]) Len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.items)
}

// PurgeExpired removes every entry whose TTL elapsed and returns the number of
// removed entries.
func (c *LRUCache[K, V]) PurgeExpired() int {
	if c.ttl <= 0 {
		return 0
	}

	var keys []K
	var values []V

	c.mu.Lock()
	for el := c.order.Back(); el != nil; {
		prev := el.Prev()
		entry := el.Value.(*lruEntry[K, V])
		if c.expired(entry) {
			k, v, removed := c.removeElementLocked(el)
			if removed {
				keys = append(keys, k)
				values = append(values, v)
			}
		}
		el = prev
	}
	c.mu.Unlock()

	c.notify(keys, values)
	return len(keys)
}

// Range calls fn for every live entry. Iteration stops when fn returns false.
// The callback runs without the cache lock, so it may call cache methods.
func (c *LRUCache[K, V]) Range(fn func(key K, value V) bool) {
	type pair struct {
		key   K
		value V
	}

	var snapshot []pair
	var expiredKeys []K
	var expiredValues []V

	c.mu.Lock()
	for el := c.order.Front(); el != nil; el = el.Next() {
		entry := el.Value.(*lruEntry[K, V])
		if c.expired(entry) {
			expiredKeys = append(expiredKeys, entry.key)
			expiredValues = append(expiredValues, entry.value)
			continue
		}
		snapshot = append(snapshot, pair{key: entry.key, value: entry.value})
	}
	for _, key := range expiredKeys {
		if el, ok := c.items[key]; ok {
			c.removeElementLocked(el)
		}
	}
	c.mu.Unlock()

	c.notify(expiredKeys, expiredValues)

	for _, item := range snapshot {
		if !fn(item.key, item.value) {
			return
		}
	}
}

// Close stops the janitor, removes all entries and invokes the eviction
// callback for each one. It is safe to call more than once.
func (c *LRUCache[K, V]) Close() {
	var keys []K
	var values []V

	c.closeOnce.Do(func() {
		close(c.stop)
		c.mu.Lock()
		c.closed = true
		for el := c.order.Back(); el != nil; {
			prev := el.Prev()
			k, v, removed := c.removeElementLocked(el)
			if removed {
				keys = append(keys, k)
				values = append(values, v)
			}
			el = prev
		}
		c.mu.Unlock()
	})

	c.notify(keys, values)
}
