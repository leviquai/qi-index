-- Migrate utxos to a range-partitioned table keyed on created_block.
-- Partition key must be part of the primary key, so PK becomes
-- (tx_hash, tx_index, created_block). The decoder ON CONFLICT clauses
-- are updated to match. Partition boundaries use 2 M-block windows.

DROP INDEX utxos_address_unspent;
DROP INDEX utxos_address_created;
DROP INDEX utxos_address_spent;
DROP INDEX utxos_trim_sweep;
DROP INDEX utxos_created_block;
DROP INDEX utxos_spent_block;

ALTER TABLE utxos RENAME TO utxos_legacy;

CREATE TABLE utxos (
    tx_hash        BYTEA    NOT NULL,
    tx_index       INTEGER  NOT NULL,
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
    PRIMARY KEY (tx_hash, tx_index, created_block)
) PARTITION BY RANGE (created_block);

CREATE TABLE utxos_p0       PARTITION OF utxos FOR VALUES FROM (0)        TO (2000000);
CREATE TABLE utxos_p1       PARTITION OF utxos FOR VALUES FROM (2000000)  TO (4000000);
CREATE TABLE utxos_p2       PARTITION OF utxos FOR VALUES FROM (4000000)  TO (6000000);
CREATE TABLE utxos_p3       PARTITION OF utxos FOR VALUES FROM (6000000)  TO (8000000);
CREATE TABLE utxos_p4       PARTITION OF utxos FOR VALUES FROM (8000000)  TO (10000000);
CREATE TABLE utxos_p5       PARTITION OF utxos FOR VALUES FROM (10000000) TO (12000000);
CREATE TABLE utxos_p6       PARTITION OF utxos FOR VALUES FROM (12000000) TO (14000000);
CREATE TABLE utxos_default  PARTITION OF utxos DEFAULT;

CREATE INDEX utxos_address_unspent
    ON utxos (address)
    WHERE spent_block IS NULL AND trimmed_block IS NULL;

CREATE INDEX utxos_address_created
    ON utxos (address, created_block DESC);

CREATE INDEX utxos_address_spent
    ON utxos (address, spent_block DESC)
    WHERE spent_block IS NOT NULL;

CREATE INDEX utxos_trim_sweep
    ON utxos (trim_deadline)
    WHERE trim_deadline IS NOT NULL
      AND spent_block   IS NULL
      AND trimmed_block IS NULL
      AND lock_height   = 0;

CREATE INDEX utxos_created_block ON utxos (created_block);
CREATE INDEX utxos_spent_block   ON utxos (spent_block) WHERE spent_block IS NOT NULL;

INSERT INTO utxos SELECT * FROM utxos_legacy;

DROP TABLE utxos_legacy;
