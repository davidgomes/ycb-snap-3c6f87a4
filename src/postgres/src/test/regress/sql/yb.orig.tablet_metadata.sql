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

-- Test start_range and end_range. Range-sharded tablets report their bounds
-- rendered as DocDB keys (NULL for unbounded edges). Hash-sharded tablets,
-- including HASH + ASC composite keys, report only hash bounds.
CREATE TABLE range_int (k INT, v INT, PRIMARY KEY (k ASC)) SPLIT AT VALUES ((100), (200));
CREATE INDEX range_int_v_idx ON range_int (v ASC) SPLIT AT VALUES ((10));
CREATE TABLE range_desc (k INT, PRIMARY KEY (k DESC)) SPLIT AT VALUES ((200), (100));
CREATE TABLE range_text (k TEXT, PRIMARY KEY (k ASC)) SPLIT AT VALUES (('m'));
CREATE TABLE range_ts (k TIMESTAMP, PRIMARY KEY (k ASC)) SPLIT AT VALUES (('2024-06-01'));
CREATE TABLE range_multi (a INT, b TEXT, PRIMARY KEY (a ASC, b ASC)) SPLIT AT VALUES ((1, 'x'), (2));
CREATE TABLE range_single (k INT, PRIMARY KEY (k ASC));
CREATE TABLE hash_range (h INT, r INT, PRIMARY KEY (h HASH, r ASC)) SPLIT INTO 2 TABLETS;

SELECT
    relname,
    start_range,
    end_range,
    start_hash_code,
    end_hash_code
FROM yb_tablet_metadata
WHERE
    db_name = current_database()
    AND relname IN ('range_int', 'range_int_v_idx', 'range_desc', 'range_text', 'range_ts',
                    'range_multi', 'range_single', 'hash_range', 'test_table_1')
ORDER BY relname, start_hash_code NULLS FIRST, start_range NULLS FIRST;

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

-- Test masking of relname, start_range and end_range. Superusers and members
-- of yb_db_admin see every row unmasked. Other roles see those columns only for
-- tables of the current database on which they have SELECT; no rows are
-- dropped.
CREATE ROLE yb_tmeta_user LOGIN;
CREATE ROLE yb_tmeta_admin LOGIN IN ROLE yb_db_admin;

\c yb_tmeta_a yugabyte

CREATE TABLE range_split (k INT, PRIMARY KEY (k ASC)) SPLIT AT VALUES ((10), (20));
CREATE TABLE hash_split (k INT PRIMARY KEY) SPLIT INTO 2 TABLETS;
GRANT SELECT ON same_name TO yb_tmeta_user;

SELECT count(*) AS yb_tmeta_superuser_row_count
FROM yb_tablet_metadata
WHERE db_name IN ('system', 'yb_tmeta_a', 'yb_tmeta_b') \gset

\c yb_tmeta_b yugabyte

GRANT SELECT ON same_name TO yb_tmeta_user;
SELECT 'same_name'::regclass::oid AS yb_tmeta_b_same_name_oid \gset

\c yb_tmeta_a yb_tmeta_user

-- Rows are not dropped for unprivileged users.
SELECT count(*) = :yb_tmeta_superuser_row_count::bigint AS same_row_count
FROM yb_tablet_metadata
WHERE db_name IN ('system', 'yb_tmeta_a', 'yb_tmeta_b');

-- Current database: only same_name (SELECT granted) is unmasked. Masked
-- hash-sharded rows keep NULL ranges, while every range cell of masked
-- range-sharded rows is masked. Rows are labeled through pg_class since relname
-- may be masked.
SELECT
    c.relname AS table_name,
    t.relname,
    t.start_range,
    t.end_range,
    t.start_hash_code,
    t.end_hash_code
FROM yb_tablet_metadata t
JOIN pg_class c ON c.oid = t.oid
WHERE t.db_name = current_database()
ORDER BY c.relname, t.start_hash_code NULLS FIRST, t.start_range NULLS FIRST;

-- Other databases are masked, even for tables on which SELECT is granted there.
SELECT
    relname,
    start_range,
    end_range,
    count(*) AS tablet_count
FROM yb_tablet_metadata
WHERE db_name = 'yb_tmeta_b'
GROUP BY relname, start_range, end_range;

SELECT
    relname,
    start_range,
    end_range
FROM yb_tablet_metadata
WHERE db_name = 'yb_tmeta_b' AND oid = :yb_tmeta_b_same_name_oid;

-- The underlying function is masked the same way.
SELECT DISTINCT
    object_name,
    start_range,
    end_range
FROM yb_get_tablet_metadata()
WHERE namespace = 'yb_tmeta_b';

-- The system transactions tablets are never masked.
SELECT DISTINCT
    relname,
    db_name,
    start_range,
    end_range
FROM yb_tablet_metadata
WHERE db_name = 'system';

\c yb_tmeta_b yb_tmeta_user

-- Connected to yb_tmeta_b, the grant on yb_tmeta_b.same_name applies.
SELECT
    c.relname AS table_name,
    t.relname,
    t.start_range,
    t.end_range
FROM yb_tablet_metadata t
JOIN pg_class c ON c.oid = t.oid
WHERE t.db_name = current_database()
ORDER BY c.relname;

-- yb_db_admin members see everything, across databases.
\c yb_tmeta_a yb_tmeta_admin

SELECT
    relname,
    db_name,
    start_range,
    end_range
FROM yb_tablet_metadata
WHERE
    db_name IN ('yb_tmeta_a', 'yb_tmeta_b')
    AND relname IN ('only_in_b', 'same_name', 'range_split')
ORDER BY db_name, relname, start_range NULLS FIRST;

-- Colocation parent rows stay masked for regular users, even with SELECT on
-- every colocated table.
\c colocated_db yugabyte

GRANT SELECT ON colocated_hash, colocated_asc, non_colocated_split TO yb_tmeta_user;

\c colocated_db yb_tmeta_user

SELECT
    relname,
    start_range,
    end_range,
    start_hash_code,
    end_hash_code
FROM yb_tablet_metadata
WHERE db_name = current_database()
ORDER BY relname, start_hash_code NULLS FIRST;

-- Grants follow the stable table oid across table rewrites.
\c yb_tmeta_a yugabyte

CREATE TABLE rewrite_table (k INT, v INT);
GRANT SELECT ON rewrite_table TO yb_tmeta_user;
ALTER TABLE rewrite_table ADD PRIMARY KEY (k ASC);
SELECT oid <> relfilenode AS rewritten FROM pg_class WHERE relname = 'rewrite_table';

-- Tablets of dropped tables may linger with an oid that no longer resolves.
CREATE TABLE dropped_table (k INT, PRIMARY KEY (k ASC));
GRANT SELECT ON dropped_table TO yb_tmeta_user;
SELECT 'dropped_table'::regclass::oid AS yb_tmeta_dropped_oid \gset
DROP TABLE dropped_table;

\c yb_tmeta_a yb_tmeta_user

SELECT
    relname,
    start_range,
    end_range,
    start_hash_code,
    end_hash_code
FROM yb_tablet_metadata
WHERE
    db_name = current_database()
    AND oid = 'rewrite_table'::regclass
    AND tablet_state = 'RUNNING';

SELECT bool_and(relname = '<insufficient privilege>') IS NOT FALSE AS dropped_masked
FROM yb_tablet_metadata
WHERE db_name = current_database() AND oid = :yb_tmeta_dropped_oid;

-- Cleanup
\c yugabyte yugabyte
DROP TABLE range_int, range_desc, range_text, range_ts, range_multi, range_single, hash_range;
DROP DATABASE colocated_db;
DROP DATABASE yb_tmeta_a;
DROP DATABASE yb_tmeta_b;
DROP ROLE yb_tmeta_user;
DROP ROLE yb_tmeta_admin;
