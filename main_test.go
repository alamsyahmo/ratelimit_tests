package main

import (
	"context"
	"testing"
	"time"

	"github.com/moneyforward/mf-common-go/ratelimit"
	"github.com/redis/go-redis/v9"
)

func newLimiter(t *testing.T, rdb *redis.Client, prefix string, ratePerSec, capacity int, ttl time.Duration) ratelimit.RateLimiter {
	t.Helper()
	limiter, err := ratelimit.NewRedisRateLimiter(
		rdb,
		ratelimit.WithRatePerSecond(float64(ratePerSec)),
		ratelimit.WithCapacity(float64(capacity)),
		ratelimit.WithTTL(ttl),
		ratelimit.WithPrefix(prefix),
	)
	if err != nil {
		t.Fatalf("NewRedisRateLimiter error: %v", err)
	}
	return limiter
}

func TestRedisRateLimiter_BurstThenDeny(t *testing.T) {
	rdb := newRedisClient()
	t.Cleanup(func() { _ = rdb.Close() })
	requireRedis(t, rdb)

	const (
		rate     = 1
		capacity = 3
	)
	limiter := newLimiter(t, rdb, uniquePrefix(t), rate, capacity, 5*time.Second)

	ctx := context.Background()
	key := "user:burst"

	for i := 0; i < capacity; i++ {
		allowed, remaining, err := limiter.Allow(ctx, key)
		if err != nil {
			t.Fatalf("Allow error on call %d: %v", i+1, err)
		}
		if !allowed {
			t.Fatalf("expected allowed=true on call %d", i+1)
		}
		if remaining < 0 {
			t.Fatalf("expected remaining >= 0, got %d on call %d", remaining, i+1)
		}
	}

	allowed, _, err := limiter.Allow(ctx, key)
	if err != nil {
		t.Fatalf("Allow error on denied call: %v", err)
	}
	if allowed {
		t.Fatalf("expected allowed=false after exhausting burst capacity")
	}
}

func TestRedisRateLimiter_RefillsOverTime(t *testing.T) {
	rdb := newRedisClient()
	t.Cleanup(func() { _ = rdb.Close() })
	requireRedis(t, rdb)

	const (
		rate     = 2 // tokens/sec
		capacity = 2
	)
	limiter := newLimiter(t, rdb, uniquePrefix(t), rate, capacity, 5*time.Second)

	ctx := context.Background()
	key := "user:refill"

	// Exhaust burst quickly.
	for i := 0; i < capacity; i++ {
		allowed, _, err := limiter.Allow(ctx, key)
		if err != nil {
			t.Fatalf("Allow error on call %d: %v", i+1, err)
		}
		if !allowed {
			t.Fatalf("expected allowed=true while exhausting burst (call %d)", i+1)
		}
	}
	allowed, _, err := limiter.Allow(ctx, key)
	if err != nil {
		t.Fatalf("Allow error on deny: %v", err)
	}
	if allowed {
		t.Fatalf("expected allowed=false once burst exhausted")
	}

	time.Sleep(2 * time.Second)
	allowed, _, err = limiter.Allow(ctx, key)
	if err != nil {
		t.Fatalf("Allow error after refill: %v", err)
	}
	if !allowed {
		t.Fatalf("expected allowed=true after waiting for refill")
	}
}

func TestRedisRateLimiter_IsolatedPerKey(t *testing.T) {
	rdb := newRedisClient()
	t.Cleanup(func() { _ = rdb.Close() })
	requireRedis(t, rdb)

	const (
		rate     = 1
		capacity = 2
	)
	limiter := newLimiter(t, rdb, uniquePrefix(t), rate, capacity, 5*time.Second)

	ctx := context.Background()
	keyA := "user:A"
	keyB := "user:B"

	// Exhaust keyA.
	for i := 0; i < capacity; i++ {
		allowed, _, err := limiter.Allow(ctx, keyA)
		if err != nil {
			t.Fatalf("Allow error for keyA call %d: %v", i+1, err)
		}
		if !allowed {
			t.Fatalf("expected keyA allowed=true for call %d", i+1)
		}
	}
	allowed, _, err := limiter.Allow(ctx, keyA)
	if err != nil {
		t.Fatalf("Allow error for keyA deny: %v", err)
	}
	if allowed {
		t.Fatalf("expected keyA allowed=false after exhausting")
	}

	// keyB should still be allowed (fresh bucket).
	allowed, _, err = limiter.Allow(ctx, keyB)
	if err != nil {
		t.Fatalf("Allow error for keyB: %v", err)
	}
	if !allowed {
		t.Fatalf("expected keyB allowed=true (independent of keyA)")
	}
}

func TestRedisRateLimiter_IsolatedPerPrefix(t *testing.T) {
	rdb := newRedisClient()
	t.Cleanup(func() { _ = rdb.Close() })
	requireRedis(t, rdb)

	const (
		rate     = 1
		capacity = 2
	)
	ctx := context.Background()
	key := "user:same"

	limiterA := newLimiter(t, rdb, uniquePrefix(t)+"A:", rate, capacity, 5*time.Second)
	limiterB := newLimiter(t, rdb, uniquePrefix(t)+"B:", rate, capacity, 5*time.Second)

	// Exhaust limiterA.
	for i := 0; i < capacity; i++ {
		allowed, _, err := limiterA.Allow(ctx, key)
		if err != nil {
			t.Fatalf("limiterA Allow error on call %d: %v", i+1, err)
		}
		if !allowed {
			t.Fatalf("expected limiterA allowed=true on call %d", i+1)
		}
	}
	allowed, _, err := limiterA.Allow(ctx, key)
	if err != nil {
		t.Fatalf("limiterA Allow error on deny: %v", err)
	}
	if allowed {
		t.Fatalf("expected limiterA allowed=false after exhausting")
	}

	// limiterB should be unaffected because prefix differs.
	allowed, _, err = limiterB.Allow(ctx, key)
	if err != nil {
		t.Fatalf("limiterB Allow error: %v", err)
	}
	if !allowed {
		t.Fatalf("expected limiterB allowed=true (independent prefix)")
	}
}

func TestRedisRateLimiter_TTLResetsState(t *testing.T) {
	rdb := newRedisClient()
	t.Cleanup(func() { _ = rdb.Close() })
	requireRedis(t, rdb)

	const (
		rate     = 1
		capacity = 2
	)
	ttl := 1 * time.Second
	limiter := newLimiter(t, rdb, uniquePrefix(t), rate, capacity, ttl)

	ctx := context.Background()
	key := "user:ttl"

	// Use up tokens.
	for i := 0; i < capacity; i++ {
		allowed, _, err := limiter.Allow(ctx, key)
		if err != nil {
			t.Fatalf("Allow error on call %d: %v", i+1, err)
		}
		if !allowed {
			t.Fatalf("expected allowed=true on call %d", i+1)
		}
	}
	allowed, _, err := limiter.Allow(ctx, key)
	if err != nil {
		t.Fatalf("Allow error on deny: %v", err)
	}
	if allowed {
		t.Fatalf("expected allowed=false after exhausting")
	}

	// Wait for TTL to expire and verify state behaves like "new".
	time.Sleep(ttl + 250*time.Millisecond)

	allowed, remaining, err := limiter.Allow(ctx, key)
	if err != nil {
		t.Fatalf("Allow error after TTL: %v", err)
	}
	if !allowed {
		t.Fatalf("expected allowed=true after TTL reset")
	}
	if remaining < 0 {
		t.Fatalf("expected remaining >= 0 after TTL reset, got %d", remaining)
	}
}
