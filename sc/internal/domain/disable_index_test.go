package domain

import (
	"strings"
	"testing"
	"time"
)

// The disable-index fix (docs/phase5o-contract.md, sections 3 and 4).

var disableOpts = map[string]string{OptionTable: "[dbo].[Orders]", OptionIndex: "IX_B", OptionKeeper: "IX_A"}

// disableWindow is the use counters' window of ordersFacts: the restart on 27 Oct.
func disableWindow() (*DesignWindow, time.Time) {
	now := time.Date(2026, 11, 8, 10, 0, 0, 0, time.UTC)
	w, _ := UsageWindowFacts{ServerNow: now, StartedAt: time.Date(2026, 10, 27, 6, 0, 0, 0, time.UTC)}.Window()
	return &w, now
}

func cleanSearch() *IndexNameSearch { return &IndexNameSearch{Index: "IX_B"} }

func buildDisable(f IndexFacts, s *IndexNameSearch, opts map[string]string) FixPreview {
	w, now := disableWindow()
	pv, _ := BuildDisableIndexFix(f, s, w, now, opts)
	return pv
}

func TestDisableIndexShown(t *testing.T) {
	w, now := disableWindow()
	pv, fix := BuildDisableIndexFix(ordersFacts(), cleanSearch(), w, now, disableOpts)
	if pv.Held != "" || len(pv.Changes) != 1 || fix.ID != FixDisableIndex || fix.CheckID != "IDX04" || fix.Target != "disabled" ||
		fix.Current != "IX_B on dbo.Orders (CustomerId, OrderDate), 1.2 GB" {
		t.Fatalf("preview %+v fix %+v", pv, fix)
	}
	if pv.Lead != "IX_B is a copy of IX_A: 1.2 GB and 4.3 million writes since 27 Oct, for nothing." {
		t.Errorf("lead %q", pv.Lead)
	}
	if pv.Statement != "ALTER INDEX [IX_B] ON [Shop].[dbo].[Orders] DISABLE;" || pv.Rollback != "ALTER INDEX [IX_B] ON [Shop].[dbo].[Orders] REBUILD WITH (ONLINE = ON);" {
		t.Errorf("statements %q %q", pv.Statement, pv.Rollback)
	}
	want := []string{
		"Queries that read IX_B (5 times since 27 Oct) will read IX_A instead.",
		"Disabling needs a short lock on the table.",
		"Undoing it rebuilds the index from the table: it takes as long as creating it, and the space comes back.",
		"The rollback rebuilds it online (ONLINE = ON): users keep working, but it still needs a short lock at the start and at the end.",
		"The 1.2 GB freed stays inside the data file: the file does not shrink; new rows reuse the space.",
		"kakapo searched the database's code, plan guides and Query Store for the name; code outside SQL Server (an application that names the index in a hint) cannot be searched.",
		"Needs the ALTER permission on dbo.Orders (db_ddladmin or db_owner in the database).",
	}
	if strings.Join(pv.Warnings, "\n") != strings.Join(want, "\n") {
		t.Errorf("warnings\n%s\nwant\n%s", strings.Join(pv.Warnings, "\n"), strings.Join(want, "\n"))
	}
	if pv.Options[OptionIndex] != "IX_B" || pv.Options[OptionKeeper] != "IX_A" || pv.Options[OptionTable] != "[dbo].[Orders]" {
		t.Errorf("options %+v", pv.Options)
	}
	// Names are matched case-insensitively and echoed as the table spells them.
	pv = buildDisable(ordersFacts(), cleanSearch(), map[string]string{OptionTable: "dbo.Orders", OptionIndex: "ix_b", OptionKeeper: "ix_a"})
	if pv.Options[OptionIndex] != "IX_B" || pv.Options[OptionKeeper] != "IX_A" || pv.Statement == "" {
		t.Errorf("case %+v", pv)
	}
}

// Standard edition: the rollback rebuilds offline and says what that locks; Query Store off; usage not read.
func TestDisableIndexOfflineAndNoUsage(t *testing.T) {
	f := ordersFacts()
	f.EngineEdition = 2
	f.UsageError = "denied"
	for i := range f.Indexes {
		f.Indexes[i].Reads, f.Indexes[i].Writes = -1, -1
	}
	s := cleanSearch()
	s.QueryStoreOff = true
	pv := buildDisable(f, s, disableOpts)
	all := strings.Join(pv.Warnings, "\n")
	if pv.Rollback != "ALTER INDEX [IX_B] ON [Shop].[dbo].[Orders] REBUILD;" || pv.Lead != "IX_B is a copy of IX_A: 1.2 GB, for nothing." ||
		!strings.Contains(all, "Queries that read IX_B will read IX_A instead.") ||
		!strings.Contains(all, "The rollback: On Standard or Web edition an index is built offline (ONLINE = OFF)") ||
		!strings.Contains(all, "kakapo searched the database's code and plan guides for the name (Query Store is off)") {
		t.Errorf("offline %q %q\n%s", pv.Rollback, pv.Lead, all)
	}
}

// Every refusal: a warning, no changes, no statement.
func TestDisableIndexRefusals(t *testing.T) {
	cases := []struct {
		name string
		edit func(f *IndexFacts, o map[string]string)
		want string
	}{
		{"missing", func(f *IndexFacts, o map[string]string) { o[OptionIndex] = "IX_Gone" },
			"IX_Gone is not on dbo.Orders any more: the indexes changed since the card was read. Read the card again."},
		{"disabled", func(f *IndexFacts, o map[string]string) { f.Indexes[2].IsDisabled = true },
			"IX_B is already disabled on dbo.Orders: there is nothing to free."},
		{"clustered", func(f *IndexFacts, o map[string]string) {
			o[OptionIndex] = "PK_Orders"
			f.Indexes[0].IsPrimaryKey, f.Indexes[0].IsUnique = false, false
		},
			"PK_Orders is the clustered index of dbo.Orders: disabling it makes the whole table unreadable until it is rebuilt, so kakapo does not."},
		{"columnstore", func(f *IndexFacts, o map[string]string) { f.Indexes[2].Type = 6 },
			"IX_B is not a plain nonclustered index (XML, spatial or columnstore): kakapo does not disable it."},
		{"unique", func(f *IndexFacts, o map[string]string) { f.Indexes[2].IsUnique = true },
			"IX_B is a unique index: it keeps two rows from having the same keys, so it is not a copy kakapo disables."},
		{"primary key", func(f *IndexFacts, o map[string]string) {
			f.Indexes[2].IsUnique, f.Indexes[2].IsPrimaryKey = true, true
		},
			"IX_B is the primary key of dbo.Orders: kakapo does not disable it."},
		{"unique constraint", func(f *IndexFacts, o map[string]string) {
			f.Indexes[2].IsUnique, f.Indexes[2].IsUniqueConstraint = true, true
		},
			"IX_B is a unique constraint on dbo.Orders: kakapo does not disable it."},
		{"foreign key", func(f *IndexFacts, o map[string]string) {
			f.Indexes[2].IsUnique, f.Indexes[2].ForeignKeys = true, []string{"FK_Lines_Orders"}
		}, "Foreign key FK_Lines_Orders references IX_B. Disabling it would switch off foreign key FK_Lines_Orders without an error, and rebuilding the index does not switch the key back on."},
		{"two foreign keys", func(f *IndexFacts, o map[string]string) { f.Indexes[2].ForeignKeys = []string{"FK_1", "FK_2"} },
			"Foreign keys FK_1 and FK_2 reference IX_B. Disabling it would switch off foreign keys FK_1 and FK_2 without an error, and rebuilding the index does not switch the keys back on."},
		{"foreign keys not read", func(f *IndexFacts, o map[string]string) { f.ForeignKeysError = "denied" },
			"Could not read which foreign keys reference IX_B (denied): kakapo does not disable an index a foreign key may use."},
		{"keeper gone", func(f *IndexFacts, o map[string]string) { o[OptionKeeper] = "IX_Old" },
			"IX_Old, the copy that would stay, is not on dbo.Orders any more: the indexes changed since the card was read. Read the card again."},
		{"keeper disabled", func(f *IndexFacts, o map[string]string) { f.Indexes[1].IsDisabled = true },
			"IX_A, the copy that would stay, is disabled: IX_B is the only working copy, so kakapo does not disable it."},
		{"keeper narrower now", func(f *IndexFacts, o map[string]string) { f.Indexes[2].Included = []string{"Status"} },
			"IX_A no longer has the same keys and every column of IX_B: the indexes changed since the card was read. Read the card again."},
		{"keeper's keys changed", func(f *IndexFacts, o map[string]string) { f.Indexes[1].Keys = []IndexKey{{Name: "CustomerId"}} },
			"IX_A no longer has the same keys and every column of IX_B: the indexes changed since the card was read. Read the card again."},
		{"keeper is the index", func(f *IndexFacts, o map[string]string) { o[OptionKeeper] = "IX_B" },
			"No other copy of IX_B was named to keep: kakapo disables a copy only while another holds every one of its columns."},
	}
	for _, c := range cases {
		f := ordersFacts()
		o := map[string]string{}
		for k, v := range disableOpts {
			o[k] = v
		}
		c.edit(&f, o)
		pv := buildDisable(f, cleanSearch(), o)
		if len(pv.Changes) != 0 || pv.Statement != "" || pv.Rollback != "" || len(pv.Warnings) != 1 || pv.Warnings[0] != c.want || pv.Lead != "" {
			t.Errorf("%s: %+v\nwant %s", c.name, pv, c.want)
		}
	}
	// The clustered index on the same keys is a keeper that holds every column.
	f := ordersFacts()
	f.Indexes[0].Keys = []IndexKey{{Name: "CustomerId"}, {Name: "OrderDate"}}
	if pv := buildDisable(f, cleanSearch(), map[string]string{OptionTable: "[dbo].[Orders]", OptionIndex: "IX_A", OptionKeeper: "PK_Orders"}); pv.Statement == "" {
		t.Errorf("clustered keeper refused: %q", pv.Warnings)
	}
}

// The name in a module, a plan guide or a Query Store text holds Apply back; so does an area that failed.
func TestDisableIndexHeld(t *testing.T) {
	text := "SELECT * FROM dbo.Orders WITH (INDEX([IX_B])) WHERE CustomerId = @c"
	s := cleanSearch()
	s.Modules = []IndexNamePlace{{Kind: "SQL_STORED_PROCEDURE", Schema: "dbo", Name: "GetOrders", Text: "CREATE PROCEDURE dbo.GetOrders AS " + text},
		{Kind: "VIEW", Schema: "dbo", Name: "OrdersByB2", Text: "CREATE VIEW dbo.OrdersByB2 AS SELECT 1 FROM Orders WITH (INDEX(IX_B2))"}}
	pv := buildDisable(ordersFacts(), s, disableOpts)
	if pv.Held != "IX_B is named in dbo.GetOrders: a query that names a disabled index fails (error 315). Change the code first, or ask the vendor." ||
		pv.Statement == "" || pv.Rollback == "" {
		t.Errorf("module %q %q", pv.Held, pv.Statement)
	}
	s = cleanSearch()
	s.PlanGuides = []IndexNamePlace{{Kind: PlacePlanGuide, Name: "PG_Orders", Text: "OPTION (TABLE HINT(o, INDEX(IX_B)))"}}
	s.QueryStore = []IndexNamePlace{{Kind: PlaceQueryStore, QueryID: 42, Text: text}, {Kind: PlaceQueryStore, QueryID: 43, Text: text},
		{Kind: PlaceQueryStore, QueryID: 44, Text: text}}
	if pv = buildDisable(ordersFacts(), s, disableOpts); pv.Held != "IX_B is named in plan guide PG_Orders, query 42 in Query Store and query 43 in Query Store: a query that names a disabled index fails (error 315). Change the code first, or ask the vendor." {
		t.Errorf("plan guide and Query Store %q", pv.Held)
	}
	// An area that failed: said in the warnings, and Apply held.
	s = cleanSearch()
	s.PlanGuidesError = "VIEW DEFINITION denied"
	pv = buildDisable(ordersFacts(), s, disableOpts)
	if pv.Held != "Could not search plan guides: VIEW DEFINITION denied. kakapo does not disable an index it could not search for." ||
		!strings.Contains(strings.Join(pv.Warnings, "\n"), "Could not search plan guides: VIEW DEFINITION denied") ||
		strings.Contains(strings.Join(pv.Warnings, "\n"), "kakapo searched") || pv.Statement == "" {
		t.Errorf("area failed %q %q", pv.Held, pv.Warnings)
	}
	s = &IndexNameSearch{Index: "IX_B", Error: "timeout"}
	if pv = buildDisable(ordersFacts(), s, disableOpts); pv.Held != "Could not search for the name: timeout. kakapo does not disable an index it could not search for." {
		t.Errorf("search failed %q", pv.Held)
	}
	if pv = buildDisable(ordersFacts(), nil, disableOpts); !strings.HasPrefix(pv.Held, "Could not search for the name") {
		t.Errorf("no search %q", pv.Held)
	}
}

// The name as a word: IX_B is not IX_B2 or XIX_B; [IX_B] and IX_B, are.
func TestNameAsWord(t *testing.T) {
	for text, want := range map[string]bool{
		"WITH (INDEX([IX_B]))": true, "INDEX(IX_B, IX_C)": true, "ix_b": true, "IX_B": true, "x IX_B\n": true,
		"INDEX(IX_B2)": false, "XIX_B": false, "IX_B_old": false, "IX_BÉ": false, "": false,
	} {
		if got := NameAsWord(text, "IX_B"); got != want {
			t.Errorf("%q: %v, want %v", text, got, want)
		}
	}
	if IndexNamePattern("IX_B") != "%[^A-Za-z0-9_]IX!_B[^A-Za-z0-9_]%" || IndexNamePattern("a[b]%c!") != "%[^A-Za-z0-9_]a![b!]!%c!![^A-Za-z0-9_]%" {
		t.Errorf("patterns %q %q", IndexNamePattern("IX_B"), IndexNamePattern("a[b]%c!"))
	}
}

// The read back and the change row.
func TestDisableIndexReadBackAndChange(t *testing.T) {
	f := ordersFacts()
	f.Indexes[2].IsDisabled, f.Indexes[2].SizeMB = true, 0
	if after, ok := DisableIndexReadBack(f, "IX_B", 1228.8); !ok || after != "IX_B is disabled; 1.2 GB freed inside the file." {
		t.Errorf("read back %q %v", after, ok)
	}
	f.Indexes[2].SizeMB = 2
	if after, ok := DisableIndexReadBack(f, "IX_B", 1228.8); ok || after != "IX_B is disabled but still uses 2 MB" {
		t.Errorf("pages left %q %v", after, ok)
	}
	f.Indexes[2].IsDisabled = false
	if _, ok := DisableIndexReadBack(f, "IX_B", 1228.8); ok {
		t.Error("an enabled index read back as disabled")
	}
	if _, ok := DisableIndexReadBack(f, "IX_Gone", 1); ok {
		t.Error("a missing index read back as disabled")
	}
	if d := DisableIndexChangeDescription(ordersFacts(), "IX_B", "IX_A", 1228.8, "Shop"); d != "Disabled index IX_B on dbo.Orders (a copy of IX_A), 1.2 GB, in Shop, for IDX04" {
		t.Errorf("description %q", d)
	}
}
