package collect

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"sqlmonitor/internal/domain"
)

// storageFileStateQuery reads what the storage fixes (docs/phase4d-contract.md, part 2) need besides
// the storage facts, and what they read back: the engine version, instant file initialization (INS07,
// the column exists from 2016 SP1; it needs VIEW SERVER STATE) and the files of this database
// (sys.master_files) and of tempdb (tempdb.sys.database_files, its current sizes, with its startup
// sizes from sys.master_files for the tempdb-files fix), each area in its own TRY with its error
// column. SELECTs only.
// The first result set is one row (version, IFI, IFI error, files error), the second the files,
// this database's first.
const storageFileStateQuery = safeSession + `SET NOCOUNT ON;
DECLARE @dbid int = DB_ID(@db);
DECLARE @vss bit = CASE WHEN HAS_PERMS_BY_NAME(NULL, NULL, 'VIEW SERVER STATE') = 1 THEN 1 ELSE 0 END;
DECLARE @ifi nvarchar(1), @ifiError nvarchar(2048), @filesError nvarchar(2048);
DECLARE @files TABLE (
    database_id int NOT NULL, file_id int NOT NULL, type_desc nvarchar(60) NOT NULL, name sysname NOT NULL,
    physical_name nvarchar(260) NOT NULL, size_mb bigint NOT NULL, growth bigint NOT NULL, is_percent_growth bit NOT NULL,
    max_size bigint NOT NULL, start_mb bigint NULL);

/* Instant file initialization (INS07) */
IF @vss = 0 SET @ifiError = N'the login does not have VIEW SERVER STATE.';
ELSE
BEGIN TRY
    IF EXISTS (SELECT 1 FROM sys.all_columns WHERE object_id = OBJECT_ID(N'sys.dm_server_services')
                                               AND name = N'instant_file_initialization_enabled')
        EXEC sys.sp_executesql
            N'/* kakapo */ SELECT TOP (1) @ifi = instant_file_initialization_enabled FROM sys.dm_server_services WHERE servicename LIKE N''SQL Server (%'';',
            N'@ifi nvarchar(1) OUTPUT', @ifi = @ifi OUTPUT;
    ELSE
        SET @ifiError = N'this version does not report it (before SQL Server 2016 SP1).';
END TRY
BEGIN CATCH
    SET @ifiError = ERROR_MESSAGE();
END CATCH;

/* The files: this database's from sys.master_files, tempdb's current ones */
BEGIN TRY
    INSERT @files (database_id, file_id, type_desc, name, physical_name, size_mb, growth, is_percent_growth, max_size)
    SELECT mf.database_id, mf.file_id, mf.type_desc, mf.name, mf.physical_name, CONVERT(bigint, mf.size) * 8 / 1024,
           mf.growth, mf.is_percent_growth, mf.max_size
    FROM sys.master_files AS mf
    WHERE mf.database_id = @dbid AND @dbid <> 2 AND mf.type IN (0, 1);
    INSERT @files (database_id, file_id, type_desc, name, physical_name, size_mb, growth, is_percent_growth, max_size, start_mb)
    SELECT 2, df.file_id, df.type_desc, df.name, df.physical_name, CONVERT(bigint, df.size) * 8 / 1024,
           df.growth, df.is_percent_growth, df.max_size, CONVERT(bigint, smf.size) * 8 / 1024
    FROM tempdb.sys.database_files AS df
    LEFT JOIN sys.master_files AS smf ON smf.database_id = 2 AND smf.file_id = df.file_id
    WHERE df.type IN (0, 1);
END TRY
BEGIN CATCH
    SET @filesError = ERROR_MESSAGE();
END CATCH;

SELECT CAST(SERVERPROPERTY('ProductVersion') AS nvarchar(64)), @ifi, @ifiError, @filesError;
SELECT database_id, file_id, type_desc, name, physical_name, size_mb, growth, is_percent_growth, max_size, start_mb
FROM @files
ORDER BY CASE WHEN database_id = 2 THEN 1 ELSE 0 END, file_id;`

// StorageFileState reads the files of a database and of tempdb as they are now, with instant file
// initialization and the engine version: the storage fixes' extra read and their read-back. A
// files area that cannot be read is an error; IFI that cannot be read is in IFIError.
func StorageFileState(ctx context.Context, db *sql.DB, database string) (domain.StorageFileState, error) {
	var st domain.StorageFileState
	rows, err := db.QueryContext(ctx, Tag(storageFileStateQuery), sql.Named("db", database))
	if err != nil {
		return st, friendlyError(err)
	}
	defer rows.Close()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return st, friendlyError(err)
		}
		return st, errors.New("SQL Server returned no file state")
	}
	var (
		version, ifi, ifiErr, filesErr sql.NullString
	)
	if err := rows.Scan(&version, &ifi, &ifiErr, &filesErr); err != nil {
		return st, fmt.Errorf("read the files of %s: %w", database, err)
	}
	st.EngineVersion, st.IFIError = version.String, ifiErr.String
	if ifi.Valid && ifi.String != "" {
		on := ifi.String == "Y"
		st.IFI = &on
	}
	if filesErr.Valid && filesErr.String != "" {
		return st, fmt.Errorf("could not read the files of %s: %s", database, filesErr.String)
	}
	if err := nextResultSet(rows, "files"); err != nil {
		return st, err
	}
	st.Files = []domain.StorageFile{}
	for rows.Next() {
		var x domain.StorageFile
		var startMB sql.NullInt64
		if err := rows.Scan(&x.DatabaseID, &x.FileID, &x.Type, &x.LogicalName, &x.PhysicalName, &x.SizeMB, &x.Growth,
			&x.IsPercentGrowth, &x.MaxSizePages, &startMB); err != nil {
			return st, fmt.Errorf("read the files of %s: %w", database, err)
		}
		x.StartupSizeMB = startMB.Int64
		x.Database = database
		if x.DatabaseID == 2 {
			x.Database = "tempdb"
		}
		st.Files = append(st.Files, x)
	}
	return st, friendlyErrOrNil(rows.Err())
}
