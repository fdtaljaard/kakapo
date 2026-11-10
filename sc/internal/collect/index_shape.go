package collect

import (
	"context"
	"database/sql"
	"fmt"

	"sqlmonitor/internal/domain"
)

// indexShapeQuery measures one index for the query page and the reorganize-index fix
// (docs/phase5q-contract.md, section 4): sys.indexes (type, disabled, ALLOW_PAGE_LOCKS, fill factor),
// the leaf level's in-row pages from sys.dm_db_index_physical_stats in SAMPLED mode (Microsoft's own
// example uses it "to return actionable results quickly"; under 10,000 pages SQL Server reads them
// all), the pages it uses from sys.dm_db_partition_stats, and the recovery model. Partitions are
// added up, density and fragmentation weighted by pages. Runs inside the database with @schema,
// @table and @index. SELECTs only. One row.
var indexShapeQuery = TagStatements(`SET NOCOUNT ON;
DECLARE @err nvarchar(2048), @obj int, @iid int, @name sysname, @type tinyint, @disabled bit, @locks bit, @ff tinyint,
        @pages bigint, @density float, @frag float, @usedMB float, @recovery nvarchar(60);
BEGIN TRY
    SELECT @recovery = recovery_model_desc FROM sys.databases WHERE database_id = DB_ID();
    SET @obj = OBJECT_ID(QUOTENAME(@schema) + N'.' + QUOTENAME(@table), N'U');
    IF @obj IS NOT NULL
        SELECT @iid = i.index_id, @name = i.name, @type = i.type, @disabled = i.is_disabled, @locks = i.allow_page_locks, @ff = i.fill_factor
        FROM sys.indexes AS i
        WHERE i.object_id = @obj AND i.name = @index;
    IF @iid IS NOT NULL AND @disabled = 0 AND @type IN (1, 2)
    BEGIN
        SELECT @pages = SUM(ps.page_count),
               @density = SUM(ps.avg_page_space_used_in_percent * ps.page_count) / NULLIF(SUM(ps.page_count), 0),
               @frag = SUM(ps.avg_fragmentation_in_percent * ps.page_count) / NULLIF(SUM(ps.page_count), 0)
        FROM sys.dm_db_index_physical_stats(DB_ID(), @obj, @iid, NULL, 'SAMPLED') AS ps
        WHERE ps.index_level = 0 AND ps.alloc_unit_type_desc = N'IN_ROW_DATA';
        SELECT @usedMB = SUM(p.used_page_count) * 8 / 1024.0
        FROM sys.dm_db_partition_stats AS p
        WHERE p.object_id = @obj AND p.index_id = @iid;
    END;
END TRY
BEGIN CATCH
    SET @err = ERROR_MESSAGE();
END CATCH;
SELECT @err, CAST(CASE WHEN @obj IS NULL THEN 0 ELSE 1 END AS bit), CAST(CASE WHEN @iid IS NULL THEN 0 ELSE 1 END AS bit),
       @name, @type, @disabled, @locks, @ff, @pages, @density, @frag, @usedMB, @recovery,
       CAST(SERVERPROPERTY('ProductVersion') AS nvarchar(64)), CONVERT(int, SERVERPROPERTY('EngineEdition'));`)

// IndexShape measures one index of a table in a database. An error the measurement itself raised
// (a permission, a lock timeout) comes back in Error with the rest of what was read; err is for a
// connection that failed.
func IndexShape(ctx context.Context, db *sql.DB, database, schema, table, index string) (domain.IndexShapeFacts, error) {
	f := domain.IndexShapeFacts{Database: database, Schema: schema, Table: table, Index: index}
	var (
		msg, name, recovery, version sql.NullString
		typ, ff                      sql.NullInt64
		disabled, locks              sql.NullBool
		pages                        sql.NullInt64
		density, frag, usedMB        sql.NullFloat64
		edition                      sql.NullInt64
	)
	err := db.QueryRowContext(ctx, Tag(safeSession+"EXEC "+domain.QuoteName(database)+
		".sys.sp_executesql @stmt, N'@schema sysname, @table sysname, @index sysname', @schema = @schema, @table = @table, @index = @index;"),
		sql.Named("stmt", indexShapeQuery), sql.Named("schema", schema), sql.Named("table", table), sql.Named("index", index)).
		Scan(&msg, &f.TableFound, &f.IndexFound, &name, &typ, &disabled, &locks, &ff, &pages, &density, &frag, &usedMB, &recovery, &version, &edition)
	if err != nil {
		return f, friendlyError(err)
	}
	if msg.Valid {
		f.Error = fmt.Sprintf("%s", msg.String)
	}
	if name.Valid {
		f.Index = name.String
	}
	f.Type, f.FillFactor = int(typ.Int64), int(ff.Int64)
	f.Disabled, f.PageLocks = disabled.Bool, locks.Bool
	f.Pages, f.Density, f.Fragmentation, f.SizeMB = pages.Int64, density.Float64, frag.Float64, usedMB.Float64
	f.Recovery, f.ProductVersion, f.EngineEdition = recovery.String, version.String, int(edition.Int64)
	return f, nil
}
