# qi-index

qi-index is an open source, self hostable indexer and API for the Qi UTXO ledger on Quai Network. It tracks the chain reorg-safely, stores complete UTXO history including trimmed denominations and coinbase lockups, and serves that history through a REST and WebSocket API that makes quais.js wallet sync fast.

No equivalent tool exists for the Qi ledger. The node exposes current unspent outpoints only. qi-index is the only way to query spent history, trimmed UTXOs, and full transaction views without running your own archive node.

## Why this exists

Wallets like Pelagus and Blippay already support Qi, and they do it by
querying `quai_getOutpointsByAddress` on a node live. That's the right way
to show a _current_ balance but it's a ceiling, not a limitation of those
wallets: the node itself only tracks unspent outpoints. Once a UTXO is spent,
or trimmed by the protocol's denomination based retention rules, the node
has nothing left to return for it. There is no "get transaction history for
this address" on the node, because the node was never built to keep that
history the protocol makes retaining every trimmed and spent UTXO forever
a deliberate node-level tradeoff it doesn't make.

qi-index isn't another wallet or another way to check a balance. It's the
permanent record layer underneath wallets like Pelagus and Blippay: it
follows the chain reorg safely from genesis, decodes and stores every UTXO
event as it happens, and keeps the spent and trimmed ones the node discards.
That turns "what's my balance right now" into "what happened to this
address, ever"; full history, block-level UTXO views, and transaction
lookups; served over a REST/WS API a wallet (or a block explorer, or
anything else) can query instead of replaying the whole chain itself.

Privacy is part of the design, not a bolt-on: every query is scoped to a
single address you already hold, there's no clustering, no heuristic
address linking, no cross address graph analysis. And because it's
self hostable, you're never handing that query history to a third party;
run your own instance and only you (or your node) ever sees what addresses
you're asking about.

## Benchmark

`POST /outpoints` resolved 78 addresses and 1218 outpoints in **63ms**. The equivalent sequential node RPC (`quai_getOutpointsByAddress` per address) took **23.5 seconds** for 20 addresses on the public endpoint.

## API

Enable the API with `-api :8080` or `QI_API_ADDR=:8080`. The full OpenAPI spec is at `GET /openapi.yaml` or browseable at [`internal/api/openapi.yaml`](internal/api/openapi.yaml).

| Method | Path                      | Description                                                                      |
| ------ | ------------------------- | -------------------------------------------------------------------------------- |
| GET    | `/tip`                    | Current indexed chain tip                                                        |
| GET    | `/stats`                  | Total UTXOs, block coverage, and aggregate value                                 |
| POST   | `/outpoints`              | Batch unspent outpoints for up to 1000 addresses, node-compatible response shape |
| GET    | `/address/{addr}/balance` | Spendable, locked, and trimmed balance                                           |
| GET    | `/address/{addr}/utxos`   | All unspent UTXOs for an address                                                 |
| GET    | `/address/{addr}/history` | Paginated full UTXO history including spent and trimmed                          |
| GET    | `/utxo/{txhash}/{index}`  | Single outpoint lookup                                                           |
| GET    | `/tx/{txhash}`            | All UTXOs created and spent by a transaction                                     |
| GET    | `/block/{height}/utxos`   | Created, spent, and trimmed UTXOs at a block height                              |
| GET    | `/ws`                     | WebSocket, live block UTXO events pushed after each block                        |
| GET    | `/openapi.yaml`           | OpenAPI 3.1 spec                                                                 |

### quais.js drop-in provider

Switch a `QiHDWallet` from the node to qi-index by swapping the provider. No other code changes needed.

```js
const { QiIndexProvider } = require("./examples/quaisjs-provider");
const provider = new QiIndexProvider("http://localhost:8080");
wallet.connect(provider);
await wallet.scan(zone);
```

See [`examples/quaisjs-provider.js`](examples/quaisjs-provider.js) for the full implementation.

## Configuration

Settings layer in this order, each overriding only what it sets: built-in defaults, YAML config file, environment variables, CLI flags.

| Setting         | Config key      | Env var            | Flag       | Default                            |
| --------------- | --------------- | ------------------ | ---------- | ---------------------------------- |
| RPC URL         | `rpc_url`       | `QI_RPC_URL`       | `-rpc`     | `https://rpc.quai.network/cyprus1` |
| WS URL          | `ws_url`        | `QI_WS_URL`        | `-ws`      | `wss://rpc.quai.network/cyprus1`   |
| Postgres URL    | `database_url`  | `QI_DATABASE_URL`  | `-db`      | empty, uses in-memory store        |
| Poll interval   | `poll_interval` | `QI_POLL_INTERVAL` | `-poll`    | `15s`                              |
| Reorg depth     | `reorg_depth`   | `QI_REORG_DEPTH`   | `-depth`   | `1024`                             |
| Metrics address | `metrics_addr`  | `QI_METRICS_ADDR`  | `-metrics` | empty, disabled                    |
| API address     | `api_addr`      | `QI_API_ADDR`      | `-api`     | empty, disabled                    |
| Verbose logging | `verbose`       | `QI_VERBOSE`       | `-v`       | `false`                            |

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
