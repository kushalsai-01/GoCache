package cache

import "time"

// expiryLoop periodically scans and removes expired entries.
//
// Why a ticker-based full scan?
//   - It's easy to reason about (correctness-first)
//   - It avoids per-entry goroutines/timers (which are expensive and hard to own)
//   - It demonstrates real-world tradeoffs: predictable simplicity vs O(n) scans
func (c *Cache) expiryLoop() {
	defer c.wg.Done()

	ticker := time.NewTicker(c.cleanupEvery)
	defer ticker.Stop()

	for {
		select {
		case <-c.ctx.Done():
			return
		case now := <-ticker.C:
			c.mu.Lock()
			// If Close raced with the ticker, still safe: Close cancels ctx, notifies loop.
			c.deleteExpiredLocked(now)
			c.mu.Unlock()
		}
	}
}
