-- This file and its contents are licensed under the Timescale License.
-- Please see the included NOTICE for copyright information and
-- LICENSE-TIMESCALE for a copy of the license.

-- Bulk loads can suppress continuous aggregate invalidation tracking.
-- The default stays off, SET LOCAL does not leak past COMMIT, and an
-- explicit forced refresh still materializes data loaded while tracking
-- was skipped. Invalidations are only recorded for modifications below
-- the invalidation threshold, so this test refreshes a window first.

\c :TEST_DBNAME :ROLE_SUPERUSER
SELECT _timescaledb_functions.stop_background_workers();
SET ROLE :ROLE_DEFAULT_PERM_USER;
SET timezone TO 'UTC';
SET datestyle TO 'ISO, YMD';
SET client_min_messages TO warning;

CREATE TABLE metrics (time timestamptz NOT NULL, device int, val double precision);
SELECT create_hypertable('metrics', 'time', chunk_time_interval => INTERVAL '1 day');

CREATE MATERIALIZED VIEW metrics_hourly
WITH (timescaledb.continuous, timescaledb.materialized_only = true)
AS
SELECT time_bucket(INTERVAL '1 hour', time) AS bucket,
       device,
       avg(val) AS avg_val
FROM metrics
GROUP BY 1, 2
WITH NO DATA;

CREATE MATERIALIZED VIEW metrics_daily
WITH (timescaledb.continuous, timescaledb.materialized_only = true)
AS
SELECT time_bucket(INTERVAL '1 day', bucket) AS bucket,
       device,
       avg(avg_val) AS avg_val
FROM metrics_hourly
GROUP BY 1, 2
WITH NO DATA;

-- Creating a continuous aggregate still records the initial invalidation
-- when the opt-out is enabled.
BEGIN;
SET LOCAL timescaledb.skip_cagg_invalidation = on;
CREATE MATERIALIZED VIEW metrics_hourly_skipped_create
WITH (timescaledb.continuous, timescaledb.materialized_only = true)
AS
SELECT time_bucket(INTERVAL '1 hour', time) AS bucket, device, avg(val) AS avg_val
FROM metrics
GROUP BY 1, 2
WITH NO DATA;
SELECT count(*) AS initial_invals_while_skipped
FROM _timescaledb_catalog.continuous_aggs_materialization_invalidation_log il
JOIN _timescaledb_catalog.continuous_agg ca
  ON ca.mat_hypertable_id = il.materialization_id
WHERE ca.user_view_name = 'metrics_hourly_skipped_create';
COMMIT;
DROP MATERIALIZED VIEW metrics_hourly_skipped_create;

CREATE VIEW inval_counts AS
SELECT (SELECT count(*)
          FROM _timescaledb_catalog.continuous_aggs_hypertable_invalidation_log il
          JOIN _timescaledb_catalog.hypertable ht ON ht.id = il.hypertable_id
         WHERE ht.table_name = 'metrics') AS metrics_h,
       (SELECT count(*)
          FROM _timescaledb_catalog.continuous_aggs_hypertable_invalidation_log il
          JOIN _timescaledb_catalog.hypertable ht ON ht.id = il.hypertable_id
         WHERE ht.table_name = 'events') AS events_h,
       (SELECT count(*)
          FROM _timescaledb_catalog.continuous_aggs_materialization_invalidation_log il
          JOIN _timescaledb_catalog.continuous_agg ca
            ON ca.mat_hypertable_id = il.materialization_id
         WHERE ca.user_view_name = 'metrics_hourly') AS hourly_m,
       (SELECT count(*)
          FROM _timescaledb_catalog.continuous_aggs_materialization_invalidation_log il
          JOIN _timescaledb_catalog.continuous_agg ca
            ON ca.mat_hypertable_id = il.materialization_id
         WHERE ca.user_view_name = 'metrics_daily') AS daily_m,
       (SELECT count(*)
          FROM _timescaledb_catalog.continuous_aggs_hypertable_invalidation_log il
          JOIN _timescaledb_catalog.continuous_agg ca
            ON ca.mat_hypertable_id = il.hypertable_id
         WHERE ca.user_view_name = 'metrics_hourly') AS hourly_as_raw;

INSERT INTO metrics VALUES
  ('2020-01-01 00:30+00', 1, 10),
  ('2020-01-01 02:30+00', 1, 30),
  ('2020-01-01 03:30+00', 1, 40),
  ('2020-01-01 04:30+00', 2, 50);

CALL refresh_continuous_aggregate('metrics_hourly', '2020-01-01', '2020-01-02');
CALL refresh_continuous_aggregate('metrics_daily', '2020-01-01', '2020-01-02');

-- Default remains off, so a modification of already-materialized data records
-- a hypertable invalidation.
SELECT current_setting('timescaledb.skip_cagg_invalidation') AS skip_setting;
SELECT * FROM inval_counts;
UPDATE metrics SET val = 11 WHERE time = '2020-01-01 00:30+00';
SELECT * FROM inval_counts;

CALL refresh_continuous_aggregate('metrics_hourly', '2020-01-01', '2020-01-02');
CALL refresh_continuous_aggregate('metrics_daily', '2020-01-01', '2020-01-02');

-- SET LOCAL covers INSERT, UPDATE, DELETE, COPY, and direct chunk writes
-- inside the already-refreshed window.
SELECT * FROM inval_counts;
BEGIN;
SET LOCAL timescaledb.skip_cagg_invalidation = on;
SELECT current_setting('timescaledb.skip_cagg_invalidation') AS skip_setting_in_xact;
UPDATE metrics SET val = 12 WHERE time = '2020-01-01 00:30+00';
INSERT INTO metrics VALUES ('2020-01-01 00:45+00', 1, 15);
DELETE FROM metrics WHERE time = '2020-01-01 02:30+00';
COPY metrics FROM STDIN;
2020-01-01 03:45+00	1	41
\.
DO $$
DECLARE
  chunk regclass;
BEGIN
  SELECT c.oid INTO chunk
  FROM show_chunks('metrics') c
  ORDER BY 1
  LIMIT 1;
  EXECUTE format('INSERT INTO %s VALUES (%L, 2, 55)', chunk, '2020-01-01 04:45+00');
END
$$;
COMMIT;

-- The opt-out does not leak past COMMIT, and the skipped statements did not
-- append invalidation log entries.
SELECT current_setting('timescaledb.skip_cagg_invalidation') AS skip_setting_after_commit;
SELECT * FROM inval_counts;

-- A normal refresh leaves the skipped modifications stale.
CALL refresh_continuous_aggregate('metrics_hourly', '2020-01-01', '2020-01-02');
SELECT * FROM metrics_hourly ORDER BY 1, 2;

-- Forced refresh materializes them.
CALL refresh_continuous_aggregate('metrics_hourly', '2020-01-01', '2020-01-02', force => true);
CALL refresh_continuous_aggregate('metrics_daily', '2020-01-01', '2020-01-02', force => true);
SELECT * FROM metrics_hourly ORDER BY 1, 2;
SELECT time_bucket(INTERVAL '1 hour', time) AS bucket, device, avg(val) AS avg_val
FROM metrics
GROUP BY 1, 2
ORDER BY 1, 2;
SELECT * FROM metrics_daily ORDER BY 1, 2;

-- Later DML with the setting off records invalidations again.
UPDATE metrics SET val = 13 WHERE time = '2020-01-01 00:30+00';
SELECT * FROM inval_counts;

CALL refresh_continuous_aggregate('metrics_hourly', '2020-01-01', '2020-01-03');
CALL refresh_continuous_aggregate('metrics_daily', '2020-01-01', '2020-01-03');

-- Direct-compress INSERT/COPY into the refreshed range uses the same opt-out.
ALTER TABLE metrics SET (
  timescaledb.compress,
  timescaledb.compress_segmentby = 'device',
  timescaledb.compress_orderby = 'time DESC'
);
SET timescaledb.enable_direct_compress_insert = on;
SET timescaledb.enable_direct_compress_insert_client_sorted = on;

SELECT * FROM inval_counts;
BEGIN;
SET LOCAL timescaledb.skip_cagg_invalidation = on;
INSERT INTO metrics
SELECT '2020-01-01 05:00+00'::timestamptz + (i || ' second')::interval, 1, i::float
FROM generate_series(0, 3000) i;
COMMIT;
SELECT * FROM inval_counts;

RESET timescaledb.enable_direct_compress_insert;
RESET timescaledb.enable_direct_compress_insert_client_sorted;

\c :TEST_DBNAME :ROLE_SUPERUSER
SET timescaledb.enable_direct_compress_copy = on;
SET timescaledb.enable_direct_compress_copy_client_sorted = on;
SET timezone TO 'UTC';
SET client_min_messages TO warning;
SELECT * FROM inval_counts;
BEGIN;
SET LOCAL timescaledb.skip_cagg_invalidation = on;
COPY metrics FROM PROGRAM 'seq 0 3000 | xargs -II date -u -d "2020-01-01 06:00:00 UTC + I second" +"%Y-%m-%d %H:%M:%S+00,1,0.I"' WITH (FORMAT CSV);
COMMIT;
SELECT * FROM inval_counts;
RESET timescaledb.enable_direct_compress_copy;
RESET timescaledb.enable_direct_compress_copy_client_sorted;
SET ROLE :ROLE_DEFAULT_PERM_USER;

-- The same direct-compress insert with tracking enabled records an invalidation.
SET timescaledb.enable_direct_compress_insert = on;
SET timescaledb.enable_direct_compress_insert_client_sorted = on;
INSERT INTO metrics
SELECT '2020-01-01 07:00+00'::timestamptz + (i || ' second')::interval, 1, i::float
FROM generate_series(0, 3000) i;
SELECT * FROM inval_counts;
RESET timescaledb.enable_direct_compress_insert;
RESET timescaledb.enable_direct_compress_insert_client_sorted;

CALL refresh_continuous_aggregate('metrics_hourly', '2020-01-01', '2020-01-03');
CALL refresh_continuous_aggregate('metrics_daily', '2020-01-01', '2020-01-03');

-- DDL that would otherwise invalidate honors the same opt-out.
CREATE TABLE events (time timestamptz NOT NULL, device int, val double precision);
SELECT create_hypertable('events', 'time', chunk_time_interval => INTERVAL '1 day');

CREATE MATERIALIZED VIEW events_hourly
WITH (timescaledb.continuous, timescaledb.materialized_only = true)
AS
SELECT time_bucket(INTERVAL '1 hour', time) AS bucket,
       device,
       sum(val) AS sum_val
FROM events
GROUP BY 1, 2
WITH NO DATA;

INSERT INTO events
SELECT '2020-01-01'::timestamptz + (i || ' day')::interval, 1, i
FROM generate_series(0, 9) i;
CALL refresh_continuous_aggregate('events_hourly', '2020-01-01', '2020-01-12');

SELECT * FROM inval_counts;
BEGIN;
SET LOCAL timescaledb.skip_cagg_invalidation = on;
SELECT count(*) AS chunks_dropped_skipped
FROM drop_chunks('events', older_than => '2020-01-04'::timestamptz);
COMMIT;
SELECT * FROM inval_counts;

SELECT count(*) AS chunks_dropped_tracked
FROM drop_chunks('events', older_than => '2020-01-07'::timestamptz);
SELECT * FROM inval_counts;

CALL refresh_continuous_aggregate('events_hourly', '2020-01-01', '2020-01-12');

SELECT * FROM inval_counts;
BEGIN;
SET LOCAL timescaledb.skip_cagg_invalidation = on;
DO $$
DECLARE
  chunk regclass;
BEGIN
  SELECT c.oid INTO chunk
  FROM show_chunks('events') c
  ORDER BY 1
  LIMIT 1;
  EXECUTE format('DROP TABLE %s', chunk);
END
$$;
COMMIT;
SELECT * FROM inval_counts;

DO $$
DECLARE
  chunk regclass;
BEGIN
  SELECT c.oid INTO chunk
  FROM show_chunks('events') c
  ORDER BY 1
  LIMIT 1;
  EXECUTE format('DROP TABLE %s', chunk);
END
$$;
SELECT * FROM inval_counts;

CALL refresh_continuous_aggregate('events_hourly', '2020-01-01', '2020-01-12');

SELECT * FROM inval_counts;
BEGIN;
SET LOCAL timescaledb.skip_cagg_invalidation = on;
DO $$
DECLARE
  chunk regclass;
BEGIN
  SELECT c.oid INTO chunk
  FROM show_chunks('events') c
  ORDER BY 1
  LIMIT 1;
  EXECUTE format('TRUNCATE TABLE %s', chunk);
END
$$;
COMMIT;
SELECT * FROM inval_counts;

DO $$
DECLARE
  chunk regclass;
BEGIN
  SELECT c.oid INTO chunk
  FROM show_chunks('events') c
  ORDER BY 1
  LIMIT 1;
  EXECUTE format('TRUNCATE TABLE %s', chunk);
END
$$;
SELECT * FROM inval_counts;

CALL refresh_continuous_aggregate('events_hourly', '2020-01-01', '2020-01-12');

SELECT * FROM inval_counts;
BEGIN;
SET LOCAL timescaledb.skip_cagg_invalidation = on;
TRUNCATE events;
COMMIT;
SELECT * FROM inval_counts;

INSERT INTO events VALUES ('2020-02-01 00:00+00', 1, 1);
CALL refresh_continuous_aggregate('events_hourly', '2020-01-01', '2020-02-02');
TRUNCATE events;
SELECT * FROM inval_counts;

-- Truncating a continuous aggregate invalidates its materialization log and,
-- for a hierarchical aggregate, the hypertable log of the upper aggregate.
CALL refresh_continuous_aggregate('metrics_hourly', '2020-01-01', '2020-01-03');
CALL refresh_continuous_aggregate('metrics_daily', '2020-01-01', '2020-01-03');
SELECT * FROM inval_counts;
BEGIN;
SET LOCAL timescaledb.skip_cagg_invalidation = on;
TRUNCATE metrics_hourly;
COMMIT;
SELECT * FROM inval_counts;

TRUNCATE metrics_hourly;
SELECT * FROM inval_counts;
