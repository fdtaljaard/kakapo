package collect

// Did my change help? (docs/phase5h-contract.md, section 2): one more area of the tuning report's
// batch, run only when the centred change is followed (@focusKind <> ''), after Top shapes and from the
// #rs, #int and #plans it filled: no second read of query_store_runtime_stats. @names holds one object
// per line, its schema, object and index apart by tabs (domain.TuningFocusTarget.FocusNames), passed as
// a parameter and split here with CHARINDEX (STRING_SPLIT needs compatibility level 130).
//
// Query Store's query text carries the server's collation and its plans the database's, so every LIKE
// carries COLLATE DATABASE_DEFAULT on both sides (a restored client copy can differ from the server:
// Msg 468). In a LIKE pattern a name's [, % and _ are escaped. An index's plans are read only for the
// queries in the window whose text names its table: reading every plan's text is far too slow.

// focusLikeName is a #fnames or #focus column as a LIKE pattern's literal part.
func focusLikeName(column string) string {
	return "REPLACE(REPLACE(REPLACE(" + column + ", N'[', N'[[]'), N'%', N'[%]'), N'_', N'[_]')"
}

// tuningFocusTables are the area's temporary tables, created with the others so the result sets read
// them whether the area ran or not. Names compare in the database's collation.
const tuningFocusTables = `CREATE TABLE #fnames (n int NOT NULL PRIMARY KEY, sch nvarchar(4000) COLLATE DATABASE_DEFAULT NOT NULL,
                      obj nvarchar(4000) COLLATE DATABASE_DEFAULT NOT NULL, idx nvarchar(4000) COLLATE DATABASE_DEFAULT NOT NULL);
CREATE TABLE #focus (n int NOT NULL PRIMARY KEY, object_id int NULL, tbl sysname COLLATE DATABASE_DEFAULT NULL, index_id int NULL,
                     iname sysname COLLATE DATABASE_DEFAULT NULL, create_date datetime NULL, modify_date datetime NULL);
CREATE TABLE #ftext (n int NOT NULL, query_text_id bigint NOT NULL, head nvarchar(200) COLLATE DATABASE_DEFAULT NULL);
CREATE TABLE #fcand (n int NOT NULL, plan_id bigint NOT NULL, query_id bigint NOT NULL);
CREATE TABLE #fq (n int NOT NULL, query_id bigint NOT NULL, PRIMARY KEY (n, query_id));
CREATE TABLE #fh (n int NOT NULL, query_hash binary(8) NULL, hour_start datetime NOT NULL, cpu bigint NOT NULL, runs bigint NOT NULL);
CREATE TABLE #fbest (n int NOT NULL PRIMARY KEY, query_hash binary(8) NOT NULL);
CREATE TABLE #fqt (n int NOT NULL, query_hash binary(8) NOT NULL, query_id bigint NOT NULL);
CREATE TABLE #fuse (n int NOT NULL PRIMARY KEY, seeks bigint NOT NULL, scans bigint NOT NULL, lookups bigint NOT NULL, updates bigint NOT NULL,
                    last_use datetime NULL);`

// tuningFocusArea is the area itself, in its own TRY.
var tuningFocusArea = `/* Did it help: the changed object's own numbers. A module by its object_id (kept across a change of its code), a view by the
   queries in the window whose text names it, an index by the queries whose plan uses it */
IF @focusKind <> N'' AND @aboutErr IS NULL AND @hoursErr IS NULL AND @topErr IS NULL
BEGIN TRY
    SET @rest = ISNULL(@names, N'');
    WHILE @rest <> N''
    BEGIN
        SET @p = CHARINDEX(NCHAR(10), @rest);
        IF @p = 0 SET @p = LEN(@rest + N'x');
        SET @line = LEFT(@rest, @p - 1);
        SET @rest = SUBSTRING(@rest, @p + 1, LEN(@rest + N'x'));
        SET @n += 1;
        SET @t1 = CHARINDEX(NCHAR(9), @line);
        SET @t2 = CHARINDEX(NCHAR(9), @line, @t1 + 1);
        IF @t1 > 0 AND @t2 > @t1
            INSERT #fnames (n, sch, obj, idx) VALUES (@n, LEFT(@line, @t1 - 1), SUBSTRING(@line, @t1 + 1, @t2 - @t1 - 1), SUBSTRING(@line, @t2 + 1, 4000));
    END;

    /* The objects: a module or a view by name; an index on its table, or by its name alone when the change names no table */
    INSERT #focus (n, object_id, tbl, index_id, iname, create_date, modify_date)
    SELECT nm.n, o.object_id, o.name, ix.index_id, ix.name, o.create_date, o.modify_date
    FROM #fnames AS nm
    JOIN sys.objects AS o ON o.object_id = OBJECT_ID(QUOTENAME(nm.sch) + N'.' + QUOTENAME(nm.obj))
    LEFT JOIN sys.indexes AS ix ON @focusKind = N'index' AND ix.object_id = o.object_id AND ix.name = nm.idx
    WHERE nm.obj <> N'';
    IF @focusKind = N'index'
        INSERT #focus (n, object_id, tbl, index_id, iname)
        SELECT nm.n, MIN(ix.object_id), MIN(o.name), MIN(ix.index_id), MIN(ix.name)
        FROM #fnames AS nm
        JOIN sys.indexes AS ix ON ix.name = nm.idx
        JOIN sys.objects AS o ON o.object_id = ix.object_id AND o.is_ms_shipped = 0
        WHERE nm.obj = N''
        GROUP BY nm.n
        HAVING COUNT(*) = 1;
    INSERT #focus (n) SELECT nm.n FROM #fnames AS nm WHERE NOT EXISTS (SELECT 1 FROM #focus AS f WHERE f.n = nm.n);

    IF @focusKind = N'module'
    BEGIN
        /* A module: its statements by object_id; per hour, its runs are the executions of its most-run statement in the
           hour, the calls of a procedure whose statements each run once a call (limit: a statement in a loop inflates
           them) */
        INSERT #fq (n, query_id)
        SELECT DISTINCT f.n, q.query_id
        FROM #focus AS f
        JOIN sys.query_store_query AS q ON q.object_id = f.object_id;
        INSERT #fh (n, query_hash, hour_start, cpu, runs)
        SELECT x.n, NULL, x.hour_start, CONVERT(bigint, SUM(x.cpu)), MAX(x.execs)
        FROM (SELECT fq.n, i.hour_start, fq.query_id, SUM(r.cpu) AS cpu, SUM(r.execs) AS execs
              FROM #fq AS fq
              JOIN sys.query_store_plan AS p ON p.query_id = fq.query_id
              JOIN #rs AS r ON r.plan_id = p.plan_id
              JOIN #int AS i ON i.interval_id = r.interval_id
              GROUP BY fq.n, i.hour_start, fq.query_id) AS x
        GROUP BY x.n, x.hour_start;
    END
    ELSE
    BEGIN
        /* A view: the queries in the window whose text names it. An index: the queries in the window whose text names its
           table, without the statements that build or rebuild an index, then only those queries' plans in the window:
           Table="[t]" Index="[i]", or Index="[i]" alone when the change names no table. The texts are read once, with
           one pattern (a view or an index is one object); a binary CHARINDEX on the upper-cased text goes first, as the
           match in the database's collation is several times slower over every text (OldSchool: 0.4 s against 1.7 s) */
        SELECT TOP (1) @fn = f.n, @pattern = N'%' + ` + focusLikeName("f.tbl") + ` + N'%', @needle = UPPER(f.tbl)
        FROM #focus AS f
        WHERE f.object_id IS NOT NULL AND (@focusKind = N'view' OR f.index_id IS NOT NULL)
        ORDER BY f.n;
        IF @pattern IS NOT NULL
            INSERT #ftext (n, query_text_id, head)
            SELECT @fn, qt.query_text_id, LEFT(LTRIM(qt.query_sql_text), 200)
            FROM sys.query_store_query_text AS qt
            WHERE CASE WHEN CHARINDEX(@needle COLLATE Latin1_General_BIN2, UPPER(qt.query_sql_text) COLLATE Latin1_General_BIN2) = 0 THEN 0
                       WHEN qt.query_sql_text COLLATE DATABASE_DEFAULT LIKE @pattern COLLATE DATABASE_DEFAULT THEN 1 ELSE 0 END = 1;
        IF @focusKind = N'view'
            INSERT #fq (n, query_id)
            SELECT DISTINCT t.n, p.query_id
            FROM #ftext AS t
            JOIN sys.query_store_query AS q ON q.query_text_id = t.query_text_id
            JOIN #plans AS p ON p.query_id = q.query_id;
        ELSE
        BEGIN
            INSERT #fcand (n, plan_id, query_id)
            SELECT t.n, p.plan_id, p.query_id
            FROM #ftext AS t
            JOIN sys.query_store_query AS q ON q.query_text_id = t.query_text_id
            JOIN #plans AS p ON p.query_id = q.query_id
            WHERE t.head COLLATE DATABASE_DEFAULT NOT LIKE N'CREATE%INDEX%' COLLATE DATABASE_DEFAULT
              AND t.head COLLATE DATABASE_DEFAULT NOT LIKE N'ALTER%INDEX%' COLLATE DATABASE_DEFAULT;
            INSERT #fq (n, query_id)
            SELECT DISTINCT c.n, c.query_id
            FROM #fcand AS c
            JOIN #focus AS f ON f.n = c.n
            JOIN #fnames AS nm ON nm.n = c.n
            JOIN sys.query_store_plan AS p ON p.plan_id = c.plan_id
            WHERE p.query_plan COLLATE DATABASE_DEFAULT LIKE (N'%' + CASE WHEN nm.obj = N'' THEN N'' ELSE N'Table="[[]' + ` + focusLikeName("f.tbl") + ` + N']" ' END + N'Index="[[]' + ` + focusLikeName("f.iname") + ` + N']"%') COLLATE DATABASE_DEFAULT;
        END;
        /* Per query and hour: its CPU and executions, every plan of it in the window */
        INSERT #fh (n, query_hash, hour_start, cpu, runs)
        SELECT fq.n, q.query_hash, i.hour_start, CONVERT(bigint, SUM(r.cpu)), SUM(r.execs)
        FROM #fq AS fq
        JOIN sys.query_store_query AS q ON q.query_id = fq.query_id
        JOIN sys.query_store_plan AS p ON p.query_id = fq.query_id
        JOIN #rs AS r ON r.plan_id = p.plan_id
        JOIN #int AS i ON i.interval_id = r.interval_id
        GROUP BY fq.n, q.query_hash, i.hour_start;
        INSERT #fqt (n, query_hash, query_id)
        SELECT fq.n, q.query_hash, MAX(fq.query_id)
        FROM #fq AS fq
        JOIN sys.query_store_query AS q ON q.query_id = fq.query_id
        GROUP BY fq.n, q.query_hash;
    END;

    /* The costliest matched statement in the window, for Investigate */
    INSERT #fbest (n, query_hash)
    SELECT b.n, b.query_hash
    FROM (SELECT fq.n, q.query_hash, ROW_NUMBER() OVER (PARTITION BY fq.n ORDER BY SUM(r.cpu) DESC) AS rn
          FROM #fq AS fq
          JOIN sys.query_store_query AS q ON q.query_id = fq.query_id
          JOIN sys.query_store_plan AS p ON p.query_id = fq.query_id
          JOIN #rs AS r ON r.plan_id = p.plan_id
          GROUP BY fq.n, q.query_hash) AS b
    WHERE b.rn = 1;

    /* An index's use since SQL Server started (a restart, a restore or taking the database offline drops its row) */
    IF @focusKind = N'index'
    BEGIN
        SELECT @started = CONVERT(datetime, sqlserver_start_time) FROM sys.dm_os_sys_info;
        INSERT #fuse (n, seeks, scans, lookups, updates, last_use)
        SELECT f.n, u.user_seeks, u.user_scans, u.user_lookups, u.user_updates,
               (SELECT MAX(v.t) FROM (VALUES (u.last_user_seek), (u.last_user_scan), (u.last_user_lookup)) AS v (t))
        FROM #focus AS f
        JOIN sys.dm_db_index_usage_stats AS u ON u.database_id = DB_ID() AND u.object_id = f.object_id AND u.index_id = f.index_id;
    END;
END TRY
BEGIN CATCH
    SET @focusErr = ERROR_MESSAGE();
END CATCH;`

// tuningFocusResults are the area's three result sets, after the batch's others: the objects, their
// hours, and a view's or an index's queries.
const tuningFocusResults = `SELECT f.n, f.object_id, f.create_date, f.modify_date, CONVERT(varchar(18), b.query_hash, 1),
       CASE WHEN f.index_id IS NOT NULL THEN OBJECT_SCHEMA_NAME(f.object_id) + N'.' + f.tbl END,
       CASE WHEN f.index_id IS NOT NULL THEN 1 ELSE 0 END, CASE WHEN u.n IS NOT NULL THEN 1 ELSE 0 END,
       ISNULL(u.seeks, 0), ISNULL(u.scans, 0), ISNULL(u.lookups, 0), ISNULL(u.updates, 0), u.last_use, @started
FROM #focus AS f
LEFT JOIN #fbest AS b ON b.n = f.n
LEFT JOIN #fuse AS u ON u.n = f.n
ORDER BY f.n;
SELECT h.n, CONVERT(varchar(18), h.query_hash, 1), h.hour_start, h.cpu, h.runs FROM #fh AS h ORDER BY h.n, h.hour_start;
SELECT t.n, CONVERT(varchar(18), t.query_hash, 1),
       CASE WHEN q.object_id > 0 THEN OBJECT_SCHEMA_NAME(q.object_id) + N'.' + OBJECT_NAME(q.object_id) END,
       LEFT(LTRIM(REPLACE(REPLACE(REPLACE(qt.query_sql_text, NCHAR(13), N' '), NCHAR(10), N' '), NCHAR(9), N' ')), 300)
FROM #fqt AS t
JOIN sys.query_store_query AS q ON q.query_id = t.query_id
JOIN sys.query_store_query_text AS qt ON qt.query_text_id = q.query_text_id
ORDER BY t.n, t.query_hash;`
