package collect

import (
	"context"
	"database/sql"
	"os"
	"strings"
	"testing"

	"sqlmonitor/internal/domain"
)

// The Design card's read (docs/phase5n-contract.md, section 2) is tagged, SELECTs only and runs each
// area in its own TRY; the foreign keys come from the query page's own query, with @tables NULL meaning
// every referenced table.
func TestDesignReadTaggedAndReadOnly(t *testing.T) {
	for name, q := range map[string]string{"design": designQuery, "unindexed foreign keys": unindexedForeignKeysQuery} {
		if !strings.HasPrefix(q, KakapoTag+" ") {
			t.Errorf("%s query does not start with the tag: %.60s", name, q)
		}
		upper := strings.ToUpper(q)
		for _, verb := range []string{"ALTER ", "DROP ", "TRUNCATE ", "DBCC ", "CREATE ", "SP_CONFIGURE", "KILL ", "MERGE ", "EXEC "} {
			if strings.Contains(upper, verb) {
				t.Errorf("%s query contains %q", name, verb)
			}
		}
		for _, line := range strings.Split(upper, "\n") {
			for _, verb := range []string{"INSERT ", "UPDATE ", "DELETE "} {
				if i := strings.Index(line, verb); i >= 0 && !strings.Contains(line[i:], "@") && !strings.Contains(line[i:], "#") {
					t.Errorf("%s query writes: %s", name, line)
				}
			}
		}
	}
	// The counts, the deletes and the window: three areas, three TRYs.
	if n := strings.Count(designQuery, "BEGIN TRY"); n != 3 || strings.Count(designQuery, "BEGIN CATCH") != 3 {
		t.Errorf("design read has %d TRY blocks, want 3", n)
	}
	for _, s := range []string{"sys.dm_db_index_operational_stats(DB_ID(), NULL, NULL, NULL)", "index_id IN (0, 1)", "leaf_delete_count + os.leaf_ghost_count",
		"sqlserver_start_time", "create_date", "is_disabled = 0", "is_ms_shipped = 0"} {
		if !strings.Contains(designQuery, s) {
			t.Errorf("design read lacks %q", s)
		}
	}
	// One query for the query page and the card: the table filter takes NULL for every table.
	for _, s := range []string{"IF @tables IS NOT NULL", "(@tables IS NULL OR n.object_id IS NOT NULL)", "delete_referential_action_desc",
		"update_referential_action_desc", "fk.referenced_object_id"} {
		if !strings.Contains(unindexedForeignKeysQuery, s) {
			t.Errorf("foreign keys query lacks %q", s)
		}
	}
}

func TestObjectsXML(t *testing.T) {
	keys := []domain.UnindexedForeignKeyFact{{RefObjectID: 10}, {RefObjectID: 11}, {RefObjectID: 10}}
	if got := objectsXML(keys); got != `<o i="10"/><o i="11"/>` {
		t.Errorf("xml %s", got)
	}
	if got := objectsXML(nil); got != "" {
		t.Errorf("empty %q", got)
	}
}

// TestDesignLive reads a real database's design facts, read-only, when SQLMONITOR_TEST_HOST (Windows
// authentication) and SQLMONITOR_TEST_DB name one: the NULL path and the list path give the same keys
// for the same referenced tables.
func TestDesignLive(t *testing.T) {
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
	f, err := ReadDesignFacts(ctx, db, dbName)
	if err != nil {
		t.Fatal(err)
	}
	if f.ForeignKeysError != "" || f.CountsError != "" || f.DeletesError != "" || f.WindowError != "" || f.StartedAt.IsZero() || f.ServerNow.IsZero() {
		t.Fatalf("design facts %+v", f)
	}
	t.Logf("%d enabled foreign keys, %d without an index, %d tables, started %v, created %v",
		f.EnabledForeignKeys, len(f.ForeignKeys), f.UserTables, f.StartedAt, f.CreateDate)

	tables := []domain.ShowplanObject{}
	seen := map[string]bool{}
	for _, k := range f.ForeignKeys {
		if !seen[k.RefSchema+"."+k.RefTable] {
			seen[k.RefSchema+"."+k.RefTable] = true
			tables = append(tables, domain.ShowplanObject{Schema: k.RefSchema, Table: k.RefTable})
		}
	}
	if len(tables) == 0 {
		t.Skip("no foreign key without an index in this database to compare")
	}
	listed, err := ReadUnindexedForeignKeys(ctx, db, dbName, tables)
	if err != nil {
		t.Fatal(err)
	}
	all, err := ReadUnindexedForeignKeys(ctx, db, dbName, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) != len(all) {
		t.Fatalf("list path %d keys, NULL path %d", len(listed), len(all))
	}
	for i := range all {
		a, b := all[i], listed[i]
		if a.Name != b.Name || a.Schema != b.Schema || a.Table != b.Table || a.Rows != b.Rows || a.RefObjectID != b.RefObjectID ||
			strings.Join(a.Columns, ",") != strings.Join(b.Columns, ",") || a.OnDelete != b.OnDelete || a.OnUpdate != b.OnUpdate {
			t.Errorf("key %d differs: %+v and %+v", i, a, b)
		}
	}
}
