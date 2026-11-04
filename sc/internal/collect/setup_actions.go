package collect

import (
	"context"
	"database/sql"
	"fmt"

	"sqlmonitor/internal/domain"
)

// The reads of the Setup card's actions (docs/phase4d-contract.md, part 1): what each fix's preview
// is built from and what its read-back reads again. SELECTs only (and DBCC TRACESTATUS, which
// reports); each area that needs a permission the login may lack is read in its own TRY with an
// error column. The statements that change something are built in domain and run by ExecFix.

// userAccessQuery reads a database's user_access_desc and the other sessions using it (who, and
// the command each runs now), for the multi-user fix.
const userAccessQuery = safeSession + `SET NOCOUNT ON;
DECLARE @found bit = 0, @access nvarchar(60), @sessionsError nvarchar(2048);
SELECT @found = 1, @access = d.user_access_desc FROM sys.databases AS d WHERE d.name = @db;
DECLARE @s TABLE (session_id int NOT NULL, login_name nvarchar(128) NULL, host_name nvarchar(128) NULL,
                  program_name nvarchar(128) NULL, command nvarchar(32) NULL);
/* Who is in the database: without VIEW SERVER STATE only the login's own sessions are visible */
IF HAS_PERMS_BY_NAME(NULL, NULL, 'VIEW SERVER STATE') = 0
    SET @sessionsError = N'the login does not have VIEW SERVER STATE';
ELSE
BEGIN TRY
    INSERT @s (session_id, login_name, host_name, program_name, command)
    SELECT TOP (10) s.session_id, s.login_name, s.host_name, s.program_name, r.command
    FROM sys.dm_exec_sessions AS s
    LEFT JOIN sys.dm_exec_requests AS r ON r.session_id = s.session_id
    WHERE s.session_id <> @@SPID AND s.is_user_process = 1
      AND (s.database_id = DB_ID(@db) OR r.database_id = DB_ID(@db))
    ORDER BY CASE WHEN r.command IS NULL THEN 1 ELSE 0 END, s.session_id;
END TRY
BEGIN CATCH
    SET @sessionsError = ERROR_MESSAGE();
END CATCH;
SELECT @found, @access, @sessionsError, CAST(ISNULL(@@SERVERNAME, N'') AS nvarchar(256)), CAST(SERVERPROPERTY('ProductVersion') AS nvarchar(64));
SELECT session_id, login_name, host_name, program_name, command FROM @s ORDER BY session_id;`

// ReadUserAccess reads what the multi-user fix needs about one database.
func ReadUserAccess(ctx context.Context, db *sql.DB, database string) (domain.UserAccessFacts, error) {
	f := domain.UserAccessFacts{Database: database, Sessions: []domain.UserAccessSession{}}
	rows, err := db.QueryContext(ctx, Tag(userAccessQuery), sql.Named("db", database))
	if err != nil {
		return f, friendlyError(err)
	}
	defer rows.Close()
	var found bool
	var access, sessionsErr, server, version sql.NullString
	if !rows.Next() {
		return f, fmt.Errorf("SQL Server returned nothing for the database %q", database)
	}
	if err := rows.Scan(&found, &access, &sessionsErr, &server, &version); err != nil {
		return f, err
	}
	if !found {
		return f, fmt.Errorf("there is no database named %q", database)
	}
	f.UserAccess, f.SessionsError, f.ServerName, f.ProductVersion = access.String, sessionsErr.String, server.String, version.String
	if err := nextResultSet(rows, "sessions"); err != nil {
		return f, err
	}
	for rows.Next() {
		var s domain.UserAccessSession
		var login, host, program, command sql.NullString
		if err := rows.Scan(&s.Session, &login, &host, &program, &command); err != nil {
			return f, err
		}
		s.Login, s.Host, s.Program, s.Command = login.String, host.String, program.String, command.String
		f.Sessions = append(f.Sessions, s)
	}
	return f, friendlyErrOrNil(rows.Err())
}

// serverConfigQuery reads the settings waiting for RECONFIGURE (value <> value_in_use) with
// whether each is dynamic and advanced, and the values in use of 'show advanced options' and the
// two memory settings, for the reconfigure and min-server-memory fixes.
const serverConfigQuery = safeSession + `SET NOCOUNT ON;
SELECT CAST(ISNULL(@@SERVERNAME, N'') AS nvarchar(256)), CAST(SERVERPROPERTY('ProductVersion') AS nvarchar(64)),
       ISNULL((SELECT CONVERT(bigint, value_in_use) FROM sys.configurations WHERE name = N'show advanced options'), 0),
       ISNULL((SELECT CONVERT(bigint, value_in_use) FROM sys.configurations WHERE name = N'min server memory (MB)'), 0),
       ISNULL((SELECT CONVERT(bigint, value_in_use) FROM sys.configurations WHERE name = N'max server memory (MB)'), 0);
SELECT name, CONVERT(bigint, value), CONVERT(bigint, value_in_use), CAST(is_dynamic AS bit), CAST(is_advanced AS bit)
FROM sys.configurations WHERE value <> value_in_use ORDER BY name;`

// ReadServerConfig reads what the reconfigure and min-server-memory fixes need from
// sys.configurations.
func ReadServerConfig(ctx context.Context, db *sql.DB) (domain.ServerConfigFacts, error) {
	f := domain.ServerConfigFacts{Pending: []domain.PendingSetting{}}
	rows, err := db.QueryContext(ctx, Tag(serverConfigQuery))
	if err != nil {
		return f, friendlyError(err)
	}
	defer rows.Close()
	if !rows.Next() {
		return f, fmt.Errorf("SQL Server returned no configuration")
	}
	if err := rows.Scan(&f.ServerName, &f.ProductVersion, &f.ShowAdvanced, &f.MinServerMemoryMB, &f.MaxServerMemoryMB); err != nil {
		return f, err
	}
	if err := nextResultSet(rows, "pending settings"); err != nil {
		return f, err
	}
	for rows.Next() {
		var p domain.PendingSetting
		var value, inUse sql.NullInt64
		if err := rows.Scan(&p.Name, &value, &inUse, &p.Dynamic, &p.Advanced); err != nil {
			return f, err
		}
		p.Value, p.ValueInUse = value.Int64, inUse.Int64
		f.Pending = append(f.Pending, p)
	}
	return f, friendlyErrOrNil(rows.Err())
}

// traceFlagQuery reads the global trace flags (DBCC TRACESTATUS(-1), which reports only) and the
// startup parameters (sys.dm_server_registry, VIEW SERVER STATE), each in its own TRY, for the
// trace-flag-off fix and its read-back.
const traceFlagQuery = safeSession + `SET NOCOUNT ON;
DECLARE @tf TABLE (trace_flag int NOT NULL, [status] int NULL, [global] int NULL, [session] int NULL);
DECLARE @args TABLE (value nvarchar(400) NULL);
DECLARE @tfError nvarchar(2048), @argsError nvarchar(2048);
BEGIN TRY
    INSERT @tf (trace_flag, [status], [global], [session])
    EXEC (N'DBCC TRACESTATUS(-1) WITH NO_INFOMSGS');
END TRY
BEGIN CATCH
    SET @tfError = ERROR_MESSAGE();
END CATCH;
IF HAS_PERMS_BY_NAME(NULL, NULL, 'VIEW SERVER STATE') = 0
    SET @argsError = N'the login does not have VIEW SERVER STATE';
ELSE
BEGIN TRY
    INSERT @args (value)
    SELECT CONVERT(nvarchar(400), value_data) FROM sys.dm_server_registry WHERE value_name LIKE N'SQLArg%';
END TRY
BEGIN CATCH
    SET @argsError = ERROR_MESSAGE();
END CATCH;
SELECT @tfError, @argsError, CAST(ISNULL(@@SERVERNAME, N'') AS nvarchar(256)), CAST(SERVERPROPERTY('ProductVersion') AS nvarchar(64));
SELECT trace_flag FROM @tf WHERE [global] = 1 ORDER BY trace_flag;
SELECT value FROM @args;`

// ReadTraceFlags reads the global trace flags and the -T flags of the startup parameters. A flag
// list that cannot be read is an error: the fix needs it.
func ReadTraceFlags(ctx context.Context, db *sql.DB) (domain.TraceFlagFacts, error) {
	f := domain.TraceFlagFacts{Flags: []int{}, StartupFlags: []int{}}
	rows, err := db.QueryContext(ctx, Tag(traceFlagQuery))
	if err != nil {
		return f, friendlyError(err)
	}
	defer rows.Close()
	if !rows.Next() {
		return f, fmt.Errorf("SQL Server returned no trace flags")
	}
	var tfErr, argsErr sql.NullString
	if err := rows.Scan(&tfErr, &argsErr, &f.ServerName, &f.ProductVersion); err != nil {
		return f, err
	}
	if tfErr.Valid && tfErr.String != "" {
		return f, fmt.Errorf("could not read the global trace flags (DBCC TRACESTATUS needs sysadmin on older versions): %s", tfErr.String)
	}
	f.StartupError = argsErr.String
	if err := nextResultSet(rows, "trace flags"); err != nil {
		return f, err
	}
	for rows.Next() {
		var n int
		if err := rows.Scan(&n); err != nil {
			return f, err
		}
		f.Flags = append(f.Flags, n)
	}
	if err := nextResultSet(rows, "startup parameters"); err != nil {
		return f, err
	}
	for rows.Next() {
		var v sql.NullString
		if err := rows.Scan(&v); err != nil {
			return f, err
		}
		if n, ok := domain.ParseStartupFlag(v.String); ok {
			f.StartupFlags = append(f.StartupFlags, n)
		}
	}
	return f, friendlyErrOrNil(rows.Err())
}

// scopedConfigBatch reads the server's MAXDOP and, inside the database, its scoped configurations,
// for the scoped-config-reset fix and its read-back.
func scopedConfigBatch(database string) string {
	return Tag(safeSession + `SET NOCOUNT ON;
SELECT CAST(ISNULL(@@SERVERNAME, N'') AS nvarchar(256)), CAST(SERVERPROPERTY('ProductVersion') AS nvarchar(64)),
       ISNULL((SELECT CONVERT(int, value_in_use) FROM sys.configurations WHERE name = N'max degree of parallelism'), -1);
EXEC ` + domain.QuoteName(database) + `.sys.sp_executesql N'` + KakapoTag + ` SELECT name, CONVERT(nvarchar(400), value) FROM sys.database_scoped_configurations ORDER BY name;';`)
}

// ReadScopedConfigs reads a database's scoped configurations (SQL Server 2016 and later) and the
// server's MAXDOP.
func ReadScopedConfigs(ctx context.Context, db *sql.DB, database string) (domain.ScopedConfigFacts, error) {
	f := domain.ScopedConfigFacts{Database: database, Configs: []domain.ScopedConfig{}, ServerMaxDop: -1}
	rows, err := db.QueryContext(ctx, scopedConfigBatch(database))
	if err != nil {
		return f, friendlyError(err)
	}
	defer rows.Close()
	if !rows.Next() {
		return f, fmt.Errorf("SQL Server returned nothing for the scoped configurations of %q", database)
	}
	if err := rows.Scan(&f.ServerName, &f.ProductVersion, &f.ServerMaxDop); err != nil {
		return f, err
	}
	if err := nextResultSet(rows, "scoped configurations"); err != nil {
		return f, err
	}
	for rows.Next() {
		var c domain.ScopedConfig
		var v sql.NullString
		if err := rows.Scan(&c.Name, &v); err != nil {
			return f, err
		}
		c.Value = v.String
		f.Configs = append(f.Configs, c)
	}
	return f, friendlyErrOrNil(rows.Err())
}
