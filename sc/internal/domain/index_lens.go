package domain

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// The Indexes page (docs/phase5t-contract.md): the indexes that the slowest queries read in full,
// across the database, with the queries as the evidence. It reuses 5q's measurement and fix; this file
// is the list: which indexes are candidates, how a row reads, and the sentence above it.
//
// Sources: as 5q. Microsoft Learn, "Maintain indexes optimally to improve performance and reduce
// resource utilization": reorganise only the indexes used by queries with degraded performance.
// Ola Hallengren, "SQL Server Index and Statistics Maintenance": MinNumberOfPages 1000. The bounds on
// how many queries and rows are read are kakapo's own and are said as such in the contract.

const (
	// LensMaxQueries is how many query shapes (by total duration over 7 days) have their plan read.
	LensMaxQueries = 100
	// LensMaxRows is how many indexes the page lists.
	LensMaxRows = 50
	// LensMeasureBatch is how many indexes one click of Measure all measures.
	LensMeasureBatch = 20
	// LensMinPages is Ola Hallengren's MinNumberOfPages: smaller indexes are not listed.
	LensMinPages = reorgMinPages
	// LensGainPct is the saving at which a measured row is pre-ticked (5q's low level).
	LensGainPct = reorgLowPct
	// LensDefaultCapGB is the size at which pre-ticking stops (kakapo's bound, an option of the fix).
	LensDefaultCapGB = 20
	// lensQueriesShown is how many queries a row names before "and n more".
	lensQueriesShown = 3
)

// LensQueryFact is one query shape of the last 7 days, with its most recently run plan.
type LensQueryFact struct {
	Hash      string // 0x and 16 hex digits
	Text      string // the first 200 characters
	Runs      int64
	ElapsedUs int64 // total duration, microseconds
	Reads     int64 // total logical reads
	LastRun   time.Time
	Plan      string // plan XML; "" when over the size limit
}

// LensScan is an index that at least one query's plan reads in full.
type LensScan struct {
	Schema  string
	Table   string
	Index   string
	Queries []int // positions in the queries it was found in, in the order given
}

// LensScans parses each query's plan and lists the indexes it reads in full (an Index Scan or
// Clustered Index Scan), once per index, with the queries that read it. Temporary tables, system
// tables and tables of other databases are left out, as 5j does.
func LensScans(database string, queries []LensQueryFact) []LensScan {
	out := []LensScan{}
	at := map[string]int{}
	for qi, q := range queries {
		if strings.TrimSpace(q.Plan) == "" {
			continue
		}
		sp := ParseShowplan(q.Plan)
		here := qPlanDatabase(database, sp.Objects)
		seen := map[string]bool{}
		for _, o := range sp.Objects {
			if !o.Scan || o.Index == "" || !qShowplanTableOf(here, o) {
				continue
			}
			key := strings.ToLower(o.Schema + "." + o.Table + "." + o.Index)
			if seen[key] {
				continue
			}
			seen[key] = true
			i, ok := at[key]
			if !ok {
				i = len(out)
				at[key] = i
				out = append(out, LensScan{Schema: o.Schema, Table: o.Table, Index: o.Index})
			}
			out[i].Queries = append(out[i].Queries, qi)
		}
	}
	return out
}

// IndexBasicsFact is what a cheap catalogue read says about one index: no physical-stats read.
type IndexBasicsFact struct {
	Schema     string
	Table      string
	Index      string
	TableFound bool
	IndexFound bool
	Type       int // sys.indexes.type: 1 clustered, 2 nonclustered, 5 and 6 columnstore
	Disabled   bool
	PageLocks  bool // allow_page_locks
	FillFactor int
	Pages      int64 // in-row data pages
	UsedPages  int64 // every page the index uses
}

// SizeMB is every page the index uses, in MB.
func (b IndexBasicsFact) SizeMB() float64 { return float64(b.UsedPages) * 8 / 1024 }

// rowstore says the index is a clustered or nonclustered rowstore index.
func (b IndexBasicsFact) rowstore() bool { return b.Type == 1 || b.Type == 2 }

// LensFacts is everything the page is built from.
type LensFacts struct {
	Database        string
	QueryStoreState string // actual_state_desc; "" when not read
	Error           string // why the queries could not be read
	Queries         []LensQueryFact
	Basics          []IndexBasicsFact // the scanned indexes, from the catalogue
	Tables          []QueryTableFact  // their tables' rows and statistics
	ServerNow       time.Time         // the server's clock, naive, when Tables was read
	TablesError     string
	Changes         []Change // kakapo's change log, recent
	CanChange       bool
	UTCOffsetMin    int
}

// LensQuery is one query that reads an index, the evidence of its row.
type LensQuery struct {
	Hash    string    `json:"hash"`
	Text    string    `json:"text"`
	Runs    int64     `json:"runs"`
	TotalMs float64   `json:"totalMs"`
	LastRun time.Time `json:"lastRun"` // UTC
	Summary string    `json:"summary"` // "4,310 runs, 38 min in 7 days"
}

// LensLast is the last time kakapo reorganised the index.
type LensLast struct {
	At     time.Time `json:"at"`     // UTC
	Result string    `json:"result"` // the change log's result: "IX_A: 12,400 pages to 7,250; …, verified"
}

// LensRow is one index of the page.
type LensRow struct {
	Schema  string      `json:"schema"`
	Table   string      `json:"table"`
	Name    string      `json:"name"` // schema.table
	Index   string      `json:"index"`
	Kind    string      `json:"kind"` // clustered | nonclustered
	Pages   int64       `json:"pages"`
	SizeMB  float64     `json:"sizeMB"`
	Queries []LensQuery `json:"queries"` // the slowest three
	// QueryCount is every query shape found reading it; More are the ones not named.
	QueryCount int     `json:"queryCount"`
	More       int     `json:"more"`
	TotalMs    float64 `json:"totalMs"`
	// Blocked says why the index cannot be reorganised ("" when it can).
	Blocked string `json:"blocked,omitempty"`
	// Stale is 5j's test; StatsNote says to update the statistics first. StatsAction is the existing
	// update-statistics-table action, offered on every row whose table has statistics.
	Stale       bool         `json:"stale"`
	StatsNote   string       `json:"statsNote,omitempty"`
	StatsAction *QueryAction `json:"statsAction,omitempty"`
	Last        *LensLast    `json:"last,omitempty"`
	// Options are the reorganize-index options of this row, as 5q's Measure action has them.
	Options map[string]string `json:"options"`
}

// IndexLens is the page (GET .../indexes/candidates).
type IndexLens struct {
	Database   string `json:"database"`
	CanChange  bool   `json:"canChange"`
	QueryStore string `json:"queryStore"` // READ_WRITE | READ_ONLY | OFF | ERROR | ""
	// Queries is how many query shapes had a plan read.
	Queries int `json:"queries"`
	// Answer is the sentence the page leads with; Reason, when the list could not be built.
	Answer           string    `json:"answer"`
	Reason           string    `json:"reason,omitempty"`
	Rows             []LensRow `json:"rows"`
	UTCOffsetMinutes int       `json:"utcOffsetMinutes"`
	// The page's rules, so that it holds no number of its own.
	MeasureBatch int     `json:"measureBatch"`
	GainPct      float64 `json:"gainPct"`
	CapGB        int     `json:"capGB"`
	Handover     string  `json:"handover"`
}

// BuildIndexLens turns the facts into the page.
func BuildIndexLens(f LensFacts) IndexLens {
	l := IndexLens{Database: f.Database, CanChange: f.CanChange, QueryStore: f.QueryStoreState, Queries: len(f.Queries), Rows: []LensRow{},
		UTCOffsetMinutes: f.UTCOffsetMin, MeasureBatch: LensMeasureBatch, GainPct: LensGainPct, CapGB: LensDefaultCapGB}
	switch {
	case f.Error != "":
		l.Reason = "kakapo could not read Query Store in " + f.Database + ": " + f.Error
	case f.QueryStoreState != "READ_WRITE" && f.QueryStoreState != "READ_ONLY":
		l.Reason = "Query Store is off in " + f.Database + ", so kakapo cannot tell which indexes your queries read in full. The Setup card turns it on."
	}
	if l.Reason != "" {
		l.Answer = l.Reason
		l.Handover = lensHandover(l)
		return l
	}
	basics := map[string]IndexBasicsFact{}
	for _, b := range f.Basics {
		basics[strings.ToLower(b.Schema+"."+b.Table+"."+b.Index)] = b
	}
	tables := map[string]QueryTableFact{}
	for _, t := range f.Tables {
		tables[strings.ToLower(t.Schema+"."+t.Table)] = t
	}
	for _, s := range LensScans(f.Database, f.Queries) {
		b, ok := basics[strings.ToLower(s.Schema+"."+s.Table+"."+s.Index)]
		if !ok || !b.TableFound || !b.IndexFound || b.Disabled || !b.rowstore() || b.Pages < LensMinPages {
			continue
		}
		l.Rows = append(l.Rows, lensRow(f, s, b, tables))
	}
	sort.SliceStable(l.Rows, func(i, j int) bool {
		if l.Rows[i].TotalMs != l.Rows[j].TotalMs {
			return l.Rows[i].TotalMs > l.Rows[j].TotalMs
		}
		return l.Rows[i].Name+l.Rows[i].Index < l.Rows[j].Name+l.Rows[j].Index
	})
	if len(l.Rows) > LensMaxRows {
		l.Rows = l.Rows[:LensMaxRows]
	}
	switch n := int64(len(l.Rows)); n {
	case 0:
		l.Answer = "No query in the last 7 days reads an index of 1,000 pages or more in full. Index work would not help your queries."
	default:
		l.Answer = fmt.Sprintf("Your slowest queries read %s in full. Measure %s to see which are worth reorganising.",
			plural(n, "big index", "big indexes"), map[bool]string{true: "it", false: "them"}[n == 1])
	}
	l.Handover = lensHandover(l)
	return l
}

func lensRow(f LensFacts, s LensScan, b IndexBasicsFact, tables map[string]QueryTableFact) LensRow {
	r := LensRow{Schema: s.Schema, Table: s.Table, Name: s.Schema + "." + s.Table, Index: b.Index, Kind: "nonclustered",
		Pages: b.Pages, SizeMB: roundHalfAway(b.SizeMB(), 1), Queries: []LensQuery{}, QueryCount: len(s.Queries),
		Options: ReorganizeIndexOptions(s.Schema, s.Table, b.Index)}
	if b.Type == 1 {
		r.Kind = "clustered"
	}
	qs := make([]LensQuery, 0, len(s.Queries))
	for _, qi := range s.Queries {
		q := f.Queries[qi]
		ms := float64(q.ElapsedUs) / 1000
		r.TotalMs += ms
		qs = append(qs, LensQuery{Hash: q.Hash, Text: q.Text, Runs: q.Runs, TotalMs: ms, LastRun: q.LastRun.UTC(),
			Summary: fmt.Sprintf("%s, %s in 7 days", plural(q.Runs, "run", "runs"), QueryDuration(ms))})
	}
	sort.SliceStable(qs, func(i, j int) bool { return qs[i].TotalMs > qs[j].TotalMs })
	if len(qs) > lensQueriesShown {
		r.More = len(qs) - lensQueriesShown
		qs = qs[:lensQueriesShown]
	}
	r.Queries = qs
	if !b.PageLocks {
		r.Blocked = b.Index + " has ALLOW_PAGE_LOCKS OFF, and an index cannot be reorganised then (Microsoft)."
	}
	if t, ok := tables[strings.ToLower(s.Schema+"."+s.Table)]; ok && f.TablesError == "" {
		stale, age := lensStale(t, f.ServerNow)
		if t.Found && t.Stats > 0 && f.CanChange {
			r.StatsAction = qStatsAction(t.Schema, t.Table)
		}
		if r.Stale = stale; stale {
			r.StatsNote = LensStatsNote(age, roundHalfAway(t.MaxModPct, 1))
		}
	}
	r.Last = lensLast(f.Changes, s.Schema, s.Table, b.Index)
	return r
}

// lensStale applies 5j's test to a table's statistics: stale or not, and their age in days.
func lensStale(t QueryTableFact, now time.Time) (bool, *int) {
	qt := QueryTable{Schema: t.Schema, Table: t.Table, Found: t.Found, Rows: t.Rows, Statistics: t.Stats, MaxModifiedPct: roundHalfAway(t.MaxModPct, 1)}
	var age *int
	if t.OldestUpdate != nil {
		d := max(int(now.Sub(*t.OldestUpdate).Hours()/24), 0)
		age = &d
		qt.AgeDays = age
	}
	return qStatsStale(qt), age
}

// LensStaleTables are the tables, as schema.table, whose statistics are old by 5j's test.
func LensStaleTables(tables []QueryTableFact, now time.Time) []string {
	out := []string{}
	for _, t := range tables {
		if stale, _ := lensStale(t, now); stale {
			out = append(out, t.Schema+"."+t.Table)
		}
	}
	return out
}

// LensStatsNote is the sentence on a row whose statistics are old (5j's test): update them first.
func LensStatsNote(ageDays *int, changedPct float64) string {
	old := "old"
	if ageDays != nil {
		switch *ageDays {
		case 0:
			old = "from today"
		case 1:
			old = "1 day old"
		default:
			old = fmt.Sprintf("%d days old", *ageDays)
		}
	}
	return fmt.Sprintf("Statistics are %s and %s%% of rows have changed. %s", old, n0(changedPct), QueryStatsFirst)
}

// lensLast is the newest reorganise of the index in the change log.
func lensLast(changes []Change, schema, table, index string) *LensLast {
	object := IndexObject(schema, table, index)
	var best *Change
	for i := range changes {
		c := &changes[i]
		if c.ChangeType != "INDEX" || c.Object != object || !strings.HasPrefix(c.Description, "Reorganised") {
			continue
		}
		if best == nil || c.ChangedAt.After(best.ChangedAt) {
			best = c
		}
	}
	if best == nil {
		return nil
	}
	return &LensLast{At: best.ChangedAt.UTC(), Result: best.Result}
}

func lensHandover(l IndexLens) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Indexes worth reorganising in %s\n\n%s\n", l.Database, l.Answer)
	for _, r := range l.Rows {
		fmt.Fprintf(&b, "\n%s.%s: %s pages (%s)", r.Name, r.Index, n0int(r.Pages), sizeText(r.SizeMB))
		for _, q := range r.Queries {
			fmt.Fprintf(&b, "\n  read by %s (%s)", q.Hash, q.Summary)
		}
	}
	if len(l.Rows) > 0 {
		b.WriteString("\n\nA reorganise is online, can be stopped, and does not update statistics (Microsoft Learn, \"Maintain indexes optimally\"). Measure an index first: kakapo shows the pages a reorganise would save.")
	}
	return b.String()
}
