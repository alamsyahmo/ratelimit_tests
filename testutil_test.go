package main

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
)

func redisAddr() string {
	if v := os.Getenv("REDIS_ADDR"); v != "" {
		return v
	}
	return "127.0.0.1:6379"
}

func newRedisClient() *redis.Client {
	return redis.NewClient(&redis.Options{Addr: redisAddr()})
}

func requireRedis(t testing.TB, rdb *redis.Client) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	// Retry briefly to avoid flakiness if Redis is just starting up.
	deadline := time.Now().Add(2 * time.Second)
	var lastErr error
	for time.Now().Before(deadline) {
		_, lastErr = rdb.Ping(ctx).Result()
		if lastErr == nil {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Skipf("redis not reachable at %q: %v (start with `docker compose up -d`)", redisAddr(), lastErr)
}

func uniquePrefix(t testing.TB) string {
	t.Helper()
	// Unique, safe isolation for tests; avoids flushing DB or deleting unknown keys.
	return fmt.Sprintf("test:rl:%s:%d:", t.Name(), time.Now().UnixNano())
}


