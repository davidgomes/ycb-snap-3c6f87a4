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

-- Materialize the user rows stored in a single compressed-batch record.
-- The input row type is taken from the record; the output shape is the
-- call site's column definition list.
CREATE OR REPLACE FUNCTION _timescaledb_functions.decompress_batch(record)
   RETURNS SETOF record
   AS '@MODULE_PATHNAME@', 'ts_decompress_batch'
   LANGUAGE C STRICT;

