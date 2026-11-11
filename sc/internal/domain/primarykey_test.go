package domain

import (
	"encoding/json"
	"strings"
	"testing"
)

// Phase 5s (docs/phase5s-contract.md): DES01 and the add-primary-key fix.

func noKey(table string, rows int64) NoKeyTableFact {
	return NoKeyTableFact{Schema: "dbo", Table: table, Rows: rows, DataMB: float64(rows) / 8000, Heap: true, Unique: []UniqueKeyCandidate{}}
}

func TestDES01Catalogue(t *testing.T) {
	c, ok := CheckByID("DES01")
	if !ok || c.Card != CardDesign || c.Title != "Tables without a primary key" || c.Question != "Which tables have no primary key?" ||
		c.Owner != OwnerVendor || c.ThenCheck != "Re-run: DES01 gone for that table." {
		t.Errorf("DES01 %+v", c)
	}
	if d, ok := FindFixDef(FixAddPrimaryKey); !ok || d.Title != "Add a primary key" || d.Scope != ScopeDatabase || d.CheckID != "DES01" ||
		d.Setting != "Primary key" || d.Card() != CardDesign {
		t.Errorf("fix def %+v", d)
	}
}

// Who fires: 10,000 rows a finding, rank 3 from a million; under 10,000 to the fold; empty nowhere; the
// tables that never are.
func TestDES01Fires(t *testing.T) {
	f := dfacts()
	moves := noKey("StockMoves", 1732636)
	moves.Identity, moves.Nonclustered = "MoveId", 3
	notes := noKey("Notes", 20000)
	notes.Heap, notes.ClusteredIndex = false, "CX_Notes"
	small := noKey("Codes", 2410)
	empty := noKey("Spare", 0)
	history := noKey("Prices_History", 50000)
	history.TemporalType = 1
	mem := noKey("Sessions", 50000)
	mem.MemoryOptimized = true
	archive := noKey("Orders_archive", 90000)
	archive.Heap, archive.ClusteredIndex = false, "CX_kakapo_archive"
	keyed := noKey("Customers", 90000)
	keyed.HasPrimaryKey = true
	f.NoKeyTables = []NoKeyTableFact{notes, small, empty, history, mem, archive, keyed, moves}

	c, fs, checks := dbuild(f)
	if len(fs) != 2 || fs[0].Object != "[dbo].[StockMoves]" || fs[1].Object != "[dbo].[Notes]" {
		t.Fatalf("findings %+v", fs)
	}
	x := fs[0]
	if x.CheckID != "DES01" || x.Severity != SeverityMedium || x.Owner != OwnerVendor || x.When != WhenWindow || x.Effort != EffortModerate ||
		x.FixID != FixAddPrimaryKey || x.Why != des01Why ||
		x.Finding != "StockMoves has no primary key: nothing stops two identical rows among its 1,732,636 rows, and no key names a row. It is a heap too (no clustered index)." ||
		x.DoThis != "Add a primary key on its identity column MoveId, clustered, which also ends the heap. Tell the vendor first: their next upgrade must expect it." ||
		x.Evidence != "Table [dbo].[StockMoves]: 1,732,636 rows, 217 MB, heap; no unique index on NOT NULL columns; identity column [MoveId]; 3 nonclustered indexes (rebuilt with a clustered key)." {
		t.Errorf("StockMoves %+v", x)
	}
	if y := fs[1]; y.Severity != SeverityLow ||
		y.DoThis != "Add a primary key on a new bigint identity column. This writes every row and adds a column, so check with the vendor first that the application names its columns when it inserts into Notes." {
		t.Errorf("Notes %+v", y)
	}
	if c.Status != StatusAmber || c.StatusText != "2 things to do" || c.ToDo != 2 || c.Fixable != 2 ||
		c.Verdict != "2 tables have no primary key, the biggest StockMoves (1.7 million rows)." {
		t.Errorf("card %+v", c)
	}
	if c.Facts[3] != (Fact{Label: "No primary key", Value: "3 tables (2 large)", Tone: ToneBad}) {
		t.Errorf("fact %+v", c.Facts[3])
	}
	if checks[2].CheckID != "DES01" || checks[2].Status != CheckFinding {
		t.Errorf("checks %+v", checks)
	}
	fold := BuildDesignNoKeyTables(f)
	if fold == nil || fold.Heading != DesignNoKeyHeading || len(fold.Tables) != 1 || fold.Tables[0].Text != "Codes (2,410 rows, heap)" {
		t.Errorf("fold %+v", fold)
	}
	fixes := AvailableDesignFixes(f, fs)
	if len(fixes) != 2 || fixes[0].ID != FixAddPrimaryKey || fixes[0].Options[OptionTable] != "[dbo].[StockMoves]" ||
		fixes[0].Options[OptionObject] != "[dbo].[StockMoves]" || fixes[0].Target != "primary key on StockMoves" {
		t.Errorf("fixes %+v", fixes)
	}

	// The fold goes out on the Design card only.
	d := CardDetail{Card: c, Findings: fs, Checks: checks, NoKeyTables: fold}
	if b, _ := json.Marshal(d); !strings.Contains(string(b), `"noKeyTables":{"heading":"Smaller tables without a primary key","tables":[{"table":"Codes"`) {
		t.Errorf("design JSON %s", b)
	}
	d.Card.ID = CardStorage
	if b, _ := json.Marshal(d); strings.Contains(string(b), "noKeyTables") {
		t.Errorf("storage JSON carries noKeyTables: %s", b)
	}

	// A refused table: still a finding, no fix, the reason in the evidence.
	f.NoKeyTables = []NoKeyTableFact{moves}
	f.NoKeyTables[0].Replicated = true
	_, fs, _ = dbuild(f)
	if len(fs) != 1 || fs[0].FixID != "" || !strings.HasSuffix(fs[0].Evidence, "kakapo does not add the key: StockMoves is published for replication: a new key or column changes what is published, so the replication's owner decides.") {
		t.Errorf("refused %+v", fs)
	}

	// A unique clustered index on NOT NULL columns: Info, not to do.
	notes.Unique = []UniqueKeyCandidate{{Index: "CX_Notes", Clustered: true, Columns: []string{"NoteId"}, KeyBytes: 4}}
	f.NoKeyTables = []NoKeyTableFact{notes}
	c, fs, _ = dbuild(f)
	if len(fs) != 1 || fs[0].Severity != SeverityInfo ||
		fs[0].Finding != "Notes has no primary key, but its clustered index CX_Notes is unique on NoteId: it has a key in all but name." ||
		c.Status != StatusGreen || c.ToDo != 0 {
		t.Errorf("all but name %+v %+v", c, fs)
	}

	// Could not be read: the check is skipped and the card grey when nothing else fired.
	f.NoKeyTables, f.NoKeyError = nil, "permission denied"
	c, _, checks = dbuild(f)
	if c.Status != StatusGrey || c.Note != des01Skipped+"permission denied" || checks[2].Status != CheckSkipped || BuildDesignNoKeyTables(f) != nil ||
		c.Facts[3].Value != "could not be read" {
		t.Errorf("grey %+v %+v", c, checks)
	}
}

func TestDES01CandidatesOrder(t *testing.T) {
	tb := noKey("Moves", 50000)
	tb.Unique = []UniqueKeyCandidate{
		{Index: "UX_Wide", Columns: []string{"A", "B"}, KeyBytes: 40},
		{Index: "UX_Narrow", Columns: []string{"C"}, KeyBytes: 4},
		{Index: "CX_Moves", Clustered: true, Columns: []string{"D", "E"}, KeyBytes: 80},
	}
	got := []string{}
	for _, c := range tb.Candidates() {
		got = append(got, c.Index)
	}
	if strings.Join(got, ",") != "CX_Moves,UX_Narrow,UX_Wide" {
		t.Errorf("order %v", got)
	}
	tb.Identity = "MoveId"
	if c := pkChoices(tb); strings.Join(c, "; ") != "Unique index CX_Moves (D, E); Unique index UX_Narrow (C); Unique index UX_Wide (A, B); Identity column MoveId; New column (bigint identity)" {
		t.Errorf("choices %v", c)
	}
}

func pkFacts(t NoKeyTableFact, edition int) PrimaryKeyFacts {
	return PrimaryKeyFacts{Found: true, Table: t, Names: []string{t.Table, "PK_Other"},
		Index: IndexFacts{ProductVersion: "17.0.1000.7", EngineEdition: edition, Database: "Shop", Schema: t.Schema, Table: t.Table, Found: true,
			Rows: t.Rows, DataMB: t.DataMB,
			Columns: []TableColumn{{Name: "MoveId", Type: "int", MaxLength: 4}, {Name: "Ref", Type: "varchar", MaxLength: 20}, {Name: "Line", Type: "int", MaxLength: 4}}}}
}

// The identity column on a heap, on Express: clustered, offline, the statements, the warnings.
func TestAddPrimaryKeyIdentity(t *testing.T) {
	tb := noKey("StockMoves", 1732636)
	tb.Identity, tb.IdentityType, tb.Nonclustered, tb.NonclusteredMB = "MoveId", "int", 3, 40
	p, fix := BuildAddPrimaryKeyFix(pkFacts(tb, 4), map[string]string{OptionTable: "[dbo].[StockMoves]"})
	if p.Statement != "ALTER TABLE [Shop].[dbo].[StockMoves] ADD CONSTRAINT [PK_StockMoves] PRIMARY KEY CLUSTERED ([MoveId]) WITH (ONLINE = OFF, SORT_IN_TEMPDB = ON);" ||
		p.Rollback != "ALTER TABLE [Shop].[dbo].[StockMoves] DROP CONSTRAINT [PK_StockMoves];" {
		t.Errorf("statements %q %q", p.Statement, p.Rollback)
	}
	if fix.Target != "PK_StockMoves on dbo.StockMoves (MoveId), clustered" || len(p.Changes) != 1 || p.Held != "" ||
		p.Lead != "StockMoves gets a primary key on MoveId: SQL Server will refuse a second row with the same key." {
		t.Errorf("fix %+v preview %+v", fix, p)
	}
	for _, w := range []string{
		"This rewrites the whole of StockMoves (217 MB) as a clustered index and rebuilds its 3 nonclustered indexes, which then carry the key instead of the row's address.",
		"On Express edition it runs offline: StockMoves can be neither read nor written until it finishes, and users who touch it wait. Apply it in a quiet moment.",
		warnIndexLock,
		"On Express a database holds at most 50 GB of data: the table's new copy counts towards it while it is built.",
		warnIndexLarge,
		"SQL Server refuses the key if two rows share a value of MoveId (an identity can repeat after a reseed or IDENTITY_INSERT), and then nothing changes.",
		"Undoing it (the rollback) drops the key: the table becomes a heap again and is rewritten once more.",
		"Tell the vendor: their next upgrade must expect this key.",
	} {
		if !containsString(p.Warnings, w) {
			t.Errorf("missing warning %q in %q", w, p.Warnings)
		}
	}
	if len(p.Needs) != 2 || p.Needs[0].Part.MB != 217+40 || p.Needs[1].Part.MB != 217 {
		t.Errorf("needs %+v", p.Needs)
	}
	names := []string{}
	for _, in := range p.Inputs {
		names = append(names, in.Name)
	}
	if strings.Join(names, ",") != "key,name,clustered" || p.Options[OptionPKKey] != "Identity column MoveId" || p.Options[OptionClustered] != "clustered" {
		t.Errorf("inputs %v options %v", names, p.Options)
	}

	// Enterprise: online by default, an input; nonclustered asked for.
	p, _ = BuildAddPrimaryKeyFix(pkFacts(tb, 3), map[string]string{OptionTable: "[dbo].[StockMoves]", OptionClustered: "nonclustered"})
	if !strings.Contains(p.Statement, "PRIMARY KEY NONCLUSTERED ([MoveId]) WITH (ONLINE = ON,") || !containsString(p.Warnings, warnIndexOnline) ||
		p.Options[OptionOnline] != "true" || p.Rollback != "ALTER TABLE [Shop].[dbo].[StockMoves] DROP CONSTRAINT [PK_StockMoves];" {
		t.Errorf("enterprise %q %v", p.Statement, p.Warnings)
	}
}

// A unique index on a clustered table: nonclustered, its columns, the index stays.
func TestAddPrimaryKeyUnique(t *testing.T) {
	tb := noKey("Moves", 50000)
	tb.Heap, tb.ClusteredIndex = false, "CX_Moves"
	tb.Identity = "MoveId"
	tb.Unique = []UniqueKeyCandidate{{Index: "UX_Moves_Ref", Columns: []string{"Ref", "Line"}, KeyBytes: 24}}
	p, fix := BuildAddPrimaryKeyFix(pkFacts(tb, 2), map[string]string{OptionTable: "[dbo].[Moves]"})
	if p.Statement != "ALTER TABLE [Shop].[dbo].[Moves] ADD CONSTRAINT [PK_Moves] PRIMARY KEY NONCLUSTERED ([Ref], [Line]) WITH (ONLINE = OFF, SORT_IN_TEMPDB = ON);" ||
		fix.Target != "PK_Moves on dbo.Moves (Ref, Line), nonclustered" ||
		!containsString(p.Warnings, "The unique index UX_Moves_Ref stays: it is then a copy of the key. Disable it later if nothing uses it (a foreign key may).") ||
		!containsString(p.Warnings, "On Standard or Web edition it runs offline: Moves can be neither read nor written until it finishes, and users who touch it wait. Apply it in a quiet moment.") {
		t.Errorf("unique %q %q %q", p.Statement, fix.Target, p.Warnings)
	}
	for _, in := range p.Inputs {
		if in.Name == OptionClustered {
			t.Errorf("a clustered table offers no clustered input")
		}
	}
	// The identity column chosen instead.
	p, _ = BuildAddPrimaryKeyFix(pkFacts(tb, 2), map[string]string{OptionTable: "[dbo].[Moves]", OptionPKKey: "Identity column MoveId"})
	if !strings.Contains(p.Statement, "NONCLUSTERED ([MoveId])") {
		t.Errorf("identity chosen %q", p.Statement)
	}
	// A choice that is not offered, a name that is taken.
	p, _ = BuildAddPrimaryKeyFix(pkFacts(tb, 2), map[string]string{OptionTable: "[dbo].[Moves]", OptionPKKey: "Column Ref", OptionIndexName: "pk_other"})
	if len(p.Changes) != 0 || p.Statement != "" || len(p.Inputs) == 0 || !strings.HasPrefix(p.Warnings[0], "Choose one of: Unique index UX_Moves_Ref (Ref, Line); ") ||
		p.Warnings[1] != "Schema dbo already has an object named pk_other: choose another name." {
		t.Errorf("wrong inputs %+v", p)
	}
}

// A new column: one statement, the rollback drops both, Held until confirmed and while code breaks.
func TestAddPrimaryKeyNewColumn(t *testing.T) {
	tb := noKey("Notes", 20000)
	f := pkFacts(tb, 4)
	p, fix := BuildAddPrimaryKeyFix(f, map[string]string{OptionTable: "[dbo].[Notes]"})
	if p.Statement != "ALTER TABLE [Shop].[dbo].[Notes] ADD [kakapo_id] bigint IDENTITY(1, 1) NOT NULL CONSTRAINT [PK_Notes] PRIMARY KEY CLUSTERED;" ||
		p.Rollback != "ALTER TABLE [Shop].[dbo].[Notes] DROP CONSTRAINT [PK_Notes]; ALTER TABLE [Shop].[dbo].[Notes] DROP COLUMN [kakapo_id];" ||
		fix.Target != "PK_Notes on dbo.Notes (kakapo_id, a new bigint identity column), clustered" {
		t.Errorf("new column %q %q %q", p.Statement, p.Rollback, fix.Target)
	}
	if p.Held != `Tick "The application names its columns when it inserts into Notes" first: a new column makes an INSERT without a column list fail.` {
		t.Errorf("held %q", p.Held)
	}
	for _, w := range []string{
		"It writes every row to fill the new column. This rewrites the whole of Notes (2.5 MB) as a clustered index.",
		"Adding a column is always offline: Notes can be neither read nor written until it finishes, and users who touch it wait. Apply it in a quiet moment.",
		"A new column changes the table's shape: an INSERT into Notes without a column list, or one that copies its rows with SELECT *, fails afterwards (error 213).",
		warnPKOrder, warnPKCodeSearch,
		"Undoing it (the rollback) drops the key: the table becomes a heap again and is rewritten once more; the dropped column's 8 bytes a row stay until the table is rebuilt.",
		"Tell the vendor: their next upgrade must expect this key and column.",
	} {
		if !containsString(p.Warnings, w) {
			t.Errorf("missing warning %q in %q", w, p.Warnings)
		}
	}
	p, _ = BuildAddPrimaryKeyFix(f, map[string]string{OptionTable: "[dbo].[Notes]", OptionPKConfirm: "true"})
	if p.Held != "" || p.Options[OptionPKConfirm] != "true" {
		t.Errorf("confirmed %q", p.Held)
	}
	f.Code.Modules = []TableCodeUse{{Kind: "SQL_STORED_PROCEDURE", Schema: "dbo", Name: "AddNote", Text: "CREATE PROCEDURE dbo.AddNote AS INSERT INTO dbo.Notes VALUES (1, 'x');"}}
	p, _ = BuildAddPrimaryKeyFix(f, map[string]string{OptionTable: "[dbo].[Notes]", OptionPKConfirm: "true"})
	if p.Held != "dbo.AddNote inserts into Notes without a column list (or with SELECT *): a new column makes it fail (error 213). Change the code first, or ask the vendor." {
		t.Errorf("held by code %q", p.Held)
	}
	// A column name that is taken.
	p, _ = BuildAddPrimaryKeyFix(f, map[string]string{OptionTable: "[dbo].[Notes]", OptionPKColumn: "ref"})
	if len(p.Changes) != 0 || p.Warnings[0] != "Notes already has a column named ref: choose another name." {
		t.Errorf("column taken %+v", p.Warnings)
	}
}

func TestAddPrimaryKeyRefusals(t *testing.T) {
	cases := []struct {
		set  func(*NoKeyTableFact)
		want string
	}{
		{func(x *NoKeyTableFact) { x.HasPrimaryKey, x.PrimaryKey = true, "PK_Notes" }, "Notes has a primary key now (PK_Notes): nothing to add."},
		{func(x *NoKeyTableFact) { x.TemporalType = 1 }, "Notes is a history table: SQL Server does not allow a primary key on it."},
		{func(x *NoKeyTableFact) { x.CDC = true }, "Notes is tracked by change data capture: a new key or column changes what is captured, so the capture's owner decides."},
		{func(x *NoKeyTableFact) { x.Partitioned = true }, "Notes is partitioned: its key must include the partitioning column, which is a design question for the application's owner."},
		{func(x *NoKeyTableFact) { x.Columnstore = true }, "Notes has a columnstore index: kakapo does not change its keys."},
		{func(x *NoKeyTableFact) { x.LedgerType = 3 }, "Notes is a ledger table: its shape is fixed by the ledger."},
	}
	for _, c := range cases {
		tb := noKey("Notes", 20000)
		c.set(&tb)
		p, _ := BuildAddPrimaryKeyFix(pkFacts(tb, 4), map[string]string{OptionTable: "[dbo].[Notes]"})
		if len(p.Changes) != 0 || len(p.Inputs) != 0 || p.Statement != "" || len(p.Warnings) != 1 || p.Warnings[0] != c.want {
			t.Errorf("%q: %+v", c.want, p)
		}
	}
}

func TestInsertBreaksOnNewColumn(t *testing.T) {
	cases := []struct {
		text    string
		onTable bool
		want    bool
	}{
		{"INSERT INTO dbo.Notes VALUES (1, 'a')", false, true},
		{"insert [dbo].[Notes] select a, b from x", false, true},
		{"INSERT INTO Notes WITH (TABLOCK) VALUES (1)", false, true},
		{"INSERT INTO Shop.dbo.Notes SELECT * FROM Staging", false, true},
		{"INSERT INTO dbo.Notes (NoteId, Body) VALUES (1, 'a')", false, false},
		{"INSERT INTO dbo.Notes\n  (NoteId, Body)\nSELECT 1, 'a'", false, false},
		{"INSERT INTO dbo.Notes WITH (TABLOCK) (NoteId) VALUES (1)", false, false},
		{"INSERT INTO dbo.Notes DEFAULT VALUES", false, false},
		{"INSERT INTO dbo.NotesArchive VALUES (1)", false, false},
		{"INSERT INTO other.Notes VALUES (1)", false, false},
		{"-- INSERT INTO dbo.Notes VALUES (1)\nSELECT 1", false, false},
		{"/* INSERT INTO Notes VALUES (1) */ SELECT 1", false, false},
		{"SELECT 'INSERT INTO Notes VALUES (1)'", false, false},
		{"INSERT INTO dbo.Archive SELECT * FROM dbo.Notes WHERE x = 1", false, true},
		{"INSERT INTO dbo.Archive (a, b) SELECT * FROM [Notes]", false, true},
		{"INSERT INTO dbo.Archive SELECT TOP (10) * FROM Notes", false, true},
		{"INSERT INTO dbo.Archive SELECT a, b FROM Notes", false, false},
		{"INSERT INTO audit.NotesLog SELECT * FROM inserted", true, true},
		{"INSERT INTO audit.NotesLog SELECT * FROM inserted", false, false},
		{"SELECT * FROM dbo.Notes", false, false},
	}
	for _, c := range cases {
		if got := InsertBreaksOnNewColumn(c.text, "dbo", "Notes", c.onTable); got != c.want {
			t.Errorf("%q (on table %v): %v, want %v", c.text, c.onTable, got, c.want)
		}
	}
}

func TestPrimaryKeyReadBack(t *testing.T) {
	tb := noKey("Notes", 20000)
	f := pkFacts(tb, 4)
	p, _ := BuildAddPrimaryKeyFix(f, map[string]string{OptionTable: "[dbo].[Notes]", OptionPKConfirm: "true"})
	after := f
	after.Table.HasPrimaryKey, after.Table.Identity, after.Table.IdentityType = true, "kakapo_id", "bigint"
	after.Index.Indexes = []ExistingIndex{{Name: "PK_Notes", IndexID: 1, Type: 1, IsUnique: true, IsPrimaryKey: true, Keys: []IndexKey{{Name: "kakapo_id"}}, SizeMB: 2.7}}
	if text, ok := PrimaryKeyReadBack(after, p); !ok || text != "PK_Notes on dbo.Notes (kakapo_id), clustered, 2.7 MB" {
		t.Errorf("read back %q %v", text, ok)
	}
	after.Table.IdentityType = "int"
	if _, ok := PrimaryKeyReadBack(after, p); ok {
		t.Errorf("an int identity verified")
	}
	after.Table.IdentityType = "bigint"
	after.Index.Indexes[0].Type = 2
	if _, ok := PrimaryKeyReadBack(after, p); ok {
		t.Errorf("a nonclustered key verified for a clustered preview")
	}
	after.Index.Indexes = nil
	if text, ok := PrimaryKeyReadBack(after, p); ok || text != "no primary key on dbo.Notes after the statement" {
		t.Errorf("no key %q", text)
	}

	// An existing column.
	tb.Identity = "MoveId"
	f = pkFacts(tb, 4)
	p, fix := BuildAddPrimaryKeyFix(f, map[string]string{OptionTable: "[dbo].[Notes]"})
	after = f
	after.Index.Indexes = []ExistingIndex{{Name: "PK_Notes", IndexID: 1, Type: 1, IsPrimaryKey: true, Keys: []IndexKey{{Name: "MoveId"}}, SizeMB: 2.5}}
	if _, ok := PrimaryKeyReadBack(after, p); !ok {
		t.Errorf("identity key not verified")
	}
	if d := PrimaryKeyChangeDescription(fix, "Shop"); d != "Added primary key PK_Notes on dbo.Notes (MoveId), clustered in Shop, for DES01" {
		t.Errorf("description %q", d)
	}
}

func containsString(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}
