package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"gocache/internal/cache"
)

func main() {
	// Signal-aware context is the root of ownership for long-lived background work.
	// When SIGINT/SIGTERM arrives, ctx is canceled and we initiate a clean shutdown.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	c := cache.New(cache.Config{
		MaxEntries:      2,
		CleanupInterval: 100 * time.Millisecond,
	})
	defer func() {
		// Close is idempotent; safe to call in defer.
		if err := c.Close(); err != nil {
			log.Printf("cache close: %v", err)
		}
	}()

	log.Println("GoCache demo starting")
	log.Printf("config: maxEntries=%d cleanupEvery=%s", 2, 100*time.Millisecond)

	// -------------------------------------------------------------------
	// 1) LRU eviction demo (capacity=2)
	// -------------------------------------------------------------------
	_ = c.Set("a", []byte("A"), 0)
	_ = c.Set("b", []byte("B"), 0)

	// Touch "a" so "b" becomes least-recently-used.
	if v, ok := c.Get("a"); ok {
		log.Printf("GET a = %q (touches a -> MRU)", string(v))
	}

	// Insert "c" => cache overflows and evicts LRU (expected: "b").
	_ = c.Set("c", []byte("C"), 0)
	if _, ok := c.Get("b"); !ok {
		log.Println("GET b: missing (evicted as LRU)")
	}
	log.Printf("keys after eviction (MRU->LRU): %v", c.Keys())

	// -------------------------------------------------------------------
	// 2) TTL expiration demo (shows background cleanup)
	// -------------------------------------------------------------------
	// Add a short-lived key. We intentionally do NOT call Get() after it expires;
	// the maintenance goroutine should remove it during its periodic scan.
	_ = c.Set("ttl", []byte("short"), 200*time.Millisecond)
	log.Printf("keys after ttl set (MRU->LRU): %v", c.Keys())

	// Wait long enough for expiry + at least one cleanup tick.
	wait := time.NewTimer(500 * time.Millisecond)
	defer wait.Stop()

	select {
	case <-ctx.Done():
		log.Println("received shutdown signal")
		return
	case <-wait.C:
	}

	log.Printf("keys after ttl + cleanup (MRU->LRU): %v", c.Keys())
	if _, ok := c.Get("ttl"); !ok {
		log.Println("GET ttl: missing (expired and removed)")
	}

	fmt.Println("Done. Press Ctrl+C to exit immediately next time.")
}
