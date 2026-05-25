// Package cache provides an optional, best-effort cache for compiled Arduino
// binaries.
//
// A compilation is a pure function of (board, code) for a fixed toolchain, so
// identical requests can return a stored binary instead of re-running
// arduino-cli. On a resource-constrained host this is a large win: a cache hit
// costs almost no CPU and — because the lookup happens before the concurrency
// semaphore — never consumes a compile slot.
//
// # Degradation contract
//
// Caching is strictly an optimization. Every implementation here MUST treat a
// backend problem (Redis down, timeout, parse error) as a cache MISS, never as
// an error that could fail a compilation. The caller relies on this: if the
// cache misbehaves, compiles simply fall through to arduino-cli as if no cache
// existed.
package cache

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/redis/go-redis/v9"
)

// Cache is the minimal surface the compiler needs. Implementations must be safe
// for concurrent use.
type Cache interface {
	// Get returns the cached value and true on a hit. A miss — including any
	// backend error — returns (nil, false).
	Get(ctx context.Context, key string) ([]byte, bool)

	// Set stores value under key with the given TTL. Best-effort: failures are
	// logged and otherwise ignored.
	Set(ctx context.Context, key string, value []byte, ttl time.Duration)
}

// --- No-op cache ---

// noopCache is used when no cache backend is configured. Every Get is a miss
// and every Set is discarded, so the compiler behaves exactly as it did before
// caching existed.
type noopCache struct{}

// NewNoop returns a cache that never stores anything.
func NewNoop() Cache { return noopCache{} }

func (noopCache) Get(context.Context, string) ([]byte, bool)         { return nil, false }
func (noopCache) Set(context.Context, string, []byte, time.Duration) {}

// --- Redis cache ---

type redisCache struct {
	rdb *redis.Client
}

// NewRedis dials the Redis server at url (e.g. "redis://redis:6379") and
// verifies connectivity with a PING within dialTimeout. On any failure it
// returns an error so the caller can fall back to NewNoop — it never blocks the
// server from starting.
//
// Cache operation timeouts are deliberately short: a struggling Redis must
// degrade to misses quickly rather than slow down every compile request.
func NewRedis(url string, dialTimeout time.Duration) (Cache, error) {
	opts, err := redis.ParseURL(url)
	if err != nil {
		return nil, fmt.Errorf("invalid REDIS_URL: %w", err)
	}
	opts.DialTimeout = dialTimeout
	opts.ReadTimeout = 250 * time.Millisecond
	opts.WriteTimeout = 250 * time.Millisecond
	opts.PoolSize = 10

	rdb := redis.NewClient(opts)

	ctx, cancel := context.WithTimeout(context.Background(), dialTimeout)
	defer cancel()
	if err := rdb.Ping(ctx).Err(); err != nil {
		_ = rdb.Close()
		return nil, fmt.Errorf("redis ping failed: %w", err)
	}
	return &redisCache{rdb: rdb}, nil
}

func (c *redisCache) Get(ctx context.Context, key string) ([]byte, bool) {
	val, err := c.rdb.Get(ctx, key).Bytes()
	if err == redis.Nil {
		return nil, false // genuine miss
	}
	if err != nil {
		// Outage / timeout / context cancellation — treat as a miss so the
		// request falls through to a real compile.
		log.Printf("[CACHE] get failed (treating as miss): %v", err)
		return nil, false
	}
	return val, true
}

func (c *redisCache) Set(ctx context.Context, key string, value []byte, ttl time.Duration) {
	if err := c.rdb.Set(ctx, key, value, ttl).Err(); err != nil {
		log.Printf("[CACHE] set failed (ignored): %v", err)
	}
}
