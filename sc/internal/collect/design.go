package collect

import (
	"context"
	"database/sql"
	"errors"
	"strconv"
	"strings"

	"sqlmonitor/internal/domain"
)

// The Design card's read (docs/phase5n-contract.md, section 2): the foreign keys without an index for
// every referenced table (unindexedForeignKeysQuery with @tables NULL, the query page's own read), the
// indexes of every user table (indexFactsQuery with @table NULL, the index fixes' own read,
// docs/phase5o-contract.md), then one batch inside the database with the deletes on their referenced
// tables, the counts and the window.
// SELECTs only, the safe session, each area in its own TRY.

// designQuery runs inside the database (EXEC [db].sys.sp_executesql) with @objects, an XML list
// <o i="object_id"/> of the referenced tables of the keys without an index. Areas: the counts (enabled
// foreign keys, user tables); the rows deleted from each of those tables, leaf_delete_count plus
// leaf_ghost_count of its heap or clustered index from sys.dm_db_index_operational_stats (Microsoft Learn:
// a delete that marks the row as a ghost first, as every delete from an index does, counts in
// leaf_ghost_count and not in leaf_delete_count; the counts live only as long as the table's metadata
// stays cached, so they are "at least"); the window, SQL Server's start
// (sys.dm_os_sys_info, else tempdb's creation when that needs a permission the login lacks) and this
// database's create_date (sp_IndexCleanup.sql 1990-2005). Result sets: one row (GETDATE(), the errors,
// the counts, the start, the creation), the deletes per object.
var designQuery = TagStatements(`SET NOCOUNT ON;
DECLARE @now datetime = GETDATE(), @countsErr nvarchar(2048), @deletesErr nvarchar(2048), @windowErr nvarchar(2048),
        @fks int, @tables int, @start datetime, @created datetime;
DECLARE @ids TABLE (object_id int NOT NULL PRIMARY KEY);
DECLARE @deletes TABLE (object_id int NOT NULL PRIMARY KEY, deletes bigint NOT NULL);

/* Counts: enabled foreign keys and user tables */
BEGIN TRY
    SELECT @fks = COUNT(*) FROM sys.foreign_keys WHERE is_disabled = 0;
    SELECT @tables = COUNT(*) FROM sys.tables WHERE is_ms_shipped = 0;
END TRY
BEGIN CATCH
    SET @countsErr = ERROR_MESSAGE();
END CATCH;

/* Deletes on the referenced tables since their metadata was cached: heap or clustered index only */
BEGIN TRY
    INSERT @ids (object_id)
    SELECT DISTINCT x.o.value('@i', 'int')
    FROM (SELECT CONVERT(xml, @objects) AS doc) AS d
    CROSS APPLY d.doc.nodes('/o') AS x(o);

    IF EXISTS (SELECT 1 FROM @ids)
        INSERT @deletes (object_id, deletes)
        SELECT os.object_id, SUM(os.leaf_delete_count + os.leaf_ghost_count)
        FROM sys.dm_db_index_operational_stats(DB_ID(), NULL, NULL, NULL) AS os
        JOIN @ids AS i ON i.object_id = os.object_id
        WHERE os.index_id IN (0, 1)
        GROUP BY os.object_id;
END TRY
BEGIN CATCH
    SET @deletesErr = ERROR_MESSAGE();
END CATCH;

/* Window: SQL Server's start and this database's creation */
BEGIN TRY
    SELECT @created = create_date FROM sys.databases WHERE database_id = DB_ID();
    IF HAS_PERMS_BY_NAME(NULL, NULL, 'VIEW SERVER STATE') = 1
        SELECT @start = sqlserver_start_time FROM sys.dm_os_sys_info;
    IF @start IS NULL
        SELECT @start = create_date FROM sys.databases WHERE database_id = 2;
END TRY
BEGIN CATCH
    SET @windowErr = ERROR_MESSAGE();
END CATCH;

SELECT @now, @countsErr, ISNULL(@fks, 0), ISNULL(@tables, 0), @deletesErr, @windowErr, @start, @created;
SELECT object_id, deletes FROM @deletes;`)

// objectsXML is the <o i="id"/> list of the keys' referenced tables, each once.
func objectsXML(keys []domain.UnindexedForeignKeyFact) string {
	var b strings.Builder
	seen := map[int64]bool{}
	for _, k := range keys {
		if seen[k.RefObjectID] {
			continue
		}
		seen[k.RefObjectID] = true
		b.WriteString(`<o i="` + strconv.FormatInt(k.RefObjectID, 10) + `"/>`)
	}
	return b.String()
}

// ReadDesignFacts reads what the Design card needs about one database (docs/phase5n-contract.md,
// section 2): the foreign keys without an index (ReadUnindexedForeignKeys for every referenced table),
// every user table's indexes (AllIndexFacts, for IDX04), the deletes on their referenced tables, the
// counts and the window's start. SELECTs only. An area that
// fails says so in its error; the error returned means the batch itself could not run. LastRestore is
// the caller's, from the Backups read.
func ReadDesignFacts(ctx context.Context, db *sql.DB, database string) (domain.DesignFacts, error) {
	f := domain.DesignFacts{Database: database, ForeignKeys: []domain.UnindexedForeignKeyFact{}}
	if keys, err := ReadUnindexedForeignKeys(ctx, db, database, nil); err != nil {
		f.ForeignKeysError = err.Error()
	} else {
		f.ForeignKeys = keys
	}
	// IDX04 (docs/phase5o-contract.md, section 1): every user table's indexes, the per-table read with a
	// NULL table.
	f.IndexTables = []domain.IndexFacts{}
	if tables, head, err := AllIndexFacts(ctx, db, database); err != nil {
		f.IndexesError = err.Error()
	} else {
		f.IndexTables, f.IndexesError, f.UsageError = tables, head.IndexesError, head.UsageError
	}
	// DES01 (docs/phase5s-contract.md, section 1): the tables without a primary key.
	if tables, err := ReadNoKeyTables(ctx, db, database); err != nil {
		f.NoKeyError = err.Error()
	} else {
		f.NoKeyTables = tables
	}

	rows, err := db.QueryContext(ctx, Tag(safeSession+"SET QUOTED_IDENTIFIER ON; EXEC "+domain.QuoteName(database)+".sys.sp_executesql @stmt, N'@objects nvarchar(max)', @objects = @objects;"),
		sql.Named("stmt", designQuery), sql.Named("objects", objectsXML(f.ForeignKeys)))
	if err != nil {
		return f, friendlyError(err)
	}
	defer rows.Close()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return f, friendlyError(err)
		}
		return f, errors.New("SQL Server returned nothing about the database's design")
	}
	var countsErr, deletesErr, windowErr sql.NullString
	var now, start, create sql.NullTime
	if err := rows.Scan(&now, &countsErr, &f.EnabledForeignKeys, &f.UserTables, &deletesErr, &windowErr, &start, &create); err != nil {
		return f, err
	}
	f.CountsError, f.DeletesError, f.WindowError = countsErr.String, deletesErr.String, windowErr.String
	if now.Valid {
		f.ServerNow = naive(now.Time)
	}
	if start.Valid {
		f.StartedAt = naive(start.Time)
	} else if f.WindowError == "" {
		f.WindowError = "SQL Server's start time could not be read"
	}
	if create.Valid {
		f.CreateDate = naive(create.Time)
	}
	if err := nextResultSet(rows, "deletes"); err != nil {
		return f, err
	}
	deletes := map[int64]int64{}
	for rows.Next() {
		var id, n int64
		if err := rows.Scan(&id, &n); err != nil {
			return f, err
		}
		deletes[id] = n
	}
	if err := rows.Err(); err != nil {
		return f, friendlyError(err)
	}
	// A table with no row in the DMV had nothing deleted since its metadata was cached: 0. Deletes that
	// could not be read are -1, so nothing is raised for them.
	for i := range f.ForeignKeys {
		if f.DeletesError != "" {
			f.ForeignKeys[i].RefDeletes = -1
		} else {
			f.ForeignKeys[i].RefDeletes = deletes[f.ForeignKeys[i].RefObjectID]
		}
	}
	return f, nil
}
