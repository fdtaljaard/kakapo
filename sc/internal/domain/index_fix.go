package domain

import (
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"
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
	Reads              int64 // user seeks, scans and lookups since the restart; -1 when not read
	Writes             int64 // user updates since the restart; -1 when not read
	// ForeignKeys are the foreign keys that reference this index (sys.foreign_keys.key_index_id):
	// disabling it disables them (docs/phase5o-contract.md, section 3).
	ForeignKeys []string
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
	// ForeignKeysError says the foreign keys that reference the indexes could not be read.
	ForeignKeysError string
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
		// Included columns (Microsoft Learn, "Create indexes with included columns"): text, ntext and
		// image cannot be included; a (max) or xml column can, but the index then keeps its own copy
		// of every value, so it can grow as large as the table.
		for _, inc := range p.included {
			c, ok := f.column(inc)
			if !ok {
				continue
			}
			switch t := strings.ToLower(c.Type); {
			case t == "text", t == "ntext", t == "image":
				out = append(out, IndexGuard{"COLUMN", fmt.Sprintf("Column %s is a %s column: SQL Server cannot include it in an index. Leave it out of the included columns.", inc, typeText(c)), true})
			case c.MaxLength < 0, t == "xml":
				rows := ""
				if f.Rows > 0 {
					rows = " (" + n0int(f.Rows) + " rows)"
				}
				out = append(out, IndexGuard{"COLUMN", fmt.Sprintf("Column %s is a %s column: the index keeps its own copy of it for every row of the table%s, so the index can grow as large as the table and every change to the column is written twice. Leave it out of the included columns unless the query cannot do without it.", inc, typeText(c), rows), false})
			}
		}
		if n := keyBytes(f, p.keys); n > 1700 {
			out = append(out, IndexGuard{"COLUMN", fmt.Sprintf("The key columns can hold up to %s bytes, over SQL Server's 1,700-byte limit: a row with that much in them will fail to insert or update.", n0int(int64(n))), false})
		}
	}

	// The IDX04 sentences name the Design card when the table already has copies there
	// (docs/phase5o-contract.md, section 5).
	designNote := ""
	if TableHasDuplicates(f) {
		designNote = " The Design card's IDX04 lists the copies " + table + " already has."
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
			out = append(out, IndexGuard{"IDX04", fmt.Sprintf("The clustered index %s already starts with these columns (%s), so SQL Server can already seek on them; another index on the same keys only adds write work.%s", x.Name, plainList(keys), designNote), true})
		case startsWithFold(keys, p.keys):
			check, note := "IDX04", designNote
			if len(keys) > len(p.keys) {
				check, note = "IDX08", ""
			}
			out = append(out, IndexGuard{check, fmt.Sprintf("Index %s already starts with these columns (%s)%s; widen it instead of adding one.%s", x.Name, plainList(keys), disabled, note), true})
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
	if t := strings.ToLower(c.Type); c.MaxLength < 0 && (t == "varchar" || t == "nvarchar" || t == "varbinary") {
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
	warnIndexOnline = "Built online: users keep working, but it still needs a short lock at the start and at the end."
	warnIndexLarge  = "Test on the sandbox first when the table is large: the build time and the effect on the queries show there."
	warnIndexLock   = "If the table is busy, kakapo gives up after 10 seconds of waiting for its lock rather than queue in front of users; then try again in a quieter moment."
	warnIndexWrites = "Each index slows down every insert, update and delete on the table a little; drop it again (the change log holds the DROP INDEX) if it does not help."
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

	fix := Fix{ID: def.ID, Title: def.Title, Scope: def.Scope, CheckID: IndexFixCheck(options), Setting: def.Setting,
		Current: "none", Target: target, Options: echo}
	pv := FixPreview{Database: f.Database, EngineVersion: f.ProductVersion, Changes: []Drift{}, Warnings: []string{}, Options: echo}
	if mb := int64(math.Ceil(p.sizeMB)); mb > 0 {
		// The room line (docs/phase5l-contract.md): the index in the data files, its sort in tempdb.
		size := "the estimated size of the index (rows x key and included columns)"
		pv.Needs = append(pv.Needs,
			RoomNeed{Path: RoleData, Part: RoomPart{Label: "the new index", MB: mb, Basis: size + "; the data files may have room inside already"}},
			RoomNeed{Path: RoleTempdb, Part: RoomPart{Label: "the sort (SORT_IN_TEMPDB)", MB: mb, Basis: size + "; in FULL recovery the log takes about as much"}})
	}

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
		pv.Warnings = append(pv.Warnings, offlineIndexWarning(f))
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
	pv.Lead = foreignKeyLead(f, options)
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

// IndexFixCheck is the check an index fix belongs to: the finding's check when the options say it
// was built from a foreign key (IDX14, the Design card and the query page's foreign-key action,
// docs/phase5n-contract.md section 4), else the fix's default (WRK03).
func IndexFixCheck(options map[string]string) string {
	if options[OptionSource] == SourceForeignKey {
		return "IDX14"
	}
	def, _ := FindFixDef(FixCreateIndex)
	return def.CheckID
}

// foreignKeyLead is the preview's first line for a foreign key's index: "Deletes from Orders read
// all 1,376,845 rows of Lines today; with this index they read only the matching rows." "" for any
// other index, or when the referenced table is not in the options.
func foreignKeyLead(f IndexFacts, options map[string]string) string {
	if options[OptionSource] != SourceForeignKey || strings.TrimSpace(options[OptionParent]) == "" {
		return ""
	}
	_, schema, table, err := SplitTableName(options[OptionParent])
	if err != nil {
		return ""
	}
	return fmt.Sprintf("Deletes from %s read all %s of %s today; with this index they read only the matching rows.",
		designName(schema, table), plural(f.Rows, "row", "rows"), designName(f.Schema, f.Table))
}

// CreateIndexChangeDescription is the change log line of a created index: "Created index {the
// fix's target} in {database}, for {check}", the check the fix was built from (WRK03, or IDX14 for a
// foreign key's index), which the change log and the tuning report show.
func CreateIndexChangeDescription(fix Fix, dbName string) string {
	d := "Created index " + fix.Target + " in " + dbName
	if fix.CheckID != "" {
		d += ", for " + fix.CheckID
	}
	return d
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

// offlineIndexWarning says what an offline build (ONLINE = OFF) does to the table, with its size:
// it holds a shared lock on the table while it builds, so reads go on and inserts, updates and
// deletes wait (Microsoft Learn, "How online index operations work" and "Guidelines for online index
// operations"). Online index operations are an Enterprise feature ("Editions and supported features
// of SQL Server"), so Standard, Web and Express always build offline.
func offlineIndexWarning(f IndexFacts) string {
	size := ""
	switch {
	case f.Rows > 0 && f.DataMB > 0:
		size = fmt.Sprintf(" (%s rows, %s)", n0int(f.Rows), mbText(int64(math.Max(1, math.Round(f.DataMB)))))
	case f.Rows > 0:
		size = fmt.Sprintf(" (%s rows)", n0int(f.Rows))
	}
	how := "Built offline (ONLINE = OFF)"
	if !OnlineIndexEdition(f.EngineEdition) {
		how = "On " + editionWords(f.EngineEdition) + " an index is built offline (ONLINE = OFF)"
	}
	return fmt.Sprintf("%s: while it builds, %s%s is locked against inserts, updates and deletes, and users who write to it wait until it finishes (reads go on). Apply it in a quiet moment.",
		how, f.TableName(), size)
}

// editionWords names the edition family of SERVERPROPERTY('EngineEdition').
func editionWords(engineEdition int) string {
	switch engineEdition {
	case 2:
		return "Standard or Web edition"
	case 4:
		return "Express edition"
	}
	return "this edition"
}

// The disable-index fix (docs/phase5o-contract.md, section 3): IDX04's safe copy is disabled, never
// dropped, so the change can be undone (PerformanceTriage.sql 510; sp_IndexCleanup.sql 7051).
// Microsoft Learn, "Disable indexes and constraints": a disabled nonclustered index's data is removed
// and its definition kept; ALTER INDEX ... REBUILD enables it again; disabling an index a foreign key
// references disables the key; a query that names a disabled index in a hint fails with error 315.
const (
	FixDisableIndex = "disable-index"
	// OptionKeeper is the copy that stays: the index that holds every column of the one disabled.
	OptionKeeper = "keeper"
)

// DisableIndexOptions are the disable-index options for a safe copy: its table as [schema].[table],
// the index and the copy that stays. The Design card's fix and the query page's line send the same.
func DisableIndexOptions(schema, table, index, keeper string) map[string]string {
	return map[string]string{OptionTable: QuoteName(schema) + "." + QuoteName(table), OptionIndex: index, OptionKeeper: keeper}
}

// Kinds of an IndexNamePlace that is not a module (a module's kind is its sys.objects type_desc).
const (
	PlacePlanGuide  = "PLAN_GUIDE"
	PlaceQueryStore = "QUERY_STORE"
)

// IndexNamePlace is one place a search for an index's name found it: a procedure, view, function or
// trigger (Kind its type_desc, Schema and Name), a plan guide (Name), or a query Query Store ran
// (QueryID). Text is what was searched; Places checks the name as a word in it.
type IndexNamePlace struct {
	Kind    string
	Schema  string
	Name    string
	QueryID int64
	Text    string
}

// IndexNameSearch is what collect.SearchIndexName found for an index's name in a database's code,
// plan guides and Query Store, each area with its own error (docs/phase5o-contract.md, section 4).
// Error says the search could not run at all.
type IndexNameSearch struct {
	Index           string
	Error           string
	Modules         []IndexNamePlace
	ModulesError    string
	PlanGuides      []IndexNamePlace
	PlanGuidesError string
	QueryStore      []IndexNamePlace
	QueryStoreError string
	QueryStoreOff   bool
}

// IndexNamePattern is the LIKE pattern of an index's name as a word, with ! as the escape character
// (ESCAPE N'!'): the name's !, [, ], _ and % escaped, a character that is not a letter, digit or
// underscore on each side. The search wraps each text in spaces, so a name at its start or end matches.
func IndexNamePattern(name string) string {
	var b strings.Builder
	for _, r := range name {
		switch r {
		case '!', '[', ']', '_', '%':
			b.WriteRune('!')
		}
		b.WriteRune(r)
	}
	return "%[^A-Za-z0-9_]" + b.String() + "[^A-Za-z0-9_]%"
}

// wordRune is a character that continues a name: a letter, a digit or an underscore.
func wordRune(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' }

// NameAsWord says whether text names name as a word, case-insensitively: somewhere the name appears with
// no letter, digit or underscore just before or after it. "IX_B" is in "WITH (INDEX([IX_B]))" and
// "IX_B, IX_C", not in "IX_B2" or "XIX_B".
func NameAsWord(text, name string) bool {
	t, n := []rune(strings.ToLower(text)), []rune(strings.ToLower(name))
	if len(n) == 0 {
		return false
	}
	for i := 0; i+len(n) <= len(t); i++ {
		if string(t[i:i+len(n)]) != string(n) {
			continue
		}
		if (i == 0 || !wordRune(t[i-1])) && (i+len(n) == len(t) || !wordRune(t[i+len(n)])) {
			return true
		}
	}
	return false
}

// Places are where the search found the index's name as a word, at most three: "dbo.GetOrders",
// "plan guide PG_Orders", "query 42 in Query Store".
func (s IndexNameSearch) Places() []string {
	out := []string{}
	for _, list := range [][]IndexNamePlace{s.Modules, s.PlanGuides, s.QueryStore} {
		for _, p := range list {
			if len(out) == 3 {
				return out
			}
			if !NameAsWord(p.Text, s.Index) {
				continue
			}
			switch p.Kind {
			case PlacePlanGuide:
				out = append(out, "plan guide "+p.Name)
			case PlaceQueryStore:
				out = append(out, fmt.Sprintf("query %d in Query Store", p.QueryID))
			default:
				name := p.Name
				if p.Schema != "" {
					name = p.Schema + "." + p.Name
				}
				out = append(out, name)
			}
		}
	}
	return out
}

// Failures are the areas the search could not read, one sentence each.
func (s IndexNameSearch) Failures() []string {
	if s.Error != "" {
		return []string{"Could not search for the name: " + s.Error}
	}
	out := []string{}
	for _, a := range []struct{ what, err string }{{"the database's code", s.ModulesError}, {"plan guides", s.PlanGuidesError}, {"Query Store", s.QueryStoreError}} {
		if a.err != "" {
			out = append(out, "Could not search "+a.what+": "+a.err)
		}
	}
	return out
}

// sizeText is "1.2 GB", "120 MB", "49.9 MB" or "3 MB" for an index's size.
func sizeText(mb float64) string {
	switch {
	case mb >= 1024:
		return n1(mb/1024) + " GB"
	case mb >= 100:
		return n0(mb) + " MB"
	}
	return strings.TrimSuffix(n1(mb), ".0") + " MB"
}

// countText is "4.3 million" from a million, else the number with thousands separators.
func countText(n int64) string {
	if n >= 1_000_000 {
		return n1(float64(n)/1_000_000) + " million"
	}
	return n0int(n)
}

// andList is "A", "A and B", "A, B and C".
func andList(items []string) string {
	switch len(items) {
	case 0:
		return ""
	case 1:
		return items[0]
	}
	return strings.Join(items[:len(items)-1], ", ") + " and " + items[len(items)-1]
}

// keyText is the keys as a sentence shows them: "CustomerId, OrderDate DESC".
func keyText(keys []IndexKey) string {
	out := make([]string, len(keys))
	for i, k := range keys {
		out[i] = k.Name
		if k.Descending {
			out[i] += " DESC"
		}
	}
	return plainList(out)
}

// sameKeys says whether two indexes have the same key columns in the same order and direction and the
// same filter: IDX04's grouping (PerformanceTriage.sql 426-433).
func sameKeys(a, b ExistingIndex) bool {
	if len(a.Keys) != len(b.Keys) || a.Filter != b.Filter {
		return false
	}
	for i := range a.Keys {
		if !strings.EqualFold(a.Keys[i].Name, b.Keys[i].Name) || a.Keys[i].Descending != b.Keys[i].Descending {
			return false
		}
	}
	return true
}

// indexColumns are every column an index holds: its keys and its included columns.
func indexColumns(x ExistingIndex) []string { return append(x.KeyNames(), x.Included...) }

// holdsEvery says whether c holds every column of d: the clustered index holds them all
// (PerformanceTriage.sql 436-447).
func holdsEvery(c, d ExistingIndex) bool {
	if c.Type == 1 {
		return true
	}
	cols := indexColumns(c)
	for _, n := range indexColumns(d) {
		if !containsFold(cols, n) {
			return false
		}
	}
	return true
}

// plainIndex is a nonclustered index that is neither unique nor a primary key: the only kind IDX04
// may disable.
func plainIndex(x ExistingIndex) bool {
	return x.Type == 2 && !x.IsUnique && !x.IsPrimaryKey && !x.IsUniqueConstraint
}

// UsageSince is how a sentence says when the use counters started: "since 27 Oct", or "" when that
// could not be read.
func UsageSince(w *DesignWindow, now time.Time) string {
	if w == nil {
		return ""
	}
	return "since " + designDay(w.Since, now)
}

// copyCost is "1.2 GB and 4.3 million writes since 27 Oct", without the writes when the use was not
// read, without the day when the window was not.
func copyCost(x ExistingIndex, since string) string {
	s := sizeText(x.SizeMB)
	if x.Writes >= 0 {
		s += " and " + countText(x.Writes) + " " + pluralWord(x.Writes, "write", "writes")
		if since != "" {
			s += " " + since
		}
	}
	return s
}

func pluralWord(n int64, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

// Texts of the disable-index fix (docs/phase5o-contract.md, section 3). Sources: Microsoft Learn,
// "Disable indexes and constraints" (a disabled clustered index makes the table unreadable; a disabled
// nonclustered index's data is removed and its definition kept; REBUILD enables it; a foreign key that
// references a disabled index is disabled; a hint naming a disabled index fails with error 315) and
// "ALTER INDEX" (DISABLE and REBUILD, the schema modification lock DISABLE takes); the foreign key not
// coming back on after the rebuild was verified on SQL Server 2022 on 2026-11-08. The guard itself:
// sp_IndexCleanup.sql 6210-6260.
const (
	disableHeldTemplate  = "%s is named in %s: a query that names a disabled index fails (error 315). Change the code first, or ask the vendor."
	disableSearchHeld    = "kakapo does not disable an index it could not search for."
	disableForeignKey    = "Foreign key %s references %s. Disabling it would switch off foreign key %s without an error, and rebuilding the index does not switch the key back on."
	disableReadsTemplate = "Queries that read %s (%s %s) will read %s instead."
	disableLock          = "Disabling needs a short lock on the table."
	disableUndo          = "Undoing it rebuilds the index from the table: it takes as long as creating it, and the space comes back."
	disableSearched      = "kakapo searched the database's code, plan guides and Query Store for the name; code outside SQL Server (an application that names the index in a hint) cannot be searched."
	disableSearchedNoQS  = "kakapo searched the database's code and plan guides for the name (Query Store is off); code outside SQL Server (an application that names the index in a hint) cannot be searched."
	disableSpace         = "The %s freed stays inside the data file: the file does not shrink; new rows reuse the space."
	disableRollbackOn    = "The rollback rebuilds it online (ONLINE = ON): users keep working, but it still needs a short lock at the start and at the end."
)

// disableRefusal says why kakapo does not disable this index, or "" when it may: the index is gone or
// already disabled, a foreign key references it (or that could not be read), it is not a plain
// nonclustered index, or the copy that stays is gone, disabled, or no longer has the same keys and
// every column (the group changed since the card was read).
func disableRefusal(f IndexFacts, name, keeper string) string {
	table := f.TableName()
	x, ok := f.Index(name)
	switch {
	case !ok || x.IndexID == 0:
		return fmt.Sprintf("%s is not on %s any more: the indexes changed since the card was read. Read the card again.", name, table)
	case x.IsDisabled:
		return fmt.Sprintf("%s is already disabled on %s: there is nothing to free.", x.Name, table)
	case len(x.ForeignKeys) > 0:
		keys := andList(x.ForeignKeys)
		if len(x.ForeignKeys) > 1 {
			return fmt.Sprintf("Foreign keys %s reference %s. Disabling it would switch off foreign keys %s without an error, and rebuilding the index does not switch the keys back on.", keys, x.Name, keys)
		}
		return fmt.Sprintf(disableForeignKey, keys, x.Name, keys)
	case f.ForeignKeysError != "":
		return fmt.Sprintf("Could not read which foreign keys reference %s (%s): kakapo does not disable an index a foreign key may use.", x.Name, f.ForeignKeysError)
	case x.Type == 1:
		return fmt.Sprintf("%s is the clustered index of %s: disabling it makes the whole table unreadable until it is rebuilt, so kakapo does not.", x.Name, table)
	case x.Type != 2:
		return fmt.Sprintf("%s is not a plain nonclustered index (XML, spatial or columnstore): kakapo does not disable it.", x.Name)
	case x.IsPrimaryKey:
		return fmt.Sprintf("%s is the primary key of %s: kakapo does not disable it.", x.Name, table)
	case x.IsUniqueConstraint:
		return fmt.Sprintf("%s is a unique constraint on %s: kakapo does not disable it.", x.Name, table)
	case x.IsUnique:
		return fmt.Sprintf("%s is a unique index: it keeps two rows from having the same keys, so it is not a copy kakapo disables.", x.Name)
	}
	k, ok := f.Index(keeper)
	switch {
	case strings.TrimSpace(keeper) == "" || strings.EqualFold(keeper, x.Name):
		return fmt.Sprintf("No other copy of %s was named to keep: kakapo disables a copy only while another holds every one of its columns.", x.Name)
	case !ok || k.IndexID == 0 || k.IsHypothetical || (k.Type != 1 && k.Type != 2):
		return fmt.Sprintf("%s, the copy that would stay, is not on %s any more: the indexes changed since the card was read. Read the card again.", keeper, table)
	case k.IsDisabled:
		return fmt.Sprintf("%s, the copy that would stay, is disabled: %s is the only working copy, so kakapo does not disable it.", k.Name, x.Name)
	case !sameKeys(k, x) || !holdsEvery(k, x):
		return fmt.Sprintf("%s no longer has the same keys and every column of %s: the indexes changed since the card was read. Read the card again.", k.Name, x.Name)
	}
	return ""
}

// disableStatement and disableRollback are the fix's ALTER INDEX statements on the table's three-part
// name; the rebuild is online on the editions that have it, as create-index builds.
func disableStatement(f IndexFacts, name string) string {
	return "ALTER INDEX " + QuoteName(name) + " ON " + f.qualified() + " DISABLE;"
}

func disableRollback(f IndexFacts, name string) string {
	s := "ALTER INDEX " + QuoteName(name) + " ON " + f.qualified() + " REBUILD"
	if OnlineIndexEdition(f.EngineEdition) {
		s += " WITH (ONLINE = ON)"
	}
	return s + ";"
}

// disableCurrent is the index as the fix describes it: "IX_B on dbo.Orders (CustomerId, OrderDate), 1.2 GB".
func disableCurrent(f IndexFacts, x ExistingIndex) string {
	return IndexDescription(x.Name, f.TableName(), x.KeyNames(), x.Included) + ", " + sizeText(x.SizeMB)
}

// BuildDisableIndexFix builds the disable-index preview from the table read again (collect.IndexFacts),
// the search for the index's name (nil when it was not run) and the use counters' window (nil when not
// read), with the options table, index and keeper. A refusal leaves the preview without changes, with
// its sentence as the first warning. The name found in code, or an area the search could not read,
// holds Apply back (Held); Copy script still works.
func BuildDisableIndexFix(f IndexFacts, search *IndexNameSearch, w *DesignWindow, now time.Time, options map[string]string) (FixPreview, Fix) {
	def, _ := FindFixDef(FixDisableIndex)
	name, keeper := strings.TrimSpace(options[OptionIndex]), strings.TrimSpace(options[OptionKeeper])
	echo := map[string]string{}
	for k, v := range options {
		echo[k] = v
	}
	echo[OptionTable] = QuoteName(f.Schema) + "." + QuoteName(f.Table)
	fix := Fix{ID: def.ID, Title: def.Title, Scope: def.Scope, CheckID: def.CheckID, Setting: def.Setting, Current: name, Target: "disabled", Options: echo}
	pv := FixPreview{Database: f.Database, EngineVersion: f.ProductVersion, Changes: []Drift{}, Warnings: []string{}, Options: echo}

	if refuse := disableRefusal(f, name, keeper); refuse != "" {
		pv.Warnings = append(pv.Warnings, refuse)
		return pv, fix
	}
	x, _ := f.Index(name)
	k, _ := f.Index(keeper)
	echo[OptionIndex], echo[OptionKeeper] = x.Name, k.Name
	fix.Current = disableCurrent(f, x)
	since := UsageSince(w, now)

	pv.Lead = fmt.Sprintf("%s is a copy of %s: %s, for nothing.", x.Name, k.Name, copyCost(x, since))
	if f.UsageError == "" && x.Reads >= 0 {
		when := since
		if when == "" {
			when = "since SQL Server started counting"
		}
		pv.Warnings = append(pv.Warnings, fmt.Sprintf(disableReadsTemplate, x.Name, plural(x.Reads, "time", "times"), when, k.Name))
	} else {
		pv.Warnings = append(pv.Warnings, fmt.Sprintf("Queries that read %s will read %s instead.", x.Name, k.Name))
	}
	pv.Warnings = append(pv.Warnings, disableLock, disableUndo)
	if OnlineIndexEdition(f.EngineEdition) {
		pv.Warnings = append(pv.Warnings, disableRollbackOn)
	} else {
		pv.Warnings = append(pv.Warnings, "The rollback: "+offlineIndexWarning(f))
	}
	pv.Warnings = append(pv.Warnings, fmt.Sprintf(disableSpace, sizeText(x.SizeMB)))

	failures := []string{}
	switch {
	case search == nil:
		failures = append(failures, "Could not search for the name: the search did not run")
	default:
		failures = append(failures, search.Failures()...)
		if len(failures) == 0 && search.QueryStoreOff {
			pv.Warnings = append(pv.Warnings, disableSearchedNoQS)
		} else if len(failures) == 0 {
			pv.Warnings = append(pv.Warnings, disableSearched)
		}
	}
	pv.Warnings = append(pv.Warnings, failures...)
	pv.Warnings = append(pv.Warnings, indexPermission(f.TableName()))

	switch {
	case search != nil && len(search.Places()) > 0:
		pv.Held = fmt.Sprintf(disableHeldTemplate, x.Name, andList(search.Places()))
	case len(failures) > 0:
		pv.Held = strings.Join(failures, ". ") + ". " + disableSearchHeld
	}
	pv.Changes = append(pv.Changes, Drift{Setting: "Index", Current: fix.Current, Target: "disabled"})
	pv.Statement = disableStatement(f, x.Name)
	pv.Rollback = disableRollback(f, x.Name)
	return pv, fix
}

// DisableIndexReadBack says whether the index read back after the change is disabled with no pages
// left: "IX_B is disabled; 1.2 GB freed inside the file." sizeMB is its size before.
func DisableIndexReadBack(f IndexFacts, name string, sizeMB float64) (after string, verified bool) {
	x, ok := f.Index(name)
	switch {
	case !ok:
		return fmt.Sprintf("%s was not found on %s after the change", name, f.TableName()), false
	case !x.IsDisabled:
		return fmt.Sprintf("%s is still enabled on %s", x.Name, f.TableName()), false
	case x.SizeMB > 0:
		return fmt.Sprintf("%s is disabled but still uses %s", x.Name, sizeText(x.SizeMB)), false
	}
	return fmt.Sprintf("%s is disabled; %s freed inside the file.", x.Name, sizeText(sizeMB)), true
}

// DisableIndexChangeDescription is the change log line of a disabled index: "Disabled index IX_B on
// dbo.Orders (a copy of IX_A), 1.2 GB, in Shop, for IDX04".
func DisableIndexChangeDescription(f IndexFacts, index, keeper string, sizeMB float64, dbName string) string {
	return fmt.Sprintf("Disabled index %s on %s (a copy of %s), %s, in %s, for IDX04", index, f.TableName(), keeper, sizeText(sizeMB), dbName)
}
