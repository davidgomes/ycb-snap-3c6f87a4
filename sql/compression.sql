-- This file and its contents are licensed under the Apache License 2.0.
-- Please see the included NOTICE for copyright information and
-- LICENSE-APACHE for a copy of the license.

CREATE OR REPLACE FUNCTION _timescaledb_functions.compressed_data_to_array(_timescaledb_internal.compressed_data, ANYELEMENT)
   RETURNS ANYARRAY
   AS '@MODULE_PATHNAME@', 'ts_compressed_data_to_array'
   LANGUAGE C IMMUTABLE PARALLEL SAFE;

CREATE OR REPLACE FUNCTION _timescaledb_functions.compressed_data_column_size(_timescaledb_internal.compressed_data, ANYELEMENT)
   RETURNS BIGINT
   AS '@MODULE_PATHNAME@', 'ts_compressed_data_column_size'
   LANGUAGE C IMMUTABLE PARALLEL SAFE;

-- Decompress a single compressed batch (a row of a compressed chunk) into the
-- rows it represents. The output row shape is given by the column definition
-- list at the call site, e.g. decompress_batch(t) AS x(time timestamptz, value float).
CREATE OR REPLACE FUNCTION _timescaledb_functions.decompress_batch(record)
   RETURNS SETOF RECORD
   AS '@MODULE_PATHNAME@', 'ts_decompress_batch'
   LANGUAGE C STRICT STABLE;

