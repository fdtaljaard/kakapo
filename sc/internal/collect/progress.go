package collect

import (
	"context"
	"database/sql"
	"strings"
	"time"

	"sqlmonitor/internal/domain"
)

// The progress read for long fixes (docs/phase4e-contract.md, part 2): the long operations running
// now on the instance, which the fix dialog polls while a CHECKDB, an index build or a statistics
// update applies. One tagged SELECT on the DMVs; nothing is written.

// ProgressTimeout bounds one read of the operations in progress: the dialog polls it every 2
// seconds, so a read that cannot answer in time is skipped rather than queued.
const ProgressTimeout = 2 * time.Second

// ProgressCommands are the sys.dm_exec_requests commands of the long operations a fix runs (or that
// compete with one): integrity checks, backups and restores, index builds, statistics updates,
// shrinks and database changes. A request with a percent_complete is read whatever its command.
var ProgressCommands = []string{
	"DBCC", "DBCC TABLE CHECK", "DBCC CHECKDB",
	"BACKUP DATABASE", "BACKUP LOG", "RESTORE DATABASE", "RESTORE LOG",
	"CREATE INDEX", "ALTER INDEX", "DROP INDEX", "UPDATE STATISTICS",
	"DbccFilesCompact", "DbccSpaceReclaim", "ALTER DATABASE",
}

// Progress is one long operation in progress. Percent is 0 when SQL Server reports none (index
// builds and statistics updates); RemainingSeconds is 0 when it gives no estimate. Mine is true when
// the session is kakapo's own (its program name is ConnString's app name).
type Progress struct {
	SessionID        int       `json:"sessionId"`
	Command          string    `json:"command"`
	Database         string    `json:"database"`
	Percent          float64   `json:"percent"`
	ElapsedSeconds   int64     `json:"elapsedSeconds"`
	RemainingSeconds int64     `json:"remainingSeconds"`
	StartedAt        time.Time `json:"startedAt"`
	WaitType         string    `json:"waitType"`
	Program          string    `json:"program"`
	Mine             bool      `json:"mine"`
}

// progressSession is the read's session: a 2-second lock timeout, so a read that has to wait gives
// way, and a low deadlock priority.
const progressSession = "SET LOCK_TIMEOUT 2000; SET DEADLOCK_PRIORITY LOW; "

// progressQuery reads the requests running one of ProgressCommands, or reporting a percentage, in
// the database @db, in tempdb, in no database, or on one of kakapo's own sessions (a fix runs on a
// connection whose database is master, so its own requests are kept wherever they run). The start
// time comes back with the server's UTC offset.
var progressQuery = Tag(progressSession + `SET NOCOUNT ON;
SELECT TOP (50) r.session_id, r.command, DB_NAME(r.database_id), CONVERT(float, r.percent_complete),
       CONVERT(bigint, r.total_elapsed_time) / 1000, CONVERT(bigint, r.estimated_completion_time) / 1000,
       TODATETIMEOFFSET(r.start_time, DATEPART(TZOFFSET, SYSDATETIMEOFFSET())), r.wait_type, s.program_name
FROM sys.dm_exec_requests AS r
JOIN sys.dm_exec_sessions AS s ON s.session_id = r.session_id
WHERE r.session_id <> @@SPID
  AND (r.command IN (` + progressCommandList() + `) OR r.percent_complete > 0)
  AND (r.database_id IS NULL OR r.database_id = 2 OR r.database_id = DB_ID(@db) OR s.program_name = @app)
ORDER BY r.start_time, r.session_id;`)

// progressCommandList writes ProgressCommands as quoted Unicode literals for the IN list.
func progressCommandList() string {
	quoted := make([]string, len(ProgressCommands))
	for i, c := range ProgressCommands {
		quoted[i] = "N'" + strings.ReplaceAll(c, "'", "''") + "'"
	}
	return strings.Join(quoted, ", ")
}

// ReadProgress reads the long operations in progress for one database's page, within
// ProgressTimeout. It returns an empty slice, never nil, when nothing runs.
func ReadProgress(ctx context.Context, db *sql.DB, database string) ([]Progress, error) {
	ctx, cancel := context.WithTimeout(ctx, ProgressTimeout)
	defer cancel()
	rows, err := db.QueryContext(ctx, progressQuery, sql.Named("db", database), sql.Named("app", domain.BlockingAppName))
	if err != nil {
		return nil, friendlyError(err)
	}
	defer rows.Close()
	out := []Progress{}
	for rows.Next() {
		var p Progress
		var dbName, waitType, program sql.NullString
		var started sql.NullTime
		if err := rows.Scan(&p.SessionID, &p.Command, &dbName, &p.Percent, &p.ElapsedSeconds, &p.RemainingSeconds,
			&started, &waitType, &program); err != nil {
			return nil, err
		}
		out = append(out, progressRow(p, dbName.String, waitType.String, program.String, started))
	}
	if err := rows.Err(); err != nil {
		return nil, friendlyError(err)
	}
	return out, nil
}

// progressRow finishes one row: the command trimmed (SQL Server pads it), the start in UTC, no
// negative estimate, and whether the session is kakapo's.
func progressRow(p Progress, database, waitType, program string, started sql.NullTime) Progress {
	p.Command, p.Database, p.WaitType, p.Program = strings.TrimSpace(p.Command), database, waitType, program
	if started.Valid {
		p.StartedAt = started.Time.UTC()
	}
	if p.Percent < 0 {
		p.Percent = 0
	}
	if p.RemainingSeconds < 0 {
		p.RemainingSeconds = 0
	}
	p.Mine = domain.IsKakapoProgram(program)
	return p
}
