package domain

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// Database design (docs/phase5n-contract.md): "Does the way it is built cost you anything?" A design
// rule ships only when kakapo can show what it costs on this database and has a source with a
// threshold (SCOPE rule 2). The first is IDX14, foreign keys without an index, raised only when rows
// were deleted from the referenced table in the window; the second IDX04, duplicate indexes, whose
// safe copies end in the disable-index fix (docs/phase5o-contract.md).

// DesignFacts is what the Design card reads about one database (collect.ReadDesignFacts), each area
// with its own error. Every time is the server's own clock (GETDATE()), carried naive in a time.Time.
type DesignFacts struct {
	Database string
	State    string // sys.databases state_desc
	// Error says nothing could be read; the card is grey.
	Error     string
	ServerNow time.Time

	// ForeignKeys are the enabled foreign keys whose columns lead no index of the referencing table,
	// for every referenced table (collect.ReadUnindexedForeignKeys with no table list), each with the
	// deletes on its referenced table in the window (RefDeletes, -1 when DeletesError).
	ForeignKeys      []UnindexedForeignKeyFact
	ForeignKeysError string
	DeletesError     string

	// The counts: enabled foreign keys and user tables.
	EnabledForeignKeys int
	UserTables         int
	CountsError        string

	// The window the deletes are counted in starts at the latest of SQL Server's start, the last
	// restore into this database and the database's creation (sp_IndexCleanup.sql 1990-2005).
	// LastRestore comes from the Backups read (nil when none, or when msdb could not be read).
	StartedAt   time.Time
	CreateDate  time.Time
	LastRestore *time.Time
	WindowError string

	// IndexTables are the indexes of every user table (collect.AllIndexFacts, docs/phase5o-contract.md
	// section 1), for IDX04. IndexesError says they could not be read; UsageError that their reads and
	// writes could not (each index's Reads and Writes are then -1).
	IndexTables  []IndexFacts
	IndexesError string
	UsageError   string

	// NoKeyTables are the user tables without a primary key (collect.ReadNoKeyTables, docs/phase5s-contract.md
	// section 1), for DES01; NoKeyError says they could not be read.
	NoKeyTables []NoKeyTableFact
	NoKeyError  string
}

// UsageWindowFacts are the moments the use and operational counters of a database start from: SQL
// Server's start, the database's creation and its latest restore (nil when none or not read), with
// the server's clock (collect.ReadUsageWindow; the Design card's read has them in DesignFacts).
type UsageWindowFacts struct {
	ServerNow   time.Time
	StartedAt   time.Time
	CreateDate  time.Time
	LastRestore *time.Time
}

// IDX14's bars (PerformanceTriage.sql IDX14, 1556-1580): a referencing table of 10,000 rows or more;
// rank 3 from 1,000,000 rows, else 4. A window of 7 days or less is too short for an absence of
// deletes to be evidence (sp_IndexCleanup.sql 2017-2023).
const (
	idx14MinRows    = 10000
	idx14Rank3Rows  = 1000000
	designEarlyDays = 7
)

// Kinds of the window's start.
const (
	DesignSinceRestart = "restart"
	DesignSinceRestore = "restore"
	DesignSinceCreated = "created"
)

// DesignWindow is when the deletes started being counted, and what that moment was.
type DesignWindow struct {
	Since    time.Time `json:"since"` // the server's clock
	Kind     string    `json:"kind"`  // restart, restore, created
	Text     string    `json:"text"`  // "the restart on 1 Oct"
	Days     int       `json:"days"`  // whole days to the read
	TooEarly bool      `json:"tooEarly"`
}

// Window is the deletes' window: from the latest of the restart, the last restore and the
// database's creation to ServerNow. false when the window could not be read.
func (f DesignFacts) Window() (DesignWindow, bool) {
	if f.WindowError != "" {
		return DesignWindow{}, false
	}
	return UsageWindowFacts{ServerNow: f.ServerNow, StartedAt: f.StartedAt, CreateDate: f.CreateDate, LastRestore: f.LastRestore}.Window()
}

// Window is when the counters started: the latest of the restart, the last restore and the database's
// creation, to ServerNow (sp_IndexCleanup.sql 1990-2005). false when the start is not known.
func (u UsageWindowFacts) Window() (DesignWindow, bool) {
	if u.StartedAt.IsZero() {
		return DesignWindow{}, false
	}
	w := DesignWindow{Since: u.StartedAt, Kind: DesignSinceRestart}
	if u.LastRestore != nil && u.LastRestore.After(w.Since) {
		w.Since, w.Kind = *u.LastRestore, DesignSinceRestore
	}
	if u.CreateDate.After(w.Since) {
		w.Since, w.Kind = u.CreateDate, DesignSinceCreated
	}
	span := u.ServerNow.Sub(w.Since)
	w.Days = int(span.Hours() / 24)
	w.TooEarly = span <= designEarlyDays*24*time.Hour
	day := designDay(w.Since, u.ServerNow)
	switch w.Kind {
	case DesignSinceRestore:
		w.Text = "the restore on " + day
	case DesignSinceCreated:
		w.Text = "the database's creation on " + day
	default:
		w.Text = "the restart on " + day
	}
	return w, true
}

// designDay is "1 Oct", with the year when it is not the server's current year.
func designDay(t, now time.Time) string {
	if t.Year() != now.Year() {
		return t.Format("2 Jan 2006")
	}
	return t.Format("2 Jan")
}

// designName names a table in a sentence: the table alone in dbo, "schema.table" elsewhere.
func designName(schema, table string) string {
	if strings.EqualFold(schema, "dbo") {
		return table
	}
	return schema + "." + table
}

// designRows is "1.4 million rows", or "20,000 rows" below a million.
func designRows(n int64) string {
	if n >= 1_000_000 {
		return n1(float64(n)/1_000_000) + " million rows"
	}
	return plural(n, "row", "rows")
}

// idx14Large says whether a key's referencing table is big enough for IDX14 (10,000 rows or more).
func idx14Large(k UnindexedForeignKeyFact) bool { return k.Rows >= idx14MinRows && len(k.Columns) > 0 }

// idx14Fires says whether IDX14 fires for a key: a large referencing table and one or more rows
// deleted from the referenced table in the window.
func idx14Fires(k UnindexedForeignKeyFact) bool { return idx14Large(k) && k.RefDeletes > 0 }

// Texts of IDX14 (docs/phase5n-contract.md, section 3). Why: Microsoft Learn, "Primary and foreign key
// constraints", indexes on foreign key constraints (changes to the referenced key are checked against
// the referencing table). Do this: kakapo's own, the owner rule of the contract (an index is invisible
// to the application and can be undone). The evidence's caveat: Microsoft Learn,
// "sys.dm_db_index_operational_stats" (the counts live only as long as the metadata cache object).
const (
	idx14Why     = "Every delete from the parent table, and every change to its key, checks this foreign key. Without an index the check reads the whole referencing table and holds its locks while it does (Microsoft Learn, 'Primary and foreign key constraints')."
	idx14Do      = "Create an index on %s (%s). It does not change how the application works; tell the vendor, so their next upgrade keeps it or adds its own."
	idx14Caveat  = "Deletes are counted since %s and may be more: SQL Server forgets them when it unloads the table's metadata."
	idx14Skipped = "Could not read the deletes on the parent tables (sys.dm_db_index_operational_stats): "

	// DesignFoldHeading and DesignFoldTooEarly head the card page's fold of the foreign keys without an
	// index that have cost nothing yet (no delete from the table they point to in the window).
	DesignFoldHeading  = "Missing indexes on foreign keys, not needed yet"
	DesignFoldTooEarly = "Missing indexes on foreign keys, too early to tell"
)

// idx14Columns is "starts with OrderId", or "starts with its columns A, B" for a key of several.
func idx14Columns(cols []string) string {
	if len(cols) > 1 {
		return "starts with its columns " + plainList(cols)
	}
	return "starts with " + plainList(cols)
}

// idx14Doing is what the delete makes SQL Server do on the referencing table, by the key's ON DELETE.
func idx14Doing(k UnindexedForeignKeyFact) string {
	switch k.OnDelete {
	case "CASCADE":
		return fmt.Sprintf("to find and delete the matching rows (ON DELETE CASCADE) of foreign key %s", k.Name)
	case "SET_NULL":
		return fmt.Sprintf("to find and update the matching rows (ON DELETE SET NULL) of foreign key %s", k.Name)
	case "SET_DEFAULT":
		return fmt.Sprintf("to find and update the matching rows (ON DELETE SET DEFAULT) of foreign key %s", k.Name)
	}
	return "to check foreign key " + k.Name
}

// idx14Evidence names the key, its columns, the referencing rows, the deletes and the window, and the
// referential actions. It starts with "Foreign key {name}:", which AvailableDesignFixes matches.
func idx14Evidence(k UnindexedForeignKeyFact, w DesignWindow) string {
	child, parent := QuoteName(k.Schema)+"."+QuoteName(k.Table), QuoteName(k.RefSchema)+"."+QuoteName(k.RefTable)
	return fmt.Sprintf("Foreign key %s: %s (%s) references %s; ON DELETE %s, ON UPDATE %s. %s has %s; %s deleted from %s since %s (%s). %s",
		k.Name, child, bracketList(k.Columns), parent, k.OnDelete, k.OnUpdate,
		designName(k.Schema, k.Table), plural(k.Rows, "row", "rows"), plural(k.RefDeletes, "row", "rows"), designName(k.RefSchema, k.RefTable),
		w.Text, w.Since.Format("2006-01-02 15:04"), fmt.Sprintf(idx14Caveat, w.Text))
}

// BuildDesignCard answers "Does the way it is built cost you anything?" for one database from what
// was read: the card, the IDX14 findings ordered by referencing rows times deletes (largest first),
// then the IDX04 findings ordered by the safe copies' size (docs/phase5o-contract.md), and the checks.
// The verdict is the first that applies: nothing could be read (grey); one or more findings (amber,
// one clause per check that has findings, IDX14's first); the foreign keys, their deletes or the
// indexes could not be read (grey); too early to tell (green); none cost work (green); every foreign
// key has an index (green).
func BuildDesignCard(f DesignFacts, def CardDef) (Card, []Finding, []CheckStatus) {
	c := Card{ID: def.ID, Title: def.Title, Question: def.Question, Facts: []Fact{}}
	findings := []Finding{}
	grey := func(note string) (Card, []Finding, []CheckStatus) {
		c.Status, c.StatusText, c.Note = StatusGrey, "Could not be read", note
		return c, findings, designChecks(findings, note, note, note)
	}
	switch {
	case f.Error != "":
		return grey(f.Error)
	case f.State != "" && f.State != "ONLINE":
		return grey(fmt.Sprintf("%s is %s, not online, so its design was not checked.", f.Database, f.State))
	}

	w, windowOK := f.Window()
	fkSkipped := ""
	switch {
	case f.ForeignKeysError != "":
		fkSkipped = "Could not read the foreign keys: " + f.ForeignKeysError
	case f.DeletesError != "":
		fkSkipped = idx14Skipped + f.DeletesError
	case !windowOK:
		fkSkipped = "Could not read when SQL Server started counting the deletes: " + f.WindowError
	}
	dupSkipped := ""
	if f.IndexesError != "" {
		dupSkipped = idx04Skipped + f.IndexesError
	}
	large := 0
	for _, k := range f.ForeignKeys {
		if idx14Large(k) {
			large++
		}
	}
	idx14 := []Finding{}
	if fkSkipped == "" {
		idx14 = idx14Findings(f, w)
	}
	var window *DesignWindow
	if windowOK {
		window = &w
	}
	groups := []DuplicateGroup{}
	if dupSkipped == "" {
		groups = firingGroups(f.IndexTables)
	}
	idx04 := idx04Findings(groups, window, f.ServerNow)
	keySkipped := ""
	if f.NoKeyError != "" {
		keySkipped = des01Skipped + f.NoKeyError
	}
	des01 := []Finding{}
	if keySkipped == "" {
		des01 = des01Findings(f.NoKeyTables)
	}
	findings = append(append(append(findings, idx14...), idx04...), des01...)
	checks := designChecks(findings, fkSkipped, dupSkipped, keySkipped)
	c.Facts = designFacts(f, w, windowOK, idx14, groups, fkSkipped, dupSkipped)
	asOf := f.ServerNow
	c.AsOf = &asOf

	// DES01's Info findings (a unique clustered index, a key in all but name) are listed, not to do.
	actionable := 0
	for _, x := range findings {
		if x.Severity.Actionable() {
			actionable++
		}
	}
	if n := actionable; n > 0 {
		c.Status, c.StatusText = StatusAmber, "1 thing to do"
		if n > 1 {
			c.StatusText = fmt.Sprintf("%d things to do", n)
		}
		clauses := []string{}
		if len(idx14) > 0 {
			clauses = append(clauses, idx14Verdict(f, len(idx14)))
		}
		if len(idx04) > 0 {
			clauses = append(clauses, idx04Verdict(groups, window, f.ServerNow))
		}
		if s := des01Verdict(f.NoKeyTables, des01); s != "" {
			clauses = append(clauses, s)
		}
		c.Verdict = strings.Join(clauses, " ")
		c.ToDo = n
		for _, x := range findings {
			if x.FixID != "" && x.Severity.Actionable() {
				c.Fixable++
			}
		}
		return c, findings, checks
	}
	switch {
	case fkSkipped != "":
		// Without the foreign keys, their deletes or their window IDX14 cannot answer; nothing is raised.
		c.Status, c.StatusText, c.Note = StatusGrey, "Could not be read", fkSkipped
		return c, findings, checks
	case dupSkipped != "":
		c.Status, c.StatusText, c.Note = StatusGrey, "Could not be read", dupSkipped
		return c, findings, checks
	case keySkipped != "":
		c.Status, c.StatusText, c.Note = StatusGrey, "Could not be read", keySkipped
		return c, findings, checks
	}

	since := designDay(w.Since, f.ServerNow)
	switch {
	case large > 0 && w.TooEarly:
		c.Status, c.StatusText = StatusGreen, "Too early to tell"
		keys := fmt.Sprintf("%d foreign keys on large tables have", large)
		if large == 1 {
			keys = "1 foreign key on a large table has"
		}
		c.Verdict = fmt.Sprintf("Too early to tell which foreign keys cost work: deletes counted for %s, since %s. %s no index.",
			designDays(w.Days), w.Text, keys)
	case len(f.ForeignKeys) > 0:
		c.Status, c.StatusText = StatusGreen, "No problem found"
		if large > 0 {
			if large == 1 {
				c.Verdict = fmt.Sprintf("1 foreign key has no index, but it has not cost work since %s: nothing was deleted from the table it points to.", since)
			} else {
				c.Verdict = fmt.Sprintf("%d foreign keys have no index, but none has cost work since %s: nothing was deleted from the tables they point to.", large, since)
			}
		} else {
			c.Verdict = fmt.Sprintf("No foreign key cost work since %s. %s no index, all on tables under %s rows.",
				since, plural(int64(len(f.ForeignKeys)), "foreign key has", "foreign keys have"), n0int(idx14MinRows))
		}
	case f.CountsError == "" && f.EnabledForeignKeys == 0:
		c.Status, c.StatusText, c.Verdict = StatusGreen, "No problem found", "This database has no foreign keys."
	default:
		c.Status, c.StatusText, c.Verdict = StatusGreen, "No problem found", "Every foreign key has an index."
	}
	return c, findings, checks
}

// idx14Verdict is IDX14's clause of the verdict (docs/phase5n-contract.md): the key on the largest
// referencing table.
func idx14Verdict(f DesignFacts, n int) string {
	big := f.ForeignKeys[0]
	for _, k := range f.ForeignKeys {
		if idx14Fires(k) && (!idx14Fires(big) || k.Rows > big.Rows) {
			big = k
		}
	}
	if n == 1 {
		return fmt.Sprintf("1 foreign key makes deletes read a whole table: %s (%s).", designName(big.Schema, big.Table), designRows(big.Rows))
	}
	return fmt.Sprintf("%d foreign keys make deletes read whole tables, the biggest on %s (%s).", n, designName(big.Schema, big.Table), designRows(big.Rows))
}

// designDays is "3 days", "1 day" or "less than a day".
func designDays(days int) string {
	if days < 1 {
		return "less than a day"
	}
	return plural(int64(days), "day", "days")
}

// idx14Findings raises IDX14 per key that fires, largest referencing rows times deletes first.
func idx14Findings(f DesignFacts, w DesignWindow) []Finding {
	keys := []UnindexedForeignKeyFact{}
	for _, k := range f.ForeignKeys {
		if idx14Fires(k) {
			keys = append(keys, k)
		}
	}
	sort.SliceStable(keys, func(i, j int) bool {
		return float64(keys[i].Rows)*float64(keys[i].RefDeletes) > float64(keys[j].Rows)*float64(keys[j].RefDeletes)
	})
	chk, _ := CheckByID("IDX14")
	since := designDay(w.Since, f.ServerNow)
	out := []Finding{}
	for _, k := range keys {
		rank := 4
		if k.Rows >= idx14Rank3Rows {
			rank = 3
		}
		child, parent := designName(k.Schema, k.Table), designName(k.RefSchema, k.RefTable)
		out = append(out, Finding{
			CheckID: "IDX14", Title: chk.Title, Severity: SeverityFromRank(rank), Owner: chk.Owner,
			When: WhenOnline, Effort: EffortQuickWin, Object: QuoteName(k.Schema) + "." + QuoteName(k.Table),
			Finding: fmt.Sprintf("Deleting from %s (%s since %s) makes SQL Server read the whole of %s (%s) %s: no index on %s %s.",
				parent, plural(k.RefDeletes, "row", "rows"), since, child, plural(k.Rows, "row", "rows"), idx14Doing(k), child, idx14Columns(k.Columns)),
			Why:       idx14Why,
			DoThis:    fmt.Sprintf(idx14Do, child, plainList(k.Columns)),
			ThenCheck: chk.ThenCheck,
			Evidence:  idx14Evidence(k, w),
			FixID:     FixCreateIndex,
		})
	}
	return out
}

// designChecks says what became of IDX14 and IDX04: skipped with its note, a finding, or passed.
func designChecks(findings []Finding, fkSkipped, dupSkipped, keySkipped string) []CheckStatus {
	out := []CheckStatus{}
	for _, c := range ChecksFor(CardDesign) {
		st := CheckStatus{CheckID: c.ID, Title: c.Title, Status: CheckPassed}
		skipped := fkSkipped
		switch c.ID {
		case "IDX04":
			skipped = dupSkipped
		case "DES01":
			skipped = keySkipped
		}
		switch {
		case skipped != "":
			st.Status, st.Note = CheckSkipped, skipped
		default:
			for _, x := range findings {
				if x.CheckID == c.ID && x.Severity.Actionable() {
					st.Status = CheckFinding
				}
			}
		}
		out = append(out, st)
	}
	return out
}

// designFacts are the five lines on the card: Foreign keys, Costing work now (IDX14), Duplicate
// indexes (IDX04: "2, 1.2 GB" safe to disable, "3 to merge", or "none"), Counted since, Tables.
func designFacts(f DesignFacts, w DesignWindow, windowOK bool, idx14 []Finding, groups []DuplicateGroup, fkSkipped, dupSkipped string) []Fact {
	keys := Fact{Label: "Foreign keys", Value: fmt.Sprintf("%d without an index", len(f.ForeignKeys)), Tone: ToneNormal}
	switch {
	case f.ForeignKeysError != "":
		keys.Value = "could not be read"
	case f.CountsError == "":
		keys.Value = fmt.Sprintf("%d, %d without an index", f.EnabledForeignKeys, len(f.ForeignKeys))
	}
	costing := Fact{Label: "Costing work now", Value: fmt.Sprint(len(idx14)), Tone: tone(len(idx14) > 0)}
	if fkSkipped != "" {
		costing.Value = "could not be read"
	}
	dups := Fact{Label: "Duplicate indexes", Value: "none", Tone: ToneNormal}
	safe, merge, safeMB := 0, 0, 0.0
	for _, g := range groups {
		safe += len(g.Safe())
		merge += len(g.Merging())
		safeMB += g.SafeMB()
	}
	switch {
	case dupSkipped != "":
		dups.Value = "could not be read"
	case safe > 0:
		dups.Value, dups.Tone = fmt.Sprintf("%d, %s", safe, sizeText(safeMB)), ToneBad
	case merge > 0:
		dups.Value, dups.Tone = fmt.Sprintf("%d to merge", merge), ToneBad
	}
	since := Fact{Label: "Counted since", Value: "could not be read", Tone: ToneNormal}
	if windowOK {
		since.Value = w.Text
	}
	tables := Fact{Label: "Tables", Value: "could not be read", Tone: ToneNormal}
	if f.CountsError == "" {
		tables.Value = n0int(int64(f.UserTables))
	}
	return []Fact{keys, costing, dups, des01Fact(f), since, tables}
}

// IDX04, duplicate indexes (docs/phase5o-contract.md, section 2; PerformanceTriage.sql 426-458 and
// 570-630, taken as it is). Copies are the enabled clustered and nonclustered, non-hypothetical
// indexes of one table with the same key columns in the same order and direction and the same filter.
// The keeper is ranked: primary key first, clustered before nonclustered, unique first, more reads,
// larger, lower index id. A plain copy (nonclustered, not unique, not a primary key) is safe to disable
// when another copy holds every one of its columns (the clustered index holds them all) and that copy
// ranks higher, or is the clustered index, or holds a column it does not (identical copies: only the
// lower-ranked one); the other plain copies of the group need merging, unless a wider index starts
// with the same keys and holds every column (the triage's IDX08 then, not here). Rank 3 when the safe
// copies of a group are 50 MB or more, else 4.
const idx04Rank3MB = 50

// Texts of IDX04 (docs/phase5o-contract.md, section 2). Why: Microsoft Learn, "Index architecture and
// design guide" (every index is kept up to date by each insert, update and delete); Do this: the owner
// rule of docs/phase5n-contract.md (an index is invisible to the application) and the triage's
// "disable rather than drop" (PerformanceTriage.sql 510); the merge case: PerformanceTriage.sql 601.
const (
	idx04Why       = "Indexes with the same keys do the same job: SQL Server keeps every copy up to date on each insert, update and delete, and stores, backs up and maintains each one."
	idx04DoOne     = "Disable %s. Queries that used it use %s, which has the same keys and every column. Tell the vendor, so their next upgrade does not create it again."
	idx04DoMany    = "Disable %s. Queries that used them use the copy named for each, which has the same keys and every column. Tell the vendor, so their next upgrade does not create them again."
	idx04DoMerge   = "Copy for someone experienced with indexes, or for the vendor."
	idx04Skipped   = "Could not read the indexes: "
	idx04SafeMark  = "Safe to disable: "
	idx04MergeMark = "Needs merging: "
)

// DuplicateCopy is one index of an IDX04 group, ranked (1 is the keeper).
type DuplicateCopy struct {
	Index ExistingIndex
	Rank  int
	// KeptIn is the copy that stays when this one is disabled: set only on a safe copy. It is the
	// best-ranked copy that holds every one of its columns and is not itself safe to disable.
	KeptIn string
	// Merge says this plain copy is not safe and no wider index covers it: it needs merging.
	Merge bool
}

// DuplicateGroup is one set of copies of one table: the same keys, in the same order and direction,
// and the same filter.
type DuplicateGroup struct {
	Schema string
	Table  string
	Keys   []IndexKey
	Filter string
	Copies []DuplicateCopy // ranked, the keeper first
}

// Safe are the copies that are safe to disable, largest first.
func (g DuplicateGroup) Safe() []DuplicateCopy {
	out := []DuplicateCopy{}
	for _, c := range g.Copies {
		if c.KeptIn != "" {
			out = append(out, c)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Index.SizeMB > out[j].Index.SizeMB })
	return out
}

// Merging are the plain copies that need merging, by name: none, or two or more.
func (g DuplicateGroup) Merging() []DuplicateCopy {
	out := []DuplicateCopy{}
	for _, c := range g.Copies {
		if c.Merge {
			out = append(out, c)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return strings.ToLower(out[i].Index.Name) < strings.ToLower(out[j].Index.Name) })
	return out
}

// SafeMB is the size of the safe copies.
func (g DuplicateGroup) SafeMB() float64 {
	mb := 0.0
	for _, c := range g.Safe() {
		mb += c.Index.SizeMB
	}
	return mb
}

// Fires says whether the group is an IDX04 finding: a safe copy, or two or more copies to merge.
func (g DuplicateGroup) Fires() bool { return len(g.Safe()) > 0 || len(g.Merging()) >= 2 }

// duplicateCandidate is an index IDX04 groups: enabled, clustered or nonclustered, not hypothetical,
// with key columns.
func duplicateCandidate(x ExistingIndex) bool {
	return (x.Type == 1 || x.Type == 2) && !x.IsHypothetical && !x.IsDisabled && len(x.Keys) > 0
}

// keeperBefore is the triage's keeper order: primary key, clustered, unique, more reads, larger,
// lower index id.
func keeperBefore(a, b ExistingIndex) bool {
	switch {
	case a.IsPrimaryKey != b.IsPrimaryKey:
		return a.IsPrimaryKey
	case a.Type != b.Type:
		return a.Type < b.Type
	case a.IsUnique != b.IsUnique:
		return a.IsUnique
	case a.Reads != b.Reads:
		return a.Reads > b.Reads
	case a.SizeMB != b.SizeMB:
		return a.SizeMB > b.SizeMB
	}
	return a.IndexID < b.IndexID
}

// keeperWhy says why the first ranks before the second: "primary key", "clustered", "unique", "most
// reads", "larger", "lowest index id".
func keeperWhy(a, b ExistingIndex) string {
	switch {
	case a.IsPrimaryKey != b.IsPrimaryKey:
		return "primary key"
	case a.Type != b.Type:
		return "clustered"
	case a.IsUnique != b.IsUnique:
		return "unique"
	case a.Reads != b.Reads:
		return "most reads"
	case a.SizeMB != b.SizeMB:
		return "larger"
	}
	return "lowest index id"
}

// coveredByWider says whether a wider index starts with the same keys and holds every column of a
// (the triage's #Wider, 459-474): such a copy is the triage's IDX08, not a copy to merge.
func coveredByWider(f IndexFacts, a ExistingIndex) bool {
	for _, b := range f.Indexes {
		if b.IndexID == a.IndexID || !duplicateCandidate(b) || b.Filter != a.Filter || len(b.Keys) <= len(a.Keys) {
			continue
		}
		prefix := true
		for i, k := range a.Keys {
			if !strings.EqualFold(b.Keys[i].Name, k.Name) || b.Keys[i].Descending != k.Descending {
				prefix = false
				break
			}
		}
		if prefix && holdsEvery(b, a) {
			return true
		}
	}
	return false
}

// hasColumnNotIn says whether c holds a column d does not.
func hasColumnNotIn(c, d ExistingIndex) bool {
	cols := indexColumns(d)
	for _, n := range indexColumns(c) {
		if !containsFold(cols, n) {
			return true
		}
	}
	return false
}

// DuplicateGroups are a table's IDX04 groups of two or more copies, in the order of their best copy's
// index id, each ranked, with its safe copies and the copies that need merging.
func DuplicateGroups(f IndexFacts) []DuplicateGroup {
	groups := []DuplicateGroup{}
	for _, x := range f.Indexes {
		if !duplicateCandidate(x) {
			continue
		}
		placed := false
		for i := range groups {
			if sameKeys(groups[i].Copies[0].Index, x) {
				groups[i].Copies = append(groups[i].Copies, DuplicateCopy{Index: x})
				placed = true
				break
			}
		}
		if !placed {
			groups = append(groups, DuplicateGroup{Schema: f.Schema, Table: f.Table, Keys: x.Keys, Filter: x.Filter, Copies: []DuplicateCopy{{Index: x}}})
		}
	}
	out := []DuplicateGroup{}
	for _, g := range groups {
		if len(g.Copies) < 2 {
			continue
		}
		sort.SliceStable(g.Copies, func(i, j int) bool { return keeperBefore(g.Copies[i].Index, g.Copies[j].Index) })
		for i := range g.Copies {
			g.Copies[i].Rank = i + 1
		}
		// Safe: the triage's CoveredBy rule.
		safe := make([]bool, len(g.Copies))
		for i, d := range g.Copies {
			if !plainIndex(d.Index) {
				continue
			}
			for j, c := range g.Copies {
				if i != j && holdsEvery(c.Index, d.Index) && (j < i || c.Index.Type == 1 || hasColumnNotIn(c.Index, d.Index)) {
					safe[i] = true
					break
				}
			}
		}
		// The copy that stays for each safe copy: the best-ranked one that holds every column and is not
		// itself safe (one always exists: holding every column carries along a chain of safe copies).
		for i, d := range g.Copies {
			if !safe[i] {
				continue
			}
			for j, c := range g.Copies {
				if i != j && !safe[j] && holdsEvery(c.Index, d.Index) {
					g.Copies[i].KeptIn = c.Index.Name
					break
				}
			}
		}
		// Merging: the plain copies left, unless a wider index covers them; only two or more make a merge
		// (the triage's MergeCount >= 2: one plain copy left is simply the one kept).
		merge := 0
		for i, d := range g.Copies {
			g.Copies[i].Merge = plainIndex(d.Index) && !safe[i] && !coveredByWider(f, d.Index)
			if g.Copies[i].Merge {
				merge++
			}
		}
		if merge < 2 {
			for i := range g.Copies {
				g.Copies[i].Merge = false
			}
		}
		out = append(out, g)
	}
	return out
}

// firingGroups are the IDX04 groups that fire across the tables, the safe copies' size largest first.
func firingGroups(tables []IndexFacts) []DuplicateGroup {
	out := []DuplicateGroup{}
	for _, t := range tables {
		for _, g := range DuplicateGroups(t) {
			if g.Fires() {
				out = append(out, g)
			}
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].SafeMB() > out[j].SafeMB() })
	return out
}

// TableHasDuplicates says whether a table has an IDX04 finding: the create-index guard then names the
// Design card (docs/phase5o-contract.md, section 5).
func TableHasDuplicates(f IndexFacts) bool {
	for _, g := range DuplicateGroups(f) {
		if g.Fires() {
			return true
		}
	}
	return false
}

// groupKeys is "(CustomerId, OrderDate)", with the filter when there is one.
func groupKeys(g DuplicateGroup) string {
	s := "(" + keyText(g.Keys) + ")"
	if g.Filter != "" {
		s += " where " + g.Filter
	}
	return s
}

// copyWhy says what becomes of one copy in the evidence: kept and why, safe to disable, or to merge.
func copyWhy(g DuplicateGroup, c DuplicateCopy) string {
	switch {
	case c.KeptIn != "":
		return "safe to disable, kept in " + QuoteName(c.KeptIn)
	case c.Merge:
		return "needs merging"
	case c.Index.IsPrimaryKey:
		return "kept, primary key"
	case c.Index.Type == 1:
		return "kept, clustered"
	case c.Index.IsUnique:
		return "kept, unique"
	}
	for _, d := range g.Copies {
		if strings.EqualFold(d.KeptIn, c.Index.Name) && c.Rank != 1 {
			return "kept, holds every column of " + QuoteName(d.Index.Name)
		}
	}
	if c.Rank == 1 {
		return "kept, " + keeperWhy(c.Index, g.Copies[1].Index)
	}
	return "kept, a wider index starts with the same keys"
}

// idx04Evidence starts with the safe copies ("Safe to disable: [IX_B] (kept in [IX_A])."), which
// IDX04SafeCopies reads back from a stored finding, or the copies to merge, then each copy: its keys,
// included columns, filter, size, reads and writes in the window, and what becomes of it.
func idx04Evidence(g DuplicateGroup, w *DesignWindow) string {
	parts := []string{}
	if safe := g.Safe(); len(safe) > 0 {
		list := []string{}
		for _, c := range safe {
			list = append(list, QuoteName(c.Index.Name)+" (kept in "+QuoteName(c.KeptIn)+")")
		}
		parts = append(parts, idx04SafeMark+strings.Join(list, ", ")+".")
	} else {
		list := []string{}
		for _, c := range g.Merging() {
			list = append(list, QuoteName(c.Index.Name))
		}
		parts = append(parts, idx04MergeMark+strings.Join(list, ", ")+".")
	}
	since := "since SQL Server started counting"
	if w != nil {
		since = "since " + w.Text
	}
	for _, c := range g.Copies {
		x := c.Index
		inc := "nothing"
		if len(x.Included) > 0 {
			inc = plainList(x.Included)
		}
		filter := "no filter"
		if x.Filter != "" {
			filter = "filter " + x.Filter
		}
		use := "reads and writes not read"
		if x.Reads >= 0 && x.Writes >= 0 {
			use = fmt.Sprintf("%s and %s %s", plural(x.Reads, "read", "reads"), plural(x.Writes, "write", "writes"), since)
		}
		parts = append(parts, fmt.Sprintf("%s (%s): keys %s; includes %s; %s; %s; %s.", QuoteName(x.Name), copyWhy(g, c), keyText(x.Keys), inc, filter, sizeText(x.SizeMB), use))
	}
	return strings.Join(parts, " ")
}

// idx04Findings raises one IDX04 finding per group that fires, in the groups' order.
func idx04Findings(groups []DuplicateGroup, w *DesignWindow, now time.Time) []Finding {
	chk, _ := CheckByID("IDX04")
	since := UsageSince(w, now)
	out := []Finding{}
	for _, g := range groups {
		table := designName(g.Schema, g.Table)
		safe, merge := g.Safe(), g.Merging()
		sentences, names := []string{}, []string{}
		for _, c := range safe {
			sentences = append(sentences, fmt.Sprintf("%s is a copy of %s on %s %s: %s, for nothing. %s holds every column of %s.",
				c.Index.Name, c.KeptIn, table, groupKeys(g), copyCost(c.Index, since), c.KeptIn, c.Index.Name))
			names = append(names, c.Index.Name)
		}
		mergeNames := []string{}
		for _, c := range merge {
			mergeNames = append(mergeNames, c.Index.Name)
		}
		if len(merge) >= 2 {
			other := "the other does not"
			if len(merge) > 2 {
				other = "the others do not"
			}
			sentences = append(sentences, fmt.Sprintf("%s on %s have the same keys (%s) but each holds columns %s. One index with all their columns would replace them.",
				andList(mergeNames), table, keyText(g.Keys), other))
		}
		x := Finding{CheckID: "IDX04", Title: chk.Title, Owner: chk.Owner, When: WhenOnline, Effort: EffortModerate,
			Object: QuoteName(g.Schema) + "." + QuoteName(g.Table), Finding: strings.Join(sentences, " "), Why: idx04Why,
			ThenCheck: chk.ThenCheck, Evidence: idx04Evidence(g, w)}
		rank := 4
		if g.SafeMB() >= idx04Rank3MB {
			rank = 3
		}
		x.Severity = SeverityFromRank(rank)
		switch {
		case len(safe) == 1:
			x.DoThis = fmt.Sprintf(idx04DoOne, safe[0].Index.Name, safe[0].KeptIn)
		case len(safe) > 1:
			x.DoThis = fmt.Sprintf(idx04DoMany, andList(names))
		}
		if len(merge) >= 2 {
			if x.DoThis != "" {
				x.DoThis += fmt.Sprintf(" %s need merging: %s", andList(mergeNames), strings.ToLower(idx04DoMerge[:1])+idx04DoMerge[1:])
			} else {
				x.DoThis = idx04DoMerge
			}
		}
		if len(safe) > 0 {
			x.FixID, x.Effort = FixDisableIndex, EffortQuickWin
		}
		out = append(out, x)
	}
	return out
}

// idx04Verdict is IDX04's clause of the verdict: "2 indexes copy another one: 1.2 GB and 4.3 million
// writes since 27 Oct for nothing.", and the copies to merge.
func idx04Verdict(groups []DuplicateGroup, w *DesignWindow, now time.Time) string {
	safe, merge, mb, writes, usage := 0, 0, 0.0, int64(0), true
	for _, g := range groups {
		for _, c := range g.Safe() {
			safe++
			mb += c.Index.SizeMB
			if c.Index.Writes < 0 {
				usage = false
			} else {
				writes += c.Index.Writes
			}
		}
		merge += len(g.Merging())
	}
	out := []string{}
	if safe > 0 {
		cost := sizeText(mb)
		if usage {
			cost += " and " + countText(writes) + " " + pluralWord(writes, "write", "writes")
			if s := UsageSince(w, now); s != "" {
				cost += " " + s
			}
		}
		lead := fmt.Sprintf("%d indexes copy another one", safe)
		if safe == 1 {
			lead = "1 index copies another one"
		}
		out = append(out, fmt.Sprintf("%s: %s for nothing.", lead, cost))
	}
	if merge > 0 {
		out = append(out, fmt.Sprintf("%d indexes have the same keys as another but hold different columns: merging them is a job for someone experienced with indexes.", merge))
	}
	return strings.Join(out, " ")
}

// IDX04SafeCopies reads the safe copies back from an IDX04 finding's evidence ("Safe to disable: [IX_B]
// (kept in [IX_A]), [IX_E] (kept in [IX_A])."): each copy and the copy that stays. The query page reads
// them from the Design card's last stored read (docs/phase5o-contract.md, section 5). nil when the
// evidence does not start that way.
func IDX04SafeCopies(evidence string) [][2]string {
	if !strings.HasPrefix(evidence, idx04SafeMark) {
		return nil
	}
	rest := evidence[len(idx04SafeMark):]
	out := [][2]string{}
	for {
		index, after, ok := cutBracketed(rest)
		if !ok || !strings.HasPrefix(after, " (kept in ") {
			return out
		}
		keeper, after, ok := cutBracketed(after[len(" (kept in "):])
		if !ok || !strings.HasPrefix(after, ")") {
			return out
		}
		out = append(out, [2]string{index, keeper})
		after = after[1:]
		if !strings.HasPrefix(after, ", ") {
			return out
		}
		rest = after[2:]
	}
}

// cutBracketed reads a [bracketed] name at the start of s (]] inside is ]) and returns it and the rest.
func cutBracketed(s string) (name, rest string, ok bool) {
	if !strings.HasPrefix(s, "[") {
		return "", s, false
	}
	var b strings.Builder
	for i := 1; i < len(s); i++ {
		if s[i] != ']' {
			b.WriteByte(s[i])
			continue
		}
		if i+1 < len(s) && s[i+1] == ']' {
			b.WriteByte(']')
			i++
			continue
		}
		return b.String(), s[i+1:], true
	}
	return "", s, false
}

// DesignForeignKeys is the card page's fold (docs/phase5n-contract.md, section 3): the keys without an
// index on referencing tables of 10,000 rows or more with no delete seen in the window, largest
// referencing table first, one line each and no button. Heading is "Too early to tell" while the window
// is 7 days or less.
type DesignForeignKeys struct {
	Heading  string                 `json:"heading"`
	TooEarly bool                   `json:"tooEarly"`
	Window   string                 `json:"window"` // "the restart on 1 Oct"; "" when not read
	Keys     []DesignForeignKeyLine `json:"keys"`
}

// DesignForeignKeyLine is one key of the fold: "Lines (1,376,845 rows): no index on OrderId, its key to Orders".
type DesignForeignKeyLine struct {
	Name    string   `json:"name"`
	Table   string   `json:"table"`
	Rows    int64    `json:"rows"`
	Parent  string   `json:"parent"`
	Columns []string `json:"columns"`
	Text    string   `json:"text"`
}

// BuildDesignForeignKeys builds the fold from the facts; nil when the foreign keys could not be read.
// Keys whose deletes were not read are listed too (nothing is raised for them).
func BuildDesignForeignKeys(f DesignFacts) *DesignForeignKeys {
	if f.Error != "" || f.ForeignKeysError != "" || (f.State != "" && f.State != "ONLINE") {
		return nil
	}
	out := &DesignForeignKeys{Heading: DesignFoldHeading, Keys: []DesignForeignKeyLine{}}
	if w, ok := f.Window(); ok {
		out.Window = w.Text
		if w.TooEarly {
			out.Heading, out.TooEarly = DesignFoldTooEarly, true
		}
	}
	keys := []UnindexedForeignKeyFact{}
	for _, k := range f.ForeignKeys {
		if idx14Large(k) && !idx14Fires(k) {
			keys = append(keys, k)
		}
	}
	sort.SliceStable(keys, func(i, j int) bool { return keys[i].Rows > keys[j].Rows })
	for _, k := range keys {
		child, parent := designName(k.Schema, k.Table), designName(k.RefSchema, k.RefTable)
		out.Keys = append(out.Keys, DesignForeignKeyLine{Name: k.Name, Table: child, Rows: k.Rows, Parent: parent, Columns: k.Columns,
			Text: fmt.Sprintf("%s (%s): no index on %s, its key to %s", child, plural(k.Rows, "row", "rows"), plainList(k.Columns), parent)})
	}
	return out
}

// Options of the create-index fix built for a foreign key (docs/phase5n-contract.md, section 4): where
// the suggestion came from, the referenced table (for the preview's first line) and the key's name.
const (
	OptionSource     = "source"
	OptionParent     = "parent"
	OptionForeignKey = "foreignKey"
	// SourceForeignKey is the source of a foreign key's index: the query page's action and IDX14's.
	SourceForeignKey = "foreign-key"
)

// ForeignKeyIndexOptions are the create-index options for a foreign key without an index: the
// referencing table, the key's columns as equality in the key's order, nothing included, the
// referenced table and the key's name. The query page's action and IDX14's fix send the same.
func ForeignKeyIndexOptions(database string, k UnindexedForeignKeyFact) map[string]string {
	return map[string]string{
		OptionSource:     SourceForeignKey,
		OptionTable:      QuoteName(database) + "." + QuoteName(k.Schema) + "." + QuoteName(k.Table),
		OptionEquality:   bracketList(k.Columns),
		OptionInequality: "",
		OptionIncluded:   "",
		OptionParent:     QuoteName(k.RefSchema) + "." + QuoteName(k.RefTable),
		OptionForeignKey: k.Name,
	}
}

// AvailableDesignFixes are the fixes of the Design card's findings: one create-index per IDX14
// finding, as performanceIndexFixes does for WRK03, carrying the finding's check (IDX14) and its
// object, so the card page sends them with the first preview; and one disable-index per safe copy of
// an IDX04 finding (docs/phase5o-contract.md, section 2), with its table, the index, the copy that
// stays and the finding's object.
func AvailableDesignFixes(f DesignFacts, findings []Finding) []Fix {
	out := []Fix{}
	def, _ := FindFixDef(FixCreateIndex)
	disable, _ := FindFixDef(FixDisableIndex)
	addKey, _ := FindFixDef(FixAddPrimaryKey)
	for _, x := range findings {
		if x.CheckID == "DES01" && x.FixID == FixAddPrimaryKey {
			// Phase 5s (docs/phase5s-contract.md, section 2): one add-primary-key per table.
			for _, t := range f.NoKeyTables {
				if t.Object() == x.Object {
					out = append(out, Fix{ID: addKey.ID, Title: addKey.Title, Scope: addKey.Scope, CheckID: x.CheckID, Setting: addKey.Setting,
						Current: "none", Target: "primary key on " + designName(t.Schema, t.Table), Options: PrimaryKeyOptions(t)})
					break
				}
			}
			continue
		}
		if x.CheckID == "IDX04" && x.FixID == FixDisableIndex {
			for _, t := range f.IndexTables {
				if QuoteName(t.Schema)+"."+QuoteName(t.Table) != x.Object {
					continue
				}
				for _, g := range DuplicateGroups(t) {
					for _, c := range g.Safe() {
						if !strings.Contains(x.Evidence, QuoteName(c.Index.Name)+" (kept in "+QuoteName(c.KeptIn)+")") {
							continue
						}
						opts := DisableIndexOptions(t.Schema, t.Table, c.Index.Name, c.KeptIn)
						opts[OptionObject] = x.Object
						out = append(out, Fix{ID: disable.ID, Title: disable.Title, Scope: disable.Scope, CheckID: x.CheckID, Setting: disable.Setting,
							Current: disableCurrent(t, c.Index), Target: "disabled", Options: opts})
					}
				}
			}
			continue
		}
		if x.CheckID != "IDX14" || x.FixID != FixCreateIndex {
			continue
		}
		for _, k := range f.ForeignKeys {
			if !idx14Fires(k) || QuoteName(k.Schema)+"."+QuoteName(k.Table) != x.Object || !strings.HasPrefix(x.Evidence, "Foreign key "+k.Name+":") {
				continue
			}
			opts := ForeignKeyIndexOptions(f.Database, k)
			opts[OptionObject] = x.Object
			out = append(out, Fix{ID: def.ID, Title: def.Title, Scope: def.Scope, CheckID: x.CheckID, Setting: def.Setting,
				Current: "none", Target: fmt.Sprintf("index on %s (%s)", designName(k.Schema, k.Table), plainList(k.Columns)), Options: opts})
			break
		}
	}
	return out
}
