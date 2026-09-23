-- This file and its contents are licensed under the Timescale License.
-- Please see the included NOTICE for copyright information and
-- LICENSE-TIMESCALE for a copy of the license.

-- Test timescaledb.skip_cagg_invalidation which allows bulk loading tools
-- that manage the refresh lifecycle themselves to suppress continuous
-- aggregate invalidation tracking.

\c :TEST_DBNAME :ROLE_SUPERUSER
SELECT _timescaledb_functions.stop_background_workers();
SET ROLE :ROLE_DEFAULT_PERM_USER;

CREATE VIEW hyper_inval_log AS
SELECT format('%I.%I', ht.schema_name, ht.table_name)::regclass AS hypertable,
       lowest_modified_value AS lowest,
       greatest_modified_value AS greatest
  FROM _timescaledb_catalog.continuous_aggs_hypertable_invalidation_log il
  JOIN _timescaledb_catalog.hypertable ht ON (ht.id = il.hypertable_id)
 ORDER BY 1, 2, 3;

CREATE VIEW cagg_inval_log AS
SELECT ca.user_view_name AS cagg,
       lowest_modified_value AS lowest,
       greatest_modified_value AS greatest
  FROM _timescaledb_catalog.continuous_aggs_materialization_invalidation_log il
  JOIN _timescaledb_catalog.continuous_agg ca ON (ca.mat_hypertable_id = il.materialization_id)
 ORDER BY 1, 2, 3;

-- Default is off
SHOW timescaledb.skip_cagg_invalidation;

CREATE TABLE conditions (time bigint NOT NULL, device int, temp float);
SELECT table_name FROM create_hypertable('conditions', 'time', chunk_time_interval => 10);

CREATE FUNCTION bigint_now() RETURNS bigint LANGUAGE SQL STABLE AS
$$ SELECT coalesce(max(time), 0) FROM conditions $$;
SELECT set_integer_now_func('conditions', 'bigint_now');

INSERT INTO conditions SELECT t, t % 4, t FROM generate_series(0, 99) t;

CREATE MATERIALIZED VIEW cond_10
WITH (timescaledb.continuous, timescaledb.materialized_only = true) AS
SELECT time_bucket(BIGINT '10', time) AS bucket, count(*) AS cnt
  FROM conditions
 GROUP BY 1 WITH NO DATA;

-- Hierarchical continuous aggregate
CREATE MATERIALIZED VIEW cond_20
WITH (timescaledb.continuous, timescaledb.materialized_only = true) AS
SELECT time_bucket(BIGINT '20', bucket) AS bucket, sum(cnt) AS cnt
  FROM cond_10
 GROUP BY 1 WITH NO DATA;

CALL refresh_continuous_aggregate('cond_10', 0, 100);
CALL refresh_continuous_aggregate('cond_20', 0, 100);

-- Remove the initial "everything invalid" entries so that subsequent
-- checks only show invalidations caused by the statements under test.
\c :TEST_DBNAME :ROLE_SUPERUSER
DELETE FROM _timescaledb_catalog.continuous_aggs_materialization_invalidation_log;
SET ROLE :ROLE_DEFAULT_PERM_USER;

SELECT * FROM hyper_inval_log;
SELECT * FROM cagg_inval_log;

-- With the setting off DML records invalidations
INSERT INTO conditions VALUES (5, 1, 1);
SELECT * FROM hyper_inval_log;
\c :TEST_DBNAME :ROLE_SUPERUSER
DELETE FROM _timescaledb_catalog.continuous_aggs_hypertable_invalidation_log;
SET ROLE :ROLE_DEFAULT_PERM_USER;

-- DML with the setting enabled via SET LOCAL does not record
-- invalidations: INSERT, UPDATE, DELETE, COPY on the hypertable and
-- INSERT/COPY directly into a chunk.
SELECT format('%I.%I', chunk_schema, chunk_name) AS "CHUNK"
  FROM timescaledb_information.chunks
 WHERE hypertable_name = 'conditions' AND range_start_integer = 30 \gset

BEGIN;
SET LOCAL timescaledb.skip_cagg_invalidation = on;
SHOW timescaledb.skip_cagg_invalidation;
INSERT INTO conditions SELECT t, 1, 1 FROM generate_series(10, 19) t;
UPDATE conditions SET time = time + 1 WHERE time = 20;
DELETE FROM conditions WHERE time = 25;
COPY conditions FROM STDIN;
26	1	1
27	1	1
\.
INSERT INTO :CHUNK VALUES (35, 1, 1);
COPY :CHUNK FROM STDIN;
36	1	1
\.
COMMIT;

SELECT * FROM hyper_inval_log;
SELECT * FROM cagg_inval_log;

-- SET LOCAL does not leak across COMMIT
SHOW timescaledb.skip_cagg_invalidation;

-- DML with the setting off records invalidations again
INSERT INTO conditions VALUES (45, 1, 1);
SELECT * FROM hyper_inval_log;
\c :TEST_DBNAME :ROLE_SUPERUSER
DELETE FROM _timescaledb_catalog.continuous_aggs_hypertable_invalidation_log;
SET ROLE :ROLE_DEFAULT_PERM_USER;

-- The skipped changes are not materialized by a regular refresh...
SELECT * FROM cond_10 WHERE bucket < 50 ORDER BY 1;
CALL refresh_continuous_aggregate('cond_10', 0, 100);
SELECT * FROM cond_10 WHERE bucket < 50 ORDER BY 1;

-- ...but a forced refresh materializes them
CALL refresh_continuous_aggregate('cond_10', 0, 100, force => true);
SELECT * FROM cond_10 WHERE bucket < 50 ORDER BY 1;
SELECT count(*) AS cnt, time_bucket(BIGINT '10', time) AS bucket FROM conditions
 WHERE time < 50 GROUP BY 2 ORDER BY 2;
CALL refresh_continuous_aggregate('cond_20', 0, 100, force => true);

\c :TEST_DBNAME :ROLE_SUPERUSER
DELETE FROM _timescaledb_catalog.continuous_aggs_hypertable_invalidation_log;
DELETE FROM _timescaledb_catalog.continuous_aggs_materialization_invalidation_log;
SET ROLE :ROLE_DEFAULT_PERM_USER;

-- DDL that invalidates continuous aggregates, with the setting off
BEGIN;
SELECT count(*) FROM drop_chunks('conditions', older_than => 10);
SELECT * FROM hyper_inval_log;
ROLLBACK;

BEGIN;
TRUNCATE cond_10;
SELECT * FROM hyper_inval_log;
SELECT * FROM cagg_inval_log;
ROLLBACK;

-- DDL with the setting enabled does not record invalidations
SELECT format('%I.%I', chunk_schema, chunk_name) AS "CHUNK_TRUNCATE"
  FROM timescaledb_information.chunks
 WHERE hypertable_name = 'conditions' AND range_start_integer = 10 \gset
SELECT format('%I.%I', chunk_schema, chunk_name) AS "CHUNK_DROP"
  FROM timescaledb_information.chunks
 WHERE hypertable_name = 'conditions' AND range_start_integer = 20 \gset

BEGIN;
SET LOCAL timescaledb.skip_cagg_invalidation = on;
SELECT count(*) FROM drop_chunks('conditions', older_than => 10);
TRUNCATE :CHUNK_TRUNCATE;
DROP TABLE :CHUNK_DROP;
COMMIT;
SELECT * FROM hyper_inval_log;
SELECT * FROM cagg_inval_log;

-- TRUNCATE of a continuous aggregate, including the cascade to the
-- hierarchical continuous aggregate on top of it
BEGIN;
SET LOCAL timescaledb.skip_cagg_invalidation = on;
TRUNCATE cond_10;
COMMIT;
SELECT * FROM hyper_inval_log;
SELECT * FROM cagg_inval_log;

-- TRUNCATE of the hypertable
BEGIN;
SET LOCAL timescaledb.skip_cagg_invalidation = on;
TRUNCATE conditions;
COMMIT;
SELECT * FROM hyper_inval_log;
SELECT * FROM cagg_inval_log;

SHOW timescaledb.skip_cagg_invalidation;

-- Direct compress INSERT and COPY
CREATE TABLE metrics (time bigint NOT NULL, device int, value float)
  WITH (tsdb.hypertable, tsdb.partition_column = 'time', tsdb.chunk_interval = 100, tsdb.orderby = 'time');

CREATE FUNCTION metrics_now() RETURNS bigint LANGUAGE SQL STABLE AS
$$ SELECT coalesce(max(time), 0) FROM metrics $$;
SELECT set_integer_now_func('metrics', 'metrics_now');

INSERT INTO metrics SELECT t, 1, t FROM generate_series(0, 999) t;

CREATE MATERIALIZED VIEW metrics_100
WITH (timescaledb.continuous, timescaledb.materialized_only = true) AS
SELECT time_bucket(BIGINT '100', time) AS bucket, count(*) AS cnt
  FROM metrics
 GROUP BY 1 WITH NO DATA;
CALL refresh_continuous_aggregate('metrics_100', 0, 1000);

\c :TEST_DBNAME :ROLE_SUPERUSER
DELETE FROM _timescaledb_catalog.continuous_aggs_hypertable_invalidation_log;
DELETE FROM _timescaledb_catalog.continuous_aggs_materialization_invalidation_log;
SET ROLE :ROLE_DEFAULT_PERM_USER;

SET timescaledb.enable_direct_compress_insert = on;
SET timescaledb.enable_direct_compress_copy = on;

-- With the setting off direct compress records invalidations
BEGIN;
INSERT INTO metrics SELECT t, 2, t FROM generate_series(100, 199) t;
COPY metrics FROM STDIN;
300	2	1
301	2	1
\.
COMMIT;
SELECT * FROM hyper_inval_log;
\c :TEST_DBNAME :ROLE_SUPERUSER
DELETE FROM _timescaledb_catalog.continuous_aggs_hypertable_invalidation_log;
SET ROLE :ROLE_DEFAULT_PERM_USER;

SET timescaledb.enable_direct_compress_insert = on;
SET timescaledb.enable_direct_compress_copy = on;

BEGIN;
SET LOCAL timescaledb.skip_cagg_invalidation = on;
INSERT INTO metrics SELECT t, 3, t FROM generate_series(500, 599) t;
COPY metrics FROM STDIN;
700	3	1
701	3	1
\.
COMMIT;
SELECT * FROM hyper_inval_log;
SELECT * FROM cagg_inval_log;

-- Direct compress was used for the backfilled chunks
SELECT range_start_integer, _timescaledb_functions.chunk_status_text(format('%I.%I', chunk_schema, chunk_name)::regclass) AS status
  FROM timescaledb_information.chunks
 WHERE hypertable_name = 'metrics' AND range_start_integer IN (500, 700)
 ORDER BY range_start_integer;

CALL refresh_continuous_aggregate('metrics_100', 0, 1000, force => true);
SELECT * FROM metrics_100 ORDER BY 1;

RESET timescaledb.enable_direct_compress_insert;
RESET timescaledb.enable_direct_compress_copy;

-- Creating a continuous aggregate still records the initial invalidation
SET timescaledb.skip_cagg_invalidation = on;
CREATE MATERIALIZED VIEW metrics_500
WITH (timescaledb.continuous, timescaledb.materialized_only = true) AS
SELECT time_bucket(BIGINT '500', time) AS bucket, count(*) AS cnt
  FROM metrics
 GROUP BY 1 WITH NO DATA;
RESET timescaledb.skip_cagg_invalidation;
SELECT * FROM cagg_inval_log;
CALL refresh_continuous_aggregate('metrics_500', 0, 1000);
SELECT * FROM metrics_500 ORDER BY 1;
