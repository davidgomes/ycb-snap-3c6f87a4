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

-- Test start_range/end_range for range-sharded and hash-sharded tables
\c yb_tmeta_a yugabyte

CREATE TABLE range_granted (k INT, PRIMARY KEY (k ASC)) SPLIT AT VALUES ((10), (20));
CREATE TABLE range_ungranted (k INT, PRIMARY KEY (k ASC)) SPLIT AT VALUES ((10));
CREATE TABLE hash_ungranted (k INT PRIMARY KEY) SPLIT INTO 2 TABLETS;
CREATE TABLE hash_asc (h INT, r INT, PRIMARY KEY (h HASH, r ASC)) SPLIT INTO 2 TABLETS;
CREATE TABLE ts_range (t TIMESTAMP, PRIMARY KEY (t ASC))
    SPLIT AT VALUES (('2000-01-01 00:00:00'));
CREATE TABLE rewrite_granted (k INT, v INT, PRIMARY KEY (k ASC)) SPLIT AT VALUES ((5));

SELECT relname, start_hash_code, end_hash_code, start_range, end_range
FROM yb_tablet_metadata
WHERE db_name = current_database()
    AND relname IN ('range_granted', 'hash_ungranted', 'hash_asc', 'ts_range')
ORDER BY relname, start_hash_code NULLS FIRST, start_range NULLS FIRST;

-- Test masking of relname/start_range/end_range for unprivileged roles
CREATE ROLE yb_tmeta_user LOGIN;
CREATE ROLE yb_tmeta_admin LOGIN;
GRANT yb_db_admin TO yb_tmeta_admin;
GRANT SELECT ON range_granted, rewrite_granted TO yb_tmeta_user;

-- Rewrite keeps the pg_class oid, so the grant must keep applying.
ALTER TABLE rewrite_granted ALTER COLUMN v TYPE BIGINT;

\c colocated_db yugabyte
GRANT SELECT ON colocated_hash TO yb_tmeta_user;

\c yb_tmeta_a yb_tmeta_user

-- Granted range table: real values.
SELECT relname, start_range, end_range
FROM yb_tablet_metadata
WHERE oid = 'range_granted'::regclass AND db_name = current_database()
ORDER BY start_range NULLS FIRST;

-- Rewritten table with a grant: still real values.
SELECT relname, start_range, end_range
FROM yb_tablet_metadata
WHERE oid = 'rewrite_granted'::regclass AND db_name = current_database()
ORDER BY start_range NULLS FIRST;

-- Ungranted range table: every range cell is masked, including open edges.
SELECT relname, start_range, end_range
FROM yb_tablet_metadata
WHERE oid = 'range_ungranted'::regclass AND db_name = current_database();

-- Ungranted hash table: relname masked, hash codes kept, ranges stay NULL.
SELECT relname, start_hash_code, end_hash_code, start_range, end_range
FROM yb_tablet_metadata
WHERE oid = 'hash_ungranted'::regclass AND db_name = current_database()
ORDER BY start_hash_code;

-- Tables in other databases are masked even if names collide.
SELECT DISTINCT relname
FROM yb_tablet_metadata
WHERE db_name = 'yb_tmeta_b';

-- Colocation parent and colocated tables in another database are masked.
SELECT DISTINCT relname
FROM yb_tablet_metadata
WHERE db_name = 'colocated_db';

-- The system transactions tablet is always unmasked.
SELECT count(*) > 0 AS has_transactions
FROM yb_tablet_metadata
WHERE db_name = 'system' AND relname = 'transactions';

-- No rows are dropped by masking.
SELECT count(*) = :yb_tmeta_a_row_count::bigint + 13 AS same_row_count
FROM yb_tablet_metadata;

\c colocated_db yb_tmeta_user

-- Colocation parent stays masked even with SELECT on a colocated table.
SELECT relname
FROM yb_tablet_metadata
WHERE db_name = current_database() AND oid IS NULL;

\c yb_tmeta_a yb_tmeta_admin

-- yb_db_admin members see real values.
SELECT relname, start_range, end_range
FROM yb_tablet_metadata
WHERE oid = 'range_ungranted'::regclass AND db_name = current_database()
ORDER BY start_range NULLS FIRST;

SELECT relname
FROM yb_tablet_metadata
WHERE relname IN ('only_in_b', 'same_name')
    AND db_name = 'yb_tmeta_b'
ORDER BY relname;

-- Cleanup
\c yugabyte yugabyte
DROP DATABASE colocated_db;
DROP DATABASE yb_tmeta_a;
DROP DATABASE yb_tmeta_b;
DROP ROLE yb_tmeta_user;
DROP ROLE yb_tmeta_admin;
