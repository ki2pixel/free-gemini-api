package api

import (
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestLRUCacheCapacityAndEvictionOrder(t *testing.T) {
	var evicted []string
	cache := NewLRUCache(2, 0, func(key string, value int) {
		evicted = append(evicted, fmt.Sprintf("%s=%d", key, value))
	})
	defer cache.Close()

	cache.Set("a", 1)
	cache.Set("b", 2)
	if got, ok := cache.Get("a"); !ok || got != 1 {
		t.Fatalf("expected a=1, got %v %v", got, ok)
	}
	cache.Set("c", 3)

	if _, ok := cache.Get("b"); ok {
		t.Fatal("least recently used entry b should have been evicted")
	}
	if got, ok := cache.Get("a"); !ok || got != 1 {
		t.Fatalf("a should still be present, got %v %v", got, ok)
	}
	if got, ok := cache.Get("c"); !ok || got != 3 {
		t.Fatalf("c should be present, got %v %v", got, ok)
	}
	if len(evicted) != 1 || evicted[0] != "b=2" {
		t.Fatalf("unexpected eviction callbacks: %v", evicted)
	}
}

func TestLRUCacheTTLExpiry(t *testing.T) {
	cache := NewLRUCache[string, string](8, 20*time.Millisecond, nil)
	defer cache.Close()

	cache.Set("short", "value")
	if got, ok := cache.Get("short"); !ok || got != "value" {
		t.Fatalf("expected live entry, got %q %v", got, ok)
	}
	time.Sleep(40 * time.Millisecond)
	if _, ok := cache.Get("short"); ok {
		t.Fatal("expired entry should not be returned")
	}
	if cache.Len() != 0 {
		t.Fatalf("expected empty cache after expiry, got %d", cache.Len())
	}
}

func TestLRUCachePurgeExpiredAndCallback(t *testing.T) {
	var evicted atomic.Int64
	cache := NewLRUCache(8, 15*time.Millisecond, func(_, _ int) {
		evicted.Add(1)
	})
	defer cache.Close()

	for i := 0; i < 4; i++ {
		cache.Set(i, i)
	}
	time.Sleep(30 * time.Millisecond)
	if removed := cache.PurgeExpired(); removed != 4 {
		t.Fatalf("expected 4 purged entries, got %d", removed)
	}
	if evicted.Load() != 4 {
		t.Fatalf("expected 4 callbacks, got %d", evicted.Load())
	}
	if cache.Len() != 0 {
		t.Fatalf("expected empty cache, got %d", cache.Len())
	}
}

func TestLRUCacheDeleteAndCloseCallbacks(t *testing.T) {
	var mu sync.Mutex
	seen := map[string]int{}
	cache := NewLRUCache(4, 0, func(key string, value int) {
		mu.Lock()
		seen[key] = value
		mu.Unlock()
	})

	cache.Set("x", 10)
	cache.Delete("x")
	cache.Set("y", 20)
	cache.Set("z", 30)
	cache.Close()

	mu.Lock()
	defer mu.Unlock()
	if seen["x"] != 10 || seen["y"] != 20 || seen["z"] != 30 {
		t.Fatalf("unexpected callback values: %v", seen)
	}
	// A second Close must not panic or duplicate callbacks.
	cache.Close()
}

func TestLRUCacheRangeStops(t *testing.T) {
	cache := NewLRUCache[int, int](8, 0, nil)
	defer cache.Close()
	for i := 0; i < 5; i++ {
		cache.Set(i, i)
	}

	visited := 0
	cache.Range(func(_, _ int) bool {
		visited++
		return visited < 3
	})
	if visited != 3 {
		t.Fatalf("expected iteration to stop after 3 entries, got %d", visited)
	}
}

func TestLRUCacheConcurrentAccess(t *testing.T) {
	cache := NewLRUCache[int, int](128, time.Minute, nil)
	defer cache.Close()

	var wg sync.WaitGroup
	for worker := 0; worker < 16; worker++ {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			for i := 0; i < 500; i++ {
				key := (worker*500 + i) % 512
				cache.Set(key, i)
				_, _ = cache.Get(key)
				if i%7 == 0 {
					cache.Delete(key)
				}
				if i%31 == 0 {
					_ = cache.Len()
				}
			}
		}(worker)
	}
	wg.Wait()

	if cache.Len() > 128 {
		t.Fatalf("cache exceeded capacity: %d", cache.Len())
	}
}
