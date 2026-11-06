package collect

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"time"

	"sqlmonitor/internal/domain"
)

// openTransactionsFragment reads the transactions open in this database for @openMinutes minutes or more
// (ACT02, PerformanceTriage.sql's database-filtered half), with the session, its program and host and its
// last statement. The Performance card's batch runs it with ACT02's 5 minutes, the row versioning read
// with 0 (docs/phase5m-contract.md, section 5), so both read open transactions the one way. It needs
// @vss, @noVss, @dbid and @openMinutes declared before it; it fills @open and sets @transError. SELECTs
// only, into a table variable, in its own TRY.
const openTransactionsFragment = `/* ACT02 Transactions open @openMinutes minutes or more in this database */
DECLARE @open TABLE (session_id int NOT NULL, login_name nvarchar(128) NULL, host_name nvarchar(128) NULL, program_name nvarchar(128) NULL,
                     status nvarchar(30) NULL, minutes int NOT NULL, started datetime NULL, log_bytes bigint NULL, last_stmt nvarchar(4000) NULL);
DECLARE @transError nvarchar(2048);
IF @vss = 0 SET @transError = @noVss;
ELSE
BEGIN TRY
    INSERT @open (session_id, login_name, host_name, program_name, status, minutes, started, log_bytes, last_stmt)
    SELECT s.session_id, s.login_name, s.host_name, s.program_name, s.status,
           DATEDIFF(MINUTE, dt.database_transaction_begin_time, GETDATE()), dt.database_transaction_begin_time,
           dt.database_transaction_log_bytes_used, LEFT(q.text, 4000)
    FROM sys.dm_tran_database_transactions dt
    JOIN sys.dm_tran_session_transactions st ON st.transaction_id = dt.transaction_id
    JOIN sys.dm_exec_sessions s ON s.session_id = st.session_id
    OUTER APPLY (SELECT TOP (1) t.text FROM sys.dm_exec_connections c CROSS APPLY sys.dm_exec_sql_text(c.most_recent_sql_handle) t
                 WHERE c.session_id = s.session_id) q
    WHERE dt.database_id = @dbid
      AND dt.database_transaction_begin_time < DATEADD(MINUTE, -@openMinutes, GETDATE())
      AND s.session_id <> @@SPID;
END TRY
BEGIN CATCH
    SET @transError = ERROR_MESSAGE();
END CATCH;
`

// openTransactionColumns are the columns of @open, in the order scanOpenTransaction reads them.
const openTransactionColumns = "session_id, login_name, host_name, program_name, status, minutes, started, log_bytes, last_stmt"

// scanOpenTransaction reads one row of openTransactionColumns.
func scanOpenTransaction(rows *sql.Rows) (domain.OpenTransaction, error) {
	var x domain.OpenTransaction
	var login, host, program, status, last sql.NullString
	var started sql.NullTime
	var logBytes sql.NullInt64
	if err := rows.Scan(&x.Session, &login, &host, &program, &status, &x.Minutes, &started, &logBytes, &last); err != nil {
		return x, err
	}
	x.Login, x.Host, x.Program, x.Status, x.LastStatement = login.String, host.String, program.String, status.String, last.String
	if started.Valid {
		x.Started = naive(started.Time)
	}
	x.LogBytes = logBytes.Int64
	return x, nil
}

// rowVersioningQuery is the row versioning read (docs/phase5m-contract.md, section 5), run with the
// overview and with the rcsi fix's preview: the database's settings (READ COMMITTED SNAPSHOT, snapshot
// isolation, ADR from SQL Server 2019 behind a COL_LENGTH guard, the compatibility level), tempdb's
// version store as TMP03 reads it (in KB here) and this database's share of it (sys.dm_tran_version_store_space_usage,
// behind an OBJECT_ID guard), this database's persistent version store when ADR is on, the oldest
// transaction open in the database (openTransactionsFragment, from 0 minutes) and the other user sessions
// in it now (the rcsi fix's count). SELECTs only; each area in its own TRY with its error column. Result
// sets: one row of facts, then the oldest open transaction (none when nothing is open).
var rowVersioningQuery = Tag(safeSession + `SET NOCOUNT ON;
DECLARE @dbid int = DB_ID(@db), @openMinutes int = 0;
DECLARE @vss bit = CASE WHEN HAS_PERMS_BY_NAME(NULL, NULL, 'VIEW SERVER STATE') = 1 THEN 1 ELSE 0 END;
DECLARE @noVss nvarchar(100) = N'the login does not have VIEW SERVER STATE.';
DECLARE @found bit = 0, @rcsi bit, @si nvarchar(60), @adr bit, @compat int, @settingsError nvarchar(2048);
DECLARE @vsKB bigint, @dbVsKB bigint, @vsError nvarchar(2048), @pvsKB bigint, @pvsError nvarchar(2048);
DECLARE @users int, @usersError nvarchar(2048);

/* Settings; is_accelerated_database_recovery_on exists from SQL Server 2019 */
BEGIN TRY
    SELECT @found = 1, @rcsi = d.is_read_committed_snapshot_on, @si = d.snapshot_isolation_state_desc, @compat = d.compatibility_level
    FROM sys.databases AS d WHERE d.name = @db;
    IF COL_LENGTH(N'sys.databases', N'is_accelerated_database_recovery_on') IS NOT NULL
        EXEC sys.sp_executesql N'/* kakapo */ SELECT @a = d.is_accelerated_database_recovery_on FROM sys.databases AS d WHERE d.name = @n;',
            N'@n sysname, @a bit OUTPUT', @n = @db, @a = @adr OUTPUT;
END TRY
BEGIN CATCH
    SET @settingsError = ERROR_MESSAGE();
END CATCH;

/* Version store: tempdb's, as TMP03 reads it, and this database's share (SQL Server 2017 and later) */
IF @vss = 0 SET @vsError = @noVss;
ELSE
BEGIN TRY
    SELECT @vsKB = SUM(CONVERT(bigint, version_store_reserved_page_count)) * 8 FROM tempdb.sys.dm_db_file_space_usage;
    IF OBJECT_ID(N'sys.dm_tran_version_store_space_usage') IS NOT NULL
        EXEC sys.sp_executesql N'/* kakapo */ SELECT @k = v.reserved_space_kb FROM sys.dm_tran_version_store_space_usage AS v WHERE v.database_id = @d;',
            N'@d int, @k bigint OUTPUT', @d = @dbid, @k = @dbVsKB OUTPUT;
END TRY
BEGIN CATCH
    SET @vsError = ERROR_MESSAGE();
END CATCH;

/* PVS: this database's persistent version store when ADR is on (SQL Server 2019 and later) */
IF @adr = 1
BEGIN TRY
    IF OBJECT_ID(N'sys.dm_tran_persistent_version_store_stats') IS NOT NULL
        EXEC sys.sp_executesql N'/* kakapo */ SELECT @k = p.persistent_version_store_size_kb FROM sys.dm_tran_persistent_version_store_stats AS p WHERE p.database_id = @d;',
            N'@d int, @k bigint OUTPUT', @d = @dbid, @k = @pvsKB OUTPUT;
END TRY
BEGIN CATCH
    SET @pvsError = ERROR_MESSAGE();
END CATCH;

` + openTransactionsFragment + `
/* Sessions: the other user sessions in the database now (the rcsi fix's count) */
BEGIN TRY
    SELECT @users = COUNT(*) FROM sys.dm_exec_sessions AS s
    WHERE s.database_id = @dbid AND s.session_id <> @@SPID AND s.is_user_process = 1;
END TRY
BEGIN CATCH
    SET @usersError = ERROR_MESSAGE();
END CATCH;

SELECT @found, @rcsi, @si, @adr, @compat, @settingsError, @vsKB, @dbVsKB, @vsError, @pvsKB, @pvsError, @transError, @users, @usersError,
       CAST(SERVERPROPERTY('ProductVersion') AS nvarchar(64)), SYSUTCDATETIME();
SELECT TOP (1) ` + openTransactionColumns + ` FROM @open ORDER BY started, session_id;`)

// ReadRowVersioning reads one database's row versioning facts, SELECTs only. An error says why nothing
// could be read (the database does not exist); a part that failed is its area's error.
func ReadRowVersioning(ctx context.Context, db *sql.DB, database string) (domain.RowVersioningFacts, error) {
	f := domain.RowVersioningFacts{Database: database}
	rows, err := db.QueryContext(ctx, rowVersioningQuery, sql.Named("db", database))
	if err != nil {
		return f, friendlyError(err)
	}
	defer rows.Close()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return f, friendlyError(err)
		}
		return f, errors.New("SQL Server returned nothing for the row versioning read")
	}
	var (
		found, rcsi, adr                                            sql.NullBool
		si, settingsErr, vsErr, pvsErr, transErr, usersErr, version sql.NullString
		compat, vsKB, dbVsKB, pvsKB, users                          sql.NullInt64
		readAt                                                      sql.NullTime
	)
	if err := rows.Scan(&found, &rcsi, &si, &adr, &compat, &settingsErr, &vsKB, &dbVsKB, &vsErr, &pvsKB, &pvsErr, &transErr, &users, &usersErr,
		&version, &readAt); err != nil {
		return f, err
	}
	if !found.Bool && settingsErr.String == "" {
		return f, fmt.Errorf("there is no database named %q", database)
	}
	f.RCSI, f.SnapshotIsolation, f.CompatibilityLevel, f.SettingsError = rcsi.Bool, si.String, int(compat.Int64), settingsErr.String
	if adr.Valid {
		v := adr.Bool
		f.ADR = &v
	}
	f.TempdbVersionStoreKB, f.DatabaseVersionKB, f.VersionStoreError = int64Ptr(vsKB), int64Ptr(dbVsKB), vsErr.String
	f.PVSKB, f.PVSError = int64Ptr(pvsKB), pvsErr.String
	f.TransactionsError, f.Users, f.UsersError = transErr.String, int(users.Int64), usersErr.String
	f.ProductVersion = version.String
	f.ReadAt = time.Now().UTC()
	if readAt.Valid {
		t := readAt.Time
		f.ReadAt = time.Date(t.Year(), t.Month(), t.Day(), t.Hour(), t.Minute(), t.Second(), t.Nanosecond(), time.UTC)
	}
	if err := nextResultSet(rows, "the oldest open transaction"); err != nil {
		return f, err
	}
	if rows.Next() {
		x, err := scanOpenTransaction(rows)
		if err != nil {
			return f, err
		}
		f.Oldest = &x
	}
	if err := rows.Err(); err != nil {
		return f, friendlyError(err)
	}
	return f, nil
}

func int64Ptr(v sql.NullInt64) *int64 {
	if !v.Valid {
		return nil
	}
	n := v.Int64
	return &n
}

// openMinutesDeclaration declares ACT02's 5 minutes for the Performance card's batch.
var openMinutesDeclaration = "DECLARE @openMinutes int = " + strconv.Itoa(domain.Act02Minutes) + ";\n"
