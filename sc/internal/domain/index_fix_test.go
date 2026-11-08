package domain

import (
	"reflect"
	"strings"
	"testing"
)

// pickFacts is dbo.Pick in WarehouseLive on a Standard Edition SQL Server 2022: 2 million rows, ten
// columns, a clustered primary key on ID and one index on Location.
func pickFacts() IndexFacts {
	return IndexFacts{
		ProductVersion: "16.0.4135.4", EngineEdition: 2, Database: "WarehouseLive", Schema: "dbo", Table: "Pick",
		Found: true, Rows: 2000000, DataMB: 400,
		Columns: []TableColumn{
			{"ID", "int", 4}, {"Status", "int", 4}, {"Date", "datetime", 8}, {"Qty", "decimal", 9},
			{"Location", "nvarchar", 200}, {"Notes", "nvarchar", -1}, {"Code", "varchar", 20}, {"Flag", "bit", 1},
			{"Owner", "int", 4}, {"Batch", "int", 4},
		},
		Indexes: []ExistingIndex{
			{Name: "PK_Pick", IndexID: 1, Type: 1, IsUnique: true, IsPrimaryKey: true, Keys: []IndexKey{{Name: "ID"}}, SizeMB: 400, Reads: 5000, Writes: 9000},
			{Name: "IX_Pick_Location", IndexID: 2, Type: 2, Keys: []IndexKey{{Name: "Location"}}, SizeMB: 50, Reads: 100, Writes: 2000},
		},
	}
}

func pickSpec(t *testing.T, opts map[string]string) CreateIndexSpec {
	t.Helper()
	s, err := ParseCreateIndexOptions(opts)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

var pickOptions = map[string]string{
	OptionTable: "[dbo].[Pick]", OptionEquality: "[Status]", OptionInequality: "[Date]", OptionIncluded: "[Qty]",
	OptionObject: "[WarehouseLive].[dbo].[Pick]",
}

func TestCreateIndexPreview(t *testing.T) {
	f := pickFacts()
	p, fix := BuildCreateIndexFix(f, pickSpec(t, pickOptions), pickOptions)
	wantStmt := "CREATE NONCLUSTERED INDEX [IX_Pick_Status_Date_kakapo] ON [WarehouseLive].[dbo].[Pick] ([Status], [Date]) INCLUDE ([Qty]) WITH (ONLINE = OFF, SORT_IN_TEMPDB = ON, DATA_COMPRESSION = PAGE);"
	if p.Statement != wantStmt {
		t.Errorf("statement\n%s\nwant\n%s", p.Statement, wantStmt)
	}
	if p.Rollback != "DROP INDEX [IX_Pick_Status_Date_kakapo] ON [WarehouseLive].[dbo].[Pick];" {
		t.Errorf("rollback %s", p.Rollback)
	}
	target := "IX_Pick_Status_Date_kakapo on dbo.Pick (Status, Date) include (Qty), about 66 MB, offline"
	if len(p.Changes) != 1 || p.Changes[0] != (Drift{Setting: "Index", Current: "none", Target: target}) {
		t.Errorf("changes %+v", p.Changes)
	}
	if fix.ID != FixCreateIndex || fix.CheckID != "WRK03" || fix.Target != target || fix.Current != "none" {
		t.Errorf("fix %+v", fix)
	}
	offline := "On Standard or Web edition an index is built offline (ONLINE = OFF): while it builds, dbo.Pick (2,000,000 rows, 400 MB) is locked against inserts, updates and deletes, and users who write to it wait until it finishes (reads go on). Apply it in a quiet moment."
	want := []string{offline, warnIndexLarge, warnIndexLock, warnIndexWrites,
		"Needs the ALTER permission on dbo.Pick (db_ddladmin or db_owner in the database)."}
	if !reflect.DeepEqual(p.Warnings, want) {
		t.Errorf("warnings\n%q\nwant\n%q", p.Warnings, want)
	}
	// Standard Edition: no online input; the options are echoed with the name and the included columns.
	if len(p.Inputs) != 2 || p.Inputs[0].Name != OptionIndexName || p.Inputs[1].Name != OptionIncluded || p.Inputs[1].Value != "[Qty]" {
		t.Errorf("inputs %+v", p.Inputs)
	}
	if p.Options[OptionIndexName] != "IX_Pick_Status_Date_kakapo" || p.Options[OptionObject] != "[WarehouseLive].[dbo].[Pick]" ||
		p.Options[OptionTable] != "[dbo].[Pick]" || p.Options[OptionIncluded] != "[Qty]" {
		t.Errorf("options %+v", p.Options)
	}
	if _, ok := p.Options[OptionOnline]; ok {
		t.Error("online echoed on Standard Edition")
	}
}

func TestCreateIndexOnlineAndCompression(t *testing.T) {
	f := pickFacts()
	f.EngineEdition = 3
	p, fix := BuildCreateIndexFix(f, pickSpec(t, pickOptions), pickOptions)
	if !strings.Contains(p.Statement, "WITH (ONLINE = ON, SORT_IN_TEMPDB = ON, DATA_COMPRESSION = PAGE);") || !strings.HasSuffix(fix.Target, ", online") {
		t.Errorf("enterprise: %s / %s", p.Statement, fix.Target)
	}
	if len(p.Inputs) != 3 || p.Inputs[2].Name != OptionOnline || p.Inputs[2].Value != "true" || p.Options[OptionOnline] != "true" {
		t.Errorf("online input %+v %+v", p.Inputs, p.Options)
	}
	if p.Warnings[0] != warnIndexOnline {
		t.Errorf("online warning %q", p.Warnings[0])
	}
	off := map[string]string{OptionOnline: "false"}
	for k, v := range pickOptions {
		off[k] = v
	}
	p, _ = BuildCreateIndexFix(f, pickSpec(t, off), off)
	if !strings.Contains(p.Statement, "ONLINE = OFF") || p.Options[OptionOnline] != "false" {
		t.Errorf("online off: %s", p.Statement)
	}
	if !strings.HasPrefix(p.Warnings[0], "Built offline (ONLINE = OFF): while it builds, dbo.Pick (2,000,000 rows, 400 MB) is locked") {
		t.Errorf("offline on Enterprise: %q", p.Warnings[0])
	}
	f.EngineEdition = 4
	if p, _ = BuildCreateIndexFix(f, pickSpec(t, pickOptions), pickOptions); !strings.HasPrefix(p.Warnings[0], "On Express edition an index is built offline") {
		t.Errorf("express: %q", p.Warnings[0])
	}

	for version, want := range map[string]bool{"12.0.6024.0": false, "13.0.1601.5": false, "13.0.4001.0": true, "15.0.2000.5": true} {
		f := pickFacts()
		f.ProductVersion = version
		p, _ := BuildCreateIndexFix(f, pickSpec(t, pickOptions), pickOptions)
		if got := strings.Contains(p.Statement, "DATA_COMPRESSION = PAGE"); got != want {
			t.Errorf("%s compression %v: %s", version, got, p.Statement)
		}
	}
}

func guardsOf(t *testing.T, f IndexFacts, opts map[string]string) []IndexGuard {
	t.Helper()
	return createIndexGuards(f, resolveIndex(f, pickSpec(t, opts)))
}

func TestCreateIndexGuards(t *testing.T) {
	if g := guardsOf(t, pickFacts(), pickOptions); len(g) != 0 {
		t.Errorf("clean table: %+v", g)
	}

	// IDX04: an index with exactly these keys; IDX08: one that starts with them, and one they start with.
	for _, c := range []struct {
		keys  []IndexKey
		check string
		text  string
	}{
		{[]IndexKey{{Name: "Status"}, {Name: "Date"}}, "IDX04", "Index IX_Old already starts with these columns (Status, Date); widen it instead of adding one."},
		{[]IndexKey{{Name: "status"}, {Name: "Date", Descending: true}, {Name: "Code"}}, "IDX08", "Index IX_Old already starts with these columns (status, Date, Code); widen it instead of adding one."},
		{[]IndexKey{{Name: "Status"}}, "IDX08", "Index IX_Old (Status) is the start of this one; widen it instead of adding one, or it becomes an overlapping index nobody needs."},
	} {
		f := pickFacts()
		f.Indexes = append(f.Indexes, ExistingIndex{Name: "IX_Old", IndexID: 3, Type: 2, Keys: c.keys})
		g := guardsOf(t, f, pickOptions)
		if len(g) != 1 || g[0].Check != c.check || !g[0].Blocks || g[0].Text != c.text {
			t.Errorf("%v: %+v", c.keys, g)
		}
		p, _ := BuildCreateIndexFix(f, pickSpec(t, pickOptions), pickOptions)
		if len(p.Changes) != 0 || p.Statement != "" || p.Warnings[0] != c.text {
			t.Errorf("blocked preview %+v", p)
		}
	}

	// A unique index they start with is not widened: no guard.
	f := pickFacts()
	f.Indexes = append(f.Indexes, ExistingIndex{Name: "UQ_Status", IndexID: 3, Type: 2, IsUnique: true, Keys: []IndexKey{{Name: "Status"}}})
	if g := guardsOf(t, f, pickOptions); len(g) != 0 {
		t.Errorf("unique: %+v", g)
	}

	// The same keys in another order: Info, does not block.
	f = pickFacts()
	f.Indexes = append(f.Indexes, ExistingIndex{Name: "IX_DS", IndexID: 3, Type: 2, Keys: []IndexKey{{Name: "Date"}, {Name: "Status"}}})
	g := guardsOf(t, f, pickOptions)
	if len(g) != 1 || g[0].Check != "ORDER" || g[0].Blocks ||
		g[0].Text != "Index IX_DS has the same key columns in another order (Date, Status): check whether it serves this query before adding another." {
		t.Errorf("order: %+v", g)
	}

	// IDX02: ten indexes already; does not block.
	f = pickFacts()
	for i := 0; i < 9; i++ {
		f.Indexes = append(f.Indexes, ExistingIndex{Name: "IX_" + string(rune('A'+i)), IndexID: 3 + i, Type: 2, Keys: []IndexKey{{Name: "Batch"}, {Name: string(rune('A' + i))}}, SizeMB: 10})
	}
	g = guardsOf(t, f, pickOptions)
	if len(g) != 1 || g[0].Check != "IDX02" || g[0].Blocks ||
		g[0].Text != "dbo.Pick already has 10 indexes using 140 MB for 400 MB of data: every index slows down inserts, updates and deletes, so make sure this one is worth it." {
		t.Errorf("idx02 count: %+v", g)
	}
	// IDX02 by size: index bytes twice the data.
	f = pickFacts()
	f.Indexes[1].SizeMB = 900
	if g = guardsOf(t, f, pickOptions); len(g) != 1 || g[0].Check != "IDX02" {
		t.Errorf("idx02 size: %+v", g)
	}

	// IDX09: five included columns making half the table: blocks, and the included input says why.
	wide := map[string]string{OptionIncluded: "[Qty], [Location], [Code], [Flag], [Owner]"}
	for k, v := range pickOptions {
		if k != OptionIncluded {
			wide[k] = v
		}
	}
	g = guardsOf(t, pickFacts(), wide)
	if len(g) != 1 || g[0].Check != "IDX09" || !g[0].Blocks ||
		g[0].Text != "This index would hold 7 of the table's 10 columns: a second copy of the table. Include only the columns the query needs (edit the included columns above)." {
		t.Errorf("idx09: %+v", g)
	}
	p, _ := BuildCreateIndexFix(pickFacts(), pickSpec(t, wide), wide)
	if len(p.Changes) != 0 || p.Inputs[1].Warning != g[0].Text {
		t.Errorf("idx09 preview %+v", p)
	}
	// With a guard that only warns found first, the blocking one still leads the warnings.
	f = pickFacts()
	f.Indexes = append(f.Indexes, ExistingIndex{Name: "IX_DS", IndexID: 3, Type: 2, Keys: []IndexKey{{Name: "Date"}, {Name: "Status"}}})
	p, _ = BuildCreateIndexFix(f, pickSpec(t, wide), wide)
	if len(p.Changes) != 0 || p.Warnings[0] != g[0].Text || !strings.HasPrefix(p.Warnings[1], "Index IX_DS has the same key columns") {
		t.Errorf("blocking first %q", p.Warnings)
	}

	// Size over 1 GB and a table written far more than read: warnings only.
	f = pickFacts()
	f.Rows = 100000000
	f.Indexes[0].Reads, f.Indexes[0].Writes = 100, 50000
	g = guardsOf(t, f, pickOptions)
	if len(g) != 2 || g[0].Check != "SIZE" || g[0].Blocks || g[1].Check != "WRITES" || g[1].Blocks ||
		g[0].Text != "The index will be about 3.2 GB: building it takes time, reads the whole table and writes about that much to the transaction log." ||
		g[1].Text != "dbo.Pick is written far more than it is read (50,000 writes, 100 reads since the restart): every index adds work to each insert, update and delete." {
		t.Errorf("size and writes: %+v", g)
	}
	p, _ = BuildCreateIndexFix(f, pickSpec(t, pickOptions), pickOptions)
	if len(p.Changes) != 1 {
		t.Errorf("warnings block: %+v", p)
	}
	// Usage not readable: no write guard.
	f.UsageError = "denied"
	if g = guardsOf(t, f, pickOptions); len(g) != 1 {
		t.Errorf("usage error: %+v", g)
	}

	// A column that is not there, a (max) key: both block.
	bad := map[string]string{OptionTable: "dbo.Pick", OptionEquality: "[Notes]", OptionIncluded: "[Gone]"}
	f = pickFacts()
	f.Rows = 1000
	g = guardsOf(t, f, bad)
	if len(g) != 2 || !g[0].Blocks || !g[1].Blocks ||
		g[0].Text != "Column Gone is not in dbo.Pick (any more): the suggestion is out of date." ||
		g[1].Text != "Column Notes is a nvarchar(max) column and cannot be an index key: include it instead." {
		t.Errorf("columns: %+v", g)
	}

	// A (max) or xml column included: warned with the rows it is copied for, not blocked; a text
	// column cannot be included at all.
	f = pickFacts()
	f.Rows = 13000000
	f.Columns = append(f.Columns, TableColumn{"Payload", "xml", -1}, TableColumn{"Memo", "text", 16})
	inc := map[string]string{OptionTable: "dbo.Pick", OptionEquality: "[Status]", OptionIncluded: "[Notes], [Payload]"}
	g = guardsOf(t, f, inc)
	want := "Column Notes is a nvarchar(max) column: the index keeps its own copy of it for every row of the table (13,000,000 rows), so the index can grow as large as the table"
	if len(g) < 2 || g[0].Blocks || g[1].Blocks || !strings.HasPrefix(g[0].Text, want) || !strings.HasPrefix(g[1].Text, "Column Payload is a xml column: the index keeps") {
		t.Errorf("max includes: %+v", g)
	}
	inc[OptionIncluded] = "[Memo]"
	if g = guardsOf(t, f, inc); len(g) == 0 || !g[0].Blocks || g[0].Text != "Column Memo is a text column: SQL Server cannot include it in an index. Leave it out of the included columns." {
		t.Errorf("text include: %+v", g)
	}
	if p, _ := BuildCreateIndexFix(f, pickSpec(t, inc), inc); p.Statement != "" {
		t.Errorf("a text column was included: %s", p.Statement)
	}
}

func TestParseCreateIndexOptions(t *testing.T) {
	s := pickSpec(t, map[string]string{OptionTable: "[Warehouse Live].[dbo].[Pick]]s]", OptionEquality: "[Status], [Date]",
		OptionIncluded: "Qty , [Status]", OptionIndexName: " IX_Mine ", OptionOnline: "false"})
	if s.Database != "Warehouse Live" || s.Schema != "dbo" || s.Table != "Pick]s" || !reflect.DeepEqual(s.Equality, []string{"Status", "Date"}) ||
		len(s.Inequality) != 0 || !reflect.DeepEqual(s.Included, []string{"Qty", "Status"}) || s.Name != "IX_Mine" || s.Online == nil || *s.Online {
		t.Errorf("spec %+v", s)
	}
	// An included column that is also a key is left out of INCLUDE.
	p := resolveIndex(pickFacts(), s)
	if !reflect.DeepEqual(p.keys, []string{"Status", "Date"}) || !reflect.DeepEqual(p.included, []string{"Qty"}) || p.name != "IX_Mine" {
		t.Errorf("plan %+v", p)
	}
	if s := pickSpec(t, map[string]string{OptionTable: "Pick", OptionInequality: "[Date]"}); s.Schema != "dbo" || s.Table != "Pick" {
		t.Errorf("bare table %+v", s)
	}
	for name, opts := range map[string]map[string]string{
		"no table":     {OptionEquality: "[A]"},
		"no keys":      {OptionTable: "dbo.T", OptionIncluded: "[A]"},
		"unclosed":     {OptionTable: "dbo.T", OptionEquality: "[A"},
		"empty column": {OptionTable: "dbo.T", OptionEquality: "[A], , [B]"},
		"four parts":   {OptionTable: "s.d.dbo.T", OptionEquality: "[A]"},
		"after quote":  {OptionTable: "dbo.T", OptionEquality: "[A]x"},
	} {
		if _, err := ParseCreateIndexOptions(opts); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
	long := strings.Repeat("Column", 30)
	if n := DefaultIndexName("Pick", []string{long, "B", "C", "D"}); len(n) != 128 || !strings.HasSuffix(n, "_kakapo") || !strings.HasPrefix(n, "IX_Pick_Column") {
		t.Errorf("long name %d %s", len(n), n)
	}
	if n := DefaultIndexName("Pick-List", []string{"A b", "B", "C", "D"}); n != "IX_PickList_Ab_B_C_kakapo" {
		t.Errorf("name %s", n)
	}
}

func TestCreateIndexReadBack(t *testing.T) {
	f := pickFacts()
	s := pickSpec(t, pickOptions)
	got := &ExistingIndex{Name: "IX_Pick_Status_Date_kakapo", IndexID: 5, Type: 2, Keys: []IndexKey{{Name: "Status"}, {Name: "Date"}}, Included: []string{"Qty"}, SizeMB: 61.25}
	after, ok := CreateIndexReadBack(f, s, got)
	if !ok || after != "IX_Pick_Status_Date_kakapo on dbo.Pick (Status, Date) include (Qty), 61.3 MB" {
		t.Errorf("read back %v %s", ok, after)
	}
	got.Included = nil
	if _, ok := CreateIndexReadBack(f, s, got); ok {
		t.Error("missing include verified")
	}
	if after, ok := CreateIndexReadBack(f, s, nil); ok || after != "IX_Pick_Status_Date_kakapo was not found on dbo.Pick after the create" {
		t.Errorf("gone %v %s", ok, after)
	}
	_, fix := BuildCreateIndexFix(f, s, pickOptions)
	if d := CreateIndexChangeDescription(fix, "WarehouseLive"); d != "Created index IX_Pick_Status_Date_kakapo on dbo.Pick (Status, Date) include (Qty), about 66 MB, offline in WarehouseLive, for WRK03" {
		t.Errorf("description %s", d)
	}
	if o := IndexObject("dbo", "Pick", "IX_Pick_Status_Date_kakapo"); o != "dbo.Pick.IX_Pick_Status_Date_kakapo" {
		t.Errorf("object %s", o)
	}
}

func TestDropIndexFix(t *testing.T) {
	f := pickFacts()
	f.Indexes = append(f.Indexes, ExistingIndex{Name: "IX_Status_kakapo", IndexID: 3, Type: 2, IsUnique: true, Filter: "([Status]=(1))",
		Keys: []IndexKey{{Name: "Status"}, {Name: "Date", Descending: true}}, Included: []string{"Qty", "Code"}, SizeMB: 12, Reads: 7, Writes: 900})
	p, fix, err := BuildDropIndexFix(f, "ix_status_kakapo", map[string]string{OptionIndex: "ix_status_kakapo", OptionTable: "dbo.Pick"})
	if err != nil {
		t.Fatal(err)
	}
	if p.Statement != "DROP INDEX [IX_Status_kakapo] ON [WarehouseLive].[dbo].[Pick];" ||
		p.Rollback != "CREATE UNIQUE NONCLUSTERED INDEX [IX_Status_kakapo] ON [WarehouseLive].[dbo].[Pick] ([Status], [Date] DESC) INCLUDE ([Qty], [Code]) WHERE ([Status]=(1));" {
		t.Errorf("statements\n%s\n%s", p.Statement, p.Rollback)
	}
	cur := "IX_Status_kakapo on dbo.Pick (Status, Date) include (Qty, Code), 12.0 MB"
	if len(p.Changes) != 1 || p.Changes[0] != (Drift{Setting: "Index", Current: cur, Target: "dropped"}) || fix.ID != FixDropIndex || fix.Current != cur {
		t.Errorf("changes %+v %+v", p.Changes, fix)
	}
	if p.Warnings[0] != "Queries that used IX_Status_kakapo may get slower: it was read 7 times and written 900 times since the restart." ||
		p.Options[OptionIndex] != "IX_Status_kakapo" || p.Options[OptionTable] != "[dbo].[Pick]" {
		t.Errorf("warnings %q options %+v", p.Warnings, p.Options)
	}

	p, _, err = BuildDropIndexFix(f, "PK_Pick", nil)
	if err != nil || len(p.Changes) != 0 || p.Statement != "" ||
		p.Warnings[0] != "PK_Pick is the primary key of dbo.Pick: kakapo does not drop it (ALTER TABLE ... DROP CONSTRAINT, by someone who knows what depends on it)." {
		t.Errorf("primary key %+v %v", p, err)
	}
	if _, _, err := BuildDropIndexFix(f, "IX_Nope", nil); err != ErrNoSuchIndex {
		t.Errorf("missing index: %v", err)
	}
	if after, ok := DropIndexReadBack(f, "IX_Status_kakapo", false); !ok || after != "IX_Status_kakapo is gone from dbo.Pick" {
		t.Errorf("drop read back %v %s", ok, after)
	}
}
