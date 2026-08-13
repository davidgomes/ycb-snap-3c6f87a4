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

-- Test that range-sharded tablets report decoded partition bounds in DocDB
-- form, while hash-sharded tablets keep NULL start_range/end_range.
CREATE TABLE range_split_test (k INT, v INT, PRIMARY KEY (k ASC))
    SPLIT AT VALUES ((100), (200));

SELECT relname, start_hash_code, end_hash_code, start_range, end_range
FROM yb_tablet_metadata
WHERE db_name = current_database() AND relname = 'range_split_test'
ORDER BY start_range NULLS FIRST;

-- Multi-column range keys render all split components.
CREATE TABLE range_multi_test (k1 INT, k2 TEXT, PRIMARY KEY (k1 ASC, k2 DESC))
    SPLIT AT VALUES ((10, 'foo'));

SELECT relname, start_range, end_range
FROM yb_tablet_metadata
WHERE db_name = current_database() AND relname = 'range_multi_test'
ORDER BY start_range NULLS FIRST;

-- Timestamps render in DocDB form, i.e. as their int64 microsecond key
-- representation, matching the master UI tablet listing.
CREATE TABLE range_ts_test (t TIMESTAMP, PRIMARY KEY (t ASC))
    SPLIT AT VALUES (('2024-01-01 00:00:00'));

SELECT relname, start_range, end_range
FROM yb_tablet_metadata
WHERE db_name = current_database() AND relname = 'range_ts_test'
ORDER BY start_range NULLS FIRST;

-- Composite HASH + ASC primary keys are hash-sharded: only hash bounds are
-- reported and the range columns stay NULL.
CREATE TABLE hash_range_test (h INT, r INT, v INT, PRIMARY KEY ((h) HASH, r ASC))
    SPLIT INTO 2 TABLETS;

SELECT relname, start_hash_code, end_hash_code, start_range, end_range
FROM yb_tablet_metadata
WHERE db_name = current_database() AND relname = 'hash_range_test'
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

-- Test that non-superusers still see cluster-wide tablet rows, but the
-- sensitive columns (relname, start_range, end_range) are masked with
-- '<insufficient privilege>' unless the caller has SELECT on the row's table
-- in the connection's current database.
\c yb_tmeta_a

CREATE ROLE yb_tmeta_user LOGIN;
GRANT SELECT ON same_name TO yb_tmeta_user;

\c yb_tmeta_a yb_tmeta_user

-- Rows of the current database: the granted table is unmasked, the
-- ungranted one is masked. No rows are dropped.
SELECT
    relname,
    db_name
FROM yb_tablet_metadata
WHERE db_name = 'yb_tmeta_a'
ORDER BY relname;

-- Cross-database rows stay masked even though the caller has SELECT on
-- yb_tmeta_a.same_name: the same-named table in yb_tmeta_b is a different
-- table, and masking prevents correlating names across databases.
SELECT
    relname,
    db_name
FROM yb_tablet_metadata
WHERE db_name = 'yb_tmeta_b'
ORDER BY relname;

-- Since names can be masked (or collide across databases), callers filtering
-- by relname should also scope by db_name / current_database().
SELECT
    relname,
    db_name
FROM yb_tablet_metadata
WHERE relname = 'same_name' AND db_name = current_database()
ORDER BY db_name;

-- Masked range-sharded rows report the placeholder on every range cell,
-- including the unbounded first/last edges that would otherwise be NULL, so
-- split boundaries and tablet edges do not leak. Masked hash-sharded rows
-- keep their natural NULL ranges, and hash codes are never masked.
\c yb_tmeta_a yugabyte

CREATE TABLE priv_range (k INT, PRIMARY KEY (k ASC)) SPLIT AT VALUES ((10));
CREATE TABLE priv_hash (k INT PRIMARY KEY) SPLIT INTO 2 TABLETS;

\c yb_tmeta_a yb_tmeta_user

SELECT relname, start_hash_code, end_hash_code, start_range, end_range
FROM yb_tablet_metadata
WHERE db_name = current_database() AND oid = 'priv_range'::regclass
ORDER BY start_range NULLS FIRST;

SELECT relname, start_hash_code, end_hash_code, start_range, end_range
FROM yb_tablet_metadata
WHERE db_name = current_database() AND oid = 'priv_hash'::regclass
ORDER BY start_hash_code NULLS FIRST;

-- Granting SELECT unmasks the rows (and revoking masks them again).
\c yb_tmeta_a yugabyte

GRANT SELECT ON priv_range TO yb_tmeta_user;

\c yb_tmeta_a yb_tmeta_user

SELECT relname, start_hash_code, end_hash_code, start_range, end_range
FROM yb_tablet_metadata
WHERE db_name = current_database() AND oid = 'priv_range'::regclass
ORDER BY start_range NULLS FIRST;

\c yb_tmeta_a yugabyte

REVOKE SELECT ON priv_range FROM yb_tmeta_user;

\c yb_tmeta_a yb_tmeta_user

SELECT relname, start_range, end_range
FROM yb_tablet_metadata
WHERE db_name = current_database() AND oid = 'priv_range'::regclass
ORDER BY start_range NULLS FIRST;

-- The system 'transactions' tablet is always shown unmasked, even for
-- unprivileged callers.
SELECT DISTINCT relname, db_name, start_range, end_range
FROM yb_tablet_metadata
WHERE db_name = 'system';

-- yb_db_admin members see everything unmasked, like superusers.
\c yb_tmeta_a yugabyte

CREATE ROLE yb_tmeta_admin_user LOGIN;
GRANT yb_db_admin TO yb_tmeta_admin_user;

\c yb_tmeta_a yb_tmeta_admin_user

SELECT
    relname,
    db_name
FROM yb_tablet_metadata
WHERE db_name IN ('yb_tmeta_a', 'yb_tmeta_b') AND relname !~ '^priv_'
ORDER BY db_name, relname;

SELECT relname, start_range, end_range
FROM yb_tablet_metadata
WHERE db_name = 'yb_tmeta_a' AND relname = 'priv_range'
ORDER BY start_range NULLS FIRST;

-- Colocation parent rows are always masked for regular users: their tablet
-- rows are attributed to the colocation parent table, which has no stable PG
-- table oid to check SELECT against. Granting SELECT on a colocated user
-- table does not reveal the shared tablet's row.
\c colocated_db yugabyte

GRANT SELECT ON colocated_asc TO yb_tmeta_user;

\c colocated_db yb_tmeta_user

SELECT count(*) AS visible_parent_rows
FROM yb_tablet_metadata
WHERE db_name = current_database()
    AND relname LIKE '%.colocation.parent.tablename';

SELECT count(*) AS masked_rows
FROM yb_tablet_metadata
WHERE db_name = current_database()
    AND relname = '<insufficient privilege>';

-- Per-table SELECT still gates the rows of non-colocated tables in a
-- colocated database.
\c colocated_db yugabyte

GRANT SELECT ON non_colocated_split TO yb_tmeta_user;

\c colocated_db yb_tmeta_user

SELECT relname, db_name, start_hash_code, end_hash_code
FROM yb_tablet_metadata
WHERE db_name = current_database() AND relname = 'non_colocated_split'
ORDER BY start_hash_code NULLS FIRST;

-- Cleanup
\c yugabyte yugabyte
DROP DATABASE colocated_db;
DROP DATABASE yb_tmeta_a;
DROP DATABASE yb_tmeta_b;
DROP ROLE yb_tmeta_user;
DROP ROLE yb_tmeta_admin_user;
