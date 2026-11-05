package domain

import (
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"unicode"
)

// The create-index and drop-index fixes (docs/phase4c-contract.md, part 3; SCOPE section 3 item
// 10): WRK03's "never a fix" becomes a fix behind the triage's index guards (PerformanceTriage.sql
// IDX02, IDX04, IDX08, IDX09) and a preview. Every guard is a warning; the ones that mean the index
// should not be created (same leading keys, a near-copy of the table, a column that is not there)
// block Apply by leaving the preview without changes.
const (
	FixCreateIndex = "create-index"
	FixDropIndex   = "drop-index"
)

// The options of the index fixes. Table, equality, inequality and included are a suggestion's
// columns as WRK03 (or a plan's missing-index hint) holds them; object is the finding's object, so
// the card page can match a finding to its fix; source and planId say where a suggestion came from
// (part 1 sends "plan").
const (
	OptionTable      = "table"
	OptionEquality   = "equality"
	OptionInequality = "inequality"
	OptionIncluded   = "included"
	OptionIndexName  = "name"
	OptionOnline     = "online"
	OptionObject     = "object"
	OptionIndex      = "index" // drop-index: the index to drop
	// OptionQueryHash is the query a fix belongs to: evict-plan's, and the QRY01 finding an archive
	// fix belongs to (the card page matches it to the hash in the finding's evidence).
	OptionQueryHash = "queryHash"
)

// TableColumn is one column of a table as sys.columns describes it.
type TableColumn struct {
	Name      string
	Type      string // the type name: int, nvarchar, datetime2
	MaxLength int    // bytes; -1 for (max)
}

// IndexKey is one key column of an index.
type IndexKey struct {
	Name       string
	Descending bool
}

// ExistingIndex is one index of a table as sys.indexes and sys.index_columns describe it, with its
// size and its use since the restart.
type ExistingIndex struct {
	Name               string
	IndexID            int
	Type               int // 0 heap, 1 clustered, 2 nonclustered, others (XML, spatial, columnstore)
	IsUnique           bool
	IsPrimaryKey       bool
	IsUniqueConstraint bool
	IsDisabled         bool
	IsHypothetical     bool
	Filter             string // filter_definition; "" when none
	Keys               []IndexKey
	Included           []string
	SizeMB             float64
	Reads              int64 // user seeks, scans and lookups since the restart
	Writes             int64 // user updates since the restart
}

// KeyNames are the index's key column names in order.
func (x ExistingIndex) KeyNames() []string {
	out := make([]string, len(x.Keys))
	for i, k := range x.Keys {
		out[i] = k.Name
	}
	return out
}

// IndexFacts is what the index fixes read about one table (collect.IndexFacts).
type IndexFacts struct {
	ProductVersion string
	EngineEdition  int // 3 Enterprise or Developer: online index operations
	Database       string
	Schema         string
	Table          string
	Found          bool // the table exists
	Rows           int64
	DataMB         float64 // the heap or clustered index
	Columns        []TableColumn
	ColumnsError   string
	Indexes        []ExistingIndex // the heap or clustered index included
	IndexesError   string
	UsageError     string // the use since the restart could not be read (sys.dm_db_index_usage_stats)
}

// TableName is "schema.table".
func (f IndexFacts) TableName() string { return f.Schema + "." + f.Table }

// qualified is "[db].[schema].[table]".
func (f IndexFacts) qualified() string {
	return QuoteName(f.Database) + "." + QuoteName(f.Schema) + "." + QuoteName(f.Table)
}

// Index finds an index of the table by name, case-insensitively.
func (f IndexFacts) Index(name string) (ExistingIndex, bool) {
	for _, x := range f.Indexes {
		if strings.EqualFold(x.Name, name) {
			return x, true
		}
	}
	return ExistingIndex{}, false
}

// base is the heap or the clustered index.
func (f IndexFacts) base() (ExistingIndex, bool) {
	for _, x := range f.Indexes {
		if x.IndexID <= 1 {
			return x, true
		}
	}
	return ExistingIndex{}, false
}

func (f IndexFacts) column(name string) (TableColumn, bool) {
	for _, c := range f.Columns {
		if strings.EqualFold(c.Name, name) {
			return c, true
		}
	}
	return TableColumn{}, false
}

// OnlineIndexEdition says whether the edition builds indexes online (Enterprise and Developer).
func OnlineIndexEdition(engineEdition int) bool { return engineEdition == 3 }

// pageCompression says whether the server can create a PAGE-compressed index: every edition from
// SQL Server 2016 SP1 (13.0.4001), Enterprise and Developer before that.
func pageCompression(engineEdition int, version string) bool {
	if engineEdition == 3 || engineEdition == 8 {
		return true
	}
	parts := strings.Split(strings.TrimSpace(version), ".")
	major := EngineMajor(version)
	if major > 13 {
		return true
	}
	if major == 13 && len(parts) >= 3 {
		build := 0
		fmt.Sscan(parts[2], &build)
		return build >= 4001
	}
	return false
}

// SplitTableName reads a table name as an option carries it: "[db].[schema].[table]" (WRK03's
// statement column), "[schema].[table]", "schema.table" or "table" (schema dbo), brackets with ]]
// inside allowed. It returns the database ("" when not named), the schema and the table.
func SplitTableName(s string) (db, schema, table string, err error) {
	parts, err := splitNameParts(s, '.')
	if err != nil {
		return "", "", "", err
	}
	switch len(parts) {
	case 1:
		return "", "dbo", parts[0], nil
	case 2:
		return "", parts[0], parts[1], nil
	case 3:
		return parts[0], parts[1], parts[2], nil
	}
	return "", "", "", fmt.Errorf("%q is not a table name (schema.table)", s)
}

// SplitColumnList reads a list of columns as the missing index DMVs write it, "[A], [B]", or as
// a person types it, "A, B". Empty for "" or "-".
func SplitColumnList(s string) ([]string, error) {
	s = strings.TrimSpace(s)
	if s == "" || s == "-" {
		return []string{}, nil
	}
	return splitNameParts(s, ',')
}

// splitNameParts splits on sep outside brackets, strips the brackets (and ]] to ]) and the spaces
// around each part. An empty part or an unclosed bracket is an error.
func splitNameParts(s string, sep rune) ([]string, error) {
	out := []string{}
	var cur strings.Builder
	quoted, inBracket := false, false
	rs := []rune(s)
	flush := func() error {
		p := cur.String()
		if !quoted {
			p = strings.TrimSpace(p)
		}
		if p == "" {
			return fmt.Errorf("%q has an empty name in it", s)
		}
		out = append(out, p)
		cur.Reset()
		quoted = false
		return nil
	}
	for i := 0; i < len(rs); i++ {
		r := rs[i]
		switch {
		case inBracket:
			if r == ']' {
				if i+1 < len(rs) && rs[i+1] == ']' {
					cur.WriteRune(']')
					i++
					continue
				}
				inBracket = false
				continue
			}
			cur.WriteRune(r)
		case r == '[':
			if strings.TrimSpace(cur.String()) != "" {
				return nil, fmt.Errorf("%q has a bracket inside a name", s)
			}
			cur.Reset()
			inBracket, quoted = true, true
		case r == sep:
			if err := flush(); err != nil {
				return nil, err
			}
		case quoted:
			if !unicode.IsSpace(r) {
				return nil, fmt.Errorf("%q has text after a closing bracket", s)
			}
		default:
			cur.WriteRune(r)
		}
	}
	if inBracket {
		return nil, fmt.Errorf("%q has an unclosed bracket", s)
	}
	if err := flush(); err != nil {
		return nil, err
	}
	return out, nil
}

// CreateIndexSpec is what the create-index fix will create, read from its options.
type CreateIndexSpec struct {
	Database   string // as named in the table option; "" when not named
	Schema     string
	Table      string
	Equality   []string
	Inequality []string
	Included   []string
	Name       string // "" for the default name
	Online     *bool  // nil for the edition's default
}

// Keys are the equality columns then the inequality columns, as SQL Server suggests ordering them.
func (s CreateIndexSpec) Keys() []string {
	return append(append([]string{}, s.Equality...), s.Inequality...)
}

// Errors of ParseCreateIndexOptions.
var (
	ErrNoIndexTable = errors.New("the suggestion names no table")
	ErrNoIndexKeys  = errors.New("the suggestion has no key columns (equality or inequality)")
)

// ParseCreateIndexOptions reads the create-index fix's options.
func ParseCreateIndexOptions(opts map[string]string) (CreateIndexSpec, error) {
	var s CreateIndexSpec
	if strings.TrimSpace(opts[OptionTable]) == "" {
		return s, ErrNoIndexTable
	}
	var err error
	if s.Database, s.Schema, s.Table, err = SplitTableName(opts[OptionTable]); err != nil {
		return s, err
	}
	if s.Equality, err = SplitColumnList(opts[OptionEquality]); err != nil {
		return s, err
	}
	if s.Inequality, err = SplitColumnList(opts[OptionInequality]); err != nil {
		return s, err
	}
	if s.Included, err = SplitColumnList(opts[OptionIncluded]); err != nil {
		return s, err
	}
	if len(s.Keys()) == 0 {
		return s, ErrNoIndexKeys
	}
	s.Name = strings.TrimSpace(opts[OptionIndexName])
	if len([]rune(s.Name)) > 128 {
		return s, fmt.Errorf("an index name is at most 128 characters")
	}
	switch strings.ToLower(strings.TrimSpace(opts[OptionOnline])) {
	case "true", "1", "on", "yes":
		v := true
		s.Online = &v
	case "false", "0", "off", "no":
		v := false
		s.Online = &v
	}
	return s, nil
}

// DefaultIndexName is IX_{table}_{first key columns}_kakapo: up to three key columns, letters,
// digits and underscores only, at most 128 characters.
func DefaultIndexName(table string, keys []string) string {
	clean := func(s string) string {
		var b strings.Builder
		for _, r := range s {
			if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' {
				b.WriteRune(r)
			}
		}
		return b.String()
	}
	parts := []string{"IX", clean(table)}
	for i, k := range keys {
		if i == 3 {
			break
		}
		parts = append(parts, clean(k))
	}
	name := strings.Join(parts, "_")
	const suffix = "_kakapo"
	if r := []rune(name); len(r)+len(suffix) > 128 {
		name = string(r[:128-len(suffix)])
	}
	return name + suffix
}

// IndexGuard is one of the create-index fix's checks that found something: a warning, and whether
// it blocks Apply.
type IndexGuard struct {
	Check  string // IDX02, IDX04, IDX08, IDX09, SIZE, WRITES, ORDER, COLUMN
	Text   string
	Blocks bool
}

// indexPlan is a create-index spec resolved against the facts: the column names as the table
// spells them, the included columns without the keys, the name and online decided.
type indexPlan struct {
	keys     []string
	included []string
	name     string
	online   bool
	sizeMB   float64
}

func canonical(f IndexFacts, cols []string) []string {
	out := make([]string, 0, len(cols))
	for _, c := range cols {
		if tc, ok := f.column(c); ok {
			c = tc.Name
		}
		out = append(out, c)
	}
	return out
}

func containsFold(list []string, s string) bool {
	for _, x := range list {
		if strings.EqualFold(x, s) {
			return true
		}
	}
	return false
}

func resolveIndex(f IndexFacts, s CreateIndexSpec) indexPlan {
	p := indexPlan{keys: []string{}, included: []string{}}
	for _, k := range canonical(f, s.Keys()) {
		if !containsFold(p.keys, k) {
			p.keys = append(p.keys, k)
		}
	}
	for _, c := range canonical(f, s.Included) {
		if !containsFold(p.keys, c) && !containsFold(p.included, c) {
			p.included = append(p.included, c)
		}
	}
	p.name = s.Name
	if p.name == "" {
		p.name = DefaultIndexName(f.Table, p.keys)
	}
	p.online = OnlineIndexEdition(f.EngineEdition)
	if p.online && s.Online != nil {
		p.online = *s.Online
	}
	p.sizeMB = estimateIndexMB(f, p.keys, p.included)
	return p
}

// columnBytes is the average width of a column in an index row: fixed types their length,
// variable types half their maximum plus two bytes, (max) types 500 bytes.
func columnBytes(c TableColumn) float64 {
	switch strings.ToLower(c.Type) {
	case "varchar", "nvarchar", "varbinary", "sql_variant":
		if c.MaxLength < 0 {
			return 500
		}
		return math.Max(1, float64(c.MaxLength)/2) + 2
	case "text", "ntext", "image", "xml":
		return 500
	}
	if c.MaxLength <= 0 {
		return 8
	}
	return float64(c.MaxLength)
}

// estimateIndexMB is the size of the new index's leaf level: rows times the key and included
// widths from sys.columns, plus the row overhead and the row locator (the clustered keys, or an
// 8-byte RID on a heap), on 8 KB pages. 0 when the columns were not read.
func estimateIndexMB(f IndexFacts, keys, included []string) float64 {
	if len(f.Columns) == 0 || f.Rows <= 0 {
		return 0
	}
	row := 7.0 // header and null bitmap
	all := append(append([]string{}, keys...), included...)
	for _, name := range all {
		if c, ok := f.column(name); ok {
			row += columnBytes(c)
		} else {
			row += 8
		}
	}
	locator := 8.0
	if b, ok := f.base(); ok && b.Type == 1 {
		locator = 0
		for _, k := range b.Keys {
			if containsFold(all, k.Name) {
				continue
			}
			if c, ok := f.column(k.Name); ok {
				locator += columnBytes(c)
			}
		}
	}
	row += locator
	perPage := math.Max(1, math.Floor(8096/(row+2)))
	pages := math.Ceil(float64(f.Rows) / perPage)
	return pages * 8 / 1024
}

// keyBytes is the widest the key can be: the maximum lengths summed.
func keyBytes(f IndexFacts, keys []string) int {
	n := 0
	for _, k := range keys {
		if c, ok := f.column(k); ok && c.MaxLength > 0 {
			n += c.MaxLength
		}
	}
	return n
}

func startsWithFold(list, prefix []string) bool {
	if len(prefix) > len(list) {
		return false
	}
	for i := range prefix {
		if !strings.EqualFold(list[i], prefix[i]) {
			return false
		}
	}
	return true
}

func sameSetFold(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for _, x := range a {
		if !containsFold(b, x) {
			return false
		}
	}
	return true
}

// bracketList is "[A], [B]".
func bracketList(cols []string) string {
	q := make([]string, len(cols))
	for i, c := range cols {
		q[i] = QuoteName(c)
	}
	return strings.Join(q, ", ")
}

// plainList is "A, B".
func plainList(cols []string) string { return strings.Join(cols, ", ") }

// createIndexGuards are PerformanceTriage.sql's index checks run against the new index (docs/phase4c-contract.md,
// part 3, item 2), in that order: a column that is not in the table or cannot be a key (blocks),
// IDX04/IDX08 the same leading keys (blocks), the same keys in another order (does not block),
// IDX02 over-indexed (does not block), IDX09 a near-copy of the table (blocks), the size over
// 1 GB, a table written ten times more than it is read.
func createIndexGuards(f IndexFacts, p indexPlan) []IndexGuard {
	out := []IndexGuard{}
	table := f.TableName()

	if len(f.Columns) > 0 {
		for _, c := range append(append([]string{}, p.keys...), p.included...) {
			if _, ok := f.column(c); !ok {
				out = append(out, IndexGuard{"COLUMN", fmt.Sprintf("Column %s is not in %s (any more): the suggestion is out of date.", c, table), true})
			}
		}
		for _, k := range p.keys {
			c, ok := f.column(k)
			if !ok {
				continue
			}
			switch t := strings.ToLower(c.Type); {
			case c.MaxLength < 0, t == "text", t == "ntext", t == "image", t == "xml", t == "geography", t == "geometry":
				out = append(out, IndexGuard{"COLUMN", fmt.Sprintf("Column %s is a %s column and cannot be an index key: include it instead.", k, typeText(c)), true})
			}
		}
		if n := keyBytes(f, p.keys); n > 1700 {
			out = append(out, IndexGuard{"COLUMN", fmt.Sprintf("The key columns can hold up to %s bytes, over SQL Server's 1,700-byte limit: a row with that much in them will fail to insert or update.", n0int(int64(n))), false})
		}
	}

	for _, x := range f.Indexes {
		if x.Type != 1 && x.Type != 2 || len(x.Keys) == 0 {
			continue
		}
		keys := x.KeyNames()
		disabled := ""
		if x.IsDisabled {
			disabled = " (disabled: rebuild it instead)"
		}
		switch {
		case x.Type == 1 && startsWithFold(keys, p.keys):
			out = append(out, IndexGuard{"IDX04", fmt.Sprintf("The clustered index %s already starts with these columns (%s), so SQL Server can already seek on them; another index on the same keys only adds write work.", x.Name, plainList(keys)), true})
		case startsWithFold(keys, p.keys):
			check := "IDX04"
			if len(keys) > len(p.keys) {
				check = "IDX08"
			}
			out = append(out, IndexGuard{check, fmt.Sprintf("Index %s already starts with these columns (%s)%s; widen it instead of adding one.", x.Name, plainList(keys), disabled), true})
		case x.Type == 2 && !x.IsUnique && !x.IsPrimaryKey && startsWithFold(p.keys, keys):
			out = append(out, IndexGuard{"IDX08", fmt.Sprintf("Index %s (%s) is the start of this one%s; widen it instead of adding one, or it becomes an overlapping index nobody needs.", x.Name, plainList(keys), disabled), true})
		case sameSetFold(keys, p.keys):
			out = append(out, IndexGuard{"ORDER", fmt.Sprintf("Index %s has the same key columns in another order (%s): check whether it serves this query before adding another.", x.Name, plainList(keys)), false})
		}
	}

	if f.IndexesError == "" {
		count, indexMB := 0, 0.0
		for _, x := range f.Indexes {
			if x.Type >= 2 && !x.IsHypothetical {
				count++
				indexMB += x.SizeMB
			}
		}
		if count >= 10 || (indexMB >= 100 && indexMB >= 2*f.DataMB) {
			out = append(out, IndexGuard{"IDX02", fmt.Sprintf("%s already has %d indexes using %s MB for %s MB of data: every index slows down inserts, updates and deletes, so make sure this one is worth it.",
				table, count, n0(indexMB), n0(f.DataMB)), false})
		}
	}

	if len(f.Columns) > 0 && len(p.included) >= 5 {
		cols := len(p.keys) + len(p.included)
		if cols*2 >= len(f.Columns) {
			out = append(out, IndexGuard{"IDX09", fmt.Sprintf("This index would hold %d of the table's %d columns: a second copy of the table. Include only the columns the query needs (edit the included columns above).",
				cols, len(f.Columns)), true})
		}
	}

	if p.sizeMB > 1024 {
		out = append(out, IndexGuard{"SIZE", fmt.Sprintf("The index will be about %s: building it takes time, reads the whole table and writes about that much to the transaction log.", mbText(int64(math.Round(p.sizeMB)))), false})
	}

	if b, ok := f.base(); ok && f.UsageError == "" && b.Writes >= 1000 && b.Writes > 10*b.Reads {
		out = append(out, IndexGuard{"WRITES", fmt.Sprintf("%s is written far more than it is read (%s writes, %s reads since the restart): every index adds work to each insert, update and delete.",
			table, n0int(b.Writes), n0int(b.Reads)), false})
	}
	return out
}

func typeText(c TableColumn) string {
	if c.MaxLength < 0 {
		return c.Type + "(max)"
	}
	return c.Type
}

// CreateIndexStatement is the statement the fix runs: CREATE NONCLUSTERED INDEX on the table's
// three-part name, with ONLINE, SORT_IN_TEMPDB and, when the server supports it, PAGE compression.
func createIndexStatement(f IndexFacts, p indexPlan) string {
	var b strings.Builder
	b.WriteString("CREATE NONCLUSTERED INDEX " + QuoteName(p.name) + " ON " + f.qualified() + " (" + bracketList(p.keys) + ")")
	if len(p.included) > 0 {
		b.WriteString(" INCLUDE (" + bracketList(p.included) + ")")
	}
	with := []string{"ONLINE = " + onOff(p.online), "SORT_IN_TEMPDB = ON"}
	if pageCompression(f.EngineEdition, f.ProductVersion) {
		with = append(with, "DATA_COMPRESSION = PAGE")
	}
	b.WriteString(" WITH (" + strings.Join(with, ", ") + ");")
	return b.String()
}

func dropIndexStatement(f IndexFacts, name string) string {
	return "DROP INDEX " + QuoteName(name) + " ON " + f.qualified() + ";"
}

// IndexDescription is "{name} on {table} ({keys}) include ({cols})".
func IndexDescription(name, table string, keys, included []string) string {
	s := name + " on " + table + " (" + plainList(keys) + ")"
	if len(included) > 0 {
		s += " include (" + plainList(included) + ")"
	}
	return s
}

// indexPermission is the create and drop fixes' permission line.
func indexPermission(table string) string {
	return "Needs the ALTER permission on " + table + " (db_ddladmin or db_owner in the database)."
}

// Warnings of the create-index fix besides its guards.
const (
	warnIndexOffline = "Creating an index locks the table while it builds (not online on this edition): users who write to it wait until it finishes, so apply it in a quiet moment."
	warnIndexOnline  = "Built online: users keep working, but it still needs a short lock at the start and at the end."
	warnIndexLarge   = "Test on the sandbox first when the table is large: the build time and the effect on the queries show there."
	warnIndexLock    = "If the table is busy, kakapo gives up after 10 seconds of waiting for its lock rather than queue in front of users; then try again in a quieter moment."
	warnIndexWrites  = "Each index slows down every insert, update and delete on the table a little; drop it again (the change log holds the DROP INDEX) if it does not help."
)

// largeTable says when "test on the sandbox first" is said: a million rows or an index over 1 GB.
func largeTable(f IndexFacts, p indexPlan) bool { return f.Rows >= 1000000 || p.sizeMB > 1024 }

// BuildCreateIndexFix builds the create-index fix's preview from what was read about the table and
// the options (ParseCreateIndexOptions): the statement, its DROP INDEX rollback, the guards as
// warnings and the inputs (name, included columns, online on Enterprise). A blocking guard leaves
// the preview without changes or statement, so nothing can be applied. options are echoed with the
// name, included columns and online as the preview built them.
func BuildCreateIndexFix(f IndexFacts, s CreateIndexSpec, options map[string]string) (FixPreview, Fix) {
	p := resolveIndex(f, s)
	def, _ := FindFixDef(FixCreateIndex)
	table := f.TableName()
	desc := IndexDescription(p.name, table, p.keys, p.included)
	target := desc
	if p.sizeMB > 0 {
		target += ", about " + mbText(int64(math.Max(1, math.Round(p.sizeMB))))
	}
	if p.online {
		target += ", online"
	} else {
		target += ", offline"
	}

	echo := map[string]string{}
	for k, v := range options {
		echo[k] = v
	}
	echo[OptionIndexName] = p.name
	echo[OptionIncluded] = bracketList(p.included)
	if OnlineIndexEdition(f.EngineEdition) {
		echo[OptionOnline] = fmt.Sprint(p.online)
	} else {
		delete(echo, OptionOnline)
	}

	fix := Fix{ID: def.ID, Title: def.Title, Scope: def.Scope, CheckID: def.CheckID, Setting: def.Setting,
		Current: "none", Target: target, Options: echo}
	pv := FixPreview{Database: f.Database, EngineVersion: f.ProductVersion, Changes: []Drift{}, Warnings: []string{}, Options: echo}

	// The guards that block come first, so the first warning says why nothing can be applied.
	guards := createIndexGuards(f, p)
	sort.SliceStable(guards, func(i, j int) bool { return guards[i].Blocks && !guards[j].Blocks })
	blocked := false
	idx09 := ""
	for _, g := range guards {
		pv.Warnings = append(pv.Warnings, g.Text)
		blocked = blocked || g.Blocks
		if g.Check == "IDX09" {
			idx09 = g.Text
		}
	}
	if p.online {
		pv.Warnings = append(pv.Warnings, warnIndexOnline)
	} else {
		pv.Warnings = append(pv.Warnings, warnIndexOffline)
	}
	if largeTable(f, p) {
		pv.Warnings = append(pv.Warnings, warnIndexLarge)
	}
	pv.Warnings = append(pv.Warnings, warnIndexLock, warnIndexWrites, indexPermission(table))

	pv.Inputs = []FixInput{
		{Name: OptionIndexName, Label: "Index name", Value: p.name, Kind: "text",
			Help: "kakapo's indexes end in _kakapo, so they are easy to find later."},
		{Name: OptionIncluded, Label: "Included columns", Value: bracketList(p.included), Kind: "text", Warning: idx09,
			Help: "As SQL Server suggested them. Leave out the columns the query does not need; empty for none."},
	}
	if OnlineIndexEdition(f.EngineEdition) {
		pv.Inputs = append(pv.Inputs, FixInput{Name: OptionOnline, Label: "Build it online", Value: fmt.Sprint(p.online), Kind: "bool",
			Help: "Users keep working while it builds. Offline is faster but locks the table."})
	}

	if blocked {
		return pv, fix
	}
	pv.Changes = append(pv.Changes, Drift{Setting: "Index", Current: "none", Target: target})
	pv.Statement = createIndexStatement(f, p)
	pv.Rollback = dropIndexStatement(f, p.name)
	return pv, fix
}

// CreateIndexReadBack says whether the index read back after the create is the one the preview
// built: its key columns in order and its included columns. after is what the change row and the
// result say.
func CreateIndexReadBack(f IndexFacts, s CreateIndexSpec, got *ExistingIndex) (after string, verified bool) {
	p := resolveIndex(f, s)
	if got == nil {
		return fmt.Sprintf("%s was not found on %s after the create", p.name, f.TableName()), false
	}
	after = IndexDescription(got.Name, f.TableName(), got.KeyNames(), got.Included) + fmt.Sprintf(", %s MB", n1(got.SizeMB))
	keys := got.KeyNames()
	verified = len(keys) == len(p.keys) && startsWithFold(keys, p.keys) && sameSetFold(got.Included, p.included) && !got.IsDisabled
	return after, verified
}

// CreateIndexChangeDescription is the change log line of a created index: "Created index {the
// fix's target} in {database}".
func CreateIndexChangeDescription(fix Fix, dbName string) string {
	return "Created index " + fix.Target + " in " + dbName
}

// IndexObject is the change row's object of an index fix: "schema.table.index".
func IndexObject(schema, table, index string) string {
	return schema + "." + table + "." + index
}

// DropIndexChangeDescription is the change log line of a dropped index.
func DropIndexChangeDescription(fix Fix, dbName string) string {
	return "Dropped index " + fix.Current + " in " + dbName
}

// RecreateIndexStatement rebuilds a nonclustered index's CREATE from sys.index_columns: unique,
// its keys in order with their direction, its included columns and its filter. Fill factor,
// compression and partitioning are not kept.
func RecreateIndexStatement(f IndexFacts, x ExistingIndex) string {
	var b strings.Builder
	b.WriteString("CREATE ")
	if x.IsUnique {
		b.WriteString("UNIQUE ")
	}
	b.WriteString("NONCLUSTERED INDEX " + QuoteName(x.Name) + " ON " + f.qualified() + " (")
	for i, k := range x.Keys {
		if i > 0 {
			b.WriteString(", ")
		}
		b.WriteString(QuoteName(k.Name))
		if k.Descending {
			b.WriteString(" DESC")
		}
	}
	b.WriteString(")")
	if len(x.Included) > 0 {
		b.WriteString(" INCLUDE (" + bracketList(x.Included) + ")")
	}
	if x.Filter != "" {
		b.WriteString(" WHERE " + x.Filter)
	}
	b.WriteString(";")
	return b.String()
}

// ErrNoSuchIndex is BuildDropIndexFix's answer for an index the table does not have.
var ErrNoSuchIndex = errors.New("no such index")

// BuildDropIndexFix builds the drop-index fix's preview: DROP INDEX with the CREATE rebuilt from
// sys.index_columns as its rollback. A primary key, a unique constraint, the clustered index and
// the index types the rollback cannot rebuild (XML, spatial, columnstore) are refused: the preview
// says why and has no changes.
func BuildDropIndexFix(f IndexFacts, name string, options map[string]string) (FixPreview, Fix, error) {
	x, ok := f.Index(name)
	if !ok || x.IndexID == 0 {
		return FixPreview{}, Fix{}, ErrNoSuchIndex
	}
	def, _ := FindFixDef(FixDropIndex)
	table := f.TableName()
	current := IndexDescription(x.Name, table, x.KeyNames(), x.Included) + fmt.Sprintf(", %s MB", n1(x.SizeMB))
	echo := map[string]string{}
	for k, v := range options {
		echo[k] = v
	}
	echo[OptionIndex] = x.Name
	echo[OptionTable] = QuoteName(f.Schema) + "." + QuoteName(f.Table)
	fix := Fix{ID: def.ID, Title: def.Title, Scope: def.Scope, CheckID: def.CheckID, Setting: def.Setting,
		Current: current, Target: "dropped", Options: echo}
	pv := FixPreview{Database: f.Database, EngineVersion: f.ProductVersion, Changes: []Drift{}, Warnings: []string{}, Options: echo}

	refuse := ""
	switch {
	case x.IsPrimaryKey:
		refuse = fmt.Sprintf("%s is the primary key of %s: kakapo does not drop it (ALTER TABLE ... DROP CONSTRAINT, by someone who knows what depends on it).", x.Name, table)
	case x.IsUniqueConstraint:
		refuse = fmt.Sprintf("%s is a unique constraint on %s: kakapo does not drop it (ALTER TABLE ... DROP CONSTRAINT, by someone who knows what depends on it).", x.Name, table)
	case x.Type == 1:
		refuse = fmt.Sprintf("%s is the clustered index of %s: dropping it rewrites the whole table, so kakapo does not.", x.Name, table)
	case x.Type != 2:
		refuse = fmt.Sprintf("%s is not a plain nonclustered index (XML, spatial or columnstore): kakapo cannot rebuild it as a rollback, so it does not drop it.", x.Name)
	}
	if refuse != "" {
		pv.Warnings = append(pv.Warnings, refuse)
		return pv, fix, nil
	}
	if f.UsageError == "" {
		pv.Warnings = append(pv.Warnings, fmt.Sprintf("Queries that used %s may get slower: it was read %s times and written %s times since the restart.", x.Name, n0int(x.Reads), n0int(x.Writes)))
	}
	if x.IsDisabled {
		pv.Warnings = append(pv.Warnings, x.Name+" is disabled: dropping it frees nothing more, but removes its definition.")
	}
	pv.Warnings = append(pv.Warnings,
		"The rollback rebuilds "+x.Name+" with its columns, uniqueness and filter; fill factor, compression and partitioning are not kept.",
		"Dropping an index needs a short lock on the table.",
		indexPermission(table))
	pv.Changes = append(pv.Changes, Drift{Setting: "Index", Current: current, Target: "dropped"})
	pv.Statement = dropIndexStatement(f, x.Name)
	pv.Rollback = RecreateIndexStatement(f, x)
	return pv, fix, nil
}

// DropIndexReadBack says whether the index is gone after the drop.
func DropIndexReadBack(f IndexFacts, name string, stillThere bool) (after string, verified bool) {
	if stillThere {
		return fmt.Sprintf("%s is still on %s", name, f.TableName()), false
	}
	return fmt.Sprintf("%s is gone from %s", name, f.TableName()), true
}
