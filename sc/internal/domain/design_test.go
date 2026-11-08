package domain

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

var designDef, _ = FindCardDef(CardDesign)

// designNow is the server's clock of every design read here.
var designNow = time.Date(2026, 11, 8, 10, 0, 0, 0, time.UTC)

// dfacts is a database read 38 days after a restart: 39 enabled foreign keys, two without an index on
// large tables (Lines to Orders, Notes to Customers), no deletes yet.
func dfacts() DesignFacts {
	return DesignFacts{Database: "Shop", State: "ONLINE", ServerNow: designNow,
		StartedAt: time.Date(2026, 10, 1, 6, 0, 0, 0, time.UTC), CreateDate: time.Date(2025, 3, 1, 0, 0, 0, 0, time.UTC),
		EnabledForeignKeys: 39, UserTables: 95,
		ForeignKeys: []UnindexedForeignKeyFact{
			{Name: "FK_Lines_Orders", Schema: "dbo", Table: "Lines", Columns: []string{"OrderId"}, RefSchema: "dbo", RefTable: "Orders",
				Rows: 1376845, RefObjectID: 10, OnDelete: "NO_ACTION", OnUpdate: "NO_ACTION"},
			{Name: "FK_Notes_Customers", Schema: "dbo", Table: "Notes", Columns: []string{"CustomerId"}, RefSchema: "dbo", RefTable: "Customers",
				Rows: 20000, RefObjectID: 11, OnDelete: "NO_ACTION", OnUpdate: "NO_ACTION"},
		}}
}

func dbuild(f DesignFacts) (Card, []Finding, []CheckStatus) { return BuildDesignCard(f, designDef) }

func TestDesignCatalogueAndDef(t *testing.T) {
	c, ok := CheckByID("IDX14")
	if !ok || c.Card != CardDesign || c.Title != "Foreign keys without an index" || c.Owner != OwnerUs ||
		c.Question != "Which foreign keys make deletes read a whole table?" || !strings.HasPrefix(c.ThenCheck, "Re-run: IDX14 gone for that key") {
		t.Errorf("IDX14 %+v", c)
	}
	if designDef.Question != "Does the way it is built cost you anything?" ||
		designDef.Note != "Foreign keys without an index and duplicate indexes, and later unused indexes and constraints SQL Server no longer trusts." {
		t.Errorf("design def %+v", designDef)
	}
	if def, _ := FindFixDef(FixCreateIndex); def.CheckID != "WRK03" {
		t.Errorf("create-index's default check %s", def.CheckID)
	}
}

// Fires with deletes and 10,000 rows; not at 9,999; not without deletes (in the fold instead).
func TestIDX14Fires(t *testing.T) {
	f := dfacts()
	f.ForeignKeys[1].RefDeletes = 10
	c, fs, checks := dbuild(f)
	if len(fs) != 1 || fs[0].CheckID != "IDX14" || fs[0].Severity != SeverityLow || fs[0].Object != "[dbo].[Notes]" || fs[0].FixID != FixCreateIndex {
		t.Fatalf("findings %+v", fs)
	}
	want := "Deleting from Customers (10 rows since 1 Oct) makes SQL Server read the whole of Notes (20,000 rows) to check foreign key FK_Notes_Customers: no index on Notes starts with CustomerId."
	if fs[0].Finding != want {
		t.Errorf("finding\n%s\nwant\n%s", fs[0].Finding, want)
	}
	if fs[0].DoThis != "Create an index on Notes (CustomerId). It does not change how the application works; tell the vendor, so their next upgrade keeps it or adds its own." ||
		!strings.Contains(fs[0].Why, "Primary and foreign key constraints") {
		t.Errorf("why/do %q %q", fs[0].Why, fs[0].DoThis)
	}
	for _, s := range []string{"Foreign key FK_Notes_Customers: [dbo].[Notes] ([CustomerId]) references [dbo].[Customers]", "ON DELETE NO_ACTION, ON UPDATE NO_ACTION",
		"Notes has 20,000 rows; 10 rows deleted from Customers since the restart on 1 Oct",
		"Deletes are counted since the restart on 1 Oct and may be more: SQL Server forgets them when it unloads the table's metadata."} {
		if !strings.Contains(fs[0].Evidence, s) {
			t.Errorf("evidence lacks %q: %s", s, fs[0].Evidence)
		}
	}
	if c.Status != StatusAmber || c.StatusText != "1 thing to do" || c.Verdict != "1 foreign key makes deletes read a whole table: Notes (20,000 rows)." ||
		c.ToDo != 1 || c.Fixable != 1 {
		t.Errorf("card %+v", c)
	}
	if len(checks) != 2 || checks[0].CheckID != "IDX14" || checks[0].Status != CheckFinding || checks[1].CheckID != "IDX04" || checks[1].Status != CheckPassed {
		t.Errorf("checks %+v", checks)
	}
	if c.Facts[1] != (Fact{Label: "Costing work now", Value: "1", Tone: ToneBad}) {
		t.Errorf("facts %+v", c.Facts)
	}
	// The other key is in the fold, not raised.
	fold := BuildDesignForeignKeys(f)
	if fold == nil || len(fold.Keys) != 1 || fold.Keys[0].Name != "FK_Lines_Orders" || fold.Keys[0].Text != "Lines (1,376,845 rows): no index on OrderId, its key to Orders" ||
		fold.Heading != DesignFoldHeading || fold.TooEarly || fold.Window != "the restart on 1 Oct" {
		t.Errorf("fold %+v", fold)
	}

	f.ForeignKeys[1].Rows = 9999
	if _, fs, _ := dbuild(f); len(fs) != 0 {
		t.Errorf("9,999 rows raised %+v", fs)
	}
	if fold := BuildDesignForeignKeys(f); len(fold.Keys) != 1 {
		t.Errorf("a small table in the fold %+v", fold.Keys)
	}
	f.ForeignKeys[1].Rows, f.ForeignKeys[1].RefDeletes = 20000, 0
	if _, fs, checks := dbuild(f); len(fs) != 0 || checks[0].Status != CheckPassed {
		t.Errorf("no deletes raised %+v %+v", fs, checks)
	}
	if fold := BuildDesignForeignKeys(f); len(fold.Keys) != 2 || fold.Keys[0].Name != "FK_Lines_Orders" || fold.Keys[1].Name != "FK_Notes_Customers" {
		t.Errorf("fold, largest first %+v", fold.Keys)
	}
}

// Rank 3 from 1,000,000 rows, 4 below; findings ordered by referencing rows times deletes.
func TestIDX14RankAndOrder(t *testing.T) {
	f := dfacts()
	f.ForeignKeys[0].RefDeletes = 1   // 1,376,845
	f.ForeignKeys[1].RefDeletes = 100 // 2,000,000
	c, fs, _ := dbuild(f)
	if len(fs) != 2 || fs[0].Object != "[dbo].[Notes]" || fs[1].Object != "[dbo].[Lines]" {
		t.Fatalf("order %+v", fs)
	}
	if fs[0].Severity != SeverityLow || fs[1].Severity != SeverityMedium {
		t.Errorf("ranks %s %s", fs[0].Severity, fs[1].Severity)
	}
	if c.StatusText != "2 things to do" || c.Verdict != "2 foreign keys make deletes read whole tables, the biggest on Lines (1.4 million rows)." {
		t.Errorf("card %q %q", c.StatusText, c.Verdict)
	}
	f.ForeignKeys[0].Rows = 999999
	if _, fs, _ := dbuild(f); fs[1].Severity != SeverityLow {
		t.Errorf("999,999 rows %s", fs[1].Severity)
	}
	f.ForeignKeys[0].Rows = 1000000
	if _, fs, _ := dbuild(f); fs[1].Severity != SeverityMedium {
		t.Errorf("1,000,000 rows %s", fs[1].Severity)
	}
}

// ON DELETE CASCADE, a key of two columns in the key's order, another schema.
func TestIDX14CascadeAndTwoColumns(t *testing.T) {
	f := dfacts()
	f.ForeignKeys = []UnindexedForeignKeyFact{{Name: "FK_Rows_Batch", Schema: "stock", Table: "Rows", Columns: []string{"BatchNo", "Site"},
		RefSchema: "stock", RefTable: "Batch", Rows: 50000, RefDeletes: 3, OnDelete: "CASCADE", OnUpdate: "NO_ACTION"}}
	_, fs, _ := dbuild(f)
	want := "Deleting from stock.Batch (3 rows since 1 Oct) makes SQL Server read the whole of stock.Rows (50,000 rows) to find and delete the matching rows (ON DELETE CASCADE) of foreign key FK_Rows_Batch: no index on stock.Rows starts with its columns BatchNo, Site."
	if len(fs) != 1 || fs[0].Finding != want {
		t.Fatalf("finding %+v\nwant %s", fs, want)
	}
	if !strings.HasPrefix(fs[0].DoThis, "Create an index on stock.Rows (BatchNo, Site).") || !strings.Contains(fs[0].Evidence, "([BatchNo], [Site])") ||
		!strings.Contains(fs[0].Evidence, "ON DELETE CASCADE") {
		t.Errorf("do/evidence %q %q", fs[0].DoThis, fs[0].Evidence)
	}
	fixes := AvailableDesignFixes(f, fs)
	if len(fixes) != 1 || fixes[0].Options[OptionEquality] != "[BatchNo], [Site]" || fixes[0].Options[OptionTable] != "[Shop].[stock].[Rows]" {
		t.Errorf("fixes %+v", fixes)
	}
}

// Every verdict and its precedence, with the 7-day window from a restart and from a restore.
func TestDesignVerdicts(t *testing.T) {
	// The foreign keys could not be read: grey, before anything else.
	f := dfacts()
	f.ForeignKeysError, f.ForeignKeys[0].RefDeletes = "permission denied", 5
	if c, fs, checks := dbuild(f); c.Status != StatusGrey || c.Note != "Could not read the foreign keys: permission denied" || len(fs) != 0 ||
		checks[0].Status != CheckSkipped || BuildDesignForeignKeys(f) != nil {
		t.Errorf("grey %+v %+v", c, checks)
	}

	// No finding, keys exist, a long window.
	f = dfacts()
	c, _, _ := dbuild(f)
	if c.Status != StatusGreen || c.Verdict != "2 foreign keys have no index, but none has cost work since 1 Oct: nothing was deleted from the tables they point to." {
		t.Errorf("no finding %+v", c)
	}
	wantFacts := []Fact{{"Foreign keys", "39, 2 without an index", ToneNormal}, {"Costing work now", "0", ToneNormal},
		{"Duplicate indexes", "none", ToneNormal}, {"Counted since", "the restart on 1 Oct", ToneNormal}, {"Tables", "95", ToneNormal}}
	for i, x := range wantFacts {
		if c.Facts[i] != x {
			t.Errorf("fact %d %+v, want %+v", i, c.Facts[i], x)
		}
	}

	// A restart 3 days ago: too early to tell.
	f.StartedAt = designNow.Add(-3*24*time.Hour - time.Hour)
	c, _, _ = dbuild(f)
	if c.Status != StatusGreen || c.StatusText != "Too early to tell" ||
		c.Verdict != "Too early to tell which foreign keys cost work: deletes counted for 3 days, since the restart on 5 Nov. 2 foreign keys on large tables have no index." {
		t.Errorf("too early (restart) %+v", c)
	}
	if fold := BuildDesignForeignKeys(f); fold.Heading != DesignFoldTooEarly || !fold.TooEarly || len(fold.Keys) != 2 {
		t.Errorf("fold too early %+v", fold)
	}
	one := f
	one.ForeignKeys = f.ForeignKeys[:1]
	if c, _, _ := dbuild(one); !strings.HasSuffix(c.Verdict, "since the restart on 5 Nov. 1 foreign key on a large table has no index.") {
		t.Errorf("too early, one key %q", c.Verdict)
	}
	// Exactly 7 days is still too early; 7 days and a minute is not.
	f.StartedAt = designNow.Add(-7 * 24 * time.Hour)
	if c, _, _ = dbuild(f); c.StatusText != "Too early to tell" {
		t.Errorf("7 days %+v", c)
	}
	f.StartedAt = designNow.Add(-7*24*time.Hour - time.Minute)
	if c, _, _ = dbuild(f); c.StatusText != "No problem found" {
		t.Errorf("7 days and a minute %+v", c)
	}

	// A restore 3 days ago after a long uptime: too early, and the window names the restore.
	f = dfacts()
	restore := time.Date(2026, 11, 5, 9, 0, 0, 0, time.UTC)
	f.LastRestore = &restore
	c, _, _ = dbuild(f)
	if c.Verdict != "Too early to tell which foreign keys cost work: deletes counted for 3 days, since the restore on 5 Nov. 2 foreign keys on large tables have no index." ||
		c.Facts[3].Value != "the restore on 5 Nov" {
		t.Errorf("too early (restore) %q %+v", c.Verdict, c.Facts)
	}
	// A database created this morning: the creation is the window.
	f.CreateDate = time.Date(2026, 11, 8, 9, 0, 0, 0, time.UTC)
	if c, _, _ = dbuild(f); !strings.Contains(c.Verdict, "deletes counted for less than a day, since the database's creation on 8 Nov.") {
		t.Errorf("created %q", c.Verdict)
	}
	// A finding wins over too early.
	f.ForeignKeys[0].RefDeletes = 10
	if c, _, _ = dbuild(f); c.Status != StatusAmber {
		t.Errorf("finding over too early %+v", c)
	}

	// Keys without an index only on small tables.
	f = dfacts()
	f.ForeignKeys[0].Rows, f.ForeignKeys[1].Rows = 500, 9999
	if c, _, _ = dbuild(f); c.Verdict != "No foreign key cost work since 1 Oct. 2 foreign keys have no index, all on tables under 10,000 rows." {
		t.Errorf("small only %q", c.Verdict)
	}

	// Every foreign key has an index; and none at all.
	f.ForeignKeys = []UnindexedForeignKeyFact{}
	if c, _, _ = dbuild(f); c.Status != StatusGreen || c.Verdict != "Every foreign key has an index." || c.Facts[0].Value != "39, 0 without an index" {
		t.Errorf("every key indexed %+v", c)
	}
	f.EnabledForeignKeys = 0
	if c, _, _ = dbuild(f); c.Verdict != "This database has no foreign keys." {
		t.Errorf("no keys %q", c.Verdict)
	}
}

// Deletes not read (-1): the card says so and raises nothing; the fold still lists the keys.
func TestDesignDeletesNotRead(t *testing.T) {
	f := dfacts()
	f.DeletesError = "VIEW DATABASE STATE permission denied"
	for i := range f.ForeignKeys {
		f.ForeignKeys[i].RefDeletes = -1
	}
	c, fs, checks := dbuild(f)
	if len(fs) != 0 || c.Status != StatusGrey || !strings.Contains(c.Note, "Could not read the deletes on the parent tables") ||
		checks[0].Status != CheckSkipped || c.Facts[1].Value != "could not be read" {
		t.Errorf("deletes not read %+v %+v %+v", c, fs, checks)
	}
	if fold := BuildDesignForeignKeys(f); fold == nil || len(fold.Keys) != 2 {
		t.Errorf("fold %+v", fold)
	}
	if fixes := AvailableDesignFixes(f, fs); len(fixes) != 0 {
		t.Errorf("fixes %+v", fixes)
	}
}

// The fix of a finding: create-index with the key's options, IDX14 as its check; the preview's first line.
func TestDesignFix(t *testing.T) {
	f := dfacts()
	f.ForeignKeys[0].RefDeletes = 812
	_, fs, _ := dbuild(f)
	fixes := AvailableDesignFixes(f, fs)
	if len(fixes) != 1 {
		t.Fatalf("fixes %+v", fixes)
	}
	x := fixes[0]
	want := map[string]string{OptionSource: SourceForeignKey, OptionTable: "[Shop].[dbo].[Lines]", OptionEquality: "[OrderId]", OptionInequality: "",
		OptionIncluded: "", OptionParent: "[dbo].[Orders]", OptionForeignKey: "FK_Lines_Orders", OptionObject: "[dbo].[Lines]"}
	if x.ID != FixCreateIndex || x.CheckID != "IDX14" || len(x.Options) != len(want) {
		t.Errorf("fix %+v", x)
	}
	for k, v := range want {
		if x.Options[k] != v {
			t.Errorf("option %s %q, want %q", k, x.Options[k], v)
		}
	}

	// The preview built from those options: IDX14, the first line, the guards as built.
	facts := IndexFacts{ProductVersion: "16.0.4135.4", EngineEdition: 3, Database: "Shop", Schema: "dbo", Table: "Lines", Found: true, Rows: 1376845, DataMB: 200,
		Columns: []TableColumn{{"Id", "int", 4}, {"OrderId", "int", 4}, {"Qty", "int", 4}},
		Indexes: []ExistingIndex{{Name: "PK_Lines", IndexID: 1, Type: 1, IsUnique: true, IsPrimaryKey: true, Keys: []IndexKey{{Name: "Id"}}, SizeMB: 200}}}
	spec, err := ParseCreateIndexOptions(x.Options)
	if err != nil {
		t.Fatal(err)
	}
	pv, fix := BuildCreateIndexFix(facts, spec, x.Options)
	if fix.CheckID != "IDX14" || pv.Lead != "Deletes from Orders read all 1,376,845 rows of Lines today; with this index they read only the matching rows." ||
		pv.Statement == "" || !strings.Contains(pv.Statement, "([OrderId])") {
		t.Errorf("preview %+v %+v", pv, fix)
	}
	if d := CreateIndexChangeDescription(fix, "Shop"); !strings.HasSuffix(d, " in Shop, for IDX14") {
		t.Errorf("change description %s", d)
	}
	// An index already leading on the key's column blocks, and then there is no first line.
	facts.Indexes = append(facts.Indexes, ExistingIndex{Name: "IX_Lines_OrderId", IndexID: 2, Type: 2, Keys: []IndexKey{{Name: "OrderId"}}})
	if pv, _ = BuildCreateIndexFix(facts, spec, x.Options); pv.Statement != "" || pv.Lead != "" {
		t.Errorf("blocked preview %+v", pv)
	}
	// A suggestion's index keeps WRK03 and has no first line.
	if pv, fix = BuildCreateIndexFix(pickFacts(), pickSpec(t, pickOptions), pickOptions); fix.CheckID != "WRK03" || pv.Lead != "" {
		t.Errorf("WRK03 preview %+v %+v", pv.Lead, fix.CheckID)
	}
}

// The card detail writes foreignKeys for the Design card only.
func TestDesignDetailJSON(t *testing.T) {
	f := dfacts()
	c, fs, checks := dbuild(f)
	d := CardDetail{Card: c, Findings: fs, Checks: checks, ForeignKeys: BuildDesignForeignKeys(f), Fixes: AvailableDesignFixes(f, fs)}
	b, err := json.Marshal(d)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"foreignKeys":{"heading":"Missing indexes on foreign keys, not needed yet","tooEarly":false,"window":"the restart on 1 Oct","keys":[{"name":"FK_Lines_Orders"`) {
		t.Errorf("design JSON %s", b)
	}
	d.Card.ID = CardStorage
	if b, _ = json.Marshal(d); strings.Contains(string(b), "foreignKeys") {
		t.Errorf("storage JSON carries foreignKeys: %s", b)
	}
}

func TestBuildCardsDesign(t *testing.T) {
	f := dfacts()
	cards := BuildCards(nil, nil, nil, nil, nil, nil, &f)
	if cards[5].ID != CardDesign || cards[5].Status != StatusGreen || len(cards[5].Facts) != 5 {
		t.Errorf("design card %+v", cards[5])
	}
	if cards := BuildCards(nil, nil, nil, nil, nil, nil, nil); cards[5].Status != StatusGrey || cards[5].StatusText != "Not checked yet" {
		t.Errorf("grey design %+v", cards[5])
	}
}
