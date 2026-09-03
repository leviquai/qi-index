/**
 * qi-index drop-in provider for quais.js QiHDWallet.
 *
 * Replaces the default node RPC calls for Qi outpoint lookups with
 * a single POST /outpoints request to qi-index, which is orders of
 * magnitude faster than querying the node per-address.
 *
 * Usage:
 *   const { QiHDWallet } = require('quais');
 *   const { QiIndexProvider } = require('./quaisjs-provider');
 *
 *   const provider = new QiIndexProvider('https://your-qi-index-host');
 *   const wallet = QiHDWallet.fromMnemonic(mnemonic);
 *   wallet.connect(provider);
 *   await wallet.scan(zone);
 */

'use strict';

const { JsonRpcProvider } = require('quais');

class QiIndexProvider extends JsonRpcProvider {
  constructor(qiIndexURL, nodeURL, network) {
    super(nodeURL ?? 'https://rpc.quai.network/cyprus1', network);
    this._qiIndexURL = qiIndexURL.replace(/\/$/, '');
  }

  async getOutpointsByAddresses(addresses) {
    const res = await fetch(`${this._qiIndexURL}/outpoints`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ addresses }),
    });
    if (!res.ok) {
      throw new Error(`qi-index /outpoints ${res.status}: ${await res.text()}`);
    }
    const raw = await res.json();
    const out = new Map();
    for (const [addr, outpoints] of Object.entries(raw)) {
      out.set(addr, (outpoints ?? []).map(op => ({
        txhash: op.txHash,
        index: parseInt(op.index, 16),
        denomination: parseInt(op.denomination, 16),
        lock: op.lock !== undefined ? parseInt(op.lock, 16) : undefined,
      })));
    }
    return out;
  }
}

module.exports = { QiIndexProvider };
