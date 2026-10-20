package postgres

// The queries below are sent to psql on standard input and printed in
// unaligned, tuple-only mode with 0x1f (unit separator) as field separator.
// Every row starts with a tag so one result set cannot be confused with
// another.
//
// All statements are read-only and cheap: no table is scanned to produce the
// dashboard. Row counts come from the planner statistics in pg_class.reltuples
// and are always reported as estimates.

// fieldSep is the unit separator used by psql (--field-separator).
const fieldSep = "\x1f"

// serverQuery runs against the maintenance database and lists the databases
// visible to the current role.
const serverQuery = `
SELECT 'srv',
       current_setting('server_version'),
       current_user,
       current_database();

SELECT 'db',
       d.datname,
       pg_get_userbyid(d.datdba),
       CASE WHEN has_database_privilege(d.datname, 'CONNECT')
            THEN pg_database_size(d.datname) ELSE -1 END,
       pg_encoding_to_char(d.encoding),
       d.datcollate,
       d.datctype,
       d.datallowconn,
       has_database_privilege(d.datname, 'CONNECT'),
       COALESCE(s.numbackends, 0)
FROM pg_database AS d
LEFT JOIN pg_stat_database AS s ON s.datid = d.oid
WHERE NOT d.datistemplate
ORDER BY d.datname;
`

// databaseQuery runs inside a single database and collects exact catalog
// counts plus relation statistics.
const databaseQuery = `
SELECT 'schema', count(*)
FROM pg_namespace AS n
WHERE n.nspname NOT LIKE 'pg\_%' AND n.nspname <> 'information_schema';

SELECT 'obj', c.relkind, count(*)
FROM pg_class AS c
JOIN pg_namespace AS n ON n.oid = c.relnamespace
WHERE n.nspname NOT LIKE 'pg\_%' AND n.nspname <> 'information_schema'
GROUP BY c.relkind;

SELECT 'fn', count(*)
FROM pg_proc AS p
JOIN pg_namespace AS n ON n.oid = p.pronamespace
WHERE n.nspname NOT LIKE 'pg\_%' AND n.nspname <> 'information_schema';

SELECT 'rows', COALESCE(SUM(GREATEST(c.reltuples, 0))::bigint, 0), count(*)
FROM pg_class AS c
JOIN pg_namespace AS n ON n.oid = c.relnamespace
WHERE c.relkind IN ('r')
  AND n.nspname NOT LIKE 'pg\_%' AND n.nspname <> 'information_schema';

SELECT 'table',
       n.nspname,
       c.relname,
       GREATEST(c.reltuples, 0)::bigint,
       pg_total_relation_size(c.oid)
FROM pg_class AS c
JOIN pg_namespace AS n ON n.oid = c.relnamespace
WHERE c.relkind IN ('r', 'p', 'm')
  AND n.nspname NOT LIKE 'pg\_%' AND n.nspname <> 'information_schema'
ORDER BY pg_total_relation_size(c.oid) DESC
LIMIT 5;
`
