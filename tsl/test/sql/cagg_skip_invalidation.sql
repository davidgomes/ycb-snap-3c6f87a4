-- This file and its contents are licensed under the Timescale License.
-- Please see the included NOTICE for copyright information and
-- LICENSE-TIMESCALE for a copy of the license.

-- Test timescaledb.skip_cagg_invalidation, which suppresses continuous
-- aggregate invalidation tracking for bulk loads.
\c :TEST_DBNAME :ROLE_SUPERUSER
SELECT _timescaledb_functions.stop_background_workers();
SET ROLE :ROLE_DEFAULT_PERM_USER;

CREATE VIEW hyper_inval_log AS
SELECT h.table_name AS hypertable, count(*) AS entries
  FROM _timescaledb_catalog.continuous_aggs_hypertable_invalidation_log l
  JOIN _timescaledb_catalog.hypertable h ON h.id = l.hypertable_id
 GROUP BY 1 ORDER BY 1;

CREATE VIEW cagg_inval_log AS
SELECT ca.user_view_name AS cagg, count(l.*) AS entries
  FROM _timescaledb_catalog.continuous_agg ca
  LEFT JOIN _timescaledb_catalog.continuous_aggs_materialization_invalidation_log l
    ON l.materialization_id = ca.mat_hypertable_id
 GROUP BY 1 ORDER BY 1;

SHOW timescaledb.skip_cagg_invalidation;

CREATE TABLE metrics (time int NOT NULL, device int, value float);
SELECT create_hypertable('metrics', 'time', chunk_time_interval => 10);

CREATE OR REPLACE FUNCTION int_now() RETURNS int LANGUAGE SQL STABLE AS
$$ SELECT coalesce(max(time), 0) FROM metrics $$;
SELECT set_integer_now_func('metrics', 'int_now');

INSERT INTO metrics SELECT t, 1, t FROM generate_series(0, 49) t;

CREATE MATERIALIZED VIEW metrics_10
WITH (timescaledb.continuous, timescaledb.materialized_only = true) AS
SELECT time_bucket(10, time) AS bucket, count(*) AS cnt, sum(value) AS total
  FROM metrics GROUP BY 1 WITH NO DATA;

CREATE MATERIALIZED VIEW metrics_20
WITH (timescaledb.continuous, timescaledb.materialized_only = true) AS
SELECT time_bucket(20, bucket) AS bucket, sum(cnt) AS cnt
  FROM metrics_10 GROUP BY 1 WITH NO DATA;

CALL refresh_continuous_aggregate('metrics_10', NULL, NULL);
CALL refresh_continuous_aggregate('metrics_20', NULL, NULL);
SELECT * FROM hyper_inval_log;
SELECT * FROM cagg_inval_log;

-- Default is off: DML records invalidations
INSERT INTO metrics VALUES (5, 2, 100);
SELECT * FROM hyper_inval_log;
CALL refresh_continuous_aggregate('metrics_10', NULL, NULL);
SELECT * FROM hyper_inval_log;

-- INSERT, UPDATE, DELETE and COPY with SET LOCAL do not record invalidations
BEGIN;
SET LOCAL timescaledb.skip_cagg_invalidation = on;
INSERT INTO metrics VALUES (1, 3, 1000);
UPDATE metrics SET value = value + 1 WHERE time = 2;
DELETE FROM metrics WHERE time = 3;
COPY metrics FROM STDIN;
4	3	1000
\.
COMMIT;
SELECT * FROM hyper_inval_log;

-- Direct chunk writes are also skipped
SELECT format('%I.%I', c.schema_name, c.table_name) AS first_chunk
  FROM _timescaledb_catalog.chunk c
  JOIN _timescaledb_catalog.hypertable h ON h.id = c.hypertable_id
 WHERE h.table_name = 'metrics'
 ORDER BY c.id LIMIT 1 \gset
BEGIN;
SET LOCAL timescaledb.skip_cagg_invalidation = on;
INSERT INTO :first_chunk VALUES (6, 3, 1000);
COMMIT;
SELECT * FROM hyper_inval_log;

-- SET LOCAL does not leak across COMMIT
SHOW timescaledb.skip_cagg_invalidation;
INSERT INTO metrics VALUES (45, 3, 1000);
SELECT * FROM hyper_inval_log;

-- The cagg is stale until a forced refresh materializes the new data
SELECT * FROM metrics_10 WHERE bucket IN (0, 40) ORDER BY 1;
CALL refresh_continuous_aggregate('metrics_10', NULL, NULL);
SELECT * FROM hyper_inval_log;
SELECT * FROM metrics_10 WHERE bucket IN (0, 40) ORDER BY 1;
CALL refresh_continuous_aggregate('metrics_10', 0, 10, force => true);
SELECT * FROM metrics_10 WHERE bucket = 0;
SELECT count(*), sum(value) FROM metrics WHERE time >= 0 AND time < 10;

-- Direct compress INSERT and COPY
ALTER TABLE metrics SET (timescaledb.compress, timescaledb.compress_segmentby = 'device');
SET timescaledb.enable_direct_compress_insert = on;
SET timescaledb.enable_direct_compress_copy = on;
BEGIN;
SET LOCAL timescaledb.skip_cagg_invalidation = on;
INSERT INTO metrics SELECT t, 4, t FROM generate_series(20, 29) t;
COPY metrics FROM STDIN;
21	5	1
22	5	2
\.
COMMIT;
SELECT * FROM hyper_inval_log;

-- Same direct compress INSERT and COPY with the setting off records invalidations
INSERT INTO metrics SELECT t, 6, t FROM generate_series(30, 39) t;
SELECT * FROM hyper_inval_log;
CALL refresh_continuous_aggregate('metrics_10', NULL, NULL);
COPY metrics FROM STDIN;
31	7	1
\.
SELECT * FROM hyper_inval_log;
RESET timescaledb.enable_direct_compress_insert;
RESET timescaledb.enable_direct_compress_copy;
SELECT count(*) AS compressed_chunks FROM show_chunks('metrics') ch
 WHERE _timescaledb_functions.chunk_status_text(ch) @> ARRAY['COMPRESSED'];
CALL refresh_continuous_aggregate('metrics_10', NULL, NULL);

-- DDL: drop_chunks, DROP TABLE on a chunk, TRUNCATE chunk, TRUNCATE hypertable
SELECT * FROM cagg_inval_log;
BEGIN;
SET LOCAL timescaledb.skip_cagg_invalidation = on;
SELECT count(*) FROM drop_chunks('metrics', older_than => 10);
SELECT format('%I.%I', c.schema_name, c.table_name) AS chunk_a
  FROM _timescaledb_catalog.chunk c
  JOIN _timescaledb_catalog.hypertable h ON h.id = c.hypertable_id
 WHERE h.table_name = 'metrics'
 ORDER BY c.id LIMIT 1 \gset
DROP TABLE :chunk_a;
SELECT format('%I.%I', c.schema_name, c.table_name) AS chunk_b
  FROM _timescaledb_catalog.chunk c
  JOIN _timescaledb_catalog.hypertable h ON h.id = c.hypertable_id
 WHERE h.table_name = 'metrics'
 ORDER BY c.id LIMIT 1 \gset
TRUNCATE :chunk_b;
TRUNCATE metrics;
COMMIT;
SELECT * FROM hyper_inval_log;
SELECT * FROM cagg_inval_log;

-- TRUNCATE a continuous aggregate, including the hierarchical cascade
BEGIN;
SET LOCAL timescaledb.skip_cagg_invalidation = on;
TRUNCATE metrics_10;
COMMIT;
SELECT * FROM hyper_inval_log;
SELECT * FROM cagg_inval_log;

-- With the setting off, TRUNCATE records invalidations again
TRUNCATE metrics_10;
SELECT * FROM hyper_inval_log;
SELECT * FROM cagg_inval_log;
