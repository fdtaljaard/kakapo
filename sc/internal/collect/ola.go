package collect

import (
	"context"
	"database/sql"
	"strings"

	"sqlmonitor/internal/domain"
)

// olaIndexQuery reads what install-ola-index needs (docs/phase5u-contract.md, section 1), SELECTs only:
// the server, which of Ola Hallengren's index objects are in master, the version he writes in his
// procedures ("--// Version: " and 19 characters, as he reads it himself), and whether kakapo's
// maintenance procedure is there.
const olaIndexQuery = `
DECLARE @def nvarchar(max) = COALESCE(OBJECT_DEFINITION(OBJECT_ID(N'master.dbo.IndexOptimize', N'P')),
                                      OBJECT_DEFINITION(OBJECT_ID(N'master.dbo.CommandExecute', N'P')));
SELECT CONVERT(nvarchar(128), SERVERPROPERTY('ServerName')), CONVERT(nvarchar(128), SERVERPROPERTY('ProductVersion')),
       CAST(CASE WHEN OBJECT_ID(N'master.dbo.IndexOptimize', N'P') IS NOT NULL THEN 1 ELSE 0 END AS bit),
       CAST(CASE WHEN OBJECT_ID(N'master.dbo.CommandExecute', N'P') IS NOT NULL THEN 1 ELSE 0 END AS bit),
       CAST(CASE WHEN OBJECT_ID(N'master.dbo.CommandLog', N'U') IS NOT NULL THEN 1 ELSE 0 END AS bit),
       CASE WHEN CHARINDEX(N'--// Version: ', @def) > 0 THEN SUBSTRING(@def, CHARINDEX(N'--// Version: ', @def) + 14, 19) END,
       CAST(CASE WHEN OBJECT_ID(N'master.dbo.kakapo_Maintenance', N'P') IS NOT NULL THEN 1 ELSE 0 END AS bit);`

// ReadOlaIndex reads Ola Hallengren's index objects in master.
func ReadOlaIndex(ctx context.Context, db *sql.DB) (domain.OlaIndexFacts, error) {
	var f domain.OlaIndexFacts
	var version sql.NullString
	err := db.QueryRowContext(ctx, Tag(olaIndexQuery)).Scan(&f.ServerName, &f.ProductVersion, &f.IndexOptimize, &f.CommandExecute,
		&f.CommandLog, &version, &f.MaintProc)
	if err != nil {
		return f, friendlyError(err)
	}
	f.Version = strings.TrimSpace(version.String)
	return f, nil
}
