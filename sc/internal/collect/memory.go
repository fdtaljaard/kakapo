package collect

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"sqlmonitor/internal/domain"
)

// The Memory screen's reads (docs/phase5v-contract.md). SELECTs only; every sum is cast to bigint and
// every page count is COUNT_BIG (an int sum of plan sizes overflowed on a client server); cached plans are
// never joined to sessions. The rights are checked first (decision 7): without VIEW SERVER STATE (or VIEW
// SERVER PERFORMANCE STATE from SQL Server 2022) nothing else is read.

// memoryPermission sets @ok: the login may read the server's memory views.
const memoryPermission = `DECLARE @major int = CONVERT(int, SERVERPROPERTY('ProductMajorVersion'));
DECLARE @ok bit = CASE WHEN HAS_PERMS_BY_NAME(NULL, NULL, N'VIEW SERVER STATE') = 1 THEN 1 ELSE 0 END;
IF @ok = 0 AND @major >= 16 AND HAS_PERMS_BY_NAME(NULL, NULL, N'VIEW SERVER PERFORMANCE STATE') = 1 SET @ok = 1;
`

// serverStateRight names the right the overview needs, for the version.
func serverStateRight(major int) string {
	if major >= 16 {
		return "VIEW SERVER PERFORMANCE STATE (or VIEW SERVER STATE)"
	}
	return "VIEW SERVER STATE"
}

// memoryOverviewQuery reads panels 2 and 3 in one batch: one row of facts; then, when the login may read
// them, page life expectancy per NUMA node and the clerks by type. The performance counters and the plan
// cache are each read in their own TRY, so a failure there costs only that line.
const memoryOverviewQuery = safeSession + `SET NOCOUNT ON;
` + memoryPermission + `
IF @ok = 0
BEGIN
    SELECT CAST(0 AS bit), @major;
    RETURN;
END;

DECLARE @memModel nvarchar(60);
IF EXISTS (SELECT 1 FROM sys.all_columns WHERE object_id = OBJECT_ID(N'sys.dm_os_sys_info') AND name = N'sql_memory_model_desc')
    EXEC sys.sp_executesql N'/* kakapo */ SELECT @m = sql_memory_model_desc FROM sys.dm_os_sys_info;',
        N'@m nvarchar(60) OUTPUT', @m = @memModel OUTPUT;

DECLARE @pending bigint, @workspaceKB bigint, @freeKB bigint, @cntError nvarchar(2048);
BEGIN TRY
    SELECT @pending     = MAX(CASE WHEN counter_name = N'Memory Grants Pending' THEN cntr_value END),
           @workspaceKB = MAX(CASE WHEN counter_name = N'Granted Workspace Memory (KB)' THEN cntr_value END),
           @freeKB      = MAX(CASE WHEN counter_name = N'Free Memory (KB)' THEN cntr_value END)
    FROM sys.dm_os_performance_counters
    WHERE object_name LIKE N'%Memory Manager%'
      AND counter_name IN (N'Memory Grants Pending', N'Granted Workspace Memory (KB)', N'Free Memory (KB)');
END TRY
BEGIN CATCH
    SET @cntError = ERROR_MESSAGE();
END CATCH;

DECLARE @adhocBytes bigint, @pcError nvarchar(2048);
BEGIN TRY
    SELECT @adhocBytes = ISNULL(SUM(CAST(size_in_bytes AS bigint)), 0)
    FROM sys.dm_exec_cached_plans
    WHERE usecounts = 1 AND objtype = N'Adhoc';
END TRY
BEGIN CATCH
    SET @pcError = ERROR_MESSAGE();
END CATCH;

SELECT CAST(1 AS bit), @major,
       CAST(SERVERPROPERTY('ProductVersion') AS nvarchar(64)),
       CAST(SERVERPROPERTY('Edition') AS nvarchar(128)),
       CONVERT(int, SERVERPROPERTY('EngineEdition')),
       SYSUTCDATETIME(),
       m.total_physical_memory_kb / 1024, m.available_physical_memory_kb / 1024, m.system_memory_state_desc,
       p.physical_memory_in_use_kb / 1024, p.locked_page_allocations_kb / 1024,
       CAST(p.process_physical_memory_low AS bit), CAST(p.process_virtual_memory_low AS bit),
       i.committed_kb / 1024, i.committed_target_kb / 1024,
       DATEADD(minute, -DATEPART(TZOFFSET, SYSDATETIMEOFFSET()), CAST(i.sqlserver_start_time AS datetime2(3))),
       @memModel,
       (SELECT CONVERT(bigint, value_in_use) FROM sys.configurations WHERE name = N'max server memory (MB)'),
       (SELECT CONVERT(bigint, value)        FROM sys.configurations WHERE name = N'max server memory (MB)'),
       (SELECT CONVERT(bigint, value_in_use) FROM sys.configurations WHERE name = N'min server memory (MB)'),
       (SELECT CONVERT(bigint, value)        FROM sys.configurations WHERE name = N'min server memory (MB)'),
       (SELECT CONVERT(int, value_in_use)    FROM sys.configurations WHERE name = N'optimize for ad hoc workloads'),
       ISNULL(@pending, 0), ISNULL(@workspaceKB, 0) / 1024, ISNULL(@freeKB, 0) / 1024, @cntError,
       CAST(ISNULL(@adhocBytes, 0) AS float) / 1048576.0, @pcError
FROM sys.dm_os_sys_memory AS m
CROSS JOIN sys.dm_os_process_memory AS p
CROSS JOIN sys.dm_os_sys_info AS i;

BEGIN TRY
    SELECT RTRIM(instance_name), CAST(cntr_value AS bigint)
    FROM sys.dm_os_performance_counters
    WHERE object_name LIKE N'%Buffer Node%' AND counter_name = N'Page life expectancy'
    ORDER BY instance_name;
END TRY
BEGIN CATCH
    SELECT CAST(NULL AS nvarchar(128)), CAST(NULL AS bigint) WHERE 1 = 0;
END CATCH;

SELECT type, SUM(CAST(pages_kb AS bigint) + CAST(virtual_memory_committed_kb AS bigint)), SUM(CAST(pages_kb AS bigint))
FROM sys.dm_os_memory_clerks
GROUP BY type;`

// MemoryOverview reads panels 2 and 3 of the Memory screen: Windows' memory, SQL Server's share, its
// settings and counters, and the memory clerks. Instant: no buffer pool scan. A login without the right
// gets facts with Permission set and nothing else.
func MemoryOverview(ctx context.Context, db *sql.DB) (domain.MemoryFacts, error) {
	f := domain.MemoryFacts{NodePLE: []domain.NodePLE{}, Clerks: []domain.ClerkFact{}}
	rows, err := db.QueryContext(ctx, Tag(memoryOverviewQuery))
	if err != nil {
		return f, friendlyError(err)
	}
	defer rows.Close()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return f, friendlyError(err)
		}
		return f, errors.New("SQL Server returned nothing about its memory")
	}
	cols, err := rows.Columns()
	if err != nil {
		return f, err
	}
	var ok bool
	var major sql.NullInt64
	if len(cols) == 2 {
		if err := rows.Scan(&ok, &major); err != nil {
			return f, err
		}
		f.Permission = serverStateRight(int(major.Int64))
		f.ReadAt = time.Now().UTC()
		return f, nil
	}
	var state, model, cntErr, pcErr sql.NullString
	var started sql.NullTime
	var maxUse, maxVal, minUse, minVal sql.NullInt64
	var adhoc sql.NullInt64
	if err := rows.Scan(&ok, &major, &f.ProductVersion, &f.Edition, &f.EngineEdition, &f.ReadAt,
		&f.TotalMB, &f.AvailableMB, &state, &f.InUseMB, &f.LockedMB, &f.ProcessPhysLow, &f.ProcessVirtLow,
		&f.CommittedMB, &f.TargetMB, &started, &model, &maxUse, &maxVal, &minUse, &minVal, &adhoc,
		&f.GrantsPending, &f.GrantedWorkspaceMB, &f.FreeMB, &cntErr, &f.SingleUseAdhocMB, &pcErr); err != nil {
		return f, fmt.Errorf("read SQL Server's memory: %w", err)
	}
	f.ReadAt = utc(f.ReadAt)
	if started.Valid {
		f.StartedAt = utc(started.Time)
	}
	f.SystemState, f.MemoryModel, f.CounterError, f.PlanCacheError = state.String, model.String, cntErr.String, pcErr.String
	f.MaxMB, f.MaxValueMB, f.MinMB, f.MinValueMB = maxUse.Int64, maxVal.Int64, minUse.Int64, minVal.Int64
	f.AdHocOn = adhoc.Int64 == 1

	if err := nextResultSet(rows, "page life expectancy"); err != nil {
		return f, err
	}
	for rows.Next() {
		var n domain.NodePLE
		if err := rows.Scan(&n.Node, &n.Seconds); err != nil {
			return f, err
		}
		f.NodePLE = append(f.NodePLE, n)
	}
	if err := nextResultSet(rows, "memory clerk"); err != nil {
		return f, err
	}
	for rows.Next() {
		var c domain.ClerkFact
		if err := rows.Scan(&c.Type, &c.KB, &c.PagesKB); err != nil {
			return f, err
		}
		f.Clerks = append(f.Clerks, c)
	}
	return f, friendlyErrOrNil(rows.Err())
}

// utc reads a datetime2 the query already moved to UTC (it carries no zone) as UTC.
func utc(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), t.Day(), t.Hour(), t.Minute(), t.Second(), t.Nanosecond(), time.UTC)
}

// memoryBuffersQuery is panel 4: one pass over the buffer descriptors grouped by database_id only, then
// the data cache clerk's pages, read straight after so the two can be reconciled. The heavy read of the
// screen (about 12.5 million rows on a 100 GB cache); run only on a click.
const memoryBuffersQuery = safeSession + `SET NOCOUNT ON;
` + memoryPermission + `
IF @ok = 0
BEGIN
    SELECT CAST(0 AS bit), @major;
    RETURN;
END;
SELECT CAST(1 AS bit), @major;
SELECT b.database_id,
       CASE WHEN b.database_id = 32767 THEN N'(resource database)' ELSE ISNULL(DB_NAME(b.database_id), N'(database ' + CAST(b.database_id AS nvarchar(11)) + N')') END,
       b.pages
FROM (SELECT database_id, COUNT_BIG(*) AS pages FROM sys.dm_os_buffer_descriptors GROUP BY database_id) AS b;
SELECT ISNULL(SUM(CAST(pages_kb AS bigint)), 0), SYSUTCDATETIME()
FROM sys.dm_os_memory_clerks
WHERE type = N'MEMORYCLERK_SQLBUFFERPOOL';`

// MemoryBuffers reads the buffer pool by database (panel 4). Bound it with a context: Stop cancels it and
// the statement ends on the server. permission names the right the login lacks ("" when it has it).
func MemoryBuffers(ctx context.Context, db *sql.DB) (b domain.BufferFacts, permission string, err error) {
	b.Databases = []domain.DatabaseBuffers{}
	start := time.Now()
	rows, err := db.QueryContext(ctx, Tag(memoryBuffersQuery))
	if err != nil {
		return b, "", friendlyError(err)
	}
	defer rows.Close()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return b, "", friendlyError(err)
		}
		return b, "", errors.New("SQL Server returned nothing about its buffer pool")
	}
	var ok bool
	var major sql.NullInt64
	if err := rows.Scan(&ok, &major); err != nil {
		return b, "", err
	}
	if !ok {
		return b, serverStateRight(int(major.Int64)), nil
	}
	if err := nextResultSet(rows, "buffer pool"); err != nil {
		return b, "", err
	}
	for rows.Next() {
		var d domain.DatabaseBuffers
		if err := rows.Scan(&d.ID, &d.Name, &d.Pages); err != nil {
			return b, "", err
		}
		b.Databases = append(b.Databases, d)
	}
	if err := nextResultSet(rows, "data cache clerk"); err != nil {
		return b, "", err
	}
	if rows.Next() {
		if err := rows.Scan(&b.DataCacheKB, &b.ReadAt); err != nil {
			return b, "", err
		}
	}
	if err := rows.Err(); err != nil {
		return b, "", friendlyError(err)
	}
	b.ReadAt, b.Seconds = utc(b.ReadAt), time.Since(start).Seconds()
	return b, "", nil
}

// databaseCacheOuter checks the rights (the server's for the buffer descriptors and the usage stats, the
// database's for the sizes) and runs the per-index pass in the database's context. First result: the
// rights, the server's clock and the start time, both UTC.
const databaseCacheOuter = `SET NOCOUNT ON;
` + memoryPermission + `
DECLARE @dbOk bit = CASE WHEN HAS_PERMS_BY_NAME(@db, N'DATABASE', N'VIEW DATABASE STATE') = 1 THEN 1 ELSE 0 END;
IF @dbOk = 0 AND @major >= 16 AND HAS_PERMS_BY_NAME(@db, N'DATABASE', N'VIEW DATABASE PERFORMANCE STATE') = 1 SET @dbOk = 1;
DECLARE @started datetime2(3);
IF @ok = 1 SELECT @started = DATEADD(minute, -DATEPART(TZOFFSET, SYSDATETIMEOFFSET()), CAST(sqlserver_start_time AS datetime2(3))) FROM sys.dm_os_sys_info;
SELECT @ok, @dbOk, @major, SYSUTCDATETIME(), @started;
IF @ok = 1 AND @dbOk = 1
    EXEC %s.sys.sp_executesql @stmt;`

// databaseCacheQuery runs inside the database. The buffer descriptors are grouped by allocation unit
// first (the 12.5 million rows become a few thousand), then joined to the partitions, as the case note's
// query does (pages 12-13). One row per index with pages in cache: names, type, cached and total pages,
// the table's own size, the usage since the restart, the last read in UTC, the key and INCLUDE columns, and
// how many other nonclustered indexes on the table start with the same key column.
const databaseCacheQuery = `SET NOCOUNT ON;
SELECT bd.allocation_unit_id, COUNT_BIG(*) AS pages
INTO #au
FROM sys.dm_os_buffer_descriptors AS bd
WHERE bd.database_id = DB_ID()
GROUP BY bd.allocation_unit_id;

SELECT p.object_id, p.index_id, SUM(a.pages) AS cached_pages
INTO #cache
FROM #au AS a
JOIN sys.allocation_units AS au ON au.allocation_unit_id = a.allocation_unit_id
JOIN sys.partitions AS p ON (au.type IN (1, 3) AND au.container_id = p.hobt_id)
                         OR (au.type = 2 AND au.container_id = p.partition_id)
GROUP BY p.object_id, p.index_id;

DECLARE @tz int = DATEPART(TZOFFSET, SYSDATETIMEOFFSET());
WITH sizes AS (
    SELECT object_id, index_id, SUM(CAST(used_page_count AS bigint)) AS pages
    FROM sys.dm_db_partition_stats
    GROUP BY object_id, index_id
)
SELECT ISNULL(OBJECT_SCHEMA_NAME(c.object_id), N''), ISNULL(OBJECT_NAME(c.object_id), N'(object ' + CAST(c.object_id AS nvarchar(11)) + N')'),
       ISNULL(i.name, N''), c.index_id, ISNULL(i.type_desc, N''),
       c.cached_pages, ISNULL(s.pages, 0),
       ISNULL((SELECT SUM(t.pages) FROM sizes AS t WHERE t.object_id = c.object_id AND t.index_id IN (0, 1)), 0),
       ISNULL(CAST(us.user_seeks AS bigint), 0), ISNULL(CAST(us.user_scans AS bigint), 0),
       ISNULL(CAST(us.user_lookups AS bigint), 0), ISNULL(CAST(us.user_updates AS bigint), 0),
       DATEADD(minute, -@tz, CAST(lr.last_read AS datetime2(3))),
       ISNULL(STUFF((SELECT N', ' + COL_NAME(ic.object_id, ic.column_id)
                     FROM sys.index_columns AS ic
                     WHERE ic.object_id = c.object_id AND ic.index_id = c.index_id AND ic.key_ordinal > 0
                     ORDER BY ic.key_ordinal
                     FOR XML PATH(''), TYPE).value('.', 'nvarchar(max)'), 1, 2, N''), N''),
       ISNULL(STUFF((SELECT N', ' + COL_NAME(ic.object_id, ic.column_id)
                     FROM sys.index_columns AS ic
                     WHERE ic.object_id = c.object_id AND ic.index_id = c.index_id AND ic.is_included_column = 1
                     ORDER BY ic.index_column_id
                     FOR XML PATH(''), TYPE).value('.', 'nvarchar(max)'), 1, 2, N''), N''),
       CASE WHEN i.type = 2 THEN
           (SELECT COUNT(*)
            FROM sys.indexes AS i2
            JOIN sys.index_columns AS k2 ON k2.object_id = i2.object_id AND k2.index_id = i2.index_id AND k2.key_ordinal = 1
            WHERE i2.object_id = c.object_id AND i2.index_id <> c.index_id AND i2.type = 2 AND i2.is_hypothetical = 0
              AND k2.column_id = (SELECT TOP (1) k1.column_id FROM sys.index_columns AS k1
                                  WHERE k1.object_id = c.object_id AND k1.index_id = c.index_id AND k1.key_ordinal = 1))
       ELSE 0 END
FROM #cache AS c
LEFT JOIN sys.indexes AS i ON i.object_id = c.object_id AND i.index_id = c.index_id
LEFT JOIN sizes AS s ON s.object_id = c.object_id AND s.index_id = c.index_id
LEFT JOIN sys.dm_db_index_usage_stats AS us
       ON us.database_id = DB_ID() AND us.object_id = c.object_id AND us.index_id = c.index_id
OUTER APPLY (SELECT MAX(v) AS last_read
             FROM (VALUES (us.last_user_seek), (us.last_user_scan), (us.last_user_lookup)) AS x(v)) AS lr;`

// DatabaseCache reads Database detail for one database: every index with pages in cache. Bound it with a
// context: Stop cancels it and the statement ends on the server. A login without a right gets facts with
// Permission set and nothing else.
func DatabaseCache(ctx context.Context, db *sql.DB, database string) (domain.DatabaseCacheFacts, error) {
	f := domain.DatabaseCacheFacts{Database: database, Indexes: []domain.IndexCacheFact{}}
	start := time.Now()
	outer := fmt.Sprintf(databaseCacheOuter, domain.QuoteName(database))
	rows, err := db.QueryContext(ctx, Tag(safeSession+outer),
		sql.Named("db", database), sql.Named("stmt", TagStatements(databaseCacheQuery)))
	if err != nil {
		return f, friendlyError(err)
	}
	defer rows.Close()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return f, friendlyError(err)
		}
		return f, errors.New("SQL Server returned nothing about the database's cache")
	}
	var srvOK, dbOK bool
	var major sql.NullInt64
	var started sql.NullTime
	if err := rows.Scan(&srvOK, &dbOK, &major, &f.ReadAt, &started); err != nil {
		return f, err
	}
	f.ReadAt = utc(f.ReadAt)
	if started.Valid {
		f.StartedAt = utc(started.Time)
	}
	switch {
	case !srvOK:
		f.Permission = serverStateRight(int(major.Int64))
		return f, nil
	case !dbOK:
		right := "VIEW DATABASE STATE"
		if major.Int64 >= 16 {
			right = "VIEW DATABASE PERFORMANCE STATE (or VIEW DATABASE STATE)"
		}
		f.Permission = right + " in " + database
		return f, nil
	}
	if err := nextResultSet(rows, "index cache"); err != nil {
		return f, err
	}
	for rows.Next() {
		var x domain.IndexCacheFact
		var last sql.NullTime
		if err := rows.Scan(&x.Schema, &x.Table, &x.Index, &x.IndexID, &x.Type, &x.CachedPages, &x.TotalPages, &x.TablePages,
			&x.Seeks, &x.Scans, &x.Lookups, &x.Updates, &last, &x.Keys, &x.Includes, &x.SameLeading); err != nil {
			return f, err
		}
		if last.Valid {
			t := utc(last.Time)
			x.LastRead = &t
		}
		x.Type = strings.TrimSpace(x.Type)
		f.Indexes = append(f.Indexes, x)
	}
	if err := rows.Err(); err != nil {
		return f, friendlyError(err)
	}
	f.Seconds = time.Since(start).Seconds()
	return f, nil
}
