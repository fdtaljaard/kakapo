package domain

import (
	"fmt"
	"strconv"
	"strings"
)

// reorganize-indexes (docs/phase5t-contract.md, section 3): the Indexes page's "Reorganise selected".
// It runs 5q's reorganize-index once per chosen index, one after another, each with its own change-log
// row. The preview is built from a cheap catalogue read (IndexBasicsFact); the run measures each index
// just before it works on it.
//
// Sources: as 5q (Microsoft Learn, "Maintain indexes optimally to improve performance and reduce
// resource utilization": reorganise is online, can be stopped and keeps its progress, updates no
// statistics, needs free space in the index's file; Brent Ozar, "How to Configure Ola Hallengren's
// IndexOptimize Maintenance Script": index work inflates log backups). The 20 GB size at which a batch
// gets the stronger warning is kakapo's own bound, an option, said as such.

// FixReorganizeIndexes reorganises the indexes the Indexes page ticked.
const FixReorganizeIndexes = "reorganize-indexes"

const (
	// OptionIndexes is the chosen indexes, one per line: "[schema].[table]", a tab, the index name.
	OptionIndexes = "indexes"
	// OptionMaxGB is the size above which the preview warns more strongly; 0 means no bound.
	OptionMaxGB = "maxGB"
)

// BatchIndex is one chosen index.
type BatchIndex struct {
	Schema string
	Table  string
	Index  string
}

// Label is "schema.table · index".
func (b BatchIndex) Label() string { return b.Schema + "." + b.Table + " · " + b.Index }

// BatchIndexesOption writes the chosen indexes as the option carries them.
func BatchIndexesOption(list []BatchIndex) string {
	lines := make([]string, 0, len(list))
	for _, b := range list {
		lines = append(lines, QuoteName(b.Schema)+"."+QuoteName(b.Table)+"\t"+b.Index)
	}
	return strings.Join(lines, "\n")
}

// ParseBatchIndexes reads the indexes option: each line a table name, a tab and an index name. A line
// that is not one is an error; repeats are kept once.
func ParseBatchIndexes(s string) ([]BatchIndex, error) {
	out := []BatchIndex{}
	seen := map[string]bool{}
	for _, line := range strings.Split(strings.ReplaceAll(s, "\r", ""), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		table, index, ok := strings.Cut(line, "\t")
		if !ok || strings.TrimSpace(index) == "" {
			return nil, fmt.Errorf("%q is not a table and an index", line)
		}
		_, schema, tbl, err := SplitTableName(table)
		if err != nil {
			return nil, err
		}
		key := strings.ToLower(schema + "." + tbl + "." + index)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, BatchIndex{Schema: schema, Table: tbl, Index: index})
	}
	return out, nil
}

// BatchMaxGB reads the maxGB option: the default when it is missing or not a number, 0 for no bound.
func BatchMaxGB(options map[string]string) int {
	v, err := strconv.Atoi(strings.TrimSpace(options[OptionMaxGB]))
	if err != nil || v < 0 {
		return LensDefaultCapGB
	}
	return v
}

// BatchFacts is the catalogue read of the chosen indexes, and what the warnings say about the database.
type BatchFacts struct {
	ProductVersion string
	Recovery       string // FULL, BULK_LOGGED or SIMPLE
	Basics         []IndexBasicsFact
	// StaleTables are the chosen indexes' tables whose statistics are old (5j's test), as schema.table.
	StaleTables []string
}

// BatchInputs are the fix's inputs: the size bound, under More options.
func batchInputs(maxGB int) []FixInput {
	return []FixInput{{Name: OptionMaxGB, Label: "Warn above (GB of indexes)", Kind: "text",
		Value: strconv.Itoa(maxGB), Advanced: true,
		Help: "A reorganise logs about the pages it moves. Above this size the preview warns more strongly. 0 means no bound."}}
}

const (
	batchOnline   = "Reorganising is online on every edition: queries and changes to the tables carry on while it runs. Each index is reorganised in turn; stop the run at any time and the work done so far is kept."
	batchContinue = "If one index fails, kakapo goes on to the next. An index that is already full and in order when its turn comes is skipped."
	batchNoStats  = "Reorganising does not update statistics. Update the statistics of the tables named on the page first: it is cheaper, and often all a slow query needs."
	batchOpen     = "kakapo must stay open until this finishes: it runs from kakapo, not from SQL Server Agent. Closing kakapo stops the run (the index in progress keeps its progress), and it does not resume."
	batchChange   = "Each index gets its own row in the change log, so a query's page compares its runs before and after, index by index."
)

// BuildReorganizeBatchFix builds reorganize-indexes from the catalogue read: one change per index that
// can be reorganised, the statements in order, and the warnings. Blocked (no changes) when nothing is
// chosen or no index can be reorganised. No rollback: a reorganise only moves pages.
func BuildReorganizeBatchFix(database string, chosen []BatchIndex, f BatchFacts, options map[string]string) (FixPreview, Fix) {
	def, _ := FindFixDef(FixReorganizeIndexes)
	maxGB := BatchMaxGB(options)
	echo := map[string]string{}
	for k, v := range options {
		echo[k] = v
	}
	echo[OptionIndexes] = BatchIndexesOption(chosen)
	echo[OptionMaxGB] = strconv.Itoa(maxGB)
	fix := Fix{ID: def.ID, Title: def.Title, Scope: def.Scope, CheckID: def.CheckID, Setting: def.Setting, Current: "not reorganised", Target: "reorganised", Options: echo}
	pv := FixPreview{Database: database, EngineVersion: f.ProductVersion, Changes: []Drift{}, Warnings: []string{}, Options: echo, Inputs: batchInputs(maxGB)}
	if len(chosen) == 0 {
		pv.Warnings = append(pv.Warnings, "Tick at least one index.")
		return pv, fix
	}
	basics := map[string]IndexBasicsFact{}
	for _, b := range f.Basics {
		basics[strings.ToLower(b.Schema+"."+b.Table+"."+b.Index)] = b
	}
	var stmts, skipped []string
	var pages, usedPages int64
	var sizeMB float64
	for _, c := range chosen {
		b, ok := basics[strings.ToLower(c.Schema+"."+c.Table+"."+c.Index)]
		if !ok {
			b = IndexBasicsFact{Schema: c.Schema, Table: c.Table, Index: c.Index}
		}
		shape := IndexShapeFacts{Database: database, Schema: c.Schema, Table: c.Table, Index: c.Index, TableFound: b.TableFound, IndexFound: b.IndexFound,
			Type: b.Type, Disabled: b.Disabled, PageLocks: b.PageLocks}
		if why := reorgRefusal(shape); why != "" {
			skipped = append(skipped, "Left out: "+why)
			continue
		}
		pages += b.Pages
		usedPages += b.UsedPages
		sizeMB += b.SizeMB()
		pv.Changes = append(pv.Changes, Drift{Setting: c.Label(), Current: plural(b.Pages, "page", "pages"), Target: "reorganised"})
		stmts = append(stmts, "-- "+c.Label()+"\n"+ReorganizeStatement(database, c.Schema, c.Table, c.Index))
	}
	if len(pv.Changes) == 0 {
		pv.Warnings = append(pv.Warnings, skipped...)
		return pv, fix
	}
	fix.Current = plural(int64(len(pv.Changes)), "index", "indexes") + ", " + plural(pages, "page", "pages")
	pv.Lead = fmt.Sprintf("%s to reorganise, one after another: %s (%s).", plural(int64(len(pv.Changes)), "index", "indexes"),
		plural(usedPages, "page", "pages"), sizeText(sizeMB))
	pv.Warnings = append(pv.Warnings, batchOnline, batchContinue)
	if strings.EqualFold(f.Recovery, "FULL") || f.Recovery == "" {
		pv.Warnings = append(pv.Warnings, fmt.Sprintf("In FULL recovery every page it moves is logged: the next log backups carry roughly %s more, and the log file grows if the log backups cannot keep up.", sizeText(sizeMB)))
	} else {
		pv.Warnings = append(pv.Warnings, fmt.Sprintf(reorgLogOther, f.Recovery))
	}
	if maxGB > 0 && sizeMB > float64(maxGB)*1024 {
		pv.Warnings = append(pv.Warnings, fmt.Sprintf("This is %s of indexes, more than the %d GB kakapo starts a batch with. Take the log backups and the free space into account, or tick fewer.", sizeText(sizeMB), maxGB))
	}
	pv.Warnings = append(pv.Warnings, reorgSpace)
	if len(f.StaleTables) > 0 {
		pv.Warnings = append(pv.Warnings, batchNoStats+" Old statistics: "+strings.Join(f.StaleTables, ", ")+".")
	} else {
		pv.Warnings = append(pv.Warnings, "Reorganising does not update statistics (Microsoft).")
	}
	pv.Warnings = append(pv.Warnings, batchOpen, batchChange)
	pv.Warnings = append(pv.Warnings, skipped...)
	pv.Warnings = append(pv.Warnings, "Needs the ALTER permission on each table.")
	pv.Statement = strings.Join(stmts, "\n")
	pv.Rollback = ""
	return pv, fix
}

// BatchChangeCount is how many indexes a batch preview would work on.
func BatchChangeCount(pv FixPreview) int { return len(pv.Changes) }

// RunItem is one index of a batch run.
type RunItem struct {
	Label    string `json:"label"`
	State    string `json:"state"` // waiting | running | done | differs | skipped | failed | stopped
	Text     string `json:"text"`
	ChangeID int64  `json:"changeId,omitempty"`
}

// The states of a RunItem.
const (
	ItemWaiting = "waiting"
	ItemRunning = "running"
	ItemDone    = "done"
	ItemDiffers = "differs"
	ItemSkipped = "skipped"
	ItemFailed  = "failed"
	ItemStopped = "stopped"
)

// BatchSummary is the run's result line: "4 reorganised, 1 skipped, 1 failed". Verified when none
// failed and it was not stopped.
func BatchSummary(items []RunItem) (string, bool) {
	counts := map[string]int{}
	for _, it := range items {
		counts[it.State]++
	}
	var parts []string
	if n := counts[ItemDone] + counts[ItemDiffers]; n > 0 {
		parts = append(parts, fmt.Sprintf("%d reorganised", n))
	}
	if n := counts[ItemSkipped]; n > 0 {
		parts = append(parts, fmt.Sprintf("%d skipped", n))
	}
	if n := counts[ItemFailed]; n > 0 {
		parts = append(parts, fmt.Sprintf("%d failed", n))
	}
	if n := counts[ItemStopped] + counts[ItemWaiting] + counts[ItemRunning]; n > 0 {
		parts = append(parts, fmt.Sprintf("%d not done (stopped)", n))
	}
	if len(parts) == 0 {
		return "nothing was reorganised", false
	}
	return strings.Join(parts, ", "), counts[ItemFailed] == 0 && counts[ItemStopped]+counts[ItemWaiting]+counts[ItemRunning] == 0 && counts[ItemDiffers] == 0
}

// ReorganizeBatchChangeResult is the change log's result for an index a stop interrupted.
func ReorganizeInterruptedText(before, after IndexShapeFacts) string {
	if after.Error != "" || !after.IndexFound {
		return "stopped part-way; the index could not be measured afterwards"
	}
	return fmt.Sprintf("stopped part-way: %s pages to %s; %s%% out of order to %s%%", n0int(before.Pages), n0int(after.Pages), n0(before.Fragmentation), n0(after.Fragmentation))
}
