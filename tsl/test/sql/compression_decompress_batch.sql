-- This file and its contents are licensed under the Timescale License.
-- Please see the included NOTICE for copyright information and
-- LICENSE-TIMESCALE for a copy of the license.

\c :TEST_DBNAME :ROLE_DEFAULT_PERM_USER

CREATE TABLE decompress_batch_test(
    time timestamptz NOT NULL,
    device_id integer NOT NULL,
    value double precision
);
SELECT create_hypertable('decompress_batch_test', 'time', chunk_time_interval => '1 day'::interval);
ALTER TABLE decompress_batch_test
SET (
    timescaledb.compress,
    timescaledb.compress_segmentby = 'device_id',
    timescaledb.compress_orderby = 'time'
);

INSERT INTO decompress_batch_test
SELECT '2024-01-01'::timestamptz + make_interval(secs => i),
       CASE WHEN i <= 1200 THEN 1 ELSE 2 END,
       CASE WHEN i % 7 = 0 THEN NULL ELSE i::double precision END
FROM generate_series(1, 1203) AS i;
CREATE TABLE decompress_batch_expected AS TABLE decompress_batch_test;

SELECT compress_chunk(show_chunks('decompress_batch_test'));

SELECT format('%I.%I', compressed.schema_name, compressed.table_name) AS compressed_chunk
FROM _timescaledb_catalog.chunk uncompressed
JOIN _timescaledb_catalog.chunk compressed
  ON compressed.id = uncompressed.compressed_chunk_id
WHERE uncompressed.hypertable_id = (
    SELECT id
    FROM _timescaledb_catalog.hypertable
    WHERE table_name = 'decompress_batch_test'
)
\gset

WITH actual AS (
    SELECT x.*
    FROM :compressed_chunk t
    CROSS JOIN LATERAL _timescaledb_functions.decompress_batch(t)
        AS x(time timestamptz, device_id integer, value double precision)
),
diff AS (
    (SELECT * FROM actual EXCEPT ALL SELECT * FROM decompress_batch_expected)
    UNION ALL
    (SELECT * FROM decompress_batch_expected EXCEPT ALL SELECT * FROM actual)
)
SELECT count(*) AS decompression_difference_count FROM diff;

SELECT count(*) = min(t._ts_meta_count) AS one_batch_matches_metadata_count
FROM :compressed_chunk t
CROSS JOIN LATERAL _timescaledb_functions.decompress_batch(t)
    AS x(time timestamptz, device_id integer, value double precision)
WHERE t.device_id = 2;

WITH actual AS (
    SELECT x.*
    FROM :compressed_chunk t
    CROSS JOIN LATERAL _timescaledb_functions.decompress_batch(t)
        AS x(time timestamptz, device_id integer, value double precision)
    WHERE t.device_id = 2
),
diff AS (
    (SELECT * FROM actual EXCEPT ALL
     SELECT * FROM decompress_batch_expected WHERE device_id = 2)
    UNION ALL
    (SELECT * FROM decompress_batch_expected WHERE device_id = 2
     EXCEPT ALL SELECT * FROM actual)
)
SELECT count(*) AS one_batch_difference_count FROM diff;

CREATE TABLE not_a_compressed_batch(value integer);
CREATE TABLE wrong_batch_metadata(value integer, _ts_meta_count text);

\set ON_ERROR_STOP 0
SELECT *
FROM not_a_compressed_batch t
CROSS JOIN LATERAL _timescaledb_functions.decompress_batch(t) AS x(value integer);
SELECT *
FROM wrong_batch_metadata t
CROSS JOIN LATERAL _timescaledb_functions.decompress_batch(t) AS x(value integer);
SELECT x.*
FROM (
    SELECT *
    FROM :compressed_chunk
    LIMIT 1
) t
CROSS JOIN LATERAL _timescaledb_functions.decompress_batch(t)
    AS x(time timestamptz, device_id text, value double precision);
\set ON_ERROR_STOP 1

DROP TABLE wrong_batch_metadata;
DROP TABLE not_a_compressed_batch;
DROP TABLE decompress_batch_expected;
DROP TABLE decompress_batch_test;
