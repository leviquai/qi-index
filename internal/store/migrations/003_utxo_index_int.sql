-- tx_index and output_index must be INTEGER (int4) not SMALLINT (int2):
-- conversion ETX outputs can have indices up to MaxOutputIndex (65535 = MaxUint16)
-- which exceeds int2 range (32767).
ALTER TABLE utxos ALTER COLUMN tx_index TYPE INTEGER;
ALTER TABLE conversion_outputs ALTER COLUMN output_index TYPE INTEGER;
