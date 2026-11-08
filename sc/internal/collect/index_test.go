package collect

import (
	"context"
	"database/sql"
	"os"
	"strings"
	"testing"

	"sqlmonitor/internal/domain"
)

// The index read for one table or every table, the name search and the use counters' window
// (docs/phase5o-contract.md, sections 1, 3 and 4) are tagged, read-only and run each area in its TRY.
func TestIndexReadsTaggedAndReadOnly(t *testing.T) {
	for name, q := range map[string]string{"index facts": TagStatements(indexFactsQuery), "name search": indexNameSearchQuery, "usage window": usageWindowQuery} {
		if !strings.HasPrefix(q, KakapoTag+" ") || strings.HasPrefix(q, KakapoTag+" "+KakapoTag) {
			t.Errorf("%s query is not tagged once: %.60s", name, q)
		}
		readOnly(t, name, q)
		if strings.Contains(strings.ToUpper(q), "EXEC ") {
			t.Errorf("%s query executes something", name)
		}
	}
	for name, c := range map[string]struct {
		q    string
		trys int
	}{"index facts": {indexFactsQuery, 4}, "name search": {indexNameSearchQuery, 3}, "usage window": {usageWindowQuery, 2}} {
		if n := strings.Count(c.q, "BEGIN TRY"); n != c.trys || strings.Count(c.q, "BEGIN CATCH") != c.trys {
			t.Errorf("%s has %d TRY blocks, want %d", name, n, c.trys)
		}
	}
	// One query for one table and every table: @table NULL reads every user table, the columns only for one.
	for _, s := range []string{"IF @table IS NULL", "WHERE t.is_ms_shipped = 0", "IF @table IS NOT NULL", "fk.key_index_id", "fk.referenced_object_id",
		"CASE WHEN @useError IS NULL THEN", "ELSE -1 END", "sys.dm_db_partition_stats", "sys.dm_db_index_usage_stats"} {
		if !strings.Contains(indexFactsQuery, s) {
			t.Errorf("index facts lacks %q", s)
		}
	}
	// The search: three areas, each TOP (3), the name as a LIKE pattern with ! as the escape.
	if n := strings.Count(indexNameSearchQuery, "TOP (3)"); n != 3 {
		t.Errorf("name search has %d TOP (3), want 3", n)
	}
	for _, s := range []string{"sys.sql_modules", "sys.plan_guides", "sys.query_store_query_text", "DATEADD(DAY, -30, SYSDATETIMEOFFSET())",
		"LIKE @pattern ESCAPE N'!'", "actual_state_desc IN (N'READ_WRITE', N'READ_ONLY')",
		// Query Store keeps index DDL and kakapo's own statements too; neither fails on a disabled index.
		"qt.query_sql_text NOT LIKE N'%/* kakapo */%'", "h.head NOT LIKE N'CREATE %INDEX %'", "h.head NOT LIKE N'ALTER INDEX %'", "h.head NOT LIKE N'DROP INDEX %'"} {
		if !strings.Contains(indexNameSearchQuery, s) {
			t.Errorf("name search lacks %q", s)
		}
	}
	for _, s := range []string{"sqlserver_start_time", "create_date", "msdb.dbo.restorehistory", "destination_database_name = DB_NAME()"} {
		if !strings.Contains(usageWindowQuery, s) {
			t.Errorf("usage window lacks %q", s)
		}
	}
}

// TestIndexReadsLive reads a real database's indexes, read-only, when SQLMONITOR_TEST_HOST (Windows
// authentication) and SQLMONITOR_TEST_DB name one: the NULL-table path and the per-table path give
// the same indexes for each table; the search for a name nobody uses finds nothing and fails nowhere.
func TestIndexReadsLive(t *testing.T) {
	host, dbName := os.Getenv("SQLMONITOR_TEST_HOST"), os.Getenv("SQLMONITOR_TEST_DB")
	if host == "" || dbName == "" {
		t.Skip("set SQLMONITOR_TEST_HOST and SQLMONITOR_TEST_DB to read a live instance")
	}
	db, err := sql.Open("sqlserver", ConnString(domain.ServerInput{Host: host, Auth: domain.AuthWindows}))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	all, head, err := AllIndexFacts(ctx, db, dbName)
	if err != nil || head.IndexesError != "" || head.ForeignKeysError != "" {
		t.Fatalf("all indexes: %v %+v", err, head)
	}
	t.Logf("%d tables", len(all))
	for i, a := range all {
		if i == 25 {
			break
		}
		one, err := IndexFacts(ctx, db, dbName, a.Schema, a.Table)
		if err != nil || !one.Found || len(one.Columns) == 0 {
			t.Fatalf("%s.%s: %v %+v", a.Schema, a.Table, err, one)
		}
		if len(a.Columns) != 0 {
			t.Errorf("%s.%s: the NULL path read the columns", a.Schema, a.Table)
		}
		if a.Rows != one.Rows || len(a.Indexes) != len(one.Indexes) {
			t.Fatalf("%s.%s: rows %d/%d, indexes %d/%d", a.Schema, a.Table, a.Rows, one.Rows, len(a.Indexes), len(one.Indexes))
		}
		for j := range a.Indexes {
			x, y := a.Indexes[j], one.Indexes[j]
			if x.Name != y.Name || x.IndexID != y.IndexID || x.Type != y.Type || x.IsUnique != y.IsUnique || x.IsDisabled != y.IsDisabled ||
				x.Filter != y.Filter || keyString(x) != keyString(y) || strings.Join(x.Included, ",") != strings.Join(y.Included, ",") ||
				strings.Join(x.ForeignKeys, ",") != strings.Join(y.ForeignKeys, ",") || x.SizeMB != y.SizeMB {
				t.Errorf("%s.%s index %d differs: %+v and %+v", a.Schema, a.Table, j, x, y)
			}
		}
	}
	if missing, err := IndexFacts(ctx, db, dbName, "dbo", "NoSuchTable_x9"); err != nil || missing.Found {
		t.Errorf("missing table %v %+v", err, missing)
	}
	s, err := SearchIndexName(ctx, db, dbName, "IX_kakapo_never_named_x9")
	if err != nil || len(s.Failures()) != 0 || len(s.Places()) != 0 {
		t.Errorf("search %v %+v", err, s)
	}
	w, err := ReadUsageWindow(ctx, db, dbName)
	if err != nil || w.StartedAt.IsZero() || w.ServerNow.IsZero() || w.CreateDate.IsZero() {
		t.Errorf("window %v %+v", err, w)
	}
}

func keyString(x domain.ExistingIndex) string {
	out := []string{}
	for _, k := range x.Keys {
		s := k.Name
		if k.Descending {
			s += " DESC"
		}
		out = append(out, s)
	}
	return strings.Join(out, ",")
}
