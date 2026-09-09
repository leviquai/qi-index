# Running qi-index

Two ways to run qi-index: Docker Compose (recommended, zero manual setup) or standalone (Go binary plus your own Postgres).

## Prerequisites

Both methods require a Quai Network RPC endpoint. The public mainnet endpoint is:

```
RPC:  https://rpc.quai.network/cyprus1
WS:   wss://rpc.quai.network/cyprus1
```

You can use these directly. No authentication required.

---

## Option 1: Docker Compose

This is the fastest path. Docker Compose starts Postgres and qi-index together. Postgres is never exposed to the internet; it only lives on the internal Docker network.

### Requirements

- Docker 24 or later
- Docker Compose v2 (included in Docker Desktop and Docker Engine 24+)

Verify:

```sh
docker --version
docker compose version
```

### Steps

**1. Clone the repository**

```sh
git clone https://github.com/leviquai/qi-index.git
cd qi-index
```

**2. Create your environment file**

```sh
cp .env.example .env
```

Open `.env` and set your RPC endpoints. The defaults already point to the public Quai mainnet node so you can leave them as-is unless you are running your own node:

```sh
QI_RPC_URL=https://rpc.quai.network/cyprus1
QI_WS_URL=wss://rpc.quai.network/cyprus1
```

The Postgres credentials in `.env` are only used internally between the two containers. You do not need to change them.

**3. Start the stack**

```sh
docker compose up --build
```

qi-index waits for Postgres to pass its health check before starting. On first run it applies all database migrations automatically.

You should see log lines like:

```
qi-index  | time=... level=INFO msg="follower resumed" height=9900000
qi-index  | time=... level=INFO msg="api listening" addr=:8080
qi-index  | time=... level=INFO msg=following rpc=... ws=... poll=15s
```

**4. Verify it is working**

```sh
curl http://localhost:8080/tip
curl http://localhost:8080/stats
```

**Ports exposed on your host machine:**

| Port | Service |
|------|---------|
| `80` | REST and WebSocket API (via nginx, rate-limited and cached) |
| `2112` | Prometheus metrics and `/healthz` |

nginx sits in front of qi-index and handles rate limiting (200 requests per minute per IP), response caching for `/tip`, `/stats`, and `/openapi.yaml`, and WebSocket proxying. qi-index itself is not exposed directly to the host.

Postgres is not exposed to the host by default. If you want to inspect the database directly, add `POSTGRES_PORT=5432` to your `.env` and it will be available on `localhost:5432`.

**5. Stop the stack**

```sh
docker compose down
```

To stop and delete all indexed data (wipes the Postgres volume):

```sh
docker compose down -v
```

### Optional: Prometheus

A Prometheus instance that scrapes qi-index metrics is available behind the `observability` profile:

```sh
docker compose --profile observability up --build
```

Prometheus is then available at `http://localhost:9090`. The scrape config is at `deploy/prometheus.yml`.

---

## Option 2: Standalone

Run the binary directly against your own Postgres instance.

### Requirements

- Go 1.25 or later
- PostgreSQL 14 or later

**Install Go:** https://go.dev/dl

**Install PostgreSQL on macOS:**

```sh
brew install postgresql@16
brew services start postgresql@16
```

**Install PostgreSQL on Ubuntu/Debian:**

```sh
sudo apt-get install -y postgresql
sudo systemctl start postgresql
```

### Steps

**1. Clone the repository**

```sh
git clone https://github.com/leviquai/qi-index.git
cd qi-index
```

**2. Create the Postgres database and user**

```sh
psql postgres
```

Inside the Postgres shell:

```sql
CREATE USER qi_index WITH PASSWORD 'qi_index';
CREATE DATABASE qi_index OWNER qi_index;
\q
```

**3. Build the binary**

```sh
make build
```

This produces `./bin/qi-index`.

**4. Run the indexer**

```sh
./bin/qi-index follow \
  -rpc https://rpc.quai.network/cyprus1 \
  -ws  wss://rpc.quai.network/cyprus1 \
  -db  "postgres://qi_index:qi_index@localhost:5432/qi_index?sslmode=disable" \
  -api :8080 \
  -metrics :2112
```

qi-index applies all database migrations on startup. You do not need to run any SQL manually.

**5. Verify it is working**

```sh
curl http://localhost:8080/tip
curl http://localhost:8080/stats
```

**6. Run as a background service (Linux with systemd)**

Create `/etc/systemd/system/qi-index.service`:

```ini
[Unit]
Description=qi-index Qi UTXO indexer
After=network.target postgresql.service

[Service]
ExecStart=/usr/local/bin/qi-index follow \
  -rpc https://rpc.quai.network/cyprus1 \
  -ws  wss://rpc.quai.network/cyprus1 \
  -db  "postgres://qi_index:qi_index@localhost:5432/qi_index?sslmode=disable" \
  -api :8080 \
  -metrics :2112
Restart=on-failure
RestartSec=5
User=qi_index

[Install]
WantedBy=multi-user.target
```

Then:

```sh
sudo systemctl daemon-reload
sudo systemctl enable qi-index
sudo systemctl start qi-index
sudo journalctl -fu qi-index
```

---

## Using a config file

Both methods support a YAML config file instead of flags:

```sh
cp configs/config.example.yaml configs/config.yaml
```

Edit `configs/config.yaml` with your values. The file is picked up automatically if it exists at `configs/config.yaml`, or you can point to it explicitly:

```sh
./bin/qi-index follow -config /path/to/config.yaml
```

---

## Syncing time

On first run qi-index starts indexing from the current chain head. It does not backfill history automatically. To index from a specific block height, leave it running from that point forward. A full historical backfill from genesis is planned for a future release.

Sync speed against the public RPC is approximately 85 to 100 blocks per second during catch-up.
