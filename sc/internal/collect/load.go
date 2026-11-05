package collect

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"sqlmonitor/internal/domain"
)

// The load strip's read (docs/phase5i-contract.md, section 2): Query Store hour by hour from a day on,
// inside the database, bucketed by the tuning report's own hour fragment (queryStoreHours). SELECTs
// only, Query Store views only (and cpu_count), each area in its own TRY with its own error, the 2017
// wait view behind its OBJECT_ID guard inside a nested batch, every batch tagged. Query Store's views
// show the interval in progress without waiting for the flush, so the last 30 minutes are read from
// Query Store too.

// loadQuery runs inside the database with @fromText, the first day read (server-local midnight, ISO
// 8601). Areas: About (Query Store's state, interval length, the oldest and newest interval held); the
// cores SQL Server sees (cpu_count, its own TRY: it needs VIEW SERVER STATE); Hours (per server-local
// hour from @from: executions and CPU summed over plans, as 5c); Waits (per hour, every category but
// Idle and User Wait, which are sleeping and WAITFOR rather than load, summed, 2017 and later); Now (the interval in progress and the one before it: executions, CPU
// and waits, and their start). Result sets: one row (about, now and the area errors), the hours.
var loadQuery = Tag(`SET NOCOUNT ON;
DECLARE @state nvarchar(60), @interval int, @oldest datetime, @newest datetime, @cpuCount int,
        @aboutErr nvarchar(2048), @cpuErr nvarchar(2048), @hoursErr nvarchar(2048), @waitErr nvarchar(2048), @nowErr nvarchar(2048),
        @offset int = DATEPART(TZOFFSET, SYSDATETIMEOFFSET()), @now datetime = GETDATE(),
        @nowStart datetime, @nowExecs bigint, @nowCpu bigint, @nowWaitMs float;
DECLARE @from datetimeoffset = TODATETIMEOFFSET(CONVERT(datetime, @fromText, 126), @offset);
` + queryStoreHoursTable + `
CREATE TABLE #hours (hour_start datetime NOT NULL PRIMARY KEY, execs bigint NOT NULL, cpu bigint NOT NULL);
CREATE TABLE #waits (hour_start datetime NOT NULL PRIMARY KEY, wait_ms float NOT NULL);
CREATE TABLE #now (interval_id bigint NOT NULL PRIMARY KEY);

/* About: Query Store's state, interval and the oldest and newest interval it holds */
BEGIN TRY
    IF OBJECT_ID(N'sys.query_store_runtime_stats') IS NULL
        SET @aboutErr = N'Query Store needs SQL Server 2016 or later.';
    ELSE
    BEGIN
        SELECT @state = CONVERT(nvarchar(60), actual_state_desc), @interval = interval_length_minutes
        FROM sys.database_query_store_options;
        SELECT @oldest = CONVERT(datetime, SWITCHOFFSET(MIN(i.start_time), @offset)), @newest = CONVERT(datetime, SWITCHOFFSET(MAX(i.end_time), @offset))
        FROM sys.query_store_runtime_stats_interval AS i;
    END;
END TRY
BEGIN CATCH
    SET @aboutErr = ERROR_MESSAGE();
END CATCH;

/* About: the cores SQL Server sees, for "1.7 of 8 cores" */
BEGIN TRY
    SELECT @cpuCount = cpu_count FROM sys.dm_os_sys_info;
END TRY
BEGIN CATCH
    SET @cpuErr = ERROR_MESSAGE();
END CATCH;

/* Hours: every plan's runtime per interval from @from, summed per hour of the server's clock (5c's sums) */
IF @aboutErr IS NULL
BEGIN TRY
    ` + queryStoreHours + `

    INSERT #hours (hour_start, execs, cpu)
    SELECT i.hour_start, ISNULL(SUM(rs.count_executions), 0), ISNULL(CONVERT(bigint, SUM(rs.avg_cpu_time * rs.count_executions)), 0)
    FROM #int AS i
    LEFT JOIN sys.query_store_runtime_stats AS rs ON rs.runtime_stats_interval_id = i.interval_id
    GROUP BY i.hour_start;
END TRY
BEGIN CATCH
    SET @hoursErr = ERROR_MESSAGE();
END CATCH;

/* Waits per hour: every category but Idle and User Wait (sleeping tasks and WAITFOR are not load), summed
   (2017 and later) */
IF @aboutErr IS NULL AND @hoursErr IS NULL
BEGIN TRY
    IF OBJECT_ID(N'sys.query_store_wait_stats') IS NULL
        SET @waitErr = N'Query Store keeps waits from SQL Server 2017 on.';
    ELSE
        EXEC sys.sp_executesql N'/* kakapo */ INSERT #waits (hour_start, wait_ms)
            SELECT i.hour_start, SUM(ws.total_query_wait_time_ms)
            FROM sys.query_store_wait_stats AS ws
            JOIN #int AS i ON i.interval_id = ws.runtime_stats_interval_id
            WHERE ws.wait_category_desc NOT IN (N''Idle'', N''User Wait'')
            GROUP BY i.hour_start;';
END TRY
BEGIN CATCH
    SET @waitErr = ERROR_MESSAGE();
END CATCH;

/* Now: the interval in progress and the one before it, by the clock (Query Store's intervals start on
   multiples of their length in UTC), so an idle database reads as nothing ran */
IF @aboutErr IS NULL AND @interval > 0
BEGIN TRY
    DECLARE @utcNow datetime = SYSUTCDATETIME();
    DECLARE @prevUtc datetimeoffset = TODATETIMEOFFSET(DATEADD(MINUTE, (DATEDIFF(MINUTE, 0, @utcNow) / @interval - 1) * @interval, 0), 0);
    SET @nowStart = CONVERT(datetime, SWITCHOFFSET(@prevUtc, @offset));

    INSERT #now (interval_id)
    SELECT i.runtime_stats_interval_id FROM sys.query_store_runtime_stats_interval AS i WHERE i.start_time >= @prevUtc;

    SELECT @nowExecs = ISNULL(SUM(rs.count_executions), 0), @nowCpu = ISNULL(CONVERT(bigint, SUM(rs.avg_cpu_time * rs.count_executions)), 0)
    FROM sys.query_store_runtime_stats AS rs
    WHERE rs.runtime_stats_interval_id IN (SELECT n.interval_id FROM #now AS n);

    IF OBJECT_ID(N'sys.query_store_wait_stats') IS NOT NULL
        EXEC sys.sp_executesql N'/* kakapo */ SELECT @w = ISNULL(SUM(ws.total_query_wait_time_ms), 0)
            FROM sys.query_store_wait_stats AS ws
            WHERE ws.runtime_stats_interval_id IN (SELECT n.interval_id FROM #now AS n)
              AND ws.wait_category_desc NOT IN (N''Idle'', N''User Wait'');',
            N'@w float OUTPUT', @w = @nowWaitMs OUTPUT;
END TRY
BEGIN CATCH
    SET @nowErr = ERROR_MESSAGE();
END CATCH;

SELECT @state, ISNULL(@interval, 0), @oldest, @newest, @now, @offset, ISNULL(@cpuCount, 0),
       @aboutErr, @cpuErr, @hoursErr, @waitErr, @nowErr,
       @nowStart, ISNULL(@nowExecs, 0), ISNULL(@nowCpu, 0), ISNULL(@nowWaitMs, 0) / 1000.0;
SELECT h.hour_start, h.execs, h.cpu, ISNULL(w.wait_ms, 0) / 1000.0
FROM #hours AS h
LEFT JOIN #waits AS w ON w.hour_start = h.hour_start
ORDER BY h.hour_start;`)

// loadReadTimeout bounds the load's read: 30 days of 15-minute intervals took 160 ms on OldSchool.
const loadReadTimeout = time.Minute

// LoadHours reads the load strip's facts of one database from the day from on (server-local
// midnight). An area that cannot be read carries its reason in the facts; the error is for a batch
// that could not run at all.
func LoadHours(ctx context.Context, db *sql.DB, database string, from time.Time) (domain.LoadFacts, error) {
	f := domain.LoadFacts{Database: database, From: from}
	ctx, cancel := context.WithTimeout(ctx, loadReadTimeout)
	defer cancel()
	q := Tag(safeSession + "EXEC " + domain.QuoteName(database) + ".sys.sp_executesql @stmt, N'@fromText nvarchar(30)', @fromText = @fromText;")
	rows, err := db.QueryContext(ctx, q, sql.Named("stmt", loadQuery), sql.Named("fromText", from.Format("2006-01-02T15:04:05")))
	if err != nil {
		return f, friendlyError(err)
	}
	defer rows.Close()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return f, friendlyError(err)
		}
		return f, errors.New("SQL Server returned nothing from Query Store")
	}
	var state, aboutErr, cpuErr, hoursErr, waitErr, nowErr sql.NullString
	var oldest, newest, nowStart sql.NullTime
	var now time.Time
	if err := rows.Scan(&state, &f.IntervalMinutes, &oldest, &newest, &now, &f.UTCOffsetMinutes, &f.CPUCount,
		&aboutErr, &cpuErr, &hoursErr, &waitErr, &nowErr,
		&nowStart, &f.NowExecutions, &f.NowCPU, &f.NowWaitSec); err != nil {
		return f, fmt.Errorf("read the load of %s: %w", database, err)
	}
	f.QueryStoreState, f.ServerNow = state.String, naive(now)
	f.OldestInterval, f.NewestInterval, f.NowStart = naivePtr(oldest), naivePtr(newest), naivePtr(nowStart)
	f.AboutError, f.CPUCountError, f.HoursError, f.WaitsError, f.NowError = aboutErr.String, cpuErr.String, hoursErr.String, waitErr.String, nowErr.String

	if err := nextResultSet(rows, "hours"); err != nil {
		return f, err
	}
	f.Hours = []domain.LoadHourFact{}
	for rows.Next() {
		var h domain.LoadHourFact
		if err := rows.Scan(&h.Hour, &h.Executions, &h.CPU, &h.WaitSec); err != nil {
			return f, err
		}
		h.Hour = naive(h.Hour)
		f.Hours = append(f.Hours, h)
	}
	return f, friendlyErrOrNil(rows.Err())
}
