-- Test that data from multiple tables is returned
CREATE TABLE test_table_1 (k INT PRIMARY KEY, v INT) SPLIT INTO 2 TABLETS;
CREATE TABLE test_table_2 (k INT, v INT, PRIMARY KEY (k asc));
CREATE TABLE range_bounds (k TIMESTAMPTZ, v INT, PRIMARY KEY (k ASC))
    SPLIT AT VALUES (('2020-01-01 00:00:00+00'));
CREATE TABLE hash_and_range (h INT, r INT, PRIMARY KEY ((h) HASH, r ASC))
    SPLIT INTO 2 TABLETS;

SELECT
    relname,
    db_name,
    start_hash_code,
    end_hash_code
FROM yb_tablet_metadata WHERE relname IN ('test_table_1', 'test_table_2')
ORDER BY start_hash_code NULLS FIRST;

-- Range bounds use the same decoded DocDB rendering as the master UI.
SELECT
    start_range,
    end_range
FROM yb_tablet_metadata
WHERE db_name = current_database() AND relname = 'range_bounds'
ORDER BY start_range NULLS FIRST;

-- Hash partitioning, including a hash key followed by range key columns, only
-- reports hash bounds.
SELECT bool_and(start_range IS NULL AND end_range IS NULL) AS hash_ranges_are_null
FROM yb_tablet_metadata
WHERE db_name = current_database()
  AND relname IN ('test_table_1', 'hash_and_range');

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
CREATE TABLE hash_in_a (k INT PRIMARY KEY) SPLIT INTO 2 TABLETS;
CREATE TABLE rewrite_grant (k INT, v INT, PRIMARY KEY (k ASC))
    SPLIT AT VALUES ((10));

\c yb_tmeta_b

CREATE TABLE only_in_b (k INT, PRIMARY KEY (k ASC));
CREATE TABLE same_name (k INT, PRIMARY KEY (k ASC));

SELECT oid AS only_in_b_oid
FROM yb_tablet_metadata
WHERE db_name = 'yb_tmeta_b' AND relname = 'only_in_b' \gset
SELECT oid AS same_name_b_oid
FROM yb_tablet_metadata
WHERE db_name = 'yb_tmeta_b' AND relname = 'same_name' \gset

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

-- Test masking for regular users. Grants are database-local even when OIDs
-- and relation names collide across databases. A table rewrite must not lose
-- the stable OID used for the ACL check.
CREATE ROLE yb_tmeta_user LOGIN;

\c yb_tmeta_a

GRANT SELECT ON same_name, rewrite_grant TO yb_tmeta_user;
ALTER TABLE rewrite_grant ALTER COLUMN v TYPE BIGINT;

\c yb_tmeta_a yb_tmeta_user

SELECT
    db_name,
    count(*) FILTER (WHERE relname = '<insufficient privilege>') AS masked,
    count(*) FILTER (WHERE relname <> '<insufficient privilege>') AS visible
FROM yb_tablet_metadata
WHERE
    (db_name = 'yb_tmeta_a'
     AND oid IN ('only_in_a'::regclass, 'same_name'::regclass,
                 'hash_in_a'::regclass, 'rewrite_grant'::regclass))
    OR
    (db_name = 'yb_tmeta_b' AND oid IN (:only_in_b_oid, :same_name_b_oid))
GROUP BY db_name
ORDER BY db_name;

SELECT relname, start_range IS NULL AS start_is_null, end_range IS NULL AS end_is_null
FROM yb_tablet_metadata
WHERE db_name = 'yb_tmeta_a' AND oid = 'same_name'::regclass;

SELECT bool_and(
    relname = '<insufficient privilege>'
    AND start_range = '<insufficient privilege>'
    AND end_range = '<insufficient privilege>') AS cross_db_range_masked
FROM yb_tablet_metadata
WHERE db_name = 'yb_tmeta_b' AND oid IN (:only_in_b_oid, :same_name_b_oid);

SELECT bool_and(
    relname = '<insufficient privilege>'
    AND start_range IS NULL
    AND end_range IS NULL) AS masked_hash_ranges_are_null
FROM yb_tablet_metadata
WHERE db_name = 'yb_tmeta_a' AND oid = 'hash_in_a'::regclass;

SELECT relname, start_range, end_range
FROM yb_tablet_metadata
WHERE db_name = 'yb_tmeta_a' AND oid = 'rewrite_grant'::regclass
ORDER BY start_range NULLS FIRST;

SELECT bool_and(relname = 'transactions') AS transactions_visible
FROM yb_tablet_metadata
WHERE db_name = 'system';

-- Colocation parents have no table OID and remain masked. SELECT on a user
-- table only reveals rows that identify that table.
\c colocated_db yugabyte
GRANT SELECT ON colocated_asc, non_colocated_split TO yb_tmeta_user;

\c colocated_db yb_tmeta_user

SELECT relname
FROM yb_tablet_metadata
WHERE db_name = 'colocated_db' AND oid IS NULL;

SELECT bool_and(relname = 'non_colocated_split') AS selected_table_is_visible
FROM yb_tablet_metadata
WHERE db_name = 'colocated_db' AND oid = 'non_colocated_split'::regclass;

-- yb_db_admin members bypass masking across databases.
\c yugabyte yugabyte
CREATE ROLE yb_tmeta_admin LOGIN;
GRANT yb_db_admin TO yb_tmeta_admin;

\c yb_tmeta_a yb_tmeta_admin

SELECT count(*) FILTER (WHERE relname = '<insufficient privilege>') AS masked
FROM yb_tablet_metadata
WHERE
    (db_name = 'yb_tmeta_a'
     AND oid IN ('only_in_a'::regclass, 'same_name'::regclass,
                 'hash_in_a'::regclass, 'rewrite_grant'::regclass))
    OR
    (db_name = 'yb_tmeta_b' AND oid IN (:only_in_b_oid, :same_name_b_oid));

-- Cleanup
\c yugabyte yugabyte
DROP DATABASE colocated_db;
DROP DATABASE yb_tmeta_a;
DROP DATABASE yb_tmeta_b;
DROP ROLE yb_tmeta_user;
DROP ROLE yb_tmeta_admin;
