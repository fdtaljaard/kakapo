package domain

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// IDX04 (docs/phase5o-contract.md, section 2): the triage's grouping, keeper and safe rule.

// ix is a plain nonclustered index on keys (a "-" prefix makes a key DESC) with included columns.
func ix(id int, name string, keys []string, included ...string) ExistingIndex {
	x := ExistingIndex{Name: name, IndexID: id, Type: 2, Keys: []IndexKey{}, Included: included, ForeignKeys: []string{}}
	if x.Included == nil {
		x.Included = []string{}
	}
	for _, k := range keys {
		if strings.HasPrefix(k, "-") {
			x.Keys = append(x.Keys, IndexKey{Name: k[1:], Descending: true})
		} else {
			x.Keys = append(x.Keys, IndexKey{Name: k})
		}
	}
	return x
}

// ordersFacts is dbo.Orders with a clustered primary key on Id, IX_A (CustomerId, OrderDate) INCLUDE
// (Total), read 812 times, and IX_B (CustomerId, OrderDate), 1.2 GB and 4.3 million writes.
func ordersFacts() IndexFacts {
	pk := ix(1, "PK_Orders", []string{"Id"})
	pk.Type, pk.IsUnique, pk.IsPrimaryKey, pk.SizeMB = 1, true, true, 4000
	a := ix(2, "IX_A", []string{"CustomerId", "OrderDate"}, "Total")
	a.SizeMB, a.Reads, a.Writes = 1300, 812, 4_300_000
	b := ix(3, "IX_B", []string{"CustomerId", "OrderDate"})
	b.SizeMB, b.Reads, b.Writes = 1228.8, 5, 4_300_000
	return IndexFacts{ProductVersion: "16.0.4135.4", EngineEdition: 3, Database: "Shop", Schema: "dbo", Table: "Orders", Found: true,
		Rows: 20_000_000, DataMB: 4000, Columns: []TableColumn{}, Indexes: []ExistingIndex{pk, a, b}}
}

// dupFacts is dfacts() with Orders' indexes read.
func dupFacts(tables ...IndexFacts) DesignFacts {
	f := dfacts()
	f.IndexTables = tables
	return f
}

func groupNames(g DuplicateGroup) string {
	out := []string{}
	for _, c := range g.Copies {
		out = append(out, c.Index.Name)
	}
	return strings.Join(out, ",")
}

func TestIDX04Catalogue(t *testing.T) {
	c, ok := CheckByID("IDX04")
	if !ok || c.Card != CardDesign || c.Title != "Duplicate indexes" || c.Question != "Which indexes copy another one?" || c.Owner != OwnerUs ||
		c.ThenCheck != "Re-run: IDX04 gone for that table; the keeper's reads went up by what the copy had." {
		t.Errorf("IDX04 %+v", c)
	}
	if checks := ChecksFor(CardDesign); len(checks) != 3 || checks[0].ID != "IDX14" || checks[1].ID != "IDX04" || checks[2].ID != "DES01" {
		t.Errorf("design checks %+v", checks)
	}
	if d, ok := FindFixDef(FixDisableIndex); !ok || d.Title != "Disable a duplicate index" || d.Scope != ScopeDatabase || d.CheckID != "IDX04" ||
		d.Setting != "Index" || d.Card() != CardDesign {
		t.Errorf("disable-index %+v", d)
	}
}

// Same keys in the same order and direction and the same filter make a group; a DESC key, another
// order or a filter make another.
func TestIDX04Grouping(t *testing.T) {
	f := ordersFacts()
	f.Indexes = append(f.Indexes,
		ix(4, "IX_Desc", []string{"CustomerId", "-OrderDate"}),
		ix(5, "IX_Order", []string{"OrderDate", "CustomerId"}),
		ix(6, "IX_Filtered", []string{"CustomerId", "OrderDate"}))
	f.Indexes[5].Filter = "([Status]=(1))"
	gs := DuplicateGroups(f)
	if len(gs) != 1 || groupNames(gs[0]) != "IX_A,IX_B" {
		t.Fatalf("groups %+v", gs)
	}
	// A second DESC copy and a second filtered copy make groups of their own.
	f.Indexes = append(f.Indexes, ix(7, "IX_Desc2", []string{"customerid", "-orderdate"}), ix(8, "IX_Filtered2", []string{"CustomerId", "OrderDate"}))
	f.Indexes[7].Filter = "([Status]=(1))"
	gs = DuplicateGroups(f)
	names := []string{}
	for _, g := range gs {
		names = append(names, groupNames(g))
	}
	if strings.Join(names, " ") != "IX_A,IX_B IX_Desc,IX_Desc2 IX_Filtered,IX_Filtered2" {
		t.Errorf("groups %v", names)
	}
	// Disabled and hypothetical indexes are not copies.
	f = ordersFacts()
	f.Indexes[2].IsDisabled = true
	if gs := DuplicateGroups(f); len(gs) != 0 {
		t.Errorf("a disabled copy grouped %+v", gs)
	}
	f = ordersFacts()
	f.Indexes[2].IsHypothetical = true
	if gs := DuplicateGroups(f); len(gs) != 0 {
		t.Errorf("a hypothetical copy grouped %+v", gs)
	}
}

// The keeper's order: primary key, clustered, unique, more reads, larger, lower index id.
func TestIDX04KeeperOrder(t *testing.T) {
	keys := []string{"K"}
	first := func(a, b ExistingIndex) string {
		f := IndexFacts{Schema: "dbo", Table: "T", Indexes: []ExistingIndex{b, a}}
		gs := DuplicateGroups(f)
		if len(gs) != 1 {
			t.Fatalf("groups %+v", gs)
		}
		return gs[0].Copies[0].Index.Name + " " + keeperWhy(gs[0].Copies[0].Index, gs[0].Copies[1].Index)
	}
	pk := ix(5, "PK", keys)
	pk.IsPrimaryKey, pk.IsUnique, pk.Type = true, true, 2
	cl := ix(1, "CL", keys)
	cl.Type = 1
	if got := first(pk, cl); got != "PK primary key" {
		t.Errorf("primary key before clustered: %s", got)
	}
	uq := ix(6, "UQ", keys)
	uq.IsUnique = true
	if got := first(cl, uq); got != "CL clustered" {
		t.Errorf("clustered before unique: %s", got)
	}
	read := ix(7, "READ", keys)
	read.Reads = 10
	if got := first(uq, read); got != "UQ unique" {
		t.Errorf("unique before reads: %s", got)
	}
	big := ix(8, "BIG", keys)
	big.SizeMB = 100
	if got := first(read, big); got != "READ most reads" {
		t.Errorf("reads before size: %s", got)
	}
	low := ix(2, "LOW", keys)
	if got := first(big, low); got != "BIG larger" {
		t.Errorf("size before index id: %s", got)
	}
	if got := first(low, ix(9, "HIGH", keys)); got != "LOW lowest index id" {
		t.Errorf("index id: %s", got)
	}
}

// Safe when a higher-ranked copy, a copy with more columns, or the clustered index holds every column;
// identical copies: only the lower-ranked one; the rest of the plain copies need merging.
func TestIDX04SafeAndMerge(t *testing.T) {
	// IX_A holds every column of IX_B and ranks higher.
	g := DuplicateGroups(ordersFacts())[0]
	if s := g.Safe(); len(s) != 1 || s[0].Index.Name != "IX_B" || s[0].KeptIn != "IX_A" || len(g.Merging()) != 0 {
		t.Fatalf("safe %+v merging %+v", g.Safe(), g.Merging())
	}
	// IX_B read more than IX_A ranks first, but IX_A holds a column IX_B does not: IX_B is still the copy.
	f := ordersFacts()
	f.Indexes[2].Reads = 5000
	g = DuplicateGroups(f)[0]
	if groupNames(g) != "IX_B,IX_A" || len(g.Safe()) != 1 || g.Safe()[0].Index.Name != "IX_B" || g.Safe()[0].KeptIn != "IX_A" {
		t.Errorf("narrow copy read more %+v", g)
	}
	// Identical copies: only the lower-ranked one.
	f = ordersFacts()
	f.Indexes[2].Included = []string{"Total"}
	g = DuplicateGroups(f)[0]
	if s := g.Safe(); len(s) != 1 || s[0].Index.Name != "IX_B" || s[0].KeptIn != "IX_A" {
		t.Errorf("identical copies %+v", s)
	}
	// The clustered index on the same keys holds every column.
	f = ordersFacts()
	f.Indexes[0].Keys = []IndexKey{{Name: "CustomerId"}, {Name: "OrderDate"}}
	f.Indexes[0].IsPrimaryKey, f.Indexes[0].IsUnique = false, false
	g = DuplicateGroups(f)[0]
	if groupNames(g) != "PK_Orders,IX_A,IX_B" || len(g.Safe()) != 2 || g.Safe()[0].KeptIn != "PK_Orders" || g.Safe()[1].KeptIn != "PK_Orders" {
		t.Errorf("clustered keeper %+v", g)
	}
	// A unique copy is kept, and holds the plain copy's columns.
	f = ordersFacts()
	f.Indexes[1].IsUnique = true
	f.Indexes[1].Included = []string{}
	g = DuplicateGroups(f)[0]
	if s := g.Safe(); len(s) != 1 || s[0].Index.Name != "IX_B" || s[0].KeptIn != "IX_A" {
		t.Errorf("unique keeper %+v", g)
	}
	// A chain: IX_1 in IX_2 in IX_3; both narrower ones are kept in IX_3, which is not disabled.
	f = IndexFacts{Schema: "dbo", Table: "T", Indexes: []ExistingIndex{
		ix(2, "IX_1", []string{"K"}), ix(3, "IX_2", []string{"K"}, "A"), ix(4, "IX_3", []string{"K"}, "A", "B")}}
	f.Indexes[1].Reads = 50 // IX_2 ranks first
	g = DuplicateGroups(f)[0]
	if s := g.Safe(); len(s) != 2 || s[0].KeptIn != "IX_3" || s[1].KeptIn != "IX_3" {
		t.Errorf("chain %+v", s)
	}

	// IX_C (CustomerId) INCLUDE (Status) and IX_D (CustomerId) INCLUDE (Total): merging, no Apply.
	f = ordersFacts()
	f.Indexes = append(f.Indexes[:1], ix(4, "IX_C", []string{"CustomerId"}, "Status"), ix(5, "IX_D", []string{"CustomerId"}, "Total"))
	g = DuplicateGroups(f)[0]
	if len(g.Safe()) != 0 || len(g.Merging()) != 2 || !g.Fires() {
		t.Fatalf("merging %+v", g)
	}
	// A wider index that starts with the same keys and holds both: the triage's IDX08, not a merge.
	f.Indexes = append(f.Indexes, ix(6, "IX_E", []string{"CustomerId", "OrderDate"}, "Status", "Total"))
	if g = DuplicateGroups(f)[0]; len(g.Merging()) != 0 || g.Fires() {
		t.Errorf("covered by a wider index %+v", g)
	}
	// One plain copy beside a unique one that does not hold its columns: nothing to do.
	f = ordersFacts()
	f.Indexes[1].IsUnique, f.Indexes[1].Included = true, []string{}
	f.Indexes[2].Included = []string{"Total"}
	if g = DuplicateGroups(f)[0]; g.Fires() {
		t.Errorf("one plain copy that the unique one does not hold %+v", g)
	}
}

// The finding, its evidence, the verdict and the fact; rank 3 at 50 MB of safe copies, 4 below.
func TestIDX04Finding(t *testing.T) {
	c, fs, checks := dbuild(dupFacts(ordersFacts()))
	if len(fs) != 1 {
		t.Fatalf("findings %+v", fs)
	}
	x := fs[0]
	want := "IX_B is a copy of IX_A on Orders (CustomerId, OrderDate): 1.2 GB and 4.3 million writes since 1 Oct, for nothing. IX_A holds every column of IX_B."
	if x.CheckID != "IDX04" || x.Title != "Duplicate indexes" || x.Owner != OwnerUs || x.Object != "[dbo].[Orders]" || x.Finding != want ||
		x.Severity != SeverityMedium || x.FixID != FixDisableIndex || x.Effort != EffortQuickWin || x.When != WhenOnline {
		t.Errorf("finding %+v\nwant %s", x, want)
	}
	if x.Why != "Indexes with the same keys do the same job: SQL Server keeps every copy up to date on each insert, update and delete, and stores, backs up and maintains each one." ||
		x.DoThis != "Disable IX_B. Queries that used it use IX_A, which has the same keys and every column. Tell the vendor, so their next upgrade does not create it again." {
		t.Errorf("why/do %q %q", x.Why, x.DoThis)
	}
	for _, s := range []string{"Safe to disable: [IX_B] (kept in [IX_A]).",
		"[IX_A] (kept, most reads): keys CustomerId, OrderDate; includes Total; no filter; 1.3 GB; 812 reads and 4,300,000 writes since the restart on 1 Oct.",
		"[IX_B] (safe to disable, kept in [IX_A]): keys CustomerId, OrderDate; includes nothing; no filter; 1.2 GB; 5 reads and 4,300,000 writes since the restart on 1 Oct."} {
		if !strings.Contains(x.Evidence, s) {
			t.Errorf("evidence lacks %q:\n%s", s, x.Evidence)
		}
	}
	if c.Status != StatusAmber || c.StatusText != "1 thing to do" || c.ToDo != 1 || c.Fixable != 1 ||
		c.Verdict != "1 index copies another one: 1.2 GB and 4.3 million writes since 1 Oct for nothing." {
		t.Errorf("card %+v", c)
	}
	if c.Facts[2] != (Fact{Label: "Duplicate indexes", Value: "1, 1.2 GB", Tone: ToneBad}) {
		t.Errorf("fact %+v", c.Facts)
	}
	if checks[0].Status != CheckPassed || checks[1].Status != CheckFinding {
		t.Errorf("checks %+v", checks)
	}

	// 50 MB of safe copies is rank 3, under it 4.
	f := ordersFacts()
	f.Indexes[2].SizeMB = 50
	if _, fs, _ := dbuild(dupFacts(f)); fs[0].Severity != SeverityMedium {
		t.Errorf("50 MB %s", fs[0].Severity)
	}
	f.Indexes[2].SizeMB = 49.9
	if _, fs, _ := dbuild(dupFacts(f)); fs[0].Severity != SeverityLow || !strings.Contains(fs[0].Finding, ": 49.9 MB and 4.3 million writes") {
		t.Errorf("49.9 MB %s %s", fs[0].Severity, fs[0].Finding)
	}
}

// Two safe copies on one table: one sentence each, one Apply each; findings ordered by the safe MB.
func TestIDX04TwoCopiesAndOrder(t *testing.T) {
	f := ordersFacts()
	e := ix(4, "IX_E", []string{"CustomerId", "OrderDate"})
	e.SizeMB, e.Writes = 10, 1
	f.Indexes = append(f.Indexes, e)
	small := IndexFacts{Database: "Shop", Schema: "sales", Table: "Notes", Found: true, Indexes: []ExistingIndex{
		ix(2, "IX_N1", []string{"Day"}, "Text"), ix(3, "IX_N2", []string{"Day"})}}
	small.Indexes[1].SizeMB = 3
	c, fs, _ := dbuild(dupFacts(small, f))
	if len(fs) != 2 || fs[0].Object != "[dbo].[Orders]" || fs[1].Object != "[sales].[Notes]" {
		t.Fatalf("order %+v", fs)
	}
	if !strings.Contains(fs[0].Finding, "IX_B is a copy of IX_A on Orders (CustomerId, OrderDate): 1.2 GB and 4.3 million writes since 1 Oct, for nothing. IX_A holds every column of IX_B. IX_E is a copy of IX_A on Orders (CustomerId, OrderDate): 10 MB and 1 write since 1 Oct, for nothing.") ||
		!strings.HasPrefix(fs[0].Evidence, "Safe to disable: [IX_B] (kept in [IX_A]), [IX_E] (kept in [IX_A]).") ||
		fs[0].DoThis != "Disable IX_B and IX_E. Queries that used them use the copy named for each, which has the same keys and every column. Tell the vendor, so their next upgrade does not create them again." {
		t.Errorf("two copies %+v", fs[0])
	}
	if !strings.HasPrefix(fs[1].Finding, "IX_N2 is a copy of IX_N1 on sales.Notes (Day): 3 MB and 0 writes since 1 Oct") {
		t.Errorf("other schema %q", fs[1].Finding)
	}
	if c.Verdict != "3 indexes copy another one: 1.2 GB and 4.3 million writes since 1 Oct for nothing." || c.Facts[2].Value != "3, 1.2 GB" || c.Fixable != 2 {
		t.Errorf("card %q %+v %d", c.Verdict, c.Facts[2], c.Fixable)
	}
	fixes := AvailableDesignFixes(dupFacts(small, f), fs)
	if len(fixes) != 3 {
		t.Fatalf("fixes %+v", fixes)
	}
	want := map[string]string{OptionTable: "[dbo].[Orders]", OptionIndex: "IX_B", OptionKeeper: "IX_A", OptionObject: "[dbo].[Orders]"}
	for k, v := range want {
		if fixes[0].Options[k] != v {
			t.Errorf("option %s %q, want %q", k, fixes[0].Options[k], v)
		}
	}
	if fixes[0].ID != FixDisableIndex || fixes[0].CheckID != "IDX04" || fixes[0].Target != "disabled" ||
		fixes[0].Current != "IX_B on dbo.Orders (CustomerId, OrderDate), 1.2 GB" || fixes[1].Options[OptionIndex] != "IX_E" ||
		fixes[2].Options[OptionTable] != "[sales].[Notes]" || fixes[2].Options[OptionIndex] != "IX_N2" {
		t.Errorf("fixes %+v", fixes)
	}
}

// The merging case: no Apply, Copy for someone experienced with indexes.
func TestIDX04Merging(t *testing.T) {
	f := ordersFacts()
	f.Indexes = append(f.Indexes[:1], ix(4, "IX_C", []string{"CustomerId"}, "Status"), ix(5, "IX_D", []string{"CustomerId"}, "Total"))
	c, fs, _ := dbuild(dupFacts(f))
	if len(fs) != 1 {
		t.Fatalf("findings %+v", fs)
	}
	x := fs[0]
	if x.Finding != "IX_C and IX_D on Orders have the same keys (CustomerId) but each holds columns the other does not. One index with all their columns would replace them." ||
		x.DoThis != "Copy for someone experienced with indexes, or for the vendor." || x.FixID != "" || x.Severity != SeverityLow || x.Effort != EffortModerate ||
		!strings.HasPrefix(x.Evidence, "Needs merging: [IX_C], [IX_D].") || !strings.Contains(x.Evidence, "[IX_C] (needs merging): keys CustomerId; includes Status") {
		t.Errorf("merging %+v", x)
	}
	if c.Fixable != 0 || c.ToDo != 1 || c.Facts[2].Value != "2 to merge" ||
		c.Verdict != "2 indexes have the same keys as another but hold different columns: merging them is a job for someone experienced with indexes." {
		t.Errorf("card %+v", c)
	}
	if fixes := AvailableDesignFixes(dupFacts(f), fs); len(fixes) != 0 {
		t.Errorf("fixes %+v", fixes)
	}
	if IDX04SafeCopies(x.Evidence) != nil {
		t.Errorf("safe copies read from a merging finding")
	}
}

// Usage not read: reads and writes -1, the writes part left out; the window not read: no day.
func TestIDX04UsageNotRead(t *testing.T) {
	f := ordersFacts()
	for i := range f.Indexes {
		f.Indexes[i].Reads, f.Indexes[i].Writes = -1, -1
	}
	d := dupFacts(f)
	d.UsageError = "VIEW DATABASE STATE permission denied"
	c, fs, _ := dbuild(d)
	if len(fs) != 1 || !strings.HasPrefix(fs[0].Finding, "IX_B is a copy of IX_A on Orders (CustomerId, OrderDate): 1.2 GB, for nothing.") ||
		!strings.Contains(fs[0].Evidence, "1.2 GB; reads and writes not read.") || c.Verdict != "1 index copies another one: 1.2 GB for nothing." {
		t.Errorf("usage not read %+v %q", fs, c.Verdict)
	}
	// With the use read but the window not, the writes stay without a day.
	d = dupFacts(ordersFacts())
	d.WindowError = "no permission"
	_, fs, _ = dbuild(d)
	if !strings.Contains(fs[0].Finding, ": 1.2 GB and 4.3 million writes, for nothing.") || !strings.Contains(fs[0].Evidence, "writes since SQL Server started counting") {
		t.Errorf("window not read %+v", fs)
	}
}

// IDX14 and IDX04 together: one clause each, IDX14's first; the indexes not read leave IDX04 skipped.
func TestDesignCardBothChecks(t *testing.T) {
	d := dupFacts(ordersFacts())
	d.ForeignKeys[0].RefDeletes = 812
	c, fs, checks := dbuild(d)
	if len(fs) != 2 || fs[0].CheckID != "IDX14" || fs[1].CheckID != "IDX04" || c.StatusText != "2 things to do" || c.Fixable != 2 ||
		c.Verdict != "1 foreign key makes deletes read a whole table: Lines (1.4 million rows). 1 index copies another one: 1.2 GB and 4.3 million writes since 1 Oct for nothing." ||
		checks[0].Status != CheckFinding || checks[1].Status != CheckFinding {
		t.Errorf("both %+v %+v", c, checks)
	}
	if fixes := AvailableDesignFixes(d, fs); len(fixes) != 2 || fixes[0].ID != FixCreateIndex || fixes[1].ID != FixDisableIndex {
		t.Errorf("fixes %+v", fixes)
	}
	// The indexes could not be read: IDX04 skipped; no finding anywhere makes the card grey.
	d = dfacts()
	d.IndexesError = "permission denied"
	c, fs, checks = dbuild(d)
	if len(fs) != 0 || c.Status != StatusGrey || c.Note != "Could not read the indexes: permission denied" || checks[0].Status != CheckPassed ||
		checks[1].Status != CheckSkipped || c.Facts[2].Value != "could not be read" {
		t.Errorf("indexes not read %+v %+v", c, checks)
	}
	// IDX14's deletes not read, but a duplicate found: amber with IDX04's clause, IDX14 skipped.
	d = dupFacts(ordersFacts())
	d.DeletesError = "denied"
	c, fs, checks = dbuild(d)
	if len(fs) != 1 || c.Status != StatusAmber || checks[0].Status != CheckSkipped || checks[1].Status != CheckFinding ||
		!strings.HasPrefix(c.Verdict, "1 index copies another one") {
		t.Errorf("deletes not read, a copy found %+v %+v", c, checks)
	}
}

// The safe copies read back from a stored finding's evidence, brackets and all.
func TestIDX04SafeCopiesFromEvidence(t *testing.T) {
	got := IDX04SafeCopies("Safe to disable: [IX_B] (kept in [IX_A]), [IX ]]odd, (kept in [x]])] (kept in [IX_A]). [IX_A], kept: ...")
	if len(got) != 2 || got[0] != [2]string{"IX_B", "IX_A"} || got[1] != [2]string{"IX ]odd, (kept in [x])", "IX_A"} {
		t.Errorf("safe copies %q", got)
	}
	if got := IDX04SafeCopies("Foreign key FK: ..."); got != nil {
		t.Errorf("not IDX04 %q", got)
	}
	f := ordersFacts()
	f.Indexes[2].Name = "IX ]]B"
	_, fs, _ := dbuild(dupFacts(f))
	if got := IDX04SafeCopies(fs[0].Evidence); len(got) != 1 || got[0] != [2]string{"IX ]]B", "IX_A"} {
		t.Errorf("round trip %q", got)
	}
}

// The query page's tables: a safe copy from the Design card's stored read, with the disable-index action.
func TestQueryTableCopies(t *testing.T) {
	_, fs, _ := dbuild(dupFacts(ordersFacts()))
	lines := qTableCopies(fs, "dbo", "Orders", true)
	if len(lines) != 1 || lines[0].Text != "IX_B is a copy of IX_A, which has the same keys and every one of its columns (IDX04 on the Design card)." ||
		lines[0].Action == nil || lines[0].Action.FixID != FixDisableIndex || lines[0].Action.Options[OptionIndex] != "IX_B" ||
		lines[0].Action.Options[OptionKeeper] != "IX_A" || lines[0].Action.Options[OptionTable] != "[dbo].[Orders]" {
		t.Errorf("copies %+v", lines)
	}
	if lines := qTableCopies(fs, "dbo", "Orders", false); len(lines) != 1 || lines[0].Action != nil {
		t.Errorf("read only %+v", lines)
	}
	if lines := qTableCopies(fs, "dbo", "Lines", true); lines != nil {
		t.Errorf("another table %+v", lines)
	}
	b, _ := json.Marshal(QueryTable{Copies: lines})
	if !strings.Contains(string(b), `"copies":[{"text":"IX_B is a copy of IX_A`) {
		t.Errorf("json %s", b)
	}
}

// The create-index guard names the Design card when the table already has copies.
func TestCreateIndexGuardNamesDesignCard(t *testing.T) {
	f := ordersFacts()
	f.Columns = []TableColumn{{"Id", "int", 4}, {"CustomerId", "int", 4}, {"OrderDate", "datetime2", 8}, {"Total", "money", 8}}
	spec := CreateIndexSpec{Schema: "dbo", Table: "Orders", Equality: []string{"CustomerId", "OrderDate"}}
	pv, _ := BuildCreateIndexFix(f, spec, map[string]string{})
	if pv.Statement != "" || !strings.Contains(pv.Warnings[0], "widen it instead of adding one. The Design card's IDX04 lists the copies dbo.Orders already has.") {
		t.Errorf("guard %q", pv.Warnings)
	}
	f.Indexes = f.Indexes[:2]
	pv, _ = BuildCreateIndexFix(f, spec, map[string]string{})
	if strings.Contains(strings.Join(pv.Warnings, " "), "Design card") {
		t.Errorf("no copies, but the guard names the Design card: %q", pv.Warnings)
	}
}

var _ = time.Now
