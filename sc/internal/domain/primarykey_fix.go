package domain

import (
	"fmt"
	"math"
	"regexp"
	"strings"
)

// The add-primary-key fix (docs/phase5s-contract.md, sections 3 to 5): DES01's Apply. The key is chosen
// from what the table has (a unique index on NOT NULL columns, then the identity column), else a new
// bigint identity column, which changes the table's shape and is held back until the user says the
// application names its columns when it inserts, and while kakapo finds code that would break.
//
// Sources: Microsoft Learn, "Primary and foreign key constraints" (a primary key's columns are NOT NULL;
// its index is clustered unless a clustered index exists or NONCLUSTERED is named); "Create primary keys"
// (adding one checks every row and fails on a duplicate); "ALTER TABLE (Transact-SQL)" (ADD ... IDENTITY on
// a table with rows fills every row; the existing rows' values follow no particular order); "Guidelines for
// online index operations" (Enterprise only).

const (
	FixAddPrimaryKey = "add-primary-key"
	// The options besides table, name (the key's) and online, which the index fixes define.
	OptionPKKey      = "key"       // the candidate, as the select offers it
	OptionPKColumn   = "column"    // the new column's name
	OptionClustered  = "clustered" // "clustered" or "nonclustered"
	OptionPKConfirm  = "confirm"   // "true": the application names its columns when it inserts
	DefaultKeyColumn = "kakapo_id"
	clusteredWord    = "clustered"
	nonclusteredWord = "nonclustered"
)

// TableCodeUse is one piece of code that names the table: a procedure, view, function or trigger (Kind
// its type_desc; OnTable for a trigger on the table itself) or a query Query Store ran (QueryID).
type TableCodeUse struct {
	Kind    string
	Schema  string
	Name    string
	OnTable bool
	QueryID int64
	Text    string
}

// TableCodeSearch is what collect.SearchTableCode found: the code that names the table and the queries
// Query Store ran in the last 30 days that insert and name it, each area with its error.
type TableCodeSearch struct {
	Modules         []TableCodeUse
	ModulesError    string
	QueryStore      []TableCodeUse
	QueryStoreError string
	QueryStoreOff   bool
}

// PrimaryKeyFacts is what the add-primary-key fix reads about one table (collect.PrimaryKeyFacts).
type PrimaryKeyFacts struct {
	Found      bool
	Table      NoKeyTableFact
	Index      IndexFacts // collect.IndexFacts: version, edition, columns, indexes and sizes
	Names      []string   // every object name in the table's schema (a constraint's name must be new there)
	NamesError string
	Code       TableCodeSearch
}

// pkPlan is a key resolved against the facts.
type pkPlan struct {
	choice    string
	columns   []string
	unique    *UniqueKeyCandidate
	identity  bool
	newColumn bool
	name      string
	column    string // the new column
	clustered bool
	online    bool
}

// pkChoices are the select's values, the default first: each unique index, the identity column, a new
// column.
func pkChoices(t NoKeyTableFact) []string {
	out := []string{}
	for _, c := range t.Candidates() {
		out = append(out, fmt.Sprintf("Unique index %s (%s)", c.Index, plainList(c.Columns)))
	}
	if t.Identity != "" {
		out = append(out, "Identity column "+t.Identity)
	}
	return append(out, "New column (bigint identity)")
}

// DefaultPrimaryKeyName is PK_{table}, at most 128 characters.
func DefaultPrimaryKeyName(table string) string {
	n := "PK_" + table
	if r := []rune(n); len(r) > 128 {
		n = string(r[:128])
	}
	return n
}

// resolvePK reads the options against the facts: the key, the names, clustered and online, and what is
// wrong with each input ("" when nothing).
func resolvePK(f PrimaryKeyFacts, options map[string]string) (p pkPlan, keyWarn, nameWarn, columnWarn string) {
	t := f.Table
	choices := pkChoices(t)
	p.choice = choices[0]
	if v := strings.TrimSpace(options[OptionPKKey]); v != "" {
		p.choice = v
		ok := false
		for _, c := range choices {
			ok = ok || c == v
		}
		if !ok {
			keyWarn = "Choose one of: " + strings.Join(choices, "; ") + "."
		}
	}
	for _, c := range t.Candidates() {
		if p.choice == fmt.Sprintf("Unique index %s (%s)", c.Index, plainList(c.Columns)) {
			c := c
			p.unique, p.columns = &c, c.Columns
		}
	}
	switch {
	case p.unique != nil:
	case t.Identity != "" && p.choice == "Identity column "+t.Identity:
		p.identity, p.columns = true, []string{t.Identity}
	default:
		p.newColumn = true
		p.column = strings.TrimSpace(options[OptionPKColumn])
		if p.column == "" {
			p.column = DefaultKeyColumn
		}
		p.columns = []string{p.column}
		switch {
		case len([]rune(p.column)) > 128:
			columnWarn = "A column name has at most 128 characters."
		default:
			if _, taken := f.Index.column(p.column); taken {
				columnWarn = fmt.Sprintf("%s already has a column named %s: choose another name.", designName(t.Schema, t.Table), p.column)
			}
		}
	}

	p.name = strings.TrimSpace(options[OptionIndexName])
	if p.name == "" {
		p.name = DefaultPrimaryKeyName(t.Table)
	}
	switch {
	case len([]rune(p.name)) > 128:
		nameWarn = "A key's name has at most 128 characters."
	default:
		for _, n := range f.Names {
			if strings.EqualFold(n, p.name) {
				nameWarn = fmt.Sprintf("Schema %s already has an object named %s: choose another name.", t.Schema, p.name)
			}
		}
	}

	p.clustered = t.Heap
	if t.Heap && strings.EqualFold(strings.TrimSpace(options[OptionClustered]), nonclusteredWord) {
		p.clustered = false
	}
	p.online = OnlineIndexEdition(f.Index.EngineEdition) && !p.newColumn
	if p.online && strings.EqualFold(strings.TrimSpace(options[OptionOnline]), "false") {
		p.online = false
	}
	return p, keyWarn, nameWarn, columnWarn
}

func (p pkPlan) kind() string {
	if p.clustered {
		return clusteredWord
	}
	return nonclusteredWord
}

// pkTarget is "PK_StockMoves on dbo.StockMoves (kakapo_id, a new bigint identity column), clustered".
func pkTarget(t NoKeyTableFact, p pkPlan) string {
	cols := plainList(p.columns)
	if p.newColumn {
		cols += ", a new bigint identity column"
	}
	return fmt.Sprintf("%s on %s (%s), %s", p.name, t.Name(), cols, p.kind())
}

// pkStatements are the statement and its rollback, on the table's three-part name.
func pkStatements(f PrimaryKeyFacts, p pkPlan) (statement, rollback string) {
	table := f.Index.qualified()
	kind := "CLUSTERED"
	if !p.clustered {
		kind = "NONCLUSTERED"
	}
	if p.newColumn {
		statement = fmt.Sprintf("ALTER TABLE %s ADD %s bigint IDENTITY(1, 1) NOT NULL CONSTRAINT %s PRIMARY KEY %s;", table, QuoteName(p.column), QuoteName(p.name), kind)
		rollback = fmt.Sprintf("ALTER TABLE %s DROP CONSTRAINT %s; ALTER TABLE %s DROP COLUMN %s;", table, QuoteName(p.name), table, QuoteName(p.column))
		return statement, rollback
	}
	statement = fmt.Sprintf("ALTER TABLE %s ADD CONSTRAINT %s PRIMARY KEY %s (%s) WITH (ONLINE = %s, SORT_IN_TEMPDB = ON);",
		table, QuoteName(p.name), kind, bracketList(p.columns), onOff(p.online))
	rollback = fmt.Sprintf("ALTER TABLE %s DROP CONSTRAINT %s;", table, QuoteName(p.name))
	return statement, rollback
}

// pkNewMB is the size of what the statement builds: the whole table for a clustered key (8 bytes a row
// more with a new column), else the key's index.
func pkNewMB(f PrimaryKeyFacts, p pkPlan) float64 {
	t := f.Table
	if p.clustered {
		mb := t.DataMB
		if p.newColumn {
			mb += float64(t.Rows) * 8 / 1024 / 1024
		}
		return mb
	}
	fi := f.Index
	if p.newColumn {
		fi.Columns = append(append([]TableColumn{}, fi.Columns...), TableColumn{Name: p.column, Type: "bigint", MaxLength: 8})
	}
	return estimateIndexMB(fi, p.columns, nil)
}

// Warnings of the add-primary-key fix.
const (
	warnPKUndoFK     = "A foreign key created later that points at the key stops the rollback until it is dropped."
	warnPKOrder      = "The existing rows get numbers in no particular order; new rows get the next ones."
	warnPKCodeSearch = "kakapo searched the database's code and the queries Query Store saw in the last 30 days for such inserts; code outside SQL Server (the application's own) cannot be searched."
)

// PKConfirmLabel is the confirm input's label.
func PKConfirmLabel(t NoKeyTableFact) string {
	return "The application names its columns when it inserts into " + designName(t.Schema, t.Table)
}

// BuildAddPrimaryKeyFix builds the add-primary-key preview from what was read about the table and the
// options (docs/phase5s-contract.md, sections 3 and 4). A refusal leaves the preview without changes or
// inputs, its first warning saying why; a wrong input keeps the inputs with the field's warning; the new
// column's preview is Held until confirmed and while code that would break was found.
func BuildAddPrimaryKeyFix(f PrimaryKeyFacts, options map[string]string) (FixPreview, Fix) {
	t := f.Table
	def, _ := FindFixDef(FixAddPrimaryKey)
	name := designName(t.Schema, t.Table)
	fix := Fix{ID: def.ID, Title: def.Title, Scope: def.Scope, CheckID: def.CheckID, Setting: def.Setting, Current: "none"}
	pv := FixPreview{Database: f.Index.Database, EngineVersion: f.Index.ProductVersion, Changes: []Drift{}, Warnings: []string{}, Options: map[string]string{}}
	refuse := func(why string) (FixPreview, Fix) {
		pv.Warnings = append(pv.Warnings, why)
		return pv, fix
	}
	switch {
	case !f.Found:
		return refuse("There is no table " + t.Name() + " in " + f.Index.Database + ".")
	case t.HasPrimaryKey:
		return refuse(fmt.Sprintf("%s has a primary key now (%s): nothing to add.", name, t.PrimaryKey))
	case t.TemporalType == 1 || t.LedgerType == 1:
		return refuse(name + " is a history table: SQL Server does not allow a primary key on it.")
	case t.MemoryOptimized || t.External || t.Graph:
		return refuse(name + " is not an ordinary table (memory-optimized, external or graph): kakapo does not change its keys.")
	case t.Refusal() != "":
		return refuse(t.Refusal())
	}

	p, keyWarn, nameWarn, columnWarn := resolvePK(f, options)
	echo := map[string]string{}
	for k, v := range options {
		echo[k] = v
	}
	echo[OptionPKKey], echo[OptionIndexName] = p.choice, p.name
	if p.newColumn {
		echo[OptionPKColumn] = p.column
	} else {
		delete(echo, OptionPKColumn)
	}
	if t.Heap {
		echo[OptionClustered] = p.kind()
	} else {
		delete(echo, OptionClustered)
	}
	if OnlineIndexEdition(f.Index.EngineEdition) && !p.newColumn {
		echo[OptionOnline] = fmt.Sprint(p.online)
	} else {
		delete(echo, OptionOnline)
	}
	confirmed := strings.EqualFold(strings.TrimSpace(options[OptionPKConfirm]), "true")
	if p.newColumn {
		echo[OptionPKConfirm] = fmt.Sprint(confirmed)
	} else {
		delete(echo, OptionPKConfirm)
	}
	pv.Options = echo
	fix.Options = echo

	pv.Inputs = []FixInput{{Name: OptionPKKey, Label: "Key", Kind: "select", Options: pkChoices(t), Value: p.choice, Warning: keyWarn,
		Help: "kakapo picks a unique index first, then the identity column; a new column is the last resort, because it changes the table's shape."}}
	if p.newColumn {
		pv.Inputs = append(pv.Inputs, FixInput{Name: OptionPKColumn, Label: "New column", Kind: "text", Value: p.column, Warning: columnWarn,
			Help: "kakapo's name, so nobody mistakes it for the application's own column."})
	}
	pv.Inputs = append(pv.Inputs, FixInput{Name: OptionIndexName, Label: "Key name", Kind: "text", Value: p.name, Warning: nameWarn,
		Help: "The primary key's name; it must be new in schema " + t.Schema + "."})
	if t.Heap {
		pv.Inputs = append(pv.Inputs, FixInput{Name: OptionClustered, Label: "Clustered", Kind: "select", Options: []string{clusteredWord, nonclusteredWord},
			Value: p.kind(), Advanced: true,
			Help: "Clustered turns the heap into a clustered table, which also ends IDX06. A nonclustered key leaves the heap as it is."})
	}
	if OnlineIndexEdition(f.Index.EngineEdition) && !p.newColumn {
		pv.Inputs = append(pv.Inputs, FixInput{Name: OptionOnline, Label: "Build it online", Kind: "bool", Value: fmt.Sprint(p.online),
			Help: "Users keep working while it builds. Offline is faster but locks the table."})
	}
	if p.newColumn {
		pv.Inputs = append(pv.Inputs, FixInput{Name: OptionPKConfirm, Label: PKConfirmLabel(t), Kind: "bool", Value: fmt.Sprint(confirmed),
			Help: "Ask the vendor if you are not sure: an INSERT without a column list fails once the table has a new column."})
	}
	for _, w := range []string{keyWarn, columnWarn, nameWarn} {
		if w != "" {
			pv.Warnings = append(pv.Warnings, w)
		}
	}
	if keyWarn != "" || columnWarn != "" || nameWarn != "" {
		return pv, fix
	}

	target := pkTarget(t, p)
	fix.Target = target
	pv.Lead = fmt.Sprintf("%s gets a primary key on %s: SQL Server will refuse a second row with the same key.", name, plainList(p.columns))
	pv.Changes = append(pv.Changes, Drift{Setting: "Primary key", Current: "none", Target: target})
	pv.Statement, pv.Rollback = pkStatements(f, p)

	newMB := pkNewMB(f, p)
	if mb := int64(math.Ceil(newMB)); mb > 0 {
		data := mb
		basis := "the table's size (the heap is rewritten as a clustered index)"
		if p.clustered && t.Nonclustered > 0 {
			data += int64(math.Ceil(t.NonclusteredMB))
			basis += " and its nonclustered indexes, rebuilt"
		}
		if !p.clustered {
			basis = "the estimated size of the key's index (rows x key columns)"
		}
		pv.Needs = append(pv.Needs,
			RoomNeed{Path: RoleData, Part: RoomPart{Label: "the primary key", MB: data, Basis: basis + "; the data files may have room inside already"}},
			RoomNeed{Path: RoleTempdb, Part: RoomPart{Label: "the sort (SORT_IN_TEMPDB)", MB: mb, Basis: basis + "; in FULL recovery the log takes about as much"}})
	}

	pv.Warnings = append(pv.Warnings, pkRewriteWarning(t, p, newMB))
	if p.unique != nil {
		pv.Warnings = append(pv.Warnings, fmt.Sprintf("The unique index %s stays: it is then a copy of the key. Disable it later if nothing uses it (a foreign key may).", p.unique.Index))
	}
	pv.Warnings = append(pv.Warnings, pkLockWarning(f, p), warnIndexLock)
	if f.Index.EngineEdition == 4 {
		pv.Warnings = append(pv.Warnings, fmt.Sprintf("On Express a database holds at most %d GB of data: the table's new copy counts towards it while it is built.", ExpressLimitMB(f.Index.ProductVersion)/1024))
	}
	if t.Rows >= 1000000 || t.DataMB > 1024 {
		pv.Warnings = append(pv.Warnings, warnIndexLarge)
	}
	if p.identity {
		pv.Warnings = append(pv.Warnings, fmt.Sprintf("SQL Server refuses the key if two rows share a value of %s (an identity can repeat after a reseed or IDENTITY_INSERT), and then nothing changes.", t.Identity))
	}
	if p.newColumn {
		pv.Warnings = append(pv.Warnings,
			fmt.Sprintf("A new column changes the table's shape: an INSERT into %s without a column list, or one that copies its rows with SELECT *, fails afterwards (error 213).", name),
			warnPKOrder, warnPKCodeSearch)
		for _, x := range f.Code.Failures() {
			pv.Warnings = append(pv.Warnings, x)
		}
	}
	pv.Warnings = append(pv.Warnings, pkUndoWarning(t, p), warnPKUndoFK)
	tell := "Tell the vendor: their next upgrade must expect this key."
	if p.newColumn {
		tell = "Tell the vendor: their next upgrade must expect this key and column."
	}
	pv.Warnings = append(pv.Warnings, tell, indexPermission(t.Name()))

	if p.newColumn {
		pv.Held = pkHeld(f, confirmed)
	}
	return pv, fix
}

// pkRewriteWarning says what the statement rewrites.
func pkRewriteWarning(t NoKeyTableFact, p pkPlan, newMB float64) string {
	fill := ""
	if p.newColumn {
		fill = "It writes every row to fill the new column. "
	}
	if p.clustered {
		s := fmt.Sprintf("%sThis rewrites the whole of %s (%s) as a clustered index", fill, designName(t.Schema, t.Table), sizeText(t.DataMB))
		if t.Nonclustered > 0 {
			s += fmt.Sprintf(" and rebuilds its %s, which then carry the key instead of the row's address", plural(int64(t.Nonclustered), "nonclustered index", "nonclustered indexes"))
		}
		return s + "."
	}
	return fmt.Sprintf("%sThis builds one new index of about %s.", fill, sizeText(math.Max(newMB, 0.1)))
}

// pkLockWarning is the locking line: online, or offline and why.
func pkLockWarning(f PrimaryKeyFacts, p pkPlan) string {
	name := designName(f.Table.Schema, f.Table.Table)
	if p.online {
		return warnIndexOnline
	}
	why := "Offline"
	switch {
	case p.newColumn:
		why = "Adding a column is always offline"
	case !OnlineIndexEdition(f.Index.EngineEdition):
		why = "On " + editionWords(f.Index.EngineEdition) + " it runs offline"
	}
	return fmt.Sprintf("%s: %s can be neither read nor written until it finishes, and users who touch it wait. Apply it in a quiet moment.", why, name)
}

// pkUndoWarning says what the rollback costs.
func pkUndoWarning(t NoKeyTableFact, p pkPlan) string {
	s := "Undoing it (the rollback) drops the key"
	if p.clustered {
		s += ": the table becomes a heap again and is rewritten once more"
	}
	if p.newColumn {
		s += "; the dropped column's 8 bytes a row stay until the table is rebuilt"
	}
	return s + "."
}

// pkHeld is why the new column's Apply is held back: not confirmed, or code that would break.
func pkHeld(f PrimaryKeyFacts, confirmed bool) string {
	name := designName(f.Table.Schema, f.Table.Table)
	if places := f.Code.Breaks(f.Table.Schema, f.Table.Table); len(places) > 0 {
		return fmt.Sprintf("%s inserts into %s without a column list (or with SELECT *): a new column makes it fail (error 213). Change the code first, or ask the vendor.",
			strings.Join(places, ", "), name)
	}
	if !confirmed {
		return fmt.Sprintf("Tick \"%s\" first: a new column makes an INSERT without a column list fail.", PKConfirmLabel(f.Table))
	}
	return ""
}

// Failures are the areas of the code search that could not be read.
func (s TableCodeSearch) Failures() []string {
	out := []string{}
	if s.ModulesError != "" {
		out = append(out, "Could not search the database's code: "+s.ModulesError)
	}
	if s.QueryStoreError != "" {
		out = append(out, "Could not search Query Store: "+s.QueryStoreError)
	}
	return out
}

// Breaks are the places whose code inserts into the table without a column list, or copies its rows with
// SELECT * into an INSERT, at most three: "dbo.ImportMoves", "query 42 in Query Store".
func (s TableCodeSearch) Breaks(schema, table string) []string {
	out := []string{}
	for _, list := range [][]TableCodeUse{s.Modules, s.QueryStore} {
		for _, u := range list {
			if len(out) == 3 {
				return out
			}
			if !InsertBreaksOnNewColumn(u.Text, schema, table, u.OnTable) {
				continue
			}
			if u.QueryID != 0 {
				out = append(out, fmt.Sprintf("query %d in Query Store", u.QueryID))
				continue
			}
			n := u.Name
			if u.Schema != "" {
				n = u.Schema + "." + u.Name
			}
			out = append(out, n)
		}
	}
	return out
}

var (
	sqlLineComment  = regexp.MustCompile(`--[^\n]*`)
	sqlBlockComment = regexp.MustCompile(`(?s)/\*.*?\*/`)
	sqlString       = regexp.MustCompile(`N?'(?:[^']|'')*'`)
)

// sqlNamePart is one part of a name: [bracketed], "quoted" or a plain identifier.
const sqlNamePart = `(?:\[(?:[^\]]|\]\])+\]|"[^"]+"|[A-Za-z_@#][\w@#$]*)`

// tableNamePattern matches the table's name with or without its schema and database, bracketed or not.
func tableNamePattern(schema, table string) string {
	one := func(s string) string {
		return `(?:\[` + regexp.QuoteMeta(strings.ReplaceAll(s, "]", "]]")) + `\]|"` + regexp.QuoteMeta(s) + `"|` + regexp.QuoteMeta(s) + `\b)`
	}
	return `(?:(?:` + sqlNamePart + `\s*\.\s*)?` + one(schema) + `\s*\.\s*)?` + one(table)
}

// InsertBreaksOnNewColumn says whether code fails once the table has a new column (docs/phase5s-contract.md,
// section 4): an INSERT into it without a column list (DEFAULT VALUES excepted), or an INSERT whose rows
// come from SELECT * on it (or on inserted and deleted, in a trigger on the table: onTable). Comments and
// strings are left out; a false positive only holds Apply back, and Copy script still works.
func InsertBreaksOnNewColumn(text, schema, table string, onTable bool) bool {
	s := sqlBlockComment.ReplaceAllString(text, " ")
	s = sqlLineComment.ReplaceAllString(s, " ")
	s = sqlString.ReplaceAllString(s, "''")
	target := tableNamePattern(schema, table)
	into := regexp.MustCompile(`(?is)\bINSERT\s+(?:INTO\s+)?` + target + `\s*(?:WITH\s*\([^)]*\)\s*)?(\S+)`)
	for _, m := range into.FindAllStringSubmatch(s, -1) {
		next := strings.ToUpper(m[1])
		if !strings.HasPrefix(next, "(") && !strings.HasPrefix(next, "DEFAULT") {
			return true
		}
	}
	source := target
	if onTable {
		source = `(?:` + target + `|inserted\b|deleted\b)`
	}
	star := regexp.MustCompile(`(?is)\bINSERT\s+(?:INTO\s+)?` + sqlNamePart + `(?:\s*\.\s*` + sqlNamePart + `){0,2}\s*(?:WITH\s*\([^)]*\)\s*)?(?:\([^)]*\)\s*)?` +
		`SELECT\s+(?:DISTINCT\s+)?(?:TOP\s*\(?\s*\d+\s*\)?\s*(?:PERCENT\s+)?)?\*\s*FROM\s+` + source)
	return star.MatchString(s)
}

// PrimaryKeyReadBack says whether the key read back after the statement is the one the preview built:
// named as previewed, enabled, clustered or not as previewed, its columns in order; for a new column also
// the column, bigint and identity. after is what the change row and the result say.
func PrimaryKeyReadBack(after PrimaryKeyFacts, preview FixPreview) (string, bool) {
	o := preview.Options
	want := o[OptionIndexName]
	var got *ExistingIndex
	for i, x := range after.Index.Indexes {
		if x.IsPrimaryKey {
			got = &after.Index.Indexes[i]
		}
	}
	if got == nil {
		return fmt.Sprintf("no primary key on %s after the statement", after.Table.Name()), false
	}
	kind := nonclusteredWord
	if got.Type == 1 {
		kind = clusteredWord
	}
	text := fmt.Sprintf("%s on %s (%s), %s, %s", got.Name, after.Table.Name(), plainList(got.KeyNames()), kind, sizeText(got.SizeMB))
	ok := strings.EqualFold(got.Name, want) && !got.IsDisabled
	wantCols := []string{}
	if col := o[OptionPKColumn]; col != "" && strings.Contains(preview.Statement, " IDENTITY(1, 1) ") {
		wantCols = []string{col}
		ok = ok && strings.EqualFold(after.Table.Identity, col) && strings.EqualFold(after.Table.IdentityType, "bigint")
	} else if len(preview.Changes) > 0 {
		wantCols = previewColumns(preview.Changes[0].Target)
	}
	ok = ok && len(wantCols) == len(got.Keys) && startsWithFold(got.KeyNames(), wantCols)
	wantKind := nonclusteredWord
	if strings.Contains(preview.Statement, " PRIMARY KEY CLUSTERED") {
		wantKind = clusteredWord
	}
	return text, ok && kind == wantKind
}

// previewColumns reads the columns back from pkTarget: "PK_X on dbo.X (A, B), clustered".
func previewColumns(target string) []string {
	i, j := strings.Index(target, " ("), strings.LastIndex(target, "), ")
	if i < 0 || j < i {
		return nil
	}
	return strings.Split(target[i+2:j], ", ")
}

// PrimaryKeyChangeDescription is the change log line: "Added primary key {target} in {db}, for DES01".
func PrimaryKeyChangeDescription(fix Fix, dbName string) string {
	return "Added primary key " + fix.Target + " in " + dbName + ", for DES01"
}

// PrimaryKeyOptions are the add-primary-key options of a DES01 finding: its table.
func PrimaryKeyOptions(t NoKeyTableFact) map[string]string {
	return map[string]string{OptionTable: t.Object(), OptionObject: t.Object()}
}
