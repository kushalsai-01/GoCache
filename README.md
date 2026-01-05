# GoCache

## Project Overview

GoCache is a single-process, in-memory key–value cache implemented from scratch in Go.
It supports per-entry TTL expiration and a fixed-size LRU eviction policy.

This project is intentionally **not** distributed and has **no persistence**. The goal is to learn how real caches work internally: how they organize data for $O(1)$ operations, how they stay correct under concurrency, how TTL interacts with eviction, and how to own and shut down long-lived goroutines cleanly.

## Why I Built This Project

I built GoCache to learn systems-level backend fundamentals by implementing the internals manually using only the Go standard library:

- In-memory data structure design (map + doubly-linked list)
- Concurrency safety and trade-offs (sync.RWMutex)
- TTL expiration semantics and cleanup strategies
- Eviction policies (LRU) and capacity enforcement
- Background goroutine ownership, cancellation, and graceful shutdown

## Architecture Overview

At its core, GoCache is a map for $O(1)$ key lookups plus a doubly-linked list to maintain recency ordering for LRU.
A background maintenance goroutine (optional) periodically removes expired entries.

ASCII diagram:

```
                  +-------------------+
                  |    Cache (RWMutex)|
                  +-------------------+
                      |         |
                      |         |
                      v         v
            +----------------+  +----------------------+
            | map[string]*E  |  | doubly-linked list    |
            | key -> element |  | Front = MRU           |
            +----------------+  | Back  = LRU           |
                      |         +----------------------+
                      |
                      v
            +-------------------+
            | list.Element.Value|
            |    *entry         |
            | key, value, TTL   |
            +-------------------+

            Background goroutine (optional):
            - ticker-based scan to remove expired entries
            - stops on Close() via context cancellation
```

Responsibilities:

- Cache data structures: store entries and maintain recency ordering
- Public API: Set/Get/Delete/Len
- TTL handling:
  - Lazy deletion on Get
  - Optional active deletion via background cleanup loop
- Eviction:
  - Enforce MaxEntries using LRU ordering
  - Prefer removing expired entries when under capacity pressure
- Lifecycle:
  - Cache owns its goroutines
  - Close() cancels context and waits for goroutines to exit

## Key Systems Concepts Demonstrated

### $O(1)$ cache operations

- Map lookup gives $O(1)$ access to the list node for a key.
- List operations (move-to-front / remove) are $O(1)$ in a doubly-linked list.

### RWMutex trade-offs

- Reads want an `RLock`, but LRU updates require a write.
- `Get` uses an “optimistic read, then confirm under write lock” pattern:
  - read to check existence/expiry
  - then take the write lock to move the item to MRU safely

This demonstrates a real trade-off: keeping reads fast while maintaining correct LRU ordering.

### TTL expiration

TTL is per key. A key can either:

- never expire (ttl <= 0)
- expire at a specific `ExpiresAt` time

GoCache supports two complementary strategies:

- Lazy expiration: on `Get`, expired keys are removed immediately
- Active expiration: a background goroutine periodically scans and removes expired keys

Lazy expiration alone can leave dead keys in memory forever if they are never accessed again.

### LRU eviction

When `MaxEntries > 0`, inserts that overflow capacity evict the least-recently-used item.
The LRU list ordering is updated on every successful `Get` and `Set`.

### TTL vs eviction interaction

When the cache is under pressure (too many entries), it first tries to reclaim expired items.
This prevents expired entries from “stealing” capacity from live data and keeps LRU semantics meaningful for non-expired keys.

### Background goroutine ownership

The cleanup goroutine:

- is started by `New(...)` only if `CleanupInterval > 0`
- is owned by the cache instance (no global background workers)
- exits when the cache’s context is canceled

### Graceful shutdown

`Close()`:

- marks the cache as closed for mutation (`Set`/`Delete` return an error)
- cancels the internal context
- waits for background goroutines to stop

This is the core "no goroutine leaks" contract.

## How It Works (Step-by-Step)

### SET flow

1. Take write lock (`Lock`) because we may mutate map, list, and do eviction
2. If key exists:
   - overwrite value
   - update TTL metadata
   - move node to MRU (front of list)
3. If key is new:
   - create an entry
   - push node to MRU (front)
   - store node pointer in the map
4. Enforce capacity:
   - opportunistically delete expired entries
   - if still over capacity, evict from LRU end

### GET flow

1. Take read lock (`RLock`) to check map and expiry cheaply
2. If expired:
   - upgrade to write lock and delete
3. If present and not expired:
   - take write lock
   - re-check (it might have been deleted between locks)
   - move node to MRU
   - return a copy of the value

Returning a copy is important: callers should not be able to mutate internal cache memory.

### Expiry flow

- Lazy (on `Get`): if `now >= ExpiresAt`, delete and return missing
- Active (background): every tick, take the write lock and scan all entries

This scan is $O(n)$, intentionally chosen for clarity and correctness.

### Eviction flow

1. If `MaxEntries <= 0`: no eviction (unbounded)
2. Otherwise:
   - attempt to delete expired entries first
   - while `len(items) > MaxEntries`, remove from the list back (LRU)

### Shutdown flow

1. `Close()` marks the cache closed for mutation
2. `Close()` cancels the internal context
3. Cleanup goroutine observes cancellation and exits
4. `Close()` waits for goroutine(s) to finish before returning

## Running the Project

From the repository root:

- Run the demo:
  - `go run ./cmd/gocache`

- Run all tests:
  - `go test ./...`

- Build everything:
  - `go build ./...`

The demo prints:

- a deterministic LRU eviction example
- a TTL key being removed by the background cleanup loop
- current keys in MRU → LRU order

- 
## Why This Matters for Backend Engineers

I built this project to understand how an in-memory cache actually works under the hood.

Working through it helped me think more clearly about:
- latency vs throughput trade-offs when adding caching
- correctness issues like stale data, race conditions, and memory growth
- how TTL and eviction interact in real usage
- how to manage long-lived goroutines and shut them down cleanly

Building this by hand forced me to deal with the same core constraints you hit in real systems: data structures, concurrency correctness, and lifecycle management.

