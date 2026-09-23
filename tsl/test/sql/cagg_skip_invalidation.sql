-- This file and its contents are licensed under the Timescale License.
-- Please see the included NOTICE for copyright information and
-- LICENSE-TIMESCALE for a copy of the license.

-- Tests for timescaledb.skip_cagg_invalidation, which lets bulk-load tools
-- that own the refresh lifecycle suppress continuous aggregate invalidations.

\c :TEST_DBNAME :ROLE_SUPERUSER
SELECT _timescaledb_functions.stop_background_workers();
SET ROLE :ROLE_DEFAULT_PERM_USER;

CREATE VIEW hyper_invals AS
SELECT ht.table_name AS hypertable,
       il.lowest_modified_value AS start,
       il.greatest_modified_value AS end
  FROM _timescaledb_catalog.continuous_aggs_hypertable_invalidation_log il
  JOIN _timescaledb_catalog.hypertable ht ON ht.id = il.hypertable_id
 ORDER BY 1, 2, 3;

CREATE VIEW cagg_invals AS
SELECT ca.user_view_name AS cagg,
       il.lowest_modified_value AS start,
       il.greatest_modified_value AS end
  FROM _timescaledb_catalog.continuous_aggs_materialization_invalidation_log il
  JOIN _timescaledb_catalog.continuous_agg ca ON ca.mat_hypertable_id = il.materialization_id
 ORDER BY 1, 2, 3;

CREATE TABLE metrics(time int NOT NULL, device int, value float);
SELECT table_name FROM create_hypertable('metrics', 'time', chunk_time_interval => 10);
CREATE FUNCTION metrics_now() RETURNS int LANGUAGE SQL STABLE AS
$$ SELECT coalesce(max(time), 0) FROM metrics $$;
SELECT set_integer_now_func('metrics', 'metrics_now');

INSERT INTO metrics SELECT t, t % 2, 1 FROM generate_series(0, 59) t;

CREATE MATERIALIZED VIEW metrics_10
WITH (timescaledb.continuous, timescaledb.materialized_only = true) AS
SELECT time_bucket(10, time) AS bucket, sum(value) AS total, count(*) AS cnt
  FROM metrics
 GROUP BY 1 WITH NO DATA;

-- Hierarchical continuous aggregate
CREATE MATERIALIZED VIEW metrics_20
WITH (timescaledb.continuous, timescaledb.materialized_only = true) AS
SELECT time_bucket(20, bucket) AS bucket, sum(total) AS total, sum(cnt) AS cnt
  FROM metrics_10
 GROUP BY 1 WITH NO DATA;

-- Move the invalidation thresholds past all the data so that every
-- modification below would normally be logged.
CALL refresh_continuous_aggregate('metrics_10', 0, 100);
CALL refresh_continuous_aggregate('metrics_20', 0, 100);

-- Buckets where the continuous aggregate differs from the raw data
CREATE VIEW metrics_10_stale AS
SELECT bucket, c.total AS cagg_total, r.total AS raw_total
  FROM metrics_10 c
  FULL JOIN (SELECT time_bucket(10, time) AS bucket, sum(value) AS total
               FROM metrics GROUP BY 1) r USING (bucket)
 WHERE c.total IS DISTINCT FROM r.total
 ORDER BY 1;

SELECT * FROM metrics_10_stale;
SELECT * FROM hyper_invals;

SELECT show_chunks('metrics', older_than => 10) AS chunk_0_10 \gset

-- Default is off
SHOW timescaledb.skip_cagg_invalidation;

--
-- DML with the setting enabled for the transaction must not produce
-- invalidations, including writes made directly to a chunk.
--
BEGIN;
SET LOCAL timescaledb.skip_cagg_invalidation = on;
SHOW timescaledb.skip_cagg_invalidation;
INSERT INTO metrics SELECT t, 0, 10 FROM generate_series(10, 19) t;
UPDATE metrics SET value = 100 WHERE time = 25;
DELETE FROM metrics WHERE time = 35;
COPY metrics FROM STDIN;
45	0	10
\.
MERGE INTO metrics m
USING (VALUES (55, 10.0)) v(time, value) ON m.time = v.time
WHEN MATCHED THEN UPDATE SET value = v.value;
INSERT INTO :chunk_0_10 VALUES (1, 0, 10);
UPDATE :chunk_0_10 SET value = 10 WHERE time = 2;
DELETE FROM :chunk_0_10 WHERE time = 3;
COPY :chunk_0_10 FROM STDIN;
4	0	10
\.
COMMIT;

-- SET LOCAL does not survive the transaction
SHOW timescaledb.skip_cagg_invalidation;

-- Nothing was logged, so a regular refresh leaves every bucket stale
SELECT * FROM hyper_invals;
CALL refresh_continuous_aggregate('metrics_10', 0, 100);
SELECT * FROM metrics_10_stale;

-- A forced refresh materializes the new data
CALL refresh_continuous_aggregate('metrics_10', 0, 100, force => true);
SELECT * FROM metrics_10_stale;

-- The forced refresh ran with the setting off, so it invalidated the
-- hierarchical continuous aggregate as usual.
SELECT * FROM hyper_invals;
CALL refresh_continuous_aggregate('metrics_20', 0, 100);
SELECT * FROM metrics_20 ORDER BY 1;
SELECT * FROM hyper_invals;

--
-- With the setting off again, the same kind of DML is logged.
--
INSERT INTO metrics VALUES (11, 0, 1);
UPDATE metrics SET value = 1 WHERE time = 25;
DELETE FROM metrics WHERE time = 45;
COPY metrics FROM STDIN;
46	0	1
\.
INSERT INTO :chunk_0_10 VALUES (5, 0, 1);
SELECT * FROM hyper_invals;

CALL refresh_continuous_aggregate('metrics_10', 0, 100);
CALL refresh_continuous_aggregate('metrics_20', 0, 100);
SELECT * FROM metrics_10_stale;
SELECT * FROM hyper_invals;

-- Only DML executed while the setting is on is skipped
BEGIN;
INSERT INTO metrics VALUES (6, 0, 1);
SET LOCAL timescaledb.skip_cagg_invalidation = on;
INSERT INTO metrics VALUES (26, 0, 1);
COMMIT;
SELECT * FROM hyper_invals;

-- Session-level SET and RESET work too
SET timescaledb.skip_cagg_invalidation = on;
INSERT INTO metrics VALUES (36, 0, 1);
RESET timescaledb.skip_cagg_invalidation;
INSERT INTO metrics VALUES (56, 0, 1);
SELECT * FROM hyper_invals;

CALL refresh_continuous_aggregate('metrics_10', 0, 100, force => true);
CALL refresh_continuous_aggregate('metrics_20', 0, 100);
SELECT * FROM metrics_10_stale;
SELECT * FROM hyper_invals;
SELECT * FROM cagg_invals;

--
-- DDL. Each command is first run with the setting off inside a rolled
-- back transaction to show the invalidation it would normally log.
--

-- TRUNCATE on a chunk
BEGIN;
TRUNCATE :chunk_0_10;
SELECT * FROM hyper_invals;
ROLLBACK;

BEGIN;
SET LOCAL timescaledb.skip_cagg_invalidation = on;
TRUNCATE :chunk_0_10;
COMMIT;
SELECT * FROM hyper_invals;

-- DROP TABLE on a chunk
SELECT format('%I.%I', chunk_schema, chunk_name) AS chunk_10_20
  FROM timescaledb_information.chunks
 WHERE hypertable_name = 'metrics' AND range_start_integer = 10 \gset

BEGIN;
DROP TABLE :chunk_10_20;
SELECT * FROM hyper_invals;
ROLLBACK;

BEGIN;
SET LOCAL timescaledb.skip_cagg_invalidation = on;
DROP TABLE :chunk_10_20;
COMMIT;
SELECT * FROM hyper_invals;

-- drop_chunks
BEGIN;
SELECT count(*) FROM drop_chunks('metrics', older_than => 30);
SELECT * FROM hyper_invals;
ROLLBACK;

BEGIN;
SET LOCAL timescaledb.skip_cagg_invalidation = on;
SELECT count(*) FROM drop_chunks('metrics', older_than => 30);
COMMIT;
SELECT * FROM hyper_invals;

-- TRUNCATE on a continuous aggregate that has a continuous aggregate on top
BEGIN;
TRUNCATE metrics_10;
SELECT * FROM hyper_invals;
SELECT * FROM cagg_invals;
ROLLBACK;

BEGIN;
SET LOCAL timescaledb.skip_cagg_invalidation = on;
TRUNCATE metrics_10;
COMMIT;
SELECT * FROM hyper_invals;
SELECT * FROM cagg_invals;
SELECT count(*) FROM metrics_10;

-- TRUNCATE on a hypertable
BEGIN;
TRUNCATE metrics;
SELECT * FROM hyper_invals;
ROLLBACK;

BEGIN;
SET LOCAL timescaledb.skip_cagg_invalidation = on;
TRUNCATE metrics;
COMMIT;
SELECT * FROM hyper_invals;
SELECT count(*) FROM metrics;

-- The setting is not left enabled after any of the transactions above
SHOW timescaledb.skip_cagg_invalidation;

--
-- Direct compress INSERT and COPY, and DELETE of whole compressed batches
--
CREATE TABLE dc(time int NOT NULL, device int, value float);
SELECT table_name FROM create_hypertable('dc', 'time', chunk_time_interval => 100);
CREATE FUNCTION dc_now() RETURNS int LANGUAGE SQL STABLE AS
$$ SELECT coalesce(max(time), 0) FROM dc $$;
SELECT set_integer_now_func('dc', 'dc_now');
ALTER TABLE dc SET (timescaledb.compress,
                    timescaledb.compress_segmentby = 'device',
                    timescaledb.compress_orderby = 'time');

INSERT INTO dc SELECT t, t % 2, 1 FROM generate_series(0, 99) t;

CREATE MATERIALIZED VIEW dc_100
WITH (timescaledb.continuous, timescaledb.materialized_only = true) AS
SELECT time_bucket(100, time) AS bucket, sum(value) AS total
  FROM dc
 GROUP BY 1 WITH NO DATA;

CALL refresh_continuous_aggregate('dc_100', 0, 1000);
SELECT count(compress_chunk(ch)) FROM show_chunks('dc') ch;

SET timescaledb.enable_direct_compress_insert = on;
SET timescaledb.enable_direct_compress_copy = on;

BEGIN;
SET LOCAL timescaledb.skip_cagg_invalidation = on;
INSERT INTO dc SELECT t, t % 2, 1 FROM generate_series(200, 299) t;
COPY dc FROM STDIN;
300	0	1
301	1	1
302	0	1
\.
DELETE FROM dc WHERE device = 0;
COMMIT;
SELECT * FROM hyper_invals;

-- Data went through direct compress: the new chunks are compressed
SELECT DISTINCT _timescaledb_functions.chunk_status_text(ch) FROM show_chunks('dc') ch;

-- With the setting off, the same operations are logged
BEGIN;
INSERT INTO dc SELECT t, t % 2, 1 FROM generate_series(400, 499) t;
COMMIT;
BEGIN;
COPY dc FROM STDIN;
500	0	1
501	1	1
\.
COMMIT;
BEGIN;
DELETE FROM dc WHERE device = 1;
COMMIT;
SELECT * FROM hyper_invals;

RESET timescaledb.enable_direct_compress_insert;
RESET timescaledb.enable_direct_compress_copy;

CALL refresh_continuous_aggregate('dc_100', 0, 1000, force => true);
SELECT bucket, c.total AS cagg_total, r.total AS raw_total
  FROM dc_100 c
  FULL JOIN (SELECT time_bucket(100, time) AS bucket, sum(value) AS total
               FROM dc GROUP BY 1) r USING (bucket)
 ORDER BY 1;
