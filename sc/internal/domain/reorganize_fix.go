package domain

import (
	"fmt"
	"math"
	"strings"
)

// The query page's index measurement and the reorganize-index fix (docs/phase5q-contract.md,
// section 4): for an index a plan reads in full, its pages, how full they are and how out of order,
// and what a reorganise would leave; the fix runs ALTER INDEX ... REORGANIZE on that one index.
//
// Sources (Microsoft Learn, "Maintain indexes optimally to improve performance and reduce resource
// utilization", 2026-10-27): reorganise "should be your preferred index maintenance method"; it "is
// always an online operation"; it "compacts index pages to make page density equal to the fill
// factor"; "Statistics aren't updated when an index is reorganized"; an interrupted reorganise keeps
// its progress; it needs free space in the index's own data file (error 1105); "An index can't be
// reorganized when ALLOW_PAGE_LOCKS is set to OFF". Ola Hallengren, "SQL Server Index and Statistics
// Maintenance": MinNumberOfPages 1000 and FragmentationLevel1 5 (below it, no maintenance).

// FixReorganizeIndex reorganises one index the query page measured.
const FixReorganizeIndex = "reorganize-index"

const (
	// reorgMinPages is Ola Hallengren's MinNumberOfPages default: smaller indexes are not maintained.
	reorgMinPages = 1000
	// reorgLowPct is Ola Hallengren's FragmentationLevel1 default (below it, no maintenance), used
	// for fragmentation and, kakapo's choice, for the share of pages a reorganise would save.
	reorgLowPct = 5.0
)

// IndexShapeFacts is what the measurement reads about one index (collect.IndexShape): the leaf
// level's in-row pages from sys.dm_db_index_physical_stats in SAMPLED mode, and sys.indexes.
type IndexShapeFacts struct {
	ProductVersion string
	EngineEdition  int
	Database       string
	Recovery       string // FULL, BULK_LOGGED or SIMPLE
	Schema         string
	Table          string
	Index          string // as asked; the index's own name once found
	TableFound     bool
	IndexFound     bool
	Type           int // sys.indexes.type: 1 clustered, 2 nonclustered, 5 and 6 columnstore
	Disabled       bool
	PageLocks      bool // allow_page_locks
	FillFactor     int  // 0 means 100
	Pages          int64
	Density        float64 // avg_page_space_used_in_percent of the leaf level
	Fragmentation  float64 // avg_fragmentation_in_percent of the leaf level
	SizeMB         float64 // every page the index uses
	Error          string  // the measurement could not run (permission, timeout)
}

// TableName is "schema.table".
func (f IndexShapeFacts) TableName() string { return f.Schema + "." + f.Table }

// fillFactor is the fill factor a reorganise packs to: 0 means 100.
func (f IndexShapeFacts) fillFactor() int {
	if f.FillFactor <= 0 || f.FillFactor > 100 {
		return 100
	}
	return f.FillFactor
}

// EstimatedPages is about how many pages the leaf would take packed to its fill factor: pages ×
// density ÷ fill factor, never more than it has now.
func (f IndexShapeFacts) EstimatedPages() int64 {
	est := int64(math.Ceil(float64(f.Pages) * f.Density / float64(f.fillFactor())))
	return min(max(est, 0), f.Pages)
}

// SavedPct is the share of its pages a scan would no longer read after a reorganise.
func (f IndexShapeFacts) SavedPct() float64 {
	if f.Pages <= 0 {
		return 0
	}
	return float64(f.Pages-f.EstimatedPages()) * 100 / float64(f.Pages)
}

// rowstore says the index is a clustered or nonclustered rowstore index, the only kind this fix takes.
func (f IndexShapeFacts) rowstore() bool { return f.Type == 1 || f.Type == 2 }

// IndexShape is the measurement as the query page shows it: one sentence, the facts, and the
// reorganize-index action when there is something to gain and the database can be changed.
type IndexShape struct {
	Schema         string       `json:"schema"`
	Table          string       `json:"table"`
	Index          string       `json:"index"`
	Measured       bool         `json:"measured"` // false: Text says why there is nothing to show
	Pages          int64        `json:"pages"`
	SizeMB         float64      `json:"sizeMB"`
	Density        float64      `json:"density"`
	Fragmentation  float64      `json:"fragmentation"`
	EstimatedPages int64        `json:"estimatedPages"`
	SavedPct       float64      `json:"savedPct"`
	Text           string       `json:"text"`
	Action         *QueryAction `json:"action,omitempty"`
}

// BuildIndexShape turns a measurement into the query page's lines (docs/phase5q-contract.md, section
// 4, decision 4): the facts and the estimate, no judgement of kakapo's own; "nothing to gain" only
// under Ola's low level; the action from 1,000 pages.
func BuildIndexShape(f IndexShapeFacts, canChange bool) IndexShape {
	x := IndexShape{Schema: f.Schema, Table: f.Table, Index: f.Index}
	switch {
	case f.Error != "":
		x.Text = "Could not measure " + f.Index + ": " + f.Error
		return x
	case !f.TableFound:
		x.Text = fmt.Sprintf("There is no table %s in %s.", f.TableName(), f.Database)
		return x
	case !f.IndexFound:
		x.Text = fmt.Sprintf("There is no index %s on %s.", f.Index, f.TableName())
		return x
	case f.Disabled:
		x.Text = f.Index + " is disabled: it has no pages to measure."
		return x
	case !f.rowstore():
		x.Text = f.Index + " is a columnstore index: its fragmentation is measured by deleted rows, which this page does not read."
		return x
	}
	x.Measured = true
	x.Pages, x.SizeMB = f.Pages, roundHalfAway(f.SizeMB, 1)
	x.Density, x.Fragmentation = roundHalfAway(f.Density, 1), roundHalfAway(f.Fragmentation, 1)
	x.EstimatedPages, x.SavedPct = f.EstimatedPages(), roundHalfAway(f.SavedPct(), 1)
	reads := fmt.Sprintf("The plan reads all of %s (%s, %s).", f.Index, plural(f.Pages, "page", "pages"), sizeText(f.SizeMB))
	shape := fmt.Sprintf("Its pages are %s%% full and %s%% out of order.", n0(f.Density), n0(f.Fragmentation))
	switch {
	case f.Pages < reorgMinPages:
		x.Text = fmt.Sprintf("%s It is under 1,000 pages: index work on an index this small does not change how a query runs (Ola Hallengren's floor).", reads)
		return x
	case f.Fragmentation < reorgLowPct && f.SavedPct() < reorgLowPct:
		x.Text = fmt.Sprintf("%s %s Index work would not help this query.", reads, shape)
		return x
	}
	x.Text = fmt.Sprintf("%s %s Reorganising it would pack it into about %s pages, so each scan would read about %s%% fewer.",
		reads, shape, n0int(x.EstimatedPages), n0(f.SavedPct()))
	if f.SavedPct() < reorgLowPct {
		x.Text = fmt.Sprintf("%s %s Reorganising it would put its pages back in order; they are already about as full as they can be.", reads, shape)
	}
	if canChange {
		x.Action = &QueryAction{FixID: FixReorganizeIndex, Label: "Reorganise index", Title: "Reorganise " + f.Index,
			Options: ReorganizeIndexOptions(f.Schema, f.Table, f.Index)}
	}
	return x
}

// ReorganizeIndexOptions are the fix's options: the table and the index.
func ReorganizeIndexOptions(schema, table, index string) map[string]string {
	return map[string]string{OptionTable: QuoteName(schema) + "." + QuoteName(table), OptionIndex: index}
}

const (
	reorgOnline   = "Reorganising is online on every edition: queries and changes to the table carry on while it runs. If it is stopped, the work done so far is kept."
	reorgNoStats  = "Reorganising does not update statistics. If this table's statistics are old, update them first (Update statistics on this table): it is cheaper, and often all a slow query needs."
	reorgLogFull  = "In FULL recovery every page it moves is logged: the next log backup carries roughly %s more, and the log file grows if the log backups cannot keep up."
	reorgLogOther = "Every page it moves is logged; in %s recovery the log empties at the next checkpoint."
	reorgSpace    = "It needs a little free space in the data file that holds the index; when that file is full it stops with error 1105."
	reorgMeasure  = "Afterwards kakapo measures the index again, and the query's verdict compares the runs before the change with the runs after it."
)

// BuildReorganizeIndexFix builds reorganize-index from the measurement: ALTER INDEX ... REORGANIZE,
// with no transaction around it. Blocked when the index cannot be reorganised (not there, disabled,
// a columnstore index, ALLOW_PAGE_LOCKS OFF) or the measurement failed. No rollback: a reorganise
// only moves pages.
func BuildReorganizeIndexFix(f IndexShapeFacts, options map[string]string) (FixPreview, Fix) {
	def, _ := FindFixDef(FixReorganizeIndex)
	echo := map[string]string{}
	for k, v := range options {
		echo[k] = v
	}
	echo[OptionTable] = QuoteName(f.Schema) + "." + QuoteName(f.Table)
	echo[OptionIndex] = f.Index
	fix := Fix{ID: def.ID, Title: def.Title, Scope: def.Scope, CheckID: def.CheckID, Setting: def.Setting, Current: f.Index, Target: "reorganised", Options: echo}
	pv := FixPreview{Database: f.Database, EngineVersion: f.ProductVersion, Changes: []Drift{}, Warnings: []string{}, Options: echo}
	if refuse := reorgRefusal(f); refuse != "" {
		pv.Warnings = append(pv.Warnings, refuse)
		return pv, fix
	}
	fix.Current = reorgCurrent(f)
	pv.Lead = fmt.Sprintf("%s: %s, %s%% full and %s%% out of order. Reorganised, about %s pages.",
		f.Index, plural(f.Pages, "page", "pages"), n0(f.Density), n0(f.Fragmentation), n0int(f.EstimatedPages()))
	pv.Warnings = append(pv.Warnings, reorgOnline, reorgNoStats)
	if strings.EqualFold(f.Recovery, "FULL") || f.Recovery == "" {
		pv.Warnings = append(pv.Warnings, fmt.Sprintf(reorgLogFull, sizeText(f.SizeMB)))
	} else {
		pv.Warnings = append(pv.Warnings, fmt.Sprintf(reorgLogOther, f.Recovery))
	}
	pv.Warnings = append(pv.Warnings, reorgSpace, reorgMeasure, indexPermission(f.TableName()))
	pv.Changes = append(pv.Changes, Drift{Setting: "Index", Current: fix.Current, Target: "reorganised (packed to " + fmt.Sprint(f.fillFactor()) + "% full, in order)"})
	pv.Statement = ReorganizeStatement(f.Database, f.Schema, f.Table, f.Index)
	return pv, fix
}

// ReorganizeStatement is the fix's one statement, with the table's three-part name as the other
// index fixes write it (the connection is in master).
func ReorganizeStatement(database, schema, table, index string) string {
	return "ALTER INDEX " + QuoteName(index) + " ON " + QuoteName(database) + "." + QuoteName(schema) + "." + QuoteName(table) + " REORGANIZE;"
}

func reorgRefusal(f IndexShapeFacts) string {
	switch {
	case f.Error != "":
		return "The index could not be measured, so the change cannot be previewed: " + f.Error
	case !f.TableFound:
		return fmt.Sprintf("There is no table %s in %s.", f.TableName(), f.Database)
	case !f.IndexFound:
		return fmt.Sprintf("There is no index %s on %s.", f.Index, f.TableName())
	case f.Disabled:
		return f.Index + " is disabled: there is nothing to reorganise."
	case !f.rowstore():
		return f.Index + " is not a clustered or nonclustered rowstore index: this fix reorganises only those."
	case !f.PageLocks:
		return f.Index + " has ALLOW_PAGE_LOCKS OFF, and an index cannot be reorganised then (Microsoft)."
	}
	return ""
}

func reorgCurrent(f IndexShapeFacts) string {
	return fmt.Sprintf("%s, %s%% full, %s%% out of order", plural(f.Pages, "page", "pages"), n0(f.Density), n0(f.Fragmentation))
}

// ReorganizeIndexReadBack compares the index measured again with before: verified when it is in
// order (under Ola's low level) or uses fewer pages. "IX_A: 12,400 pages to 7,250; 41% out of order to 1%."
func ReorganizeIndexReadBack(before, after IndexShapeFacts) (string, bool) {
	switch {
	case after.Error != "":
		return "the index could not be measured again: " + after.Error, false
	case !after.IndexFound:
		return fmt.Sprintf("%s was not found on %s after the change", before.Index, before.TableName()), false
	}
	text := fmt.Sprintf("%s: %s pages to %s; %s%% out of order to %s%%, %s%% full to %s%%.", after.Index,
		n0int(before.Pages), n0int(after.Pages), n0(before.Fragmentation), n0(after.Fragmentation), n0(before.Density), n0(after.Density))
	return text, after.Fragmentation < reorgLowPct || after.Pages < before.Pages
}

// ReorganizeIndexDone reads a reorganise back when only the measurement after it is known (its
// connection was lost): done when the index is in order, under Ola's low level.
func ReorganizeIndexDone(after IndexShapeFacts) (string, bool) {
	switch {
	case after.Error != "":
		return "the index could not be measured again: " + after.Error, false
	case !after.IndexFound:
		return fmt.Sprintf("%s was not found on %s after the change", after.Index, after.TableName()), false
	}
	return fmt.Sprintf("%s: %s, %s%% out of order, %s%% full.", after.Index, plural(after.Pages, "page", "pages"), n0(after.Fragmentation), n0(after.Density)),
		after.Fragmentation < reorgLowPct
}

// ReorganizeChangeDescription is the change log's line.
func ReorganizeChangeDescription(f IndexShapeFacts, database string) string {
	return fmt.Sprintf("Reorganised %s on %s in %s (%s)", f.Index, f.TableName(), database, reorgCurrent(f))
}
