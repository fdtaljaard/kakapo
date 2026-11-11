package collect

import (
	"context"
	"database/sql"
	"fmt"

	"sqlmonitor/internal/domain"
)

// The primary key read (docs/phase5s-contract.md, section 1): the user tables without a primary key for
// the Design card's DES01 (@table NULL), or one table, key or not, for the add-primary-key fix. SELECTs
// only, the safe session.

// noKeyQuery runs inside the database (EXEC [db].sys.sp_executesql with @schema and @table). The flags of
// sys.tables that newer versions added (temporal_type 2016, is_memory_optimized 2014, is_external 2016,
// is_node and is_edge 2017, ledger_type 2022) are read only where the column exists. Each table's rows and
// size (sys.dm_db_partition_stats, the heap or clustered index), its clustered index, its enabled
// nonclustered rowstore indexes and their size, its identity column (sys.identity_columns), whether it is
// partitioned or has a columnstore index; then the unique indexes whose key columns are all NOT NULL and not
// computed (enabled, unfiltered, not hypothetical, rowstore, not the primary key), and with one table the
// names of every object in its schema (a constraint's name must be new there). Result sets: one row (the
// errors), the tables, the candidates' key columns in order, the names.
var noKeyQuery = TagStatements(`SET NOCOUNT ON;
DECLARE @err nvarchar(2048), @namesErr nvarchar(2048), @sql nvarchar(max);
DECLARE @t TABLE (object_id int NOT NULL PRIMARY KEY, schema_name nvarchar(128) NOT NULL, table_name nvarchar(128) NOT NULL,
                  pk_name nvarchar(128) NULL, temporal_type int NOT NULL, mem bit NOT NULL, ext bit NOT NULL, graph bit NOT NULL,
                  ledger_type int NOT NULL, repl bit NOT NULL, cdc bit NOT NULL);
DECLARE @s TABLE (object_id int NOT NULL PRIMARY KEY, rows bigint NOT NULL, data_mb float NOT NULL, heap bit NOT NULL, clustered_name nvarchar(128) NULL,
                  nc int NOT NULL, nc_mb float NOT NULL, identity_name nvarchar(128) NULL, identity_type nvarchar(128) NULL,
                  partitioned bit NOT NULL, columnstore bit NOT NULL);
DECLARE @c TABLE (object_id int NOT NULL, index_name nvarchar(128) NOT NULL, is_clustered bit NOT NULL, key_ordinal int NOT NULL,
                  column_name nvarchar(128) NOT NULL, max_length int NOT NULL);
DECLARE @names TABLE (name nvarchar(128) NOT NULL);
BEGIN TRY
    SET @sql = N'SELECT t.object_id, SCHEMA_NAME(t.schema_id), t.name, pk.name, '
        + CASE WHEN COL_LENGTH(N'sys.tables', N'temporal_type') IS NULL THEN N'0' ELSE N't.temporal_type' END + N', '
        + CASE WHEN COL_LENGTH(N'sys.tables', N'is_memory_optimized') IS NULL THEN N'0' ELSE N't.is_memory_optimized' END + N', '
        + CASE WHEN COL_LENGTH(N'sys.tables', N'is_external') IS NULL THEN N'0' ELSE N't.is_external' END + N', '
        + CASE WHEN COL_LENGTH(N'sys.tables', N'is_node') IS NULL THEN N'0' ELSE N'CASE WHEN t.is_node = 1 OR t.is_edge = 1 THEN 1 ELSE 0 END' END + N', '
        + CASE WHEN COL_LENGTH(N'sys.tables', N'ledger_type') IS NULL THEN N'0' ELSE N't.ledger_type' END + N',
       CASE WHEN t.is_replicated = 1 OR t.is_merge_published = 1 THEN 1 ELSE 0 END, t.is_tracked_by_cdc
FROM sys.tables AS t
LEFT JOIN sys.key_constraints AS pk ON pk.parent_object_id = t.object_id AND pk.type = ''PK''
WHERE t.is_ms_shipped = 0
  AND ((@table IS NULL AND pk.object_id IS NULL) OR t.object_id = OBJECT_ID(QUOTENAME(@schema) + N''.'' + QUOTENAME(@table)));';
    INSERT @t EXEC sys.sp_executesql @sql, N'@schema nvarchar(128), @table nvarchar(128)', @schema = @schema, @table = @table;

    INSERT @s (object_id, rows, data_mb, heap, clustered_name, nc, nc_mb, identity_name, identity_type, partitioned, columnstore)
    SELECT t.object_id,
           ISNULL((SELECT SUM(ps.row_count) FROM sys.dm_db_partition_stats AS ps WHERE ps.object_id = t.object_id AND ps.index_id IN (0, 1)), 0),
           ISNULL((SELECT SUM(ps.used_page_count) * 8 / 1024.0 FROM sys.dm_db_partition_stats AS ps WHERE ps.object_id = t.object_id AND ps.index_id IN (0, 1)), 0),
           CASE WHEN EXISTS (SELECT 1 FROM sys.indexes AS i WHERE i.object_id = t.object_id AND i.index_id = 0) THEN 1 ELSE 0 END,
           (SELECT i.name FROM sys.indexes AS i WHERE i.object_id = t.object_id AND i.index_id = 1),
           (SELECT COUNT(*) FROM sys.indexes AS i WHERE i.object_id = t.object_id AND i.type = 2 AND i.is_disabled = 0 AND i.is_hypothetical = 0),
           ISNULL((SELECT SUM(ps.used_page_count) * 8 / 1024.0 FROM sys.dm_db_partition_stats AS ps
                   JOIN sys.indexes AS i ON i.object_id = ps.object_id AND i.index_id = ps.index_id
                   WHERE ps.object_id = t.object_id AND i.type = 2 AND i.is_disabled = 0), 0),
           (SELECT ic.name FROM sys.identity_columns AS ic WHERE ic.object_id = t.object_id),
           (SELECT TYPE_NAME(ic.system_type_id) FROM sys.identity_columns AS ic WHERE ic.object_id = t.object_id),
           CASE WHEN (SELECT COUNT(*) FROM sys.partitions AS p WHERE p.object_id = t.object_id AND p.index_id IN (0, 1)) > 1 THEN 1 ELSE 0 END,
           CASE WHEN EXISTS (SELECT 1 FROM sys.indexes AS i WHERE i.object_id = t.object_id AND i.type IN (5, 6)) THEN 1 ELSE 0 END
    FROM @t AS t;

    INSERT @c (object_id, index_name, is_clustered, key_ordinal, column_name, max_length)
    SELECT i.object_id, i.name, CASE WHEN i.type = 1 THEN 1 ELSE 0 END, ic.key_ordinal, c.name, c.max_length
    FROM @t AS t
    JOIN sys.indexes AS i ON i.object_id = t.object_id
    JOIN sys.index_columns AS ic ON ic.object_id = i.object_id AND ic.index_id = i.index_id AND ic.key_ordinal > 0
    JOIN sys.columns AS c ON c.object_id = ic.object_id AND c.column_id = ic.column_id
    WHERE i.is_unique = 1 AND i.is_primary_key = 0 AND i.is_disabled = 0 AND i.has_filter = 0 AND i.is_hypothetical = 0 AND i.type IN (1, 2)
      AND NOT EXISTS (SELECT 1 FROM sys.index_columns AS ic2
                      JOIN sys.columns AS c2 ON c2.object_id = ic2.object_id AND c2.column_id = ic2.column_id
                      WHERE ic2.object_id = i.object_id AND ic2.index_id = i.index_id AND ic2.key_ordinal > 0
                        AND (c2.is_nullable = 1 OR c2.is_computed = 1));
END TRY
BEGIN CATCH
    SET @err = ERROR_MESSAGE();
END CATCH;
IF @table IS NOT NULL
BEGIN
    BEGIN TRY
        INSERT @names (name) SELECT o.name FROM sys.objects AS o WHERE o.schema_id = SCHEMA_ID(@schema);
    END TRY
    BEGIN CATCH
        SET @namesErr = ERROR_MESSAGE();
    END CATCH;
END;
SELECT @err, @namesErr;
SELECT t.object_id, t.schema_name, t.table_name, t.pk_name, t.temporal_type, t.mem, t.ext, t.graph, t.ledger_type, t.repl, t.cdc,
       s.rows, s.data_mb, s.heap, s.clustered_name, s.nc, s.nc_mb, s.identity_name, s.identity_type, s.partitioned, s.columnstore
FROM @t AS t JOIN @s AS s ON s.object_id = t.object_id
ORDER BY t.schema_name, t.table_name;
SELECT object_id, index_name, is_clustered, column_name, max_length FROM @c ORDER BY object_id, index_name, key_ordinal;
SELECT name FROM @names;`)

// readNoKey runs noKeyQuery for every table without a key (schema and table nil) or one table.
func readNoKey(ctx context.Context, db *sql.DB, database string, schema, table any) (tables []domain.NoKeyTableFact, names []string, namesErr string, err error) {
	tables, names = []domain.NoKeyTableFact{}, []string{}
	rows, err := db.QueryContext(ctx, Tag(safeSession+"EXEC "+domain.QuoteName(database)+".sys.sp_executesql @stmt, N'@schema nvarchar(128), @table nvarchar(128)', @schema = @schema, @table = @table;"),
		sql.Named("stmt", noKeyQuery), sql.Named("schema", schema), sql.Named("table", table))
	if err != nil {
		return tables, names, "", friendlyError(err)
	}
	defer rows.Close()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return tables, names, "", friendlyError(err)
		}
		return tables, names, "", fmt.Errorf("SQL Server returned nothing about the primary keys of %s", database)
	}
	var e, ne sql.NullString
	if err := rows.Scan(&e, &ne); err != nil {
		return tables, names, "", err
	}
	if e.Valid {
		return tables, names, "", fmt.Errorf("%s", e.String)
	}
	namesErr = ne.String

	if err := nextResultSet(rows, "tables"); err != nil {
		return tables, names, namesErr, err
	}
	byObject := map[int64]int{}
	for rows.Next() {
		var id int64
		var t domain.NoKeyTableFact
		var pk, clustered, identity, identityType sql.NullString
		if err := rows.Scan(&id, &t.Schema, &t.Table, &pk, &t.TemporalType, &t.MemoryOptimized, &t.External, &t.Graph, &t.LedgerType,
			&t.Replicated, &t.CDC, &t.Rows, &t.DataMB, &t.Heap, &clustered, &t.Nonclustered, &t.NonclusteredMB, &identity, &identityType,
			&t.Partitioned, &t.Columnstore); err != nil {
			return tables, names, namesErr, err
		}
		t.HasPrimaryKey, t.PrimaryKey = pk.Valid, pk.String
		t.ClusteredIndex, t.Identity, t.IdentityType = clustered.String, identity.String, identityType.String
		t.Unique = []domain.UniqueKeyCandidate{}
		byObject[id] = len(tables)
		tables = append(tables, t)
	}

	if err := nextResultSet(rows, "key candidates"); err != nil {
		return tables, names, namesErr, err
	}
	for rows.Next() {
		var id int64
		var index, column string
		var clustered bool
		var length int
		if err := rows.Scan(&id, &index, &clustered, &column, &length); err != nil {
			return tables, names, namesErr, err
		}
		i, ok := byObject[id]
		if !ok {
			continue
		}
		u := tables[i].Unique
		if n := len(u); n == 0 || u[n-1].Index != index {
			u = append(u, domain.UniqueKeyCandidate{Index: index, Clustered: clustered, Columns: []string{}})
		}
		last := &u[len(u)-1]
		last.Columns = append(last.Columns, column)
		last.KeyBytes += length
		tables[i].Unique = u
	}

	if err := nextResultSet(rows, "names"); err != nil {
		return tables, names, namesErr, err
	}
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return tables, names, namesErr, err
		}
		names = append(names, n)
	}
	return tables, names, namesErr, friendlyErrOrNil(rows.Err())
}

// ReadNoKeyTables reads the user tables without a primary key of a database (the Design card's DES01).
func ReadNoKeyTables(ctx context.Context, db *sql.DB, database string) ([]domain.NoKeyTableFact, error) {
	tables, _, _, err := readNoKey(ctx, db, database, nil, nil)
	return tables, err
}

// tableCodeQuery runs inside the database with @pattern (the table's name as a word,
// domain.IndexNamePattern), @id (the table's object_id): the procedures, views, functions and triggers
// that name the table, triggers on the table marked, and the queries Query Store saw run in the last 30
// days that insert and name it (leaving out kakapo's own). TOP (50) each. SELECTs only. Result sets: one
// row (the errors, Query Store off), the modules, the queries.
var tableCodeQuery = TagStatements(`SET NOCOUNT ON;
DECLARE @modulesError nvarchar(2048), @qsError nvarchar(2048), @qsOff bit = 0;
DECLARE @modules TABLE (schema_name nvarchar(128) NULL, name nvarchar(128) NULL, kind nvarchar(60) NULL, on_table bit NOT NULL, definition nvarchar(max) NULL);
DECLARE @queries TABLE (query_id bigint NOT NULL, text nvarchar(max) NULL);
BEGIN TRY
    INSERT @modules (schema_name, name, kind, on_table, definition)
    SELECT TOP (50) OBJECT_SCHEMA_NAME(m.object_id), o.name, o.type_desc, CASE WHEN o.parent_object_id = @id THEN 1 ELSE 0 END, m.definition
    FROM sys.sql_modules AS m
    JOIN sys.objects AS o ON o.object_id = m.object_id
    WHERE o.parent_object_id = @id OR N' ' + m.definition + N' ' LIKE @pattern ESCAPE N'!'
    ORDER BY CASE WHEN o.parent_object_id = @id THEN 0 ELSE 1 END, o.name;
END TRY
BEGIN CATCH
    SET @modulesError = ERROR_MESSAGE();
END CATCH;
BEGIN TRY
    IF NOT EXISTS (SELECT 1 FROM sys.database_query_store_options WHERE actual_state_desc IN (N'READ_WRITE', N'READ_ONLY'))
        SET @qsOff = 1;
    ELSE
        INSERT @queries (query_id, text)
        SELECT TOP (50) q.query_id, qt.query_sql_text
        FROM sys.query_store_query AS q
        JOIN sys.query_store_query_text AS qt ON qt.query_text_id = q.query_text_id
        WHERE q.last_execution_time >= DATEADD(DAY, -30, SYSDATETIMEOFFSET())
          AND qt.query_sql_text LIKE N'%INSERT%'
          AND N' ' + qt.query_sql_text + N' ' LIKE @pattern ESCAPE N'!'
          AND qt.query_sql_text NOT LIKE N'%/* kakapo */%'
        ORDER BY q.last_execution_time DESC;
END TRY
BEGIN CATCH
    SET @qsError = ERROR_MESSAGE();
END CATCH;
SELECT @modulesError, @qsError, @qsOff;
SELECT schema_name, name, kind, on_table, definition FROM @modules;
SELECT query_id, text FROM @queries;`)

// SearchTableCode looks for the code that names a table (docs/phase5s-contract.md, section 4); the
// domain decides which of it would break on a new column (domain.TableCodeSearch.Breaks).
func SearchTableCode(ctx context.Context, db *sql.DB, database, schema, table string) (domain.TableCodeSearch, error) {
	s := domain.TableCodeSearch{}
	// @id is the table's object_id, read with its three-part name.
	rows, err := db.QueryContext(ctx, Tag(safeSession+"DECLARE @id int = OBJECT_ID(QUOTENAME(@db) + N'.' + QUOTENAME(@schema) + N'.' + QUOTENAME(@table)); "+
		"EXEC "+domain.QuoteName(database)+".sys.sp_executesql @stmt, N'@pattern nvarchar(max), @id int', @pattern = @pattern, @id = @id;"),
		sql.Named("stmt", tableCodeQuery), sql.Named("pattern", domain.IndexNamePattern(table)),
		sql.Named("db", database), sql.Named("schema", schema), sql.Named("table", table))
	if err != nil {
		return s, friendlyError(err)
	}
	defer rows.Close()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return s, friendlyError(err)
		}
		return s, fmt.Errorf("SQL Server returned nothing from the search for %s.%s", schema, table)
	}
	var modulesErr, qsErr sql.NullString
	if err := rows.Scan(&modulesErr, &qsErr, &s.QueryStoreOff); err != nil {
		return s, err
	}
	s.ModulesError, s.QueryStoreError = modulesErr.String, qsErr.String
	if err := nextResultSet(rows, "modules"); err != nil {
		return s, err
	}
	for rows.Next() {
		var schemaName, name, kind, text sql.NullString
		var onTable bool
		if err := rows.Scan(&schemaName, &name, &kind, &onTable, &text); err != nil {
			return s, err
		}
		s.Modules = append(s.Modules, domain.TableCodeUse{Kind: kind.String, Schema: schemaName.String, Name: name.String, OnTable: onTable, Text: text.String})
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
		s.QueryStore = append(s.QueryStore, domain.TableCodeUse{Kind: domain.PlaceQueryStore, QueryID: id, Text: text.String})
	}
	return s, friendlyErrOrNil(rows.Err())
}

// PrimaryKeyFacts reads what the add-primary-key fix needs about one table: the table as the primary key
// read has it, its columns and indexes (IndexFacts), the names in its schema and the code that names it.
// A table that does not exist is Found false.
func PrimaryKeyFacts(ctx context.Context, db *sql.DB, database, schema, table string) (domain.PrimaryKeyFacts, error) {
	f := domain.PrimaryKeyFacts{Table: domain.NoKeyTableFact{Schema: schema, Table: table}}
	ix, err := IndexFacts(ctx, db, database, schema, table)
	f.Index = ix
	if err != nil {
		return f, err
	}
	if !ix.Found {
		return f, nil
	}
	tables, names, namesErr, err := readNoKey(ctx, db, database, schema, table)
	if err != nil {
		return f, err
	}
	if len(tables) != 1 {
		return f, nil
	}
	f.Found, f.Table, f.Names, f.NamesError = true, tables[0], names, namesErr
	code, err := SearchTableCode(ctx, db, database, schema, table)
	if err != nil {
		code.ModulesError = err.Error()
	}
	f.Code = code
	return f, nil
}
