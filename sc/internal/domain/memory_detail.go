package domain

import (
	"fmt"
	"math"
	"sort"
	"strings"
	"time"
)

// DatabaseCacheFacts is Database detail's one pass over the buffer descriptors of one database, in that
// database's context (docs/phase5v-contract.md, decision 3), joined to the sizes and the index usage.
type DatabaseCacheFacts struct {
	Database   string           `json:"database"`
	ReadAt     time.Time        `json:"readAt"` // the server's clock, UTC
	StartedAt  time.Time        `json:"startedAt"`
	Seconds    float64          `json:"seconds"`
	Permission string           `json:"permission,omitempty"` // the right the login lacks
	Indexes    []IndexCacheFact `json:"indexes"`
}

// IndexCacheFact is one index (or heap) with pages in cache.
type IndexCacheFact struct {
	Schema      string     `json:"schema"`
	Table       string     `json:"table"`
	Index       string     `json:"index"` // "" for a heap
	IndexID     int        `json:"indexId"`
	Type        string     `json:"type"` // HEAP, CLUSTERED, NONCLUSTERED, CLUSTERED COLUMNSTORE, ...
	CachedPages int64      `json:"cachedPages"`
	TotalPages  int64      `json:"totalPages"` // used pages, all partitions
	TablePages  int64      `json:"tablePages"` // the table's clustered index or heap
	Seeks       int64      `json:"seeks"`
	Scans       int64      `json:"scans"`
	Lookups     int64      `json:"lookups"`
	Updates     int64      `json:"updates"`
	LastRead    *time.Time `json:"lastRead,omitempty"` // UTC; nil when not read since the restart
	Keys        string     `json:"keys"`
	Includes    string     `json:"includes"`
	// SameLeading counts the other nonclustered indexes on the table whose first key column is this one's.
	SameLeading int `json:"sameLeading"`
}

// DatabaseMemory is Database detail as shown and as stored (its snapshot keeps the built rows, since the
// facts of a large database run to thousands of indexes).
type DatabaseMemory struct {
	Database   string          `json:"database"`
	TakenAt    time.Time       `json:"takenAt"`
	Seconds    float64         `json:"seconds"`
	Permission string          `json:"permission,omitempty"`
	Answer     string          `json:"answer"`
	CachedGB   float64         `json:"cachedGB"`
	HotGB      float64         `json:"hotGB"`
	WarmGB     float64         `json:"warmGB"`
	ColdGB     float64         `json:"coldGB"`
	ColdNote   string          `json:"coldNote"`
	UsageNote  string          `json:"usageNote,omitempty"` // up less than 24 hours
	Indexes    int             `json:"indexes"`
	Rows       []IndexCacheRow `json:"rows"`
	Rest       *IndexCacheRest `json:"rest,omitempty"`
	Handover   string          `json:"handover"`
}

// IndexCacheRow is one line of Database detail.
type IndexCacheRow struct {
	Table       string     `json:"table"` // schema.table
	Index       string     `json:"index"` // "(heap)" for a heap
	Type        string     `json:"type"`
	CachedMB    float64    `json:"cachedMB"`
	TotalMB     float64    `json:"totalMB"`
	CachedPct   float64    `json:"cachedPct"`
	Seeks       int64      `json:"seeks"`
	Scans       int64      `json:"scans"`
	Lookups     int64      `json:"lookups"`
	Updates     int64      `json:"updates"`
	LastRead    *time.Time `json:"lastRead,omitempty"`
	Temperature string     `json:"temperature"` // hot | warm | cold
	Keys        string     `json:"keys"`
	Includes    string     `json:"includes"`
	Flags       []string   `json:"flags"`
}

// IndexCacheRest sums the indexes beyond the top MemoryDetailRows.
type IndexCacheRest struct {
	Indexes  int     `json:"indexes"`
	CachedMB float64 `json:"cachedMB"`
}

const memoryColdNote = "Cold is the number to trust: one seek marks a whole index as read."

// Temperature is decision 3's: hot when read in the last hour, warm in the last 24 hours, else cold.
func Temperature(lastRead *time.Time, now time.Time) string {
	switch {
	case lastRead == nil:
		return "cold"
	case now.Sub(*lastRead) <= MemoryHot:
		return "hot"
	case now.Sub(*lastRead) <= MemoryWarm:
		return "warm"
	}
	return "cold"
}

func round1(x float64) float64 { return math.Round(x*10) / 10 }

// IndexCacheFlags are decision 6's Database detail flags for one index.
func IndexCacheFlags(x IndexCacheFact) []string {
	flags := []string{}
	cached, total := pagesMB(x.CachedPages), pagesMB(x.TotalPages)
	if total >= MemoryMostlyCachedMinMB && cached >= total*MemoryMostlyCachedPct/100 {
		flags = append(flags, "Mostly cached: probably scanned.")
	}
	nonclustered := strings.EqualFold(x.Type, "NONCLUSTERED")
	if nonclustered && x.TablePages > 0 && total >= MemoryWideIndexMinMB && float64(x.TotalPages) >= float64(x.TablePages)*MemoryWideIndexPct/100 {
		pct := float64(x.TotalPages) / float64(x.TablePages) * 100
		if x.Includes != "" {
			flags = append(flags, fmt.Sprintf("%.0f%% of its table's size: a wide INCLUDE list, close to a second copy.", pct))
		} else {
			flags = append(flags, fmt.Sprintf("%.0f%% of its table's size: close to a second copy of the table.", pct))
		}
	}
	if nonclustered && x.SameLeading > 0 {
		lead, _, _ := strings.Cut(x.Keys, ",")
		flags = append(flags, fmt.Sprintf("Shares its leading key column (%s) with %s on this table.",
			strings.TrimSpace(lead), plural(int64(x.SameLeading), "other index", "other indexes")))
	}
	if strings.EqualFold(x.Type, "HEAP") && cached >= MemoryHeapCachedMB {
		flags = append(flags, "A heap: it is read by scanning.")
	}
	return flags
}

// BuildDatabaseMemory builds Database detail: the hot, warm and cold split over every cached index, the top
// MemoryDetailRows by cached MB with their flags, and the rest in one line.
func BuildDatabaseMemory(f DatabaseCacheFacts) DatabaseMemory {
	d := DatabaseMemory{Database: f.Database, TakenAt: f.ReadAt, Seconds: f.Seconds, Permission: f.Permission,
		ColdNote: memoryColdNote, Rows: []IndexCacheRow{}}
	if f.Permission != "" {
		d.Answer = "kakapo cannot read this database's cache: the login needs " + f.Permission + "."
		d.Handover = d.Answer
		return d
	}
	xs := append([]IndexCacheFact(nil), f.Indexes...)
	sort.SliceStable(xs, func(i, j int) bool { return xs[i].CachedPages > xs[j].CachedPages })
	var hot, warm, cold float64
	for i, x := range xs {
		mb := pagesMB(x.CachedPages)
		t := Temperature(x.LastRead, f.ReadAt)
		switch t {
		case "hot":
			hot += mb
		case "warm":
			warm += mb
		default:
			cold += mb
		}
		if i >= MemoryDetailRows {
			if d.Rest == nil {
				d.Rest = &IndexCacheRest{}
			}
			d.Rest.Indexes++
			d.Rest.CachedMB += mb
			continue
		}
		name := x.Index
		if strings.EqualFold(x.Type, "HEAP") || name == "" {
			name = "(heap)"
		}
		pct := 0.0
		if x.TotalPages > 0 {
			pct = math.Min(100, float64(x.CachedPages)/float64(x.TotalPages)*100)
		}
		d.Rows = append(d.Rows, IndexCacheRow{
			Table: x.Schema + "." + x.Table, Index: name, Type: strings.ToLower(x.Type),
			CachedMB: round1(mb), TotalMB: round1(pagesMB(x.TotalPages)), CachedPct: round1(pct),
			Seeks: x.Seeks, Scans: x.Scans, Lookups: x.Lookups, Updates: x.Updates, LastRead: x.LastRead,
			Temperature: t, Keys: x.Keys, Includes: x.Includes, Flags: IndexCacheFlags(x),
		})
	}
	if d.Rest != nil {
		d.Rest.CachedMB = round1(d.Rest.CachedMB)
	}
	d.Indexes = len(xs)
	d.HotGB, d.WarmGB, d.ColdGB, d.CachedGB = gbOf(hot), gbOf(warm), gbOf(cold), gbOf(hot+warm+cold)
	if !f.StartedAt.IsZero() && f.ReadAt.Sub(f.StartedAt) < MemoryUsageReliable {
		d.UsageNote = fmt.Sprintf("SQL Server started %s ago, and index usage is counted from the restart: the temperatures are not reliable yet.",
			uptimeText(f.ReadAt.Sub(f.StartedAt)))
	}
	switch {
	case len(xs) == 0:
		d.Answer = "Nothing of " + f.Database + " is in cache."
	default:
		d.Answer = fmt.Sprintf("%s of %s is in cache: %s hot, %s warm, %s cold.", gbText(hot+warm+cold), f.Database,
			gbText(hot), gbText(warm), gbText(cold))
	}
	d.Handover = databaseMemoryHandover(d)
	return d
}

func databaseMemoryHandover(d DatabaseMemory) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s in SQL Server's cache, read %s UTC\n\n%s %s\n", d.Database, d.TakenAt.UTC().Format("2006-01-02 15:04"), d.Answer, d.ColdNote)
	if d.UsageNote != "" {
		fmt.Fprintf(&b, "%s\n", d.UsageNote)
	}
	b.WriteString("\nLargest in cache\n")
	for i, r := range d.Rows {
		if i == 20 {
			fmt.Fprintf(&b, "- and %d more\n", len(d.Rows)-20)
			break
		}
		fmt.Fprintf(&b, "- %s · %s: %.0f MB of %.0f MB cached (%.0f%%), %s", r.Table, r.Index, r.CachedMB, r.TotalMB, r.CachedPct, r.Temperature)
		if len(r.Flags) > 0 {
			fmt.Fprintf(&b, "; %s", strings.Join(r.Flags, " "))
		}
		b.WriteString("\n")
	}
	if d.Rest != nil {
		fmt.Fprintf(&b, "- %d more indexes: %.0f MB\n", d.Rest.Indexes, d.Rest.CachedMB)
	}
	return strings.TrimRight(b.String(), "\n")
}

// DatabaseMemoryPage is what Database detail gets: the snapshot shown (nil when the database's cache was
// never read) and the list of its saved snapshots.
type DatabaseMemoryPage struct {
	Detail     *DatabaseMemory      `json:"detail"`
	SnapshotID int64                `json:"snapshotId"`
	Snapshots  []MemorySnapshotInfo `json:"snapshots"`
	ServerName string               `json:"serverName"`
}
