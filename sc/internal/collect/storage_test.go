package collect

import (
	"context"
	"os"
	"strings"
	"testing"

	"sqlmonitor/internal/domain"
)

func TestEstimateParsing(t *testing.T) {
	if kb, ok := estimateFromText("Estimated TEMPDB space (in KB) needed for CHECKDB on database Laylax = 4604522."); !ok || kb != 4604522 {
		t.Errorf("message: %d %v", kb, ok)
	}
	if _, ok := estimateFromText("DBCC execution completed."); ok {
		t.Error("a message without an estimate")
	}
	for _, c := range []struct {
		col  string
		v    any
		want int64
		ok   bool
	}{
		{"Estimated TEMPDB space (in KB) needed for CHECKALLOC", int64(13), 13, true},
		{"Estimated TEMPDB space (in KB) needed for CHECKTABLES", int32(2048), 2048, true},
		{"Estimated TEMPDB space needed for CHECKTABLES (KB)", []byte(" 77 "), 77, true},
		{"Something else", int64(5), 0, false},
	} {
		if kb, ok := estimateFromColumn(c.col, c.v); kb != c.want || ok != c.ok {
			t.Errorf("%q %v: %d %v", c.col, c.v, kb, ok)
		}
	}
}

// TestStorageInstanceQuery checks the phase 3f reads, each in its own TRY: what fills tempdb, the
// top tempdb sessions and the oldest snapshot transaction (TMP03), the PAGELATCH waits against the
// uptime (TMP01), and this database's slowest growth in the default trace (STO02/STO03).
func TestStorageInstanceQuery(t *testing.T) {
	for _, want := range []string{"user_object_reserved_page_count", "internal_object_reserved_page_count",
		"version_store_reserved_page_count", "tempdb.sys.dm_db_session_space_usage", "tempdb.sys.dm_db_task_space_usage",
		"sys.dm_tran_active_snapshot_database_transactions", "N'PAGELATCH_UP', N'PAGELATCH_EX'", "sqlserver_start_time",
		"t.EventClass IN (92, 93) AND t.DatabaseID IN (@dbid, 2)", "FROM @grow AS g WHERE g.database_id = @dbid",
		"ORDER BY g.duration_us DESC", "FROM @grow AS g WHERE g.database_id = 2", "WHEN g.event_class = 93",
		"SET @growthError = N'the default trace is off.'",
		"LEFT JOIN sys.master_files AS smf ON smf.database_id = 2 AND smf.file_id = df.file_id",
		"@tgCount, @tgSecs, @tgLog, @tgLast, @tgSlowMs, @tgSlowFile, @tgSlowAt",
		"SELECT session_id, login_name, program_name, mb FROM @tsess"} {
		if !strings.Contains(storageInstanceQuery, want) {
			t.Errorf("storage instance query lacks %q", want)
		}
	}
	if n := strings.Count(storageInstanceQuery, "BEGIN TRY"); n != 7 {
		t.Errorf("%d TRY blocks", n)
	}
	// The dynamic SQL inside the database is cached under its own text: it carries the tag too.
	if !strings.Contains(storageDatabaseQuery, "N'"+KakapoTag+" SELECT @o") || strings.Contains(storageDatabaseQuery, "SQLMonitor") {
		t.Error("the oldest-row read of the tables that only grow must carry the kakapo tag")
	}
	if !strings.HasPrefix(inDatabase("WarehouseLive"), KakapoTag) {
		t.Errorf("in-database batch not tagged: %q", inDatabase("WarehouseLive"))
	}
}

// TestGrowingTablesQuery checks IDX10's read: user tables named like log or history tables (the
// domain's words), a million rows or 500 MB, the 10 biggest, and one MIN of the first date column
// per table when that column leads an index, else the oldest row's date through an ascending
// identity clustered key (a seek either way), read uncommitted and in its own TRY so
// one table cannot stop the others.
func TestGrowingTablesQuery(t *testing.T) {
	q := storageDatabaseQuery
	for _, w := range domain.GrowingTableWords {
		if !strings.Contains(q, "(N'"+w+"')") {
			t.Errorf("growing tables read lacks the word %q", w)
		}
	}
	for _, want := range []string{"FROM sys.tables AS t", "JOIN sys.dm_db_partition_stats AS p ON p.object_id = t.object_id",
		"WHERE LOWER(t.name) LIKE N'%' + w.word + N'%'", "SELECT TOP (10) t.object_id", ">= 1000000", ">= 500",
		"TYPE_NAME(c.system_type_id) IN (N'datetime', N'datetime2', N'date')", "ORDER BY c.column_id",
		"JOIN sys.indexes AS i ON i.object_id = ic.object_id AND i.index_id = ic.index_id",
		"ic.column_id = d.column_id AND ic.key_ordinal = 1", "i.is_disabled = 0 AND i.is_hypothetical = 0 AND i.has_filter = 0",
		"MIN(' + QUOTENAME(@col) + N')", "WITH (READUNCOMMITTED)", "IF @col IS NOT NULL AND (@indexed = 1 OR @key IS NOT NULL)",
		// Otherwise the oldest row through an ascending identity that leads the clustered index (a seek).
		"JOIN sys.identity_columns AS c ON c.object_id = ic.object_id AND c.column_id = ic.column_id",
		"ic.index_id = 1 AND ic.key_ordinal = 1", "ic.is_descending_key = 0 AND CONVERT(bigint, c.increment_value) > 0",
		"SELECT TOP (1) @o = CONVERT(datetime2, ' + QUOTENAME(@col) + N')", "ORDER BY ' + QUOTENAME(@key) + N';'",
		"SELECT schema_name, table_name, rows_count, mb, date_col, date_indexed, key_col, oldest FROM @gt ORDER BY n;"} {
		if !strings.Contains(q, want) {
			t.Errorf("growing tables read lacks %q", want)
		}
	}
	for _, gone := range []string{"IntegrationLog", "COL_LENGTH", "[Date]", "@lt"} {
		if strings.Contains(q, gone) {
			t.Errorf("growing tables read still names %q", gone)
		}
	}
}

// The archive-first read of QRY01 and the query page is the Storage card's own block of the tables
// that only grow, nothing more: tagged (the batch and the dynamic SQL inside), run inside the
// database, and read-only (its INSERT and UPDATE are on @gt).
func TestGrowingTablesOnlyQuery(t *testing.T) {
	if !strings.Contains(storageDatabaseQuery, growingTablesBlock) || !strings.Contains(storageDatabaseQuery, growingTablesSelect) {
		t.Error("the Storage card's read does not hold the shared block")
	}
	if !strings.Contains(growingTablesQuery, growingTablesBlock) || !strings.Contains(growingTablesQuery, growingTablesSelect) ||
		!strings.Contains(growingTablesQuery, "DECLARE @growError nvarchar(2048);") || !strings.Contains(growingTablesQuery, "SELECT @growError;") {
		t.Errorf("growing tables query:\n%s", growingTablesQuery)
	}
	if n := strings.Count(growingTablesQuery, "BEGIN TRY"); n != 2 {
		t.Errorf("%d TRY blocks, want the block's own two", n)
	}
	for _, other := range []string{"@ident", "@tables", "@space", "sys.dm_io_virtual_file_stats", "sys.dm_db_log_info"} {
		if strings.Contains(growingTablesQuery, other) {
			t.Errorf("growing tables query reads more than the block: %q", other)
		}
	}
	batch := inDatabaseQuoted("Warehouse]Live")
	if !strings.HasPrefix(batch, KakapoTag+" "+safeSession) || !strings.Contains(batch, "EXEC [Warehouse]]Live].sys.sp_executesql @stmt;") {
		t.Errorf("in-database batch %q", batch)
	}
	for name, q := range map[string]string{"growing tables": Tag(growingTablesQuery), "in database": batch} {
		if !strings.HasPrefix(q, KakapoTag+" ") || strings.HasPrefix(q, KakapoTag+" "+KakapoTag) {
			t.Errorf("%s is not tagged once: %.60s", name, q)
		}
		readOnly(t, name, q)
	}
	if !strings.Contains(growingTablesQuery, "N'"+KakapoTag+" SELECT @o") {
		t.Error("the oldest-row read must carry the kakapo tag")
	}
}

// TestGrowingTablesLive reads the tables that only grow of one database on a real instance,
// read-only, when SQLMONITOR_TEST_HOST and SQLMONITOR_TEST_DB name one: the same tables as the
// Storage card's read.
func TestGrowingTablesLive(t *testing.T) {
	host, name := os.Getenv("SQLMONITOR_TEST_HOST"), os.Getenv("SQLMONITOR_TEST_DB")
	if host == "" || name == "" {
		t.Skip("set SQLMONITOR_TEST_HOST and SQLMONITOR_TEST_DB to read a live instance")
	}
	ctx := context.Background()
	db, err := Open(ctx, domain.ServerInput{Host: host, Auth: domain.AuthWindows})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	tables, growErr, err := GrowingTables(ctx, db, name)
	if err != nil || growErr != "" {
		t.Fatalf("read: %v %q", err, growErr)
	}
	f, err := StorageFacts(ctx, db, name)
	if err != nil {
		t.Fatal(err)
	}
	if len(tables) != len(f.GrowingTables) {
		t.Errorf("%d tables, the Storage card read %d", len(tables), len(f.GrowingTables))
	}
	for i, x := range tables {
		t.Logf("%s.%s rows %d mb %.1f date %q indexed %v key %q oldest %v", x.Schema, x.Name, x.Rows, x.MB, x.DateCol, x.DateIndexed, x.KeyCol, x.Oldest)
		if i < len(f.GrowingTables) && (x.Schema != f.GrowingTables[i].Schema || x.Name != f.GrowingTables[i].Name) {
			t.Errorf("table %d: %s.%s against %s.%s", i, x.Schema, x.Name, f.GrowingTables[i].Schema, f.GrowingTables[i].Name)
		}
	}
}

// The storage instance read, with tempdb's growth events and startup sizes (docs/phase4e-contract.md),
// is tagged and only reads: its INSERTs and its one UPDATE are on table variables.
func TestStorageInstanceQueryReadOnly(t *testing.T) {
	q := storageInstanceQuery
	if !strings.HasPrefix(Tag(q), KakapoTag+" ") || !strings.HasPrefix(q, safeSession) {
		t.Error("storage instance query is not tagged and safe")
	}
	// ALTER TRACE is only the permission it checks for; no other ALTER.
	if strings.Count(q, "ALTER ") != strings.Count(q, "ALTER TRACE") {
		t.Error("storage instance query has an ALTER")
	}
	for _, write := range []string{"DELETE", "DROP ", "DBCC ", "INSERT INTO", "TRUNCATE", "RECONFIGURE", "sp_configure", "MODIFY FILE", "sp_trace_set"} {
		if strings.Contains(q, write) {
			t.Errorf("storage instance query writes: %q", write)
		}
	}
	for _, line := range strings.Split(q, "\n") {
		if i := strings.Index(line, "INSERT "); i >= 0 && !strings.HasPrefix(line[i:], "INSERT @") {
			t.Errorf("inserts into a table: %q", line)
		}
		if i := strings.Index(line, "UPDATE "); i >= 0 && !strings.HasPrefix(line[i:], "UPDATE f SET") {
			t.Errorf("updates a table: %q", line)
		}
	}
	if !strings.Contains(q, "FROM @files AS f CROSS APPLY") {
		t.Error("the UPDATE alias f must be the @files table variable")
	}
}

// TestStorageFactsLive reads the storage facts of one database on a real instance, read-only,
// and runs the CHECKDB estimate (ESTIMATEONLY checks and changes nothing), when
// SQLMONITOR_TEST_HOST (Windows authentication) and SQLMONITOR_TEST_DB name one.
func TestStorageFactsLive(t *testing.T) {
	host, name := os.Getenv("SQLMONITOR_TEST_HOST"), os.Getenv("SQLMONITOR_TEST_DB")
	if host == "" || name == "" {
		t.Skip("set SQLMONITOR_TEST_HOST and SQLMONITOR_TEST_DB to read a live instance")
	}
	ctx := context.Background()
	db, err := Open(ctx, domain.ServerInput{Host: host, Auth: domain.AuthWindows})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	f, err := StorageFacts(ctx, db, name)
	if err != nil {
		t.Fatal(err)
	}
	if f.State == "" || f.ServerNow.IsZero() || len(f.DatabaseFiles()) == 0 || len(f.TempdbFiles()) == 0 {
		t.Errorf("facts: %+v", f)
	}
	for _, x := range f.Files {
		t.Logf("file %s %s %s %d MB used %v growth %d%v max %d on %q free %d MB io %v %d/%d",
			x.Database, x.Type, x.LogicalName, x.SizeMB, x.UsedMB, x.Growth, x.IsPercentGrowth, x.MaxSizePages,
			x.Volume, x.VolumeFreeMB, x.HasIO, x.Reads, x.Writes)
	}
	t.Logf("state %s, %s, reuse %s, edition %d, cpu %d, vlf %v, tempdb %v/%v log %v, tables %d (%.0f + %.0f MB), top %d, tables that only grow %+v, identities %d",
		f.State, f.RecoveryModel, f.LogReuseWait, f.EngineEdition, f.CPUCount, f.VLFCount, f.TempdbAllocMB, f.TempdbFreeMB,
		f.TempdbLogUsedPct, f.TableCount, f.TotalDataMB, f.TotalIndexMB, len(f.Tables), f.GrowingTables, len(f.Identities))
	t.Logf("errors: files %q vol %q io %q cpu %q vlf %q temp %q space %q tables %q grow %q ident %q",
		f.FilesError, f.VolumeError, f.LatencyError, f.CPUError, f.VLFError, f.TempdbError, f.SpaceUsedError,
		f.TablesError, f.GrowingTablesError, f.IdentityError)

	kb, took, err := RunCheckdbEstimate(ctx, db, name)
	if err != nil || kb <= 0 {
		t.Errorf("estimate: %d %v %v", kb, took, err)
	}
	t.Logf("estimate %d KB in %v", kb, took)

	if _, err := StorageFacts(ctx, db, "NoSuchDatabase_x9"); err == nil {
		t.Error("an unknown database should be an error")
	}
}
