-- Test that data from multiple tables is returned
CREATE TABLE test_table_1 (k INT PRIMARY KEY, v INT) SPLIT INTO 2 TABLETS;
CREATE TABLE test_table_2 (k INT, v INT, PRIMARY KEY (k asc));

SELECT
    relname,
    db_name,
    start_hash_code,
    end_hash_code
FROM yb_tablet_metadata WHERE relname IN ('test_table_1', 'test_table_2')
ORDER BY start_hash_code NULLS FIRST;

-- Test that we are able to join with yb_servers()
SELECT
    ytm.relname,
    ytm.db_name,
    ytm.start_hash_code,
    ytm.end_hash_code,
    ys.cloud,
    ys.region,
    ys.zone
FROM yb_tablet_metadata ytm
JOIN yb_servers() ys
    ON split_part(ytm.leader, ':', 1) = ys.host
    AND split_part(ytm.leader, ':', 2)::int = ys.port
WHERE ytm.relname IN ('test_table_1', 'test_table_2')
ORDER BY ytm.start_hash_code NULLS FIRST;

-- Test start_range/end_range population.
-- Hash-sharded tablets keep NULL ranges: hash bounds are reported only in
-- start_hash_code/end_hash_code.
SELECT
    relname,
    start_hash_code,
    end_hash_code,
    start_range,
    end_range
FROM yb_tablet_metadata
WHERE db_name = current_database() AND relname = 'test_table_1'
ORDER BY start_hash_code;

-- Range-sharded tablets expose decoded DocDB partition bounds; unbounded
-- edges stay NULL.
CREATE TABLE range_int_split (k INT, v TEXT, PRIMARY KEY (k ASC))
    SPLIT AT VALUES ((100), (200));

SELECT
    relname,
    start_hash_code,
    end_hash_code,
    start_range,
    end_range
FROM yb_tablet_metadata
WHERE db_name = current_database() AND relname = 'range_int_split'
ORDER BY start_range NULLS FIRST;

-- Text range bounds.
CREATE TABLE range_text_split (k TEXT, v INT, PRIMARY KEY (k ASC))
    SPLIT AT VALUES (('m'));

SELECT
    relname,
    start_range,
    end_range
FROM yb_tablet_metadata
WHERE db_name = current_database() AND relname = 'range_text_split'
ORDER BY start_range NULLS FIRST;

-- Timestamp range bounds render in DocDB form: int64 microseconds since the
-- PostgreSQL epoch (2000-01-01), matching the master UI tablet listing.
CREATE TABLE range_ts_split (t TIMESTAMP, PRIMARY KEY (t ASC))
    SPLIT AT VALUES (('2024-01-01 00:00:00'));

SELECT
    relname,
    start_range,
    end_range
FROM yb_tablet_metadata
WHERE db_name = current_database() AND relname = 'range_ts_split'
ORDER BY start_range NULLS FIRST;

-- Composite HASH + ASC keys are hash partitioned: only hash bounds are
-- reported, ranges stay NULL.
CREATE TABLE hash_asc_split (h INT, r INT, v INT, PRIMARY KEY (h HASH, r ASC))
    SPLIT INTO 2 TABLETS;

SELECT
    relname,
    start_hash_code,
    end_hash_code,
    start_range,
    end_range
FROM yb_tablet_metadata
WHERE db_name = current_database() AND relname = 'hash_asc_split'
ORDER BY start_hash_code;

-- Test that data from multiple databases is returned
CREATE DATABASE test_db;
\c test_db

CREATE TABLE test_table_1 (k INT PRIMARY KEY, v INT) SPLIT INTO 2 TABLETS;
CREATE TABLE test_table_2 (k INT, v INT, PRIMARY KEY (k asc));

SELECT
    relname,
    db_name,
    start_hash_code,
    end_hash_code
FROM yb_tablet_metadata
WHERE
    relname IN ('test_table_1', 'test_table_2')
    AND db_name IN ('test_db', 'yugabyte')
ORDER BY db_name, start_hash_code NULLS FIRST;

-- Test per-tablet behavior with colocated tables.
-- colocated_hash and colocated_asc are colocated (share one tablet), while
-- non_colocated_split opts out and is split into 2 tablets.  Because the view
-- is per-tablet, the colocated group produces a single row whose relname is
-- the colocation parent table, not the individual user tables.
CREATE DATABASE colocated_db WITH COLOCATION = true;
\c colocated_db

CREATE TABLE colocated_hash (k INT PRIMARY KEY, v INT);
CREATE TABLE colocated_asc (k INT, v INT, PRIMARY KEY (k asc));
CREATE TABLE non_colocated_split (k INT PRIMARY KEY, v INT) WITH (COLOCATION = false) SPLIT INTO 2 TABLETS;

-- Non-colocated non_colocated_split still produces one row per tablet.
SELECT
    relname,
    db_name,
    start_hash_code,
    end_hash_code
FROM yb_tablet_metadata
WHERE
    relname = 'non_colocated_split'
    AND db_name = 'colocated_db'
ORDER BY start_hash_code NULLS FIRST;

-- The colocated group produces exactly one tablet row (not one per table).
SELECT count(*) AS colocated_tablet_count
FROM yb_tablet_metadata
WHERE
    db_name = 'colocated_db'
    AND relname LIKE '%.colocation.parent.tablename';

-- Test that yb_tablet_metadata is independent of the connected database
CREATE DATABASE yb_tmeta_a;
CREATE DATABASE yb_tmeta_b;

\c yb_tmeta_a

CREATE TABLE only_in_a (k INT, PRIMARY KEY (k ASC));
CREATE TABLE same_name (k INT, PRIMARY KEY (k ASC));

\c yb_tmeta_b

CREATE TABLE only_in_b (k INT, PRIMARY KEY (k ASC));
CREATE TABLE same_name (k INT, PRIMARY KEY (k ASC));

\c yb_tmeta_a

SELECT
    relname,
    db_name
FROM yb_tablet_metadata
WHERE
    relname IN ('only_in_a', 'only_in_b', 'same_name')
    AND db_name IN ('yb_tmeta_a', 'yb_tmeta_b')
ORDER BY db_name, relname;

SELECT count(*) AS yb_tmeta_a_row_count FROM yb_tablet_metadata \gset

\c yb_tmeta_b

SELECT count(*) = :yb_tmeta_a_row_count::bigint AS same_row_count
FROM yb_tablet_metadata;

SELECT
    relname,
    db_name
FROM yb_tablet_metadata
WHERE
    relname IN ('only_in_a', 'only_in_b', 'same_name')
    AND db_name IN ('yb_tmeta_a', 'yb_tmeta_b')
ORDER BY db_name, relname;

-- Test masking of the sensitive columns (relname, start_range, end_range)
-- for unprivileged roles.  Rows are never dropped, only masked; hash codes
-- are never masked.
CREATE ROLE yb_tmeta_user LOGIN;
CREATE ROLE yb_tmeta_admin LOGIN;
GRANT yb_db_admin TO yb_tmeta_admin;

\c yugabyte yugabyte

CREATE TABLE granted_range (k INT, v INT, PRIMARY KEY (k ASC))
    SPLIT AT VALUES ((10));
CREATE TABLE ungranted_range (k INT, v INT, PRIMARY KEY (k ASC))
    SPLIT AT VALUES ((10));
CREATE TABLE granted_hash (k INT PRIMARY KEY, v INT) SPLIT INTO 2 TABLETS;
CREATE TABLE ungranted_hash (k INT PRIMARY KEY, v INT) SPLIT INTO 2 TABLETS;
GRANT SELECT ON granted_range, granted_hash TO yb_tmeta_user;
-- test_table_1 also exists in test_db; only the rows of this database's
-- table become visible to yb_tmeta_user through this grant.
GRANT SELECT ON test_table_1 TO yb_tmeta_user;

\c yugabyte yb_tmeta_user

-- Tables the caller can SELECT in the current database show real values.
SELECT relname, start_range, end_range
FROM yb_tablet_metadata
WHERE oid = 'granted_range'::regclass
ORDER BY start_range NULLS FIRST;

SELECT relname, start_hash_code, end_hash_code, start_range, end_range
FROM yb_tablet_metadata
WHERE oid = 'granted_hash'::regclass
ORDER BY start_hash_code;

-- Without SELECT, relname and both range cells are masked -- including the
-- unbounded edges that would otherwise be NULL -- so range edges do not leak.
SELECT relname, start_range, end_range
FROM yb_tablet_metadata
WHERE oid = 'ungranted_range'::regclass;

-- Masked hash-sharded rows keep their natural NULL ranges, and hash codes
-- are never masked.
SELECT relname, start_hash_code, end_hash_code, start_range, end_range
FROM yb_tablet_metadata
WHERE oid = 'ungranted_hash'::regclass
ORDER BY start_hash_code;

-- Masked rows are not dropped.
SELECT count(*) AS ungranted_range_rows
FROM yb_tablet_metadata
WHERE oid = 'ungranted_range'::regclass;

-- The system 'transactions' tablet is always shown unmasked.
SELECT DISTINCT relname, db_name
FROM yb_tablet_metadata
WHERE db_name = 'system' AND relname = 'transactions';

-- Rows from other databases are always masked for unprivileged roles, even
-- when a table with the same name is visible in the current database.  This
-- also means callers filtering by relname alone only see rows from the
-- current database and must scope by db_name/current_database().
SELECT db_name, relname, count(*) AS tablets
FROM yb_tablet_metadata
WHERE relname = 'test_table_1'
GROUP BY db_name, relname
ORDER BY db_name;

SELECT DISTINCT relname
FROM yb_tablet_metadata
WHERE db_name = 'test_db';

-- yb_db_admin members see everything unmasked.
\c yugabyte yb_tmeta_admin

SELECT relname, start_range, end_range
FROM yb_tablet_metadata
WHERE oid = 'ungranted_range'::regclass
ORDER BY start_range NULLS FIRST;

SELECT db_name, relname, count(*) AS tablets
FROM yb_tablet_metadata
WHERE relname = 'test_table_1'
GROUP BY db_name, relname
ORDER BY db_name;

-- Colocation: the colocation parent row stays masked for regular users,
-- while per-table SELECT gates the rows of tables that opted out of
-- colocation.
\c colocated_db yugabyte
GRANT SELECT ON non_colocated_split TO yb_tmeta_user;

\c colocated_db yb_tmeta_user

SELECT relname, start_hash_code, end_hash_code
FROM yb_tablet_metadata
WHERE oid = 'non_colocated_split'::regclass
ORDER BY start_hash_code;

SELECT count(*) AS visible_colocation_parents
FROM yb_tablet_metadata
WHERE db_name = current_database()
  AND relname LIKE '%.colocation.parent.tablename';

-- The parent tablet row is still present, just masked.
SELECT relname, start_range, end_range
FROM yb_tablet_metadata
WHERE db_name = current_database()
  AND relname = '<insufficient privilege>';

-- Test that the SELECT privilege check is keyed by the stable pg_class oid,
-- so a grant given before a table rewrite still unmasks the row afterwards.
\c yugabyte yugabyte

CREATE TABLE rewrite_range (k INT, v INT);
GRANT SELECT ON rewrite_range TO yb_tmeta_user;
-- Force a table rewrite: the DocDB table (and relfilenode) changes but the
-- pg_class oid does not.
ALTER TABLE rewrite_range ADD PRIMARY KEY (k ASC);
SELECT oid <> relfilenode AS rewritten FROM pg_class WHERE relname = 'rewrite_range';

\c yugabyte yb_tmeta_user

SELECT DISTINCT relname
FROM yb_tablet_metadata
WHERE oid = 'rewrite_range'::regclass AND tablet_state = 'RUNNING';

-- Cleanup
\c yugabyte yugabyte
DROP DATABASE colocated_db;
DROP DATABASE yb_tmeta_a;
DROP DATABASE yb_tmeta_b;
DROP OWNED BY yb_tmeta_user;
DROP ROLE yb_tmeta_user;
DROP ROLE yb_tmeta_admin;
