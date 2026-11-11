package domain

import (
	"fmt"
	"sort"
	"strings"
)

// DES01, tables without a primary key (docs/phase5s-contract.md, section 2), on the Design card. kakapo's
// own check: no source on disk has a threshold, so it takes IDX14's bars for a large table (10,000 rows a
// finding, rank 3 from 1,000,000). Why: Microsoft Learn, "Primary and foreign key constraints" (a primary
// key identifies each row and makes SQL Server refuse a second row with the same key); "Publish data and
// database objects" (transactional replication needs one); "Enable and disable change tracking" (so does
// change tracking).

const (
	des01MinRows   = idx14MinRows
	des01Rank3Rows = idx14Rank3Rows
	// kakapoArchiveIndex marks kakapo's own archive tables (phase 5a): never a finding.
	kakapoArchiveIndex = archiveIndexName
)

// UniqueKeyCandidate is an enabled, unfiltered unique index of a table whose key columns are all NOT NULL
// and not computed: a primary key on the same columns cannot fail.
type UniqueKeyCandidate struct {
	Index     string
	Clustered bool
	Columns   []string
	KeyBytes  int // the key columns' max_length summed
}

// NoKeyTableFact is one user table as the primary key read has it (collect.ReadNoKeyTables): the tables
// with no primary key for the Design card, or one table for the add-primary-key fix (then HasPrimaryKey
// may be true).
type NoKeyTableFact struct {
	Schema         string
	Table          string
	HasPrimaryKey  bool
	PrimaryKey     string // its name when HasPrimaryKey
	Rows           int64
	DataMB         float64 // the heap or clustered index
	Heap           bool
	ClusteredIndex string // the clustered index's name; "" on a heap
	Nonclustered   int    // enabled nonclustered rowstore indexes
	NonclusteredMB float64
	Identity       string // the identity column; "" when none
	IdentityType   string
	Unique         []UniqueKeyCandidate
	// sys.tables: 1 a temporal history table, 2 a system-versioned table.
	TemporalType    int
	MemoryOptimized bool
	External        bool
	Graph           bool // a node or edge table
	// sys.tables.ledger_type: 1 a ledger history table, 2 updatable, 3 append-only.
	LedgerType  int
	Replicated  bool // published (transactional, snapshot or merge)
	CDC         bool // tracked by change data capture
	Partitioned bool
	Columnstore bool
}

// Name is "schema.table".
func (t NoKeyTableFact) Name() string { return t.Schema + "." + t.Table }

// Object is "[schema].[table]", a finding's object.
func (t NoKeyTableFact) Object() string { return QuoteName(t.Schema) + "." + QuoteName(t.Table) }

// never says whether a table is never a DES01 finding: it has a key; a history table cannot have one;
// memory-optimized, external and graph tables are not ordinary tables; kakapo's own archive tables.
func (t NoKeyTableFact) never() bool {
	return t.HasPrimaryKey || t.TemporalType == 1 || t.LedgerType == 1 || t.MemoryOptimized || t.External || t.Graph ||
		strings.EqualFold(t.ClusteredIndex, kakapoArchiveIndex)
}

// Candidates are the unique indexes ordered as the fix offers them: the clustered one first, then the
// narrowest key, then by name.
func (t NoKeyTableFact) Candidates() []UniqueKeyCandidate {
	out := append([]UniqueKeyCandidate{}, t.Unique...)
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.Clustered != b.Clustered {
			return a.Clustered
		}
		if a.KeyBytes != b.KeyBytes {
			return a.KeyBytes < b.KeyBytes
		}
		return strings.ToLower(a.Index) < strings.ToLower(b.Index)
	})
	return out
}

// keyInAllButName says whether the clustered index is unique on NOT NULL columns (decision 6).
func (t NoKeyTableFact) keyInAllButName() (UniqueKeyCandidate, bool) {
	for _, c := range t.Unique {
		if c.Clustered {
			return c, true
		}
	}
	return UniqueKeyCandidate{}, false
}

// Refusal is why kakapo does not add a key to the table (docs/phase5s-contract.md, section 4); "" when it
// can. The card names it in the evidence and the finding then has no fix.
func (t NoKeyTableFact) Refusal() string {
	name := designName(t.Schema, t.Table)
	switch {
	case t.Replicated:
		return name + " is published for replication: a new key or column changes what is published, so the replication's owner decides."
	case t.CDC:
		return name + " is tracked by change data capture: a new key or column changes what is captured, so the capture's owner decides."
	case t.LedgerType >= 2:
		return name + " is a ledger table: its shape is fixed by the ledger."
	case t.Partitioned:
		return name + " is partitioned: its key must include the partitioning column, which is a design question for the application's owner."
	case t.Columnstore:
		return name + " has a columnstore index: kakapo does not change its keys."
	}
	return ""
}

// Texts of DES01 (docs/phase5s-contract.md, section 2).
const (
	des01Why = "A primary key makes SQL Server refuse a second row with the same key, and replication, change tracking and many tools need one (Microsoft Learn, 'Primary and foreign key constraints')."
	// DesignNoKeyHeading heads the card page's fold of the smaller tables without a primary key.
	DesignNoKeyHeading = "Smaller tables without a primary key"
	des01Skipped       = "Could not read which tables have no primary key: "
)

// des01Do is the finding's Do this, by the key kakapo would add.
func des01Do(t NoKeyTableFact) string {
	name := designName(t.Schema, t.Table)
	vendor := " Tell the vendor first: their next upgrade must expect it."
	if c, ok := t.keyInAllButName(); ok {
		return fmt.Sprintf("Nothing urgent: %s already has a unique key. A primary key on the same columns (%s) costs a second copy of the key and only matters for tools that need one.",
			name, plainList(c.Columns))
	}
	if cs := t.Candidates(); len(cs) > 0 {
		return fmt.Sprintf("Add a primary key on the columns of its unique index %s (%s)%s.%s", cs[0].Index, plainList(cs[0].Columns), heapClause(t), vendor)
	}
	if t.Identity != "" {
		return fmt.Sprintf("Add a primary key on its identity column %s%s.%s", t.Identity, heapClause(t), vendor)
	}
	return fmt.Sprintf("Add a primary key on a new bigint identity column%s. This writes every row and adds a column, so check with the vendor first that the application names its columns when it inserts into %s.",
		heapClause(t), name)
}

func heapClause(t NoKeyTableFact) string {
	if t.Heap {
		return ", clustered, which also ends the heap"
	}
	return ""
}

// des01Evidence names the table, its size, heap or clustered, the candidates and the refusal.
func des01Evidence(t NoKeyTableFact) string {
	parts := []string{fmt.Sprintf("Table %s: %s, %s", t.Object(), plural(t.Rows, "row", "rows"), sizeText(t.DataMB))}
	if t.Heap {
		parts[0] += ", heap"
	} else {
		parts[0] += ", clustered index " + QuoteName(t.ClusteredIndex)
	}
	if cs := t.Candidates(); len(cs) > 0 {
		names := make([]string, len(cs))
		for i, c := range cs {
			names[i] = QuoteName(c.Index) + " (" + plainList(c.Columns) + ")"
		}
		parts = append(parts, "unique on NOT NULL columns: "+strings.Join(names, ", "))
	} else {
		parts = append(parts, "no unique index on NOT NULL columns")
	}
	if t.Identity != "" {
		parts = append(parts, "identity column "+QuoteName(t.Identity))
	} else {
		parts = append(parts, "no identity column")
	}
	if t.Nonclustered > 0 {
		s := plural(int64(t.Nonclustered), "nonclustered index", "nonclustered indexes")
		if t.Heap {
			s += " (rebuilt with a clustered key)"
		}
		parts = append(parts, s)
	}
	out := strings.Join(parts, "; ") + "."
	if r := t.Refusal(); r != "" {
		out += " kakapo does not add the key: " + r
	}
	return out
}

// des01Findings raises DES01 per table of 10,000 rows or more, largest first.
func des01Findings(tables []NoKeyTableFact) []Finding {
	ts := []NoKeyTableFact{}
	for _, t := range tables {
		if !t.never() && t.Rows >= des01MinRows {
			ts = append(ts, t)
		}
	}
	sort.SliceStable(ts, func(i, j int) bool { return ts[i].Rows > ts[j].Rows })
	chk, _ := CheckByID("DES01")
	out := []Finding{}
	for _, t := range ts {
		name := designName(t.Schema, t.Table)
		rank := 4
		if t.Rows >= des01Rank3Rows {
			rank = 3
		}
		text := fmt.Sprintf("%s has no primary key: nothing stops two identical rows among its %s, and no key names a row.", name, plural(t.Rows, "row", "rows"))
		if t.Heap {
			text += " It is a heap too (no clustered index)."
		}
		if c, ok := t.keyInAllButName(); ok {
			rank = 5
			text = fmt.Sprintf("%s has no primary key, but its clustered index %s is unique on %s: it has a key in all but name.", name, c.Index, plainList(c.Columns))
		}
		x := Finding{
			CheckID: "DES01", Title: chk.Title, Severity: SeverityFromRank(rank), Owner: chk.Owner,
			When: WhenWindow, Effort: EffortModerate, Object: t.Object(),
			Finding: text, Why: des01Why, DoThis: des01Do(t), ThenCheck: chk.ThenCheck, Evidence: des01Evidence(t),
		}
		if t.Refusal() == "" {
			x.FixID = FixAddPrimaryKey
		}
		out = append(out, x)
	}
	return out
}

// des01Verdict is DES01's clause of the verdict: the count and the biggest table, the Info findings
// left out.
func des01Verdict(tables []NoKeyTableFact, findings []Finding) string {
	n := 0
	for _, x := range findings {
		if x.Severity.Actionable() {
			n++
		}
	}
	if n == 0 {
		return ""
	}
	var big NoKeyTableFact
	for _, t := range tables {
		if _, all := t.keyInAllButName(); !t.never() && !all && t.Rows >= des01MinRows && t.Rows > big.Rows {
			big = t
		}
	}
	if n == 1 {
		return fmt.Sprintf("1 table has no primary key: %s (%s).", designName(big.Schema, big.Table), designRows(big.Rows))
	}
	return fmt.Sprintf("%d tables have no primary key, the biggest %s (%s).", n, designName(big.Schema, big.Table), designRows(big.Rows))
}

// des01Fact is the card's line: "8 tables (2 large)", "none".
func des01Fact(f DesignFacts) Fact {
	fact := Fact{Label: "No primary key", Value: "none", Tone: ToneNormal}
	if f.NoKeyError != "" {
		fact.Value = "could not be read"
		return fact
	}
	all, large := 0, 0
	for _, t := range f.NoKeyTables {
		if t.never() || t.Rows == 0 {
			continue
		}
		all++
		if t.Rows >= des01MinRows {
			large++
		}
	}
	switch {
	case all == 0:
	case large == 0:
		fact.Value = plural(int64(all), "table", "tables") + ", all small"
	default:
		fact.Value = fmt.Sprintf("%s (%d large)", plural(int64(all), "table", "tables"), large)
		fact.Tone = ToneBad
	}
	return fact
}

// DesignNoKeyTables is the card page's fold of the smaller tables without a primary key
// (docs/phase5s-contract.md, decision 2): tables with rows but under 10,000, largest first, one line each,
// no button.
type DesignNoKeyTables struct {
	Heading string             `json:"heading"`
	Tables  []DesignNoKeyTable `json:"tables"`
}

// DesignNoKeyTable is one line of the fold: "Notes (2,410 rows, heap)".
type DesignNoKeyTable struct {
	Table string `json:"table"`
	Rows  int64  `json:"rows"`
	Text  string `json:"text"`
}

// BuildDesignNoKeyTables builds the fold; nil when the tables could not be read.
func BuildDesignNoKeyTables(f DesignFacts) *DesignNoKeyTables {
	if f.Error != "" || f.NoKeyError != "" || (f.State != "" && f.State != "ONLINE") {
		return nil
	}
	ts := []NoKeyTableFact{}
	for _, t := range f.NoKeyTables {
		if !t.never() && t.Rows > 0 && t.Rows < des01MinRows {
			ts = append(ts, t)
		}
	}
	sort.SliceStable(ts, func(i, j int) bool { return ts[i].Rows > ts[j].Rows })
	out := &DesignNoKeyTables{Heading: DesignNoKeyHeading, Tables: []DesignNoKeyTable{}}
	for _, t := range ts {
		name := designName(t.Schema, t.Table)
		text := fmt.Sprintf("%s (%s", name, plural(t.Rows, "row", "rows"))
		if t.Heap {
			text += ", heap"
		}
		out.Tables = append(out.Tables, DesignNoKeyTable{Table: name, Rows: t.Rows, Text: text + ")"})
	}
	return out
}

// IDX06NoKeyHint is what IDX06's Do this adds when the heap has no primary key either (decision 10).
const IDX06NoKeyHint = " The Design card's DES01 adds a clustered primary key."
