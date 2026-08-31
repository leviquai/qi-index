CREATE TABLE blocks (
    hash        TEXT PRIMARY KEY,
    parent_hash TEXT NOT NULL,
    height      BIGINT NOT NULL
);

CREATE INDEX blocks_height_idx ON blocks (height);

CREATE TABLE meta (
    key   TEXT PRIMARY KEY,
    value TEXT NOT NULL
);
