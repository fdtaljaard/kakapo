package collect

import (
	"context"
	"database/sql"
	"strings"

	"sqlmonitor/internal/domain"
)

// The reads behind the room line (docs/phase5l-contract.md parts 2 and 3). SELECTs and
// xp_fixeddrives only.

// volumesQuery is every volume holding a database file of the server, with its total and free
// MB; then xp_fixeddrives (the free MB of each local drive letter), for a folder on a drive that
// holds no database file. A failure of either read leaves its part empty.
const volumesQuery = safeSession + `SET NOCOUNT ON;
DECLARE @v TABLE (name nvarchar(260) PRIMARY KEY, total_mb bigint, free_mb bigint);
BEGIN TRY
    INSERT @v (name, total_mb, free_mb)
    SELECT vs.volume_mount_point, MAX(vs.total_bytes / 1048576), MIN(vs.available_bytes / 1048576)
    FROM sys.master_files AS f
    CROSS APPLY sys.dm_os_volume_stats(f.database_id, f.file_id) AS vs
    GROUP BY vs.volume_mount_point;
END TRY
BEGIN CATCH
END CATCH;
DECLARE @d TABLE (drive nchar(1), free_mb bigint);
BEGIN TRY
    INSERT @d EXEC master.dbo.xp_fixeddrives;
END TRY
BEGIN CATCH
END CATCH;
SELECT name, total_mb, free_mb FROM @v
UNION ALL
SELECT d.drive + N':\', 0, d.free_mb FROM @d AS d
WHERE NOT EXISTS (SELECT 1 FROM @v AS v WHERE v.name = d.drive + N':\');`

// Volumes reads the server's volumes: those holding database files with their total, and the other
// local drives with their free space only (TotalMB 0).
func Volumes(ctx context.Context, db *sql.DB) ([]domain.Volume, error) {
	rows, err := db.QueryContext(ctx, Tag(volumesQuery))
	if err != nil {
		return nil, friendlyError(err)
	}
	defer rows.Close()
	var out []domain.Volume
	for rows.Next() {
		var v domain.Volume
		if err := rows.Scan(&v.Name, &v.TotalMB, &v.FreeMB); err != nil {
			return nil, err
		}
		v.Name = strings.TrimSpace(v.Name)
		out = append(out, v)
	}
	return out, rows.Err()
}

// backupSizesQuery is part 3's msdb read for one database: its last full backup (size and
// compressed size), the server's compression over the compressed fulls of 30 days, this
// database's log backup MB over 7 days and the days that had one; then the used pages of its data
// files, read inside the database.
const backupSizesQuery = safeSession + `SET NOCOUNT ON;
DECLARE @lastMB bigint = 0, @lastComp bigint = 0, @ratio float = 0, @logMB bigint = 0, @logDays int = 0, @used bigint = 0;
SELECT TOP (1) @lastMB = CAST(b.backup_size / 1048576 AS bigint), @lastComp = CAST(ISNULL(b.compressed_backup_size, b.backup_size) / 1048576 AS bigint)
FROM msdb.dbo.backupset AS b
WHERE b.database_name = @db AND b.type = 'D' AND b.is_copy_only = 0
ORDER BY b.backup_finish_date DESC;
SELECT @ratio = ISNULL(SUM(b.compressed_backup_size) / NULLIF(SUM(b.backup_size), 0), 0)
FROM msdb.dbo.backupset AS b
WHERE b.type = 'D' AND b.backup_finish_date >= DATEADD(DAY, -30, GETDATE()) AND b.compressed_backup_size < b.backup_size;
SELECT @logMB = ISNULL(CAST(SUM(ISNULL(b.compressed_backup_size, b.backup_size)) / 1048576 AS bigint), 0),
       @logDays = COUNT(DISTINCT CONVERT(date, b.backup_finish_date))
FROM msdb.dbo.backupset AS b
WHERE b.database_name = @db AND b.type = 'L' AND b.backup_finish_date >= DATEADD(DAY, -7, GETDATE());
IF DATABASEPROPERTYEX(@db, 'Status') = N'ONLINE'
BEGIN TRY
    DECLARE @proc nvarchar(400) = QUOTENAME(@db) + N'.sys.sp_executesql';
    EXEC @proc N'SELECT /* kakapo */ @u = SUM(CAST(FILEPROPERTY(name, ''SpaceUsed'') AS bigint)) / 128 FROM sys.database_files WHERE type = 0;',
        N'@u bigint OUTPUT', @u = @used OUTPUT;
END TRY
BEGIN CATCH
END CATCH;
SELECT @lastMB, @lastComp, @ratio, @logMB, @logDays, ISNULL(@used, 0);`

// BackupSizes reads part 3's backup sizes of one database. A failure is in Error.
func BackupSizes(ctx context.Context, db *sql.DB, database string) domain.BackupSizes {
	var b domain.BackupSizes
	var logMB int64
	err := db.QueryRowContext(ctx, Tag(backupSizesQuery), sql.Named("db", database)).
		Scan(&b.LastFullMB, &b.LastFullCompressedMB, &b.ServerRatio, &logMB, &b.LogDays, &b.UsedDataMB)
	if err != nil {
		b.Error = friendlyError(err).Error()
		return b
	}
	if b.LogDays > 0 {
		b.LogMBPerDay = float64(logMB) / float64(b.LogDays)
	}
	return b
}

// rolePathsQuery is the first data and log file of every database and the error log's path, for
// the room line's roles. SELECT only.
const rolePathsQuery = safeSession + `SET NOCOUNT ON;
SELECT N'@' + CASE f.type WHEN 0 THEN N'data' ELSE N'log' END + N':' + LOWER(DB_NAME(f.database_id)), MIN(f.physical_name)
FROM sys.master_files AS f WHERE f.type IN (0, 1) GROUP BY f.database_id, f.type
UNION ALL
SELECT N'@tempdb', MIN(f.physical_name) FROM sys.master_files AS f WHERE f.database_id = 2 AND f.type = 0
UNION ALL
SELECT N'@errorlog', CAST(SERVERPROPERTY('ErrorLogFileName') AS nvarchar(260));`

// RolePaths reads where the room line's roles are (domain.RoleData and the others), keyed in lower
// case: "@data:sales", "@log:sales", "@tempdb", "@errorlog".
func RolePaths(ctx context.Context, db *sql.DB) (map[string]string, error) {
	rows, err := db.QueryContext(ctx, Tag(rolePathsQuery))
	if err != nil {
		return nil, friendlyError(err)
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var k string
		var p sql.NullString
		if err := rows.Scan(&k, &p); err != nil {
			return nil, err
		}
		out[strings.ToLower(k)] = p.String
	}
	return out, rows.Err()
}
