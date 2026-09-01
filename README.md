# qi-index

An indexer for the Qi ledger on Quai Network. M1 scope: `follow` tracks one
zone's chain spine reorg-safely — subscribing to new heads over WS, polling
as a safety net, walking back to the common ancestor on reorgs, and catching
up from a checkpoint after downtime.

## How it works

- `internal/chain` — the shared `Block` / `Update` types.
- `internal/rpcclient` — a batched JSON-RPC client plus a WS head subscriber
  for a go-quai zone endpoint.
- `internal/follower` — the reorg-safe spine tracker: an in-memory window of
  recent headers, per-block parent-hash checks, and ancestor walks on
  divergence.
- `internal/store` — the persistence interface, with an in-memory
  implementation for development and a Postgres implementation (schema in
  `internal/store/migrations/`) that applies each rollback/apply/tip-move as
  one transaction.
- `internal/metrics` — Prometheus counters/gauges plus a `/metrics` and
  `/healthz` HTTP server.
- `internal/config` — layered config loading (defaults → YAML file → env
  vars → CLI flags) for each subcommand.
- `internal/indexer` — the follow loop: feeds heads from the RPC client's WS
  stream and a safety-net poll into the follower, records metrics, serves
  `/metrics`.
- `cmd/qi-index` — the CLI entrypoint. Thin by design: it loads config and
  builds the store, RPC client and follower, then hands them to
  `internal/indexer` to run.
- `cmd/m0spike` — throwaway M0 spike code, kept for reference only.

## Configuration

Settings are resolved in this order, each layer overriding only what it
sets:

1. **Built-in defaults**
2. **A YAML config file** — `configs/config.yaml` if present, or a path
   given via `-config` / `QI_CONFIG`. See
   [`configs/config.example.yaml`](configs/config.example.yaml) for every
   available key.
3. **Environment variables**
4. **CLI flags**

| Setting        | Config file key (`follow:`) | Env var             | Flag       | Default                                |
|----------------|------------------------------|----------------------|------------|-----------------------------------------|
| RPC URL        | `rpc_url`                    | `QI_RPC_URL`         | `-rpc`     | `https://rpc.quai.network/cyprus1`      |
| WS URL         | `ws_url`                     | `QI_WS_URL`          | `-ws`      | `wss://rpc.quai.network/cyprus1`        |
| Postgres URL   | `database_url`               | `QI_DATABASE_URL`    | `-db`      | *(empty → in-memory store)*             |
| Poll interval  | `poll_interval`               | `QI_POLL_INTERVAL`   | `-poll`    | `15s`                                    |
| Reorg depth    | `reorg_depth`                 | `QI_REORG_DEPTH`     | `-depth`   | `1024`                                   |
| Metrics addr   | `metrics_addr`                | `QI_METRICS_ADDR`    | `-metrics` | *(empty → metrics server off)*          |
| Verbose        | `verbose`                     | `QI_VERBOSE`         | `-v`       | `false`                                  |

An empty WS URL disables the WS subscription (polling only); an empty
Postgres URL runs with the in-memory store (state is lost on restart).

To set up a local config file:

```sh
cp configs/config.example.yaml configs/config.yaml
$EDITOR configs/config.yaml
```

## Common tasks

All of the below are also wrapped in a `Makefile` — run `make help` for the
full list:

```sh
make build              # go build ./cmd/qi-index -> ./bin/qi-index
make run                # go run ./cmd/qi-index follow
make test               # unit tests (Postgres-backed tests self-skip)
make test-integration   # spins up a throwaway Postgres, runs the full suite, tears it down
make compose-up         # docker compose up --build (postgres + qi-index)
make docker-build       # docker build -t qi-index:latest .
```

## Running locally

```sh
go run ./cmd/qi-index follow
```

With flags:

```sh
go run ./cmd/qi-index follow \
  -rpc https://rpc.quai.network/cyprus1 \
  -ws wss://rpc.quai.network/cyprus1 \
  -db postgres://qi_index:qi_index@localhost:5432/qi_index?sslmode=disable \
  -metrics :2112
```

## Running with Docker Compose

This brings up Postgres and qi-index together; qi-index waits for Postgres
to report healthy before starting.

```sh
cp .env.example .env
$EDITOR .env   # set the RPC/WS endpoints and Postgres credentials
docker compose up --build
```

- qi-index's `/metrics` and `/healthz` are published on `$METRICS_PORT`
  (default `2112`).
- Postgres is published on `$POSTGRES_PORT` (default `5432`) for local
  inspection.
- `./configs` is mounted read-only into the container at `/app/configs`, so
  a local `configs/config.yaml` is picked up automatically.
- An optional Prometheus instance (scraping qi-index's `/metrics`) is
  available behind the `observability` profile:

  ```sh
  docker compose --profile observability up --build
  ```

  Prometheus is then reachable at `http://localhost:9090`.

To stop everything and drop the Postgres volume:

```sh
make compose-down   # same as: docker compose down -v
```

## Tests

Unit tests need nothing external and self-skip anything requiring Postgres:

```sh
make test         # same as: go test ./...
```

`internal/store/postgres_test.go` runs against a real Postgres when
`TEST_DATABASE_URL` is set (and refuses to run against anything whose
database name doesn't contain `test`, as a guard rail against pointing it at
a real database by accident). To run the full suite including those tests:

```sh
make test-integration
```

This starts a throwaway `postgres-test` container (docker compose, `test`
profile, tmpfs storage, port `5433` by default), runs `go test ./...` against
it with `TEST_DATABASE_URL` set, then tears the container down regardless of
the test outcome. To manage that container by hand instead:

```sh
make test-up      # start it and wait for it to become healthy
TEST_DATABASE_URL=postgres://qi_index:qi_index@localhost:5433/qi_index_test?sslmode=disable go test ./...
make test-down    # stop it and drop its data
```
