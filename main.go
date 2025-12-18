package main

import (
	"context"
	"fmt"
	"time"

	"github.com/moneyforward/mf-common-go/ratelimit"
	"github.com/redis/go-redis/v9"
)

func main() {
	ctx := context.Background()

	rdb := redis.NewClient(&redis.Options{Addr: "127.0.0.1:6379"})

	limiter, err := ratelimit.NewRedisRateLimiter(
		rdb,
		ratelimit.WithRatePerSecond(5),    // 5 tokens/sec
		ratelimit.WithCapacity(10),        // burst up to 10
		ratelimit.WithTTL(1*time.Hour),    // Redis key TTL
		ratelimit.WithPrefix("myapp:rl:"), // key prefix
	)
	if err != nil {
		panic(err)
	}

	allowed, remaining, err := limiter.Allow(ctx, "user:123")
	if err != nil {
		panic(err)
	}

	fmt.Printf("allowed: %v, remaining: %v\n", allowed, remaining)
}
