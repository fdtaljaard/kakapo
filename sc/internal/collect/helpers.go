package collect

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"

	"sqlmonitor/internal/domain"
)

// The reads of kakapo's installed helpers (docs/phase5e-contract.md): the state of kakapo's Extended
// Events session and the session's files on disk. SELECTs only; each area that needs a permission the
// login may lack (the session catalog, the running sessions, the configuration, the other sessions) is
// read in its own TRY with an error column. The statements that create or drop the session are built in
// domain and run by ExecFix inside the registered fixes.

// xeSessionQuery reads whether a session named kakapo exists (sys.server_event_sessions) with its
// events, targets and file target settings, whether it runs (sys.dm_xe_sessions: since when, in UTC;
// the file it writes and its file target's execution_count), the blocked process threshold in use,
// and the other sessions that record the blocked process report; with @@SERVERNAME, the version, the
// engine edition and the error log file. Result sets: one row, then (kind, name) rows: event, target,
// other.
const xeSessionQuery = safeSession + `SET NOCOUNT ON;
DECLARE @exists bit = 0, @running bit = 0, @startup bit = 0, @dispatch bigint, @created datetime2(3), @file nvarchar(520), @current nvarchar(520),
        @maxMB bigint, @maxFiles bigint, @execs bigint, @threshold int = -1,
        @sessionErr nvarchar(2048), @runErr nvarchar(2048), @configErr nvarchar(2048), @othersErr nvarchar(2048);
DECLARE @names TABLE (kind nvarchar(10) NOT NULL, name nvarchar(300) NOT NULL);
DECLARE @offset int = DATEDIFF(MINUTE, SYSDATETIME(), SYSUTCDATETIME());

BEGIN TRY
    SELECT @exists = 1, @startup = s.startup_state, @dispatch = s.max_dispatch_latency
    FROM sys.server_event_sessions AS s WHERE s.name = N'kakapo';
    INSERT @names (kind, name)
    SELECT N'event', e.package + N'.' + e.name
    FROM sys.server_event_session_events AS e
    JOIN sys.server_event_sessions AS s ON s.event_session_id = e.event_session_id
    WHERE s.name = N'kakapo';
    INSERT @names (kind, name)
    SELECT N'target', t.package + N'.' + t.name
    FROM sys.server_event_session_targets AS t
    JOIN sys.server_event_sessions AS s ON s.event_session_id = t.event_session_id
    WHERE s.name = N'kakapo';
    SELECT @file = MAX(CASE WHEN f.name = N'filename' THEN CONVERT(nvarchar(520), f.value) END),
           @maxMB = MAX(CASE WHEN f.name = N'max_file_size' THEN CONVERT(bigint, f.value) END),
           @maxFiles = MAX(CASE WHEN f.name = N'max_rollover_files' THEN CONVERT(bigint, f.value) END)
    FROM sys.server_event_session_fields AS f
    JOIN sys.server_event_session_targets AS t ON t.event_session_id = f.event_session_id AND t.target_id = f.object_id
    JOIN sys.server_event_sessions AS s ON s.event_session_id = t.event_session_id
    WHERE s.name = N'kakapo' AND t.name = N'event_file';
END TRY
BEGIN CATCH
    SET @sessionErr = ERROR_MESSAGE();
END CATCH;

BEGIN TRY
    SELECT @running = 1, @created = DATEADD(MINUTE, @offset, x.create_time) FROM sys.dm_xe_sessions AS x WHERE x.name = N'kakapo';
    SELECT @execs = t.execution_count,
           @current = CONVERT(xml, t.target_data).value('(EventFileTarget/File/@name)[1]', 'nvarchar(520)')
    FROM sys.dm_xe_session_targets AS t
    JOIN sys.dm_xe_sessions AS x ON x.address = t.event_session_address
    WHERE x.name = N'kakapo' AND t.target_name = N'event_file';
END TRY
BEGIN CATCH
    SET @runErr = ERROR_MESSAGE();
END CATCH;

BEGIN TRY
    SELECT @threshold = CONVERT(int, c.value_in_use) FROM sys.configurations AS c WHERE c.name = N'blocked process threshold (s)';
END TRY
BEGIN CATCH
    SET @configErr = ERROR_MESSAGE();
END CATCH;

BEGIN TRY
    INSERT @names (kind, name)
    SELECT DISTINCT N'other', s.name
    FROM sys.server_event_sessions AS s
    JOIN sys.server_event_session_events AS e ON e.event_session_id = s.event_session_id
    WHERE e.name = N'blocked_process_report' AND s.name <> N'kakapo';
END TRY
BEGIN CATCH
    SET @othersErr = ERROR_MESSAGE();
END CATCH;

SELECT @exists, @running, @startup, @dispatch, @created, @file, @current, @maxMB, @maxFiles, @execs, @threshold,
       @sessionErr, @runErr, @configErr, @othersErr,
       CAST(ISNULL(@@SERVERNAME, N'') AS nvarchar(256)), CAST(SERVERPROPERTY('ProductVersion') AS nvarchar(64)),
       CONVERT(int, SERVERPROPERTY('EngineEdition')), CAST(SERVERPROPERTY('ErrorLogFileName') AS nvarchar(520));
SELECT kind, name FROM @names ORDER BY kind, name;`

// xeRow is the first row of xeSessionQuery.
type xeRow struct {
	exists, running, startup                    bool
	dispatch, maxMB, maxFiles, execs, threshold sql.NullInt64
	created                                     sql.NullTime
	file, current                               sql.NullString
	sessionErr, runErr, configErr, othersErr    sql.NullString
}

// xeSessionFrom builds the session state from the query's row and its names: the shape kakapo creates
// marked, the dispatch latency in seconds (sys.server_event_sessions keeps it in milliseconds), the
// errors of the parts that could not be read joined.
func xeSessionFrom(r xeRow, names [][2]string) domain.XESession {
	s := domain.XESession{Exists: r.exists, Running: r.running, StartupState: r.startup, Events: []string{}, Targets: []string{},
		FileName: r.file.String, CurrentFile: r.current.String, MaxFileMB: int(r.maxMB.Int64), MaxFiles: int(r.maxFiles.Int64),
		EventsCaptured: r.execs.Int64, ThresholdSeconds: -1}
	if r.threshold.Valid {
		s.ThresholdSeconds = int(r.threshold.Int64)
	}
	if d := r.dispatch.Int64; d > 0 {
		s.DispatchSec = int(d)
		if d >= 1000 {
			s.DispatchSec = int(d / 1000)
		}
	}
	if r.running && r.created.Valid {
		t := r.created.Time
		// datetime2 has no zone; the query already moved it to UTC.
		u := time.Date(t.Year(), t.Month(), t.Day(), t.Hour(), t.Minute(), t.Second(), t.Nanosecond(), time.UTC)
		s.StartedAt = &u
	}
	for _, n := range names {
		switch n[0] {
		case "event":
			s.Events = append(s.Events, n[1])
		case "target":
			s.Targets = append(s.Targets, n[1])
		case "other":
			s.OtherSessions = append(s.OtherSessions, n[1])
		}
	}
	s.Ours = s.HasKakapoShape()
	errs := []string{}
	for _, e := range []struct{ what, msg string }{
		{"the session catalog", r.sessionErr.String}, {"the running sessions", r.runErr.String},
		{"the blocked process threshold", r.configErr.String}, {"the other sessions", r.othersErr.String},
	} {
		if e.msg != "" {
			errs = append(errs, e.what+" ("+e.msg+")")
		}
	}
	if len(errs) > 0 {
		s.Error = "could not read " + strings.Join(errs, "; ")
	}
	return s
}

// ReadHelpers reads the state of kakapo's event session with the server's name, version, edition and
// error log file. An error means nothing could be read; a part that failed is in Session.Error.
func ReadHelpers(ctx context.Context, db *sql.DB) (domain.HelperFacts, error) {
	var f domain.HelperFacts
	rows, err := db.QueryContext(ctx, Tag(xeSessionQuery))
	if err != nil {
		return f, friendlyError(err)
	}
	defer rows.Close()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return f, friendlyError(err)
		}
		return f, errors.New("SQL Server returned nothing for kakapo's event session")
	}
	var r xeRow
	var server, version, errorLog sql.NullString
	var edition sql.NullInt64
	if err := rows.Scan(&r.exists, &r.running, &r.startup, &r.dispatch, &r.created, &r.file, &r.current, &r.maxMB, &r.maxFiles, &r.execs, &r.threshold,
		&r.sessionErr, &r.runErr, &r.configErr, &r.othersErr, &server, &version, &edition, &errorLog); err != nil {
		return f, err
	}
	if err := nextResultSet(rows, "session events"); err != nil {
		return f, err
	}
	names := [][2]string{}
	for rows.Next() {
		var kind, name sql.NullString
		if err := rows.Scan(&kind, &name); err != nil {
			return f, err
		}
		names = append(names, [2]string{kind.String, name.String})
	}
	if err := rows.Err(); err != nil {
		return f, friendlyError(err)
	}
	f.ServerName, f.ProductVersion, f.EngineEdition, f.ErrorLogFile = server.String, version.String, int(edition.Int64), errorLog.String
	f.Session = xeSessionFrom(r, names)
	return f, nil
}

// xeFilesQuery lists the session's .xel files in its folder (sys.dm_os_enumerate_filesystem, SQL
// Server 2017 and later), for the remove fix's warning; in its own TRY with an error column. SELECTs
// only. Result sets: one row (error), then the files.
const xeFilesQuery = safeSession + `SET NOCOUNT ON;
DECLARE @filesErr nvarchar(2048);
DECLARE @files TABLE (path nvarchar(520) NOT NULL);
DECLARE @major int = CONVERT(int, PARSENAME(CONVERT(varchar(32), SERVERPROPERTY('ProductVersion')), 4));
IF @major < 14
    SET @filesErr = N'SQL Server 2016 and earlier cannot list a folder';
ELSE
BEGIN TRY
    INSERT @files (path)
    EXEC sys.sp_executesql N'/* kakapo */ SELECT TOP (50) full_filesystem_path FROM sys.dm_os_enumerate_filesystem(@folder, N''kakapo*.xel'') WHERE is_directory = 0 ORDER BY full_filesystem_path;',
        N'@folder nvarchar(520)', @folder = @folder;
END TRY
BEGIN CATCH
    SET @filesErr = ERROR_MESSAGE();
END CATCH;
SELECT @filesErr;
SELECT path FROM @files ORDER BY path;`

// ReadXEFiles lists the session's files in a folder. When they cannot be listed the error says why
// and the remove fix names the pattern instead.
func ReadXEFiles(ctx context.Context, db *sql.DB, folder string) ([]string, error) {
	rows, err := db.QueryContext(ctx, Tag(xeFilesQuery), sql.Named("folder", folder))
	if err != nil {
		return nil, friendlyError(err)
	}
	defer rows.Close()
	if !rows.Next() {
		return nil, errors.New("SQL Server returned nothing for the session's files")
	}
	var filesErr sql.NullString
	if err := rows.Scan(&filesErr); err != nil {
		return nil, err
	}
	if filesErr.String != "" {
		return nil, errors.New(filesErr.String)
	}
	if err := nextResultSet(rows, "files"); err != nil {
		return nil, err
	}
	out := []string{}
	for rows.Next() {
		var p sql.NullString
		if err := rows.Scan(&p); err != nil {
			return nil, err
		}
		out = append(out, p.String)
	}
	return out, friendlyErrOrNil(rows.Err())
}
