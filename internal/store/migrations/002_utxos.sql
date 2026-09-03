-- One row per outpoint. Spend and trim fill columns in-place.
-- source: 0=qi_tx, 1=coinbase ETX, 2=conversion ETX.

CREATE TABLE utxos (
    tx_hash        BYTEA    NOT NULL,
    tx_index       SMALLINT NOT NULL,
    address        BYTEA    NOT NULL,
    zone           SMALLINT NOT NULL DEFAULT 0,
    denomination   SMALLINT NOT NULL,
    lock_height    BIGINT   NOT NULL DEFAULT 0,
    source         SMALLINT NOT NULL,
    created_block  BIGINT   NOT NULL,
    created_tx     BYTEA    NOT NULL,
    spent_block    BIGINT,
    spent_tx       BYTEA,
    spender_pubkey BYTEA,
    trim_deadline  BIGINT,
    trimmed_block  BIGINT,
    PRIMARY KEY (tx_hash, tx_index)
);

-- hot path: spendable UTXOs for an address (wallet balance query)
CREATE INDEX utxos_address_unspent
    ON utxos (address)
    WHERE spent_block IS NULL AND trimmed_block IS NULL;

-- history: all UTXOs created for an address, newest first
CREATE INDEX utxos_address_created
    ON utxos (address, created_block DESC);

-- history: spent UTXOs for an address, newest spend first
CREATE INDEX utxos_address_spent
    ON utxos (address, spent_block DESC)
    WHERE spent_block IS NOT NULL;

-- trim sweep: only unspent, unlocked, unset trimmed rows with a deadline
CREATE INDEX utxos_trim_sweep
    ON utxos (trim_deadline)
    WHERE trim_deadline IS NOT NULL
      AND spent_block  IS NULL
      AND trimmed_block IS NULL
      AND lock_height = 0;

-- reorg rollback scans
CREATE INDEX utxos_created_block ON utxos (created_block);
CREATE INDEX utxos_spent_block ON utxos (spent_block) WHERE spent_block IS NOT NULL;

-- Quai-scope outputs from type-0x2 Qi txs; not UTXOs, tracked for conversion history.
CREATE TABLE conversion_outputs (
    tx_hash      BYTEA    NOT NULL,
    output_index SMALLINT NOT NULL,
    to_address   BYTEA    NOT NULL,
    denomination SMALLINT NOT NULL,
    block_height BIGINT   NOT NULL,
    PRIMARY KEY (tx_hash, output_index)
);

CREATE INDEX conversion_outputs_block ON conversion_outputs (block_height);
CREATE INDEX conversion_outputs_addr  ON conversion_outputs (to_address);
