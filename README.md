# qi-index

qi-index is an open-source, self-hostable indexer and API for the Qi UTXO ledger on Quai Network. It tracks the chain reorg-safely, stores complete UTXO history including trimmed denominations and coinbase lockups, and serves that history through a REST and WebSocket API that makes quais.js wallet sync fast.

No equivalent tool exists for the Qi ledger. The node exposes current unspent outpoints only. qi-index is the only way to query spent history, trimmed UTXOs, and full transaction views without running your own archive node.

## Benchmark

`POST /outpoints` resolved 78 addresses and 1218 outpoints in **63ms**. The equivalent sequential node RPC (`quai_getOutpointsByAddress` per address) took **23.5 seconds** for 20 addresses on the public endpoint.

## API

Enable the API with `-api :8080` or `QI_API_ADDR=:8080`. The full OpenAPI spec is at `GET /openapi.yaml` or browseable at [`internal/api/openapi.yaml`](internal/api/openapi.yaml).

| Method | Path | Description |
|--------|------|-------------|
| GET | `/tip` | Current indexed chain tip |
| GET | `/stats` | Total UTXOs, block coverage, and aggregate value |
| POST | `/outpoints` | Batch unspent outpoints for up to 1000 addresses, node-compatible response shape |
| GET | `/address/{addr}/balance` | Spendable, locked, and trimmed balance |
| GET | `/address/{addr}/utxos` | All unspent UTXOs for an address |
| GET | `/address/{addr}/history` | Paginated full UTXO history including spent and trimmed |
| GET | `/utxo/{txhash}/{index}` | Single outpoint lookup |
| GET | `/tx/{txhash}` | All UTXOs created and spent by a transaction |
| GET | `/block/{height}/utxos` | Created, spent, and trimmed UTXOs at a block height |
| GET | `/ws` | WebSocket, live block UTXO events pushed after each block |
| GET | `/openapi.yaml` | OpenAPI 3.1 spec |

### quais.js drop-in provider

Switch a `QiHDWallet` from the node to qi-index by swapping the provider. No other code changes needed.

```js
const { QiIndexProvider } = require('./examples/quaisjs-provider');
const provider = new QiIndexProvider('http://localhost:8080');
wallet.connect(provider);
await wallet.scan(zone);
```

See [`examples/quaisjs-provider.js`](examples/quaisjs-provider.js) for the full implementation.

## Configuration

Settings layer in this order, each overriding only what it sets: built-in defaults, YAML config file, environment variables, CLI flags.

| Setting | Config key | Env var | Flag | Default |
|---------|------------|---------|------|---------|
| RPC URL | `rpc_url` | `QI_RPC_URL` | `-rpc` | `https://rpc.quai.network/cyprus1` |
| WS URL | `ws_url` | `QI_WS_URL` | `-ws` | `wss://rpc.quai.network/cyprus1` |
| Postgres URL | `database_url` | `QI_DATABASE_URL` | `-db` | empty, uses in-memory store |
| Poll interval | `poll_interval` | `QI_POLL_INTERVAL` | `-poll` | `15s` |
| Reorg depth | `reorg_depth` | `QI_REORG_DEPTH` | `-depth` | `1024` |
| Metrics address | `metrics_addr` | `QI_METRICS_ADDR` | `-metrics` | empty, disabled |
| API address | `api_addr` | `QI_API_ADDR` | `-api` | empty, disabled |
| Verbose logging | `verbose` | `QI_VERBOSE` | `-v` | `false` |

An empty WS URL falls back to polling only. An empty Postgres URL uses an in-memory store that does not survive restarts.

See [`configs/config.example.yaml`](configs/config.example.yaml) for the YAML format.

## Running

See [docs/setup.md](docs/setup.md) for full setup instructions covering Docker Compose and standalone installs.

## Tests

```sh
make test
```

Integration tests against a real Postgres:

```sh
make test-integration
```
