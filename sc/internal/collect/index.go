package collect

import (
	"context"
	"database/sql"
	"fmt"

	"sqlmonitor/internal/domain"
)

// indexFactsQuery runs inside the database (EXEC [db].sys.sp_executesql, with @schema and @table)
// and reads what the index fixes need about one user table (docs/phase4c-contract.md, part 3), or,
// with @table NULL, the indexes of every user table (the Design card's IDX04,
// docs/phase5o-contract.md section 1: one query for both): the table's columns with their types and
// lengths (sys.columns, one table only), its indexes with their key and included columns
// (sys.indexes, sys.index_columns), each index's size and the table's rows
// (sys.dm_db_partition_stats), each index's use since the restart (sys.dm_db_index_usage_stats,
// -1 when it cannot be read) and the foreign keys that name an index as the key they reference
// (sys.foreign_keys.key_index_id: disabling that index disables the key, Microsoft Learn, "Disable
// indexes and constraints"). The columns, the indexes, the use and the foreign keys are each read in
// their own TRY. SELECTs only. Result sets: one row (found, version, edition, the four errors), then
// the tables, the columns, the indexes, the index columns and the foreign keys' indexes.
const indexFactsQuery = `SET NOCOUNT ON;
DECLARE @objs TABLE (object_id int NOT NULL PRIMARY KEY, schema_name nvarchar(128) NOT NULL, table_name nvarchar(128) NOT NULL);
IF @table IS NULL
    INSERT @objs (object_id, schema_name, table_name)
    SELECT t.object_id, SCHEMA_NAME(t.schema_id), t.name FROM sys.tables AS t WHERE t.is_ms_shipped = 0;
ELSE
    INSERT @objs (object_id, schema_name, table_name)
    SELECT t.object_id, SCHEMA_NAME(t.schema_id), t.name FROM sys.tables AS t
    WHERE t.object_id = OBJECT_ID(QUOTENAME(@schema) + N'.' + QUOTENAME(@table)) AND t.is_ms_shipped = 0;
DECLARE @colsError nvarchar(2048), @ixError nvarchar(2048), @useError nvarchar(2048), @fkError nvarchar(2048);
DECLARE @tabs TABLE (object_id int NOT NULL PRIMARY KEY, rows bigint NULL, data_mb float NULL);
DECLARE @cols TABLE (object_id int NOT NULL, column_id int NOT NULL, name nvarchar(128) NOT NULL, type_name nvarchar(128) NULL, max_length int NOT NULL);
DECLARE @ix TABLE (object_id int NOT NULL, index_id int NOT NULL, name nvarchar(128) NULL, type int NOT NULL, is_unique bit NOT NULL, is_pk bit NOT NULL,
                   is_uq bit NOT NULL, is_disabled bit NOT NULL, is_hypothetical bit NOT NULL, filter nvarchar(max) NULL,
                   size_mb float NULL);
DECLARE @use TABLE (object_id int NOT NULL, index_id int NOT NULL, reads bigint NOT NULL, writes bigint NOT NULL);
DECLARE @ixc TABLE (object_id int NOT NULL, index_id int NOT NULL, name nvarchar(128) NOT NULL, key_ordinal int NOT NULL, is_included bit NOT NULL,
                    is_desc bit NOT NULL, index_column_id int NOT NULL);
DECLARE @fk TABLE (object_id int NOT NULL, index_id int NOT NULL, name nvarchar(128) NOT NULL);
IF EXISTS (SELECT 1 FROM @objs)
BEGIN
    /* Columns: one table only (the create-index guards); every table's would be read for nothing */
    BEGIN TRY
        IF @table IS NOT NULL
            INSERT @cols (object_id, column_id, name, type_name, max_length)
            SELECT c.object_id, c.column_id, c.name, CASE WHEN ty.is_user_defined = 1 THEN TYPE_NAME(c.system_type_id) ELSE ty.name END, c.max_length
            FROM sys.columns AS c
            JOIN @objs AS o ON o.object_id = c.object_id
            JOIN sys.types AS ty ON ty.user_type_id = c.user_type_id;
    END TRY
    BEGIN CATCH
        SET @colsError = ERROR_MESSAGE();
    END CATCH;

    /* Indexes, their columns, their sizes and the tables' rows */
    BEGIN TRY
        INSERT @ix (object_id, index_id, name, type, is_unique, is_pk, is_uq, is_disabled, is_hypothetical, filter, size_mb)
        SELECT i.object_id, i.index_id, i.name, i.type, i.is_unique, i.is_primary_key, i.is_unique_constraint, i.is_disabled, i.is_hypothetical,
               i.filter_definition, sz.size_mb
        FROM sys.indexes AS i
        JOIN @objs AS o ON o.object_id = i.object_id
        OUTER APPLY (SELECT SUM(ps.used_page_count) * 8 / 1024.0 AS size_mb FROM sys.dm_db_partition_stats AS ps
                     WHERE ps.object_id = i.object_id AND ps.index_id = i.index_id) AS sz;

        INSERT @ixc (object_id, index_id, name, key_ordinal, is_included, is_desc, index_column_id)
        SELECT ic.object_id, ic.index_id, c.name, ic.key_ordinal, ic.is_included_column, ic.is_descending_key, ic.index_column_id
        FROM sys.index_columns AS ic
        JOIN @objs AS o ON o.object_id = ic.object_id
        JOIN sys.columns AS c ON c.object_id = ic.object_id AND c.column_id = ic.column_id;

        INSERT @tabs (object_id, rows, data_mb)
        SELECT o.object_id, SUM(ps.row_count), SUM(ps.used_page_count) * 8 / 1024.0
        FROM @objs AS o
        LEFT JOIN sys.dm_db_partition_stats AS ps ON ps.object_id = o.object_id AND ps.index_id IN (0, 1)
        GROUP BY o.object_id;
    END TRY
    BEGIN CATCH
        SET @ixError = ERROR_MESSAGE();
    END CATCH;

    /* Use since the restart */
    BEGIN TRY
        INSERT @use (object_id, index_id, reads, writes)
        SELECT u.object_id, u.index_id, u.user_seeks + u.user_scans + u.user_lookups, u.user_updates
        FROM sys.dm_db_index_usage_stats AS u
        JOIN @objs AS o ON o.object_id = u.object_id
        WHERE u.database_id = DB_ID();
    END TRY
    BEGIN CATCH
        SET @useError = ERROR_MESSAGE();
    END CATCH;

    /* The indexes foreign keys reference (key_index_id, on the referenced table) */
    BEGIN TRY
        INSERT @fk (object_id, index_id, name)
        SELECT fk.referenced_object_id, fk.key_index_id, fk.name
        FROM sys.foreign_keys AS fk
        JOIN @objs AS o ON o.object_id = fk.referenced_object_id;
    END TRY
    BEGIN CATCH
        SET @fkError = ERROR_MESSAGE();
    END CATCH;
END;
SELECT CASE WHEN EXISTS (SELECT 1 FROM @objs) THEN 1 ELSE 0 END,
       CAST(SERVERPROPERTY('ProductVersion') AS nvarchar(64)), CONVERT(int, SERVERPROPERTY('EngineEdition')),
       @colsError, @ixError, @useError, @fkError;
SELECT o.object_id, o.schema_name, o.table_name, ISNULL(t.rows, 0), ISNULL(t.data_mb, 0)
FROM @objs AS o LEFT JOIN @tabs AS t ON t.object_id = o.object_id ORDER BY o.schema_name, o.table_name;
SELECT object_id, name, type_name, max_length FROM @cols ORDER BY object_id, column_id;
SELECT x.object_id, x.index_id, x.name, x.type, x.is_unique, x.is_pk, x.is_uq, x.is_disabled, x.is_hypothetical, x.filter, ISNULL(x.size_mb, 0),
       CASE WHEN @useError IS NULL THEN ISNULL((SELECT SUM(u.reads) FROM @use AS u WHERE u.object_id = x.object_id AND u.index_id = x.index_id), 0) ELSE -1 END,
       CASE WHEN @useError IS NULL THEN ISNULL((SELECT SUM(u.writes) FROM @use AS u WHERE u.object_id = x.object_id AND u.index_id = x.index_id), 0) ELSE -1 END
FROM @ix AS x ORDER BY x.object_id, x.index_id;
SELECT object_id, index_id, name, key_ordinal, is_included, is_desc FROM @ixc ORDER BY object_id, index_id, key_ordinal, index_column_id;
SELECT object_id, index_id, name FROM @fk ORDER BY object_id, index_id, name;`

// indexFactsBatch is the batch that runs indexFactsQuery inside a database with the table's name
// (NULL for every user table).
func indexFactsBatch(database string) string {
	return Tag(safeSession + "EXEC " + domain.QuoteName(database) +
		".sys.sp_executesql @stmt, N'@schema nvarchar(128), @table nvarchar(128)', @schema = @schema, @table = @table;")
}

// IndexFacts reads what the create-index, drop-index and disable-index fixes need about one table
// of a database (the guards' facts, and the index read back after the change). A table that does not
// exist is Found false; an area that could not be read carries its reason.
func IndexFacts(ctx context.Context, db *sql.DB, database, schema, table string) (domain.IndexFacts, error) {
	all, head, err := readIndexFacts(ctx, db, database, schema, table)
	if len(all) == 1 {
		return all[0], err
	}
	head.Schema, head.Table, head.Found = schema, table, false
	return head, err
}

// AllIndexFacts reads the indexes of every user table of a database, with the same query and in the
// same shape as IndexFacts (docs/phase5o-contract.md, section 1): the Design card's IDX04. The tables'
// columns are not read. head carries the version, the edition and the areas' errors, which every
// table carries too.
func AllIndexFacts(ctx context.Context, db *sql.DB, database string) (tables []domain.IndexFacts, head domain.IndexFacts, err error) {
	return readIndexFacts(ctx, db, database, nil, nil)
}

// readIndexFacts runs indexFactsQuery for one table (schema and table strings) or every user table
// (nil): one IndexFacts per table found, ordered by schema and table, and the head row's facts.
func readIndexFacts(ctx context.Context, db *sql.DB, database string, schema, table any) ([]domain.IndexFacts, domain.IndexFacts, error) {
	head := domain.IndexFacts{Database: database, Columns: []domain.TableColumn{}, Indexes: []domain.ExistingIndex{}}
	out := []domain.IndexFacts{}
	rows, err := db.QueryContext(ctx, indexFactsBatch(database),
		sql.Named("stmt", TagStatements(indexFactsQuery)), sql.Named("schema", schema), sql.Named("table", table))
	if err != nil {
		return out, head, friendlyError(err)
	}
	defer rows.Close()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return out, head, friendlyError(err)
		}
		return out, head, fmt.Errorf("SQL Server returned nothing about the indexes of %s", database)
	}
	var found int
	var colsErr, ixErr, useErr, fkErr sql.NullString
	if err := rows.Scan(&found, &head.ProductVersion, &head.EngineEdition, &colsErr, &ixErr, &useErr, &fkErr); err != nil {
		return out, head, err
	}
	head.Found = found == 1
	head.ColumnsError, head.IndexesError, head.UsageError, head.ForeignKeysError = colsErr.String, ixErr.String, useErr.String, fkErr.String

	if err := nextResultSet(rows, "tables"); err != nil {
		return out, head, err
	}
	byObject := map[int64]int{}
	for rows.Next() {
		f := head
		f.Columns, f.Indexes = []domain.TableColumn{}, []domain.ExistingIndex{}
		var id int64
		if err := rows.Scan(&id, &f.Schema, &f.Table, &f.Rows, &f.DataMB); err != nil {
			return out, head, err
		}
		f.Found = true
		byObject[id] = len(out)
		out = append(out, f)
	}

	if err := nextResultSet(rows, "columns"); err != nil {
		return out, head, err
	}
	for rows.Next() {
		var id int64
		var c domain.TableColumn
		var typ sql.NullString
		if err := rows.Scan(&id, &c.Name, &typ, &c.MaxLength); err != nil {
			return out, head, err
		}
		c.Type = typ.String
		if t, ok := byObject[id]; ok {
			out[t].Columns = append(out[t].Columns, c)
		}
	}

	if err := nextResultSet(rows, "indexes"); err != nil {
		return out, head, err
	}
	type ixRef struct{ table, index int }
	byIndex := map[[2]int64]ixRef{}
	for rows.Next() {
		var id int64
		var x domain.ExistingIndex
		var name, filter sql.NullString
		if err := rows.Scan(&id, &x.IndexID, &name, &x.Type, &x.IsUnique, &x.IsPrimaryKey, &x.IsUniqueConstraint, &x.IsDisabled, &x.IsHypothetical,
			&filter, &x.SizeMB, &x.Reads, &x.Writes); err != nil {
			return out, head, err
		}
		t, ok := byObject[id]
		if !ok {
			continue
		}
		x.Name, x.Filter = name.String, filter.String
		x.Keys, x.Included, x.ForeignKeys = []domain.IndexKey{}, []string{}, []string{}
		byIndex[[2]int64{id, int64(x.IndexID)}] = ixRef{t, len(out[t].Indexes)}
		out[t].Indexes = append(out[t].Indexes, x)
	}

	if err := nextResultSet(rows, "index columns"); err != nil {
		return out, head, err
	}
	// The rows come ordered by table, index and key ordinal, so the keys arrive in their order.
	for rows.Next() {
		var id int64
		var ix, ordinal int
		var name string
		var included, desc bool
		if err := rows.Scan(&id, &ix, &name, &ordinal, &included, &desc); err != nil {
			return out, head, err
		}
		r, ok := byIndex[[2]int64{id, int64(ix)}]
		if !ok {
			continue
		}
		x := &out[r.table].Indexes[r.index]
		switch {
		case included:
			x.Included = append(x.Included, name)
		case ordinal > 0:
			x.Keys = append(x.Keys, domain.IndexKey{Name: name, Descending: desc})
		}
	}

	if err := nextResultSet(rows, "foreign keys"); err != nil {
		return out, head, err
	}
	for rows.Next() {
		var id int64
		var ix int
		var name string
		if err := rows.Scan(&id, &ix, &name); err != nil {
			return out, head, err
		}
		if r, ok := byIndex[[2]int64{id, int64(ix)}]; ok {
			x := &out[r.table].Indexes[r.index]
			x.ForeignKeys = append(x.ForeignKeys, name)
		}
	}
	return out, head, friendlyErrOrNil(rows.Err())
}

// indexNameSearchQuery runs inside the database with @pattern, a LIKE pattern of the index's name as
// a word (domain.IndexNamePattern), and finds where code names the index (docs/phase5o-contract.md,
// section 4): a query that names a disabled index in a hint fails with error 315 (Microsoft Learn,
// "Disable indexes and constraints"). Three areas, each in its own TRY and each TOP (3): the
// definitions of procedures, views, functions and triggers (sys.sql_modules); plan guides' hints and
// statements (sys.plan_guides); the text of the queries Query Store saw run in the last 30 days, when
// Query Store is on (sys.query_store_query, sys.query_store_query_text), leaving out kakapo's own
// statements and CREATE, ALTER and DROP INDEX, which Query Store keeps too and which a disabled index
// does not make fail the way a hint does. SELECTs only. Result sets: one row (the three errors, Query
// Store off), the modules, the plan guides, the queries.
var indexNameSearchQuery = TagStatements(`SET NOCOUNT ON;
DECLARE @modulesError nvarchar(2048), @guidesError nvarchar(2048), @qsError nvarchar(2048), @qsOff bit = 0;
DECLARE @modules TABLE (schema_name nvarchar(128) NULL, name nvarchar(128) NULL, kind nvarchar(60) NULL, definition nvarchar(max) NULL);
DECLARE @guides TABLE (name nvarchar(128) NOT NULL, text nvarchar(max) NULL);
DECLARE @queries TABLE (query_id bigint NOT NULL, text nvarchar(max) NULL);

/* Procedures, views, functions and triggers */
BEGIN TRY
    INSERT @modules (schema_name, name, kind, definition)
    SELECT TOP (3) OBJECT_SCHEMA_NAME(m.object_id), COALESCE(o.name, tr.name), COALESCE(o.type_desc, tr.type_desc), m.definition
    FROM sys.sql_modules AS m
    LEFT JOIN sys.objects AS o ON o.object_id = m.object_id
    LEFT JOIN sys.triggers AS tr ON tr.object_id = m.object_id
    WHERE N' ' + m.definition + N' ' LIKE @pattern ESCAPE N'!'
    ORDER BY COALESCE(o.name, tr.name);
END TRY
BEGIN CATCH
    SET @modulesError = ERROR_MESSAGE();
END CATCH;

/* Plan guides: their hints and their statement */
BEGIN TRY
    INSERT @guides (name, text)
    SELECT TOP (3) pg.name, ISNULL(pg.hints, N'') + N' ' + ISNULL(pg.query_text, N'')
    FROM sys.plan_guides AS pg
    WHERE N' ' + ISNULL(pg.hints, N'') + N' ' LIKE @pattern ESCAPE N'!'
       OR N' ' + ISNULL(pg.query_text, N'') + N' ' LIKE @pattern ESCAPE N'!'
    ORDER BY pg.name;
END TRY
BEGIN CATCH
    SET @guidesError = ERROR_MESSAGE();
END CATCH;

/* Query Store: the queries that ran in the last 30 days, when it is on */
BEGIN TRY
    IF NOT EXISTS (SELECT 1 FROM sys.database_query_store_options WHERE actual_state_desc IN (N'READ_WRITE', N'READ_ONLY'))
        SET @qsOff = 1;
    ELSE
        INSERT @queries (query_id, text)
        SELECT TOP (3) q.query_id, qt.query_sql_text
        FROM sys.query_store_query AS q
        JOIN sys.query_store_query_text AS qt ON qt.query_text_id = q.query_text_id
        CROSS APPLY (SELECT LTRIM(REPLACE(REPLACE(REPLACE(LEFT(qt.query_sql_text, 100), NCHAR(13), N' '), NCHAR(10), N' '), NCHAR(9), N' ')) AS head) AS h
        WHERE q.last_execution_time >= DATEADD(DAY, -30, SYSDATETIMEOFFSET())
          AND N' ' + qt.query_sql_text + N' ' LIKE @pattern ESCAPE N'!'
          AND qt.query_sql_text NOT LIKE N'%/* kakapo */%'
          AND h.head NOT LIKE N'CREATE %INDEX %' AND h.head NOT LIKE N'ALTER INDEX %' AND h.head NOT LIKE N'DROP INDEX %'
        ORDER BY q.last_execution_time DESC;
END TRY
BEGIN CATCH
    SET @qsError = ERROR_MESSAGE();
END CATCH;

SELECT @modulesError, @guidesError, @qsError, @qsOff;
SELECT schema_name, name, kind, definition FROM @modules;
SELECT name, text FROM @guides;
SELECT query_id, text FROM @queries;`)

// SearchIndexName looks for an index's name in the database's code, plan guides and Query Store
// (docs/phase5o-contract.md, section 4): up to three places in each, by a LIKE on the name as a word.
// The texts come back with the places; domain.IndexNameSearch.Places checks the word in Go. An area
// that fails carries its reason; the error returned means the batch itself could not run.
func SearchIndexName(ctx context.Context, db *sql.DB, database, index string) (domain.IndexNameSearch, error) {
	s := domain.IndexNameSearch{Index: index}
	rows, err := db.QueryContext(ctx, Tag(safeSession+"EXEC "+domain.QuoteName(database)+".sys.sp_executesql @stmt, N'@pattern nvarchar(max)', @pattern = @pattern;"),
		sql.Named("stmt", indexNameSearchQuery), sql.Named("pattern", domain.IndexNamePattern(index)))
	if err != nil {
		return s, friendlyError(err)
	}
	defer rows.Close()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return s, friendlyError(err)
		}
		return s, fmt.Errorf("SQL Server returned nothing from the search for %s", index)
	}
	var modulesErr, guidesErr, qsErr sql.NullString
	if err := rows.Scan(&modulesErr, &guidesErr, &qsErr, &s.QueryStoreOff); err != nil {
		return s, err
	}
	s.ModulesError, s.PlanGuidesError, s.QueryStoreError = modulesErr.String, guidesErr.String, qsErr.String

	if err := nextResultSet(rows, "modules"); err != nil {
		return s, err
	}
	for rows.Next() {
		var schema, name, kind, text sql.NullString
		if err := rows.Scan(&schema, &name, &kind, &text); err != nil {
			return s, err
		}
		s.Modules = append(s.Modules, domain.IndexNamePlace{Kind: kind.String, Schema: schema.String, Name: name.String, Text: text.String})
	}
	if err := nextResultSet(rows, "plan guides"); err != nil {
		return s, err
	}
	for rows.Next() {
		var name string
		var text sql.NullString
		if err := rows.Scan(&name, &text); err != nil {
			return s, err
		}
		s.PlanGuides = append(s.PlanGuides, domain.IndexNamePlace{Kind: domain.PlacePlanGuide, Name: name, Text: text.String})
	}
	if err := nextResultSet(rows, "Query Store"); err != nil {
		return s, err
	}
	for rows.Next() {
		var id int64
		var text sql.NullString
		if err := rows.Scan(&id, &text); err != nil {
			return s, err
		}
		s.QueryStore = append(s.QueryStore, domain.IndexNamePlace{Kind: domain.PlaceQueryStore, QueryID: id, Text: text.String})
	}
	return s, friendlyErrOrNil(rows.Err())
}

// usageWindowQuery reads when the index use counters started (the disable-index preview's "since
// {day}", docs/phase5o-contract.md section 3): SQL Server's start (sys.dm_os_sys_info, else tempdb's
// creation when that needs a permission the login lacks) and the database's creation, as designQuery
// reads them, and the latest restore into it (msdb.dbo.restorehistory, as the Backups read has it;
// left out when msdb cannot be read). SELECTs only. One row: GETDATE(), the error, the start, the
// creation, the restore.
var usageWindowQuery = TagStatements(`SET NOCOUNT ON;
DECLARE @now datetime = GETDATE(), @err nvarchar(2048), @start datetime, @created datetime, @restored datetime;
BEGIN TRY
    SELECT @created = create_date FROM sys.databases WHERE database_id = DB_ID();
    IF HAS_PERMS_BY_NAME(NULL, NULL, 'VIEW SERVER STATE') = 1
        SELECT @start = sqlserver_start_time FROM sys.dm_os_sys_info;
    IF @start IS NULL
        SELECT @start = create_date FROM sys.databases WHERE database_id = 2;
END TRY
BEGIN CATCH
    SET @err = ERROR_MESSAGE();
END CATCH;
BEGIN TRY
    SELECT @restored = MAX(rh.restore_date) FROM msdb.dbo.restorehistory AS rh WHERE rh.destination_database_name = DB_NAME();
END TRY
BEGIN CATCH
    SET @restored = NULL;
END CATCH;
SELECT @now, @err, @start, @created, @restored;`)

// ReadUsageWindow reads when the index use counters of a database started: the server's clock, SQL
// Server's start, the database's creation and its latest restore (nil when none, or when msdb could
// not be read).
func ReadUsageWindow(ctx context.Context, db *sql.DB, database string) (domain.UsageWindowFacts, error) {
	var w domain.UsageWindowFacts
	var now, start, created, restored sql.NullTime
	var msg sql.NullString
	err := db.QueryRowContext(ctx, Tag(safeSession+"EXEC "+domain.QuoteName(database)+".sys.sp_executesql @stmt;"), sql.Named("stmt", usageWindowQuery)).
		Scan(&now, &msg, &start, &created, &restored)
	if err != nil {
		return w, friendlyError(err)
	}
	if msg.Valid {
		return w, fmt.Errorf("%s", msg.String)
	}
	if now.Valid {
		w.ServerNow = naive(now.Time)
	}
	if start.Valid {
		w.StartedAt = naive(start.Time)
	}
	if created.Valid {
		w.CreateDate = naive(created.Time)
	}
	w.LastRestore = naivePtr(restored)
	return w, nil
}
