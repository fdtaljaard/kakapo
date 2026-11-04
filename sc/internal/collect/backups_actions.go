package collect

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	mssql "github.com/microsoft/go-mssqldb"

	"sqlmonitor/internal/domain"
)

// The reads of the Backups card's phase 4d fixes (docs/phase4d-contract.md, part 2). SELECTs only;
// the fix statements are built in domain and run through ExecFix, and the CHECKDB run through
// RunCheckdb.

// checkdbJobFactsQuery reads what the integrity check fixes need besides the backup facts: the
// database's data size (sys.master_files, for the PHYSICAL_ONLY default) and the Agent jobs that
// already check it (a step running DBCC CHECKDB naming it or every database, Ola Hallengren's
// DatabaseIntegrityCheck for it or for all, or kakapo's own job by name), each area in its own TRY
// with its error column. Express has no Agent, so no jobs are read there.
const checkdbJobFactsQuery = safeSession + `SET NOCOUNT ON;
DECLARE @dataMB bigint, @dataError nvarchar(2048), @jobsError nvarchar(2048);
DECLARE @jobs TABLE (name sysname NOT NULL, enabled bit NOT NULL);
BEGIN TRY
    SELECT @dataMB = SUM(CONVERT(bigint, mf.size)) * 8 / 1024
    FROM sys.master_files AS mf
    WHERE mf.database_id = DB_ID(@db) AND mf.type = 0;
    IF @dataMB IS NULL
        SET @dataError = N'sys.master_files shows no data files of this database to this login (it needs VIEW ANY DEFINITION).';
END TRY
BEGIN CATCH
    SET @dataError = ERROR_MESSAGE();
END CATCH;
IF CONVERT(int, SERVERPROPERTY('EngineEdition')) <> 4
BEGIN TRY
    INSERT @jobs (name, enabled)
    SELECT j.name, j.enabled
    FROM msdb.dbo.sysjobs AS j
    WHERE j.name = @job
       OR EXISTS (SELECT 1 FROM msdb.dbo.sysjobsteps AS s
                  WHERE s.job_id = j.job_id
                    AND ((s.command LIKE N'%CHECKDB%' AND (CHARINDEX(@db, s.command) > 0 OR s.command LIKE N'%sp_MSforeachdb%'))
                      OR (s.command LIKE N'%DatabaseIntegrityCheck%'
                          AND (CHARINDEX(@db, s.command) > 0 OR s.command LIKE N'%USER_DATABASES%' OR s.command LIKE N'%ALL_DATABASES%'))));
END TRY
BEGIN CATCH
    SET @jobsError = ERROR_MESSAGE();
END CATCH;
SELECT @dataMB, @dataError, @jobsError;
SELECT name, CAST(enabled AS bit) FROM @jobs ORDER BY name;`

// CheckdbJobFacts reads the data size of a database and the jobs that already check its integrity.
func CheckdbJobFacts(ctx context.Context, db *sql.DB, database string) (domain.CheckdbJobFacts, error) {
	var f domain.CheckdbJobFacts
	rows, err := db.QueryContext(ctx, Tag(checkdbJobFactsQuery), sql.Named("db", database), sql.Named("job", domain.CheckdbJobName(database)))
	if err != nil {
		return f, friendlyError(err)
	}
	defer rows.Close()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return f, friendlyError(err)
		}
		return f, errors.New("SQL Server returned no integrity check facts")
	}
	var dataMB sql.NullInt64
	var dataErr, jobsErr sql.NullString
	if err := rows.Scan(&dataMB, &dataErr, &jobsErr); err != nil {
		return f, fmt.Errorf("read the integrity check facts of %s: %w", database, err)
	}
	if dataMB.Valid {
		f.DataMB = &dataMB.Int64
	}
	f.DataError, f.JobsError = dataErr.String, jobsErr.String
	if err := nextResultSet(rows, "integrity check jobs"); err != nil {
		return f, err
	}
	f.Jobs = []domain.IntegrityJob{}
	for rows.Next() {
		var j domain.IntegrityJob
		if err := rows.Scan(&j.Name, &j.Enabled); err != nil {
			return f, fmt.Errorf("read the integrity check jobs: %w", err)
		}
		f.Jobs = append(f.Jobs, j)
	}
	return f, friendlyErrOrNil(rows.Err())
}

// checkdbJobQuery reads one of kakapo's jobs back after a fix: whether it exists and is enabled,
// its schedule enabled, and the next run Agent worked out (0 before it has); then the server's
// GETDATE(). SELECT only.
const checkdbJobQuery = safeSession + `SET NOCOUNT ON;
SELECT j.name, CAST(j.enabled AS bit), CAST(ISNULL(sc.enabled, 0) AS bit), ISNULL(js.next_run_date, 0), ISNULL(js.next_run_time, 0)
FROM msdb.dbo.sysjobs AS j
LEFT JOIN msdb.dbo.sysjobschedules AS js ON js.job_id = j.job_id
LEFT JOIN msdb.dbo.sysschedules AS sc ON sc.schedule_id = js.schedule_id
WHERE j.name = @name;
SELECT GETDATE();`

// ReadCheckdbJob reads kakapo's integrity check job on a database back, with the server's clock.
func ReadCheckdbJob(ctx context.Context, db *sql.DB, database string) ([]domain.BackupJobState, time.Time, error) {
	rows, err := db.QueryContext(ctx, Tag(checkdbJobQuery), sql.Named("name", domain.CheckdbJobName(database)))
	if err != nil {
		return nil, time.Time{}, friendlyError(err)
	}
	defer rows.Close()
	out := []domain.BackupJobState{}
	for rows.Next() {
		var st domain.BackupJobState
		var day, at int
		if err := rows.Scan(&st.Name, &st.Enabled, &st.ScheduleEnabled, &day, &at); err != nil {
			return nil, time.Time{}, fmt.Errorf("read the integrity check job back: %w", err)
		}
		if day > 0 {
			t := runDate(day).Add(time.Duration(at/10000)*time.Hour + time.Duration(at/100%100)*time.Minute + time.Duration(at%100)*time.Second)
			st.NextRun = &t
		}
		out = append(out, st)
	}
	if err := nextResultSet(rows, "server clock"); err != nil {
		return nil, time.Time{}, err
	}
	var now time.Time
	if rows.Next() {
		if err := rows.Scan(&now); err != nil {
			return nil, time.Time{}, fmt.Errorf("read the server clock: %w", err)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, time.Time{}, friendlyError(err)
	}
	return out, naive(now), nil
}

// recoveryModelQuery reads a database's recovery model: the recovery-model fix's read-back.
const recoveryModelQuery = safeSession + `SELECT recovery_model_desc FROM sys.databases WHERE name = @db;`

// ReadRecoveryModel reads a database's recovery model (FULL, BULK_LOGGED, SIMPLE).
func ReadRecoveryModel(ctx context.Context, db *sql.DB, database string) (string, error) {
	var model string
	if err := db.QueryRowContext(ctx, Tag(recoveryModelQuery), sql.Named("db", database)).Scan(&model); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", fmt.Errorf("there is no database named %q", database)
		}
		return "", friendlyError(err)
	}
	return model, nil
}

// suspectPagesQuery reads this database's rows of msdb.dbo.suspect_pages: the suspect-page clear's
// preview and read-back. SELECT only.
const suspectPagesQuery = safeSession + `SELECT file_id, page_id, event_type, error_count, last_update_date
FROM msdb.dbo.suspect_pages
WHERE database_id = DB_ID(@db)
ORDER BY file_id, page_id;`

// ReadSuspectPages reads the suspect-page records of a database.
func ReadSuspectPages(ctx context.Context, db *sql.DB, database string) ([]domain.SuspectPage, error) {
	rows, err := db.QueryContext(ctx, Tag(suspectPagesQuery), sql.Named("db", database))
	if err != nil {
		return nil, friendlyError(err)
	}
	defer rows.Close()
	out := []domain.SuspectPage{}
	for rows.Next() {
		var p domain.SuspectPage
		var at time.Time
		if err := rows.Scan(&p.FileID, &p.PageID, &p.EventType, &p.ErrorCount, &at); err != nil {
			return nil, fmt.Errorf("read the suspect pages: %w", err)
		}
		p.LastUpdate = naive(at)
		out = append(out, p)
	}
	return out, friendlyErrOrNil(rows.Err())
}

// checkdbSession is what a CHECKDB run's connection is set to: no lock timeout (the check takes a
// few short locks over hours; the reads' 10-second timeout would abort it) and a low deadlock
// priority, so the application wins any deadlock.
const checkdbSession = "SET LOCK_TIMEOUT -1; SET DEADLOCK_PRIORITY LOW;"

// checkdbTableResults makes CHECKDB return its findings as rows instead of messages. The message
// queue (sqlexp.ReturnMessage) cannot be used here: go-mssqldb reads the response on a goroutine
// that hands tokens over a channel of five, CHECKDB sends more than five tokens that carry no
// message before its final DONE, and a loop waiting for a message then waits for ever (seen in
// the sandbox, docs/phase4f-contract.md; a short check slips under five tokens, which hid it).
const checkdbTableResults = "TABLERESULTS"

// RunCheckdb runs the DBCC CHECKDB statement a checkdb-now preview built and the user confirmed, on
// one connection, with TABLERESULTS added, and returns every finding it sent as rows (Error, Level,
// MessageText; a clean run returns none) in order. err is how the run ended when it did not end
// cleanly: an error SQL Server raised (a missing database, a permission), a cancelled or timed-out
// context, or a connection failure. It checks and changes nothing else.
func RunCheckdb(ctx context.Context, db *sql.DB, statement string) ([]domain.CheckdbMessage, error) {
	conn, err := db.Conn(ctx)
	if err != nil {
		return nil, friendlyError(err)
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, checkdbSession); err != nil {
		return nil, friendlyError(err)
	}
	rows, err := conn.QueryContext(ctx, Tag(withTableResults(statement)))
	if err != nil {
		return nil, friendlyError(err)
	}
	defer rows.Close()

	out := []domain.CheckdbMessage{}
	cols, err := rows.Columns()
	if err != nil {
		return nil, friendlyError(err)
	}
	at := map[string]int{}
	for i, c := range cols {
		at[strings.ToLower(c)] = i
	}
	for rows.Next() {
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return out, friendlyError(err)
		}
		m := domain.CheckdbMessage{
			Number: int32(intAt(vals, at, "error")), Class: uint8(intAt(vals, at, "level")),
			Text: textAt(vals, at, "messagetext"),
		}
		m.Error = m.Class >= 16 || m.Number >= 8900
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		return out, friendlyError(err)
	}
	return out, nil
}

// withTableResults adds TABLERESULTS to a CHECKDB statement that lacks it.
func withTableResults(statement string) string {
	if strings.Contains(strings.ToUpper(statement), checkdbTableResults) {
		return statement
	}
	s := strings.TrimRight(strings.TrimSpace(statement), ";")
	if strings.Contains(strings.ToUpper(s), " WITH ") {
		return s + ", " + checkdbTableResults + ";"
	}
	return s + " WITH " + checkdbTableResults + ";"
}

func intAt(vals []any, at map[string]int, col string) int {
	i, ok := at[col]
	if !ok || vals[i] == nil {
		return 0
	}
	switch v := vals[i].(type) {
	case int64:
		return int(v)
	case int32:
		return int(v)
	case int:
		return v
	case []byte:
		n, _ := strconv.Atoi(string(v))
		return n
	}
	return 0
}

func textAt(vals []any, at map[string]int, col string) string {
	i, ok := at[col]
	if !ok || vals[i] == nil {
		return ""
	}
	switch v := vals[i].(type) {
	case string:
		return v
	case []byte:
		return string(v)
	}
	return fmt.Sprint(vals[i])
}

// lastGoodCheckdbQuery reads a database's LastGoodCheckDbTime (server time): how a CHECKDB run whose
// connection was lost is read back (docs/phase4f-contract.md). SELECT only.
const lastGoodCheckdbQuery = safeSession + `SET NOCOUNT ON;
` + openAutoClose + `
SELECT CONVERT(datetime, DATABASEPROPERTYEX(@db, 'LastGoodCheckDbTime'));`

// ReadLastGoodCheckdb reads when CHECKDB last finished clean on a database, as the backup facts
// keep it (naive server time); nil when this version does not report it.
func ReadLastGoodCheckdb(ctx context.Context, db *sql.DB, database string) (*time.Time, error) {
	var at sql.NullTime
	if err := db.QueryRowContext(ctx, Tag(lastGoodCheckdbQuery), sql.Named("db", database)).Scan(&at); err != nil {
		return nil, friendlyError(err)
	}
	return naivePtr(at), nil
}

// checkdbNotice is an informational message as a CheckdbMessage.
func checkdbNotice(m fmt.Stringer) domain.CheckdbMessage {
	if e, ok := m.(mssql.Error); ok {
		return domain.CheckdbMessage{Number: e.Number, Class: e.Class, Text: e.Message}
	}
	return domain.CheckdbMessage{Text: m.String()}
}

// checkdbError is an error SQL Server sent as a CheckdbMessage.
func checkdbError(err error) domain.CheckdbMessage {
	var e mssql.Error
	if errors.As(err, &e) {
		return domain.CheckdbMessage{Number: e.Number, Class: e.Class, Text: e.Message, Error: true}
	}
	return domain.CheckdbMessage{Text: err.Error(), Error: true}
}
