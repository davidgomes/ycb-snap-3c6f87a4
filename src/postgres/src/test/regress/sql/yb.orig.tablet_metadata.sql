-- Test that data from multiple tables is returned
CREATE ROLE yb_tmeta_user LOGIN;
CREATE ROLE yb_tmeta_admin LOGIN;
GRANT yb_db_admin TO yb_tmeta_admin;

CREATE TABLE test_table_1 (k INT PRIMARY KEY, v INT) SPLIT INTO 2 TABLETS;
CREATE TABLE test_table_2 (k INT, v INT, PRIMARY KEY (k asc));
CREATE TABLE test_table_range (
    k TIMESTAMP,
    PRIMARY KEY (k ASC)
) SPLIT AT VALUES (('2024-06-01'), ('2024-09-01'));
CREATE TABLE test_table_hash_range (
    h INT,
    r INT,
    PRIMARY KEY (h HASH, r ASC)
) SPLIT INTO 2 TABLETS;

SELECT
    relname,
    db_name,
    start_hash_code,
    end_hash_code
FROM yb_tablet_metadata
WHERE db_name = current_database()
    AND relname IN ('test_table_1', 'test_table_2')
ORDER BY start_hash_code NULLS FIRST;

-- Range bounds use the same DocDB rendering as the master UI. Hash and
-- composite HASH+ASC tables expose only hash bounds.
SELECT
    relname,
    start_hash_code,
    end_hash_code,
    start_range,
    end_range
FROM yb_tablet_metadata
WHERE
    db_name = current_database()
    AND relname IN ('test_table_range', 'test_table_hash_range')
ORDER BY relname, start_hash_code NULLS FIRST, start_range NULLS FIRST;

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
WHERE ytm.db_name = current_database()
    AND ytm.relname IN ('test_table_1', 'test_table_2')
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
GRANT SELECT ON colocated_hash, colocated_asc TO yb_tmeta_user;

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

SELECT tablet_id AS colocated_tablet_id
FROM yb_tablet_metadata
WHERE
    db_name = 'colocated_db'
    AND relname LIKE '%.colocation.parent.tablename'
\gset

-- Test that yb_tablet_metadata is independent of the connected database
CREATE DATABASE yb_tmeta_a;
CREATE DATABASE yb_tmeta_b;

\c yb_tmeta_a

CREATE TABLE only_in_a (k INT, PRIMARY KEY (k ASC));
CREATE TABLE same_name (k INT, PRIMARY KEY (k ASC));
CREATE TABLE hash_in_a (k INT PRIMARY KEY) SPLIT INTO 2 TABLETS;
CREATE TABLE masked_range_in_a (
    k INT,
    PRIMARY KEY (k ASC)
) SPLIT AT VALUES ((10), (20));
CREATE TABLE selected_range_in_a (
    k INT,
    PRIMARY KEY (k ASC)
) SPLIT AT VALUES ((10), (20));
CREATE TABLE rewrite_in_a (k INT);

GRANT SELECT ON only_in_a, selected_range_in_a, rewrite_in_a TO yb_tmeta_user;
ALTER TABLE rewrite_in_a ADD PRIMARY KEY (k);

SELECT
    'only_in_a'::regclass::oid AS only_in_a_oid,
    'same_name'::regclass::oid AS same_name_a_oid,
    'hash_in_a'::regclass::oid AS hash_in_a_oid,
    'masked_range_in_a'::regclass::oid AS masked_range_in_a_oid,
    'selected_range_in_a'::regclass::oid AS selected_range_in_a_oid,
    'rewrite_in_a'::regclass::oid AS rewrite_in_a_oid
\gset

SELECT oid <> relfilenode AS rewrite_changed_relfilenode
FROM pg_class
WHERE oid = :rewrite_in_a_oid;

\c yb_tmeta_b

CREATE TABLE only_in_b (k INT, PRIMARY KEY (k ASC));
CREATE TABLE same_name (k INT, PRIMARY KEY (k ASC));
CREATE TABLE selected_range_in_b (
    k INT,
    PRIMARY KEY (k ASC)
) SPLIT AT VALUES ((10), (20));

GRANT SELECT ON only_in_b, selected_range_in_b TO yb_tmeta_user;

SELECT
    'only_in_b'::regclass::oid AS only_in_b_oid,
    'same_name'::regclass::oid AS same_name_b_oid,
    'selected_range_in_b'::regclass::oid AS selected_range_in_b_oid
\gset

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

-- Regular users retain every row, but names and range bounds require SELECT
-- on a relation in the current database.
\c yb_tmeta_a yb_tmeta_user

SELECT
    count(*) = :yb_tmeta_a_row_count::bigint AS same_row_count
FROM yb_tablet_metadata;

SELECT
    bool_and(relname = 'only_in_a') AS selected_table_visible
FROM yb_tablet_metadata
WHERE
    db_name = current_database()
    AND oid = :only_in_a_oid;

SELECT
    relname,
    start_range,
    end_range
FROM yb_tablet_metadata
WHERE
    db_name = current_database()
    AND oid = :rewrite_in_a_oid;

SELECT
    count(*) AS masked_hash_tablets,
    bool_and(relname = '<insufficient privilege>') AS names_masked,
    bool_and(start_range IS NULL AND end_range IS NULL) AS ranges_stay_null,
    min(start_hash_code) AS min_hash,
    max(end_hash_code) AS max_hash
FROM yb_tablet_metadata
WHERE
    db_name = current_database()
    AND oid = :hash_in_a_oid;

SELECT
    count(*) AS masked_range_tablets,
    bool_and(relname = '<insufficient privilege>') AS names_masked,
    bool_and(start_range = '<insufficient privilege>') AS starts_masked,
    bool_and(end_range = '<insufficient privilege>') AS ends_masked,
    bool_and(start_hash_code IS NULL AND end_hash_code IS NULL) AS hashes_stay_null
FROM yb_tablet_metadata
WHERE
    db_name = current_database()
    AND oid = :masked_range_in_a_oid;

SELECT
    count(*) AS selected_range_tablets,
    bool_and(relname = 'selected_range_in_a') AS names_visible,
    count(*) FILTER (WHERE start_range IS NULL) AS unbounded_starts,
    count(*) FILTER (WHERE end_range IS NULL) AS unbounded_ends,
    bool_and(
        start_range IS DISTINCT FROM '<insufficient privilege>' AND
        end_range IS DISTINCT FROM '<insufficient privilege>'
    ) AS ranges_visible
FROM yb_tablet_metadata
WHERE
    db_name = current_database()
    AND oid = :selected_range_in_a_oid;

-- The underlying function enforces the same masking policy as the view.
SELECT
    object_name,
    start_range,
    end_range
FROM yb_get_tablet_metadata()
WHERE
    namespace = current_database()
    AND oid = :masked_range_in_a_oid
ORDER BY tablet_id
LIMIT 1;

-- A grant in another database does not reveal that database's metadata.
SELECT
    count(*) AS cross_db_range_tablets,
    bool_and(relname = '<insufficient privilege>') AS names_masked,
    bool_and(start_range = '<insufficient privilege>') AS starts_masked,
    bool_and(end_range = '<insufficient privilege>') AS ends_masked
FROM yb_tablet_metadata
WHERE
    db_name = 'yb_tmeta_b'
    AND oid = :selected_range_in_b_oid;

-- The transactions tablet is always unmasked.
SELECT
    bool_and(relname = 'transactions') AS transactions_visible
FROM yb_tablet_metadata
WHERE db_name = 'system';

\c yb_tmeta_b yb_tmeta_user

SELECT
    count(*) AS selected_range_tablets,
    bool_and(relname = 'selected_range_in_b') AS names_visible,
    count(*) FILTER (WHERE start_range IS NULL) AS unbounded_starts,
    count(*) FILTER (WHERE end_range IS NULL) AS unbounded_ends
FROM yb_tablet_metadata
WHERE
    db_name = current_database()
    AND oid = :selected_range_in_b_oid;

SELECT
    count(*) AS cross_db_range_tablets,
    bool_and(relname = '<insufficient privilege>') AS names_masked,
    bool_and(start_range = '<insufficient privilege>') AS starts_masked,
    bool_and(end_range = '<insufficient privilege>') AS ends_masked
FROM yb_tablet_metadata
WHERE
    db_name = 'yb_tmeta_a'
    AND oid = :selected_range_in_a_oid;

\c colocated_db yb_tmeta_user

SELECT
    relname,
    start_range,
    end_range
FROM yb_tablet_metadata
WHERE tablet_id = :'colocated_tablet_id';

-- yb_db_admin members see sensitive metadata in every database.
\c yb_tmeta_a yb_tmeta_admin

SELECT
    count(*) AS cross_db_range_tablets,
    bool_and(relname = 'selected_range_in_b') AS names_visible,
    count(*) FILTER (WHERE start_range IS NULL) AS unbounded_starts,
    count(*) FILTER (WHERE end_range IS NULL) AS unbounded_ends
FROM yb_tablet_metadata
WHERE
    db_name = 'yb_tmeta_b'
    AND oid = :selected_range_in_b_oid;

-- Cleanup
\c yugabyte yugabyte
DROP DATABASE colocated_db;
DROP DATABASE yb_tmeta_a;
DROP DATABASE yb_tmeta_b;
DROP ROLE yb_tmeta_user;
DROP ROLE yb_tmeta_admin;
