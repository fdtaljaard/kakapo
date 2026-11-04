package domain

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// The query page's fixes (docs/phase4c-contract.md, part 1): force or stop forcing a Query Store
// plan, remove one plan from the plan cache, update the statistics of one table. Each preview is
// built from what was read just before, never from the options alone: the options only say which
// plan, handle or table.

// Ids of the query page's fixes.
const (
	FixForcePlan             = "force-plan"
	FixUnforcePlan           = "unforce-plan"
	FixEvictPlan             = "evict-plan"
	FixUpdateStatisticsTable = "update-statistics-table"
)

// QueryFixError is why a query fix cannot be previewed: Invalid for options that are wrong (the
// API answers 400), else nothing to apply (404). Reason is a sentence for the dialog.
type QueryFixError struct {
	Reason  string
	Invalid bool
}

func (e *QueryFixError) Error() string { return e.Reason }

func qRefuse(format string, args ...any) error {
	return &QueryFixError{Reason: fmt.Sprintf(format, args...)}
}

func qInvalid(format string, args ...any) error {
	return &QueryFixError{Reason: fmt.Sprintf(format, args...), Invalid: true}
}

// ParsePlanID reads the planId option: a Query Store plan id, a positive whole number.
func ParsePlanID(s string) (int64, error) {
	id, err := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
	if err != nil || id <= 0 {
		return 0, qInvalid("planId must be a Query Store plan id (a positive whole number), not %q", s)
	}
	return id, nil
}

// ParsePlanHandle reads the planHandle option: 0x and up to 64 bytes in hex, as
// sys.dm_exec_query_stats writes plan_handle. It returns it in upper case with its bytes.
func ParsePlanHandle(s string) (string, []byte, error) {
	s = strings.TrimSpace(s)
	bad := qInvalid("planHandle must be a plan handle (0x followed by up to 128 hexadecimal digits), not %q", s)
	if len(s) < 4 || len(s) > 130 || len(s)%2 != 0 || !strings.EqualFold(s[:2], "0x") {
		return "", nil, bad
	}
	b := make([]byte, 0, (len(s)-2)/2)
	for i := 2; i < len(s); i += 2 {
		v, err := strconv.ParseUint(s[i:i+2], 16, 8)
		if err != nil {
			return "", nil, bad
		}
		b = append(b, byte(v))
	}
	return "0x" + strings.ToUpper(s[2:]), b, nil
}

// ParseTableOption reads the table option: "[schema].[table]", "schema.table" or "table" (schema
// dbo); brackets may hold dots and ]].
func ParseTableOption(s string) (schema, table string, err error) {
	parts := []string{}
	var cur strings.Builder
	in := false
	rs := []rune(strings.TrimSpace(s))
	for i := 0; i < len(rs); i++ {
		r := rs[i]
		switch {
		case in && r == ']' && i+1 < len(rs) && rs[i+1] == ']':
			cur.WriteRune(']')
			i++
		case in && r == ']':
			in = false
		case !in && r == '[':
			in = true
		case !in && r == '.':
			parts = append(parts, strings.TrimSpace(cur.String()))
			cur.Reset()
		default:
			cur.WriteRune(r)
		}
	}
	parts = append(parts, strings.TrimSpace(cur.String()))
	bad := qInvalid("table must name one table as schema.table, not %q", s)
	if in {
		return "", "", bad
	}
	for _, p := range parts {
		if p == "" || len([]rune(p)) > 128 {
			return "", "", bad
		}
	}
	switch len(parts) {
	case 1:
		return "dbo", parts[0], nil
	case 2:
		return parts[0], parts[1], nil
	}
	return "", "", bad
}

// PlanForceFacts is what the force-plan and unforce-plan previews read inside the database: Query
// Store's state, and the plan with every plan of its query and their runtime over 7 days.
type PlanForceFacts struct {
	Database        string
	ProductVersion  string
	QueryStoreState string // actual_state_desc
	PlanID          int64
	Found           bool // the plan is in this database's Query Store
	QueryID         int64
	QueryHash       string
	Plans           []QueryStorePlanFact // every plan of the query
}

func (f PlanForceFacts) plan(id int64) (QueryStorePlanFact, bool) {
	for _, p := range f.Plans {
		if p.PlanID == id {
			return p, true
		}
	}
	return QueryStorePlanFact{}, false
}

// inUse is the plan SQL Server uses now, other than skip: the forced one, else the one that ran
// last.
func (f PlanForceFacts) inUse(skip int64) (QueryStorePlanFact, bool) {
	var best QueryStorePlanFact
	found := false
	for _, p := range f.Plans {
		if p.PlanID == skip {
			continue
		}
		if p.Forced {
			return p, true
		}
		if !found || qLaterThan(p.LastRun, best.LastRun) {
			best, found = p, true
		}
	}
	return best, found
}

func qForcedOther(f PlanForceFacts, skip int64) (QueryStorePlanFact, bool) {
	for _, p := range f.Plans {
		if p.PlanID != skip && p.Forced {
			return p, true
		}
	}
	return QueryStorePlanFact{}, false
}

// qForcePlanStatement and qUnforcePlanStatement run in the database by name, as sp_QuickieStore
// writes its toggle (sp_QuickieStore.sql 13604-13610).
func qForcePlanStatement(db string, queryID, planID int64) string {
	return fmt.Sprintf("EXEC %s.sys.sp_query_store_force_plan @query_id = %d, @plan_id = %d;", QuoteName(db), queryID, planID)
}

func qUnforcePlanStatement(db string, queryID, planID int64) string {
	return fmt.Sprintf("EXEC %s.sys.sp_query_store_unforce_plan @query_id = %d, @plan_id = %d;", QuoteName(db), queryID, planID)
}

func qAvgMsOf(p QueryStorePlanFact) string {
	if p.Runs <= 0 {
		return "no runs in 7 days"
	}
	return "avg " + n0(float64(p.Duration)/float64(p.Runs)/1000) + " ms"
}

const (
	qForceWarnRegressions = "Forcing hides regressions: SQL Server keeps this plan even when the data changes so that another plan would be faster. Look at this query's page again after a business day."
	qForceWarnUnforce     = "Stop forcing it when the code, the indexes or the SQL Server version change (Stop forcing on this query's page)."
	qUnforceWarn          = "SQL Server goes back to choosing the plan itself; its next compile may pick a slower plan again."
)

func qForcePermission(db string) string {
	return "sys.sp_query_store_force_plan and sp_query_store_unforce_plan need ALTER on the database " + db + "."
}

// BuildPlanForceFix builds force-plan or unforce-plan for f.PlanID from what was read: refused
// when Query Store is off, the plan is not in it, or (force) it is already forced, (unforce) it
// is not.
func BuildPlanForceFix(fixID string, f PlanForceFacts) (FixPreview, Fix, error) {
	d, ok := FindFixDef(fixID)
	if !ok || (fixID != FixForcePlan && fixID != FixUnforcePlan) {
		return FixPreview{}, Fix{}, ErrUnknownFix
	}
	if !qQueryStoreOn(f.QueryStoreState) {
		state := strings.ToLower(f.QueryStoreState)
		if state == "" {
			state = "off"
		}
		return FixPreview{}, Fix{}, qRefuse("Query Store is %s on %s: plans can only be forced while it is on. Turn it on first (the Setup card).", state, f.Database)
	}
	p, found := f.plan(f.PlanID)
	if !f.Found || !found {
		return FixPreview{}, Fix{}, qRefuse("there is no plan %d in the Query Store of %s (it may have been cleaned up)", f.PlanID, f.Database)
	}
	qid := p.QueryID
	if qid == 0 {
		qid = f.QueryID
	}
	fix := Fix{ID: d.ID, Title: d.Title, Scope: d.Scope, CheckID: d.CheckID, Setting: d.Setting,
		Options: map[string]string{"planId": strconv.FormatInt(p.PlanID, 10)}}
	pv := FixPreview{Database: f.Database, EngineVersion: f.ProductVersion, Changes: []Drift{}, Warnings: []string{}}
	setting := fmt.Sprintf("Plan for query %d", qid)

	if fixID == FixForcePlan {
		if p.Forced {
			return FixPreview{}, Fix{}, qRefuse("plan %d is already forced for query %d", p.PlanID, qid)
		}
		other, hasOther := qForcedOther(f, p.PlanID)
		use, hasUse := f.inUse(p.PlanID)
		switch {
		case hasOther:
			fix.Current = fmt.Sprintf("plan %d forced, %s", other.PlanID, qAvgMsOf(other))
		case hasUse:
			fix.Current = fmt.Sprintf("plan %d in use, not forced, %s", use.PlanID, qAvgMsOf(use))
		default:
			fix.Current = "no plan forced"
		}
		fix.Target = fmt.Sprintf("plan %d forced, %s", p.PlanID, qAvgMsOf(p))
		pv.Statement = qForcePlanStatement(f.Database, qid, p.PlanID)
		if hasOther {
			pv.Rollback = qForcePlanStatement(f.Database, qid, other.PlanID)
		} else {
			pv.Rollback = qUnforcePlanStatement(f.Database, qid, p.PlanID)
		}
		lead := fmt.Sprintf("Force plan %d for this query: %s", p.PlanID, qAvgMsOf(p))
		if hasUse {
			lead += fmt.Sprintf(" against %s for the plan now in use (plan %d)", strings.TrimPrefix(qAvgMsOf(use), "avg "), use.PlanID)
		}
		pv.Warnings = append(pv.Warnings, lead+".")
		if hasUse && p.Runs > 0 && use.Runs > 0 && p.Duration*use.Runs >= use.Duration*p.Runs {
			pv.Warnings = append(pv.Warnings, fmt.Sprintf("Plan %d is not faster than plan %d on average: forcing it will not make the query faster.", p.PlanID, use.PlanID))
		}
		if p.Runs > 0 && p.Runs < 10 {
			pv.Warnings = append(pv.Warnings, fmt.Sprintf("Plan %d ran only %s in 7 days: its average says little yet.", p.PlanID, plural(p.Runs, "time", "times")))
		}
		if p.ForceFailures > 0 {
			pv.Warnings = append(pv.Warnings, fmt.Sprintf("Forcing plan %d has failed before (%s, %s): SQL Server may not be able to use it.",
				p.PlanID, plural(p.ForceFailures, "time", "times"), orDash(strings.ToLower(p.ForceFailureReason))))
		}
		pv.Warnings = append(pv.Warnings, qForceWarnRegressions, qForceWarnUnforce)
	} else {
		if !p.Forced {
			return FixPreview{}, Fix{}, qRefuse("plan %d is not forced for query %d", p.PlanID, qid)
		}
		fix.Current = fmt.Sprintf("plan %d forced, %s", p.PlanID, qAvgMsOf(p))
		fix.Target = "no plan forced"
		pv.Statement = qUnforcePlanStatement(f.Database, qid, p.PlanID)
		pv.Rollback = qForcePlanStatement(f.Database, qid, p.PlanID)
		lead := fmt.Sprintf("Stop forcing plan %d for this query.", p.PlanID)
		pv.Warnings = append(pv.Warnings, lead, qUnforceWarn)
	}
	if strings.EqualFold(f.QueryStoreState, "READ_ONLY") {
		pv.Warnings = append(pv.Warnings, "Query Store is read-only on "+f.Database+": forcing still works, but no new figures are collected to show whether it helped.")
	}
	pv.Warnings = append(pv.Warnings, qForcePermission(f.Database))
	pv.Changes = append(pv.Changes, Drift{Setting: setting, Current: fix.Current, Target: fix.Target})
	return pv, fix, nil
}

// PlanForceReadBack says what the plan's forcing reads back as after the fix, and whether that is
// what the fix set.
func PlanForceReadBack(fixID string, f PlanForceFacts) (string, bool) {
	p, ok := f.plan(f.PlanID)
	if !ok {
		return fmt.Sprintf("plan %d is no longer in Query Store", f.PlanID), false
	}
	s := fmt.Sprintf("plan %d not forced", p.PlanID)
	if p.Forced {
		s = fmt.Sprintf("plan %d forced", p.PlanID)
	}
	if p.ForceFailures > 0 && p.ForceFailureReason != "" {
		s += fmt.Sprintf(" (last forcing failure: %s)", strings.ToLower(p.ForceFailureReason))
	}
	return s, p.Forced == (fixID == FixForcePlan)
}

// PlanForceDescription is the change log line of a forcing fix.
func PlanForceDescription(fixID string, f PlanForceFacts, fix Fix) string {
	verb := "Forced"
	if fixID == FixUnforcePlan {
		verb = "Stopped forcing"
	}
	s := fmt.Sprintf("%s plan %d", verb, f.PlanID)
	if p, ok := f.plan(f.PlanID); ok && p.QueryID > 0 {
		s += fmt.Sprintf(" for query %d", p.QueryID)
	}
	if f.QueryHash != "" {
		s += " (" + f.QueryHash + ")"
	}
	return s + " in " + f.Database + ": " + fix.Current + " -> " + fix.Target
}

// CachedPlanFacts is what the evict-plan preview reads about one plan handle in the plan cache.
type CachedPlanFacts struct {
	Handle     string // 0x...
	Found      bool
	ObjectType string // Proc, Adhoc, Prepared, ...
	ObjectName string // schema.procedure for a procedure's plan
	UseCount   int64
	SizeKB     int64
	Created    *time.Time // server time
	Text       string     // the batch or procedure text, first characters
}

const (
	qEvictWarnHarmless = "Remove this plan from the cache so SQL Server compiles a fresh one. Harmless; the next run compiles."
	qEvictWarnSame     = "If the fresh plan is just as slow, the plan was not the problem: look at the query and its indexes."
	qEvictWarnOne      = "Only this plan is removed, not the whole plan cache."
	qEvictPermission   = "DBCC FREEPROCCACHE needs the ALTER SERVER STATE permission on the server (sysadmin or serveradmin)."
)

// BuildEvictPlanFix builds evict-plan for one plan handle: refused when the plan is no longer in
// the cache.
func BuildEvictPlanFix(f CachedPlanFacts, database, version string) (FixPreview, Fix, error) {
	d, _ := FindFixDef(FixEvictPlan)
	if !f.Found {
		return FixPreview{}, Fix{}, qRefuse("this plan is no longer in the plan cache: SQL Server already compiles a fresh one on the next run")
	}
	fix := Fix{ID: d.ID, Title: d.Title, Scope: d.Scope, CheckID: d.CheckID, Setting: d.Setting,
		Current: fmt.Sprintf("in the cache, used %s", plural(f.UseCount, "time", "times")), Target: "removed from the cache",
		Options: map[string]string{"planHandle": f.Handle}}
	if f.Created != nil {
		fix.Current = fmt.Sprintf("in the cache since %s, used %s", sqlTime(*f.Created), plural(f.UseCount, "time", "times"))
	}
	pv := FixPreview{Database: "", EngineVersion: version, Changes: []Drift{}, Warnings: []string{qEvictWarnHarmless}}
	_ = database
	pv.Statement = "DBCC FREEPROCCACHE (" + f.Handle + ");"
	if strings.EqualFold(f.ObjectType, "Proc") && f.ObjectName != "" {
		pv.Warnings = append(pv.Warnings, "The plan is the whole procedure "+f.ObjectName+": every statement in it compiles again on its next run.")
	}
	pv.Warnings = append(pv.Warnings, qEvictWarnSame, qEvictWarnOne, "No rollback: a removed plan cannot be put back, and none is needed.", qEvictPermission)
	pv.Changes = append(pv.Changes, Drift{Setting: "Cached plan " + qShortHandle(f.Handle), Current: fix.Current, Target: fix.Target})
	return pv, fix, nil
}

func qShortHandle(h string) string {
	if len(h) <= 18 {
		return h
	}
	return h[:18] + "…"
}

// EvictPlanReadBack says whether the plan is gone from the cache.
func EvictPlanReadBack(after CachedPlanFacts) (string, bool) {
	if after.Found {
		return "the plan is still in the cache", false
	}
	return "removed from the plan cache", true
}

// EvictPlanDescription is the change log line of evict-plan.
func EvictPlanDescription(f CachedPlanFacts, queryHash string) string {
	s := "Removed plan " + qShortHandle(f.Handle) + " from the plan cache"
	if queryHash != "" {
		s += " (query " + queryHash + ")"
	}
	if f.ObjectName != "" {
		s += ", procedure " + f.ObjectName
	}
	return s
}

// TableStatsFacts is what the update-statistics-table preview reads about one table.
type TableStatsFacts struct {
	Database       string
	ProductVersion string
	Schema         string
	Table          string
	Found          bool
	Rows           int64
	Stats          int
	OldestUpdate   *time.Time // server time
	MaxModPct      float64
	ServerNow      time.Time // server time, when read
}

func (f TableStatsFacts) name() string { return f.Schema + "." + f.Table }

// TableStatsSampled says whether the table is large enough to sample 25 percent instead of a full
// scan (over 10 million rows).
func (f TableStatsFacts) TableStatsSampled() bool { return f.Rows > queryLargeTableRows }

// TableStatsTimeout is how long the update may run: 10 minutes, 30 for a table over 10 million
// rows.
func TableStatsTimeout(rows int64) time.Duration {
	if rows > queryLargeTableRows {
		return 30 * time.Minute
	}
	return 10 * time.Minute
}

func (f TableStatsFacts) current() string {
	s := fmt.Sprintf("%s, %s rows", plural(int64(f.Stats), "statistic", "statistics"), QueryRows(f.Rows))
	if f.OldestUpdate != nil {
		days := max(0, int(f.ServerNow.Sub(*f.OldestUpdate).Hours()/24))
		s += fmt.Sprintf(", oldest updated %s (%s ago)", sqlDay(*f.OldestUpdate), plural(int64(days), "day", "days"))
	} else {
		s += ", some never updated"
	}
	return s + fmt.Sprintf(", %s%% of rows changed", n0(f.MaxModPct))
}

// BuildTableStatsFix builds update-statistics-table: FULLSCAN, or SAMPLE 25 PERCENT over 10
// million rows. Refused when the table is not there or has no statistics yet.
func BuildTableStatsFix(f TableStatsFacts) (FixPreview, Fix, error) {
	d, _ := FindFixDef(FixUpdateStatisticsTable)
	if !f.Found {
		return FixPreview{}, Fix{}, qRefuse("there is no table %s in %s", f.name(), f.Database)
	}
	if f.Stats == 0 {
		return FixPreview{}, Fix{}, qRefuse("%s has no statistics yet: SQL Server creates them when a query needs them", f.name())
	}
	scan, target := "WITH FULLSCAN", "updated now, full scan"
	if f.TableStatsSampled() {
		scan, target = "WITH SAMPLE 25 PERCENT", "updated now, 25% sample"
	}
	fix := Fix{ID: d.ID, Title: d.Title, Scope: d.Scope, CheckID: d.CheckID, Setting: d.Setting,
		Current: f.current(), Target: target, Options: map[string]string{"table": QuoteName(f.Schema) + "." + QuoteName(f.Table)}}
	pv := FixPreview{Database: f.Database, EngineVersion: f.ProductVersion, Changes: []Drift{}, Warnings: []string{}}
	pv.Statement = "UPDATE STATISTICS " + QuoteName(f.Schema) + "." + QuoteName(f.Table) + " " + scan + ";"
	switch {
	case f.TableStatsSampled():
		pv.Warnings = append(pv.Warnings, fmt.Sprintf("%s has %s rows: over 10 million rows a 25%% sample is read instead of the whole table; it still takes minutes and competes with users for the disk, so run it outside peak hours.", f.name(), QueryRows(f.Rows)))
	case f.Rows >= 1_000_000:
		pv.Warnings = append(pv.Warnings, fmt.Sprintf("%s has %s rows: a full scan reads the whole table, takes minutes and competes with users for the disk, so run it outside peak hours.", f.name(), QueryRows(f.Rows)))
	default:
		pv.Warnings = append(pv.Warnings, fmt.Sprintf("%s has %s rows: this takes seconds.", f.name(), QueryRows(f.Rows)))
	}
	pv.Warnings = append(pv.Warnings,
		"It runs online: users keep working; queries that use the table compile a new plan on their next run.",
		"No rollback: updated statistics are never worse than stale ones.",
		"Needs ALTER on the table (db_owner or db_ddladmin).")
	pv.Changes = append(pv.Changes, Drift{Setting: "Statistics on " + f.name(), Current: fix.Current, Target: fix.Target})
	return pv, fix, nil
}

// TableStatsReadBack says how old the table's oldest statistics are after the update, and whether
// every statistic was updated after the update started (startedAt, server time).
func TableStatsReadBack(after TableStatsFacts, startedAt time.Time) (string, bool) {
	if !after.Found {
		return "the table is no longer there", false
	}
	if after.OldestUpdate == nil {
		return "a statistic of the table has never been updated", false
	}
	s := "oldest statistics updated " + after.OldestUpdate.Format("2006-01-02 15:04:05")
	return s, !after.OldestUpdate.Before(startedAt.Add(-2 * time.Second))
}

// TableStatsDescription is the change log line of update-statistics-table.
func TableStatsDescription(before TableStatsFacts, statement string) string {
	return TableStatsDescriptionOf(before.name(), before.current(), statement)
}

// TableStatsDescriptionOf is TableStatsDescription from the table's name and its statistics as the
// preview showed them (the fix's current value): a run read back after a lost connection
// (docs/phase4f-contract.md) has no read of its own from before the update.
func TableStatsDescriptionOf(table, current, statement string) string {
	how := "full scan"
	if strings.Contains(statement, "SAMPLE") {
		how = "25% sample"
	}
	return fmt.Sprintf("Updated statistics on %s (%s), %s", table, current, how)
}
