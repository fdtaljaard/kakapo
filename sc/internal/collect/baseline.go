package collect

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"sqlmonitor/internal/domain"
)

// The settings baseline (docs/phase5w-contract.md, A1): one read, SELECTs only, each area in its own TRY so
// a missing right empties that area and says which right. The global trace flags come from the existing
// trace flag read (ReadTraceFlags). Files are read without sizes or VLFs: they move by themselves.
const baselineQuery = safeSession + `SET NOCOUNT ON;
DECLARE @configError nvarchar(2048), @serverError nvarchar(2048), @memoryError nvarchar(2048), @dbError nvarchar(2048),
        @fileError nvarchar(2048), @jobError nvarchar(2048), @kakapoError nvarchar(2048);
DECLARE @cfg TABLE (name nvarchar(128) NOT NULL, value bigint NULL, value_in_use bigint NULL);
BEGIN TRY
    INSERT @cfg (name, value, value_in_use)
    SELECT CONVERT(nvarchar(128), name), CONVERT(bigint, value), CONVERT(bigint, value_in_use) FROM sys.configurations;
END TRY
BEGIN CATCH
    SET @configError = ERROR_MESSAGE();
END CATCH;

DECLARE @version nvarchar(64), @cu nvarchar(64), @edition nvarchar(128);
BEGIN TRY
    SELECT @version = CAST(SERVERPROPERTY('ProductVersion') AS nvarchar(64)),
           @cu = ISNULL(CAST(SERVERPROPERTY('ProductUpdateLevel') AS nvarchar(64)), CAST(SERVERPROPERTY('ProductLevel') AS nvarchar(64))),
           @edition = CAST(SERVERPROPERTY('Edition') AS nvarchar(128));
END TRY
BEGIN CATCH
    SET @serverError = ERROR_MESSAGE();
END CATCH;

/* sys.dm_os_sys_info and sys.dm_os_process_memory need VIEW SERVER STATE; sql_memory_model_desc is 2016 SP1
   and later, so it is read through sp_executesql only where the column exists. */
DECLARE @start nvarchar(30), @model nvarchar(60), @targetKB bigint, @inUseKB bigint;
IF HAS_PERMS_BY_NAME(NULL, NULL, 'VIEW SERVER STATE') = 0
    SET @memoryError = N'the login does not have VIEW SERVER STATE';
ELSE
BEGIN TRY
    SELECT @start = CONVERT(nvarchar(30), sqlserver_start_time, 126), @targetKB = CONVERT(bigint, committed_target_kb) FROM sys.dm_os_sys_info;
    IF COL_LENGTH('sys.dm_os_sys_info', 'sql_memory_model_desc') IS NOT NULL
        EXEC sys.sp_executesql N'` + KakapoTag + ` SELECT @m = CONVERT(nvarchar(60), sql_memory_model_desc) FROM sys.dm_os_sys_info;',
            N'@m nvarchar(60) OUTPUT', @m = @model OUTPUT;
    SELECT @inUseKB = CONVERT(bigint, physical_memory_in_use_kb) FROM sys.dm_os_process_memory;
END TRY
BEGIN CATCH
    SET @memoryError = ERROR_MESSAGE();
END CATCH;

/* Every database, from master. is_query_store_on is 2016 and later, delayed_durability_desc 2014 and later:
   older versions read NULL. A closed AUTO_CLOSE database reads NULL for some: NULL is not read. */
DECLARE @dbs TABLE (name nvarchar(128) NOT NULL, state nvarchar(60) NULL, user_access nvarchar(60) NULL, read_only bit NULL,
                    recovery nvarchar(60) NULL, compat int NULL, rcsi bit NULL, snapshot nvarchar(60) NULL, auto_create bit NULL,
                    auto_upd bit NULL, auto_upd_async bit NULL, auto_close bit NULL, auto_shrink bit NULL,
                    page_verify nvarchar(60) NULL, query_store bit NULL, delayed nvarchar(60) NULL, target_recovery int NULL);
BEGIN TRY
    DECLARE @dbq nvarchar(max) = N'` + KakapoTag + ` SELECT d.name, d.state_desc, d.user_access_desc, d.is_read_only, d.recovery_model_desc,
        d.compatibility_level, d.is_read_committed_snapshot_on, d.snapshot_isolation_state_desc, d.is_auto_create_stats_on,
        d.is_auto_update_stats_on, d.is_auto_update_stats_async_on, d.is_auto_close_on, d.is_auto_shrink_on, d.page_verify_option_desc, '
        + CASE WHEN COL_LENGTH('sys.databases', 'is_query_store_on') IS NULL THEN N'NULL' ELSE N'd.is_query_store_on' END + N', '
        + CASE WHEN COL_LENGTH('sys.databases', 'delayed_durability_desc') IS NULL THEN N'NULL' ELSE N'd.delayed_durability_desc' END + N', '
        + CASE WHEN COL_LENGTH('sys.databases', 'target_recovery_time_in_seconds') IS NULL THEN N'NULL' ELSE N'd.target_recovery_time_in_seconds' END
        + N' FROM sys.databases AS d;';
    INSERT @dbs EXEC sys.sp_executesql @dbq;
END TRY
BEGIN CATCH
    SET @dbError = ERROR_MESSAGE();
END CATCH;

DECLARE @files TABLE (db nvarchar(128) NULL, name nvarchar(128) NOT NULL, type_desc nvarchar(60) NULL, pct bit NULL, growth bigint NULL, max_size bigint NULL);
BEGIN TRY
    INSERT @files (db, name, type_desc, pct, growth, max_size)
    SELECT DB_NAME(f.database_id), f.name, f.type_desc, f.is_percent_growth, CONVERT(bigint, f.growth), CONVERT(bigint, f.max_size)
    FROM sys.master_files AS f;
END TRY
BEGIN CATCH
    SET @fileError = ERROR_MESSAGE();
END CATCH;

/* Agent jobs (SQLAgentReaderRole or sysadmin): a job takes backups when a step backs up, runs Ola's
   DatabaseBackup or kakapo's maintenance procedure, or its name says backup. */
DECLARE @jobs TABLE (name nvarchar(128) NOT NULL, enabled tinyint NULL, modified nvarchar(30) NULL, version int NULL, schedules int NULL, takes_backup bit NULL);
BEGIN TRY
    INSERT @jobs (name, enabled, modified, version, schedules, takes_backup)
    EXEC sys.sp_executesql N'` + KakapoTag + ` SELECT j.name, j.enabled, CONVERT(nvarchar(30), j.date_modified, 126), j.version_number,
        (SELECT COUNT(*) FROM msdb.dbo.sysjobschedules AS js JOIN msdb.dbo.sysschedules AS s ON s.schedule_id = js.schedule_id
         WHERE js.job_id = j.job_id AND s.enabled = 1),
        CASE WHEN j.name LIKE N''%backup%'' OR EXISTS (SELECT 1 FROM msdb.dbo.sysjobsteps AS st WHERE st.job_id = j.job_id
             AND (st.command LIKE N''%BACKUP DATABASE%'' OR st.command LIKE N''%BACKUP LOG%'' OR st.command LIKE N''%DatabaseBackup%''
                  OR st.command LIKE N''%kakapo[_]Maintenance%'')) THEN 1 ELSE 0 END
        FROM msdb.dbo.sysjobs AS j;';
END TRY
BEGIN CATCH
    SET @jobError = ERROR_MESSAGE();
END CATCH;

DECLARE @changeLog nvarchar(40);
BEGIN TRY
    SET @changeLog = CASE WHEN OBJECT_ID(N'master.dbo.kakapo_ChangeLog', N'U') IS NULL THEN N'absent'
                          WHEN OBJECT_ID(N'master.dbo.kakapo_ChangeLog_AppendOnly', N'TR') IS NULL THEN N'present, its trigger missing'
                          WHEN EXISTS (SELECT 1 FROM master.sys.triggers WHERE name = N'kakapo_ChangeLog_AppendOnly' AND is_disabled = 1)
                               THEN N'present, its trigger disabled'
                          ELSE N'present' END;
END TRY
BEGIN CATCH
    SET @kakapoError = ERROR_MESSAGE();
END CATCH;

SELECT @configError, @serverError, @memoryError, @dbError, @fileError, @jobError, @kakapoError,
       @version, @cu, @edition, @start, @model, @targetKB / 1024, @inUseKB / 1024, @changeLog;
SELECT name, value, value_in_use FROM @cfg ORDER BY name;
SELECT name, state, user_access, read_only, recovery, compat, rcsi, snapshot, auto_create, auto_upd, auto_upd_async,
       auto_close, auto_shrink, page_verify, query_store, delayed, target_recovery FROM @dbs ORDER BY name;
SELECT db, name, type_desc, pct, growth, max_size FROM @files ORDER BY db, name;
SELECT name, enabled, modified, version, schedules, takes_backup FROM @jobs ORDER BY name;`

// ReadBaseline reads the settings baseline of an instance (decision A1). An area that cannot be read is
// left out and named in Errors with the right it needs; the error returned is for a read that could not
// run at all.
func ReadBaseline(ctx context.Context, db *sql.DB) (domain.BaselineFacts, error) {
	f := domain.BaselineFacts{Items: map[string]*string{}, Errors: map[string]string{}}
	rows, err := db.QueryContext(ctx, Tag(baselineQuery))
	if err != nil {
		return f, friendlyError(err)
	}
	defer rows.Close()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return f, friendlyError(err)
		}
		return f, errors.New("SQL Server returned no baseline")
	}
	var cfgE, srvE, memE, dbE, fileE, jobE, kE, version, cu, edition, start, model, changeLog sql.NullString
	var targetMB, inUseMB sql.NullInt64
	if err := rows.Scan(&cfgE, &srvE, &memE, &dbE, &fileE, &jobE, &kE, &version, &cu, &edition, &start, &model, &targetMB, &inUseMB, &changeLog); err != nil {
		return f, fmt.Errorf("read the baseline: %w", err)
	}
	areaErr := func(area string, e sql.NullString, right string) bool {
		if e.Valid && e.String != "" {
			f.Errors[area] = e.String
			if right != "" && !strings.Contains(e.String, right) {
				f.Errors[area] += " (needs " + right + ")"
			}
			return true
		}
		return false
	}
	set := func(key string, v sql.NullString) {
		if v.Valid {
			f.Items[key] = domain.Str(v.String)
		} else {
			f.Items[key] = nil
		}
	}
	if !areaErr(domain.AreaServer, srvE, "") {
		set(domain.ItemVersion, version)
		set(domain.ItemCU, cu)
		set(domain.ItemEdition, edition)
	}
	if !areaErr(domain.AreaMemory, memE, "VIEW SERVER STATE") {
		set(domain.ItemStartTime, start)
		set(domain.ItemMemoryModel, model)
		if targetMB.Valid {
			f.TargetMemoryMB = &targetMB.Int64
		}
		if inUseMB.Valid {
			f.SQLMemoryMB = &inUseMB.Int64
		}
	}
	if !areaErr(domain.AreaKakapo, kE, "") {
		set(domain.ItemChangeLog, changeLog)
	}
	cfgBad := areaErr(domain.AreaConfig, cfgE, "")
	dbBad := areaErr(domain.AreaDatabases, dbE, "")
	fileBad := areaErr(domain.AreaFiles, fileE, "VIEW ANY DEFINITION")
	jobBad := areaErr(domain.AreaJobs, jobE, "SQLAgentReaderRole or sysadmin")

	// Configurations.
	if err := nextResultSet(rows, "configurations"); err != nil {
		return f, err
	}
	for rows.Next() {
		var name string
		var value, inUse sql.NullInt64
		if err := rows.Scan(&name, &value, &inUse); err != nil {
			return f, err
		}
		if cfgBad {
			continue
		}
		f.Items["config/"+name] = intItem(inUse)
		if value.Valid && inUse.Valid && value.Int64 != inUse.Int64 {
			f.Items["config.pending/"+name] = intItem(value)
		}
	}

	// Databases.
	if err := nextResultSet(rows, "databases"); err != nil {
		return f, err
	}
	for rows.Next() {
		var name string
		var state, access, recovery, snapshot, verify, delayed sql.NullString
		var ro, rcsi, ac, au, aua, close, shrink, qs sql.NullBool
		var compat, target sql.NullInt64
		if err := rows.Scan(&name, &state, &access, &ro, &recovery, &compat, &rcsi, &snapshot, &ac, &au, &aua, &close, &shrink, &verify,
			&qs, &delayed, &target); err != nil {
			return f, err
		}
		if dbBad {
			continue
		}
		p := "db/" + name + "/"
		f.Items[p+"present"] = domain.Str("yes")
		set(p+"state", state)
		set(p+"user_access", access)
		f.Items[p+"read_only"] = onOff(ro)
		set(p+"recovery_model", recovery)
		f.Items[p+"compatibility_level"] = intItem(compat)
		f.Items[p+"rcsi"] = onOff(rcsi)
		set(p+"snapshot_isolation", snapshot)
		f.Items[p+"auto_create_stats"] = onOff(ac)
		f.Items[p+"auto_update_stats"] = onOff(au)
		f.Items[p+"auto_update_stats_async"] = onOff(aua)
		f.Items[p+"auto_close"] = onOff(close)
		f.Items[p+"auto_shrink"] = onOff(shrink)
		set(p+"page_verify", verify)
		f.Items[p+"query_store"] = onOff(qs)
		set(p+"delayed_durability", delayed)
		f.Items[p+"target_recovery_time"] = intItem(target)
	}

	// Files, and tempdb's data file count.
	if err := nextResultSet(rows, "files"); err != nil {
		return f, err
	}
	tempdbData := 0
	for rows.Next() {
		var dbName, typ sql.NullString
		var name string
		var pct sql.NullBool
		var growth, maxSize sql.NullInt64
		if err := rows.Scan(&dbName, &name, &typ, &pct, &growth, &maxSize); err != nil {
			return f, err
		}
		if fileBad || !dbName.Valid {
			continue
		}
		p := "file/" + dbName.String + "/" + name + "/"
		f.Items[p+"present"] = domain.Str("yes")
		f.Items[p+"growth"] = growthItem(pct, growth)
		f.Items[p+"max_size"] = maxSizeItem(maxSize)
		if strings.EqualFold(dbName.String, "tempdb") && typ.String == "ROWS" {
			tempdbData++
		}
	}
	if !fileBad {
		f.Items["db/tempdb/data_files"] = domain.Str(strconv.Itoa(tempdbData))
	}

	// Agent jobs.
	if err := nextResultSet(rows, "jobs"); err != nil {
		return f, err
	}
	for rows.Next() {
		var name string
		var modified sql.NullString
		var enabled, version, schedules sql.NullInt64
		var backup sql.NullBool
		if err := rows.Scan(&name, &enabled, &modified, &version, &schedules, &backup); err != nil {
			return f, err
		}
		if jobBad {
			continue
		}
		p := "job/" + name + "/"
		f.Items[p+"present"] = domain.Str("yes")
		f.Items[p+"enabled"] = intItem(enabled)
		set(p+"date_modified", modified)
		f.Items[p+"version"] = intItem(version)
		f.Items[p+"schedules"] = intItem(schedules)
		if backup.Valid {
			f.Items[p+"backup"] = domain.Str(map[bool]string{true: "yes", false: "no"}[backup.Bool])
		}
	}
	if err := friendlyErrOrNil(rows.Err()); err != nil {
		return f, err
	}
	rows.Close()

	// The global trace flags, through the trace flag read.
	tf, err := ReadTraceFlags(ctx, db)
	if err != nil {
		f.Errors[domain.AreaTraceFlag] = err.Error()
	} else {
		f.Items[domain.ItemTraceFlags] = domain.Str(TraceFlagList(tf.Flags))
	}
	return f, nil
}

// TraceFlagList is the global trace flags as one value: "1117, 3226", or "none".
func TraceFlagList(flags []int) string {
	if len(flags) == 0 {
		return "none"
	}
	s := append([]int(nil), flags...)
	sort.Ints(s)
	parts := make([]string, len(s))
	for i, n := range s {
		parts[i] = strconv.Itoa(n)
	}
	return strings.Join(parts, ", ")
}

func intItem(v sql.NullInt64) *string {
	if !v.Valid {
		return nil
	}
	return domain.Str(strconv.FormatInt(v.Int64, 10))
}

func onOff(v sql.NullBool) *string {
	if !v.Valid {
		return nil
	}
	if v.Bool {
		return domain.Str("ON")
	}
	return domain.Str("OFF")
}

// growthItem is a file's growth as people say it: "10%", "64 MB", "none". growth is in 8 KB pages unless
// it is a percentage.
func growthItem(pct sql.NullBool, growth sql.NullInt64) *string {
	if !pct.Valid || !growth.Valid {
		return nil
	}
	switch {
	case growth.Int64 == 0:
		return domain.Str("none")
	case pct.Bool:
		return domain.Str(strconv.FormatInt(growth.Int64, 10) + "%")
	}
	return domain.Str(mbWords(growth.Int64 * 8 / 1024))
}

// maxSizeItem is a file's maximum size: "unlimited", or in MB (max_size is in 8 KB pages; -1 is unlimited,
// and 268435456 is a log's 2 TB "unlimited").
func maxSizeItem(v sql.NullInt64) *string {
	if !v.Valid {
		return nil
	}
	if v.Int64 == -1 || v.Int64 == 268435456 {
		return domain.Str("unlimited")
	}
	return domain.Str(mbWords(v.Int64 * 8 / 1024))
}

func mbWords(mb int64) string { return strconv.FormatInt(mb, 10) + " MB" }

// LastLogBackups reads each database's last log backup from msdb (UTC, by lower-case name), for C3's
// recovery-model rule. A login that cannot read msdb gets an error.
func LastLogBackups(ctx context.Context, db *sql.DB) (map[string]time.Time, error) {
	rows, err := db.QueryContext(ctx, Tag(safeSession+`SELECT b.database_name,
		DATEADD(MINUTE, -DATEPART(TZOFFSET, SYSDATETIMEOFFSET()), MAX(b.backup_finish_date))
		FROM msdb.dbo.backupset AS b WHERE b.type = 'L' GROUP BY b.database_name;`))
	if err != nil {
		return nil, friendlyError(err)
	}
	defer rows.Close()
	out := map[string]time.Time{}
	for rows.Next() {
		var name string
		var at sql.NullTime
		if err := rows.Scan(&name, &at); err != nil {
			return nil, err
		}
		if at.Valid {
			out[strings.ToLower(name)] = naive(at.Time)
		}
	}
	return out, friendlyErrOrNil(rows.Err())
}

// SQLLogin is the login of a connection (SUSER_SNAME()), for kakapo's log (B3).
func SQLLogin(ctx context.Context, db *sql.DB) (string, error) {
	var v sql.NullString
	if err := db.QueryRowContext(ctx, Tag(`SELECT SUSER_SNAME();`)).Scan(&v); err != nil {
		return "", friendlyError(err)
	}
	return v.String, nil
}

// ReadTraceHarvest reads the instance part of the Upkeep read (docs/phase3d-contract.md) as master's, for
// the What changed? screen: the same single read of the default trace that the database page harvests
// (docs/phase5f-contract.md and phase5w A6), without any one database's statistics. SELECTs only.
func ReadTraceHarvest(ctx context.Context, db *sql.DB) (domain.UpkeepFacts, error) {
	f := domain.UpkeepFacts{Database: "master"}
	err := readUpkeepInstance(ctx, db, "master", &f)
	return f, err
}
