package collect

import (
	"context"
	"database/sql"
	"time"

	"sqlmonitor/internal/domain"
)

// instanceQuery reads the header of the instance page in one batch. sqlserver_start_time is in
// the server's local time, so it is moved to UTC with the server's own offset from
// SYSDATETIMEOFFSET(). sys.dm_os_sys_info needs VIEW SERVER STATE; a login without it gets the
// creation time of tempdb instead, which is rebuilt at every start and so within seconds of it.
const instanceQuery = safeSession + `SET NOCOUNT ON;
DECLARE @started datetime2(3);
IF HAS_PERMS_BY_NAME(NULL, NULL, 'VIEW SERVER STATE') = 1
    SELECT @started = sqlserver_start_time FROM sys.dm_os_sys_info;
IF @started IS NULL
    SELECT @started = create_date FROM sys.databases WHERE database_id = 2;
SELECT CAST(ISNULL(@@SERVERNAME, N'') AS nvarchar(256)),
       CAST(SERVERPROPERTY('Edition') AS nvarchar(128)),
       CAST(SERVERPROPERTY('ProductVersion') AS nvarchar(64)),
       CAST(SERVERPROPERTY('ProductLevel') AS nvarchar(64)),
       DATEADD(minute, -DATEPART(TZOFFSET, SYSDATETIMEOFFSET()), @started),
       (SELECT COUNT(*) FROM sys.databases
        WHERE database_id > 4            -- not master, tempdb, model, msdb
          AND source_database_id IS NULL -- not database snapshots
       );`

// InstanceInfo reads @@SERVERNAME, the edition and build, when the instance started (UTC), how
// many user databases it holds and the state of kakapo's event session (ReadHelpers). ServerID, Name,
// Host, ProductName and UptimeDays are left for the caller, which knows the registration and the time.
func InstanceInfo(ctx context.Context, db *sql.DB) (domain.InstanceInfo, error) {
	var i domain.InstanceInfo
	var started sql.NullTime
	err := db.QueryRowContext(ctx, Tag(instanceQuery)).Scan(&i.ServerName, &i.Edition, &i.ProductVersion,
		&i.ProductLevel, &started, &i.DatabaseCount)
	if err != nil {
		return i, friendlyError(err)
	}
	if started.Valid {
		t := started.Time
		// datetime2 has no zone; the query already moved it to UTC.
		i.StartedAt = time.Date(t.Year(), t.Month(), t.Day(), t.Hour(), t.Minute(), t.Second(), t.Nanosecond(), time.UTC)
	}
	// Phase 5e: kakapo's installed helper, in its own read; a failure there never costs the header.
	if hf, err := ReadHelpers(ctx, db); err != nil {
		i.XESession = &domain.XESession{Events: []string{}, Targets: []string{}, ThresholdSeconds: -1, Error: err.Error()}
	} else {
		i.XESession = &hf.Session
	}
	return i, nil
}
