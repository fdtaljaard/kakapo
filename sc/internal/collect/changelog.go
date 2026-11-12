package collect

import (
	"context"
	"database/sql"
	"strings"
	"time"
	"unicode/utf8"

	"sqlmonitor/internal/domain"
)

// kakapo's change log on the server (docs/phase5w-contract.md, B5-B6): the reads are SELECTs, tagged; the
// one write is a row of kakapo's own log put in master.dbo.kakapo_ChangeLog, only when the table is there
// and only when the row is not (its unique key is the action and the phase). Beside ExecFix, it is the only
// place the app writes to a monitored server, and it writes nothing else.

const changeLogFactsQuery = safeSession + `SET NOCOUNT ON;
DECLARE @t int = OBJECT_ID(N'master.dbo.kakapo_ChangeLog', N'U');
SELECT CONVERT(nvarchar(256), @@SERVERNAME), CONVERT(nvarchar(64), SERVERPROPERTY('ProductVersion')),
       CASE WHEN @t IS NULL THEN 0 ELSE 1 END,
       CASE WHEN OBJECT_ID(N'master.dbo.kakapo_ChangeLog_AppendOnly', N'TR') IS NULL THEN 0 ELSE 1 END,
       ISNULL((SELECT MAX(CONVERT(int, is_disabled)) FROM master.sys.triggers WHERE name = N'kakapo_ChangeLog_AppendOnly'), 0),
       CASE WHEN EXISTS (SELECT 1 FROM master.sys.extended_properties
                         WHERE class = 1 AND major_id = @t AND minor_id = 0 AND name = N'kakapo') THEN 1 ELSE 0 END,
       ISNULL((SELECT SUM(CONVERT(bigint, p.rows)) FROM master.sys.partitions AS p WHERE p.object_id = @t AND p.index_id IN (0, 1)), 0);`

// ReadChangeLog reads whether kakapo's change log is on the server: the table, its trigger (and whether it
// is disabled), its extended property and its row count.
func ReadChangeLog(ctx context.Context, db *sql.DB) (domain.ChangeLogFacts, error) {
	var f domain.ChangeLogFacts
	var name, version sql.NullString
	var table, trigger, off, prop int
	if err := db.QueryRowContext(ctx, Tag(changeLogFactsQuery)).Scan(&name, &version, &table, &trigger, &off, &prop, &f.Rows); err != nil {
		return f, friendlyError(err)
	}
	f.ServerName, f.ProductVersion = name.String, version.String
	f.Table, f.Trigger, f.TriggerOff, f.Property = table == 1, trigger == 1, off == 1, prop == 1
	return f, nil
}

const changeLogInstalledQuery = `SELECT CASE WHEN OBJECT_ID(N'master.dbo.kakapo_ChangeLog', N'U') IS NULL THEN 0 ELSE 1 END;`

// ChangeLogInstalled says whether the table is on the server.
func ChangeLogInstalled(ctx context.Context, db *sql.DB) (bool, error) {
	var n int
	if err := db.QueryRowContext(ctx, Tag(changeLogInstalledQuery)).Scan(&n); err != nil {
		return false, friendlyError(err)
	}
	return n == 1, nil
}

const changeLogKeysQuery = safeSession + `SELECT LOWER(CONVERT(nvarchar(36), ActionID)), Phase FROM master.dbo.kakapo_ChangeLog;`

// ReadChangeLogKeys reads the (action, phase) keys the table holds, as domain.ChangeLogKey writes them;
// installed false (and no keys) when the table is not there.
func ReadChangeLogKeys(ctx context.Context, db *sql.DB) (map[string]bool, bool, error) {
	installed, err := ChangeLogInstalled(ctx, db)
	if err != nil || !installed {
		return nil, installed, err
	}
	rows, err := db.QueryContext(ctx, Tag(changeLogKeysQuery))
	if err != nil {
		return nil, true, friendlyError(err)
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var id, phase string
		if err := rows.Scan(&id, &phase); err != nil {
			return nil, true, friendlyError(err)
		}
		out[domain.ChangeLogKey(id, phase)] = true
	}
	return out, true, friendlyErrOrNil(rows.Err())
}

const changeLogRowsQuery = safeSession + `SELECT LOWER(CONVERT(nvarchar(36), ActionID)), Phase, LoggedAtUtc, StartedAtUtc, EndedAtUtc, DurationMs,
       WindowsUser, SqlLogin, KakapoHost, KakapoVersion, DatabaseName, ObjectName, ChangeType, FixID, Description, Statement,
       RollbackStatement, ValueBefore, ValueAfter, Outcome, Result
FROM master.dbo.kakapo_ChangeLog ORDER BY StartedAtUtc, ID;`

// ReadChangeLogRows reads every row of the table (the What changed? screen shows the actions another kakapo
// install wrote there); nil when the table is not there.
func ReadChangeLogRows(ctx context.Context, db *sql.DB, serverID int64) ([]domain.ActionLogRow, error) {
	installed, err := ChangeLogInstalled(ctx, db)
	if err != nil || !installed {
		return nil, err
	}
	rows, err := db.QueryContext(ctx, Tag(changeLogRowsQuery))
	if err != nil {
		return nil, friendlyError(err)
	}
	defer rows.Close()
	out := []domain.ActionLogRow{}
	for rows.Next() {
		r := domain.ActionLogRow{ServerID: serverID}
		var ended sql.NullTime
		var dur sql.NullInt64
		var dbName, rollback, before, after, outcome, result sql.NullString
		if err := rows.Scan(&r.ActionID, &r.Phase, &r.LoggedAt, &r.StartedAt, &ended, &dur, &r.WindowsUser, &r.SQLLogin, &r.KakapoHost,
			&r.KakapoVersion, &dbName, &r.Object, &r.ChangeType, &r.FixID, &r.Description, &r.Statement, &rollback, &before, &after,
			&outcome, &result); err != nil {
			return nil, friendlyError(err)
		}
		r.LoggedAt, r.StartedAt = naive(r.LoggedAt), naive(r.StartedAt)
		if ended.Valid {
			t := naive(ended.Time)
			r.EndedAt = &t
		}
		if dur.Valid {
			r.DurationMs = &dur.Int64
		}
		r.Database, r.Rollback, r.ValueBefore, r.ValueAfter = dbName.String, rollback.String, before.String, after.String
		r.Outcome, r.Result = outcome.String, result.String
		out = append(out, r)
	}
	return out, friendlyErrOrNil(rows.Err())
}

// changeLogInsert puts one row of kakapo's own log in the table: nothing when the table is not there (0) or
// already holds the row (1); 2 when it was written. LoggedAtUtc is the table's default, when it reached the
// server; SqlLogin is the row's, or ORIGINAL_LOGIN() when kakapo did not record one.
const changeLogInsert = `SET NOCOUNT ON;
IF OBJECT_ID(N'master.dbo.kakapo_ChangeLog', N'U') IS NULL
    SELECT 0;
ELSE
BEGIN
    INSERT INTO master.dbo.kakapo_ChangeLog (ActionID, Phase, StartedAtUtc, EndedAtUtc, DurationMs, WindowsUser, SqlLogin, KakapoHost,
        KakapoVersion, DatabaseName, ObjectName, ChangeType, FixID, Description, Statement, RollbackStatement, ValueBefore, ValueAfter,
        Outcome, Result)
    SELECT CONVERT(uniqueidentifier, @ActionID), @Phase, CONVERT(datetime2(3), @StartedAt, 126), CONVERT(datetime2(3), @EndedAt, 126),
        @DurationMs, @WindowsUser, ISNULL(@SqlLogin, ORIGINAL_LOGIN()), @KakapoHost, @KakapoVersion, @DatabaseName, @ObjectName, @ChangeType,
        @FixID, @Description, @Statement, @RollbackStatement, @ValueBefore, @ValueAfter, @Outcome, @Result
    WHERE NOT EXISTS (SELECT 1 FROM master.dbo.kakapo_ChangeLog
                      WHERE ActionID = CONVERT(uniqueidentifier, @ActionID) AND Phase = @Phase);
    SELECT 1 + @@ROWCOUNT;
END;`

// changeLogTime is a time as the insert converts it (style 126).
func changeLogTime(t time.Time) string { return t.UTC().Format("2006-01-02T15:04:05.000") }

// cut keeps at most n characters of s, for the table's sized columns.
func cut(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	return string(r[:n])
}

func nullable(s string, n int) any {
	if s == "" {
		return nil
	}
	if n > 0 {
		return cut(s, n)
	}
	return s
}

// WriteChangeLogRow puts one row of kakapo's own log in the server's table, on db. installed false when the
// table is not there (nothing written); written false when it already held the row.
func WriteChangeLogRow(ctx context.Context, db *sql.DB, r domain.ActionLogRow) (installed, written bool, err error) {
	var ended, dur, login any
	if r.EndedAt != nil {
		ended = changeLogTime(*r.EndedAt)
	}
	if r.DurationMs != nil {
		dur = *r.DurationMs
	}
	if r.SQLLogin != "" || r.Phase == domain.ActionCopied {
		login = cut(r.SQLLogin, 256)
	}
	conn, err := db.Conn(ctx)
	if err != nil {
		return false, false, friendlyError(err)
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, safeSession); err != nil {
		return false, false, friendlyError(err)
	}
	var n int
	err = conn.QueryRowContext(ctx, Tag(changeLogInsert),
		sql.Named("ActionID", r.ActionID), sql.Named("Phase", r.Phase), sql.Named("StartedAt", changeLogTime(r.StartedAt)),
		sql.Named("EndedAt", ended), sql.Named("DurationMs", dur), sql.Named("WindowsUser", cut(r.WindowsUser, 256)),
		sql.Named("SqlLogin", login), sql.Named("KakapoHost", cut(r.KakapoHost, 256)), sql.Named("KakapoVersion", cut(r.KakapoVersion, 64)),
		sql.Named("DatabaseName", nullable(r.Database, 128)), sql.Named("ObjectName", cut(r.Object, 512)),
		sql.Named("ChangeType", cut(r.ChangeType, 32)), sql.Named("FixID", cut(r.FixID, 64)), sql.Named("Description", cut(r.Description, 2000)),
		sql.Named("Statement", r.Statement), sql.Named("RollbackStatement", nullable(r.Rollback, 0)),
		sql.Named("ValueBefore", nullable(r.ValueBefore, 4000)), sql.Named("ValueAfter", nullable(r.ValueAfter, 4000)),
		sql.Named("Outcome", nullable(r.Outcome, 16)), sql.Named("Result", nullable(r.Result, 4000))).Scan(&n)
	if err != nil {
		if strings.Contains(err.Error(), "UQ_kakapo_ChangeLog_Action") {
			// Another writer put it there between the check and the insert: it is there.
			return true, false, nil
		}
		return false, false, friendlyError(err)
	}
	return n > 0, n == 2, nil
}
