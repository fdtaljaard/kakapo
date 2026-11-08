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
// were deleted from the referenced table in the window.

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
	if f.WindowError != "" || f.StartedAt.IsZero() {
		return DesignWindow{}, false
	}
	w := DesignWindow{Since: f.StartedAt, Kind: DesignSinceRestart}
	if f.LastRestore != nil && f.LastRestore.After(w.Since) {
		w.Since, w.Kind = *f.LastRestore, DesignSinceRestore
	}
	if f.CreateDate.After(w.Since) {
		w.Since, w.Kind = f.CreateDate, DesignSinceCreated
	}
	span := f.ServerNow.Sub(w.Since)
	w.Days = int(span.Hours() / 24)
	w.TooEarly = span <= designEarlyDays*24*time.Hour
	day := designDay(w.Since, f.ServerNow)
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

	// DesignFoldHeading and DesignFoldTooEarly head the card page's fold of the keys that would cost
	// work if rows were deleted.
	DesignFoldHeading  = "Would cost work if rows were deleted"
	DesignFoldTooEarly = "Too early to tell"
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
// and the checks. The verdict is the first that applies: the foreign keys could not be read (grey);
// one or more findings (amber); too early to tell (green); none cost work (green); every foreign key
// has an index (green).
func BuildDesignCard(f DesignFacts, def CardDef) (Card, []Finding, []CheckStatus) {
	c := Card{ID: def.ID, Title: def.Title, Question: def.Question, Facts: []Fact{}}
	findings := []Finding{}
	grey := func(note string) (Card, []Finding, []CheckStatus) {
		c.Status, c.StatusText, c.Note = StatusGrey, "Could not be read", note
		return c, findings, designChecks(findings, note)
	}
	switch {
	case f.Error != "":
		return grey(f.Error)
	case f.State != "" && f.State != "ONLINE":
		return grey(fmt.Sprintf("%s is %s, not online, so its design was not checked.", f.Database, f.State))
	case f.ForeignKeysError != "":
		return grey("Could not read the foreign keys: " + f.ForeignKeysError)
	}

	w, windowOK := f.Window()
	skipped := ""
	switch {
	case f.DeletesError != "":
		skipped = idx14Skipped + f.DeletesError
	case !windowOK:
		skipped = "Could not read when SQL Server started counting the deletes: " + f.WindowError
	}
	large := 0
	for _, k := range f.ForeignKeys {
		if idx14Large(k) {
			large++
		}
	}
	if skipped == "" {
		findings = idx14Findings(f, w)
	}
	checks := designChecks(findings, skipped)
	c.Facts = designFacts(f, w, windowOK, findings, skipped)
	asOf := f.ServerNow
	c.AsOf = &asOf
	if skipped != "" {
		// Without the deletes or their window IDX14 cannot answer; nothing is raised.
		c.Status, c.StatusText, c.Note = StatusGrey, "Could not be read", skipped
		return c, findings, checks
	}

	since := designDay(w.Since, f.ServerNow)
	switch n := len(findings); {
	case n > 0:
		c.Status, c.StatusText = StatusAmber, "1 thing to do"
		if n > 1 {
			c.StatusText = fmt.Sprintf("%d things to do", n)
		}
		big := f.ForeignKeys[0]
		for _, k := range f.ForeignKeys {
			if idx14Fires(k) && (!idx14Fires(big) || k.Rows > big.Rows) {
				big = k
			}
		}
		if n == 1 {
			c.Verdict = fmt.Sprintf("1 foreign key makes deletes read a whole table: %s (%s).", designName(big.Schema, big.Table), designRows(big.Rows))
		} else {
			c.Verdict = fmt.Sprintf("%d foreign keys make deletes read whole tables, the biggest on %s (%s).", n, designName(big.Schema, big.Table), designRows(big.Rows))
		}
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
			parents := "their parent tables"
			if large == 1 {
				parents = "its parent table"
			}
			c.Verdict = fmt.Sprintf("No foreign key cost work since %s. %d would if rows were deleted from %s.", since, large, parents)
		} else {
			c.Verdict = fmt.Sprintf("No foreign key cost work since %s. %s no index, all on tables under %s rows.",
				since, plural(int64(len(f.ForeignKeys)), "foreign key has", "foreign keys have"), n0int(idx14MinRows))
		}
	case f.CountsError == "" && f.EnabledForeignKeys == 0:
		c.Status, c.StatusText, c.Verdict = StatusGreen, "No problem found", "This database has no foreign keys."
	default:
		c.Status, c.StatusText, c.Verdict = StatusGreen, "No problem found", "Every foreign key has an index."
	}
	c.ToDo, c.Fixable = len(findings), len(findings)
	return c, findings, checks
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

// designChecks says what became of IDX14: skipped with its note, a finding, or passed.
func designChecks(findings []Finding, skipped string) []CheckStatus {
	out := []CheckStatus{}
	for _, c := range ChecksFor(CardDesign) {
		st := CheckStatus{CheckID: c.ID, Title: c.Title, Status: CheckPassed}
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

// designFacts are the four lines on the card: Foreign keys, Costing work now, Counted since, Tables.
func designFacts(f DesignFacts, w DesignWindow, windowOK bool, findings []Finding, skipped string) []Fact {
	keys := Fact{Label: "Foreign keys", Value: fmt.Sprintf("%d without an index", len(f.ForeignKeys)), Tone: ToneNormal}
	if f.CountsError == "" {
		keys.Value = fmt.Sprintf("%d, %d without an index", f.EnabledForeignKeys, len(f.ForeignKeys))
	}
	costing := Fact{Label: "Costing work now", Value: fmt.Sprint(len(findings)), Tone: tone(len(findings) > 0)}
	if skipped != "" {
		costing.Value = "could not be read"
	}
	since := Fact{Label: "Counted since", Value: "could not be read", Tone: ToneNormal}
	if windowOK {
		since.Value = w.Text
	}
	tables := Fact{Label: "Tables", Value: "could not be read", Tone: ToneNormal}
	if f.CountsError == "" {
		tables.Value = n0int(int64(f.UserTables))
	}
	return []Fact{keys, costing, since, tables}
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

// DesignForeignKeyLine is one key of the fold: "Lines (1,376,845 rows) → Orders, on OrderId".
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
			Text: fmt.Sprintf("%s (%s) → %s, on %s", child, plural(k.Rows, "row", "rows"), parent, plainList(k.Columns))})
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
// object, so the card page sends them with the first preview.
func AvailableDesignFixes(f DesignFacts, findings []Finding) []Fix {
	out := []Fix{}
	def, _ := FindFixDef(FixCreateIndex)
	for _, x := range findings {
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
