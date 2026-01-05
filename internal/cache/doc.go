// Package cache implements a single-process, in-memory key–value cache.
//
// Goals for this package:
//   - Make the core data structures explicit (map + doubly-linked list)
//   - Provide O(1) Set/Get/Delete via map index + LRU pointers
//   - Be concurrency-safe (RWMutex) with correctness as the primary goal
//   - Support per-entry TTL with both lazy and active expiration
//   - Own and cleanly stop long-lived goroutines (no leaks on shutdown)
package cache
