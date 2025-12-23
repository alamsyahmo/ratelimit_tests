# ratelimit_tests

Small Go sandbox for trying **`github.com/moneyforward/mf-common-go/ratelimit`** with a real **Redis** backend (via OrbStack + Docker Compose).

## Prerequisites

- Go (this repo is set to `go 1.25.5` in `go.mod`)
- OrbStack (or any Docker Desktop compatible runtime)
- Docker Compose (available as `docker compose`)

## Start Redis (OrbStack)

From the repo root:

```bash
docker compose up -d
docker compose ps
```

Stop it when you’re done:

```bash
docker compose down
```

## Run the example program

`main.go` creates a Redis-backed token bucket limiter and calls `Allow(ctx, "user:123")`.

```bash
go run .
```

## Run tests

Tests are **integration tests** (they talk to Redis). Start Redis first (see above), then:

```bash
go test ./...
```

To see test output (e.g. `fmt.Printf(...)` logs), run with verbose mode:

```bash
go test -v ./...
```

## Comparison load test: `redis_rate/v10` vs `mf-common-go`

There is a long-running comparison test that runs **3 scenarios** for **20 seconds each** (so ~2 minutes total):

- **normal**: 100 req/s for 20s
- **slow then burst**: default 10 req/s for 5s, then 1000 req/s for 15s
- **burst**: 1000 req/s for 20s

All scenarios use a limiter configured with:

- **refill rate**: 100 tokens/second
- **capacity**: 100 (default; can be overridden)

Run it:

```bash
go test -v -run TestComparison_redis_rate_vs_mf_common_go .
```

Useful knobs (optional):

- **`REFILL_RATE`**: tokens/sec (default `100`)
- **`CAPACITY`**: bucket capacity / burst (default `100`)
- **`CONCURRENCY`**: number of goroutines generating load (default `50`)
- **`DURATION`**: scenario duration (default `20s`)
- **`SLOW_RPS`**: case #2 slow phase rps (default `10`)
- **`SLOW_DURATION`**: case #2 slow phase duration (default `5s`)
- **`BURST_RPS`**: burst rps for cases #2/#3 (default `1000`)

Example:

```bash
REFILL_RATE=100 CAPACITY=100 CONCURRENCY=50 go test -v -run TestComparison_redis_rate_vs_mf_common_go .
```

The output includes:

- achieved **req/s**
- total/allowed/denied/errors
- heap delta + max heap observed (approx)
- GC count delta

## Test scenarios covered

See `main_test.go`:

- **Burst then deny**: consumes up to capacity, then expects a deny
- **Refill over time**: after exhaustion, waits and expects allow again
- **Per-key isolation**: different keys get independent buckets
- **Per-prefix isolation**: different limiter prefixes do not interfere
- **TTL reset**: after the Redis key TTL expires, state behaves like a fresh limiter

Each test uses a **unique key prefix** to avoid flushing Redis or touching unrelated data.

## Configuration

- **`REDIS_ADDR`**: Redis address to use for tests (defaults to `127.0.0.1:6379`)

Example:

```bash
REDIS_ADDR=localhost:6379 go test -v ./...
```


