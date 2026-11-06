package collect

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"sqlmonitor/internal/domain"
)

// setupInstanceQuery reads what the Setup card needs from the instance, in one batch
// (docs/phase3c-contract.md, "Reads"): the scan's environment block, sys.configurations,
// sys.databases for the database, sys.dm_os_sys_info and the schedulers, instant file
// initialization and the memory model (2016 SP1+), the plan cache sums, Windows memory and the
// resource monitor ring buffer, the power plan from the registry, the msdb restore history and the
// server-wide CLR state, the settings waiting for RECONFIGURE (INS14), the global trace flags (INS15)
// and the database's user access mode (DBC13). Result sets: one row of facts, then the pending
// settings, then the global trace flags. Each area that needs a permission the login may lack is read in its own
// TRY, so a missing permission gives that area's error column, not a failed read. SELECTs only;
// xp_regread only reads the registry.
const setupInstanceQuery = safeSession + `SET NOCOUNT ON;
DECLARE @now datetime = GETDATE();
` + openAutoClose + `
DECLARE @dbid int, @state nvarchar(60), @autoClose bit, @autoShrink bit, @pageVerify nvarchar(60), @createStats bit,
        @updateStats bit, @compat int, @ownerMissing bit, @trv int, @trust bit, @dbColl sysname, @userAccess nvarchar(60);
SELECT @dbid = d.database_id, @state = d.state_desc, @autoClose = d.is_auto_close_on, @autoShrink = d.is_auto_shrink_on,
       @pageVerify = d.page_verify_option_desc, @createStats = d.is_auto_create_stats_on,
       @updateStats = d.is_auto_update_stats_on, @compat = d.compatibility_level,
       @ownerMissing = CASE WHEN SUSER_SNAME(d.owner_sid) IS NULL THEN 1 ELSE 0 END,
       @trv = d.target_recovery_time_in_seconds, @trust = d.is_trustworthy_on, @dbColl = d.collation_name,
       @userAccess = d.user_access_desc
FROM sys.databases AS d WHERE d.name = @db;

DECLARE @vss bit = CASE WHEN HAS_PERMS_BY_NAME(NULL, NULL, 'VIEW SERVER STATE') = 1 THEN 1 ELSE 0 END;
DECLARE @noVss nvarchar(100) = N'the login does not have VIEW SERVER STATE.';

/* CPUs, memory, start time, cores per NUMA node, memory model (INS02-INS05, INS08); the schedulers SQL Server
   cannot use, the affinity, whether it runs in a VM and the processor layout (INS17; socket_count and
   cores_per_socket from 2016 SP2) */
DECLARE @sysError nvarchar(2048), @cpu int, @physMB bigint, @start datetime, @cores int, @memModel nvarchar(60),
        @schedOnline int, @schedOffline int, @runnableNow int, @affType nvarchar(60), @vmType nvarchar(60), @sockets int, @coresPerSocket int;
IF @vss = 0 SET @sysError = @noVss;
ELSE
BEGIN TRY
    SELECT @cpu = cpu_count, @physMB = physical_memory_kb / 1024, @start = sqlserver_start_time,
           @affType = CONVERT(nvarchar(60), affinity_type_desc), @vmType = CONVERT(nvarchar(60), virtual_machine_type_desc)
    FROM sys.dm_os_sys_info;
    SELECT @cores = MAX(n.Schedulers)
    FROM (SELECT parent_node_id, COUNT(*) AS Schedulers
          FROM sys.dm_os_schedulers WHERE status = N'VISIBLE ONLINE' GROUP BY parent_node_id) AS n;
    SELECT @schedOnline  = SUM(CASE WHEN status = N'VISIBLE ONLINE' THEN 1 ELSE 0 END),
           @schedOffline = SUM(CASE WHEN status = N'VISIBLE OFFLINE' THEN 1 ELSE 0 END),
           @runnableNow  = SUM(CASE WHEN status = N'VISIBLE ONLINE' THEN runnable_tasks_count ELSE 0 END)
    FROM sys.dm_os_schedulers;
    IF EXISTS (SELECT 1 FROM sys.all_columns WHERE object_id = OBJECT_ID(N'sys.dm_os_sys_info') AND name = N'socket_count')
        EXEC sys.sp_executesql N'/* kakapo */ SELECT @s = socket_count, @c = cores_per_socket FROM sys.dm_os_sys_info;',
            N'@s int OUTPUT, @c int OUTPUT', @s = @sockets OUTPUT, @c = @coresPerSocket OUTPUT;
    IF EXISTS (SELECT 1 FROM sys.all_columns WHERE object_id = OBJECT_ID(N'sys.dm_os_sys_info') AND name = N'sql_memory_model_desc')
        EXEC sys.sp_executesql N'/* kakapo */ SELECT @m = sql_memory_model_desc FROM sys.dm_os_sys_info;',
            N'@m nvarchar(60) OUTPUT', @m = @memModel OUTPUT;
END TRY
BEGIN CATCH
    SET @sysError = ERROR_MESSAGE();
END CATCH;

/* Instant file initialization (INS07): the column exists from 2016 SP1 */
DECLARE @ifi nvarchar(1);
IF @vss = 1
BEGIN TRY
    IF EXISTS (SELECT 1 FROM sys.all_columns WHERE object_id = OBJECT_ID(N'sys.dm_server_services')
                                               AND name = N'instant_file_initialization_enabled')
        EXEC sys.sp_executesql
            N'SELECT TOP (1) @ifi = instant_file_initialization_enabled FROM sys.dm_server_services WHERE servicename LIKE N''SQL Server (%'';',
            N'@ifi nvarchar(1) OUTPUT', @ifi = @ifi OUTPUT;
END TRY
BEGIN CATCH
    SET @ifi = NULL;
END CATCH;

/* Server configuration, and the settings waiting for RECONFIGURE (INS14) */
DECLARE @cfgError nvarchar(2048), @maxMem bigint, @maxDop int, @cost int, @adhoc int, @boost int, @bkc int, @ff int,
        @clr int, @clrStrict int, @minMem bigint, @lwp int, @aff bigint, @aff64 bigint, @affIO bigint, @aff64IO bigint;
DECLARE @pending TABLE (name nvarchar(128) NOT NULL, value bigint NULL, value_in_use bigint NULL);
BEGIN TRY
    SELECT @maxMem    = MAX(CASE WHEN name = N'max server memory (MB)'         THEN CONVERT(bigint, value_in_use) END),
           @maxDop    = MAX(CASE WHEN name = N'max degree of parallelism'      THEN CONVERT(int, value_in_use) END),
           @cost      = MAX(CASE WHEN name = N'cost threshold for parallelism' THEN CONVERT(int, value_in_use) END),
           @adhoc     = MAX(CASE WHEN name = N'optimize for ad hoc workloads'  THEN CONVERT(int, value_in_use) END),
           @boost     = MAX(CASE WHEN name = N'priority boost'                 THEN CONVERT(int, value_in_use) END),
           @bkc       = MAX(CASE WHEN name = N'backup compression default'     THEN CONVERT(int, value_in_use) END),
           @ff        = MAX(CASE WHEN name = N'fill factor (%)'                THEN CONVERT(int, value_in_use) END),
           @clr       = MAX(CASE WHEN name = N'clr enabled'                    THEN CONVERT(int, value_in_use) END),
           @clrStrict = MAX(CASE WHEN name = N'clr strict security'            THEN CONVERT(int, value_in_use) END),
           @minMem    = MAX(CASE WHEN name = N'min server memory (MB)'         THEN CONVERT(bigint, value_in_use) END),
           @lwp       = MAX(CASE WHEN name = N'lightweight pooling'            THEN CONVERT(int, value_in_use) END),
           @aff       = MAX(CASE WHEN name = N'affinity mask'                  THEN CONVERT(bigint, value_in_use) END),
           @aff64     = MAX(CASE WHEN name = N'affinity64 mask'                THEN CONVERT(bigint, value_in_use) END),
           @affIO     = MAX(CASE WHEN name = N'affinity I/O mask'              THEN CONVERT(bigint, value_in_use) END),
           @aff64IO   = MAX(CASE WHEN name = N'affinity64 I/O mask'            THEN CONVERT(bigint, value_in_use) END)
    FROM sys.configurations;
    INSERT @pending (name, value, value_in_use)
    SELECT name, CONVERT(bigint, value), CONVERT(bigint, value_in_use) FROM sys.configurations WHERE value <> value_in_use;
END TRY
BEGIN CATCH
    SET @cfgError = ERROR_MESSAGE();
END CATCH;

/* Plan cache (INS06) */
DECLARE @pcError nvarchar(2048), @pcMB decimal(18, 1), @suMB decimal(18, 1);
IF @vss = 0 SET @pcError = @noVss;
ELSE
BEGIN TRY
    SELECT @pcMB = ISNULL(SUM(CONVERT(bigint, size_in_bytes)), 0) / 1048576.0,
           @suMB = ISNULL(SUM(CASE WHEN usecounts = 1 AND objtype IN (N'Adhoc', N'Prepared')
                                   THEN CONVERT(bigint, size_in_bytes) ELSE 0 END), 0) / 1048576.0
    FROM sys.dm_exec_cached_plans;
END TRY
BEGIN CATCH
    SET @pcError = ERROR_MESSAGE();
END CATCH;

/* Windows low on memory (INS12): system-wide signals only, as the scan */
DECLARE @memError nvarchar(2048), @memState nvarchar(60), @availMB bigint, @lowLast datetime, @low24 int, @low7 int;
IF @vss = 0 SET @memError = @noVss;
ELSE
BEGIN TRY
    SELECT @memState = system_memory_state_desc, @availMB = available_physical_memory_kb / 1024 FROM sys.dm_os_sys_memory;
    SELECT @low24 = ISNULL(SUM(CASE WHEN x.EventTime >= DATEADD(HOUR, -24, GETDATE()) THEN 1 ELSE 0 END), 0),
           @low7  = COUNT(*), @lowLast = MAX(x.EventTime)
    FROM (SELECT DATEADD(SECOND, -(si.ms_ticks - r.timestamp) / 1000, GETDATE()) AS EventTime
          FROM sys.dm_os_ring_buffers AS r CROSS JOIN sys.dm_os_sys_info AS si
          WHERE r.ring_buffer_type = N'RING_BUFFER_RESOURCE_MONITOR'
            AND r.record LIKE N'%RESOURCE_MEMPHYSICAL_LOW%'
            AND r.record NOT LIKE N'%<IndicatorsSystem>0</IndicatorsSystem>%') AS x
    WHERE x.EventTime >= DATEADD(DAY, -7, GETDATE());
END TRY
BEGIN CATCH
    SET @memError = ERROR_MESSAGE();
END CATCH;

/* Windows power plan (INS13): a registry read, which needs sysadmin; in its own TRY */
DECLARE @power nvarchar(64), @powerError nvarchar(2048);
BEGIN TRY
    EXEC master.dbo.xp_regread N'HKEY_LOCAL_MACHINE', N'SYSTEM\CurrentControlSet\Control\Power\User\PowerSchemes',
         N'ActivePowerScheme', @power OUTPUT, N'no_output';
END TRY
BEGIN CATCH
    SET @powerError = ERROR_MESSAGE();
END CATCH;

/* Global trace flags (INS15): DBCC TRACESTATUS needs sysadmin on older versions; in its own TRY */
DECLARE @tf TABLE (trace_flag int NOT NULL, [status] int NULL, [global] int NULL, [session] int NULL);
DECLARE @tfError nvarchar(2048);
BEGIN TRY
    INSERT @tf (trace_flag, [status], [global], [session])
    EXEC (N'DBCC TRACESTATUS(-1) WITH NO_INFOMSGS');
END TRY
BEGIN CATCH
    SET @tfError = ERROR_MESSAGE();
END CATCH;

/* Where the database came from (CPY01): msdb restore history */
DECLARE @restoreError nvarchar(2048), @restoredAt datetime, @srcServer nvarchar(128), @srcDb nvarchar(128), @srcBackupAt datetime;
BEGIN TRY
    EXEC sys.sp_executesql N'/* kakapo */ SELECT TOP (1) @r = rh.restore_date, @s = bs.server_name, @d = bs.database_name, @b = bs.backup_finish_date
        FROM msdb.dbo.restorehistory AS rh
        JOIN msdb.dbo.backupset AS bs ON bs.backup_set_id = rh.backup_set_id
        WHERE rh.destination_database_name = @db
        ORDER BY rh.restore_date DESC;',
        N'@db sysname, @r datetime OUTPUT, @s nvarchar(128) OUTPUT, @d nvarchar(128) OUTPUT, @b datetime OUTPUT',
        @db = @db, @r = @restoredAt OUTPUT, @s = @srcServer OUTPUT, @d = @srcDb OUTPUT, @b = @srcBackupAt OUTPUT;
END TRY
BEGIN CATCH
    SET @restoreError = ERROR_MESSAGE();
END CATCH;

/* Server-wide CLR state (CPY06): trusted assemblies (2017+) and whether this database's CLR code has run */
DECLARE @trusted int = 0, @clrLoaded bit = 0, @clrError nvarchar(2048);
BEGIN TRY
    IF OBJECT_ID(N'sys.trusted_assemblies') IS NOT NULL
        EXEC sys.sp_executesql N'/* kakapo */ SELECT @n = COUNT(*) FROM sys.trusted_assemblies;', N'@n int OUTPUT', @n = @trusted OUTPUT;
    IF @vss = 1 AND EXISTS (SELECT 1
                            FROM sys.dm_clr_loaded_assemblies AS la
                            JOIN sys.dm_clr_appdomains AS ad ON ad.appdomain_address = la.appdomain_address
                            WHERE ad.db_id = @dbid)
        SET @clrLoaded = 1;
END TRY
BEGIN CATCH
    SET @clrError = ERROR_MESSAGE();
END CATCH;

SELECT CAST(CASE WHEN @dbid IS NULL THEN 0 ELSE 1 END AS bit),
       CAST(ISNULL(@@SERVERNAME, N'') AS nvarchar(256)),
       CAST(SERVERPROPERTY('ProductVersion') AS nvarchar(64)),
       CAST(SERVERPROPERTY('ProductLevel') AS nvarchar(64)),
       CAST(SERVERPROPERTY('ProductUpdateLevel') AS nvarchar(64)),
       CAST(SERVERPROPERTY('Edition') AS nvarchar(128)),
       CONVERT(int, SERVERPROPERTY('EngineEdition')),
       CAST(SERVERPROPERTY('Collation') AS nvarchar(128)),
       @state, @autoClose, @autoShrink, @pageVerify, @createStats, @updateStats, @compat, @ownerMissing, @trv, @trust, @dbColl,
       @userAccess,
       @sysError, @cpu, @physMB, @start, @cores, @memModel, @ifi,
       @schedOnline, @schedOffline, @runnableNow, @affType, @vmType, @sockets, @coresPerSocket,
       @cfgError, @maxMem, @maxDop, @cost, @adhoc, @boost, @bkc, @ff, @clr, @clrStrict,
       @minMem, @lwp, @aff, @aff64, @affIO, @aff64IO,
       @pcError, @pcMB, @suMB,
       @memError, @memState, @availMB, @lowLast, @low24, @low7,
       @power, @powerError,
       @restoreError, @restoredAt, @srcServer, @srcDb, @srcBackupAt,
       @trusted, @clrLoaded, @clrError,
       @tfError,
       @now, DATEPART(TZOFFSET, SYSDATETIMEOFFSET());
SELECT name, value, value_in_use FROM @pending ORDER BY name;
SELECT trace_flag FROM @tf WHERE [global] = 1 ORDER BY trace_flag;`

// openAutoClose opens an AUTO_CLOSE database that is closed, with a statement that reads nothing,
// as the Backups card does: a closed database shows NULL for some sys.databases columns
// (target_recovery_time_in_seconds, collation_name). It expects @db.
const openAutoClose = `IF EXISTS (SELECT 1 FROM sys.databases WHERE name = @db AND is_auto_close_on = 1 AND state = 0)
BEGIN TRY
    DECLARE @open nvarchar(400) = N'EXEC ' + QUOTENAME(@db) + N'.sys.sp_executesql N''/* kakapo */ DECLARE @opened int = 1;'';';
    EXEC sp_executesql @open;
END TRY
BEGIN CATCH
END CATCH;`

// setupDatabaseQuery runs inside the database (EXEC [db].sys.sp_executesql): the legacy
// cardinality estimator (2016+), the orphaned SQL users, the user assemblies, the collation of the
// databases this database's code reads from and of the text columns, and the first data file's
// name, and every database scoped configuration (DBC14), each in its own TRY. Result sets: one row
// of facts, then the orphaned users, then the scoped configurations.
const setupDatabaseQuery = `SET NOCOUNT ON;
DECLARE @ceExists bit = CASE WHEN OBJECT_ID(N'sys.database_scoped_configurations') IS NULL THEN 0 ELSE 1 END;
DECLARE @ceError nvarchar(2048), @legacyCe int, @orphError nvarchar(2048), @clrError nvarchar(2048), @asm int, @safe int,
        @collError nvarchar(2048), @sameDb sysname, @otherCt int, @dataFile sysname;
DECLARE @dbColl sysname = CONVERT(sysname, DATABASEPROPERTYEX(DB_NAME(), 'Collation'));
DECLARE @orph TABLE (name sysname NOT NULL, default_schema sysname NULL, login_exists bit NOT NULL);
DECLARE @dsc TABLE (configuration_id int NULL, name nvarchar(128) NOT NULL, value nvarchar(400) NULL);
DECLARE @dscError nvarchar(2048);

IF @ceExists = 1
BEGIN TRY
    EXEC sys.sp_executesql
        N'SELECT @v = CONVERT(int, value) FROM sys.database_scoped_configurations WHERE name = N''LEGACY_CARDINALITY_ESTIMATION'';',
        N'@v int OUTPUT', @v = @legacyCe OUTPUT;
END TRY
BEGIN CATCH
    SET @ceError = ERROR_MESSAGE();
END CATCH;

/* Every database scoped configuration (DBC14), 2016+ */
IF @ceExists = 1
BEGIN TRY
    INSERT @dsc (configuration_id, name, value)
    EXEC sys.sp_executesql N'/* kakapo */ SELECT configuration_id, name, CONVERT(nvarchar(400), value) FROM sys.database_scoped_configurations;';
END TRY
BEGIN CATCH
    SET @dscError = ERROR_MESSAGE();
END CATCH;

BEGIN TRY
    INSERT @orph (name, default_schema, login_exists)
    SELECT dp.name, dp.default_schema_name, CASE WHEN sp.name IS NULL THEN 0 ELSE 1 END
    FROM sys.database_principals AS dp
    LEFT JOIN sys.server_principals AS sid ON sid.sid = dp.sid
    LEFT JOIN sys.server_principals AS sp ON sp.name = dp.name COLLATE DATABASE_DEFAULT AND sp.type = 'S'
    WHERE dp.type = 'S' AND dp.authentication_type = 1 AND dp.principal_id > 4 AND sid.sid IS NULL;
END TRY
BEGIN CATCH
    SET @orphError = ERROR_MESSAGE();
END CATCH;

BEGIN TRY
    SELECT @asm = COUNT(*), @safe = ISNULL(SUM(CASE WHEN permission_set_desc = N'SAFE_ACCESS' THEN 1 ELSE 0 END), 0)
    FROM sys.assemblies WHERE is_user_defined = 1;
END TRY
BEGIN CATCH
    SET @clrError = ERROR_MESSAGE();
END CATCH;

BEGIN TRY
    SELECT TOP (1) @sameDb = d.name
    FROM (SELECT DISTINCT referenced_database_name AS name
          FROM sys.sql_expression_dependencies WHERE referenced_database_name IS NOT NULL) AS r
    JOIN sys.databases AS d ON d.name COLLATE DATABASE_DEFAULT = r.name
    WHERE d.database_id <> DB_ID() AND d.collation_name COLLATE DATABASE_DEFAULT = @dbColl
    ORDER BY d.name;
    SELECT @otherCt = COUNT(*)
    FROM sys.columns AS c
    JOIN sys.objects AS o ON o.object_id = c.object_id AND o.is_ms_shipped = 0 AND o.type = 'U'
    WHERE c.collation_name IS NOT NULL AND c.collation_name <> @dbColl;
END TRY
BEGIN CATCH
    SET @collError = ERROR_MESSAGE();
END CATCH;

SELECT TOP (1) @dataFile = name FROM sys.database_files WHERE type = 0 ORDER BY file_id;

SELECT @ceExists, @legacyCe, @ceError, @orphError, @asm, @safe, @clrError, @sameDb, ISNULL(@otherCt, 0), @collError, @dataFile, @dscError;
SELECT name, default_schema, login_exists FROM @orph ORDER BY name;
SELECT name, value FROM @dsc ORDER BY configuration_id, name;`

// SetupFacts reads what the Setup card needs about one database and its instance
// (docs/phase3c-contract.md), SELECTs only, in two batches: the instance, then inside the database
// when it is online. An area the login may not read carries its reason in the facts instead of
// failing the read; the error returned is for a read that could not run at all, or a database
// that does not exist. DB (the database as listed, with its Query Store) is left for the caller.
func SetupFacts(ctx context.Context, db *sql.DB, database string) (domain.SetupFacts, error) {
	f := domain.SetupFacts{Database: database}
	if err := readSetupInstance(ctx, db, database, &f); err != nil {
		return f, err
	}
	if f.State == "ONLINE" {
		if err := readSetupDatabase(ctx, db, database, &f); err != nil {
			msg := err.Error()
			f.LegacyCEError, f.OrphansError, f.CollationError, f.ScopedConfigsError = msg, msg, msg, msg
			if f.ClrError == "" {
				f.ClrError = msg
			}
		}
	}
	return f, nil
}

func readSetupInstance(ctx context.Context, db *sql.DB, database string, f *domain.SetupFacts) error {
	rows, err := db.QueryContext(ctx, Tag(setupInstanceQuery), sql.Named("db", database))
	if err != nil {
		return friendlyError(err)
	}
	defer rows.Close()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return friendlyError(err)
		}
		return errors.New("SQL Server returned no setup facts")
	}
	var (
		found                                                      bool
		update, state, pageVerify, dbColl                          sql.NullString
		autoClose, autoShrink, createStats, updateStats, ownerMiss sql.NullBool
		trust, clrLoaded                                           sql.NullBool
		userAccess, tfErr                                          sql.NullString
		lwp                                                        sql.NullInt32
		minMem, aff, aff64, affIO, aff64IO                         sql.NullInt64
		compat, trv                                                sql.NullInt32
		sysErr, memModel, ifi, cfgErr, pcErr, memErr, memState     sql.NullString
		power, powerErr, restoreErr, srcServer, srcDb, clrErr      sql.NullString
		cpu, cores, maxDop, cost, adhoc, boost, bkc, ff, clr, low7 sql.NullInt32
		clrStrict, low24, trusted                                  sql.NullInt32
		physMB, maxMem, availMB                                    sql.NullInt64
		pcMB, suMB                                                 sql.NullFloat64
		start, lowLast, restoredAt, srcBackupAt                    sql.NullTime
		schedOnline, schedOffline, runnableNow, sockets, coresPS   sql.NullInt32
		affType, vmType                                            sql.NullString
	)
	if err := rows.Scan(&found, &f.ServerName, &f.ProductVersion, &f.ProductLevel, &update, &f.Edition, &f.EngineEdition, &f.Collation,
		&state, &autoClose, &autoShrink, &pageVerify, &createStats, &updateStats, &compat, &ownerMiss, &trv, &trust, &dbColl,
		&userAccess,
		&sysErr, &cpu, &physMB, &start, &cores, &memModel, &ifi,
		&schedOnline, &schedOffline, &runnableNow, &affType, &vmType, &sockets, &coresPS,
		&cfgErr, &maxMem, &maxDop, &cost, &adhoc, &boost, &bkc, &ff, &clr, &clrStrict,
		&minMem, &lwp, &aff, &aff64, &affIO, &aff64IO,
		&pcErr, &pcMB, &suMB,
		&memErr, &memState, &availMB, &lowLast, &low24, &low7,
		&power, &powerErr,
		&restoreErr, &restoredAt, &srcServer, &srcDb, &srcBackupAt,
		&trusted, &clrLoaded, &clrErr,
		&tfErr,
		&f.ServerNow, &f.UTCOffsetMinutes); err != nil {
		return fmt.Errorf("read the setup facts of %s: %w", database, err)
	}
	if !found {
		return fmt.Errorf("there is no database named %q", database)
	}
	f.ServerNow = naive(f.ServerNow)
	f.UpdateLevel = update.String

	f.State, f.PageVerify, f.DbCollation = state.String, pageVerify.String, dbColl.String
	f.UserAccess = userAccess.String
	f.AutoClose, f.AutoShrink = autoClose.Bool, autoShrink.Bool
	f.AutoCreateStats, f.AutoUpdateStats = createStats.Bool, updateStats.Bool
	f.CompatLevel, f.TargetRecoverySec = int(compat.Int32), int(trv.Int32)
	if !trv.Valid {
		f.TargetRecoverySec = -1 // not shown (a closed AUTO_CLOSE database): unknown, so DBC09 does not fire
	}
	f.OwnerMissing, f.Trustworthy = ownerMiss.Bool, trust.Bool

	f.SysInfoError = sysErr.String
	f.CPUCount, f.PhysicalMB, f.CoresPerNode = int(cpu.Int32), physMB.Int64, int(cores.Int32)
	f.OnlineSchedulers, f.OfflineSchedulers, f.RunnableNow = int(schedOnline.Int32), int(schedOffline.Int32), int(runnableNow.Int32)
	f.AffinityType, f.VirtualMachine = affType.String, vmType.Valid && vmType.String != "NONE"
	if sockets.Valid && coresPS.Valid {
		f.Sockets, f.CoresPerSocket = int(sockets.Int32), int(coresPS.Int32)
	}
	if start.Valid {
		f.StartTime = naive(start.Time)
	}
	if memModel.Valid {
		f.MemoryModel = &memModel.String
	}
	if ifi.Valid {
		v := strings.ToUpper(ifi.String)
		f.IFI = &v
	}

	f.ConfigError = cfgErr.String
	f.MaxServerMemoryMB, f.MaxDop, f.CostThreshold = maxMem.Int64, int(maxDop.Int32), int(cost.Int32)
	f.OptimizeAdHoc, f.PriorityBoost, f.BackupCompression = int(adhoc.Int32), int(boost.Int32), int(bkc.Int32)
	f.FillFactor, f.ClrEnabled = int(ff.Int32), int(clr.Int32)
	if clrStrict.Valid {
		v := int(clrStrict.Int32)
		f.ClrStrict = &v
	}
	f.MinServerMemoryMB, f.LightweightPooling = minMem.Int64, int(lwp.Int32)
	f.AffinityMask, f.Affinity64Mask, f.AffinityIOMask, f.Affinity64IOMask = aff.Int64, aff64.Int64, affIO.Int64, aff64IO.Int64
	if f.ConfigError == "" && !maxMem.Valid {
		f.ConfigError = "sys.configurations returned no max server memory."
	}

	f.PlanCacheError, f.PlanCacheMB, f.SingleUseMB = pcErr.String, pcMB.Float64, suMB.Float64
	f.MemoryError, f.MemoryState, f.AvailableMB = memErr.String, memState.String, availMB.Int64
	f.Low24, f.Low7, f.LowLast = int(low24.Int32), int(low7.Int32), naivePtr(lowLast)

	f.PowerError = powerErr.String
	if power.Valid && power.String != "" {
		f.PowerScheme = &power.String
	}

	f.RestoreError = restoreErr.String
	f.RestoredAt, f.SourceBackupAt = naivePtr(restoredAt), naivePtr(srcBackupAt)
	f.SourceServer, f.SourceDb = srcServer.String, srcDb.String

	f.Trusted, f.ClrLoaded, f.ClrError = int(trusted.Int32), clrLoaded.Bool, clrErr.String
	f.TraceFlagError = tfErr.String

	if err := nextResultSet(rows, "pending configuration"); err != nil {
		return err
	}
	f.PendingConfig = []domain.PendingConfig{}
	for rows.Next() {
		var p domain.PendingConfig
		var value, inUse sql.NullInt64
		if err := rows.Scan(&p.Name, &value, &inUse); err != nil {
			return err
		}
		p.Value, p.ValueInUse = value.Int64, inUse.Int64
		f.PendingConfig = append(f.PendingConfig, p)
	}
	if err := nextResultSet(rows, "trace flags"); err != nil {
		return err
	}
	if f.TraceFlagError == "" {
		f.TraceFlags = []int{}
	}
	for rows.Next() {
		var n int
		if err := rows.Scan(&n); err != nil {
			return err
		}
		f.TraceFlags = append(f.TraceFlags, n)
	}
	return friendlyErrOrNil(rows.Err())
}

func readSetupDatabase(ctx context.Context, db *sql.DB, database string, f *domain.SetupFacts) error {
	q := Tag(safeSession + "EXEC " + domain.QuoteName(database) + ".sys.sp_executesql @stmt;")
	rows, err := db.QueryContext(ctx, q, sql.Named("stmt", TagStatements(setupDatabaseQuery)))
	if err != nil {
		return friendlyError(err)
	}
	defer rows.Close()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return friendlyError(err)
		}
		return errors.New("SQL Server returned nothing from inside the database")
	}
	var (
		ceExists                                    bool
		legacyCe, asm, safe                         sql.NullInt32
		ceErr, orphErr, clrErr, sameDb, collErr, df sql.NullString
		dscErr                                      sql.NullString
	)
	if err := rows.Scan(&ceExists, &legacyCe, &ceErr, &orphErr, &asm, &safe, &clrErr, &sameDb, &f.OtherCollationCt, &collErr, &df, &dscErr); err != nil {
		return err
	}
	if ceExists && ceErr.String == "" {
		v := int(legacyCe.Int32)
		f.LegacyCE = &v
	}
	f.LegacyCEError, f.OrphansError, f.CollationError = ceErr.String, orphErr.String, collErr.String
	if clrErr.String != "" {
		f.ClrError = clrErr.String
	}
	f.Assemblies, f.SafeAssemblies = int(asm.Int32), int(safe.Int32)
	f.SameCollationDb, f.DataFileName = sameDb.String, df.String

	if err := nextResultSet(rows, "orphaned users"); err != nil {
		return err
	}
	f.Orphans = []domain.OrphanUser{}
	for rows.Next() {
		var u domain.OrphanUser
		var schema sql.NullString
		if err := rows.Scan(&u.Name, &schema, &u.LoginExists); err != nil {
			return err
		}
		u.DefaultSchema = schema.String
		f.Orphans = append(f.Orphans, u)
	}

	if err := nextResultSet(rows, "database scoped configurations"); err != nil {
		return err
	}
	f.ScopedConfigsError = dscErr.String
	if ceExists && f.ScopedConfigsError == "" {
		f.ScopedConfigs = []domain.ScopedConfig{}
	}
	for rows.Next() {
		var c domain.ScopedConfig
		var v sql.NullString
		if err := rows.Scan(&c.Name, &v); err != nil {
			return err
		}
		c.Value = v.String
		f.ScopedConfigs = append(f.ScopedConfigs, c)
	}
	return friendlyErrOrNil(rows.Err())
}

// ReadSetting reads back the value a fix changes, as the fix shows it (domain.ConfigValue or
// domain.DatabaseValue): a server configuration's value_in_use, or the database options in
// sys.databases. SELECTs only. The query-store fix is read back by its own phase 1 path.
func ReadSetting(ctx context.Context, db *sql.DB, fixID, database string) (string, error) {
	def, ok := domain.FindFixDef(fixID)
	if !ok || fixID == domain.FixQueryStore {
		return "", fmt.Errorf("no setting to read for fix %q", fixID)
	}
	if def.Scope == domain.ScopeServer {
		v, err := ReadConfiguration(ctx, db, def.Setting)
		if err != nil {
			return "", err
		}
		return domain.ConfigValue(fixID, v), nil
	}
	o, err := ReadDatabaseOptions(ctx, db, database)
	if err != nil {
		return "", err
	}
	return domain.DatabaseValue(fixID, o), nil
}

// ReadConfiguration reads one server configuration's value_in_use.
func ReadConfiguration(ctx context.Context, db *sql.DB, name string) (int64, error) {
	var v sql.NullInt64
	err := db.QueryRowContext(ctx, Tag(safeSession+`SELECT CONVERT(bigint, value_in_use) FROM sys.configurations WHERE name = @name`),
		sql.Named("name", name)).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && !v.Valid) {
		return 0, fmt.Errorf("SQL Server has no configuration named %q", name)
	}
	if err != nil {
		return 0, friendlyError(err)
	}
	return v.Int64, nil
}

// ReadDatabaseOptions reads the database options the database fixes change.
func ReadDatabaseOptions(ctx context.Context, db *sql.DB, database string) (domain.DatabaseOptions, error) {
	var o domain.DatabaseOptions
	err := db.QueryRowContext(ctx, Tag(safeSession+"SET NOCOUNT ON;\n"+openAutoClose+`
		SELECT is_auto_close_on, is_auto_shrink_on, CAST(ISNULL(page_verify_option_desc, N'') AS nvarchar(60)),
		       is_auto_create_stats_on, is_auto_update_stats_on, ISNULL(target_recovery_time_in_seconds, -1)
		FROM sys.databases WHERE name = @db`), sql.Named("db", database)).Scan(
		&o.AutoClose, &o.AutoShrink, &o.PageVerify, &o.AutoCreateStats, &o.AutoUpdateStats, &o.TargetRecoverySec)
	if errors.Is(err, sql.ErrNoRows) {
		return o, fmt.Errorf("there is no database named %q", database)
	}
	if err != nil {
		return o, friendlyError(err)
	}
	return o, nil
}
