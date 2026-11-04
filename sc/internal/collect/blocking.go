package collect

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/golang-sql/sqlexp"

	"sqlmonitor/internal/domain"
)

// The blocking page's reads (docs/phase4c-contract.md, part 2): the chain now across the instance, the
// one session a kill-session preview reads again, the session after the KILL, and the database's READ
// COMMITTED SNAPSHOT setting. SELECTs only (and KILL ... WITH STATUSONLY, which reports and changes
// nothing); every area in its own TRY with an error column; every batch tagged.
//
// The classification follows DarlingData sp_HumanEventsBlockViewer.sql (MIT): the session status
// (3837-3854), the transaction name implicit_transaction (4029-4046), the isolation level (3753-3812)
// and the wait resource LIKE '%COMPILE%' (3963-3980) are read here and judged in domain/blocking.go;
// the SQL is our own, on the DMVs instead of the blocked process report.

// blkSessionColumns is the SELECT list of one session in a chain, over the aliases of blkSessionFrom:
// who, state, database, the statement running now (or the last batch it sent when nothing runs), the
// wait, the isolation level, the oldest transaction and the log it has written.
const blkSessionColumns = `s.session_id,
           CASE WHEN r.blocking_session_id > 0 AND r.blocking_session_id <> r.session_id THEN r.blocking_session_id ELSE 0 END,
           s.login_time, s.login_name, s.host_name, s.program_name, s.status, s.is_user_process, r.command,
           DB_NAME(COALESCE(r.database_id, s.database_id)),
           LEFT(COALESCE(cur.stmt, lst.text), 4000),
           CASE WHEN cur.stmt IS NOT NULL THEN OBJECT_SCHEMA_NAME(cur.objectid, cur.dbid) + N'.' + OBJECT_NAME(cur.objectid, cur.dbid)
                ELSE OBJECT_SCHEMA_NAME(lst.objectid, lst.dbid) + N'.' + OBJECT_NAME(lst.objectid, lst.dbid) END,
           CASE WHEN cur.stmt IS NULL THEN 1 ELSE 0 END,
           r.wait_type, r.wait_time, r.wait_resource, r.start_time, s.transaction_isolation_level, s.open_transaction_count,
           tx.began, tx.name, lb.log_bytes, r.percent_complete, r.estimated_completion_time`

// blkSessionFrom joins a session to its request, its statements and its transactions.
const blkSessionFrom = `FROM sys.dm_exec_sessions AS s
    OUTER APPLY (SELECT TOP (1) r0.session_id, r0.blocking_session_id, r0.command, r0.database_id, r0.sql_handle,
                        r0.statement_start_offset, r0.statement_end_offset, r0.wait_type, r0.wait_time, r0.wait_resource,
                        r0.start_time, r0.percent_complete, r0.estimated_completion_time
                 FROM sys.dm_exec_requests AS r0 WHERE r0.session_id = s.session_id
                 ORDER BY r0.blocking_session_id DESC, r0.request_id) AS r
    OUTER APPLY (SELECT t.dbid, t.objectid,
                        SUBSTRING(t.text, r.statement_start_offset / 2 + 1,
                                  (CASE WHEN r.statement_end_offset = -1 THEN DATALENGTH(t.text) ELSE r.statement_end_offset END
                                   - r.statement_start_offset) / 2 + 1) AS stmt
                 FROM sys.dm_exec_sql_text(r.sql_handle) AS t) AS cur
    OUTER APPLY (SELECT TOP (1) t.text, t.dbid, t.objectid
                 FROM sys.dm_exec_connections AS c CROSS APPLY sys.dm_exec_sql_text(c.most_recent_sql_handle) AS t
                 WHERE c.session_id = s.session_id) AS lst
    OUTER APPLY (SELECT TOP (1) at.transaction_begin_time AS began, at.name
                 FROM sys.dm_tran_session_transactions AS st
                 JOIN sys.dm_tran_active_transactions AS at ON at.transaction_id = st.transaction_id
                 WHERE st.session_id = s.session_id
                 ORDER BY at.transaction_begin_time) AS tx
    OUTER APPLY (SELECT SUM(dt.database_transaction_log_bytes_used + dt.database_transaction_log_bytes_used_system) AS log_bytes
                 FROM sys.dm_tran_session_transactions AS st2
                 JOIN sys.dm_tran_database_transactions AS dt ON dt.transaction_id = st2.transaction_id
                 WHERE st2.session_id = s.session_id) AS lb`

// blkSessionTable is the shape blkSessionColumns fills.
const blkSessionTable = `(session_id int NOT NULL PRIMARY KEY, blocked_by int NOT NULL, login_time datetime NULL,
    login_name nvarchar(128) NULL, host_name nvarchar(128) NULL, program_name nvarchar(128) NULL, status nvarchar(30) NULL,
    is_user bit NOT NULL, command nvarchar(32) NULL, database_name nvarchar(128) NULL, stmt nvarchar(4000) NULL,
    object_name nvarchar(300) NULL, stmt_is_last bit NOT NULL, wait_type nvarchar(60) NULL, wait_ms bigint NULL,
    wait_resource nvarchar(256) NULL, request_start datetime NULL, isolation int NULL, open_tran int NULL,
    tran_begin datetime NULL, tran_name nvarchar(32) NULL, log_bytes bigint NULL, percent_complete real NULL,
    remaining_ms bigint NULL, job_name nvarchar(128) NULL)`

const blkSessionInsertColumns = `(session_id, blocked_by, login_time, login_name, host_name, program_name, status, is_user, command,
        database_name, stmt, object_name, stmt_is_last, wait_type, wait_ms, wait_resource, request_start, isolation, open_tran,
        tran_begin, tran_name, log_bytes, percent_complete, remaining_ms)`

const blkSessionSelectColumns = `session_id, blocked_by, login_time, login_name, host_name, program_name, job_name, status, is_user, command,
       database_name, stmt, object_name, stmt_is_last, wait_type, wait_ms, wait_resource, request_start, isolation, open_tran,
       tran_begin, tran_name, log_bytes, percent_complete`

// blkJobName decodes the job id from a job step's program name ("SQLAgent - TSQL JobStep (Job
// 0x9A3F...: Step 2)") and names the job from msdb; table is the session table to fill.
func blkJobName(table string) string {
	return `UPDATE ` + table + ` SET job_name = j.name
    FROM ` + table + `
    JOIN (SELECT x.session_id,
                 TRY_CONVERT(varbinary(16), SUBSTRING(x.program_name, CHARINDEX(N'(Job 0x', x.program_name) + 5, 34), 1) AS job_bin
          FROM ` + table + ` AS x WHERE x.program_name LIKE N'SQLAgent - TSQL JobStep (Job 0x%') AS k ON k.session_id = ` + table + `.session_id
    JOIN msdb.dbo.sysjobs AS j ON j.job_id = CONVERT(uniqueidentifier, k.job_bin);`
}

// blockingQuery reads every session that blocks or is blocked across the instance, the lock each
// blocked one waits for with the mode its blocker holds, and the tables of those locks named in their
// own database. Result sets: one row of facts, the sessions, the lock waits.
var blockingQuery = Tag(safeSession + `SET NOCOUNT ON;
DECLARE @found bit = 0, @rcsi bit = 0;
SELECT @found = 1, @rcsi = d.is_read_committed_snapshot_on FROM sys.databases AS d WHERE d.name = @db;
DECLARE @vss bit = CASE WHEN HAS_PERMS_BY_NAME(NULL, NULL, 'VIEW SERVER STATE') = 1 THEN 1 ELSE 0 END;
DECLARE @noVss nvarchar(100) = N'the login does not have VIEW SERVER STATE.';

/* The sessions in a chain: every request blocked by another session, and every session that blocks one.
   A request blocked only by its own parallel threads (blocking_session_id = session_id) is left out. */
CREATE TABLE #blk_sessions ` + blkSessionTable + `;
DECLARE @sessionsError nvarchar(2048);
IF @vss = 0 SET @sessionsError = @noVss;
ELSE
BEGIN TRY
    INSERT #blk_sessions ` + blkSessionInsertColumns + `
    SELECT TOP (500) ` + blkSessionColumns + `
    ` + blkSessionFrom + `
    WHERE s.session_id IN (SELECT b.session_id FROM sys.dm_exec_requests AS b
                           WHERE b.blocking_session_id > 0 AND b.blocking_session_id <> b.session_id
                           UNION ALL
                           SELECT b.blocking_session_id FROM sys.dm_exec_requests AS b
                           WHERE b.blocking_session_id > 0 AND b.blocking_session_id <> b.session_id)
    ORDER BY s.session_id;
END TRY
BEGIN CATCH
    SET @sessionsError = ERROR_MESSAGE();
END CATCH;

/* SQL Agent job names, from msdb when the login may read it */
DECLARE @jobsError nvarchar(2048);
IF EXISTS (SELECT 1 FROM #blk_sessions WHERE program_name LIKE N'SQLAgent - TSQL JobStep (Job 0x%')
BEGIN
    BEGIN TRY
        ` + blkJobName("#blk_sessions") + `
    END TRY
    BEGIN CATCH
        SET @jobsError = ERROR_MESSAGE();
    END CATCH;
END;

/* The lock each blocked session waits for, and the mode its blocker holds on the same resource */
CREATE TABLE #blk_waits (session_id int NOT NULL, resource_type nvarchar(60) NOT NULL, db_id int NOT NULL, entity bigint NULL,
                         wait_mode nvarchar(60) NULL, held_mode nvarchar(60) NULL, object_id int NULL, index_name nvarchar(128) NULL);
DECLARE @locksError nvarchar(2048), @namesError nvarchar(2048);
IF @vss = 0 SET @locksError = @noVss;
ELSE
BEGIN TRY
    INSERT #blk_waits (session_id, resource_type, db_id, entity, wait_mode, held_mode)
    SELECT TOP (500) w.request_session_id, w.resource_type, w.resource_database_id, w.resource_associated_entity_id, w.request_mode,
           (SELECT TOP (1) h.request_mode FROM sys.dm_tran_locks AS h
            WHERE h.request_session_id = bs.blocked_by AND h.request_status = N'GRANT'
              AND h.resource_type = w.resource_type AND h.resource_database_id = w.resource_database_id
              AND h.resource_associated_entity_id = w.resource_associated_entity_id
              AND h.resource_description = w.resource_description)
    FROM sys.dm_tran_locks AS w
    JOIN #blk_sessions AS bs ON bs.session_id = w.request_session_id AND bs.blocked_by > 0
    WHERE w.request_status = N'WAIT';

    UPDATE #blk_waits SET object_id = CONVERT(int, entity) WHERE resource_type = N'OBJECT';

    /* KEY, PAGE, RID and HOBT locks name a hobt: its table and index live in the lock's own database */
    DECLARE @d int, @proc nvarchar(300);
    DECLARE @names nvarchar(max) = N'UPDATE #blk_waits SET object_id = p.object_id, index_name = i.name
        FROM #blk_waits JOIN sys.partitions AS p ON p.hobt_id = #blk_waits.entity
        LEFT JOIN sys.indexes AS i ON i.object_id = p.object_id AND i.index_id = p.index_id
        WHERE #blk_waits.db_id = @d AND #blk_waits.resource_type IN (N''KEY'', N''PAGE'', N''RID'', N''HOBT'');';
    DECLARE blk_dbs CURSOR LOCAL FAST_FORWARD FOR
        SELECT DISTINCT w.db_id FROM #blk_waits AS w
        WHERE w.resource_type IN (N'KEY', N'PAGE', N'RID', N'HOBT') AND DB_NAME(w.db_id) IS NOT NULL;
    OPEN blk_dbs;
    FETCH NEXT FROM blk_dbs INTO @d;
    WHILE @@FETCH_STATUS = 0
    BEGIN
        BEGIN TRY
            SET @proc = QUOTENAME(DB_NAME(@d)) + N'.sys.sp_executesql';
            EXEC @proc @names, N'@d int', @d = @d;
        END TRY
        BEGIN CATCH
            SET @namesError = ISNULL(@namesError + N' ', N'') + DB_NAME(@d) + N': ' + ERROR_MESSAGE();
        END CATCH;
        FETCH NEXT FROM blk_dbs INTO @d;
    END;
    CLOSE blk_dbs;
    DEALLOCATE blk_dbs;
END TRY
BEGIN CATCH
    SET @locksError = ERROR_MESSAGE();
END CATCH;

SELECT @found, @rcsi, @@SPID, CAST(@@SERVERNAME AS nvarchar(256)), CAST(SERVERPROPERTY('ProductVersion') AS nvarchar(64)), GETDATE(),
       @sessionsError, @jobsError, @locksError, @namesError;

SELECT ` + blkSessionSelectColumns + `
FROM #blk_sessions ORDER BY session_id;

SELECT w.session_id, w.resource_type, DB_NAME(w.db_id),
       OBJECT_SCHEMA_NAME(w.object_id, w.db_id) + N'.' + OBJECT_NAME(w.object_id, w.db_id), w.index_name, w.wait_mode, w.held_mode
FROM #blk_waits AS w ORDER BY w.session_id;

DROP TABLE #blk_waits;
DROP TABLE #blk_sessions;`)

// ReadBlocking reads the blocking chain now, across the instance, for the blocking page of database.
func ReadBlocking(ctx context.Context, db *sql.DB, database string) (domain.BlockingFacts, error) {
	f := domain.BlockingFacts{Database: database, Sessions: []domain.BlockingSession{}, Waits: []domain.LockWait{}}
	rows, err := db.QueryContext(ctx, blockingQuery, sql.Named("db", database))
	if err != nil {
		return f, friendlyError(err)
	}
	defer rows.Close()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return f, friendlyError(err)
		}
		return f, errors.New("SQL Server returned no blocking facts")
	}
	var sessErr, jobsErr, locksErr, namesErr, server, version sql.NullString
	if err := rows.Scan(&f.Found, &f.RCSI, &f.OwnSession, &server, &version, &f.ServerNow, &sessErr, &jobsErr, &locksErr, &namesErr); err != nil {
		return f, fmt.Errorf("read the blocking facts: %w", err)
	}
	f.ServerNow = naive(f.ServerNow)
	f.ServerName, f.ProductVersion = server.String, version.String
	f.SessionsError, f.JobsError, f.LocksError, f.NamesError = sessErr.String, jobsErr.String, locksErr.String, namesErr.String

	if err := nextResultSet(rows, "blocking sessions"); err != nil {
		return f, err
	}
	for rows.Next() {
		s, err := scanBlockingSession(rows)
		if err != nil {
			return f, err
		}
		f.Sessions = append(f.Sessions, s)
	}

	if err := nextResultSet(rows, "lock waits"); err != nil {
		return f, err
	}
	for rows.Next() {
		var w domain.LockWait
		var dbName, object, index, wantMode, heldMode sql.NullString
		if err := rows.Scan(&w.Session, &w.ResourceType, &dbName, &object, &index, &wantMode, &heldMode); err != nil {
			return f, err
		}
		w.Database, w.Object, w.Index, w.WaitMode, w.HeldMode = dbName.String, object.String, index.String, wantMode.String, heldMode.String
		f.Waits = append(f.Waits, w)
	}
	return f, friendlyErrOrNil(rows.Err())
}

// scanBlockingSession reads one row of blkSessionSelectColumns.
func scanBlockingSession(rows *sql.Rows) (domain.BlockingSession, error) {
	var s domain.BlockingSession
	var login, host, program, job, status, command, dbName, stmt, object, waitType, waitRes, tranName sql.NullString
	var loginTime, reqStart, tranBegin sql.NullTime
	var waitMs, logBytes sql.NullInt64
	var isolation, openTran sql.NullInt32
	var percent sql.NullFloat64
	if err := rows.Scan(&s.Session, &s.BlockedBy, &loginTime, &login, &host, &program, &job, &status, &s.IsUser, &command,
		&dbName, &stmt, &object, &s.StatementIsLast, &waitType, &waitMs, &waitRes, &reqStart, &isolation, &openTran,
		&tranBegin, &tranName, &logBytes, &percent); err != nil {
		return s, fmt.Errorf("read a blocking session: %w", err)
	}
	s.Login, s.Host, s.Program, s.JobName, s.Status = login.String, host.String, program.String, job.String, status.String
	s.Command, s.Database, s.Statement, s.ObjectName = strings.TrimSpace(command.String), dbName.String, stmt.String, object.String
	s.WaitType, s.WaitMs, s.WaitResource = waitType.String, waitMs.Int64, waitRes.String
	s.IsolationLevel, s.OpenTran, s.TranName, s.LogBytes = int(isolation.Int32), int(openTran.Int32), tranName.String, logBytes.Int64
	s.PercentComplete = percent.Float64
	if loginTime.Valid {
		s.LoginTime = naive(loginTime.Time)
	}
	if reqStart.Valid {
		s.RequestStarted = naive(reqStart.Time)
	}
	if tranBegin.Valid {
		s.TranStarted = naive(tranBegin.Time)
	}
	return s, nil
}

// killTargetQuery reads one session again for the kill-session preview, with the requests waiting on
// it now and its SQL Agent job's name. Result sets: one row of facts, then the session (none when it
// is gone).
var killTargetQuery = Tag(safeSession + `SET NOCOUNT ON;
DECLARE @vss bit = CASE WHEN HAS_PERMS_BY_NAME(NULL, NULL, 'VIEW SERVER STATE') = 1 THEN 1 ELSE 0 END;
CREATE TABLE #blk_target ` + blkSessionTable + `;
DECLARE @sessionError nvarchar(2048), @jobsError nvarchar(2048), @behind int = 0;
BEGIN TRY
    INSERT #blk_target ` + blkSessionInsertColumns + `
    SELECT TOP (1) ` + blkSessionColumns + `
    ` + blkSessionFrom + `
    WHERE s.session_id = @session;
    SELECT @behind = COUNT(*) FROM sys.dm_exec_requests AS b WHERE b.blocking_session_id = @session AND b.session_id <> @session;
END TRY
BEGIN CATCH
    SET @sessionError = ERROR_MESSAGE();
END CATCH;

IF EXISTS (SELECT 1 FROM #blk_target WHERE program_name LIKE N'SQLAgent - TSQL JobStep (Job 0x%')
BEGIN
    BEGIN TRY
        ` + blkJobName("#blk_target") + `
    END TRY
    BEGIN CATCH
        SET @jobsError = ERROR_MESSAGE();
    END CATCH;
END;

SELECT @@SPID, CAST(@@SERVERNAME AS nvarchar(256)), CAST(SERVERPROPERTY('ProductVersion') AS nvarchar(64)), GETDATE(),
       @behind, @vss, @sessionError, @jobsError;
SELECT ` + blkSessionSelectColumns + `
FROM #blk_target;
DROP TABLE #blk_target;`)

// ReadKillTarget reads one session again for the kill-session preview. A session that is not
// connected comes back with Found false. Without VIEW SERVER STATE only the login's own sessions
// are visible, so another session reads as gone: that is an error naming the permission.
func ReadKillTarget(ctx context.Context, db *sql.DB, session int) (domain.KillTarget, error) {
	var t domain.KillTarget
	rows, err := db.QueryContext(ctx, killTargetQuery, sql.Named("session", session))
	if err != nil {
		return t, friendlyError(err)
	}
	defer rows.Close()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return t, friendlyError(err)
		}
		return t, errors.New("SQL Server returned nothing for the session")
	}
	var server, version, sessErr, jobsErr sql.NullString
	var vss bool
	if err := rows.Scan(&t.OwnSession, &server, &version, &t.ServerNow, &t.Behind, &vss, &sessErr, &jobsErr); err != nil {
		return t, fmt.Errorf("read the session facts: %w", err)
	}
	t.ServerNow = naive(t.ServerNow)
	t.ServerName, t.ProductVersion, t.JobsError = server.String, version.String, jobsErr.String
	if sessErr.String != "" {
		return t, errors.New(sessErr.String)
	}
	if err := nextResultSet(rows, "session"); err != nil {
		return t, err
	}
	for rows.Next() {
		s, err := scanBlockingSession(rows)
		if err != nil {
			return t, err
		}
		t.Session, t.Found = s, true
	}
	if err := rows.Err(); err != nil {
		return t, friendlyError(err)
	}
	if !t.Found && !vss {
		return t, errors.New("the login does not have VIEW SERVER STATE, so kakapo cannot see the session")
	}
	return t, nil
}

// killStatusQuery reads a session after KILL: still there or not, its login time, and its rollback.
const killStatusQuery = safeSession + `SET NOCOUNT ON;
SELECT CAST(CASE WHEN s.session_id IS NULL THEN 0 ELSE 1 END AS bit), s.login_time, s.status, r.command,
       r.percent_complete, r.estimated_completion_time
FROM (SELECT 1 AS one) AS x
LEFT JOIN sys.dm_exec_sessions AS s ON s.session_id = @session
LEFT JOIN sys.dm_exec_requests AS r ON r.session_id = @session;`

// ReadKillStatus reads a session after KILL. When it is rolling back, KILL ... WITH STATUSONLY is
// asked how far it has come; its message is kept, and a failure to get it is not an error (the
// percentage comes from sys.dm_exec_requests as well).
func ReadKillStatus(ctx context.Context, db *sql.DB, session int) (domain.KillReadBack, error) {
	var r domain.KillReadBack
	var loginTime sql.NullTime
	var status, command sql.NullString
	var percent sql.NullFloat64
	var remaining sql.NullInt64
	if err := db.QueryRowContext(ctx, Tag(killStatusQuery), sql.Named("session", session)).Scan(&r.Present, &loginTime, &status, &command,
		&percent, &remaining); err != nil {
		return r, friendlyError(err)
	}
	if loginTime.Valid {
		r.LoginTime = naive(loginTime.Time)
	}
	r.Status, r.Command, r.PercentComplete, r.RemainingMs = status.String, strings.TrimSpace(command.String), percent.Float64, remaining.Int64
	if r.Present && strings.HasPrefix(strings.ToUpper(r.Command), "KILLED") {
		r.Message = killStatusOnly(ctx, db, session)
	}
	return r, nil
}

// killStatusOnly runs KILL n WITH STATUSONLY and returns the message SQL Server sends ("SPID 83:
// transaction rollback in progress. Estimated rollback completion: 40%. Estimated time remaining: 30
// seconds."), or "" when there is none.
func killStatusOnly(ctx context.Context, db *sql.DB, session int) string {
	msgs := &sqlexp.ReturnMessage{}
	rows, err := db.QueryContext(ctx, Tag(domain.KillStatusOnlyStatement(session)), msgs)
	if err != nil {
		return ""
	}
	defer rows.Close()
	text := []string{}
	for active := true; active; {
		switch m := msgs.Message(ctx).(type) {
		case sqlexp.MsgNotice:
			text = append(text, strings.TrimSpace(m.Message.String()))
		case sqlexp.MsgNext:
			for rows.Next() {
			}
		case sqlexp.MsgNextResultSet:
			active = rows.NextResultSet()
		case sqlexp.MsgError, nil:
			active = false
		}
		if ctx.Err() != nil {
			break
		}
	}
	return strings.Join(text, " ")
}

// rcsiQuery reads a database's READ COMMITTED SNAPSHOT setting and the sessions using it now.
const rcsiQuery = safeSession + `SET NOCOUNT ON;
DECLARE @users int = 0, @usersError nvarchar(2048);
BEGIN TRY
    SELECT @users = COUNT(*) FROM sys.dm_exec_sessions AS s
    WHERE s.database_id = DB_ID(@db) AND s.session_id <> @@SPID AND s.is_user_process = 1;
END TRY
BEGIN CATCH
    SET @usersError = ERROR_MESSAGE();
END CATCH;
SELECT d.is_read_committed_snapshot_on, @users, CAST(SERVERPROPERTY('ProductVersion') AS nvarchar(64))
FROM sys.databases AS d WHERE d.name = @db;`

// ReadRCSI reads the rcsi fix's facts: the setting, and how many other sessions use the database.
func ReadRCSI(ctx context.Context, db *sql.DB, database string) (domain.RCSIFacts, error) {
	f := domain.RCSIFacts{Database: database}
	var version sql.NullString
	err := db.QueryRowContext(ctx, Tag(rcsiQuery), sql.Named("db", database)).Scan(&f.RCSI, &f.Users, &version)
	if errors.Is(err, sql.ErrNoRows) {
		return f, fmt.Errorf("there is no database named %q", database)
	}
	if err != nil {
		return f, friendlyError(err)
	}
	f.ProductVersion = version.String
	return f, nil
}
