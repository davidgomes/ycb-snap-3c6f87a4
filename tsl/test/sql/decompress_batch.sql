-- This file and its contents are licensed under the Timescale License.
-- Please see the included NOTICE for copyright information and
-- LICENSE-TIMESCALE for a copy of the license.

\c :TEST_DBNAME :ROLE_DEFAULT_PERM_USER

CREATE TABLE metrics(time timestamptz NOT NULL, device_id int, value float, label text, flag bool, n numeric);
SELECT create_hypertable('metrics', 'time', chunk_time_interval => interval '10 days');

INSERT INTO metrics
SELECT '2024-01-01'::timestamptz + i * interval '5 min',
       i % 5,
       CASE WHEN i % 7 = 0 THEN NULL ELSE i * 1.5 END,
       CASE WHEN i % 3 = 0 THEN NULL ELSE 'l' || (i % 4) END,
       i % 2 = 0,
       i / 3.0
FROM generate_series(1, 5000) i;
INSERT INTO metrics VALUES ('2024-01-02', NULL, 1, 'nulldev', NULL, NULL);

ALTER TABLE metrics SET (timescaledb.compress,
                         timescaledb.compress_segmentby = 'device_id',
                         timescaledb.compress_orderby = 'time');
SELECT count(compress_chunk(c)) FROM show_chunks('metrics') c;

-- column added with a default after compression
ALTER TABLE metrics ADD COLUMN extra int DEFAULT 42;

CREATE FUNCTION decompress_all()
RETURNS TABLE("time" timestamptz, device_id int, value float, label text, flag bool, n numeric, extra int)
LANGUAGE plpgsql AS
$$
DECLARE
  r record;
BEGIN
  FOR r IN
    SELECT format('%I.%I', ch.schema_name, ch.table_name) AS rel
    FROM _timescaledb_catalog.chunk ch
    JOIN _timescaledb_catalog.chunk uc ON uc.compressed_chunk_id = ch.id
    JOIN _timescaledb_catalog.hypertable h ON h.id = uc.hypertable_id
    WHERE h.table_name = 'metrics'
  LOOP
    RETURN QUERY EXECUTE format(
      'SELECT x.* FROM %s t CROSS JOIN LATERAL _timescaledb_functions.decompress_batch(t) '
      'AS x(time timestamptz, device_id int, value float, label text, flag bool, n numeric, extra int)',
      r.rel);
  END LOOP;
END
$$;

-- decompressing all batches must give back exactly the hypertable data
SELECT count(*) FROM decompress_all();
SELECT count(*) FROM metrics;
SELECT count(*) AS differences FROM (
  (SELECT * FROM decompress_all() EXCEPT ALL SELECT * FROM metrics)
  UNION ALL
  (SELECT * FROM metrics EXCEPT ALL SELECT * FROM decompress_all())
) d;
SELECT * FROM decompress_all() WHERE device_id IS NULL;

SELECT format('%I.%I', ch.schema_name, ch.table_name) AS "COMPRESSED_CHUNK"
FROM _timescaledb_catalog.chunk ch
JOIN _timescaledb_catalog.chunk uc ON uc.compressed_chunk_id = ch.id
JOIN _timescaledb_catalog.hypertable h ON h.id = uc.hypertable_id
WHERE h.table_name = 'metrics'
ORDER BY uc.id LIMIT 1 \gset

-- decompressing a single batch yields exactly that batch's rows
SELECT t._ts_meta_count, t.device_id, t._ts_meta_min_1, t._ts_meta_max_1, count(*), min(x.time), max(x.time), bool_and(x.device_id = t.device_id)
FROM (SELECT * FROM :COMPRESSED_CHUNK WHERE device_id = 3) t
CROSS JOIN LATERAL _timescaledb_functions.decompress_batch(t) AS x(time timestamptz, device_id int)
GROUP BY 1, 2, 3, 4;

-- a subset of columns in any order
SELECT x.* FROM :COMPRESSED_CHUNK t
CROSS JOIN LATERAL _timescaledb_functions.decompress_batch(t) AS x(label text, time timestamptz)
WHERE t.device_id = 3 ORDER BY x.time LIMIT 3;

\set ON_ERROR_STOP 0
-- not compressed batches
SELECT * FROM _timescaledb_functions.decompress_batch(row(1, 2)) AS x(a int);
SELECT x.* FROM metrics t CROSS JOIN LATERAL _timescaledb_functions.decompress_batch(t) AS x(time timestamptz) LIMIT 1;
CREATE TABLE wrong_count_type(_ts_meta_count text, v int);
INSERT INTO wrong_count_type VALUES ('1', 1);
SELECT x.* FROM wrong_count_type t CROSS JOIN LATERAL _timescaledb_functions.decompress_batch(t) AS x(v int);
CREATE TABLE no_compressed_columns(_ts_meta_count int, v int);
INSERT INTO no_compressed_columns VALUES (1, 1);
SELECT x.* FROM no_compressed_columns t CROSS JOIN LATERAL _timescaledb_functions.decompress_batch(t) AS x(v int);

-- column definition list does not match the batch
SELECT x.* FROM :COMPRESSED_CHUNK t CROSS JOIN LATERAL _timescaledb_functions.decompress_batch(t) AS x(time timestamptz, device_id text) LIMIT 1;
SELECT x.* FROM :COMPRESSED_CHUNK t CROSS JOIN LATERAL _timescaledb_functions.decompress_batch(t) AS x(time timestamptz, value int) LIMIT 1;
SELECT x.* FROM :COMPRESSED_CHUNK t CROSS JOIN LATERAL _timescaledb_functions.decompress_batch(t) AS x(time timestamptz, nosuch int) LIMIT 1;
-- same for records that are not typed as a compressed chunk row
SELECT x.* FROM (SELECT * FROM :COMPRESSED_CHUNK LIMIT 1) t CROSS JOIN LATERAL _timescaledb_functions.decompress_batch(t) AS x(time timestamptz, device_id text);
SELECT x.* FROM (SELECT * FROM :COMPRESSED_CHUNK LIMIT 1) t CROSS JOIN LATERAL _timescaledb_functions.decompress_batch(t) AS x(time timestamptz, value int);
SELECT x.* FROM (SELECT * FROM :COMPRESSED_CHUNK LIMIT 1) t CROSS JOIN LATERAL _timescaledb_functions.decompress_batch(t) AS x(time timestamptz, label int);
-- column definition list is required
SELECT * FROM :COMPRESSED_CHUNK t, _timescaledb_functions.decompress_batch(t) LIMIT 1;
\set ON_ERROR_STOP 1

DROP FUNCTION decompress_all();
DROP TABLE metrics, wrong_count_type, no_compressed_columns;
