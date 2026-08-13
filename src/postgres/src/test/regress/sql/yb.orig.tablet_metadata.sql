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

-- Test that non-superusers still see cluster-wide rows, but relname (and
-- range bounds) of tables they cannot SELECT are masked.  Rows are never
-- dropped, only masked.
CREATE ROLE yb_tmeta_user LOGIN;

\c yb_tmeta_a yb_tmeta_user

-- Tables of another database are masked, so filtering by real relname finds
-- nothing.
SELECT
    relname,
    db_name
FROM yb_tablet_metadata
WHERE
    relname IN ('only_in_b', 'same_name')
    AND db_name = 'yb_tmeta_b'
ORDER BY relname;

-- The rows themselves are still returned: relname is masked, and since both
-- tables are range-sharded, every range cell (including the unbounded edges
-- that would otherwise be NULL) carries the placeholder too.
SELECT
    relname,
    db_name,
    start_range,
    end_range
FROM yb_tablet_metadata
WHERE db_name = 'yb_tmeta_b'
ORDER BY relname;

-- Range-sharded tablets expose decoded DocDB range bounds.  Hash-sharded
-- tablets keep NULL ranges: hash bounds are reported only via
-- start_hash_code/end_hash_code, including for composite HASH + range keys.
\c yugabyte yugabyte

CREATE TABLE range_bounds_test (k INT, v INT, PRIMARY KEY (k ASC)) SPLIT AT VALUES ((100), (200));
CREATE TABLE hash_range_test (h INT, r INT, PRIMARY KEY (h HASH, r ASC)) SPLIT INTO 2 TABLETS;

SELECT
    relname,
    start_range,
    end_range
FROM yb_tablet_metadata
WHERE db_name = current_database() AND relname = 'range_bounds_test'
ORDER BY start_range NULLS FIRST;

SELECT
    relname,
    start_hash_code,
    end_hash_code,
    start_range,
    end_range
FROM yb_tablet_metadata
WHERE db_name = current_database() AND relname = 'test_table_1'
ORDER BY start_hash_code;

SELECT
    relname,
    start_hash_code,
    end_hash_code,
    start_range,
    end_range
FROM yb_tablet_metadata
WHERE db_name = current_database() AND relname = 'hash_range_test'
ORDER BY start_hash_code;

-- Test masking of sensitive columns (relname, start_range, end_range) for
-- unprivileged roles.  start_hash_code/end_hash_code are never masked.
CREATE ROLE yb_tmeta_priv_user LOGIN;
CREATE ROLE yb_tmeta_admin_user LOGIN;
GRANT yb_db_admin TO yb_tmeta_admin_user;

CREATE TABLE granted_range (k INT, PRIMARY KEY (k ASC)) SPLIT AT VALUES ((10));
CREATE TABLE ungranted_range (k INT, PRIMARY KEY (k ASC)) SPLIT AT VALUES ((10));
CREATE TABLE ungranted_hash (k INT PRIMARY KEY) SPLIT INTO 2 TABLETS;
GRANT SELECT ON granted_range TO yb_tmeta_priv_user;

\c yugabyte yb_tmeta_priv_user

-- SELECT privilege on a table of the current database => real values.
SELECT
    relname,
    start_range,
    end_range
FROM yb_tablet_metadata
WHERE db_name = current_database() AND relname = 'granted_range'
ORDER BY start_range NULLS FIRST;

-- No SELECT privilege => masked relname, and every range cell of the
-- range-sharded table gets the placeholder (even the unbounded edges), so
-- tablet count and range edges do not leak.  The oid column is not masked.
SELECT
    relname,
    start_range,
    end_range
FROM yb_tablet_metadata
WHERE oid = 'ungranted_range'::regclass
ORDER BY start_range;

-- Masked hash-sharded rows keep their natural NULL ranges, and the hash
-- bounds stay visible.
SELECT
    relname,
    start_hash_code,
    end_hash_code,
    start_range,
    end_range
FROM yb_tablet_metadata
WHERE oid = 'ungranted_hash'::regclass
ORDER BY start_hash_code;

-- The system 'transactions' tablet is always shown unmasked.
SELECT DISTINCT relname
FROM yb_tablet_metadata
WHERE db_name = 'system' AND relname = 'transactions';

\c yugabyte yb_tmeta_admin_user

-- yb_db_admin members see everything without explicit grants.
SELECT
    relname,
    start_range,
    end_range
FROM yb_tablet_metadata
WHERE db_name = current_database() AND relname = 'ungranted_range'
ORDER BY start_range NULLS FIRST;

-- Test that a grant only reveals rows when connected to the table's own
-- database: the same role with SELECT on the table sees real values from the
-- table's database, and masked values from any other database.
\c yb_tmeta_a yugabyte

CREATE TABLE cross_db_range (k INT, PRIMARY KEY (k ASC)) SPLIT AT VALUES ((5));
GRANT SELECT ON cross_db_range TO yb_tmeta_user;
SELECT oid AS cross_db_oid FROM pg_class WHERE relname = 'cross_db_range' \gset

\c yb_tmeta_a yb_tmeta_user

SELECT
    relname,
    start_range,
    end_range
FROM yb_tablet_metadata
WHERE db_name = 'yb_tmeta_a' AND oid = :cross_db_oid
ORDER BY start_range NULLS FIRST;

\c yb_tmeta_b yb_tmeta_user

SELECT
    relname,
    start_range,
    end_range
FROM yb_tablet_metadata
WHERE db_name = 'yb_tmeta_a' AND oid = :cross_db_oid
ORDER BY start_range;

-- Test colocation masking: the colocation parent row (which has no pg_class
-- oid) stays masked for regular users, while tables with their own tablets
-- are gated by per-table SELECT.
\c colocated_db yugabyte

GRANT SELECT ON non_colocated_split TO yb_tmeta_user;

\c colocated_db yb_tmeta_user

SELECT
    relname,
    start_hash_code,
    end_hash_code
FROM yb_tablet_metadata
WHERE db_name = current_database() AND relname = 'non_colocated_split'
ORDER BY start_hash_code;

-- Only the (range-partitioned) colocation parent row remains masked; its
-- range cells carry the placeholder as well.
SELECT
    relname,
    start_range,
    end_range
FROM yb_tablet_metadata
WHERE db_name = current_database() AND relname = '<insufficient privilege>';

-- Test that grants keep working across a table rewrite: visibility is
-- checked via the stable pg_class oid, which survives the rewrite.
\c yugabyte yugabyte

CREATE TABLE rewrite_grant_test (k INT, v INT, PRIMARY KEY (k ASC)) SPLIT AT VALUES ((50));
GRANT SELECT ON rewrite_grant_test TO yb_tmeta_priv_user;
ALTER TABLE rewrite_grant_test ALTER COLUMN v TYPE BIGINT;

-- Guard the premise: the rewrite must have moved the storage.
SELECT oid <> relfilenode AS rewritten FROM pg_class WHERE relname = 'rewrite_grant_test';

\c yugabyte yb_tmeta_priv_user

SELECT DISTINCT relname
FROM yb_tablet_metadata
WHERE db_name = current_database() AND oid = 'rewrite_grant_test'::regclass;

-- Cleanup
\c yugabyte yugabyte
DROP TABLE range_bounds_test;
DROP TABLE hash_range_test;
DROP TABLE granted_range;
DROP TABLE ungranted_range;
DROP TABLE ungranted_hash;
DROP TABLE rewrite_grant_test;
DROP DATABASE colocated_db;
DROP DATABASE yb_tmeta_a;
DROP DATABASE yb_tmeta_b;
DROP ROLE yb_tmeta_user;
DROP ROLE yb_tmeta_priv_user;
DROP ROLE yb_tmeta_admin_user;
