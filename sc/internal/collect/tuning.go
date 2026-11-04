package collect

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"sqlmonitor/internal/domain"
)

// The tuning report's read (docs/phase5c-contract.md, "The read"): Query Store's hour by hour
// history of one database, as the toolkit's TuningReport.sql reads it (results 0 to 5), inside the
// database. SELECTs only, Query Store views only, each area in its own TRY with its own error
// column, the 2017 wait view behind its OBJECT_ID guard, every batch tagged. Times are the server's
// clock: SWITCHOFFSET to the server's current UTC offset, rounded down to the hour, as the toolkit.

// tuningRegressionRule is the toolkit's rule 4 (TuningReport.sql, result 4) with domain's
// thresholds, so the read and the domain test share one set of numbers: the newest plan (by first
// seen, then plan id) ran at least 50 times and uses at least 1.5 x the CPU per run of the previous
// plan, which used at least 1 ms.
var tuningRegressionRule = fmt.Sprintf("n.rn = 1 AND n.execs >= %d AND o.cpu_ms >= %g AND n.cpu_ms >= o.cpu_ms * %g",
	domain.PlanRegressionMinRuns, domain.PlanRegressionMinOldCPUMs, domain.PlanRegressionRatio)

// tuningReportQuery runs inside the database with @days. Areas: About (Query Store's state,
// interval, the oldest interval held, wait capture); Hours (per hour: executions, CPU, reads,
// duration, aborted and exception runs, summed over plans; waits per category, 2017 and later);
// Top shapes (the top 15 query_hash by CPU, their text and object, and per hour runs and CPU);
// Plan regressions (rule 4); Failures (per query, aborted and exception counts, first and last
// failure, top 10). kakapo's own statements and the noise patterns stay out of the queries, as in
// QRY01. Result sets: one row (about and the area errors), the hours, the shapes, the shapes per
// hour, the regressions, the failures.
//
// The totals per interval are avg_* x count_executions, summed, then divided by the summed runs:
// the execution-weighted average, never an average of averages.
// Source: DarlingData sp_QuickieStore.sql 12415-12470 (MIT)
var tuningReportQuery = Tag(`SET NOCOUNT ON;
DECLARE @state nvarchar(60), @interval int, @waitCapture nvarchar(60), @oldest datetime,
        @aboutErr nvarchar(2048), @hoursErr nvarchar(2048), @waitErr nvarchar(2048), @topErr nvarchar(2048),
        @regErr nvarchar(2048), @failErr nvarchar(2048),
        @offset int = DATEPART(TZOFFSET, SYSDATETIMEOFFSET()), @now datetime = GETDATE(),
        @from datetimeoffset = DATEADD(DAY, -@days, SYSDATETIMEOFFSET());
CREATE TABLE #int (interval_id bigint NOT NULL PRIMARY KEY, local_start datetime NOT NULL, hour_start datetime NOT NULL);
CREATE TABLE #rs (interval_id bigint NOT NULL, plan_id bigint NOT NULL, exec_type tinyint NOT NULL, execs bigint NOT NULL, cpu float NOT NULL,
                  reads float NOT NULL, duration float NOT NULL, last_run datetime NULL);
CREATE TABLE #hours (hour_start datetime NOT NULL PRIMARY KEY, execs bigint NULL, cpu bigint NULL, reads bigint NULL, duration bigint NULL,
                     aborted bigint NULL, exceptions bigint NULL);
CREATE TABLE #waits (hour_start datetime NOT NULL, category nvarchar(20) NOT NULL, wait_ms float NOT NULL);
CREATE TABLE #plans (plan_id bigint NOT NULL PRIMARY KEY, query_id bigint NOT NULL, query_hash binary(8) NOT NULL, object_id int NULL);
CREATE TABLE #top (rnk int NOT NULL, query_hash binary(8) NOT NULL PRIMARY KEY, execs bigint NOT NULL, cpu bigint NOT NULL, query_id bigint NOT NULL,
                   object_id int NULL);
CREATE TABLE #shape_hours (query_hash binary(8) NOT NULL, hour_start datetime NOT NULL, execs bigint NOT NULL, cpu bigint NOT NULL);
CREATE TABLE #pp (query_id bigint NOT NULL, plan_id bigint NOT NULL, execs bigint NOT NULL, cpu_ms float NULL, first_seen datetime NOT NULL);
CREATE TABLE #reg (query_id bigint NOT NULL, old_plan bigint NOT NULL, old_cpu_ms float NOT NULL, old_execs bigint NOT NULL, new_plan bigint NOT NULL,
                   new_cpu_ms float NOT NULL, new_execs bigint NOT NULL, first_seen datetime NOT NULL);
CREATE TABLE #fail (query_id bigint NOT NULL, aborted bigint NOT NULL, exceptions bigint NOT NULL, first_failed datetime NULL, last_failed datetime NULL);

/* About: Query Store's state, interval and the oldest interval it holds */
BEGIN TRY
    IF OBJECT_ID(N'sys.query_store_runtime_stats') IS NULL
        SET @aboutErr = N'Query Store needs SQL Server 2016 or later.';
    ELSE
    BEGIN
        SELECT @state = CONVERT(nvarchar(60), actual_state_desc), @interval = interval_length_minutes
        FROM sys.database_query_store_options;
        SELECT @oldest = CONVERT(datetime, SWITCHOFFSET(MIN(i.start_time), @offset)) FROM sys.query_store_runtime_stats_interval AS i;
        IF OBJECT_ID(N'sys.query_store_wait_stats') IS NOT NULL
            EXEC sys.sp_executesql N'/* kakapo */ SELECT @w = CONVERT(nvarchar(60), wait_stats_capture_mode_desc) FROM sys.database_query_store_options;',
                N'@w nvarchar(60) OUTPUT', @w = @waitCapture OUTPUT;
    END;
END TRY
BEGIN CATCH
    SET @aboutErr = ERROR_MESSAGE();
END CATCH;

/* Hours: every plan's runtime per interval in the window, summed per hour of the server's clock */
IF @aboutErr IS NULL
BEGIN TRY
    INSERT #int (interval_id, local_start, hour_start)
    SELECT i.runtime_stats_interval_id, CONVERT(datetime, SWITCHOFFSET(i.start_time, @offset)),
           DATEADD(HOUR, DATEDIFF(HOUR, 0, CONVERT(datetime, SWITCHOFFSET(i.start_time, @offset))), 0)
    FROM sys.query_store_runtime_stats_interval AS i
    WHERE i.end_time > @from;

    INSERT #rs (interval_id, plan_id, exec_type, execs, cpu, reads, duration, last_run)
    SELECT rs.runtime_stats_interval_id, rs.plan_id, rs.execution_type, rs.count_executions,
           rs.avg_cpu_time * rs.count_executions, rs.avg_logical_io_reads * rs.count_executions, rs.avg_duration * rs.count_executions,
           CONVERT(datetime, SWITCHOFFSET(rs.last_execution_time, @offset))
    FROM sys.query_store_runtime_stats AS rs
    JOIN #int AS i ON i.interval_id = rs.runtime_stats_interval_id;

    INSERT #hours (hour_start, execs, cpu, reads, duration, aborted, exceptions)
    SELECT i.hour_start, SUM(r.execs), CONVERT(bigint, SUM(r.cpu)), CONVERT(bigint, SUM(r.reads)), CONVERT(bigint, SUM(r.duration)),
           SUM(CASE WHEN r.exec_type = 3 THEN r.execs ELSE 0 END), SUM(CASE WHEN r.exec_type = 4 THEN r.execs ELSE 0 END)
    FROM #int AS i
    LEFT JOIN #rs AS r ON r.interval_id = i.interval_id
    GROUP BY i.hour_start;
END TRY
BEGIN CATCH
    SET @hoursErr = ERROR_MESSAGE();
END CATCH;

/* Waits per hour by category: Parallelism, Lock, CPU, the rest as Other (2017 and later) */
IF @aboutErr IS NULL AND @hoursErr IS NULL
BEGIN TRY
    IF OBJECT_ID(N'sys.query_store_wait_stats') IS NULL
        SET @waitErr = N'Query Store keeps waits from SQL Server 2017 on.';
    ELSE
        EXEC sys.sp_executesql N'/* kakapo */ INSERT #waits (hour_start, category, wait_ms)
            SELECT i.hour_start, c.category, SUM(ws.total_query_wait_time_ms)
            FROM sys.query_store_wait_stats AS ws
            JOIN #int AS i ON i.interval_id = ws.runtime_stats_interval_id
            CROSS APPLY (SELECT CASE WHEN ws.wait_category_desc IN (N''Parallelism'', N''Lock'', N''CPU'')
                                     THEN CONVERT(nvarchar(20), ws.wait_category_desc) ELSE N''Other'' END AS category) AS c
            GROUP BY i.hour_start, c.category;';
END TRY
BEGIN CATCH
    SET @waitErr = ERROR_MESSAGE();
END CATCH;

/* Top shapes: the top 15 query_hash by CPU in the window, and their runs and CPU per hour */
IF @aboutErr IS NULL AND @hoursErr IS NULL
BEGIN TRY
    INSERT #plans (plan_id, query_id, query_hash, object_id)
    SELECT p.plan_id, p.query_id, q.query_hash, q.object_id
    FROM sys.query_store_plan AS p
    JOIN sys.query_store_query AS q ON q.query_id = p.query_id
    JOIN sys.query_store_query_text AS qt ON qt.query_text_id = q.query_text_id
    WHERE p.plan_id IN (SELECT DISTINCT r.plan_id FROM #rs AS r)
      AND ` + noiseFilter("qt.query_sql_text") + `;

    INSERT #top (rnk, query_hash, execs, cpu, query_id, object_id)
    SELECT TOP (15) ROW_NUMBER() OVER (ORDER BY SUM(r.cpu) DESC), p.query_hash, SUM(r.execs), CONVERT(bigint, SUM(r.cpu)), MAX(p.query_id),
           MAX(NULLIF(p.object_id, 0))
    FROM #rs AS r
    JOIN #plans AS p ON p.plan_id = r.plan_id
    GROUP BY p.query_hash
    ORDER BY SUM(r.cpu) DESC;

    INSERT #shape_hours (query_hash, hour_start, execs, cpu)
    SELECT p.query_hash, i.hour_start, SUM(r.execs), CONVERT(bigint, SUM(r.cpu))
    FROM #rs AS r
    JOIN #int AS i ON i.interval_id = r.interval_id
    JOIN #plans AS p ON p.plan_id = r.plan_id
    WHERE p.query_hash IN (SELECT t.query_hash FROM #top AS t)
    GROUP BY p.query_hash, i.hour_start;
END TRY
BEGIN CATCH
    SET @topErr = ERROR_MESSAGE();
END CATCH;

/* Plan regressions: queries with more than one plan in the window; the newest plan against the one before */
IF @aboutErr IS NULL AND @hoursErr IS NULL AND @topErr IS NULL
BEGIN TRY
    INSERT #pp (query_id, plan_id, execs, cpu_ms, first_seen)
    SELECT p.query_id, r.plan_id, SUM(r.execs), SUM(r.cpu) / NULLIF(SUM(r.execs), 0) / 1000.0, MIN(i.local_start)
    FROM #rs AS r
    JOIN #int AS i ON i.interval_id = r.interval_id
    JOIN #plans AS p ON p.plan_id = r.plan_id
    WHERE p.query_id IN (SELECT p2.query_id FROM #plans AS p2 GROUP BY p2.query_id HAVING COUNT(*) > 1)
    GROUP BY p.query_id, r.plan_id;

    WITH x AS (
        SELECT pp.query_id, pp.plan_id, pp.execs, pp.cpu_ms, pp.first_seen,
               ROW_NUMBER() OVER (PARTITION BY pp.query_id ORDER BY pp.first_seen DESC, pp.plan_id DESC) AS rn
        FROM #pp AS pp)
    INSERT #reg (query_id, old_plan, old_cpu_ms, old_execs, new_plan, new_cpu_ms, new_execs, first_seen)
    SELECT TOP (10) n.query_id, o.plan_id, o.cpu_ms, o.execs, n.plan_id, n.cpu_ms, n.execs, n.first_seen
    FROM x AS n
    JOIN x AS o ON o.query_id = n.query_id AND o.rn = 2
    WHERE ` + tuningRegressionRule + `
    ORDER BY (n.cpu_ms - o.cpu_ms) * n.execs DESC;
END TRY
BEGIN CATCH
    SET @regErr = ERROR_MESSAGE();
END CATCH;

/* Failures: aborted (timeouts, cancels) and exception runs per query, the first and the last */
IF @aboutErr IS NULL AND @hoursErr IS NULL AND @topErr IS NULL
BEGIN TRY
    INSERT #fail (query_id, aborted, exceptions, first_failed, last_failed)
    SELECT TOP (10) p.query_id, SUM(CASE WHEN r.exec_type = 3 THEN r.execs ELSE 0 END), SUM(CASE WHEN r.exec_type = 4 THEN r.execs ELSE 0 END),
           MIN(i.local_start), MAX(r.last_run)
    FROM #rs AS r
    JOIN #int AS i ON i.interval_id = r.interval_id
    JOIN #plans AS p ON p.plan_id = r.plan_id
    WHERE r.exec_type <> 0
    GROUP BY p.query_id
    ORDER BY SUM(r.execs) DESC;
END TRY
BEGIN CATCH
    SET @failErr = ERROR_MESSAGE();
END CATCH;

SELECT CAST(ISNULL(@@SERVERNAME, N'') AS nvarchar(256)), @state, ISNULL(@interval, 0), @waitCapture, @oldest, @now, @offset,
       @aboutErr, @hoursErr, @waitErr, @topErr, @regErr, @failErr;
SELECT h.hour_start, ISNULL(h.execs, 0), ISNULL(h.cpu, 0), ISNULL(h.reads, 0), ISNULL(h.duration, 0), ISNULL(h.aborted, 0), ISNULL(h.exceptions, 0),
       ISNULL(w.parallel_sec, 0), ISNULL(w.lock_sec, 0), ISNULL(w.cpu_sec, 0), ISNULL(w.other_sec, 0)
FROM #hours AS h
OUTER APPLY (SELECT SUM(CASE WHEN x.category = N'Parallelism' THEN x.wait_ms END) / 1000.0 AS parallel_sec,
                    SUM(CASE WHEN x.category = N'Lock' THEN x.wait_ms END) / 1000.0 AS lock_sec,
                    SUM(CASE WHEN x.category = N'CPU' THEN x.wait_ms END) / 1000.0 AS cpu_sec,
                    SUM(CASE WHEN x.category = N'Other' THEN x.wait_ms END) / 1000.0 AS other_sec
             FROM #waits AS x WHERE x.hour_start = h.hour_start) AS w
ORDER BY h.hour_start;
SELECT t.rnk, CONVERT(varchar(18), t.query_hash, 1),
       CASE WHEN t.object_id > 0 THEN OBJECT_SCHEMA_NAME(t.object_id) + N'.' + OBJECT_NAME(t.object_id) END,
       LEFT(LTRIM(REPLACE(REPLACE(REPLACE(qt.query_sql_text, NCHAR(13), N' '), NCHAR(10), N' '), NCHAR(9), N' ')), 300),
       t.execs, t.cpu
FROM #top AS t
JOIN sys.query_store_query AS q ON q.query_id = t.query_id
JOIN sys.query_store_query_text AS qt ON qt.query_text_id = q.query_text_id
ORDER BY t.rnk;
SELECT CONVERT(varchar(18), s.query_hash, 1), s.hour_start, s.execs, s.cpu FROM #shape_hours AS s ORDER BY s.query_hash, s.hour_start;
SELECT g.query_id, CONVERT(varchar(18), q.query_hash, 1),
       CASE WHEN q.object_id > 0 THEN OBJECT_SCHEMA_NAME(q.object_id) + N'.' + OBJECT_NAME(q.object_id) END,
       LEFT(LTRIM(REPLACE(REPLACE(REPLACE(qt.query_sql_text, NCHAR(13), N' '), NCHAR(10), N' '), NCHAR(9), N' ')), 300),
       g.old_plan, g.old_cpu_ms, g.old_execs, g.new_plan, g.new_cpu_ms, g.new_execs, g.first_seen
FROM #reg AS g
JOIN sys.query_store_query AS q ON q.query_id = g.query_id
JOIN sys.query_store_query_text AS qt ON qt.query_text_id = q.query_text_id
ORDER BY (g.new_cpu_ms - g.old_cpu_ms) * g.new_execs DESC;
SELECT f.query_id, CONVERT(varchar(18), q.query_hash, 1),
       CASE WHEN q.object_id > 0 THEN OBJECT_SCHEMA_NAME(q.object_id) + N'.' + OBJECT_NAME(q.object_id) END,
       LEFT(LTRIM(REPLACE(REPLACE(REPLACE(qt.query_sql_text, NCHAR(13), N' '), NCHAR(10), N' '), NCHAR(9), N' ')), 300),
       f.aborted, f.exceptions, f.first_failed, f.last_failed
FROM #fail AS f
JOIN sys.query_store_query AS q ON q.query_id = f.query_id
JOIN sys.query_store_query_text AS qt ON qt.query_text_id = q.query_text_id
ORDER BY f.aborted + f.exceptions DESC;`)

// serverClockQuery reads the server's clock and its UTC offset, to place a time given in server
// time (a change recorded by hand).
var serverClockQuery = Tag(`SELECT GETDATE(), DATEPART(TZOFFSET, SYSDATETIMEOFFSET());`)

// tuningReadTimeout bounds the report's read: a large Query Store over 30 days can take a minute or
// two, as the toolkit's README warns.
const tuningReadTimeout = 3 * time.Minute

// TuningReport reads the tuning report's facts of one database over the last days (1 to 30). An
// area that cannot be read carries its reason in the facts; the error is for a batch that could
// not run at all.
func TuningReport(ctx context.Context, db *sql.DB, database string, days int) (domain.TuningFacts, error) {
	f := domain.TuningFacts{Database: database, Days: days}
	ctx, cancel := context.WithTimeout(ctx, tuningReadTimeout)
	defer cancel()
	q := Tag(safeSession + "EXEC " + domain.QuoteName(database) + ".sys.sp_executesql @stmt, N'@days int', @days = @days;")
	rows, err := db.QueryContext(ctx, q, sql.Named("stmt", tuningReportQuery), sql.Named("days", days))
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
	var state, waitCapture, aboutErr, hoursErr, waitErr, topErr, regErr, failErr sql.NullString
	var oldest sql.NullTime
	var now time.Time
	if err := rows.Scan(&f.ServerName, &state, &f.IntervalMinutes, &waitCapture, &oldest, &now, &f.UTCOffsetMinutes,
		&aboutErr, &hoursErr, &waitErr, &topErr, &regErr, &failErr); err != nil {
		return f, fmt.Errorf("read the tuning report of %s: %w", database, err)
	}
	f.QueryStoreState, f.WaitCapture, f.OldestInterval, f.ServerNow = state.String, waitCapture.String, naivePtr(oldest), naive(now)
	f.AboutError, f.HoursError, f.WaitsError = aboutErr.String, hoursErr.String, waitErr.String
	f.ShapesError, f.RegressionsError, f.FailuresError = topErr.String, regErr.String, failErr.String

	if err := nextResultSet(rows, "hours"); err != nil {
		return f, err
	}
	f.Hours = []domain.TuningHourFact{}
	for rows.Next() {
		var h domain.TuningHourFact
		if err := rows.Scan(&h.Hour, &h.Executions, &h.CPU, &h.Reads, &h.Duration, &h.Aborted, &h.Exceptions,
			&h.ParallelWaitSec, &h.LockWaitSec, &h.CPUWaitSec, &h.OtherWaitSec); err != nil {
			return f, err
		}
		h.Hour = naive(h.Hour)
		f.Hours = append(f.Hours, h)
	}

	if err := nextResultSet(rows, "top shapes"); err != nil {
		return f, err
	}
	f.Shapes = []domain.TuningShapeFact{}
	for rows.Next() {
		var s domain.TuningShapeFact
		var object, text sql.NullString
		if err := rows.Scan(&s.Rank, &s.QueryHash, &object, &text, &s.Executions, &s.CPU); err != nil {
			return f, err
		}
		s.ObjectName, s.Text = object.String, text.String
		f.Shapes = append(f.Shapes, s)
	}

	if err := nextResultSet(rows, "shapes per hour"); err != nil {
		return f, err
	}
	f.ShapeHours = []domain.TuningShapeHourFact{}
	for rows.Next() {
		var s domain.TuningShapeHourFact
		if err := rows.Scan(&s.QueryHash, &s.Hour, &s.Executions, &s.CPU); err != nil {
			return f, err
		}
		s.Hour = naive(s.Hour)
		f.ShapeHours = append(f.ShapeHours, s)
	}

	if err := nextResultSet(rows, "plan regressions"); err != nil {
		return f, err
	}
	f.Regressions = []domain.PlanRegressionFact{}
	for rows.Next() {
		var g domain.PlanRegressionFact
		var object, text sql.NullString
		if err := rows.Scan(&g.QueryID, &g.QueryHash, &object, &text, &g.OldPlanID, &g.OldCPUMs, &g.OldRuns, &g.NewPlanID, &g.NewCPUMs,
			&g.NewRuns, &g.NewFirstSeen); err != nil {
			return f, err
		}
		g.ObjectName, g.Text, g.NewFirstSeen = object.String, text.String, naive(g.NewFirstSeen)
		f.Regressions = append(f.Regressions, g)
	}

	if err := nextResultSet(rows, "failures"); err != nil {
		return f, err
	}
	f.Failures = []domain.QueryFailureFact{}
	for rows.Next() {
		var fl domain.QueryFailureFact
		var object, text sql.NullString
		var first, last sql.NullTime
		if err := rows.Scan(&fl.QueryID, &fl.QueryHash, &object, &text, &fl.Aborted, &fl.Exceptions, &first, &last); err != nil {
			return f, err
		}
		fl.ObjectName, fl.Text, fl.FirstFailed, fl.LastFailed = object.String, text.String, naivePtr(first), naivePtr(last)
		f.Failures = append(f.Failures, fl)
	}
	return f, friendlyErrOrNil(rows.Err())
}

// ServerClock reads the server's clock (GETDATE, kept naive) and its offset from UTC in minutes.
func ServerClock(ctx context.Context, db *sql.DB) (time.Time, int, error) {
	var now time.Time
	var offset int
	if err := db.QueryRowContext(ctx, serverClockQuery).Scan(&now, &offset); err != nil {
		return time.Time{}, 0, friendlyError(err)
	}
	return naive(now), offset, nil
}
