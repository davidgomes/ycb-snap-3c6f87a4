-- This file and its contents are licensed under the Timescale License.
-- Please see the included NOTICE for copyright information and
-- LICENSE-TIMESCALE for a copy of the license.

\set VERBOSITY default

CREATE TABLE metrics(time timestamptz NOT NULL, device_id int, value float, note text);
SELECT table_name FROM create_hypertable('metrics', 'time', chunk_time_interval => interval '10 days');
ALTER TABLE metrics SET (timescaledb.compress,
    timescaledb.compress_segmentby = 'device_id',
    timescaledb.compress_orderby = 'time');

-- Three segments per chunk (including a NULL segment), spanning more than one
-- batch each. The value and note columns contain NULLs.
INSERT INTO metrics
SELECT t,
    CASE WHEN d = 0 THEN NULL ELSE d END,
    CASE WHEN extract(minute FROM t)::int % 7 = 0 THEN NULL ELSE d * 100 + extract(epoch FROM t) / 3600 END,
    CASE WHEN (extract(epoch FROM t)::int / 600) % 5 = 0 THEN NULL ELSE 'note ' || d END
FROM generate_series('2024-01-01'::timestamptz, '2024-01-15'::timestamptz, interval '10 minutes') t,
    generate_series(0, 2) d;

SELECT count(compress_chunk(c)) FROM show_chunks('metrics') c;

SELECT format('%I.%I', cc.schema_name, cc.table_name) AS "COMPRESSED_CHUNK",
    format('%I.%I', ch.schema_name, ch.table_name) AS "CHUNK"
FROM _timescaledb_catalog.chunk ch
JOIN _timescaledb_catalog.chunk cc ON cc.id = ch.compressed_chunk_id
JOIN _timescaledb_catalog.hypertable ht ON ht.id = ch.hypertable_id
WHERE ht.table_name = 'metrics'
ORDER BY ch.id DESC LIMIT 1 \gset

-- Decompressing all batches is set-equal to the hypertable data
CREATE TEMP TABLE decompressed(time timestamptz, device_id int, value float, note text);
DO $$
DECLARE
    rel regclass;
BEGIN
    FOR rel IN
        SELECT format('%I.%I', cc.schema_name, cc.table_name)::regclass
        FROM _timescaledb_catalog.chunk ch
        JOIN _timescaledb_catalog.chunk cc ON cc.id = ch.compressed_chunk_id
        JOIN _timescaledb_catalog.hypertable ht ON ht.id = ch.hypertable_id
        WHERE ht.table_name = 'metrics'
    LOOP
        EXECUTE format($sql$
            INSERT INTO decompressed
            SELECT x.*
            FROM %s t
            CROSS JOIN LATERAL _timescaledb_functions.decompress_batch(t)
                AS x(time timestamptz, device_id int, value float, note text)
        $sql$, rel);
    END LOOP;
END;
$$;

SELECT (SELECT count(*) FROM metrics) AS hypertable_rows,
    (SELECT count(*) FROM decompressed) AS decompressed_rows,
    (SELECT count(*) FROM metrics WHERE value IS NULL) AS hypertable_null_values,
    (SELECT count(*) FROM decompressed WHERE value IS NULL) AS decompressed_null_values,
    (SELECT count(*) FROM decompressed WHERE device_id IS NULL) AS decompressed_null_devices;

SELECT count(*) AS missing FROM (SELECT * FROM metrics EXCEPT ALL SELECT * FROM decompressed) s;
SELECT count(*) AS extra FROM (SELECT * FROM decompressed EXCEPT ALL SELECT * FROM metrics) s;

-- Decompressing a single batch yields exactly the rows of that batch
SELECT count(*) AS batches, sum(_ts_meta_count) AS rows FROM :COMPRESSED_CHUNK;

SELECT t.device_id, t._ts_meta_count,
    (SELECT count(*) FROM _timescaledb_functions.decompress_batch(t)
        AS x(time timestamptz, device_id int, value float, note text)) AS decompressed_rows,
    (SELECT count(*) FROM (
        SELECT * FROM :CHUNK c
        WHERE c.device_id IS NOT DISTINCT FROM t.device_id
            AND c.time BETWEEN t._ts_meta_min_1 AND t._ts_meta_max_1
        EXCEPT ALL
        SELECT * FROM _timescaledb_functions.decompress_batch(t)
            AS x(time timestamptz, device_id int, value float, note text)) s) AS missing,
    (SELECT count(*) FROM (
        SELECT * FROM _timescaledb_functions.decompress_batch(t)
            AS x(time timestamptz, device_id int, value float, note text)
        EXCEPT ALL
        SELECT * FROM :CHUNK c
        WHERE c.device_id IS NOT DISTINCT FROM t.device_id
            AND c.time BETWEEN t._ts_meta_min_1 AND t._ts_meta_max_1) s) AS extra
FROM :COMPRESSED_CHUNK t
ORDER BY t.device_id NULLS FIRST, t._ts_meta_min_1;

-- Rows come out in batch order
SELECT x.*
FROM (SELECT * FROM :COMPRESSED_CHUNK WHERE device_id = 2 ORDER BY _ts_meta_min_1 LIMIT 1) t
CROSS JOIN LATERAL _timescaledb_functions.decompress_batch(t)
    AS x(time timestamptz, device_id int, value float, note text)
LIMIT 5;

-- A subset of the columns, in any order
SELECT x.*
FROM (SELECT * FROM :COMPRESSED_CHUNK WHERE device_id = 2 ORDER BY _ts_meta_min_1 LIMIT 1) t
CROSS JOIN LATERAL _timescaledb_functions.decompress_batch(t) AS x(note text, time timestamptz)
LIMIT 3;

-- NULL input yields no rows since the function is strict
SELECT count(*) FROM _timescaledb_functions.decompress_batch(NULL::record) AS x(time timestamptz);

\set ON_ERROR_STOP 0
-- Not a compressed batch
SELECT * FROM (SELECT * FROM metrics LIMIT 1) t
CROSS JOIN LATERAL _timescaledb_functions.decompress_batch(t) AS x(time timestamptz);
SELECT * FROM _timescaledb_functions.decompress_batch(ROW(1, 'foo')) AS x(f1 int);
CREATE TABLE fake_batch(device_id int, _ts_meta_count text);
INSERT INTO fake_batch VALUES (1, '10');
SELECT * FROM fake_batch t
CROSS JOIN LATERAL _timescaledb_functions.decompress_batch(t) AS x(device_id int);
DROP TABLE fake_batch;
CREATE TABLE fake_batch(device_id int, _ts_meta_count int);
INSERT INTO fake_batch VALUES (1, NULL);
SELECT * FROM fake_batch t
CROSS JOIN LATERAL _timescaledb_functions.decompress_batch(t) AS x(device_id int);
DROP TABLE fake_batch;

-- Output shape does not match the batch
SELECT x.* FROM (SELECT * FROM :COMPRESSED_CHUNK ORDER BY device_id, _ts_meta_min_1 LIMIT 1) t
CROSS JOIN LATERAL _timescaledb_functions.decompress_batch(t)
    AS x(time timestamptz, device_id text, value float, note text);
SELECT x.* FROM :COMPRESSED_CHUNK t
CROSS JOIN LATERAL _timescaledb_functions.decompress_batch(t)
    AS x(time timestamptz, device_id int, value int, note text);
SELECT x.* FROM :COMPRESSED_CHUNK t
CROSS JOIN LATERAL _timescaledb_functions.decompress_batch(t)
    AS x(time timestamp, device_id int);
-- Without the uncompressed relation, types are checked against the
-- compressed data itself
SELECT x.* FROM (SELECT * FROM :COMPRESSED_CHUNK ORDER BY device_id, _ts_meta_min_1 LIMIT 1) t
CROSS JOIN LATERAL _timescaledb_functions.decompress_batch(t)
    AS x(time timestamptz, device_id int, value text);
SELECT x.* FROM (SELECT * FROM :COMPRESSED_CHUNK ORDER BY device_id, _ts_meta_min_1 LIMIT 1) t
CROSS JOIN LATERAL _timescaledb_functions.decompress_batch(t)
    AS x(time timestamptz, device_id int, note int);
SELECT x.* FROM (SELECT * FROM :COMPRESSED_CHUNK ORDER BY device_id, _ts_meta_min_1 LIMIT 1) t
CROSS JOIN LATERAL _timescaledb_functions.decompress_batch(t)
    AS x(time float, device_id int);
SELECT x.* FROM (SELECT * FROM :COMPRESSED_CHUNK ORDER BY device_id, _ts_meta_min_1 LIMIT 1) t
CROSS JOIN LATERAL _timescaledb_functions.decompress_batch(t)
    AS x(time timestamptz, device_id int, missing float);
SELECT x.* FROM (SELECT * FROM :COMPRESSED_CHUNK ORDER BY device_id, _ts_meta_min_1 LIMIT 1) t
CROSS JOIN LATERAL _timescaledb_functions.decompress_batch(t)
    AS x(time timestamptz, _ts_meta_count int);

-- A column definition list is required
SELECT _timescaledb_functions.decompress_batch(t) FROM (SELECT * FROM :COMPRESSED_CHUNK ORDER BY device_id, _ts_meta_min_1 LIMIT 1) t;
\set ON_ERROR_STOP 1

DROP TABLE metrics;
