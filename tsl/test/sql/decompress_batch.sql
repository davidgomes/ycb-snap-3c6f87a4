-- This file and its contents are licensed under the Timescale License.
-- Please see the included NOTICE for copyright information and
-- LICENSE-TIMESCALE for a copy of the license.

-- Test _timescaledb_functions.decompress_batch(record), which expands a
-- single compressed batch into the rows it represents.

CREATE TABLE metrics(time timestamptz NOT NULL, device_id int, value float, label text, flag bool);
SELECT create_hypertable('metrics', 'time', chunk_time_interval => interval '1 day');

INSERT INTO metrics
SELECT t, d,
       CASE WHEN extract(minute FROM t)::int % 7 = 0 THEN NULL ELSE d * extract(epoch FROM t) / 1000 END,
       CASE WHEN extract(minute FROM t)::int % 5 = 0 THEN NULL ELSE 'label_' || (extract(minute FROM t)::int % 3) END,
       CASE WHEN extract(minute FROM t)::int % 11 = 0 THEN NULL ELSE extract(minute FROM t)::int % 2 = 0 END
FROM generate_series('2024-01-01 00:00'::timestamptz, '2024-01-02 11:59', '1 minute') t,
     generate_series(1, 3) d;

-- Batch with a NULL segmentby value
INSERT INTO metrics VALUES
    ('2024-01-01 12:00:30', NULL, 1.5, NULL, NULL),
    ('2024-01-01 12:01:30', NULL, NULL, 'x', true);

CREATE TABLE metrics_uncompressed AS SELECT * FROM metrics;

ALTER TABLE metrics SET (timescaledb.compress,
                         timescaledb.compress_segmentby = 'device_id',
                         timescaledb.compress_orderby = 'time');
SELECT count(compress_chunk(c)) FROM show_chunks('metrics') c;

-- Decompress every batch of every compressed chunk of a hypertable
CREATE FUNCTION decompress_all_batches(ht regclass)
RETURNS SETOF metrics_uncompressed
LANGUAGE plpgsql AS $$
DECLARE
    compressed regclass;
BEGIN
    FOR compressed IN
        SELECT cs.compress_relid
        FROM show_chunks(ht) c
        JOIN _timescaledb_catalog.compression_settings cs ON cs.relid = c
        WHERE cs.compress_relid IS NOT NULL
    LOOP
        RETURN QUERY EXECUTE format(
            'SELECT x.* FROM %s t, LATERAL _timescaledb_functions.decompress_batch(t) '
            'AS x(time timestamptz, device_id int, value float, label text, flag bool)',
            compressed);
    END LOOP;
END;
$$;

SELECT count(*) FROM metrics_uncompressed;
SELECT count(*) FROM decompress_all_batches('metrics');

-- Decompressing all batches gives back exactly the original rows, including
-- NULLs. Both differences should be empty.
SELECT * FROM decompress_all_batches('metrics')
EXCEPT ALL
SELECT * FROM metrics_uncompressed;

SELECT * FROM metrics_uncompressed
EXCEPT ALL
SELECT * FROM decompress_all_batches('metrics');

SELECT cs.compress_relid AS "COMPRESSED_CHUNK"
FROM show_chunks('metrics') c
JOIN _timescaledb_catalog.compression_settings cs ON cs.relid = c
ORDER BY c LIMIT 1 \gset

-- Decompressing a single batch gives exactly the rows of that batch
SELECT _ts_meta_count AS "BATCH_COUNT", _ts_meta_min_1 AS "BATCH_MIN", _ts_meta_max_1 AS "BATCH_MAX"
FROM :COMPRESSED_CHUNK
WHERE device_id = 2
ORDER BY _ts_meta_min_1 DESC LIMIT 1 \gset

CREATE TEMP TABLE one_batch AS
SELECT x.*
FROM (SELECT * FROM :COMPRESSED_CHUNK
      WHERE device_id = 2
      ORDER BY _ts_meta_min_1 DESC LIMIT 1) t
CROSS JOIN LATERAL _timescaledb_functions.decompress_batch(t)
    AS x(time timestamptz, device_id int, value float, label text, flag bool);

SELECT count(*) = :BATCH_COUNT AS count_matches, min(time), max(time) FROM one_batch;

SELECT * FROM one_batch
EXCEPT ALL
SELECT * FROM metrics_uncompressed
WHERE device_id = 2 AND time BETWEEN :'BATCH_MIN' AND :'BATCH_MAX';

SELECT * FROM metrics_uncompressed
WHERE device_id = 2 AND time BETWEEN :'BATCH_MIN' AND :'BATCH_MAX'
EXCEPT ALL
SELECT * FROM one_batch;

-- The batch with the NULL segmentby value
SELECT x.*
FROM :COMPRESSED_CHUNK t
CROSS JOIN LATERAL _timescaledb_functions.decompress_batch(t)
    AS x(time timestamptz, device_id int, value float, label text, flag bool)
WHERE t.device_id IS NULL;

-- Rows are returned in the order of the batch, columns are matched by name
-- and can be a subset of the batch columns in any order
SELECT x.*
FROM (SELECT * FROM :COMPRESSED_CHUNK
      WHERE device_id = 1
      ORDER BY _ts_meta_min_1 LIMIT 1) t
CROSS JOIN LATERAL ROWS FROM (_timescaledb_functions.decompress_batch(t)
    AS (flag bool, value float, time timestamptz)) WITH ORDINALITY AS x(flag, value, time, n)
ORDER BY n
LIMIT 5;

-- Anonymous records work as well
SELECT x.*
FROM (SELECT device_id, _ts_meta_count, time, value, _ts_meta_min_1
      FROM :COMPRESSED_CHUNK
      WHERE device_id = 3
      ORDER BY _ts_meta_min_1 LIMIT 1) t
CROSS JOIN LATERAL _timescaledb_functions.decompress_batch(t)
    AS x(time timestamptz, device_id int, value float)
LIMIT 3;

-- NULL input returns no rows
SELECT * FROM _timescaledb_functions.decompress_batch(NULL::record) AS x(a int);

-- Dropped columns and columns added after compression
ALTER TABLE metrics DROP COLUMN label;
ALTER TABLE metrics ADD COLUMN extra int;

SELECT x.*
FROM (SELECT * FROM :COMPRESSED_CHUNK
      WHERE device_id = 1
      ORDER BY _ts_meta_min_1 LIMIT 1) t
CROSS JOIN LATERAL _timescaledb_functions.decompress_batch(t)
    AS x(time timestamptz, device_id int, value float, flag bool, extra int)
LIMIT 3;

-- Errors
CREATE TYPE fake_batch_wrong_count_type AS (_ts_meta_count bigint, device_id int);
CREATE TYPE fake_batch AS (_ts_meta_count int, device_id int);

\set ON_ERROR_STOP 0
-- Records that are not compressed batches
SELECT * FROM _timescaledb_functions.decompress_batch(ROW(1, 2)) AS x(a int, b int);
SELECT x.* FROM metrics_uncompressed m
CROSS JOIN LATERAL _timescaledb_functions.decompress_batch(m) AS x(time timestamptz);
SELECT * FROM _timescaledb_functions.decompress_batch(ROW(1, 2)::fake_batch_wrong_count_type)
    AS x(device_id int);
SELECT * FROM _timescaledb_functions.decompress_batch(ROW(NULL, 2)::fake_batch)
    AS x(device_id int);

-- Types in the column definition list that do not match the batch
SELECT x.* FROM :COMPRESSED_CHUNK t
CROSS JOIN LATERAL _timescaledb_functions.decompress_batch(t)
    AS x(time timestamptz, device_id text, value float);
SELECT x.* FROM :COMPRESSED_CHUNK t
CROSS JOIN LATERAL _timescaledb_functions.decompress_batch(t)
    AS x(time text, device_id int, value float);
SELECT x.* FROM :COMPRESSED_CHUNK t
CROSS JOIN LATERAL _timescaledb_functions.decompress_batch(t)
    AS x(time timestamptz, device_id int, value text);
SELECT x.* FROM :COMPRESSED_CHUNK t
CROSS JOIN LATERAL _timescaledb_functions.decompress_batch(t)
    AS x(time timestamptz, flag int);

-- A column definition list is required
SELECT _timescaledb_functions.decompress_batch(t) FROM :COMPRESSED_CHUNK t;
\set ON_ERROR_STOP 1

-- The errors about the input are user-facing invalid parameter errors
CREATE FUNCTION error_code(query text) RETURNS text LANGUAGE plpgsql AS $$
BEGIN
    EXECUTE query;
    RETURN 'no error';
EXCEPTION WHEN OTHERS THEN
    RETURN SQLSTATE;
END;
$$;

SELECT q, error_code(q) FROM (VALUES
    ($$ SELECT * FROM _timescaledb_functions.decompress_batch(ROW(1, 2)) AS x(a int, b int) $$),
    ($$ SELECT * FROM _timescaledb_functions.decompress_batch(ROW(1, 2)::fake_batch_wrong_count_type) AS x(device_id int) $$),
    ($$ SELECT * FROM _timescaledb_functions.decompress_batch(ROW(NULL, 2)::fake_batch) AS x(device_id int) $$),
    (format($$ SELECT x.* FROM %s t, _timescaledb_functions.decompress_batch(t) AS x(device_id text) $$, :'COMPRESSED_CHUNK')),
    (format($$ SELECT x.* FROM %s t, _timescaledb_functions.decompress_batch(t) AS x(time text) $$, :'COMPRESSED_CHUNK')),
    (format($$ SELECT x.* FROM %s t, _timescaledb_functions.decompress_batch(t) AS x(flag int) $$, :'COMPRESSED_CHUNK'))
) v(q);

DROP FUNCTION error_code(text);
DROP FUNCTION decompress_all_batches(regclass);
DROP TYPE fake_batch;
DROP TYPE fake_batch_wrong_count_type;
DROP TABLE metrics_uncompressed;
DROP TABLE metrics;
