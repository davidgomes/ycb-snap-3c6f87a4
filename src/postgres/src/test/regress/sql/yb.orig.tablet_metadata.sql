-- Test that data from multiple tables is returned
CREATE TABLE test_table_1 (k INT PRIMARY KEY, v INT) SPLIT INTO 2 TABLETS;
CREATE TABLE test_table_2 (k INT, v INT, PRIMARY KEY (k asc));
CREATE TABLE range_bounds (k INT PRIMARY KEY) SPLIT AT VALUES ((10));
CREATE TABLE timestamp_bounds (k TIMESTAMP PRIMARY KEY)
    SPLIT AT VALUES (('1970-01-01 00:00:01'));
CREATE TABLE hash_and_range_bounds (
    h INT,
    r INT,
    PRIMARY KEY (h HASH, r ASC)
) SPLIT INTO 2 TABLETS;

-- Range bounds use the same DocDB rendering as the master tablet listing.
-- Hash-sharded tables, including HASH+ASC tables, do not expose range bounds.
SELECT
    relname,
    start_range,
    end_range
FROM yb_tablet_metadata
WHERE
    db_name = current_database()
    AND relname IN ('range_bounds', 'timestamp_bounds', 'hash_and_range_bounds')
ORDER BY relname, start_range NULLS FIRST;

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
SELECT
    'only_in_a'::regclass::oid AS only_in_a_oid,
    'same_name'::regclass::oid AS same_name_a_oid
\gset

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

-- Test visibility of cluster-wide tablet metadata for regular and yb_db_admin roles.
CREATE ROLE yb_tmeta_user LOGIN;
CREATE ROLE yb_tmeta_admin LOGIN;
GRANT yb_db_admin TO yb_tmeta_admin;

\c yb_tmeta_a

CREATE TABLE range_visible (k INT PRIMARY KEY) SPLIT AT VALUES ((10));
CREATE TABLE range_hidden (k INT PRIMARY KEY) SPLIT AT VALUES ((10));
CREATE TABLE hash_hidden (k INT PRIMARY KEY);
CREATE TABLE rewrite_visible (k INT, v INT);
GRANT SELECT ON only_in_a, range_visible, rewrite_visible TO yb_tmeta_user;
ALTER TABLE rewrite_visible ADD PRIMARY KEY (k);
SELECT
    'range_visible'::regclass::oid AS range_visible_oid,
    'range_hidden'::regclass::oid AS range_hidden_oid,
    'hash_hidden'::regclass::oid AS hash_hidden_oid,
    'rewrite_visible'::regclass::oid AS rewrite_visible_oid
\gset

-- The rewrite changes physical storage without changing the relation OID.
SELECT oid = :rewrite_visible_oid::oid AS oid_stable,
       relfilenode <> oid AS relfilenode_changed
FROM pg_class
WHERE oid = :rewrite_visible_oid::oid;

\c yb_tmeta_a yb_tmeta_user

-- A grant in the current database exposes the table name and real range bounds.
SELECT
    bool_and(
        relname = 'range_visible' AND
        (
            (start_range IS NULL AND end_range = 'DocKey([], [10])') OR
            (start_range = 'DocKey([], [10])' AND end_range IS NULL)
        )
    ) AS granted_range_visible
FROM yb_tablet_metadata
WHERE oid = :range_visible_oid::oid;

-- A missing grant masks all sensitive range cells, including unbounded edges.
SELECT bool_and(
    relname = '<insufficient privilege>' AND
    start_range = '<insufficient privilege>' AND
    end_range = '<insufficient privilege>'
) AS ungranted_range_masked
FROM yb_tablet_metadata
WHERE oid = :range_hidden_oid::oid;

-- Hash bounds remain visible while the name remains masked and ranges stay NULL.
SELECT bool_and(
    relname = '<insufficient privilege>' AND
    start_hash_code IS NOT NULL AND
    end_hash_code IS NOT NULL AND
    start_range IS NULL AND
    end_range IS NULL
) AS ungranted_hash_bounds_visible
FROM yb_tablet_metadata
WHERE oid = :hash_hidden_oid::oid;

SELECT relname = '<insufficient privilege>' AS current_db_without_select_masked
FROM yb_tablet_metadata
WHERE oid = :same_name_a_oid::oid;

-- Data from another database remains present but is masked, even for same-name tables.
SELECT bool_and(relname = '<insufficient privilege>') AS cross_db_masked
FROM yb_tablet_metadata
WHERE db_name = 'yb_tmeta_b';

-- ACL lookup must use the stable relation OID after a table rewrite.
SELECT bool_and(relname = 'rewrite_visible') AS rewritten_table_grant_visible
FROM yb_tablet_metadata
WHERE oid = :rewrite_visible_oid::oid;

-- The transaction-status tablet is always identifiable.
SELECT relname = 'transactions' AS transactions_unmasked
FROM yb_tablet_metadata
WHERE db_name = 'system';

-- A yb_db_admin member sees unmasked metadata across databases.
\c yb_tmeta_a yb_tmeta_admin

SELECT bool_and(relname <> '<insufficient privilege>') AS db_admin_unmasked
FROM yb_tablet_metadata
WHERE db_name = 'yb_tmeta_b';

-- Colocation parent metadata is masked for regular roles; table SELECT still gates user rows.
\c colocated_db

SELECT 'non_colocated_split'::regclass::oid AS non_colocated_split_oid \gset
GRANT SELECT ON non_colocated_split TO yb_tmeta_user;

\c colocated_db yb_tmeta_user

SELECT bool_and(relname = 'non_colocated_split') AS granted_colocated_db_table_visible
FROM yb_tablet_metadata
WHERE oid = :non_colocated_split_oid::oid;

SELECT count(*) = 1 AND
       bool_and(relname = '<insufficient privilege>') AS colocation_parent_masked
FROM yb_tablet_metadata
WHERE db_name = current_database() AND oid IS NULL;

-- Cleanup
\c yugabyte yugabyte
DROP DATABASE colocated_db;
DROP DATABASE yb_tmeta_a;
DROP DATABASE yb_tmeta_b;
DROP ROLE yb_tmeta_user;
DROP ROLE yb_tmeta_admin;
