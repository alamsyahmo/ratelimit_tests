package main

import (
	"context"
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-redis/redis_rate/v10"
	"github.com/moneyforward/mf-common-go/ratelimit"
	"github.com/redis/go-redis/v9"
)

type allowFunc func(ctx context.Context, key string) (allowed bool, remaining int, err error)

type segment struct {
	dur time.Duration
	rps int
}

type scenario struct {
	name     string
	segments []segment
}

type loadStats struct {
	total     int64
	allowed   int64
	denied    int64
	errs      int64
	duration  time.Duration
	maxHeap   uint64
	startHeap uint64
	endHeap   uint64
	allocDiff uint64
	gcsDiff   uint32
}

func envInt(name string, def int) int {
	if v := strings.TrimSpace(os.Getenv(name)); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}

func envDuration(name string, def time.Duration) time.Duration {
	if v := strings.TrimSpace(os.Getenv(name)); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
	}
	return def
}

func newMFCommonAllow(t testing.TB, rdb *redis.Client, prefix string, ratePerSec, capacity int, ttl time.Duration) allowFunc {
	t.Helper()
	limiter, err := ratelimit.NewRedisRateLimiter(
		rdb,
		ratelimit.WithRatePerSecond(float64(ratePerSec)),
		ratelimit.WithCapacity(float64(capacity)),
		ratelimit.WithTTL(ttl),
		ratelimit.WithPrefix(prefix),
	)
	if err != nil {
		t.Fatalf("mf-common-go: NewRedisRateLimiter error: %v", err)
	}
	return func(ctx context.Context, key string) (bool, int, error) {
		return limiter.Allow(ctx, key)
	}
}

func newRedisRateAllow(rdb *redis.Client, ratePerSec, capacity int) allowFunc {
	limiter := redis_rate.NewLimiter(rdb)
	limit := redis_rate.Limit{
		Rate:   ratePerSec,
		Burst:  capacity,
		Period: time.Second,
	}
	return func(ctx context.Context, key string) (bool, int, error) {
		res, err := limiter.Allow(ctx, key, limit)
		if err != nil {
			return false, 0, err
		}
		allowed := res.Allowed > 0
		return allowed, res.Remaining, nil
	}
}

func warmup(ctx context.Context, af allowFunc, key string, n int) {
	for i := 0; i < n; i++ {
		_, _, _ = af(ctx, key)
	}
}

func runScenario(ctx context.Context, af allowFunc, key string, segments []segment, concurrency int) loadStats {
	var total, allowed, denied, errs int64

	// Warm up once to load scripts / prime clients, then GC so the measured heap looks sane.
	warmup(ctx, af, key, 100)
	runtime.GC()

	var msBefore runtime.MemStats
	runtime.ReadMemStats(&msBefore)

	maxHeap := msBefore.HeapAlloc

	samplerDone := make(chan struct{})
	go func() {
		t := time.NewTicker(250 * time.Millisecond)
		defer t.Stop()
		for {
			select {
			case <-samplerDone:
				return
			case <-t.C:
				var ms runtime.MemStats
				runtime.ReadMemStats(&ms)
				for {
					old := atomic.LoadUint64(&maxHeap)
					if ms.HeapAlloc <= old {
						break
					}
					if atomic.CompareAndSwapUint64(&maxHeap, old, ms.HeapAlloc) {
						break
					}
				}
			}
		}
	}()

	start := time.Now()

	var wg sync.WaitGroup
	wg.Add(concurrency)
	for w := 0; w < concurrency; w++ {
		go func(worker int) {
			defer wg.Done()
			for _, seg := range segments {
				if seg.rps <= 0 || seg.dur <= 0 {
					continue
				}

				// Per-worker pacing that still works when seg.rps < concurrency:
				// distribute the remainder so some workers run at +1 rps and others at 0.
				base := seg.rps / concurrency
				rem := seg.rps % concurrency
				perWorker := base
				if worker < rem {
					perWorker++
				}
				if perWorker <= 0 {
					time.Sleep(seg.dur)
					continue
				}

				interval := time.Duration(float64(time.Second) / float64(perWorker))
				segStart := time.Now()
				segEnd := segStart.Add(seg.dur)

				for i := 0; ; i++ {
					next := segStart.Add(time.Duration(i) * interval)
					if next.After(segEnd) {
						break
					}
					if d := time.Until(next); d > 0 {
						time.Sleep(d)
					}

					ok, _, err := af(ctx, key)
					atomic.AddInt64(&total, 1)
					if err != nil {
						atomic.AddInt64(&errs, 1)
						continue
					}
					if ok {
						atomic.AddInt64(&allowed, 1)
					} else {
						atomic.AddInt64(&denied, 1)
					}
				}
			}
		}(w)
	}
	wg.Wait()

	dur := time.Since(start)
	close(samplerDone)

	runtime.GC()
	var msAfter runtime.MemStats
	runtime.ReadMemStats(&msAfter)

	return loadStats{
		total:     total,
		allowed:   allowed,
		denied:    denied,
		errs:      errs,
		duration:  dur,
		maxHeap:   atomic.LoadUint64(&maxHeap),
		startHeap: msBefore.HeapAlloc,
		endHeap:   msAfter.HeapAlloc,
		allocDiff: msAfter.TotalAlloc - msBefore.TotalAlloc,
		gcsDiff:   msAfter.NumGC - msBefore.NumGC,
	}
}

func TestComparison_redis_rate_vs_mf_common_go(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping load comparison in -short mode")
	}

	ratePerSec := envInt("REFILL_RATE", 100)
	capacity := envInt("CAPACITY", 100)
	concurrency := envInt("CONCURRENCY", 50)
	ttl := envDuration("MF_TTL", 1*time.Hour)

	// Required by the prompt: 20 seconds.
	duration := envDuration("DURATION", 20*time.Second)
	if duration <= 0 {
		duration = 20 * time.Second
	}

	// Case #2 is underspecified ("start slow"); defaults below are tweakable via env.
	slowRPS := envInt("SLOW_RPS", 10)
	slowDur := envDuration("SLOW_DURATION", 5*time.Second)
	burstRPS := envInt("BURST_RPS", 1000)

	if slowDur >= duration {
		slowDur = duration / 4
	}

	scenarios := []scenario{
		{
			name:     "normal_100rps_20s",
			segments: []segment{{dur: duration, rps: 100}},
		},
		{
			name: "slow_then_burst_20s",
			segments: []segment{
				{dur: slowDur, rps: slowRPS},
				{dur: duration - slowDur, rps: burstRPS},
			},
		},
		{
			name:     "burst_1000rps_20s",
			segments: []segment{{dur: duration, rps: burstRPS}},
		},
	}

	rdb := newRedisClient()
	t.Cleanup(func() { _ = rdb.Close() })
	requireRedis(t, rdb)

	t.Logf("settings: refill_rate=%d/s capacity=%d concurrency=%d duration=%s (case2: slow=%d/s for %s then burst=%d/s)",
		ratePerSec, capacity, concurrency, duration, slowRPS, slowDur, burstRPS,
	)

	type row struct {
		impl     string
		scenario string
		stats    loadStats
	}
	var rows []row

	for _, sc := range scenarios {
		// mf-common-go
		{
			ctx, cancel := context.WithTimeout(context.Background(), duration+10*time.Second)
			t.Logf("running: impl=%s scenario=%s", "mf-common-go", sc.name)

			prefix := uniquePrefix(t) + "mf:" + sc.name + ":"
			key := "user:bench"
			af := newMFCommonAllow(t, rdb, prefix, ratePerSec, capacity, ttl)
			stats := runScenario(ctx, af, key, sc.segments, concurrency)
			cancel()
			rows = append(rows, row{impl: "mf-common-go", scenario: sc.name, stats: stats})
			t.Logf("done:    impl=%s scenario=%s total=%d allowed=%d denied=%d errs=%d dur=%s",
				"mf-common-go", sc.name, stats.total, stats.allowed, stats.denied, stats.errs, stats.duration,
			)
		}
		// redis_rate/v10 (prefix goes into the key)
		{
			ctx, cancel := context.WithTimeout(context.Background(), duration+10*time.Second)
			t.Logf("running: impl=%s scenario=%s", "redis_rate/v10", sc.name)

			prefix := uniquePrefix(t) + "rr:" + sc.name + ":"
			key := prefix + "user:bench"
			af := newRedisRateAllow(rdb, ratePerSec, capacity)
			stats := runScenario(ctx, af, key, sc.segments, concurrency)
			cancel()
			rows = append(rows, row{impl: "redis_rate/v10", scenario: sc.name, stats: stats})
			t.Logf("done:    impl=%s scenario=%s total=%d allowed=%d denied=%d errs=%d dur=%s",
				"redis_rate/v10", sc.name, stats.total, stats.allowed, stats.denied, stats.errs, stats.duration,
			)
		}
	}

	// Print a simple table (kept in logs so it doesn’t fail CI due to formatting).
	t.Logf("%-14s | %-22s | %10s | %10s | %10s | %8s | %9s | %9s | %9s | %6s",
		"impl", "scenario", "total", "allowed", "denied", "errs", "req/s", "heapΔ", "maxHeap", "GCs",
	)
	t.Log(strings.Repeat("-", 130))
	for _, r := range rows {
		reqPerSec := float64(r.stats.total) / r.stats.duration.Seconds()
		heapDelta := int64(r.stats.endHeap) - int64(r.stats.startHeap)
		t.Logf("%-14s | %-22s | %10d | %10d | %10d | %8d | %9.1f | %9d | %9d | %6d",
			r.impl,
			r.scenario,
			r.stats.total,
			r.stats.allowed,
			r.stats.denied,
			r.stats.errs,
			reqPerSec,
			heapDelta,
			r.stats.maxHeap,
			r.stats.gcsDiff,
		)
	}

	// Also log total allocated bytes to compare allocator pressure.
	t.Logf("")
	t.Logf("%-14s | %-22s | %12s", "impl", "scenario", "TotalAllocΔ")
	t.Log(strings.Repeat("-", 60))
	for _, r := range rows {
		t.Logf("%-14s | %-22s | %12d", r.impl, r.scenario, r.stats.allocDiff)
	}

	// Small sanity check to catch totally broken runs.
	for _, r := range rows {
		if r.stats.total == 0 {
			t.Fatalf("no requests executed for %s / %s", r.impl, r.scenario)
		}
	}
}
