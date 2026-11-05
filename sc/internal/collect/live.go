package collect

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"sqlmonitor/internal/domain"
)

// The live checks, ACT01 (blocking now) and PRS01 (pressure now), read in one place
// (docs/phase5i-contract.md, section 3): the Performance card's batch and the load strip's minute
// read both run liveFragment, select liveColumns and liveResultSets, and scan them with liveRow and
// scanLiveSets, so the strip and the card read the same facts the same way.

// liveFragment reads the head blockers across the instance (ACT01), the worker threads and
// schedulers and the memory grants (PRS01; the grant timeouts of PRS02 come with them). It needs
// @vss (VIEW SERVER STATE held) and @noVss (the sentence when not) declared before it; each area is
// read in its own TRY. SELECTs only, into table variables.
//
// Source: DarlingData sp_PressureDetector/sp_PressureDetector.sql (MIT): the scheduler, worker
// thread and memory grant reads of PRS01.
const liveFragment = `/* ACT01 Head blockers right now, across the instance; a blocker blocked only by itself (its own parallel threads,
   blocking_session_id = session_id) is still a head blocker */
DECLARE @blockers TABLE (session_id int NOT NULL, blocked int NOT NULL, longest_sec bigint NOT NULL, database_name nvarchar(128) NULL,
                         login_name nvarchar(128) NULL, host_name nvarchar(128) NULL, program_name nvarchar(128) NULL,
                         status nvarchar(30) NULL, open_tran int NULL, last_stmt nvarchar(4000) NULL, self_blocked bit NOT NULL);
DECLARE @blockingError nvarchar(2048);
IF @vss = 0 SET @blockingError = @noVss;
ELSE
BEGIN TRY
    INSERT @blockers (session_id, blocked, longest_sec, database_name, login_name, host_name, program_name, status, open_tran, last_stmt, self_blocked)
    SELECT s.session_id, b.Blocked, b.LongestSec, b.DbName, s.login_name, s.host_name, s.program_name, s.status, s.open_transaction_count,
           LEFT(q.text, 4000),
           CASE WHEN EXISTS (SELECT 1 FROM sys.dm_exec_requests r3 WHERE r3.session_id = b.Blocker AND r3.blocking_session_id = r3.session_id)
                THEN 1 ELSE 0 END
    FROM (SELECT r.blocking_session_id AS Blocker, COUNT(*) AS Blocked, MAX(r.wait_time) / 1000 AS LongestSec,
                 MAX(DB_NAME(r.database_id)) AS DbName
          FROM sys.dm_exec_requests r
          WHERE r.blocking_session_id > 0 AND r.blocking_session_id <> r.session_id
          GROUP BY r.blocking_session_id) b
    JOIN sys.dm_exec_sessions s ON s.session_id = b.Blocker
    OUTER APPLY (SELECT TOP (1) t.text FROM sys.dm_exec_connections c CROSS APPLY sys.dm_exec_sql_text(c.most_recent_sql_handle) t
                 WHERE c.session_id = s.session_id) q
    WHERE NOT EXISTS (SELECT 1 FROM sys.dm_exec_requests r2
                      WHERE r2.session_id = b.Blocker AND r2.blocking_session_id > 0 AND r2.blocking_session_id <> r2.session_id);
END TRY
BEGIN CATCH
    SET @blockingError = ERROR_MESSAGE();
END CATCH;

/* PRS01 Worker threads and schedulers right now (sp_PressureDetector's reads) */
DECLARE @maxWorkers int, @curWorkers int, @online int, @runnable int, @busy int, @maxRunnable int, @offline int,
        @workQueue bigint, @affinity nvarchar(60), @schedError nvarchar(2048);
IF @vss = 0 SET @schedError = @noVss;
ELSE
BEGIN TRY
    SELECT @maxWorkers  = MAX(osi.max_workers_count),
           @curWorkers  = SUM(CASE WHEN dos.status = N'VISIBLE ONLINE' THEN dos.current_workers_count ELSE 0 END),
           @online      = SUM(CASE WHEN dos.status = N'VISIBLE ONLINE' THEN 1 ELSE 0 END),
           @runnable    = SUM(CASE WHEN dos.status = N'VISIBLE ONLINE' THEN dos.runnable_tasks_count ELSE 0 END),
           @busy        = SUM(CASE WHEN dos.status = N'VISIBLE ONLINE' AND dos.runnable_tasks_count > 0 THEN 1 ELSE 0 END),
           @maxRunnable = MAX(CASE WHEN dos.status = N'VISIBLE ONLINE' THEN dos.runnable_tasks_count ELSE 0 END),
           @offline     = SUM(CASE WHEN dos.status = N'VISIBLE OFFLINE' THEN 1 ELSE 0 END),
           @workQueue   = SUM(CASE WHEN dos.status = N'VISIBLE ONLINE' THEN CONVERT(bigint, dos.work_queue_count) ELSE 0 END),
           @affinity    = MAX(CONVERT(nvarchar(60), osi.affinity_type_desc))
    FROM sys.dm_os_schedulers AS dos
    CROSS JOIN sys.dm_os_sys_info AS osi;
END TRY
BEGIN CATCH
    SET @schedError = ERROR_MESSAGE();
END CATCH;

/* PRS01 Memory grants: waiting now, and forced since the restart; PRS02 grant timeouts since the restart (error 8645);
   the three largest grants held right now, read only when queries wait for memory (sp_PressureDetector's read) */
DECLARE @gWaiting bigint, @gGranted bigint, @gForced bigint, @gTimeouts bigint, @grantsError nvarchar(2048);
DECLARE @holders TABLE (session_id int NOT NULL, granted_kb bigint NULL, requested_kb bigint NULL, wait_ms bigint NULL, stmt nvarchar(200) NULL);
IF @vss = 0 SET @grantsError = @noVss;
ELSE
BEGIN TRY
    SELECT @gWaiting = SUM(CONVERT(bigint, waiter_count)), @gGranted = SUM(CONVERT(bigint, grantee_count)),
           @gForced = SUM(CONVERT(bigint, forced_grant_count))
    FROM sys.dm_exec_query_resource_semaphores
    WHERE max_target_memory_kb IS NOT NULL;

    SELECT @gTimeouts = SUM(CONVERT(bigint, ISNULL(timeout_error_count, 0)))
    FROM sys.dm_exec_query_resource_semaphores;

    IF @gWaiting > 0
        INSERT @holders (session_id, granted_kb, requested_kb, wait_ms, stmt)
        SELECT TOP (3) mg.session_id, mg.granted_memory_kb, mg.requested_memory_kb, mg.wait_time_ms,
               LEFT(SUBSTRING(t.text, ISNULL(r.statement_start_offset, 0) / 2 + 1, 200), 200)
        FROM sys.dm_exec_query_memory_grants AS mg
        LEFT JOIN sys.dm_exec_requests AS r ON r.session_id = mg.session_id AND r.request_id = mg.request_id
        OUTER APPLY sys.dm_exec_sql_text(mg.sql_handle) AS t
        WHERE mg.granted_memory_kb IS NOT NULL AND mg.session_id <> @@SPID
        ORDER BY mg.granted_memory_kb DESC, mg.session_id;
END TRY
BEGIN CATCH
    SET @grantsError = ERROR_MESSAGE();
END CATCH;
`

// liveColumns are the live areas' values, selected at the end of the batch's one-row result set.
const liveColumns = `@blockingError,
       @maxWorkers, @curWorkers, @online, @runnable, @busy, @maxRunnable, @offline, @workQueue, @affinity, @schedError,
       @gWaiting, @gGranted, @gForced, ISNULL(@gTimeouts, 0), @grantsError`

// liveResultSets are the head blockers and the largest memory grants held now, the batch's last two
// result sets.
const liveResultSets = `SELECT session_id, blocked, longest_sec, database_name, login_name, host_name, program_name, status, open_tran, last_stmt, self_blocked
FROM @blockers ORDER BY longest_sec DESC, session_id;
SELECT session_id, granted_kb, requested_kb, wait_ms, stmt FROM @holders ORDER BY granted_kb DESC, session_id;`

// liveRow scans liveColumns.
type liveRow struct {
	blockingErr, affinity, schedErr, grantsErr      sql.NullString
	maxW, curW, online, runnable, busy, maxRun, off sql.NullInt32
	workQueue, gWaiting, gGranted, gForced          sql.NullInt64
	gTimeouts                                       int64
}

// dest are the scan targets of liveColumns, in order.
func (l *liveRow) dest() []any {
	return []any{&l.blockingErr,
		&l.maxW, &l.curW, &l.online, &l.runnable, &l.busy, &l.maxRun, &l.off, &l.workQueue, &l.affinity, &l.schedErr,
		&l.gWaiting, &l.gGranted, &l.gForced, &l.gTimeouts, &l.grantsErr}
}

// apply puts the live values into the facts: an area that could not be read carries its reason.
func (l *liveRow) apply(f *domain.PerformanceFacts) {
	f.BlockingError = l.blockingErr.String
	f.SchedulersError = l.schedErr.String
	if f.SchedulersError == "" {
		f.Schedulers = &domain.SchedulerLoad{MaxWorkers: int(l.maxW.Int32), CurrentWorkers: int(l.curW.Int32), OnlineSchedulers: int(l.online.Int32),
			RunnableTasks: int(l.runnable.Int32), BusySchedulers: int(l.busy.Int32), MaxRunnable: int(l.maxRun.Int32),
			OfflineSchedulers: int(l.off.Int32), WorkQueue: l.workQueue.Int64, AffinityManual: strings.EqualFold(l.affinity.String, "MANUAL")}
	}
	f.GrantsError = l.grantsErr.String
	if f.GrantsError == "" {
		f.Grants = &domain.MemoryGrants{Waiting: l.gWaiting.Int64, Granted: l.gGranted.Int64, Forced: l.gForced.Int64, Timeouts: l.gTimeouts,
			Holders: []domain.GrantHolder{}}
	}
}

// scanLiveSets reads liveResultSets: it moves to the head blockers' result set, then to the memory
// grant holders'.
func scanLiveSets(rows *sql.Rows, f *domain.PerformanceFacts) error {
	if err := nextResultSet(rows, "blockers"); err != nil {
		return err
	}
	f.Blockers = []domain.Blocker{}
	for rows.Next() {
		var x domain.Blocker
		var dbName, login, host, program, status, last sql.NullString
		var openTran sql.NullInt32
		if err := rows.Scan(&x.Session, &x.Blocked, &x.LongestSec, &dbName, &login, &host, &program, &status, &openTran, &last, &x.SelfBlocked); err != nil {
			return err
		}
		x.Database, x.Login, x.Host, x.Program, x.Status = dbName.String, login.String, host.String, program.String, status.String
		x.OpenTran, x.LastStatement = int(openTran.Int32), last.String
		f.Blockers = append(f.Blockers, x)
	}

	// The largest memory grants held right now, when queries wait for memory.
	if err := nextResultSet(rows, "memory grant holders"); err != nil {
		return err
	}
	for rows.Next() {
		var x domain.GrantHolder
		var granted, requested, wait sql.NullInt64
		var stmt sql.NullString
		if err := rows.Scan(&x.Session, &granted, &requested, &wait, &stmt); err != nil {
			return err
		}
		x.GrantedKB, x.RequestedKB, x.WaitMs, x.Statement = granted.Int64, requested.Int64, wait.Int64, stmt.String
		if f.Grants != nil {
			f.Grants.Holders = append(f.Grants.Holders, x)
		}
	}
	return nil
}

// liveQuery is the load strip's minute read of the live checks: the database's name, state and READ
// COMMITTED SNAPSHOT setting (ACT01's Do this), then liveFragment.
var liveQuery = Tag(safeSession + `SET NOCOUNT ON;
DECLARE @now datetime = GETDATE();
DECLARE @found bit = 0, @state nvarchar(60), @rcsi bit = 0;
SELECT @found = 1, @state = d.state_desc, @rcsi = d.is_read_committed_snapshot_on
FROM sys.databases AS d WHERE d.name = @db;
DECLARE @vss bit = CASE WHEN HAS_PERMS_BY_NAME(NULL, NULL, 'VIEW SERVER STATE') = 1 THEN 1 ELSE 0 END;
DECLARE @noVss nvarchar(100) = N'the login does not have VIEW SERVER STATE.';

` + liveFragment + `
SELECT @found, CAST(ISNULL(@@SERVERNAME, N'') AS nvarchar(256)), @state, @rcsi, @now, DATEPART(TZOFFSET, SYSDATETIMEOFFSET()),
       ` + liveColumns + `;
` + liveResultSets)

// LiveFacts reads the live checks of one database (ACT01 and PRS01) with the Performance card's own
// fragment: the facts carry the head blockers, the schedulers and the memory grants, each area with
// its reason when it could not be read. The error is for a read that could not run, or a database
// that does not exist.
func LiveFacts(ctx context.Context, db *sql.DB, database string) (domain.PerformanceFacts, error) {
	f := domain.PerformanceFacts{Database: database}
	rows, err := db.QueryContext(ctx, liveQuery, sql.Named("db", database))
	if err != nil {
		return f, friendlyError(err)
	}
	defer rows.Close()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return f, friendlyError(err)
		}
		return f, errors.New("SQL Server returned no live facts")
	}
	var found, rcsi bool
	var state sql.NullString
	var live liveRow
	if err := rows.Scan(append([]any{&found, &f.ServerName, &state, &rcsi, &f.ServerNow, &f.UTCOffsetMinutes}, live.dest()...)...); err != nil {
		return f, fmt.Errorf("read the live facts of %s: %w", database, err)
	}
	if !found {
		return f, fmt.Errorf("there is no database named %q", database)
	}
	f.State, f.RCSI, f.ServerNow = state.String, rcsi, naive(f.ServerNow)
	live.apply(&f)
	if err := scanLiveSets(rows, &f); err != nil {
		return f, err
	}
	return f, friendlyErrOrNil(rows.Err())
}
