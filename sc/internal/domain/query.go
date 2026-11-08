package domain

import (
	"encoding/hex"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Investigate a query (docs/phase4c-contract.md, part 1): everything kakapo reads about one query
// shape of a QRY01 finding, by its query hash, and the plain sentences that say what it means.

// Query detail sources.
const (
	QueryDetailFromQueryStore = "query-store"
	QueryDetailFromPlanCache  = "plan-cache"
	QueryDetailFromNothing    = "none"
)

// ErrBadQueryHash is a query hash that is not 0x and 16 hex digits.
var ErrBadQueryHash = errors.New("a query hash is 0x followed by 16 hexadecimal digits, as the finding shows it")

// ParseQueryHash checks a query hash as a QRY01 finding shows it ("0x9F3A..."), and returns it in
// upper case as SQL Server writes it, with its 8 bytes.
func ParseQueryHash(s string) (string, []byte, error) {
	s = strings.TrimSpace(s)
	if len(s) != 18 || !strings.EqualFold(s[:2], "0x") {
		return "", nil, ErrBadQueryHash
	}
	b, err := hex.DecodeString(s[2:])
	if err != nil {
		return "", nil, ErrBadQueryHash
	}
	return "0x" + strings.ToUpper(s[2:]), b, nil
}

// QueryFacts is what collect.QueryDetail read about one query shape in one database. Times of
// work are in microseconds and totals over the window (Query Store: 7 days; plan cache: since
// the plan was cached), as both sources keep them; reads are 8 KB pages. Times are UTC. An area
// that could not be read carries its reason in its error field.
type QueryFacts struct {
	Database         string
	QueryHash        string
	ServerName       string
	ProductVersion   string
	ServerNow        time.Time // UTC
	UTCOffsetMinutes int       // the server's offset, to say times as the server shows them

	QueryStoreState string // actual_state_desc; "" when it could not be read
	QueryStoreError string
	Text            string // the statement text (Query Store, else the plan cache)
	ObjectName      string // schema.procedure; "" for ad hoc
	Plans           []QueryStorePlanFact

	HistoryError string
	History      []QueryHourFact

	WaitsError string
	Waits      []QueryWaitFact

	PlanCacheError string
	CachedPlans    []CachedPlanFact

	StatementsError string
	Statements      []ProcStatementFact

	TablesError string
	Tables      []QueryTableFact

	// The foreign keys that reference a table the plans delete from or update and whose referencing
	// columns lead no index (collect.ReadUnindexedForeignKeys); ForeignKeysError says why they could
	// not be read.
	UnindexedForeignKeys []UnindexedForeignKeyFact
	ForeignKeysError     string

	// The tables that only grow that nothing cleans up (GrowingCandidates, read beside the query):
	// the page says "archive before an index" when the query reads much of one. nil when they or
	// kakapo's archive jobs could not be read.
	GrowingCandidates []GrowingTable
}

// UnindexedForeignKeyFact is an enabled foreign key of Schema.Table (the referencing table, with its
// rows) on Columns, in the key's order, to RefSchema.RefTable, where no enabled index of the
// referencing table has those columns, in any order, as its first key columns.
type UnindexedForeignKeyFact struct {
	Name      string
	Schema    string
	Table     string
	Columns   []string
	RefSchema string
	RefTable  string
	Rows      int64
	// RefObjectID is the referenced table's object id; RefDeletes the rows deleted from it in the
	// Design card's window (sys.dm_db_index_operational_stats leaf_delete_count plus leaf_ghost_count of
	// its heap or clustered index), -1 when not read (docs/phase5n-contract.md, section 2).
	RefObjectID int64
	RefDeletes  int64
	// OnDelete and OnUpdate are the key's referential actions: NO_ACTION, CASCADE, SET_NULL, SET_DEFAULT.
	OnDelete string
	OnUpdate string
}

// QueryStorePlanFact is one plan of the query in Query Store, with its runtime statistics over the
// last 7 days.
type QueryStorePlanFact struct {
	PlanID             int64
	QueryID            int64
	Forced             bool
	InitialCompile     *time.Time
	LastCompile        *time.Time
	LastRun            *time.Time
	EngineVersion      string
	Compat             int
	PlanHash           string // 0x...
	ForceFailures      int64
	ForceFailureReason string
	Runs               int64
	CPU                int64 // total, microseconds
	MaxCPU             int64
	Duration           int64 // total, microseconds
	MaxDuration        int64
	Reads              int64 // total pages
	MaxReads           int64
	TempdbKB           int64 // total; -1 when the version keeps no TempDB figure
	PlanChars          int64
	Plan               string // the plan XML; "" when not read or too large
}

// CachedPlanFact is one plan of the query in the plan cache, with its figures since it was cached.
type CachedPlanFact struct {
	PlanHandle  string // 0x...
	PlanHash    string // 0x...
	Runs        int64
	CPU         int64
	MaxCPU      int64
	Duration    int64
	MaxDuration int64
	Reads       int64
	MinReads    int64
	MaxReads    int64
	Created     time.Time
	LastRun     time.Time
	Text        string
	PlanChars   int64
	Plan        string
}

// QueryHourFact is one plan's runs in one hour (UTC, on the hour) from Query Store.
type QueryHourFact struct {
	Hour     time.Time
	PlanID   int64
	Runs     int64
	CPU      int64 // microseconds
	Duration int64 // microseconds
	Reads    int64
}

// QueryWaitFact is the query's waiting in one Query Store wait category over 7 days.
type QueryWaitFact struct {
	Category string // wait_category_desc
	WaitMs   int64
}

// ProcStatementFact is one statement of the procedure the query belongs to, from the plan cache
// (InvestigateProcedure.sql, part 2).
type ProcStatementFact struct {
	Line       int
	Text       string
	QueryHash  string
	Runs       int64
	MinCPU     int64
	MaxCPU     int64
	CPU        int64
	MinReads   int64
	MaxReads   int64
	Reads      int64
	Created    time.Time
	LastRun    time.Time
	Conversion bool // its plan has an implicit conversion that stops a seek
}

// QueryTableFact is one table the query's plans touch, with its size and statistics age.
type QueryTableFact struct {
	Schema        string
	Table         string
	Found         bool
	Rows          int64
	Stats         int
	OldestUpdate  *time.Time // server time, as SQL Server keeps it
	MaxModPct     float64    // the highest modification_counter * 100 / rows of its statistics
	Modifications int64
	Updates       []QueryStatUpdateFact // its statistics updated in the last 8 days, oldest first
}

// QueryStatUpdateFact is one statistics object of a table and when it was last updated (phase 5j).
type QueryStatUpdateFact struct {
	Name    string
	Index   bool      // an index's own statistics
	Updated time.Time // server time, as SQL Server keeps it
	Rows    int64
	Sampled int64 // rows read; Rows when every row was read
}

// QueryDetail is the query's page (GET .../queries/{hash}).
type QueryDetail struct {
	Database         string          `json:"database"`
	QueryHash        string          `json:"queryHash"`
	ServerName       string          `json:"serverName"`
	CapturedAt       time.Time       `json:"capturedAt"`
	UTCOffsetMinutes int             `json:"utcOffsetMinutes"` // the server's offset: the page says every time on the server's clock
	Text             string          `json:"text"`
	ObjectName       string          `json:"objectName"` // "" for ad hoc
	Source           string          `json:"source"`     // query-store, plan-cache, none
	QueryStoreState  string          `json:"queryStoreState"`
	Summary          QuerySummary    `json:"summary"`
	Verdict          *QueryVerdict   `json:"verdict"` // phase 5j; nil when it ran one way all week
	Events           []QueryEvent    `json:"events"`  // plans begun and statistics updated in the window, oldest first
	Explain          []ExplainLine   `json:"explain"`
	Plans            []QueryPlan     `json:"plans"`
	History          []QueryHour     `json:"history"`
	Waits            []QueryWait     `json:"waits"`
	Statements       []ProcStatement `json:"statements"`
	Tables           []QueryTable    `json:"tables"`
	Notes            []string        `json:"notes"` // what could not be read, and why
	Handover         string          `json:"handover"`
}

// QuerySummary is the query's numbers over the window, every plan together.
type QuerySummary struct {
	Window        string     `json:"window"` // "7 days" | "since 2 Nov 10:15"
	Runs          int64      `json:"runs"`
	AvgDurationMs float64    `json:"avgDurationMs"`
	MaxDurationMs float64    `json:"maxDurationMs"`
	AvgCPUMs      float64    `json:"avgCpuMs"`
	AvgReads      float64    `json:"avgReads"`
	MaxReads      int64      `json:"maxReads"`
	LastRun       *time.Time `json:"lastRun"`
}

// ExplainLine is one plain sentence about the query; Action, when set, is the fix it names.
type ExplainLine struct {
	Text   string       `json:"text"`
	Action *QueryAction `json:"action,omitempty"`
}

// QueryAction opens the fix dialog: the fix, its title, and the options its preview needs.
type QueryAction struct {
	FixID   string            `json:"fixId"`
	Label   string            `json:"label"` // the button
	Title   string            `json:"title"` // the dialog
	Options map[string]string `json:"options"`
}

// QueryPlan is one plan of the query: from Query Store, or only in the plan cache.
type QueryPlan struct {
	PlanID             int64                `json:"planId"`  // 0 when only in the plan cache
	QueryID            int64                `json:"queryId"` // Query Store's query id; 0 in the plan cache
	PlanHash           string               `json:"planHash"`
	PlanHandle         string               `json:"planHandle"`             // "" when not in the plan cache
	CachedCopies       int                  `json:"cachedCopies,omitempty"` // a plan only in the plan cache: its entries there (one per set of SET options); 0 otherwise
	Source             string               `json:"source"`                 // query-store, plan-cache
	Forced             bool                 `json:"forced"`
	Active             bool                 `json:"active"`  // the plan used most recently
	Fastest            bool                 `json:"fastest"` // the lowest average duration, of two or more that ran
	LastCompile        *time.Time           `json:"lastCompile"`
	LastRun            *time.Time           `json:"lastRun"`
	FirstRun           *time.Time           `json:"firstRun"` // its first run in the window (phase 5j); Query Store only
	EngineVersion      string               `json:"engineVersion"`
	CompatibilityLevel int                  `json:"compatibilityLevel"`
	Runs               int64                `json:"runs"`
	AvgCPUMs           float64              `json:"avgCpuMs"`
	MaxCPUMs           float64              `json:"maxCpuMs"`
	AvgDurationMs      float64              `json:"avgDurationMs"`
	MaxDurationMs      float64              `json:"maxDurationMs"`
	AvgReads           float64              `json:"avgReads"`
	MaxReads           int64                `json:"maxReads"`
	TempdbMBPerRun     *float64             `json:"tempdbMbPerRun"` // null when the source keeps no TempDB figure
	ForceFailures      int64                `json:"forceFailures"`
	ForceFailureReason string               `json:"forceFailureReason"`
	PlanTooBig         bool                 `json:"planTooBig"`
	Conversion         string               `json:"conversion"` // the expression of an implicit conversion that stops a seek
	Warnings           []string             `json:"warnings"`
	Parameters         []QueryPlanParameter `json:"parameters"`
	MissingIndexes     []QueryMissingIndex  `json:"missingIndexes"`
	PlanXML            string               `json:"planXml,omitempty"` // to save as .sqlplan; the first plans only
	Actions            []QueryAction        `json:"actions"`
}

// QueryPlanParameter is one parameter of the plan's <ParameterList>.
type QueryPlanParameter struct {
	Name          string `json:"name"`
	DataType      string `json:"dataType"`
	CompiledValue string `json:"compiledValue"`
	RuntimeValue  string `json:"runtimeValue"` // only in an actual plan
}

// QueryMissingIndex is a plan's <MissingIndexes> hint, in the columns' form of WRK03
// (sys.dm_db_missing_index_details): Table "[db].[schema].[table]", columns "[A], [B]".
type QueryMissingIndex struct {
	Table      string       `json:"table"`
	TableName  string       `json:"tableName"` // schema.table
	Equality   string       `json:"equality"`
	Inequality string       `json:"inequality"`
	Included   string       `json:"included"`
	Impact     float64      `json:"impact"` // percent
	Action     *QueryAction `json:"action,omitempty"`
}

// QueryHour is one hour of the query's history, newest first.
type QueryHour struct {
	Hour          time.Time `json:"hour"` // UTC
	Runs          int64     `json:"runs"`
	CPUMs         float64   `json:"cpuMs"`
	DurationMs    float64   `json:"durationMs"`
	AvgDurationMs float64   `json:"avgDurationMs"`
	Reads         int64     `json:"reads"`
	Plans         []int64   `json:"plans"`
}

// QueryWait is one wait category of the query over 7 days.
type QueryWait struct {
	Category string  `json:"category"`
	Words    string  `json:"words"`
	WaitMs   int64   `json:"waitMs"`
	Pct      float64 `json:"pct"` // of all its waiting
}

// ProcStatement is one statement of the procedure, most reads first.
type ProcStatement struct {
	Line      int       `json:"line"`
	Text      string    `json:"text"`
	Runs      int64     `json:"runs"`
	MinCPUMs  float64   `json:"minCpuMs"`
	AvgCPUMs  float64   `json:"avgCpuMs"`
	MaxCPUMs  float64   `json:"maxCpuMs"`
	MinReads  int64     `json:"minReads"`
	AvgReads  int64     `json:"avgReads"`
	MaxReads  int64     `json:"maxReads"`
	PctReads  float64   `json:"pctReads"` // of the procedure's reads
	ThisQuery bool      `json:"thisQuery"`
	LookAt    []string  `json:"lookAt"`
	LastRun   time.Time `json:"lastRun"`
}

// QueryTable is one table of this database the query's plans touch.
type QueryTable struct {
	Schema         string       `json:"schema"`
	Table          string       `json:"table"`
	Name           string       `json:"name"` // schema.table
	Found          bool         `json:"found"`
	Rows           int64        `json:"rows"`
	Statistics     int          `json:"statistics"`
	OldestUpdate   *time.Time   `json:"oldestUpdate"` // server time
	AgeDays        *int         `json:"ageDays"`
	MaxModifiedPct float64      `json:"maxModifiedPct"`
	Stale          bool         `json:"stale"`
	Indexes        []string     `json:"indexes"` // the indexes the plans use
	Action         *QueryAction `json:"action,omitempty"`
}

// ShowplanObject is a table (or index) a plan touches, from its <Object> nodes, brackets removed.
type ShowplanObject struct {
	Database string
	Schema   string
	Table    string
	Index    string
}

// Showplan is what kakapo reads from a plan's XML besides PlanWarnings: the compiled parameters,
// the missing index hints and the objects touched.
type Showplan struct {
	Parameters     []QueryPlanParameter
	MissingIndexes []QueryMissingIndex
	Objects        []ShowplanObject
	// Modified are the tables a DELETE or UPDATE operator changes (the <Object> right under its
	// <Update> or <SimpleUpdate>, the RelOp's LogicalOp Delete or Update), once per table and kind;
	// Read are the tables the plan reads, every other <Object>, once per table (Index left empty).
	// A foreign key check is a read of the referencing table in the plan of a DELETE or of an UPDATE
	// of the referenced key (seen live: an UPDATE of other columns reads no referencing table).
	Modified []ShowplanModified
	Read     []ShowplanObject
}

// ShowplanModified is a table a plan deletes from or updates: Kind "DELETE" or "UPDATE".
type ShowplanModified struct {
	Kind string
	ShowplanObject
}

// ParseShowplan reads a plan's XML as a stream (plans can be large): <ParameterList>'s
// ColumnReference attributes, <MissingIndexes> (group impact, table, columns by usage) and every
// <Object>. A plan that does not parse to its end gives what was read up to the error.
func ParseShowplan(plan string) Showplan {
	out := Showplan{Parameters: []QueryPlanParameter{}, MissingIndexes: []QueryMissingIndex{}, Objects: []ShowplanObject{}, Modified: []ShowplanModified{}, Read: []ShowplanObject{}}
	if strings.TrimSpace(plan) == "" {
		return out
	}
	dec := xml.NewDecoder(strings.NewReader(plan))
	// The text is already UTF-8 here, whatever the declaration says (utf-16 from SQL Server).
	dec.CharsetReader = func(_ string, r io.Reader) (io.Reader, error) { return r, nil }
	var (
		stack   []string
		impact  float64
		usage   string
		current *QueryMissingIndex
		cols    map[string][]string
		params  = map[string]bool{}
		mis     = map[string]bool{}
		objects = map[string]bool{}
		relOps  []string // the LogicalOp of each open <RelOp>
		read    = map[string]bool{}
		changed = map[string]bool{}
	)
	attr := func(e xml.StartElement, name string) string {
		for _, a := range e.Attr {
			if a.Name.Local == name {
				return a.Value
			}
		}
		return ""
	}
	for {
		tok, err := dec.Token()
		if err != nil {
			break
		}
		switch t := tok.(type) {
		case xml.StartElement:
			parent := ""
			if len(stack) > 0 {
				parent = stack[len(stack)-1]
			}
			name := t.Name.Local
			stack = append(stack, name)
			switch name {
			case "RelOp":
				relOps = append(relOps, attr(t, "LogicalOp"))
			case "ColumnReference":
				if parent != "ParameterList" {
					continue
				}
				p := QueryPlanParameter{Name: attr(t, "Column"), DataType: attr(t, "ParameterDataType"),
					CompiledValue: qParamValue(attr(t, "ParameterCompiledValue")), RuntimeValue: qParamValue(attr(t, "ParameterRuntimeValue"))}
				if p.Name != "" && !params[p.Name] {
					params[p.Name] = true
					out.Parameters = append(out.Parameters, p)
				}
			case "MissingIndexGroup":
				impact, _ = strconv.ParseFloat(attr(t, "Impact"), 64)
			case "MissingIndex":
				current = &QueryMissingIndex{Table: attr(t, "Database") + "." + attr(t, "Schema") + "." + attr(t, "Table"),
					TableName: qUnbracket(attr(t, "Schema")) + "." + qUnbracket(attr(t, "Table")), Impact: impact}
				cols = map[string][]string{}
			case "ColumnGroup":
				usage = strings.ToUpper(attr(t, "Usage"))
			case "Column":
				if current != nil && parent == "ColumnGroup" {
					cols[usage] = append(cols[usage], attr(t, "Name"))
				}
			case "Object":
				o := ShowplanObject{Database: qUnbracket(attr(t, "Database")), Schema: qUnbracket(attr(t, "Schema")),
					Table: qUnbracket(attr(t, "Table")), Index: qUnbracket(attr(t, "Index"))}
				key := strings.ToLower(o.Database + "|" + o.Schema + "|" + o.Table + "|" + o.Index)
				if o.Table != "" && !objects[key] {
					objects[key] = true
					out.Objects = append(out.Objects, o)
				}
				if o.Table == "" {
					continue
				}
				table := ShowplanObject{Database: o.Database, Schema: o.Schema, Table: o.Table}
				tkey := strings.ToLower(o.Database + "|" + o.Schema + "|" + o.Table)
				logical := ""
				if len(relOps) > 0 {
					logical = relOps[len(relOps)-1]
				}
				switch {
				case (parent == "Update" || parent == "SimpleUpdate") && (logical == "Delete" || logical == "Update"):
					kind := strings.ToUpper(logical)
					if !changed[kind+"|"+tkey] {
						changed[kind+"|"+tkey] = true
						out.Modified = append(out.Modified, ShowplanModified{Kind: kind, ShowplanObject: table})
					}
				case parent == "Update" || parent == "SimpleUpdate":
					// an insert's target is neither a change of a key nor a read
				case !read[tkey]:
					read[tkey] = true
					out.Read = append(out.Read, table)
				}
			}
		case xml.EndElement:
			if len(stack) > 0 {
				stack = stack[:len(stack)-1]
			}
			if t.Name.Local == "RelOp" && len(relOps) > 0 {
				relOps = relOps[:len(relOps)-1]
			}
			if t.Name.Local == "MissingIndex" && current != nil {
				current.Equality = strings.Join(cols["EQUALITY"], ", ")
				current.Inequality = strings.Join(cols["INEQUALITY"], ", ")
				current.Included = strings.Join(cols["INCLUDE"], ", ")
				key := strings.ToLower(current.Table + "|" + current.Equality + "|" + current.Inequality + "|" + current.Included)
				if !mis[key] {
					mis[key] = true
					out.MissingIndexes = append(out.MissingIndexes, *current)
				}
				current = nil
			}
		}
	}
	return out
}

// qParamValue writes a compiled or runtime value as SQL Server keeps it, without the parentheses
// it puts around numbers: "(5)" -> "5", "'2019-01-01'" stays.
func qParamValue(v string) string {
	v = strings.TrimSpace(v)
	if len(v) >= 2 && v[0] == '(' && v[len(v)-1] == ')' {
		return v[1 : len(v)-1]
	}
	return v
}

// qUnbracket removes T-SQL brackets: "[a]]b]" -> "a]b".
func qUnbracket(s string) string {
	if len(s) >= 2 && s[0] == '[' && s[len(s)-1] == ']' {
		return strings.ReplaceAll(s[1:len(s)-1], "]]", "]")
	}
	return s
}

// ShowplanTables are the tables of this database that the plans touch, once each, with the
// indexes used on them: no temporary tables, table variables, worktables or system tables. A plan
// compiled before the database was restored or renamed names the database it had then (phase 5j):
// when none of its objects names this database and they all name one other, that one is this.
func ShowplanTables(database string, plans ...string) []ShowplanObject {
	out := []ShowplanObject{}
	at := map[string]int{}
	for _, p := range plans {
		objs := ParseShowplan(p).Objects
		here := qPlanDatabase(database, objs)
		for _, o := range objs {
			if !qShowplanTableOf(here, o) {
				continue
			}
			key := strings.ToLower(o.Schema + "." + o.Table)
			i, ok := at[key]
			if !ok {
				i = len(out)
				at[key] = i
				out = append(out, ShowplanObject{Database: database, Schema: o.Schema, Table: o.Table})
			}
			if o.Index != "" && !qContainsFold(strings.Split(out[i].Index, "\n"), o.Index) {
				if out[i].Index != "" {
					out[i].Index += "\n"
				}
				out[i].Index += o.Index
			}
		}
	}
	return out
}

// ShowplanDMLTargets are the tables of this database the plans delete from or update, once per
// table and kind, as ShowplanTables names them (Database this database).
func ShowplanDMLTargets(database string, plans ...string) []ShowplanModified {
	out := []ShowplanModified{}
	seen := map[string]bool{}
	for _, p := range plans {
		sp := ParseShowplan(p)
		here := qPlanDatabase(database, sp.Objects)
		for _, m := range sp.Modified {
			key := strings.ToLower(m.Kind + "|" + m.Schema + "|" + m.Table)
			if !qShowplanTableOf(here, m.ShowplanObject) || seen[key] {
				continue
			}
			seen[key] = true
			out = append(out, ShowplanModified{Kind: m.Kind, ShowplanObject: ShowplanObject{Database: database, Schema: m.Schema, Table: m.Table}})
		}
	}
	return out
}

// qPlansRead says whether the plans read schema.table of this database.
func qPlansRead(database, schema, table string, plans []string) bool {
	for _, p := range plans {
		sp := ParseShowplan(p)
		here := qPlanDatabase(database, sp.Objects)
		for _, o := range sp.Read {
			if qShowplanTableOf(here, o) && strings.EqualFold(o.Schema, schema) && strings.EqualFold(o.Table, table) {
				return true
			}
		}
	}
	return false
}

// qPlanDatabase is the name this database has in a plan's objects: its own name when one object
// names it, else the one other database every user table of the plan names (a restored copy).
func qPlanDatabase(database string, objs []ShowplanObject) string {
	other := ""
	for _, o := range objs {
		if o.Database == "" || !qShowplanTableOf(o.Database, o) {
			continue
		}
		switch {
		case strings.EqualFold(o.Database, database):
			return database
		case strings.EqualFold(o.Database, "tempdb"):
		case other == "":
			other = o.Database
		case !strings.EqualFold(other, o.Database):
			return database
		}
	}
	if other == "" {
		return database
	}
	return other
}

func qShowplanTableOf(database string, o ShowplanObject) bool {
	switch {
	case o.Schema == "" || o.Table == "":
		return false
	case strings.HasPrefix(o.Table, "#") || strings.HasPrefix(o.Table, "@"):
		return false
	case strings.EqualFold(o.Schema, "sys"):
		return false
	case o.Database != "" && !strings.EqualFold(o.Database, database):
		return false
	}
	return true
}

func qContainsFold(list []string, s string) bool {
	for _, x := range list {
		if strings.EqualFold(x, s) {
			return true
		}
	}
	return false
}

// qQueryWaitWords are sp_QuickieStore's wait categories in the words a non-DBA reads.
var qQueryWaitWords = map[string]string{
	"CPU":               "waiting for its turn on a CPU (long runs take turns; or the server is busy)",
	"Worker Thread":     "waiting for a worker thread (the server ran out of threads)",
	"Lock":              "waiting for locks held by other sessions (blocking)",
	"Latch":             "waiting on SQL Server's internal structures (latches)",
	"Buffer Latch":      "waiting on hot data pages in memory (often TempDB or one busy table)",
	"Buffer IO":         "reading data pages from disk",
	"Compilation":       "compiling its plan",
	"SQL CLR":           "running .NET (CLR) code",
	"Mirroring":         "waiting for database mirroring",
	"Transaction":       "on transaction bookkeeping",
	"Idle":              "idle",
	"Preemptive":        "in calls outside SQL Server (the operating system, linked servers)",
	"Service Broker":    "waiting for Service Broker",
	"Tran Log IO":       "writing to the transaction log",
	"Network IO":        "waiting for the application to read the results",
	"Parallelism":       "waiting for its parallel threads to finish (parallelism)",
	"Memory":            "waiting for memory to run (a memory grant)",
	"User Wait":         "in a WAITFOR in the code",
	"Tracing":           "waiting for traces or Extended Events",
	"Full Text Search":  "on full-text search",
	"Other Disk IO":     "on other disk reads and writes (backups, file growth)",
	"Replication":       "waiting for replication",
	"Log Rate Governor": "held back by the log rate limit",
}

// QueryWaitWords says a Query Store wait category in plain words.
func QueryWaitWords(category string) string {
	for k, v := range qQueryWaitWords {
		if strings.EqualFold(k, category) {
			return v
		}
	}
	return strings.ToLower(category)
}

// Thresholds of the query page.
const (
	queryStaleDays       = 7  // statistics older than this with qStaleChangedPct changed are stale
	queryStaleChangedPct = 10 // percent of rows changed
	queryVeryChangedPct  = 20 // stale at any age
	queryStaleMinRows    = 1000
	queryRegressionRatio = 2.0 // the plan in use is this many times slower than the fastest
	// queryForceMinRuns: a faster plan with fewer runs than this may only have met favourable values;
	// it keeps its Force button, but the sentence asks for more runs before forcing it.
	queryForceMinRuns = 10
	// queryWorkRatio: the faster plan does less work when its CPU or its pages read per run are this
	// many times lower; either is enough (a plan may trade a little CPU for far fewer reads).
	queryWorkRatio  = 1.5
	queryPlanXMLMax = 5 // plans whose XML the page carries (Save .sqlplan)
	// queryParallelRatio: a plan whose CPU per run is this many times its time per run runs in parallel (5j).
	queryParallelRatio  = 1.5
	queryLargeTableRows = 10_000_000
)

// BuildQueryDetail builds the query's page from what was read. canChange says whether the
// database can be changed from here (online, not read-only, not a secondary); when it cannot, the
// database fixes are not offered.
func BuildQueryDetail(f QueryFacts, canChange bool) QueryDetail {
	d := QueryDetail{
		Database: f.Database, QueryHash: f.QueryHash, ServerName: f.ServerName, CapturedAt: f.ServerNow, UTCOffsetMinutes: f.UTCOffsetMinutes,
		Text: f.Text, ObjectName: f.ObjectName, QueryStoreState: f.QueryStoreState,
		Explain: []ExplainLine{}, Plans: []QueryPlan{}, History: []QueryHour{}, Waits: []QueryWait{},
		Statements: []ProcStatement{}, Tables: []QueryTable{}, Notes: []string{},
	}
	qsOn := qQueryStoreOn(f.QueryStoreState)

	// Plans: Query Store's, each with its plan handle when the plan cache holds the same plan;
	// then the cached plans Query Store does not have. The cache's entries of one plan hash are one
	// plan (qMergeCachedPlans).
	cached := qMergeCachedPlans(f.CachedPlans)
	cachedByHash := map[string]cachedPlan{}
	for _, c := range cached {
		if c.PlanHash != "" {
			cachedByHash[strings.ToUpper(c.PlanHash)] = c
		}
	}
	usedCache := map[string]bool{}
	qsWithRuns := 0
	for _, p := range f.Plans {
		qp := qQueryPlanFromStore(p)
		if c, ok := cachedByHash[strings.ToUpper(p.PlanHash)]; ok {
			qp.PlanHandle = c.PlanHandle
			usedCache[c.PlanHandle] = true
		}
		if p.Runs > 0 {
			qsWithRuns++
		}
		d.Plans = append(d.Plans, qp)
	}
	for _, c := range cached {
		if usedCache[c.PlanHandle] {
			continue
		}
		qp := qQueryPlanFromCache(c.CachedPlanFact)
		qp.CachedCopies = c.copies
		d.Plans = append(d.Plans, qp)
	}
	switch {
	case len(f.Plans) > 0:
		d.Source = QueryDetailFromQueryStore
	case len(f.CachedPlans) > 0:
		d.Source = QueryDetailFromPlanCache
	default:
		d.Source = QueryDetailFromNothing
	}
	if d.Text == "" {
		for _, c := range f.CachedPlans {
			if c.Text != "" {
				d.Text = c.Text
				break
			}
		}
	}

	// The active plan (used most recently) and the fastest (lowest average duration, of two or
	// more that ran in the window), within the source the page reads from.
	inSource := func(p QueryPlan) bool {
		return d.Source != QueryDetailFromQueryStore || p.Source == QueryDetailFromQueryStore
	}
	active, fastest, ran := -1, -1, 0
	for i, p := range d.Plans {
		if !inSource(p) || p.Runs == 0 {
			continue
		}
		ran++
		if active < 0 || qLaterThan(p.LastRun, d.Plans[active].LastRun) {
			active = i
		}
		if fastest < 0 || p.AvgDurationMs < d.Plans[fastest].AvgDurationMs {
			fastest = i
		}
	}
	if active >= 0 {
		d.Plans[active].Active = true
	}
	if ran >= 2 && fastest >= 0 {
		d.Plans[fastest].Fastest = true
	}
	sort.SliceStable(d.Plans, func(i, j int) bool {
		a, b := d.Plans[i], d.Plans[j]
		if a.Active != b.Active {
			return a.Active
		}
		if a.Forced != b.Forced {
			return a.Forced
		}
		return qLaterThan(a.LastRun, b.LastRun)
	})

	// What each plan says, and its actions.
	for i := range d.Plans {
		p := &d.Plans[i]
		if i < queryPlanXMLMax {
			p.PlanXML = qPlanXMLOf(f, *p)
		}
		xmlText := qPlanXMLOf(f, *p)
		if !p.PlanTooBig && xmlText != "" {
			p.Conversion, p.Warnings = PlanWarnings(xmlText)
			sp := ParseShowplan(xmlText)
			p.Parameters = sp.Parameters
			for _, mi := range sp.MissingIndexes {
				table, here := qThisDatabaseTable(mi.Table, f.Database, sp)
				if canChange && here {
					mi.Table = table
					mi.Action = qCreateIndexAction(mi, p.PlanID)
				}
				p.MissingIndexes = append(p.MissingIndexes, mi)
			}
		}
		if p.Source == QueryDetailFromQueryStore && qsOn && canChange {
			switch {
			case p.Forced:
				p.Actions = append(p.Actions, qUnforceAction(p.PlanID))
			case qsWithRuns >= 2:
				p.Actions = append(p.Actions, qForceAction(p.PlanID))
			}
		}
		if p.PlanHandle != "" {
			p.Actions = append(p.Actions, qEvictAction(p.PlanHandle, f.QueryHash))
		}
	}

	d.Summary = qQuerySummary(f, d)
	d.History = qQueryHistory(f.History)
	d.Waits = qQueryWaits(f.Waits)
	d.Statements = qProcStatements(f.Statements, f.QueryHash)
	d.Tables = qQueryTables(f, d.Plans, canChange)
	if d.Source == QueryDetailFromQueryStore {
		qPlanFirstRuns(f, d.Plans)
		d.Events = qQueryEvents(f, d)
		d.Verdict = qQueryVerdict(f, d)
	} else {
		d.Events = []QueryEvent{}
	}
	d.Waits = qShownWaits(d.Waits)

	for _, n := range []struct{ what, err string }{
		{"Query Store", f.QueryStoreError}, {"History", f.HistoryError}, {"Waits", f.WaitsError},
		{"Plan cache", f.PlanCacheError}, {"Statements of the procedure", f.StatementsError}, {"Tables", f.TablesError}, {"Foreign keys", f.ForeignKeysError},
	} {
		if n.err != "" {
			d.Notes = append(d.Notes, n.what+": could not be read: "+n.err)
		}
	}

	d.Explain = qExplainLines(f, d, canChange)
	d.Handover = QueryHandover(d)
	return d
}

func qQueryStoreOn(state string) bool {
	return strings.EqualFold(state, "READ_WRITE") || strings.EqualFold(state, "READ_ONLY")
}

func qLaterThan(a, b *time.Time) bool {
	switch {
	case a == nil:
		return false
	case b == nil:
		return true
	}
	return a.After(*b)
}

func qPerRunMs(totalMicro, runs int64) float64 {
	if runs <= 0 {
		return 0
	}
	return roundHalfAway(float64(totalMicro)/float64(runs)/1000, 1)
}

func qPerRun(total, runs int64) float64 {
	if runs <= 0 {
		return 0
	}
	return roundHalfAway(float64(total)/float64(runs), 0)
}

func qQueryPlanFromStore(p QueryStorePlanFact) QueryPlan {
	qp := QueryPlan{
		PlanID: p.PlanID, QueryID: p.QueryID, PlanHash: p.PlanHash, Source: QueryDetailFromQueryStore, Forced: p.Forced,
		LastCompile: p.LastCompile, LastRun: p.LastRun, EngineVersion: p.EngineVersion, CompatibilityLevel: p.Compat,
		Runs: p.Runs, AvgCPUMs: qPerRunMs(p.CPU, p.Runs), MaxCPUMs: roundHalfAway(float64(p.MaxCPU)/1000, 1),
		AvgDurationMs: qPerRunMs(p.Duration, p.Runs), MaxDurationMs: roundHalfAway(float64(p.MaxDuration)/1000, 1),
		AvgReads: qPerRun(p.Reads, p.Runs), MaxReads: p.MaxReads,
		ForceFailures: p.ForceFailures, ForceFailureReason: p.ForceFailureReason,
		PlanTooBig: p.PlanChars > qPlanLimitChars, Warnings: []string{}, Parameters: []QueryPlanParameter{},
		MissingIndexes: []QueryMissingIndex{}, Actions: []QueryAction{},
	}
	if p.TempdbKB >= 0 && p.Runs > 0 {
		v := roundHalfAway(float64(p.TempdbKB)/1024/float64(p.Runs), 1)
		qp.TempdbMBPerRun = &v
	}
	return qp
}

func qQueryPlanFromCache(c CachedPlanFact) QueryPlan {
	last, created := c.LastRun, c.Created
	return QueryPlan{
		PlanHash: c.PlanHash, PlanHandle: c.PlanHandle, Source: QueryDetailFromPlanCache,
		LastCompile: &created, LastRun: &last,
		Runs: c.Runs, AvgCPUMs: qPerRunMs(c.CPU, c.Runs), MaxCPUMs: roundHalfAway(float64(c.MaxCPU)/1000, 1),
		AvgDurationMs: qPerRunMs(c.Duration, c.Runs), MaxDurationMs: roundHalfAway(float64(c.MaxDuration)/1000, 1),
		AvgReads: qPerRun(c.Reads, c.Runs), MaxReads: c.MaxReads,
		PlanTooBig: c.PlanChars > qPlanLimitChars, Warnings: []string{}, Parameters: []QueryPlanParameter{},
		MissingIndexes: []QueryMissingIndex{}, Actions: []QueryAction{},
	}
}

// cachedPlan is one plan of the plan cache: its entries of one plan hash taken together.
type cachedPlan struct {
	CachedPlanFact
	copies int // the cache entries it was read from
}

// qMergeCachedPlans takes the plan cache's entries of one plan hash as one plan. SQL Server caches
// a plan once for each set of SET options its connections use (Microsoft Learn, "Execution plan
// caching and reuse": the SET options are part of a plan's cache key; sys.dm_exec_plan_attributes
// set_options), so two entries with the same plan hash are the same plan cached twice, not two
// plans: their runs and totals are added, the maxima and first and last times kept, and the
// handle, text and plan XML are the most recently used entry's that has its XML. An entry without a
// plan hash stays on its own. The order is the facts' (most recently used first).
func qMergeCachedPlans(cs []CachedPlanFact) []cachedPlan {
	out := []cachedPlan{}
	at := map[string]int{}
	for _, c := range cs {
		key := strings.ToUpper(c.PlanHash)
		i, ok := at[key]
		if !ok || key == "" {
			if key != "" {
				at[key] = len(out)
			}
			out = append(out, cachedPlan{CachedPlanFact: c, copies: 1})
			continue
		}
		m := &out[i]
		m.copies++
		m.Runs += c.Runs
		m.CPU += c.CPU
		m.Duration += c.Duration
		m.Reads += c.Reads
		m.MaxCPU, m.MaxDuration, m.MaxReads = max(m.MaxCPU, c.MaxCPU), max(m.MaxDuration, c.MaxDuration), max(m.MaxReads, c.MaxReads)
		m.MinReads = min(m.MinReads, c.MinReads)
		if c.Created.Before(m.Created) {
			m.Created = c.Created
		}
		newer := c.LastRun.After(m.LastRun)
		if newer {
			m.LastRun = c.LastRun
		}
		if m.Plan == "" && c.Plan != "" || newer && c.Plan != "" {
			m.PlanHandle, m.Plan, m.PlanChars = c.PlanHandle, c.Plan, c.PlanChars
			if c.Text != "" {
				m.Text = c.Text
			}
		}
	}
	return out
}

// qPlanLimitChars is the plan size kakapo still reads for warnings (2 MB of text), as collect
// reads it.
const qPlanLimitChars = 2 * 1024 * 1024

// qPlanXMLOf is the plan XML that was read for a plan: Query Store's, else the cached plan's.
func qPlanXMLOf(f QueryFacts, p QueryPlan) string {
	if p.Source == QueryDetailFromQueryStore {
		for _, x := range f.Plans {
			if x.PlanID == p.PlanID {
				if x.Plan != "" {
					return x.Plan
				}
				break
			}
		}
	}
	if p.PlanHandle != "" {
		for _, c := range f.CachedPlans {
			if c.PlanHandle == p.PlanHandle {
				return c.Plan
			}
		}
	}
	return ""
}

func qForceAction(planID int64) QueryAction {
	id := strconv.FormatInt(planID, 10)
	return QueryAction{FixID: "force-plan", Label: "Force this plan", Title: "Force plan " + id, Options: map[string]string{"planId": id}}
}

func qUnforceAction(planID int64) QueryAction {
	id := strconv.FormatInt(planID, 10)
	return QueryAction{FixID: "unforce-plan", Label: "Stop forcing", Title: "Stop forcing plan " + id, Options: map[string]string{"planId": id}}
}

func qEvictAction(handle, hash string) QueryAction {
	return QueryAction{FixID: "evict-plan", Label: "Remove from cache", Title: "Remove the plan from the cache",
		Options: map[string]string{"planHandle": handle, "queryHash": hash}}
}

// qThisDatabaseTable is a missing index hint's [database].[schema].[table] as this database's. A plan
// compiled before the database was restored or renamed names the database it had then (Query Store
// keeps the plan as compiled), so when none of the plan's objects names this database, the hint's
// database becomes this one. False when the hint is about another database the plan really reads:
// no index is offered there.
func qThisDatabaseTable(table, database string, sp Showplan) (string, bool) {
	i := strings.Index(table, "].")
	if !strings.HasPrefix(table, "[") || i < 0 {
		return table, true
	}
	if strings.EqualFold(qUnbracket(table[:i+1]), database) {
		return table, true
	}
	for _, o := range sp.Objects {
		if strings.EqualFold(o.Database, database) {
			return table, false
		}
	}
	return QuoteName(database) + table[i+1:], true
}

func qCreateIndexAction(mi QueryMissingIndex, planID int64) *QueryAction {
	opts := map[string]string{"source": "plan", "table": mi.Table, "equality": mi.Equality, "inequality": mi.Inequality, "included": mi.Included}
	if planID > 0 {
		opts["planId"] = strconv.FormatInt(planID, 10)
	}
	return &QueryAction{FixID: "create-index", Label: "Create index", Title: "Create the suggested index on " + mi.TableName, Options: opts}
}

func qStatsAction(schema, table string) *QueryAction {
	name := schema + "." + table
	return &QueryAction{FixID: "update-statistics-table", Label: "Update statistics", Title: "Update statistics on " + name,
		Options: map[string]string{"table": QuoteName(schema) + "." + QuoteName(table)}}
}

func qQuerySummary(f QueryFacts, d QueryDetail) QuerySummary {
	s := QuerySummary{Window: "7 days"}
	var cpu, dur, reads int64
	for _, p := range d.Plans {
		if d.Source == QueryDetailFromQueryStore && p.Source != QueryDetailFromQueryStore {
			continue
		}
		s.Runs += p.Runs
		cpu += int64(p.AvgCPUMs * 1000 * float64(p.Runs))
		dur += int64(p.AvgDurationMs * 1000 * float64(p.Runs))
		reads += int64(p.AvgReads * float64(p.Runs))
		s.MaxDurationMs = max(s.MaxDurationMs, p.MaxDurationMs)
		s.MaxReads = max(s.MaxReads, p.MaxReads)
		if qLaterThan(p.LastRun, s.LastRun) {
			s.LastRun = p.LastRun
		}
	}
	if d.Source == QueryDetailFromPlanCache {
		var first *time.Time
		for _, p := range d.Plans {
			if p.LastCompile != nil && (first == nil || p.LastCompile.Before(*first)) {
				first = p.LastCompile
			}
		}
		s.Window = "since the plan was cached"
		if first != nil {
			s.Window = "since " + qServerClock(*first, f)
		}
	}
	s.AvgCPUMs, s.AvgDurationMs, s.AvgReads = qPerRunMs(cpu, s.Runs), qPerRunMs(dur, s.Runs), qPerRun(reads, s.Runs)
	return s
}

// qServerClock writes a UTC time as the server's clock shows it: "10:15" today, "2 Nov 10:15"
// before.
func qServerClock(t time.Time, f QueryFacts) string {
	off := time.Duration(f.UTCOffsetMinutes) * time.Minute
	lt, now := t.Add(off), f.ServerNow.Add(off)
	if lt.Year() == now.Year() && lt.YearDay() == now.YearDay() {
		return lt.Format("15:04")
	}
	return lt.Format("2 Jan 15:04")
}

func qQueryHistory(hs []QueryHourFact) []QueryHour {
	byHour := map[time.Time]*QueryHour{}
	order := []time.Time{}
	for _, h := range hs {
		x, ok := byHour[h.Hour]
		if !ok {
			x = &QueryHour{Hour: h.Hour, Plans: []int64{}}
			byHour[h.Hour] = x
			order = append(order, h.Hour)
		}
		x.Runs += h.Runs
		x.CPUMs += float64(h.CPU) / 1000
		x.DurationMs += float64(h.Duration) / 1000
		x.Reads += h.Reads
		if h.PlanID > 0 && !qContainsInt(x.Plans, h.PlanID) {
			x.Plans = append(x.Plans, h.PlanID)
		}
	}
	sort.Slice(order, func(i, j int) bool { return order[i].After(order[j]) })
	out := make([]QueryHour, 0, len(order))
	for _, t := range order {
		x := byHour[t]
		x.CPUMs, x.DurationMs = roundHalfAway(x.CPUMs, 1), roundHalfAway(x.DurationMs, 1)
		if x.Runs > 0 {
			x.AvgDurationMs = roundHalfAway(x.DurationMs/float64(x.Runs), 1)
		}
		sort.Slice(x.Plans, func(i, j int) bool { return x.Plans[i] < x.Plans[j] })
		out = append(out, *x)
	}
	return out
}

func qContainsInt(list []int64, v int64) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

func qQueryWaits(ws []QueryWaitFact) []QueryWait {
	var total int64
	for _, w := range ws {
		total += w.WaitMs
	}
	out := []QueryWait{}
	for _, w := range ws {
		if w.WaitMs <= 0 {
			continue
		}
		out = append(out, QueryWait{Category: w.Category, Words: QueryWaitWords(w.Category), WaitMs: w.WaitMs,
			Pct: roundHalfAway(float64(w.WaitMs)*100/float64(total), 1)})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].WaitMs > out[j].WaitMs })
	return out
}

// qProcStatements are the procedure's statements, most reads first, with InvestigateProcedure.sql's
// "what to look at".
func qProcStatements(ss []ProcStatementFact, hash string) []ProcStatement {
	var total int64
	for _, s := range ss {
		total += s.Reads
	}
	out := []ProcStatement{}
	for _, s := range ss {
		x := ProcStatement{Line: s.Line, Text: strings.TrimSpace(s.Text), Runs: s.Runs,
			MinCPUMs: roundHalfAway(float64(s.MinCPU)/1000, 1), AvgCPUMs: qPerRunMs(s.CPU, s.Runs), MaxCPUMs: roundHalfAway(float64(s.MaxCPU)/1000, 1),
			MinReads: s.MinReads, AvgReads: int64(qPerRun(s.Reads, s.Runs)), MaxReads: s.MaxReads,
			ThisQuery: hash != "" && strings.EqualFold(s.QueryHash, hash), LookAt: []string{}, LastRun: s.LastRun}
		if total > 0 {
			x.PctReads = roundHalfAway(float64(s.Reads)*100/float64(total), 1)
		}
		// Source: InvestigateProcedure.sql part 2, WhatToLookAt.
		if total > 0 && s.Reads*2 >= total {
			x.LookAt = append(x.LookAt, "most of the cost of this procedure")
		}
		if s.MaxReads >= 10000 && s.MaxReads >= s.MinReads*20 {
			x.LookAt = append(x.LookAt, "cost depends on the input: test with real values")
		}
		if s.Conversion {
			x.LookAt = append(x.LookAt, "implicit conversion may stop an index being used (a variable or column of a different type)")
		}
		if x.AvgReads >= 10000 {
			x.LookAt = append(x.LookAt, "reads many pages every run")
		}
		out = append(out, x)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].PctReads != out[j].PctReads {
			return out[i].PctReads > out[j].PctReads
		}
		return out[i].Line < out[j].Line
	})
	return out
}

// qQueryTables are the tables of this database the plans touch, with their size and statistics
// age, largest first; stale statistics carry the update-statistics-table action.
func qQueryTables(f QueryFacts, plans []QueryPlan, canChange bool) []QueryTable {
	indexes := map[string][]string{}
	for _, o := range ShowplanTables(f.Database, qPlanXMLs(f, plans)...) {
		if o.Index != "" {
			indexes[strings.ToLower(o.Schema+"."+o.Table)] = strings.Split(o.Index, "\n")
		}
	}
	out := []QueryTable{}
	for _, t := range f.Tables {
		x := QueryTable{Schema: t.Schema, Table: t.Table, Name: t.Schema + "." + t.Table, Found: t.Found, Rows: t.Rows,
			Statistics: t.Stats, OldestUpdate: t.OldestUpdate, MaxModifiedPct: roundHalfAway(t.MaxModPct, 1),
			Indexes: indexes[strings.ToLower(t.Schema+"."+t.Table)]}
		if x.Indexes == nil {
			x.Indexes = []string{}
		}
		if t.OldestUpdate != nil {
			days := qStatsAgeDays(*t.OldestUpdate, f)
			x.AgeDays = &days
		}
		x.Stale = qStatsStale(x)
		if t.Found && t.Stats > 0 && canChange {
			x.Action = qStatsAction(t.Schema, t.Table)
		}
		out = append(out, x)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Rows > out[j].Rows })
	return out
}

// qPlanXMLs are the XML of the plans that can be inspected.
func qPlanXMLs(f QueryFacts, plans []QueryPlan) []string {
	xmls := []string{}
	for _, p := range plans {
		if x := qPlanXMLOf(f, p); x != "" && !p.PlanTooBig {
			xmls = append(xmls, x)
		}
	}
	return xmls
}

// qArchiveAction opens the archive fix on a table that only grows, with the date column IDX10 read
// when it is indexed, as the Storage card's entry does (ArchiveFixesFor).
func qArchiveAction(t GrowingTable) *QueryAction {
	opts := map[string]string{OptionTable: archiveTableOption(t.Schema, t.Name)}
	if t.DateIndexed {
		opts[OptionArchiveDateColumn] = t.DateCol
	}
	return &QueryAction{FixID: FixArchiveTable, Label: "Archive old rows", Title: "Archive or delete old rows of " + t.Schema + "." + t.Name, Options: opts}
}

// qStatsAgeDays is how old statistics are, in whole days: the update is server time, ServerNow UTC.
func qStatsAgeDays(updated time.Time, f QueryFacts) int {
	now := f.ServerNow.Add(time.Duration(f.UTCOffsetMinutes) * time.Minute)
	d := int(now.Sub(updated).Hours() / 24)
	return max(d, 0)
}

func qStatsStale(t QueryTable) bool {
	if !t.Found || t.Statistics == 0 || t.Rows < queryStaleMinRows {
		return false
	}
	if t.MaxModifiedPct >= queryVeryChangedPct {
		return true
	}
	return t.AgeDays != nil && *t.AgeDays >= queryStaleDays && t.MaxModifiedPct >= queryStaleChangedPct
}

// QueryDuration writes a time in milliseconds for a sentence: "under 1 ms", "45 ms", "2.3 s",
// "41 s", "3.5 min".
func QueryDuration(ms float64) string {
	switch {
	case ms <= 0:
		return "0 ms"
	case ms < 1:
		return "under 1 ms"
	case ms < 1000:
		return n0(ms) + " ms"
	case ms < 10_000:
		return n1(ms/1000) + " s"
	case ms < 120_000:
		return n0(ms/1000) + " s"
	}
	return n1(ms/60_000) + " min"
}

// QueryRows writes a row count for a sentence: "980", "45,210", "4.2 M".
func QueryRows(n int64) string {
	if n < 1_000_000 {
		return n0int(n)
	}
	return n1(float64(n)/1_000_000) + " M"
}

var qCountWords = []string{"No", "One", "Two", "Three", "Four", "Five", "Six", "Seven", "Eight", "Nine"}

func qCountWord(n int) string {
	if n >= 0 && n < len(qCountWords) {
		return qCountWords[n]
	}
	return strconv.Itoa(n)
}

// ExplainQuery is the query's page in plain sentences a non-DBA can read; each sentence that has an
// action names it (ExplainLines carries the action itself).
func ExplainQuery(d QueryDetail) []string {
	out := make([]string, 0, len(d.Explain))
	for _, l := range d.Explain {
		out = append(out, l.Text)
	}
	return out
}

func qExplainLines(f QueryFacts, d QueryDetail, canChange bool) []ExplainLine {
	out := []ExplainLine{}
	add := func(text string, a *QueryAction) { out = append(out, ExplainLine{Text: text, Action: a}) }
	s := d.Summary

	// How often and how long.
	switch {
	case d.Source == QueryDetailFromNothing:
		add("Nothing is known about this query in "+d.Database+" now: it is not in Query Store for the last 7 days, nor in the plan cache. It may not have run since the server restarted.", nil)
	case s.Runs == 0:
		add("It has not run in the last 7 days.", nil)
	default:
		when := "in 7 days"
		if d.Source == QueryDetailFromPlanCache {
			when = s.Window
		}
		add(fmt.Sprintf("It ran %s %s, %s each on average; the slowest run took %s.",
			plural(s.Runs, "time", "times"), when, QueryDuration(s.AvgDurationMs), QueryDuration(s.MaxDurationMs)), nil)
		if v := d.Verdict; v != nil {
			add(fmt.Sprintf("Each run now uses %s of CPU and reads %s pages (%s MB) from memory or disk; before %s it used %s and read %s pages (%s MB).",
				QueryDuration(v.Now.AvgCPUMs), n0(v.Now.AvgReads), kbAsMB(v.Now.AvgReads*8), qServerClock(v.Since, f),
				QueryDuration(v.Before.AvgCPUMs), n0(v.Before.AvgReads), kbAsMB(v.Before.AvgReads*8)), nil)
		} else {
			add(fmt.Sprintf("Each run uses %s of CPU and reads %s pages (%s MB) from memory or disk.",
				QueryDuration(s.AvgCPUMs), n0(s.AvgReads), kbAsMB(s.AvgReads*8)), nil)
		}
		// Parallel: the plan that cost the most uses more CPU than time.
		var top *QueryPlan
		for i := range d.Plans {
			p := &d.Plans[i]
			if p.Runs > 0 && (top == nil || p.AvgCPUMs*float64(p.Runs) > top.AvgCPUMs*float64(top.Runs)) {
				top = p
			}
		}
		if top != nil && top.AvgDurationMs >= 100 && top.AvgCPUMs >= queryParallelRatio*top.AvgDurationMs {
			add(fmt.Sprintf("%s runs on several CPUs at once (in parallel), so its CPU per run (%s) is more than its time per run (%s).",
				upperFirst(qPlanName(*top)), QueryDuration(top.AvgCPUMs), QueryDuration(top.AvgDurationMs)), nil)
		}
		if n := qStatements(d.Plans); n > 1 {
			add(fmt.Sprintf("It is %d statements that differ only in the values typed into them: Query Store keeps them apart, kakapo counts them together.", n), nil)
		}
	}

	// Archive before an index (IDX10, as QRY01 says it): the query reads much of a table that only
	// grows. The fix only for a table with a date column, as IDX10's finding carries it.
	archive, share, archiveFirst := GrowingTable{}, 0.0, false
	if d.Source != QueryDetailFromNothing && s.Runs > 0 {
		archive, share, archiveFirst = archiveFirstTable(f.Database, f.GrowingCandidates, s.AvgReads, qPlanXMLs(f, d.Plans)...)
	}
	if archiveFirst {
		var a *QueryAction
		if canChange && archive.DateCol != "" {
			a = qArchiveAction(archive)
		}
		add(archiveFirstDo(archive, share), a)
	}

	// Foreign keys without an index on the referencing side (qForeignKeyLines).
	for _, l := range qForeignKeyLines(f, d, canChange) {
		add(l.Text, l.Action)
	}

	// The plans.
	var active, fastest, forced *QueryPlan
	ran := 0
	for i := range d.Plans {
		p := &d.Plans[i]
		if d.Source == QueryDetailFromQueryStore && p.Source != QueryDetailFromQueryStore {
			continue
		}
		if p.Runs > 0 {
			ran++
		}
		if p.Active {
			active = p
		}
		if p.Fastest {
			fastest = p
		}
		if p.Forced {
			forced = p
		}
	}
	lowRun := 0
	for _, p := range d.Plans {
		if p.Runs > 0 && p.Runs < 5 {
			lowRun++
		}
	}
	switch {
	case active != nil && fastest != nil && fastest != active && fastest.AvgDurationMs > 0 &&
		active.AvgDurationMs >= queryRegressionRatio*fastest.AvgDurationMs && active.AvgDurationMs >= 10:
		ratio := active.AvgDurationMs / fastest.AvgDurationMs
		text := fmt.Sprintf("%s plans: the one in use since %s is %sx slower than %s (%s against %s on average, %s against %s).",
			qCountWord(ran), qPlanSince(f, *active), qRatioText(ratio), qPlanName(*fastest), QueryDuration(active.AvgDurationMs), QueryDuration(fastest.AvgDurationMs),
			plural(active.Runs, "run", "runs"), plural(fastest.Runs, "run", "runs"))
		// Does the faster plan do less work, or did it only wait less? CPU and reads per run, over the same
		// runs as the durations (Query Store's per-plan averages of the same window).
		cpuX, readsX := qWorkRatio(active.AvgCPUMs, fastest.AvgCPUMs), qWorkRatio(active.AvgReads, fastest.AvgReads)
		lessWork := cpuX >= queryWorkRatio || readsX >= queryWorkRatio
		if lessWork {
			var parts []string
			if cpuX >= queryWorkRatio {
				parts = append(parts, "uses "+qRatioText(cpuX)+"x less CPU")
			}
			if readsX >= queryWorkRatio {
				parts = append(parts, "reads "+qRatioText(readsX)+"x fewer pages")
			}
			text += " " + upperFirst(qPlanName(*fastest)) + " " + qJoinAnd(parts) + " per run, so the plan itself does less work."
		}
		var a *QueryAction
		switch {
		case fastest.Runs < queryForceMinRuns:
			text += fmt.Sprintf(" But %s ran only %s: compare it on more runs before forcing it.", qPlanName(*fastest), plural(fastest.Runs, "time", "times"))
		case !lessWork && active.AvgCPUMs > 0 && active.AvgReads > 0:
			text += " CPU and pages read per run are similar, so the difference is in elapsed time (waiting, not the work done): forcing it may not make runs consistently faster."
		case qFindAction(fastest.Actions, "force-plan") != nil:
			text += fmt.Sprintf(" Force plan %d so SQL Server uses the faster one.", fastest.PlanID)
			a = qFindAction(fastest.Actions, "force-plan")
		case qFindAction(active.Actions, "evict-plan") != nil:
			text += " Remove the slow plan from the cache so SQL Server compiles a fresh one."
			a = qFindAction(active.Actions, "evict-plan")
		}
		add(text, a)
	case ran >= 5 && lowRun*2 >= ran:
		add(fmt.Sprintf("It got %d plans, most of them used only a few times: SQL Server keeps compiling it again (a RECOMPILE hint, SET options that differ between connections, or statistics that change fast).", ran), nil)
	case ran >= 2 && d.Verdict == nil && qPlansAlike(d.Plans):
		add(fmt.Sprintf("%s plans of similar speed: the plan is not the problem; the cost is in the query itself or its indexes.", qCountWord(ran)), nil)
	case ran == 1 && d.Source == QueryDetailFromQueryStore:
		add("One plan in 7 days: the plan is stable, so the cost is in the query itself or its indexes.", nil)
	}
	if active != nil && active.CachedCopies > 1 {
		add(fmt.Sprintf("The plan in use is one plan cached %s, once for each set of SET options its connections use (ARITHABORT, ANSI_NULLS and the like): its runs are counted together.",
			timesWord(active.CachedCopies)), nil)
	}
	if forced != nil {
		text := fmt.Sprintf("Plan %d is forced: SQL Server must use it.", forced.PlanID)
		if qStatements(d.Plans) > 1 {
			text = fmt.Sprintf("Plan %d is forced for one of its statements: SQL Server must use it there. It ran %s in 7 days.", forced.PlanID, plural(forced.Runs, "time", "times"))
		}
		var a *QueryAction
		if forced.ForceFailures > 0 {
			text += fmt.Sprintf(" Forcing it has failed %s (%s), so SQL Server may be compiling its own plan instead.",
				plural(forced.ForceFailures, "time", "times"), orDash(strings.ToLower(forced.ForceFailureReason)))
		}
		for _, p := range d.Plans {
			if p.PlanID != forced.PlanID && p.QueryID == forced.QueryID && p.Source == QueryDetailFromQueryStore && p.Runs > 0 && forced.Runs > 0 &&
				forced.AvgDurationMs >= queryRegressionRatio*p.AvgDurationMs && forced.AvgDurationMs >= 10 {
				text += fmt.Sprintf(" Plan %d ran faster (%s against %s): stop forcing plan %d.", p.PlanID, QueryDuration(p.AvgDurationMs), QueryDuration(forced.AvgDurationMs), forced.PlanID)
				a = qFindAction(forced.Actions, "unforce-plan")
				break
			}
		}
		if a == nil && forced.ForceFailures > 0 {
			a = qFindAction(forced.Actions, "unforce-plan")
		}
		add(text, a)
	}

	// The plan in use: parameters, warnings, missing index.
	if active != nil {
		compiled := []string{}
		for _, p := range active.Parameters {
			if p.CompiledValue != "" && len(compiled) < 4 {
				compiled = append(compiled, p.Name+" = "+p.CompiledValue)
			}
		}
		if len(compiled) > 0 {
			var text string
			if len(compiled) == 1 {
				p := active.Parameters[0]
				for _, x := range active.Parameters {
					if x.CompiledValue != "" {
						p = x
						break
					}
				}
				text = fmt.Sprintf("Its parameter %s was compiled with %s, which may not be typical.", p.Name, p.CompiledValue)
			} else {
				text = "Its parameters were compiled with " + qJoinAnd(compiled) + ", which may not be typical."
			}
			var a *QueryAction
			if active.Runs > 1 && active.MaxDurationMs >= 1000 && active.MaxDurationMs >= 10*active.AvgDurationMs {
				text += fmt.Sprintf(" Its runs swing from %s on average to %s: a plan built for one value is reused for values it does not suit (parameter sniffing).",
					QueryDuration(active.AvgDurationMs), QueryDuration(active.MaxDurationMs))
				if a = qFindAction(active.Actions, "evict-plan"); a != nil {
					text += " Remove the plan from the cache so the next run compiles for its own values."
				}
			}
			add(text, a)
		}
		if active.Conversion != "" {
			add("Its plan converts a column's type for every row ("+strings.NewReplacer("&gt;", ">", "&lt;", "<").Replace(active.Conversion)+
				"), which can stop SQL Server using an index: a variable, parameter or join column has a different type from the column. The fix is in the code: Copy for the vendor.", nil)
		}
		flags := []string{}
		for _, w := range active.Warnings {
			if !strings.HasPrefix(w, "SQL Server suggests an index") {
				flags = append(flags, w)
			}
		}
		if len(flags) > 0 {
			add("Its plan shows "+qJoinAnd(flags)+".", nil)
		}
		if active.PlanTooBig {
			add("Its plan is too large to inspect here (over 2 MB): save it from SSMS.", nil)
		}
	}
	shown := 0
	seen := map[string]bool{}
	for _, p := range d.Plans {
		if d.Verdict != nil && !qRanSince(p, d.Verdict.Since) {
			continue
		}
		for _, mi := range p.MissingIndexes {
			key := strings.ToLower(mi.Table + mi.Equality + mi.Inequality + mi.Included)
			if seen[key] || shown == 2 {
				continue
			}
			seen[key] = true
			shown++
			keys := strings.Trim(mi.Equality+", "+mi.Inequality, ", ")
			text := fmt.Sprintf("SQL Server suggests an index on %s (%s) that it estimates would cut this query's cost by %s%%.",
				mi.TableName, qUnbracketList(keys), n0(mi.Impact))
			switch {
			case archiveFirst:
				text += " Archive " + archive.Schema + "." + archive.Name + " first; create the index only if it is still slow after that."
			case mi.Action != nil:
				text += " Create the index: the preview checks the existing indexes first."
			}
			add(text, mi.Action)
		}
	}

	// Tables with stale statistics.
	stale := 0
	for _, t := range d.Tables {
		if !t.Stale || stale == 3 {
			continue
		}
		stale++
		age := "never updated"
		if t.AgeDays != nil {
			age = plural(int64(*t.AgeDays), "day", "days") + " old"
		}
		text := fmt.Sprintf("It reads %s (%s rows) whose statistics are %s with %s%% of rows changed.", t.Name, QueryRows(t.Rows), age, n0(t.MaxModifiedPct))
		if t.Action != nil {
			text += " Update statistics on it."
		}
		add(text, t.Action)
	}

	// Waits.
	if len(d.Waits) > 0 && s.Runs > 0 {
		var waitMs int64
		for _, w := range d.Waits {
			waitMs += w.WaitMs
		}
		totalMs := s.AvgDurationMs * float64(s.Runs)
		top := d.Waits[0]
		if totalMs > 0 && float64(waitMs) >= totalMs/4 && top.Pct >= 50 {
			text := fmt.Sprintf("Most of its waiting (%s%%) is spent %s.", n0(top.Pct), top.Words)
			switch {
			case strings.EqualFold(top.Category, "Parallelism"):
				text = fmt.Sprintf("Most of its waiting (%s%%) is its parallel threads waiting for each other: a sign that it reads a lot, not a cause; reading less removes it.", n0(top.Pct))
			case strings.EqualFold(top.Category, "Lock"):
				text += " Open Blocking from the Performance card to see who holds them."
			}
			if v := d.Verdict; v != nil && v.Now.DurationMs > 0 && v.Before.DurationMs >= queryNearlyAllRatio*v.Now.DurationMs {
				text += " Nearly all of it is from before " + qServerClock(v.Since, f) + ", when its runs took far longer."
			}
			add(text, nil)
		}
	}

	// The procedure's statements.
	if d.ObjectName != "" && len(d.Statements) >= 2 {
		top := d.Statements[0]
		if top.PctReads >= 50 {
			who := "this query"
			if !top.ThisQuery {
				who = "a different statement from this query"
			}
			add(fmt.Sprintf("In %s, the statement on line %d does %s%% of the procedure's page reads; it is %s.", d.ObjectName, top.Line, n0(top.PctReads), who), nil)
		}
	}

	// Where the figures come from.
	if d.Source == QueryDetailFromPlanCache {
		text := "Query Store is off for " + d.Database + ", so these figures come from the plan cache " + s.Window + ", with no history."
		if qQueryStoreOn(f.QueryStoreState) {
			text = "Query Store has nothing on this query, so these figures come from the plan cache " + s.Window + ", with no history."
		}
		var a *QueryAction
		if strings.EqualFold(f.QueryStoreState, "OFF") && canChange {
			text += " Turn Query Store on for 7 days of history."
			a = &QueryAction{FixID: FixQueryStore, Label: "Turn Query Store on", Title: "Apply recommended Query Store settings", Options: map[string]string{}}
		}
		add(text, a)
	}
	return out
}

func qFindAction(as []QueryAction, fixID string) *QueryAction {
	for i := range as {
		if as[i].FixID == fixID {
			a := as[i]
			return &a
		}
	}
	return nil
}

func qPlanName(p QueryPlan) string {
	if p.PlanID > 0 {
		return fmt.Sprintf("plan %d", p.PlanID)
	}
	return "the other plan"
}

// qPlanSince is when the plan began to be used: its first hour in the history, else its last
// compile.
func qPlanSince(f QueryFacts, p QueryPlan) string {
	var first *time.Time
	for _, h := range f.History {
		if h.PlanID == p.PlanID && p.PlanID > 0 && (first == nil || h.Hour.Before(*first)) {
			t := h.Hour
			first = &t
		}
	}
	if first == nil {
		first = p.LastCompile
	}
	if first == nil {
		return "its last compile"
	}
	return qServerClock(*first, f)
}

// qWorkRatio is how many times more work the plan in use does than the faster one; 0 when unknown.
func qWorkRatio(active, faster float64) float64 {
	if active <= 0 || faster <= 0 {
		return 0
	}
	return active / faster
}

func qRatioText(r float64) string {
	if r < 10 {
		return strings.TrimSuffix(n1(r), ".0")
	}
	return n0(r)
}

func qJoinAnd(xs []string) string {
	switch len(xs) {
	case 0:
		return ""
	case 1:
		return xs[0]
	}
	return strings.Join(xs[:len(xs)-1], ", ") + " and " + xs[len(xs)-1]
}

// qUnbracketList writes "[A], [B]" as "A, B".
func qUnbracketList(s string) string {
	parts := strings.Split(s, ",")
	for i, p := range parts {
		parts[i] = qUnbracket(strings.TrimSpace(p))
	}
	return strings.Join(parts, ", ")
}

// QueryHandover is the query page's Copy: one plain-text block for the vendor with the query text,
// the numbers, what the plans say and the compiled parameter values.
func QueryHandover(d QueryDetail) string {
	var b strings.Builder
	head := "Query " + d.QueryHash + " in " + d.Database
	if d.ServerName != "" {
		head += " on " + d.ServerName
	}
	b.WriteString(head + "\n")
	if d.ObjectName != "" {
		b.WriteString("Procedure: " + d.ObjectName + "\n")
	}
	if !d.CapturedAt.IsZero() {
		// One clock for the whole block, said once: the server's, as the sentences say their times.
		at := d.CapturedAt.UTC().Add(time.Duration(d.UTCOffsetMinutes) * time.Minute)
		b.WriteString("Read at " + at.Format("2006-01-02 15:04") + " server time (" + utcOffsetText(d.UTCOffsetMinutes) + "); every time here is the server's clock\n")
	}

	if len(d.Explain) > 0 {
		b.WriteString("\nWhat we see\n")
		for _, l := range d.Explain {
			b.WriteString("- " + l.Text + "\n")
		}
	}

	s := d.Summary
	if s.Runs > 0 {
		fmt.Fprintf(&b, "\nNumbers (%s)\nRuns: %s. Duration: %s on average, %s at most. CPU: %s per run. Page reads: %s per run, %s at most.\n",
			s.Window, n0int(s.Runs), QueryDuration(s.AvgDurationMs), QueryDuration(s.MaxDurationMs), QueryDuration(s.AvgCPUMs), n0(s.AvgReads), n0int(s.MaxReads))
	}
	if len(d.Plans) > 0 {
		b.WriteString("\nPlans\n")
		for _, p := range d.Plans {
			marks := []string{}
			if p.Active {
				marks = append(marks, "in use")
			}
			if p.Forced {
				marks = append(marks, "forced")
			}
			if p.Fastest {
				marks = append(marks, "fastest")
			}
			if p.CachedCopies > 1 {
				marks = append(marks, "one plan cached "+timesWord(p.CachedCopies))
			}
			name := qPlanName(p)
			if p.PlanID == 0 {
				name = "cached plan " + p.PlanHash
			}
			m := ""
			if len(marks) > 0 {
				m = " (" + strings.Join(marks, ", ") + ")"
			}
			fmt.Fprintf(&b, "- %s%s: %s runs, %s on average, %s at most, %s page reads per run.\n",
				name, m, n0int(p.Runs), QueryDuration(p.AvgDurationMs), QueryDuration(p.MaxDurationMs), n0(p.AvgReads))
		}
	}
	for _, p := range d.Plans {
		if !p.Active {
			continue
		}
		lines := []string{}
		if p.Conversion != "" {
			lines = append(lines, "- Implicit conversion: "+strings.NewReplacer("&gt;", ">", "&lt;", "<").Replace(p.Conversion))
		}
		for _, w := range p.Warnings {
			if len(p.MissingIndexes) == 0 || !strings.HasPrefix(w, "SQL Server suggests an index") {
				lines = append(lines, "- "+w)
			}
		}
		for _, mi := range p.MissingIndexes {
			lines = append(lines, fmt.Sprintf("- SQL Server suggests an index on %s: keys %s%s, impact %s%%.", mi.TableName,
				qUnbracketList(strings.Trim(mi.Equality+", "+mi.Inequality, ", ")), qIncludeText(mi.Included), n0(mi.Impact)))
		}
		if len(lines) > 0 {
			b.WriteString("\nWhat the plan in use says\n" + strings.Join(lines, "\n") + "\n")
		}
		if len(p.Parameters) > 0 {
			b.WriteString("\nCompiled with\n")
			for _, x := range p.Parameters {
				line := "- " + x.Name
				if x.DataType != "" {
					line += " (" + x.DataType + ")"
				}
				line += " = " + orDash(x.CompiledValue)
				if x.RuntimeValue != "" && x.RuntimeValue != x.CompiledValue {
					line += ", last run with " + x.RuntimeValue
				}
				b.WriteString(line + "\n")
			}
		}
	}
	if d.Text != "" {
		b.WriteString("\nQuery text\n" + strings.TrimSpace(d.Text) + "\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

func qIncludeText(included string) string {
	if included == "" {
		return ""
	}
	return " include " + qUnbracketList(included)
}

// utcOffsetText is the server's offset as "UTC+2", "UTC+5:30", "UTC-3" or "UTC".
func utcOffsetText(minutes int) string {
	if minutes == 0 {
		return "UTC"
	}
	sign := "+"
	if minutes < 0 {
		sign, minutes = "-", -minutes
	}
	if minutes%60 == 0 {
		return fmt.Sprintf("UTC%s%d", sign, minutes/60)
	}
	return fmt.Sprintf("UTC%s%d:%02d", sign, minutes/60, minutes%60)
}

// queryFKMinRows is the referencing table's size from which a foreign key without an index is
// said (PerformanceTriage.sql IDX14: tables of 10,000 rows or more).
const queryFKMinRows = 10000

// qForeignKeyPointer ends the query page's foreign-key sentence: the same rule, database-wide.
const qForeignKeyPointer = "IDX14 on the Design card lists every such key."

// queryFKLinesMax is how many foreign keys the page names, the largest referencing tables first.
const queryFKLinesMax = 5

// qForeignKeyLines say, for a query that deletes from a table or updates its key, each foreign key
// that references the table where the referencing columns lead no index: to check the key, SQL
// Server reads the whole referencing table for every statement (Microsoft Learn, "Primary and
// foreign key constraints": an index on the foreign key columns serves the check when the
// referenced row is deleted or its key changed; PerformanceTriage.sql IDX14, from sp_BlitzIndex
// check 72). Only for a key the plans really check, a read of the referencing table in a plan of
// the DELETE or UPDATE, and a referencing table of queryFKMinRows or more. Each line ends in the
// create-index fix on the key's columns when the database can be changed.
func qForeignKeyLines(f QueryFacts, d QueryDetail, canChange bool) []ExplainLine {
	out := []ExplainLine{}
	if d.Source == QueryDetailFromNothing || len(f.UnindexedForeignKeys) == 0 {
		return out
	}
	plans := qPlanXMLs(f, d.Plans)
	kinds := map[string]string{}
	for _, m := range ShowplanDMLTargets(f.Database, plans...) {
		key := strings.ToLower(m.Schema + "." + m.Table)
		if kinds[key] == "" || m.Kind == "DELETE" {
			kinds[key] = m.Kind
		}
	}
	fks := append([]UnindexedForeignKeyFact{}, f.UnindexedForeignKeys...)
	sort.SliceStable(fks, func(i, j int) bool { return fks[i].Rows > fks[j].Rows })
	for _, k := range fks {
		kind := kinds[strings.ToLower(k.RefSchema+"."+k.RefTable)]
		// An UPDATE reads its own table whether or not it changes the key, so a self-reference is said
		// for a DELETE only.
		self := strings.EqualFold(k.Schema, k.RefSchema) && strings.EqualFold(k.Table, k.RefTable)
		if kind == "UPDATE" && self {
			continue
		}
		if kind == "" || k.Rows < queryFKMinRows || len(k.Columns) == 0 || !qPlansRead(f.Database, k.Schema, k.Table, plans) {
			continue
		}
		if len(out) == queryFKLinesMax {
			break
		}
		ref, table, cols := k.RefSchema+"."+k.RefTable, k.Schema+"."+k.Table, plainList(k.Columns)
		doing := "Each delete from " + ref
		if kind == "UPDATE" {
			doing = "Each change to the key of " + ref
		}
		lead := "starts with " + cols
		if len(k.Columns) > 1 {
			lead = "starts with its columns " + cols
		}
		// The sentence ends with the Design card's pointer (docs/phase5n-contract.md, section 5).
		text := fmt.Sprintf("%s checks foreign key %s, and %s (%s) has no index that %s, so SQL Server reads the whole of %s to check it. An index on %s (%s) lets it seek instead. %s",
			doing, k.Name, table, plural(k.Rows, "row", "rows"), lead, table, table, cols, qForeignKeyPointer)
		var a *QueryAction
		if canChange {
			opts := ForeignKeyIndexOptions(f.Database, k)
			a = &QueryAction{FixID: FixCreateIndex, Label: "Create index", Title: "Create an index for foreign key " + k.Name, Options: opts}
		}
		out = append(out, ExplainLine{Text: text, Action: a})
	}
	return out
}
