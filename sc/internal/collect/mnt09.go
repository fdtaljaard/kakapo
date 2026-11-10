package collect

import (
	"context"
	"database/sql"
	"fmt"

	"sqlmonitor/internal/domain"
)

// indexJobsQuery reads MNT09's facts inside a database (docs/phase5q-contract.md, section 1),
// SELECTs only:
//   - the procedures whose code alters indexes (ALTER INDEX, DBREINDEX, INDEXDEFRAG) it picks from
//     sys.dm_db_index_physical_stats or DBCC SHOWCONTIG, kakapo's own and Ola's IndexOptimize left out;
//   - in msdb, in its own TRY (empty on Express, refused to a login without msdb rights): the enabled
//     jobs whose step names one of them, and the other enabled index job steps (IndexOptimize, ALTER
//     INDEX, DBREINDEX) that name none;
//   - the fingerprint: index statistics updated in the last 8 days that read every row, on indexes
//     over @minPages pages (sys.allocation_units, no VIEW DATABASE STATE needed), counted by hour of
//     the server clock: the latest hour with @minCount or more, else the latest hour.
//
// Result sets: one row (error, msdb error, hour, count); the procedures; the jobs naming them; the
// other index steps.
var indexJobsQuery = TagStatements(`SET NOCOUNT ON;
DECLARE @err nvarchar(2048), @jobsErr nvarchar(2048), @hour datetime, @count int;
DECLARE @p TABLE (schema_name sysname NOT NULL, name sysname NOT NULL, rebuilds bit NOT NULL, reorganizes bit NOT NULL, online bit NOT NULL);
DECLARE @j TABLE (schema_name sysname NOT NULL, name sysname NOT NULL, job sysname NOT NULL);
DECLARE @s TABLE (job sysname NOT NULL, command nvarchar(max) NULL);
BEGIN TRY
    INSERT @p (schema_name, name, rebuilds, reorganizes, online)
    SELECT SCHEMA_NAME(o.schema_id), o.name,
           CASE WHEN m.definition LIKE N'%REBUILD%' OR m.definition LIKE N'%DBREINDEX%' THEN 1 ELSE 0 END,
           CASE WHEN m.definition LIKE N'%REORGANIZE%' OR m.definition LIKE N'%INDEXDEFRAG%' THEN 1 ELSE 0 END,
           CASE WHEN m.definition LIKE N'%ONLINE%=%ON%' THEN 1 ELSE 0 END
    FROM sys.sql_modules AS m
    JOIN sys.objects AS o ON o.object_id = m.object_id
    WHERE o.type = 'P' AND o.is_ms_shipped = 0
      AND (m.definition LIKE N'%ALTER INDEX%' OR m.definition LIKE N'%DBREINDEX%' OR m.definition LIKE N'%INDEXDEFRAG%')
      AND (m.definition LIKE N'%dm[_]db[_]index[_]physical[_]stats%' OR m.definition LIKE N'%SHOWCONTIG%')
      AND o.name NOT LIKE N'kakapo[_]%' AND o.name <> N'IndexOptimize';

    SELECT TOP (1) @hour = h.hour, @count = h.n
    FROM (SELECT DATEADD(HOUR, DATEDIFF(HOUR, 0, sp.last_updated), 0) AS hour, COUNT(*) AS n
          FROM sys.stats AS st
          JOIN sys.indexes AS i ON i.object_id = st.object_id AND i.index_id = st.stats_id
          JOIN sys.objects AS o ON o.object_id = st.object_id AND o.is_ms_shipped = 0
          CROSS APPLY sys.dm_db_stats_properties(st.object_id, st.stats_id) AS sp
          WHERE sp.last_updated >= DATEADD(DAY, -8, GETDATE()) AND sp.rows > 0 AND sp.rows_sampled = sp.rows
            AND (SELECT SUM(au.used_pages)
                 FROM sys.partitions AS p
                 JOIN sys.allocation_units AS au ON au.container_id = p.partition_id
                 WHERE p.object_id = st.object_id AND p.index_id = st.stats_id) > @minPages
          GROUP BY DATEADD(HOUR, DATEDIFF(HOUR, 0, sp.last_updated), 0)) AS h
    ORDER BY CASE WHEN h.n >= @minCount THEN 0 ELSE 1 END, h.hour DESC;
END TRY
BEGIN CATCH
    SET @err = ERROR_MESSAGE();
END CATCH;
BEGIN TRY
    IF OBJECT_ID(N'msdb.dbo.sysjobs') IS NOT NULL
    BEGIN
        INSERT @j (schema_name, name, job)
        SELECT DISTINCT p.schema_name, p.name, j.name
        FROM @p AS p
        JOIN msdb.dbo.sysjobsteps AS s ON CHARINDEX(p.name, s.command) > 0
        JOIN msdb.dbo.sysjobs AS j ON j.job_id = s.job_id
        WHERE j.enabled = 1;
        INSERT @s (job, command)
        SELECT TOP (20) j.name, s.command
        FROM msdb.dbo.sysjobs AS j
        JOIN msdb.dbo.sysjobsteps AS s ON s.job_id = j.job_id
        WHERE j.enabled = 1
          AND (s.command LIKE N'%IndexOptimize%' OR s.command LIKE N'%ALTER INDEX%' OR s.command LIKE N'%DBREINDEX%')
          AND NOT EXISTS (SELECT 1 FROM @p AS p WHERE CHARINDEX(p.name, s.command) > 0)
        ORDER BY j.name;
    END;
END TRY
BEGIN CATCH
    SET @jobsErr = ERROR_MESSAGE();
END CATCH;
SELECT @err, @jobsErr, @hour, @count;
SELECT schema_name, name, rebuilds, reorganizes, online FROM @p ORDER BY schema_name, name;
SELECT schema_name, name, job FROM @j ORDER BY schema_name, name, job;
SELECT job, command FROM @s ORDER BY job;`)

// readIndexJobs fills MNT09's facts. A failed read inside the database is IndexJobError; msdb that
// could not be read leaves the jobs empty and says so in IndexJobStepsError.
func readIndexJobs(ctx context.Context, db *sql.DB, database string, f *domain.UpkeepFacts) {
	rows, err := db.QueryContext(ctx, Tag(safeSession+"EXEC "+domain.QuoteName(database)+
		".sys.sp_executesql @stmt, N'@minPages bigint, @minCount int', @minPages = @minPages, @minCount = @minCount;"),
		sql.Named("stmt", indexJobsQuery), sql.Named("minPages", int64(domain.Mnt09FingerprintPages)), sql.Named("minCount", domain.Mnt09FingerprintMin))
	if err != nil {
		f.IndexJobError = friendlyError(err).Error()
		return
	}
	defer rows.Close()
	var msg, jobsMsg sql.NullString
	var hour sql.NullTime
	var count sql.NullInt64
	if !rows.Next() {
		f.IndexJobError = "the read returned nothing"
		return
	}
	if err := rows.Scan(&msg, &jobsMsg, &hour, &count); err != nil {
		f.IndexJobError = err.Error()
		return
	}
	if msg.Valid {
		f.IndexJobError = msg.String
		return
	}
	for rows.Next() { // the driver moves to the next result set only once this one is read to its end
	}
	f.IndexJobStepsError = jobsMsg.String
	if hour.Valid && count.Int64 > 0 {
		h := naive(hour.Time)
		f.RebuildHour, f.RebuildCount = &h, int(count.Int64)
	}
	at := map[string]int{}
	if rows.NextResultSet() {
		for rows.Next() {
			var p domain.IndexProcedure
			if err := rows.Scan(&p.Schema, &p.Name, &p.Rebuilds, &p.Reorganizes, &p.Online); err != nil {
				f.IndexJobError = err.Error()
				return
			}
			at[p.Schema+"."+p.Name] = len(f.IndexProcedures)
			f.IndexProcedures = append(f.IndexProcedures, p)
		}
	}
	if rows.NextResultSet() {
		for rows.Next() {
			var schema, name, job string
			if err := rows.Scan(&schema, &name, &job); err != nil {
				f.IndexJobError = err.Error()
				return
			}
			if i, ok := at[schema+"."+name]; ok {
				f.IndexProcedures[i].Jobs = append(f.IndexProcedures[i].Jobs, job)
			}
		}
	}
	if rows.NextResultSet() {
		for rows.Next() {
			var s domain.IndexJobStep
			var cmd sql.NullString
			if err := rows.Scan(&s.Job, &cmd); err != nil {
				f.IndexJobError = err.Error()
				return
			}
			s.Command = cmd.String
			f.IndexJobSteps = append(f.IndexJobSteps, s)
		}
	}
	if err := rows.Err(); err != nil {
		f.IndexJobError = fmt.Sprint(friendlyError(err))
	}
}
