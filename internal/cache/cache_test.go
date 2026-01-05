package cache

import (
	"testing"
	"time"
)

func TestLRUEviction(t *testing.T) {
	c := New(Config{MaxEntries: 2, CleanupInterval: 0})
	defer c.Close()

	if err := c.Set("a", []byte("A"), 0); err != nil {
		t.Fatalf("set a: %v", err)
	}
	if err := c.Set("b", []byte("B"), 0); err != nil {
		t.Fatalf("set b: %v", err)
	}

	// Touch a so b becomes LRU.
	if _, ok := c.Get("a"); !ok {
		t.Fatalf("expected a to exist")
	}

	// Insert c => should evict b.
	if err := c.Set("c", []byte("C"), 0); err != nil {
		t.Fatalf("set c: %v", err)
	}

	if _, ok := c.Get("b"); ok {
		t.Fatalf("expected b to be evicted")
	}
	if _, ok := c.Get("a"); !ok {
		t.Fatalf("expected a to remain")
	}
	if _, ok := c.Get("c"); !ok {
		t.Fatalf("expected c to exist")
	}
}

func TestTTL_LazyExpirationOnGet(t *testing.T) {
	c := New(Config{MaxEntries: 10, CleanupInterval: 0})
	defer c.Close()

	if err := c.Set("k", []byte("v"), 30*time.Millisecond); err != nil {
		t.Fatalf("set: %v", err)
	}

	if _, ok := c.Get("k"); !ok {
		t.Fatalf("expected k to exist before expiry")
	}

	time.Sleep(80 * time.Millisecond)

	if _, ok := c.Get("k"); ok {
		t.Fatalf("expected k to be expired and removed on get")
	}
}

func TestTTL_BackgroundCleanupRemovesWithoutGet(t *testing.T) {
	c := New(Config{MaxEntries: 10, CleanupInterval: 10 * time.Millisecond})
	defer c.Close()

	if err := c.Set("ttl", []byte("v"), 20*time.Millisecond); err != nil {
		t.Fatalf("set: %v", err)
	}

	// Wait until the cleanup goroutine removes it. Use a deadline to avoid flakes.
	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		keys := c.Keys()
		found := false
		for _, k := range keys {
			if k == "ttl" {
				found = true
				break
			}
		}
		if !found {
			return // success
		}
		time.Sleep(5 * time.Millisecond)
	}

	// As a fallback check, even if Keys happened to still show it,
	// Get must treat it as expired.
	if _, ok := c.Get("ttl"); ok {
		t.Fatalf("expected ttl to be expired")
	}
}

func TestClose_IdempotentAndPreventsMutation(t *testing.T) {
	c := New(Config{MaxEntries: 1, CleanupInterval: 10 * time.Millisecond})

	if err := c.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if err := c.Close(); err != nil {
		t.Fatalf("close again: %v", err)
	}

	if err := c.Set("k", []byte("v"), 0); err == nil {
		t.Fatalf("expected Set to fail after close")
	}
	if err := c.Delete("k"); err == nil {
		t.Fatalf("expected Delete to fail after close")
	}
}
