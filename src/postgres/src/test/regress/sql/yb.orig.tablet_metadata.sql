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

-- Oids captured here address tablets after relname is masked. The same name
-- can also exist in more than one database, so relname alone is not a key.
SELECT oid AS only_in_b_oid FROM pg_class WHERE relname = 'only_in_b' \gset
SELECT oid AS same_name_b_oid FROM pg_class WHERE relname = 'same_name' \gset

\c yb_tmeta_a
SELECT oid AS only_in_a_oid FROM pg_class WHERE relname = 'only_in_a' \gset
SELECT oid AS same_name_a_oid FROM pg_class WHERE relname = 'same_name' \gset

-- Range bounds are DocDB debug strings (DocKey::DebugSliceToString), the same
-- rendering as the master UI tablet listing. Unbounded edges stay NULL.
-- Timestamps are the int64 microseconds since the Postgres epoch (2000-01-01),
-- so 2000-01-02 is 86400000000.
\c yugabyte
CREATE TABLE range_bounds (k INT, PRIMARY KEY (k ASC)) SPLIT AT VALUES ((10), (20));
CREATE TABLE range_ts (ts TIMESTAMP, PRIMARY KEY (ts ASC))
    SPLIT AT VALUES (('2000-01-02 00:00:00'));

SELECT
    start_hash_code IS NULL AS hash_null,
    start_range,
    end_range
FROM yb_tablet_metadata
WHERE db_name = current_database() AND relname = 'range_bounds'
ORDER BY start_range NULLS FIRST;

SELECT
    start_range,
    end_range
FROM yb_tablet_metadata
WHERE db_name = current_database() AND relname = 'range_ts'
ORDER BY start_range NULLS FIRST;

-- Hash tablets, including composite HASH+ASC, keep range columns NULL.
-- Hash bounds stay in start_hash_code / end_hash_code.
CREATE TABLE hash_only (k INT PRIMARY KEY) SPLIT INTO 2 TABLETS;
CREATE TABLE hash_and_asc (h INT, r INT, PRIMARY KEY (h HASH, r ASC)) SPLIT INTO 2 TABLETS;

SELECT
    relname,
    start_hash_code,
    end_hash_code,
    start_range,
    end_range
FROM yb_tablet_metadata
WHERE db_name = current_database() AND relname IN ('hash_only', 'hash_and_asc')
ORDER BY relname, start_hash_code;

-- Privilege cases. rewrite_grant is granted before the rewrite; the stable oid
-- keeps the grant valid. secret_* and owned_range are not granted.
CREATE TABLE owned_range (k INT, PRIMARY KEY (k ASC)) SPLIT AT VALUES ((5));
CREATE TABLE granted_hash (k INT PRIMARY KEY) SPLIT INTO 2 TABLETS;
CREATE TABLE secret_range (k INT, PRIMARY KEY (k ASC)) SPLIT AT VALUES ((1), (2));
CREATE TABLE secret_hash (k INT PRIMARY KEY) SPLIT INTO 2 TABLETS;
CREATE TABLE rewrite_grant (id INT, v TEXT);

CREATE ROLE yb_tmeta_user LOGIN;
GRANT SELECT ON rewrite_grant TO yb_tmeta_user;
ALTER TABLE rewrite_grant ADD PRIMARY KEY (id);
SELECT (oid <> relfilenode) AS rewritten
FROM pg_class WHERE relname = 'rewrite_grant';

GRANT SELECT ON granted_hash TO yb_tmeta_user;
GRANT SELECT ON range_bounds TO yb_tmeta_user;

-- Colocated user tables share the parent tablet, so SELECT on one of them must
-- not unmask the parent. Opted-out tables have their own tablets.
\c colocated_db
CREATE TABLE opted_out_visible (k INT PRIMARY KEY) WITH (COLOCATION = false);
CREATE TABLE opted_out_secret (k INT PRIMARY KEY) WITH (COLOCATION = false);
GRANT SELECT ON colocated_hash TO yb_tmeta_user;
GRANT SELECT ON opted_out_visible TO yb_tmeta_user;

SELECT count(*) AS colocated_user_rows
FROM yb_tablet_metadata
WHERE db_name = current_database() AND relname = 'colocated_hash';

SELECT (start_hash_code IS NULL AND end_hash_code IS NULL
        AND start_range IS NULL AND end_range IS NULL) AS parent_unbounded
FROM yb_tablet_metadata
WHERE db_name = current_database()
  AND relname LIKE '%.colocation.parent.tablename';

-- Dropped storage may linger with an oid that is no longer in pg_class.
-- Reading the view must mask that row instead of failing.
\c yugabyte
CREATE TABLE doomed (k INT PRIMARY KEY);
SELECT oid AS doomed_oid FROM pg_class WHERE relname = 'doomed' \gset
DROP TABLE doomed;

CREATE ROLE yb_tmeta_admin LOGIN;
GRANT yb_db_admin TO yb_tmeta_admin;

SELECT count(*) AS yb_tmeta_all_rows FROM yb_tablet_metadata \gset

-- Regular role: rows stay, sensitive columns are masked unless SELECT applies
-- in the current database. transactions stays visible. Hash codes stay visible.
\c yugabyte yb_tmeta_user

SELECT count(*) = :yb_tmeta_all_rows::bigint AS rows_not_dropped
FROM yb_tablet_metadata;

SELECT
    count(*) > 0 AS sees_transactions,
    bool_and(relname = 'transactions') AS name_visible,
    bool_and(start_range IS DISTINCT FROM '<insufficient privilege>'
         AND end_range IS DISTINCT FROM '<insufficient privilege>') AS ranges_unmasked
FROM yb_tablet_metadata
WHERE db_name = 'system';

-- A relname predicate does not match masked rows.
SELECT count(*) = 0 AS relname_filter_misses_masked
FROM yb_tablet_metadata
WHERE db_name = current_database() AND relname = 'secret_range';

-- Masked range tablets placeholder every cell, including unbounded edges.
SELECT
    count(*) = 3 AS three_tablets,
    bool_and(relname = '<insufficient privilege>') AS relname_masked,
    bool_and(start_range = '<insufficient privilege>'
         AND end_range = '<insufficient privilege>') AS ranges_masked,
    bool_and(start_hash_code IS NULL AND end_hash_code IS NULL) AS hash_null
FROM yb_tablet_metadata
WHERE db_name = current_database() AND oid = 'secret_range'::regclass;

SELECT
    count(*) = 2 AS two_tablets,
    bool_and(start_range = '<insufficient privilege>'
         AND end_range = '<insufficient privilege>') AS ranges_masked
FROM yb_tablet_metadata
WHERE db_name = current_database() AND oid = 'range_ts'::regclass;

SELECT
    count(*) = 2 AS two_tablets,
    bool_and(relname = '<insufficient privilege>') AS relname_masked,
    bool_and(start_range = '<insufficient privilege>'
         AND end_range = '<insufficient privilege>') AS ranges_masked
FROM yb_tablet_metadata
WHERE db_name = current_database() AND oid = 'owned_range'::regclass;

-- Masked hash tablets keep NULL ranges and still expose hash codes.
SELECT
    relname,
    start_hash_code,
    end_hash_code,
    start_range,
    end_range
FROM yb_tablet_metadata
WHERE db_name = current_database() AND oid = 'secret_hash'::regclass
ORDER BY start_hash_code;

-- SELECT in the current database reveals the name and the DocDB range bounds.
SELECT
    relname,
    start_range,
    end_range
FROM yb_tablet_metadata
WHERE db_name = current_database() AND oid = 'range_bounds'::regclass
ORDER BY start_range NULLS FIRST;

SELECT
    relname,
    start_hash_code,
    end_hash_code,
    start_range IS NULL AS start_null,
    end_range IS NULL AS end_null
FROM yb_tablet_metadata
WHERE db_name = current_database() AND oid = 'granted_hash'::regclass
ORDER BY start_hash_code;

-- The grant survives the rewrite because the view reports pg_class.oid.
SELECT
    bool_and(relname = 'rewrite_grant') AS name_visible,
    bool_and(start_range IS NULL AND end_range IS NULL) AS ranges_null,
    bool_and(start_hash_code IS NOT NULL) AS hash_visible
FROM yb_tablet_metadata
WHERE db_name = current_database() AND oid = 'rewrite_grant'::regclass;

-- Other databases stay masked even when a same-named table exists locally.
SELECT
    count(*) = 4 AS four_tablets,
    bool_and(relname = '<insufficient privilege>') AS relname_masked,
    bool_and(start_range = '<insufficient privilege>'
         AND end_range = '<insufficient privilege>') AS ranges_masked
FROM yb_tablet_metadata
WHERE oid IN (:only_in_a_oid, :same_name_a_oid, :only_in_b_oid, :same_name_b_oid);

-- An orphaned oid does not abort the scan.
SELECT coalesce(bool_and(relname = '<insufficient privilege>'), true) AS orphan_masked_or_absent
FROM yb_tablet_metadata
WHERE oid = :doomed_oid;

\c colocated_db yb_tmeta_user

SELECT
    count(*) = 1 AS one_parent,
    bool_and(relname = '<insufficient privilege>') AS parent_name_masked,
    bool_and(start_range = '<insufficient privilege>'
         AND end_range = '<insufficient privilege>') AS parent_ranges_masked
FROM yb_tablet_metadata
WHERE db_name = current_database() AND oid IS NULL;

SELECT count(*) = 0 AS colocated_hash_has_no_tablet
FROM yb_tablet_metadata
WHERE oid = 'colocated_hash'::regclass;

SELECT
    relname,
    start_range IS NULL AS start_null,
    end_range IS NULL AS end_null,
    start_hash_code IS NOT NULL AS hash_visible
FROM yb_tablet_metadata
WHERE db_name = current_database() AND oid = 'opted_out_visible'::regclass;

SELECT
    bool_and(relname = '<insufficient privilege>') AS secret_masked,
    bool_and(start_range IS NULL AND end_range IS NULL) AS ranges_null,
    bool_and(start_hash_code IS NOT NULL) AS hash_visible
FROM yb_tablet_metadata
WHERE db_name = current_database() AND oid = 'opted_out_secret'::regclass;

-- yb_db_admin sees every database, including the colocation parent.
\c yugabyte yb_tmeta_admin

SELECT
    start_range,
    end_range
FROM yb_tablet_metadata
WHERE db_name = current_database() AND relname = 'range_bounds'
ORDER BY start_range NULLS FIRST;

SELECT
    relname,
    db_name
FROM yb_tablet_metadata
WHERE oid IN (:only_in_b_oid, :same_name_b_oid)
ORDER BY db_name, relname;

\c colocated_db yb_tmeta_admin

SELECT (relname LIKE '%.colocation.parent.tablename'
        AND start_range IS NULL AND end_range IS NULL) AS parent_visible
FROM yb_tablet_metadata
WHERE db_name = current_database() AND oid IS NULL;

-- Cleanup
\c yugabyte yugabyte
DROP DATABASE colocated_db;
DROP DATABASE yb_tmeta_a;
DROP DATABASE yb_tmeta_b;
DROP TABLE range_bounds, range_ts, hash_only, hash_and_asc, owned_range,
           granted_hash, secret_range, secret_hash, rewrite_grant;
DROP ROLE yb_tmeta_user;
DROP ROLE yb_tmeta_admin;
