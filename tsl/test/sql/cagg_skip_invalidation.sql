-- This file and its contents are licensed under the Timescale License.
-- Please see the included NOTICE for copyright information and
-- LICENSE-TIMESCALE for a copy of the license.

-- timescaledb.skip_cagg_invalidation lets a bulk load skip continuous
-- aggregate invalidation logging. The caller owns refresh afterwards.

\c :TEST_DBNAME :ROLE_SUPERUSER
SELECT _timescaledb_functions.stop_background_workers();
SET ROLE :ROLE_DEFAULT_PERM_USER;
SET timezone TO 'UTC';
SET datestyle TO 'ISO, YMD';

-- Default is off, so ordinary DML still records invalidations.
SHOW timescaledb.skip_cagg_invalidation;

CREATE TABLE metrics (time timestamptz NOT NULL, device int, value double precision);
SELECT create_hypertable('metrics', 'time', chunk_time_interval => interval '1 day');

CREATE MATERIALIZED VIEW metrics_hourly
WITH (timescaledb.continuous, timescaledb.materialized_only = true) AS
SELECT time_bucket('1 hour', time) AS bucket, device, avg(value) AS avg_value
FROM metrics
GROUP BY 1, 2
WITH NO DATA;

CREATE MATERIALIZED VIEW metrics_daily
WITH (timescaledb.continuous, timescaledb.materialized_only = true) AS
SELECT time_bucket('1 day', bucket) AS bucket, device, avg(avg_value) AS avg_value
FROM metrics_hourly
GROUP BY 1, 2
WITH NO DATA;

-- Creating a continuous aggregate still records its initial invalidation.
SELECT count(*) AS mat_invals_after_create
FROM _timescaledb_catalog.continuous_aggs_materialization_invalidation_log;

INSERT INTO metrics VALUES ('2024-01-01 00:00:00+00', 1, 10);
CALL refresh_continuous_aggregate('metrics_hourly', NULL, NULL);
CALL refresh_continuous_aggregate('metrics_daily', NULL, NULL);

\c :TEST_DBNAME :ROLE_SUPERUSER
TRUNCATE _timescaledb_catalog.continuous_aggs_hypertable_invalidation_log,
         _timescaledb_catalog.continuous_aggs_materialization_invalidation_log;
\c :TEST_DBNAME :ROLE_DEFAULT_PERM_USER
SET timezone TO 'UTC';
SET datestyle TO 'ISO, YMD';

-- Repeatable read always appends an invalidation, independent of the watermark.
BEGIN ISOLATION LEVEL REPEATABLE READ;
INSERT INTO metrics VALUES ('2024-01-01 00:30:00+00', 1, 11);
COMMIT;
SELECT count(*) AS hyper_invals_default
FROM _timescaledb_catalog.continuous_aggs_hypertable_invalidation_log;

\c :TEST_DBNAME :ROLE_SUPERUSER
TRUNCATE _timescaledb_catalog.continuous_aggs_hypertable_invalidation_log,
         _timescaledb_catalog.continuous_aggs_materialization_invalidation_log;
\c :TEST_DBNAME :ROLE_DEFAULT_PERM_USER
SET timezone TO 'UTC';
SET datestyle TO 'ISO, YMD';

SELECT format('%I.%I', chunk_schema, chunk_name) AS direct_chunk
FROM timescaledb_information.chunks
WHERE hypertable_name = 'metrics'
ORDER BY range_start
LIMIT 1 \gset

-- SET LOCAL suppresses INSERT, UPDATE, DELETE, COPY, and direct chunk writes.
BEGIN ISOLATION LEVEL REPEATABLE READ;
SET LOCAL timescaledb.skip_cagg_invalidation = on;
SHOW timescaledb.skip_cagg_invalidation;
INSERT INTO metrics VALUES ('2024-01-01 01:00:00+00', 1, 30);
UPDATE metrics SET value = 31 WHERE time = '2024-01-01 01:00:00+00';
DELETE FROM metrics WHERE time = '2024-01-01 01:00:00+00';
COPY metrics (time, device, value) FROM STDIN WITH (FORMAT csv);
2024-01-01 02:00:00+00,1,40
\.
INSERT INTO :direct_chunk VALUES ('2024-01-01 03:00:00+00', 1, 50);
COPY :direct_chunk (time, device, value) FROM STDIN WITH (FORMAT csv);
2024-01-01 04:00:00+00,1,60
\.
COMMIT;

SELECT count(*) AS hyper_invals_skipped
FROM _timescaledb_catalog.continuous_aggs_hypertable_invalidation_log;
SELECT count(*) AS mat_invals_skipped
FROM _timescaledb_catalog.continuous_aggs_materialization_invalidation_log;

-- SET LOCAL does not survive COMMIT.
SHOW timescaledb.skip_cagg_invalidation;

BEGIN ISOLATION LEVEL REPEATABLE READ;
INSERT INTO metrics VALUES ('2024-01-01 05:00:00+00', 1, 70);
COMMIT;
SELECT count(*) AS hyper_invals_after_commit
FROM _timescaledb_catalog.continuous_aggs_hypertable_invalidation_log;

-- The skipped rows are invisible to a normal refresh, and visible after force.
SELECT avg_value FROM metrics_hourly WHERE bucket = '2024-01-01 02:00:00+00';
CALL refresh_continuous_aggregate('metrics_hourly', '2024-01-01 00:00:00+00', '2024-01-02 00:00:00+00');
SELECT avg_value FROM metrics_hourly WHERE bucket = '2024-01-01 02:00:00+00' ORDER BY 1;
CALL refresh_continuous_aggregate('metrics_hourly', '2024-01-01 00:00:00+00', '2024-01-02 00:00:00+00', force => true);
SELECT bucket, avg_value
FROM metrics_hourly
WHERE bucket >= '2024-01-01 00:00:00+00' AND bucket < '2024-01-02 00:00:00+00'
ORDER BY 1;

-- Direct-compress INSERT and COPY honor the same opt-out.
ALTER TABLE metrics SET (timescaledb.compress, timescaledb.compress_orderby = 'time');
SET timescaledb.enable_direct_compress_insert = on;
SET timescaledb.enable_direct_compress_insert_client_sorted = on;
SET timescaledb.enable_direct_compress_copy = on;
SET timescaledb.enable_direct_compress_copy_client_sorted = on;

\c :TEST_DBNAME :ROLE_SUPERUSER
TRUNCATE _timescaledb_catalog.continuous_aggs_hypertable_invalidation_log,
         _timescaledb_catalog.continuous_aggs_materialization_invalidation_log;
\c :TEST_DBNAME :ROLE_DEFAULT_PERM_USER
SET timezone TO 'UTC';
SET datestyle TO 'ISO, YMD';
SET timescaledb.enable_direct_compress_insert = on;
SET timescaledb.enable_direct_compress_insert_client_sorted = on;
SET timescaledb.enable_direct_compress_copy = on;
SET timescaledb.enable_direct_compress_copy_client_sorted = on;

BEGIN ISOLATION LEVEL REPEATABLE READ;
SET LOCAL timescaledb.skip_cagg_invalidation = on;
INSERT INTO metrics
SELECT '2024-01-01 06:00:00+00'::timestamptz + (i || ' seconds')::interval, 1, i
FROM generate_series(1, 20) i;
COPY metrics (time, device, value) FROM STDIN WITH (FORMAT csv);
2024-01-01 07:00:00+00,1,80
2024-01-01 07:00:01+00,1,81
2024-01-01 07:00:02+00,1,82
2024-01-01 07:00:03+00,1,83
2024-01-01 07:00:04+00,1,84
2024-01-01 07:00:05+00,1,85
2024-01-01 07:00:06+00,1,86
2024-01-01 07:00:07+00,1,87
2024-01-01 07:00:08+00,1,88
2024-01-01 07:00:09+00,1,89
\.
COMMIT;
SELECT count(*) AS hyper_invals_direct_compress_skipped
FROM _timescaledb_catalog.continuous_aggs_hypertable_invalidation_log;

BEGIN ISOLATION LEVEL REPEATABLE READ;
INSERT INTO metrics
SELECT '2024-01-01 08:00:00+00'::timestamptz + (i || ' seconds')::interval, 1, i
FROM generate_series(1, 20) i;
COMMIT;
SELECT count(*) > 0 AS hyper_invals_direct_compress_recorded
FROM _timescaledb_catalog.continuous_aggs_hypertable_invalidation_log;

-- Read-committed backfill below the watermark is recorded, unless skipped.
\c :TEST_DBNAME :ROLE_SUPERUSER
TRUNCATE _timescaledb_catalog.continuous_aggs_hypertable_invalidation_log,
         _timescaledb_catalog.continuous_aggs_materialization_invalidation_log;
\c :TEST_DBNAME :ROLE_DEFAULT_PERM_USER
SET timezone TO 'UTC';
SET datestyle TO 'ISO, YMD';

INSERT INTO metrics VALUES ('2020-01-01 00:00:00+00', 1, 1);
SELECT count(*) > 0 AS hyper_invals_backfill
FROM _timescaledb_catalog.continuous_aggs_hypertable_invalidation_log;

\c :TEST_DBNAME :ROLE_SUPERUSER
TRUNCATE _timescaledb_catalog.continuous_aggs_hypertable_invalidation_log,
         _timescaledb_catalog.continuous_aggs_materialization_invalidation_log;
\c :TEST_DBNAME :ROLE_DEFAULT_PERM_USER
SET timezone TO 'UTC';
SET datestyle TO 'ISO, YMD';

BEGIN;
SET LOCAL timescaledb.skip_cagg_invalidation = on;
INSERT INTO metrics VALUES ('2020-01-02 00:00:00+00', 1, 1);
COMMIT;
SELECT count(*) AS hyper_invals_backfill_skipped
FROM _timescaledb_catalog.continuous_aggs_hypertable_invalidation_log;

-- drop_chunks records an invalidation unless the opt-out is set.
INSERT INTO metrics VALUES ('2023-01-01 00:00:00+00', 1, 1);
INSERT INTO metrics VALUES ('2023-02-01 00:00:00+00', 1, 1);
\c :TEST_DBNAME :ROLE_SUPERUSER
TRUNCATE _timescaledb_catalog.continuous_aggs_hypertable_invalidation_log,
         _timescaledb_catalog.continuous_aggs_materialization_invalidation_log;
\c :TEST_DBNAME :ROLE_DEFAULT_PERM_USER
SET timezone TO 'UTC';
SET datestyle TO 'ISO, YMD';

SELECT count(*) AS dropped_default FROM drop_chunks('metrics', older_than => '2023-01-15'::timestamptz);
SELECT count(*) > 0 AS hyper_invals_drop_chunks
FROM _timescaledb_catalog.continuous_aggs_hypertable_invalidation_log;

\c :TEST_DBNAME :ROLE_SUPERUSER
TRUNCATE _timescaledb_catalog.continuous_aggs_hypertable_invalidation_log,
         _timescaledb_catalog.continuous_aggs_materialization_invalidation_log;
\c :TEST_DBNAME :ROLE_DEFAULT_PERM_USER
SET timezone TO 'UTC';
SET datestyle TO 'ISO, YMD';

BEGIN;
SET LOCAL timescaledb.skip_cagg_invalidation = on;
SELECT count(*) AS dropped_skipped FROM drop_chunks('metrics', older_than => '2023-02-15'::timestamptz);
COMMIT;
SELECT count(*) AS hyper_invals_drop_chunks_skipped
FROM _timescaledb_catalog.continuous_aggs_hypertable_invalidation_log;

-- DROP TABLE on a chunk.
INSERT INTO metrics VALUES ('2023-05-01 00:00:00+00', 1, 1);
SELECT format('%I.%I', chunk_schema, chunk_name) AS drop_chunk
FROM timescaledb_information.chunks
WHERE hypertable_name = 'metrics' AND range_start = '2023-05-01 00:00:00+00' \gset
\c :TEST_DBNAME :ROLE_SUPERUSER
TRUNCATE _timescaledb_catalog.continuous_aggs_hypertable_invalidation_log,
         _timescaledb_catalog.continuous_aggs_materialization_invalidation_log;
\c :TEST_DBNAME :ROLE_DEFAULT_PERM_USER
SET timezone TO 'UTC';
SET datestyle TO 'ISO, YMD';
SELECT format('%I.%I', chunk_schema, chunk_name) AS drop_chunk
FROM timescaledb_information.chunks
WHERE hypertable_name = 'metrics' AND range_start = '2023-05-01 00:00:00+00' \gset

DROP TABLE :drop_chunk;
SELECT count(*) > 0 AS hyper_invals_drop_table
FROM _timescaledb_catalog.continuous_aggs_hypertable_invalidation_log;

INSERT INTO metrics VALUES ('2023-06-01 00:00:00+00', 1, 1);
SELECT format('%I.%I', chunk_schema, chunk_name) AS drop_chunk
FROM timescaledb_information.chunks
WHERE hypertable_name = 'metrics' AND range_start = '2023-06-01 00:00:00+00' \gset
\c :TEST_DBNAME :ROLE_SUPERUSER
TRUNCATE _timescaledb_catalog.continuous_aggs_hypertable_invalidation_log,
         _timescaledb_catalog.continuous_aggs_materialization_invalidation_log;
\c :TEST_DBNAME :ROLE_DEFAULT_PERM_USER
SET timezone TO 'UTC';
SET datestyle TO 'ISO, YMD';
SELECT format('%I.%I', chunk_schema, chunk_name) AS drop_chunk
FROM timescaledb_information.chunks
WHERE hypertable_name = 'metrics' AND range_start = '2023-06-01 00:00:00+00' \gset

BEGIN;
SET LOCAL timescaledb.skip_cagg_invalidation = on;
DROP TABLE :drop_chunk;
COMMIT;
SELECT count(*) AS hyper_invals_drop_table_skipped
FROM _timescaledb_catalog.continuous_aggs_hypertable_invalidation_log;

-- TRUNCATE chunk.
INSERT INTO metrics VALUES ('2023-07-01 00:00:00+00', 1, 1);
\c :TEST_DBNAME :ROLE_SUPERUSER
TRUNCATE _timescaledb_catalog.continuous_aggs_hypertable_invalidation_log,
         _timescaledb_catalog.continuous_aggs_materialization_invalidation_log;
\c :TEST_DBNAME :ROLE_DEFAULT_PERM_USER
SET timezone TO 'UTC';
SET datestyle TO 'ISO, YMD';
SELECT format('%I.%I', chunk_schema, chunk_name) AS trunc_chunk
FROM timescaledb_information.chunks
WHERE hypertable_name = 'metrics' AND range_start = '2023-07-01 00:00:00+00' \gset

TRUNCATE :trunc_chunk;
SELECT count(*) > 0 AS hyper_invals_truncate_chunk
FROM _timescaledb_catalog.continuous_aggs_hypertable_invalidation_log;

INSERT INTO metrics VALUES ('2023-08-01 00:00:00+00', 1, 1);
\c :TEST_DBNAME :ROLE_SUPERUSER
TRUNCATE _timescaledb_catalog.continuous_aggs_hypertable_invalidation_log,
         _timescaledb_catalog.continuous_aggs_materialization_invalidation_log;
\c :TEST_DBNAME :ROLE_DEFAULT_PERM_USER
SET timezone TO 'UTC';
SET datestyle TO 'ISO, YMD';
SELECT format('%I.%I', chunk_schema, chunk_name) AS trunc_chunk
FROM timescaledb_information.chunks
WHERE hypertable_name = 'metrics' AND range_start = '2023-08-01 00:00:00+00' \gset

BEGIN;
SET LOCAL timescaledb.skip_cagg_invalidation = on;
TRUNCATE :trunc_chunk;
COMMIT;
SELECT count(*) AS hyper_invals_truncate_chunk_skipped
FROM _timescaledb_catalog.continuous_aggs_hypertable_invalidation_log;

-- TRUNCATE hypertable.
\c :TEST_DBNAME :ROLE_SUPERUSER
TRUNCATE _timescaledb_catalog.continuous_aggs_hypertable_invalidation_log,
         _timescaledb_catalog.continuous_aggs_materialization_invalidation_log;
\c :TEST_DBNAME :ROLE_DEFAULT_PERM_USER
SET timezone TO 'UTC';
SET datestyle TO 'ISO, YMD';

TRUNCATE metrics;
SELECT count(*) > 0 AS hyper_invals_truncate_ht
FROM _timescaledb_catalog.continuous_aggs_hypertable_invalidation_log;

\c :TEST_DBNAME :ROLE_SUPERUSER
TRUNCATE _timescaledb_catalog.continuous_aggs_hypertable_invalidation_log,
         _timescaledb_catalog.continuous_aggs_materialization_invalidation_log;
\c :TEST_DBNAME :ROLE_DEFAULT_PERM_USER
SET timezone TO 'UTC';
SET datestyle TO 'ISO, YMD';

BEGIN;
SET LOCAL timescaledb.skip_cagg_invalidation = on;
TRUNCATE metrics;
COMMIT;
SELECT count(*) AS hyper_invals_truncate_ht_skipped
FROM _timescaledb_catalog.continuous_aggs_hypertable_invalidation_log;

-- TRUNCATE continuous aggregate, including the hierarchical cascade.
\c :TEST_DBNAME :ROLE_SUPERUSER
TRUNCATE _timescaledb_catalog.continuous_aggs_hypertable_invalidation_log,
         _timescaledb_catalog.continuous_aggs_materialization_invalidation_log;
\c :TEST_DBNAME :ROLE_DEFAULT_PERM_USER
SET timezone TO 'UTC';
SET datestyle TO 'ISO, YMD';

TRUNCATE metrics_hourly;
SELECT count(*) > 0 AS mat_invals_truncate_cagg
FROM _timescaledb_catalog.continuous_aggs_materialization_invalidation_log;
SELECT count(*) > 0 AS hyper_invals_truncate_cagg_cascade
FROM _timescaledb_catalog.continuous_aggs_hypertable_invalidation_log;

\c :TEST_DBNAME :ROLE_SUPERUSER
TRUNCATE _timescaledb_catalog.continuous_aggs_hypertable_invalidation_log,
         _timescaledb_catalog.continuous_aggs_materialization_invalidation_log;
\c :TEST_DBNAME :ROLE_DEFAULT_PERM_USER
SET timezone TO 'UTC';
SET datestyle TO 'ISO, YMD';

BEGIN;
SET LOCAL timescaledb.skip_cagg_invalidation = on;
TRUNCATE metrics_hourly;
COMMIT;
SELECT count(*) AS mat_invals_truncate_cagg_skipped
FROM _timescaledb_catalog.continuous_aggs_materialization_invalidation_log;
SELECT count(*) AS hyper_invals_truncate_cagg_cascade_skipped
FROM _timescaledb_catalog.continuous_aggs_hypertable_invalidation_log;

-- Bootstrap invalidation is still recorded when the opt-out is on.
BEGIN;
SET LOCAL timescaledb.skip_cagg_invalidation = on;
CREATE MATERIALIZED VIEW metrics_bootstrap
WITH (timescaledb.continuous, timescaledb.materialized_only = true) AS
SELECT time_bucket('1 hour', time) AS bucket, count(*) AS n
FROM metrics
GROUP BY 1
WITH NO DATA;
COMMIT;
SELECT count(*) AS mat_invals_bootstrap
FROM _timescaledb_catalog.continuous_aggs_materialization_invalidation_log;
