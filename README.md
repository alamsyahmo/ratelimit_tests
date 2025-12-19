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


