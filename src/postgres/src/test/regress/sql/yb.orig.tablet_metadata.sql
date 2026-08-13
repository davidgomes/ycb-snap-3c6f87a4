-- Test that data from multiple tables is returned
CREATE TABLE test_table_1 (k INT PRIMARY KEY, v INT) SPLIT INTO 2 TABLETS;
CREATE TABLE test_table_2 (k INT, v INT, PRIMARY KEY (k asc));
CREATE TABLE range_bounds (
    k TIMESTAMP,
    PRIMARY KEY (k ASC)
) SPLIT AT VALUES (('2024-06-01'), ('2024-09-01'));
CREATE TABLE hash_with_range (
    h INT,
    r INT,
    PRIMARY KEY ((h) HASH, r ASC)
) SPLIT INTO 2 TABLETS;

SELECT
    relname,
    db_name,
    start_hash_code,
    end_hash_code
FROM yb_tablet_metadata WHERE relname IN ('test_table_1', 'test_table_2')
ORDER BY start_hash_code NULLS FIRST;

-- Range bounds use DocDB rendering, while hash and HASH+ASC tables expose only
-- hash bounds.
SELECT start_range, end_range
FROM yb_tablet_metadata
WHERE db_name = current_database() AND relname = 'range_bounds'
ORDER BY start_range NULLS FIRST;

SELECT
    bool_and(start_hash_code IS NOT NULL AND end_hash_code IS NOT NULL) AS has_hash_bounds,
    bool_and(start_range IS NULL AND end_range IS NULL) AS has_no_range_bounds
FROM yb_tablet_metadata
WHERE db_name = current_database()
  AND relname IN ('test_table_1', 'hash_with_range');

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

CREATE ROLE yb_tmeta_user LOGIN;
CREATE ROLE yb_tmeta_admin LOGIN;
GRANT yb_db_admin TO yb_tmeta_admin;

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
GRANT SELECT ON colocated_hash TO yb_tmeta_user;

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

CREATE TABLE only_in_a (k INT, PRIMARY KEY (k ASC)) SPLIT AT VALUES ((10));
CREATE TABLE same_name (k INT, PRIMARY KEY (k ASC));
CREATE TABLE rewritten_table (k INT);
SELECT oid AS only_in_a_oid FROM pg_class WHERE relname = 'only_in_a' \gset
SELECT oid AS same_name_a_oid FROM pg_class WHERE relname = 'same_name' \gset
SELECT oid AS rewritten_table_oid FROM pg_class WHERE relname = 'rewritten_table' \gset
GRANT SELECT ON only_in_a, rewritten_table TO yb_tmeta_user;
ALTER TABLE rewritten_table ADD PRIMARY KEY (k ASC);

\c yb_tmeta_b

CREATE TABLE only_in_b (k INT, PRIMARY KEY (k ASC)) SPLIT AT VALUES ((10));
CREATE TABLE same_name (k INT, PRIMARY KEY (k ASC));
SELECT oid AS only_in_b_oid FROM pg_class WHERE relname = 'only_in_b' \gset
GRANT SELECT ON only_in_b TO yb_tmeta_user;

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

-- Regular users retain all rows, but only see sensitive metadata for tables in
-- the current database on which they have SELECT.
\c yb_tmeta_a yb_tmeta_user

SELECT
    db_name,
    relname,
    start_range,
    end_range
FROM yb_tablet_metadata
WHERE
    (db_name = 'yb_tmeta_a' AND oid = :only_in_a_oid)
    OR (db_name = 'yb_tmeta_b' AND oid = :only_in_b_oid)
ORDER BY db_name, start_range NULLS FIRST;

SELECT relname, start_range, end_range
FROM yb_tablet_metadata
WHERE db_name = current_database() AND oid = :same_name_a_oid;

-- Grants survive a table rewrite because the master reports the stable
-- pg_class OID, not the rewritten relfilenode.
SELECT relname
FROM yb_tablet_metadata
WHERE db_name = current_database() AND oid = :rewritten_table_oid;

SELECT bool_and(relname = 'transactions') AS transactions_unmasked
FROM yb_tablet_metadata
WHERE db_name = 'system';

\c colocated_db yb_tmeta_user

SELECT DISTINCT relname
FROM yb_tablet_metadata
WHERE db_name = current_database() AND oid IS NULL;

-- yb_db_admin members can see metadata across databases.
\c yb_tmeta_b yb_tmeta_admin

SELECT db_name, relname, start_range, end_range
FROM yb_tablet_metadata
WHERE db_name = 'yb_tmeta_a' AND oid = :only_in_a_oid
ORDER BY start_range NULLS FIRST;

-- Cleanup
\c yugabyte yugabyte
DROP DATABASE colocated_db;
DROP DATABASE yb_tmeta_a;
DROP DATABASE yb_tmeta_b;
DROP ROLE yb_tmeta_user;
DROP ROLE yb_tmeta_admin;
