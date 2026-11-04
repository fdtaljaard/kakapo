package collect

import (
	"context"
	"database/sql"
	"fmt"
	"sort"

	"sqlmonitor/internal/domain"
)

// indexFactsQuery runs inside the database (EXEC [db].sys.sp_executesql, with @schema and @table)
// and reads what the index fixes need about one user table (docs/phase4c-contract.md, part 3): its
// columns with their types and lengths (sys.columns), its indexes with their key and included
// columns (sys.indexes, sys.index_columns), each index's size and the table's rows
// (sys.dm_db_partition_stats), and each index's use since the restart (sys.dm_db_index_usage_stats).
// The columns, the indexes and the use are each read in their own TRY. SELECTs only. Result sets:
// one row (found, version, edition, rows, data MB, the three errors), then the columns, the
// indexes and the index columns.
const indexFactsQuery = `SET NOCOUNT ON;
DECLARE @obj int = OBJECT_ID(QUOTENAME(@schema) + N'.' + QUOTENAME(@table));
IF @obj IS NOT NULL AND NOT EXISTS (SELECT 1 FROM sys.tables AS t WHERE t.object_id = @obj AND t.is_ms_shipped = 0)
    SET @obj = NULL;
DECLARE @rows bigint, @dataMB float, @colsError nvarchar(2048), @ixError nvarchar(2048), @useError nvarchar(2048);
DECLARE @cols TABLE (column_id int NOT NULL, name nvarchar(128) NOT NULL, type_name nvarchar(128) NULL, max_length int NOT NULL);
DECLARE @ix TABLE (index_id int NOT NULL, name nvarchar(128) NULL, type int NOT NULL, is_unique bit NOT NULL, is_pk bit NOT NULL,
                   is_uq bit NOT NULL, is_disabled bit NOT NULL, is_hypothetical bit NOT NULL, filter nvarchar(max) NULL,
                   size_mb float NULL);
DECLARE @use TABLE (index_id int NOT NULL, reads bigint NOT NULL, writes bigint NOT NULL);
DECLARE @ixc TABLE (index_id int NOT NULL, name nvarchar(128) NOT NULL, key_ordinal int NOT NULL, is_included bit NOT NULL,
                    is_desc bit NOT NULL, index_column_id int NOT NULL);
IF @obj IS NOT NULL
BEGIN
    BEGIN TRY
        INSERT @cols (column_id, name, type_name, max_length)
        SELECT c.column_id, c.name, CASE WHEN ty.is_user_defined = 1 THEN TYPE_NAME(c.system_type_id) ELSE ty.name END, c.max_length
        FROM sys.columns AS c
        JOIN sys.types AS ty ON ty.user_type_id = c.user_type_id
        WHERE c.object_id = @obj;
    END TRY
    BEGIN CATCH
        SET @colsError = ERROR_MESSAGE();
    END CATCH;

    BEGIN TRY
        INSERT @ix (index_id, name, type, is_unique, is_pk, is_uq, is_disabled, is_hypothetical, filter, size_mb)
        SELECT i.index_id, i.name, i.type, i.is_unique, i.is_primary_key, i.is_unique_constraint, i.is_disabled, i.is_hypothetical,
               i.filter_definition,
               (SELECT SUM(ps.used_page_count) * 8 / 1024.0 FROM sys.dm_db_partition_stats AS ps
                WHERE ps.object_id = i.object_id AND ps.index_id = i.index_id)
        FROM sys.indexes AS i
        WHERE i.object_id = @obj;

        INSERT @ixc (index_id, name, key_ordinal, is_included, is_desc, index_column_id)
        SELECT ic.index_id, c.name, ic.key_ordinal, ic.is_included_column, ic.is_descending_key, ic.index_column_id
        FROM sys.index_columns AS ic
        JOIN sys.columns AS c ON c.object_id = ic.object_id AND c.column_id = ic.column_id
        WHERE ic.object_id = @obj;

        SELECT @rows = SUM(ps.row_count), @dataMB = SUM(ps.used_page_count) * 8 / 1024.0
        FROM sys.dm_db_partition_stats AS ps
        WHERE ps.object_id = @obj AND ps.index_id IN (0, 1);
    END TRY
    BEGIN CATCH
        SET @ixError = ERROR_MESSAGE();
    END CATCH;

    BEGIN TRY
        INSERT @use (index_id, reads, writes)
        SELECT u.index_id, u.user_seeks + u.user_scans + u.user_lookups, u.user_updates
        FROM sys.dm_db_index_usage_stats AS u
        WHERE u.database_id = DB_ID() AND u.object_id = @obj;
    END TRY
    BEGIN CATCH
        SET @useError = ERROR_MESSAGE();
    END CATCH;
END;
SELECT CASE WHEN @obj IS NULL THEN 0 ELSE 1 END,
       CAST(SERVERPROPERTY('ProductVersion') AS nvarchar(64)), CONVERT(int, SERVERPROPERTY('EngineEdition')),
       ISNULL(@rows, 0), ISNULL(@dataMB, 0), @colsError, @ixError, @useError;
SELECT name, type_name, max_length FROM @cols ORDER BY column_id;
SELECT x.index_id, x.name, x.type, x.is_unique, x.is_pk, x.is_uq, x.is_disabled, x.is_hypothetical, x.filter, ISNULL(x.size_mb, 0),
       ISNULL((SELECT SUM(u.reads) FROM @use AS u WHERE u.index_id = x.index_id), 0),
       ISNULL((SELECT SUM(u.writes) FROM @use AS u WHERE u.index_id = x.index_id), 0)
FROM @ix AS x ORDER BY x.index_id;
SELECT index_id, name, key_ordinal, is_included, is_desc FROM @ixc ORDER BY index_id, key_ordinal, index_column_id;`

// indexFactsBatch is the batch that runs indexFactsQuery inside a database with the table's name.
func indexFactsBatch(database string) string {
	return Tag(safeSession + "EXEC " + domain.QuoteName(database) +
		".sys.sp_executesql @stmt, N'@schema nvarchar(128), @table nvarchar(128)', @schema = @schema, @table = @table;")
}

// IndexFacts reads what the create-index and drop-index fixes need about one table of a database
// (the guards' facts, and the index read back after the change). A table that does not exist is
// Found false; an area that could not be read carries its reason.
func IndexFacts(ctx context.Context, db *sql.DB, database, schema, table string) (domain.IndexFacts, error) {
	f := domain.IndexFacts{Database: database, Schema: schema, Table: table, Columns: []domain.TableColumn{}, Indexes: []domain.ExistingIndex{}}
	rows, err := db.QueryContext(ctx, indexFactsBatch(database),
		sql.Named("stmt", Tag(indexFactsQuery)), sql.Named("schema", schema), sql.Named("table", table))
	if err != nil {
		return f, friendlyError(err)
	}
	defer rows.Close()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return f, friendlyError(err)
		}
		return f, fmt.Errorf("SQL Server returned no row for the table %s.%s", schema, table)
	}
	var found int
	var colsErr, ixErr, useErr sql.NullString
	if err := rows.Scan(&found, &f.ProductVersion, &f.EngineEdition, &f.Rows, &f.DataMB, &colsErr, &ixErr, &useErr); err != nil {
		return f, err
	}
	f.Found = found == 1
	f.ColumnsError, f.IndexesError, f.UsageError = colsErr.String, ixErr.String, useErr.String

	if err := nextResultSet(rows, "columns"); err != nil {
		return f, err
	}
	for rows.Next() {
		var c domain.TableColumn
		var typ sql.NullString
		if err := rows.Scan(&c.Name, &typ, &c.MaxLength); err != nil {
			return f, err
		}
		c.Type = typ.String
		f.Columns = append(f.Columns, c)
	}

	if err := nextResultSet(rows, "indexes"); err != nil {
		return f, err
	}
	byID := map[int]int{}
	for rows.Next() {
		var x domain.ExistingIndex
		var name, filter sql.NullString
		if err := rows.Scan(&x.IndexID, &name, &x.Type, &x.IsUnique, &x.IsPrimaryKey, &x.IsUniqueConstraint, &x.IsDisabled, &x.IsHypothetical,
			&filter, &x.SizeMB, &x.Reads, &x.Writes); err != nil {
			return f, err
		}
		x.Name, x.Filter = name.String, filter.String
		x.Keys, x.Included = []domain.IndexKey{}, []string{}
		byID[x.IndexID] = len(f.Indexes)
		f.Indexes = append(f.Indexes, x)
	}

	if err := nextResultSet(rows, "index columns"); err != nil {
		return f, err
	}
	type keyCol struct {
		ordinal int
		key     domain.IndexKey
	}
	keys := map[int][]keyCol{}
	for rows.Next() {
		var id, ordinal int
		var name string
		var included, desc bool
		if err := rows.Scan(&id, &name, &ordinal, &included, &desc); err != nil {
			return f, err
		}
		i, ok := byID[id]
		if !ok {
			continue
		}
		switch {
		case included:
			f.Indexes[i].Included = append(f.Indexes[i].Included, name)
		case ordinal > 0:
			keys[id] = append(keys[id], keyCol{ordinal, domain.IndexKey{Name: name, Descending: desc}})
		}
	}
	for id, ks := range keys {
		sort.Slice(ks, func(a, b int) bool { return ks[a].ordinal < ks[b].ordinal })
		for _, k := range ks {
			f.Indexes[byID[id]].Keys = append(f.Indexes[byID[id]].Keys, k.key)
		}
	}
	return f, friendlyErrOrNil(rows.Err())
}
