package domain

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// The change audit (docs/phase5w-contract.md): kakapo's own log of every action (section B), who made a
// setting change (A5), the changes that always light up (C3) and the What changed? screen's rows and verdict
// (C2). Pure: the service reads and stores, this decides.

// --- B: kakapo's own log ---

// Action log phases and outcomes.
const (
	ActionStarted = "started"
	ActionEnded   = "ended"
	ActionCopied  = "copied"

	OutcomeVerified = "verified"
	OutcomeDiffers  = "differs"
	OutcomeFailed   = "failed"
	OutcomeStopped  = "stopped"
	OutcomeUnknown  = "unknown"
)

// ActionLogRow is one row of action_log: a started or ended row of an action, or a copied one (an action
// from before the change audit, seeded from the change table).
type ActionLogRow struct {
	ID            int64      `json:"id"`
	ActionID      string     `json:"actionId"`
	Phase         string     `json:"phase"`
	ServerID      int64      `json:"serverId"`
	LoggedAt      time.Time  `json:"loggedAt"`
	StartedAt     time.Time  `json:"startedAt"`
	EndedAt       *time.Time `json:"endedAt,omitempty"`
	DurationMs    *int64     `json:"durationMs,omitempty"`
	WindowsUser   string     `json:"windowsUser"`
	SQLLogin      string     `json:"sqlLogin"`
	KakapoHost    string     `json:"kakapoHost"`
	KakapoVersion string     `json:"kakapoVersion"`
	Database      string     `json:"database,omitempty"`
	Object        string     `json:"object"`
	ChangeType    string     `json:"changeType"`
	FixID         string     `json:"fixId"`
	Description   string     `json:"description"`
	Statement     string     `json:"statement"`
	Rollback      string     `json:"rollback,omitempty"`
	ValueBefore   string     `json:"valueBefore,omitempty"`
	ValueAfter    string     `json:"valueAfter,omitempty"`
	Outcome       string     `json:"outcome,omitempty"`
	Result        string     `json:"result,omitempty"`
	ChangeIDs     string     `json:"changeIds,omitempty"`
}

// ActionOutcome is how an action ended: stopped when a stop was asked for, failed on an error, verified
// when the value read back is the target, differs otherwise.
func ActionOutcome(res FixResult, err error, stopped bool) string {
	switch {
	case stopped:
		return OutcomeStopped
	case err != nil:
		return OutcomeFailed
	case res.Verified:
		return OutcomeVerified
	}
	return OutcomeDiffers
}

// ActionResult is the ended row's result: the error, or the value read back.
func ActionResult(res FixResult, err error) string {
	if err != nil {
		return err.Error()
	}
	if res.Verified {
		return res.After + ", verified"
	}
	return res.After + ", differs"
}

// ActionChangeType is the change type of an action of a fix, before its change rows are written.
func ActionChangeType(def FixDef) string {
	switch def.Setting {
	case "Index", "Primary key":
		return "INDEX"
	case "Statistics":
		return "STATISTICS"
	case "Plan":
		return "PLAN"
	case "TempDB files", "FILEGROWTH":
		return "FILE"
	case "Maintenance job", "Maintenance plan", "Maintenance procedure", "CHECKDB job", "Job step", "msdb history",
		"Backup schedule", "IndexOptimize", "CHECKDB", "Alerts", "Archive":
		return "MAINTENANCE"
	case "Session":
		return "OTHER"
	}
	return "SETTING"
}

// ActionDescription is the started row's description: what the fix does, where.
func ActionDescription(def FixDef, fix Fix, serverName, database string) string {
	where := database
	if def.Scope == ScopeServer {
		where = serverName
	}
	s := def.Title
	if fix.Current != "" && fix.Target != "" && fix.Current != fix.Target {
		s += ": " + orQ(fix.Setting) + " from " + fix.Current + " to " + fix.Target
	}
	if where != "" {
		s += " on " + where
	}
	return s
}

// AuditAction is one action of kakapo's as the log holds it: its started row (or its copied row), and its
// ended row when it has one.
type AuditAction struct {
	Start ActionLogRow
	End   *ActionLogRow
}

// Copied says the action is from before the change audit.
func (a AuditAction) Copied() bool { return a.Start.Phase == ActionCopied }

// Outcome is the ended row's outcome; unknown when the action started and never ended (kakapo stopped while
// it ran); a copied row's own.
func (a AuditAction) Outcome() string {
	if a.Copied() {
		return a.Start.Outcome
	}
	if a.End == nil {
		return OutcomeUnknown
	}
	return a.End.Outcome
}

// GroupActions joins the started and ended rows of each action, oldest first; an ended row without its
// started row is kept as its own action.
func GroupActions(rows []ActionLogRow) []AuditAction {
	by := map[string]*AuditAction{}
	order := []string{}
	for _, r := range rows {
		r := r
		a, ok := by[r.ActionID]
		if !ok {
			a = &AuditAction{}
			by[r.ActionID] = a
			order = append(order, r.ActionID)
		}
		switch r.Phase {
		case ActionEnded:
			a.End = &r
			if a.Start.ActionID == "" {
				a.Start = r
			}
		default:
			a.Start = r
		}
	}
	out := make([]AuditAction, 0, len(order))
	for _, id := range order {
		out = append(out, *by[id])
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Start.StartedAt.Before(out[j].Start.StartedAt) })
	return out
}

// actionScope is whether an action changed a server setting: its fix's scope, or, for a copied row
// without a fix id, whether its object is a server fix's setting.
func actionScope(r ActionLogRow) string {
	if def, ok := FindFixDef(r.FixID); ok {
		return def.Scope
	}
	for _, d := range FixDefs {
		if d.Scope == ScopeServer && strings.EqualFold(d.Setting, r.Object) {
			return ScopeServer
		}
	}
	return ScopeDatabase
}

// --- A6: setting changes from the default trace ---

// SettingChangeFact is one ErrorLog event (22) of the default trace whose text is a configuration change.
// At is UTC.
type SettingChangeFact struct {
	At      time.Time
	Text    string
	Login   string
	Host    string
	Program string
}

var configChangeText = regexp.MustCompile(`Configuration option '(.+?)' changed from (\S+?) to (\S+?)\.`)

// ParseConfigChange reads "2026-11-12 12:49:16.95 spid85      Configuration option 'max server memory (MB)'
// changed from 2147483647 to 98304. Run the RECONFIGURE statement to install." (the error log's time and spid
// come first in the trace's text): the option, from and to.
func ParseConfigChange(text string) (option, from, to string, ok bool) {
	m := configChangeText.FindStringSubmatch(text)
	if m == nil {
		return "", "", "", false
	}
	return m[1], m[2], m[3], true
}

// BuildSeenSettingChanges turns the trace's configuration changes from since (UTC) on into change rows seen
// on the server (decision A6): server level (no database), type SETTING, the option as the object, "max
// server memory (MB) changed from 2147483647 to 98304 by DOMAIN\jane on APP-SRV01 (Microsoft SQL Server
// Management Studio)". kakapo's own and the ones that change nothing ("from 1 to 1", which every
// "show advanced options" writes) are left out.
func BuildSeenSettingChanges(serverID int64, facts []SettingChangeFact, since time.Time) []Change {
	out := []Change{}
	for _, x := range facts {
		option, from, to, ok := ParseConfigChange(x.Text)
		if !ok || from == to || IsKakapoProgram(x.Program) || x.At.UTC().Before(since) {
			continue
		}
		out = append(out, Change{ServerID: serverID, ChangedAt: x.At.UTC(), Object: option, ChangeType: "SETTING",
			Description: option + " changed from " + from + " to " + to + seenBy(x.Login, []string{x.Host}, x.Program),
			Result:      ChangeSeenOnServer})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].ChangedAt.Before(out[j].ChangedAt) })
	return out
}

var seenSettingText = regexp.MustCompile(`^(.+) changed from (\S+) to (\S+) by `)

// seenSettingChange reads back a server-level setting change seen in the trace as a setting change, for
// the C3 rules.
func seenSettingChange(c Change) (SettingChange, bool) {
	if c.Database != "" || c.ChangeType != "SETTING" || c.Result != ChangeSeenOnServer {
		return SettingChange{}, false
	}
	m := seenSettingText.FindStringSubmatch(c.Description)
	if m == nil {
		return SettingChange{}, false
	}
	return SettingChange{Item: "config/" + m[1], Kind: SettingChanged, From: m[2], To: m[3], Before: c.ChangedAt, After: c.ChangedAt}, true
}

// seenWho is the "by ..." part of a change seen on the server.
func seenWho(description string) string {
	_, who := seenSplit(description)
	return who
}

// seenSplit splits a change seen on the server into what changed and who: "Procedure dbo.p altered by DOM\jane
// on PC1 (SSMS)" is "Procedure dbo.p altered" and "DOM\jane on PC1 (SSMS)"; a deploy's "Deploy by DOM\build
// on BUILD01 (DacFx): 40 objects (...)" is "Deploy: 40 objects (...)" and "DOM\build on BUILD01 (DacFx)".
func seenSplit(description string) (what, who string) {
	i := strings.LastIndex(description, " by ")
	if i < 0 {
		return description, ""
	}
	before, after := description[:i], description[i+4:]
	if j := strings.Index(after, ": "); j >= 0 {
		return before + after[j:], strings.TrimSpace(after[:j])
	}
	return before, strings.TrimSpace(after)
}

// --- A5: who made a setting change ---

// Attribution kinds.
const (
	ByKakapo  = "kakapo"
	ByTrace   = "trace"
	ByUnknown = "unknown"
)

// Attribution is who made a setting change: kakapo (an action of the log), SQL Server's trace (a change seen
// on the server), or unknown. Who is never blank.
type Attribution struct {
	Kind     string     `json:"kind"`
	Who      string     `json:"who"`
	At       *time.Time `json:"at,omitempty"`
	ActionID string     `json:"-"`
	ChangeID int64      `json:"-"`
}

// traceSlack is how far a trace event may sit outside the reads' bracket and still be the change: the
// server's clock and kakapo's are not the same clock.
const traceSlack = 2 * time.Minute

// actionItems says which baseline items a fix changes: exact keys or prefixes ending in "/". nil means the
// fix is not known here (the bracket alone decides); an empty list means it changes no baseline item.
func actionItems(r ActionLogRow) []string {
	db := r.Database
	switch r.FixID {
	case "max-server-memory", "maxdop", "cost-threshold", "optimize-adhoc", "backup-compression", "min-server-memory",
		FixEnableDefaultTrace:
		def, _ := FindFixDef(r.FixID)
		return []string{"config/" + def.Setting, "config.pending/" + def.Setting}
	case "reconfigure":
		return []string{"config/", "config.pending/"}
	case "trace-flag-off":
		return []string{ItemTraceFlags}
	case "auto-close":
		return []string{"db/" + db + "/auto_close"}
	case "auto-shrink":
		return []string{"db/" + db + "/auto_shrink"}
	case "page-verify":
		return []string{"db/" + db + "/page_verify"}
	case "auto-statistics":
		return []string{"db/" + db + "/auto_create_stats", "db/" + db + "/auto_update_stats"}
	case "indirect-checkpoints":
		return []string{"db/" + db + "/target_recovery_time"}
	case FixQueryStore:
		return []string{"db/" + db + "/query_store"}
	case "rcsi":
		return []string{"db/" + db + "/rcsi", "db/" + db + "/snapshot_isolation"}
	case "multi-user":
		return []string{"db/" + db + "/user_access"}
	case "compat-level":
		return []string{"db/" + db + "/compatibility_level"}
	case "recovery-model":
		return []string{"db/" + db + "/recovery_model"}
	case "autogrowth":
		return []string{"file/" + db + "/"}
	case "tempdb-files", "add-tempdb-files":
		return []string{"file/tempdb/", "db/tempdb/data_files"}
	case "maintenance-job", "checkdb-job", "remove-shrink-step", "history-cleanup", "maintenance-plan", FixBackupSchedule,
		FixRemoveBackupSchedule, FixInstallMaintenanceProc, FixRemoveMaintenanceProc, FixInstallOlaIndex, "archive-table", "remove-archive":
		return []string{"job/"}
	case "create-index", "drop-index", FixDisableIndex, FixReorganizeIndex, FixReorganizeIndexes, "kill-session", "force-plan",
		"unforce-plan", "evict-plan", FixUpdateStatistics, "update-statistics-table", "checkdb-now", "suspect-pages-clear",
		FixAddPrimaryKey, "scoped-config-reset", "operator-alerts", "install-xe-session", "remove-xe-session":
		return []string{}
	}
	return nil
}

func itemMatches(items []string, item string) bool {
	if items == nil {
		return true
	}
	for _, p := range items {
		if strings.HasSuffix(p, "/") && strings.HasPrefix(strings.ToLower(item), strings.ToLower(p)) {
			return true
		}
		if strings.EqualFold(p, item) {
			return true
		}
	}
	return false
}

// kakapoWho is "Set by kakapo: DOMAIN\jane on LAPTOP-7"; for a copied action, from before the audit.
func kakapoWho(r ActionLogRow) string {
	if r.Phase == ActionCopied || r.WindowsUser == "" {
		return "Set by kakapo (before the change audit, so who and where were not recorded)"
	}
	s := "Set by kakapo: " + r.WindowsUser
	if r.KakapoHost != "" {
		s += " on " + r.KakapoHost
	}
	if r.SQLLogin != "" && !strings.EqualFold(r.SQLLogin, r.WindowsUser) {
		s += " (SQL login " + r.SQLLogin + ")"
	}
	return s
}

// AttributeSettingChange says who made a setting change (decision A5), in this order: a kakapo action whose
// item matches between the two snapshots; a change seen in SQL Server's trace for that setting (a
// configuration change, or the database altered) inside the bracket; otherwise outside kakapo, who unknown.
func AttributeSettingChange(c SettingChange, actions []AuditAction, seen []Change) Attribution {
	for i := len(actions) - 1; i >= 0; i-- {
		a := actions[i]
		if a.Copied() {
			continue
		}
		r := a.Start
		inBracket := (!r.StartedAt.Before(c.Before) && !r.StartedAt.After(c.After)) || (c.AfterAction != "" && c.AfterAction == r.ActionID)
		if !inBracket || !itemMatches(actionItems(r), c.Item) {
			continue
		}
		if items := actionItems(r); items == nil && c.Database() != "" && actionScope(r) == ScopeDatabase && !strings.EqualFold(c.Database(), r.Database) {
			continue
		}
		at := r.StartedAt
		return Attribution{Kind: ByKakapo, Who: kakapoWho(r), At: &at, ActionID: r.ActionID}
	}
	kind, _, _ := itemParts(c.Item)
	var best *Change
	for i := range seen {
		x := seen[i]
		if x.Result != ChangeSeenOnServer || x.ChangeType != "SETTING" {
			continue
		}
		if x.ChangedAt.Before(c.Before.Add(-traceSlack)) || x.ChangedAt.After(c.After.Add(traceSlack)) {
			continue
		}
		switch kind {
		case "config", "config.pending":
			name := strings.TrimPrefix(strings.TrimPrefix(c.Item, "config.pending/"), "config/")
			if x.Database != "" || !strings.EqualFold(x.Object, name) {
				continue
			}
		case "db", "file":
			if x.Database == "" || !strings.EqualFold(x.Database, c.Database()) {
				continue
			}
		default:
			continue
		}
		if best == nil || x.ChangedAt.After(best.ChangedAt) {
			best = &seen[i]
		}
	}
	if best != nil {
		at := best.ChangedAt
		who := seenWho(best.Description)
		if who == "" {
			who = "someone"
		}
		return Attribution{Kind: ByTrace, Who: "By " + who + ", from SQL Server's trace", At: &at, ChangeID: best.ID}
	}
	switch c.Kind {
	case SettingRestart, SettingVersion:
		return Attribution{Kind: ByUnknown, Who: "Outside kakapo, between the two reads"}
	}
	return Attribution{Kind: ByUnknown, Who: "Changed outside kakapo. SQL Server's trace no longer holds who."}
}

// --- C3: the changes that always light up ---

// Flags of the audit rows (DESIGN.md, The One Meaning Rule); "" is no flag.
const (
	FlagRed   = "red"
	FlagAmber = "amber"
	FlagGrey  = "grey"
)

// Screens a row links to: Memory is the instance's Memory screen; the others are the database's cards.
const (
	ScreenMemory      = "memory"
	ScreenPerformance = "performance"
	ScreenBackups     = "backups"
	ScreenStorage     = "storage"
)

// AuditRule is one named rule of C3.
type AuditRule struct {
	Name    string
	Flag    string
	Tell    string
	Screens []string
}

// AuditRules is C3's table, by name.
var AuditRules = map[string]AuditRule{
	"memory-cap-raised":   {"memory-cap-raised", FlagRed, "SQL Server can take more RAM, and Windows and other services can be squeezed.", []string{ScreenMemory}},
	"memory-cap-lowered":  {"memory-cap-lowered", FlagAmber, "Less cache, so more disk reads. Also clears the plan cache.", []string{ScreenMemory, ScreenPerformance}},
	"min-memory-raised":   {"min-memory-raised", FlagAmber, "SQL Server won't give that memory back to Windows.", []string{ScreenMemory}},
	"lock-pages":          {"lock-pages", FlagAmber, "Changes how SQL Server's memory shows in Task Manager.", []string{ScreenMemory}},
	"parallelism":         {"parallelism", FlagAmber, "Query plans change, so CPU and speed can move.", []string{ScreenPerformance}},
	"recovery-full":       {"recovery-full", FlagRed, "The log grows until it's backed up.", []string{ScreenBackups, ScreenStorage}},
	"backup-job":          {"backup-job", FlagRed, "No safe restore point.", []string{ScreenBackups}},
	"auto-shrink-close":   {"auto-shrink-close", FlagRed, "Constant shrinking and fragmentation, slow first queries.", []string{ScreenStorage, ScreenPerformance}},
	"compatibility-level": {"compatibility-level", FlagAmber, "Query plans can change for the whole database.", []string{ScreenPerformance}},
	"row-versioning":      {"row-versioning", FlagAmber, "tempdb grows with the version store.", []string{ScreenStorage}},
	"query-store-off":     {"query-store-off", FlagAmber, "Query history for troubleshooting stops.", []string{ScreenPerformance}},
	"database-added":      {"database-added", FlagAmber, "Competes for memory and disk.", []string{ScreenMemory}},
	"trace-flags":         {"trace-flags", FlagAmber, "Server-wide behaviour changes.", []string{ScreenPerformance}},
	"version":             {"version", FlagAmber, "New behaviour; compare before and after.", []string{ScreenPerformance}},
	"restart":             {"restart", FlagGrey, "The cache starts empty and refills, and usage stats reset.", []string{ScreenMemory}},
	"kakapo-cache":        {"kakapo-cache", FlagGrey, "Fills the cache with that table. Normal, unless pressure follows.", []string{ScreenMemory}},
}

// FlagContext is what the rules read beside the change: each database's last log backup (UTC, by lower-case
// name), read when the screen opens.
type FlagContext struct {
	LastLogBackup map[string]time.Time
}

// RuleHit is a rule that matched, with its flag as it applies to this change.
type RuleHit struct {
	Rule    string
	Flag    string
	Tell    string
	Screens []string
}

func hit(name string) *RuleHit {
	r := AuditRules[name]
	return &RuleHit{Rule: r.Name, Flag: r.Flag, Tell: r.Tell, Screens: append([]string(nil), r.Screens...)}
}

func num(s string) (int64, bool) {
	n, err := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
	return n, err == nil
}

// RuleForSetting is the C3 rule a setting change matches, nil for none. A server-level change links to
// Memory only. Recovery to full is red until a log backup has run after it, then grey; a database added is
// red when its name looks like a test or dev copy (5v's rule).
func RuleForSetting(c SettingChange, ctx FlagContext) *RuleHit {
	h := ruleForSetting(c, ctx)
	if h != nil && c.Database() == "" {
		h.Screens = []string{ScreenMemory}
	}
	return h
}

func ruleForSetting(c SettingChange, ctx FlagContext) *RuleHit {
	kind, _, prop := itemParts(c.Item)
	switch c.Kind {
	case SettingRestart:
		return hit("restart")
	case SettingVersion:
		return hit("version")
	case SettingAdded:
		if kind == "db" {
			h := hit("database-added")
			if LooksLikeTestDatabase(c.Database()) {
				h.Flag = FlagRed
			}
			return h
		}
		return nil
	case SettingRemoved:
		if kind == "job" && c.Props["backup"] == "yes" {
			return hit("backup-job")
		}
		return nil
	case SettingPending:
		return nil
	}
	from, fok := num(c.From)
	to, tok := num(c.To)
	switch c.Item {
	case ItemMaxServerMemory:
		if fok && tok && to > from {
			return hit("memory-cap-raised")
		}
		if fok && tok && to < from {
			return hit("memory-cap-lowered")
		}
		return nil
	case ItemMinServerMemory:
		if fok && tok && to > from {
			return hit("min-memory-raised")
		}
		return nil
	case ItemMemoryModel:
		if c.From == "LOCK_PAGES" || c.To == "LOCK_PAGES" {
			return hit("lock-pages")
		}
		return nil
	case ItemMaxDop, ItemCostThreshold:
		return hit("parallelism")
	case ItemTraceFlags:
		return hit("trace-flags")
	}
	switch kind {
	case "db":
		on := strings.EqualFold(c.To, "ON")
		switch prop {
		case "recovery_model":
			if !strings.EqualFold(c.To, "FULL") {
				return nil
			}
			h := hit("recovery-full")
			if t, ok := ctx.LastLogBackup[strings.ToLower(c.Database())]; ok && t.After(c.After) {
				h.Flag = FlagGrey
				h.Tell += " A log backup has run since " + t.UTC().Format("2 Jan 15:04") + " UTC."
			}
			return h
		case "auto_shrink", "auto_close":
			if on {
				return hit("auto-shrink-close")
			}
		case "compatibility_level":
			return hit("compatibility-level")
		case "rcsi":
			if on {
				return hit("row-versioning")
			}
		case "snapshot_isolation":
			if strings.EqualFold(c.To, "ON") {
				return hit("row-versioning")
			}
		case "query_store":
			if strings.EqualFold(c.To, "OFF") {
				return hit("query-store-off")
			}
		case "query_store_state":
			if strings.EqualFold(c.To, "READ_ONLY") {
				return hit("query-store-off")
			}
		}
	case "job":
		if prop == "enabled" && c.To == "0" && c.Props["backup"] == "yes" {
			return hit("backup-job")
		}
	}
	return nil
}

// RuleForAction is the C3 rule of a kakapo action: an index reorganise or rebuild, full-scan statistics or
// archiving fills the cache (grey).
func RuleForAction(r ActionLogRow) *RuleHit {
	switch r.FixID {
	case FixReorganizeIndex, FixReorganizeIndexes, FixUpdateStatistics, "update-statistics-table", "archive-table":
		return hit("kakapo-cache")
	}
	if r.FixID == "" && (r.ChangeType == "INDEX" || r.ChangeType == "STATISTICS") {
		d := strings.ToLower(r.Description)
		if strings.Contains(d, "reorganis") || strings.Contains(d, "rebuil") || strings.Contains(d, "statistics") {
			return hit("kakapo-cache")
		}
	}
	return nil
}

// --- C2: the rows and the verdict ---

// Row sources.
const (
	SourceKakapo   = "kakapo"
	SourceBaseline = "baseline"
	SourceTrace    = "trace"
	SourceByHand   = "by hand"
)

// AuditRow is one row of the What changed? list: when (exact, or between two reads), the flag, the change in
// a sentence and what it means, who, the screens it affects, and the fold.
type AuditRow struct {
	Key       string     `json:"key"`
	At        *time.Time `json:"at,omitempty"`
	From      *time.Time `json:"from,omitempty"`
	To        *time.Time `json:"to,omitempty"`
	Flag      string     `json:"flag,omitempty"`
	Rule      string     `json:"rule,omitempty"`
	Headline  string     `json:"headline"`
	Tell      string     `json:"tell,omitempty"`
	Who       string     `json:"who"`
	ByKakapo  bool       `json:"byKakapo"`
	Source    string     `json:"source"`
	Database  string     `json:"database,omitempty"`
	Screens   []string   `json:"screens"`
	Statement string     `json:"statement,omitempty"`
	Rollback  string     `json:"rollback,omitempty"`
	Before    string     `json:"before,omitempty"`
	After     string     `json:"after,omitempty"`
	Outcome   string     `json:"outcome,omitempty"`
	Result    string     `json:"result,omitempty"`
	Item      string     `json:"item,omitempty"`
	Line      string     `json:"line"`
}

// When is the row's time for sorting and windows: its exact time, else the read after it.
func (r AuditRow) When() time.Time {
	if r.At != nil {
		return *r.At
	}
	if r.To != nil {
		return *r.To
	}
	return time.Time{}
}

// AuditInputs are the sources of the list.
type AuditInputs struct {
	Changes []SettingChange // DiffBaselines of every pair of reads
	Actions []AuditAction   // kakapo's own log
	Seen    []Change        // the change log's rows seen on the server and recorded by hand
	Context FlagContext
}

func when(t time.Time) string { return t.UTC().Format("2006-01-02 15:04") + " UTC" }

var actionOutcomeWords = map[string]string{
	OutcomeVerified: "done and read back",
	OutcomeDiffers:  "done, but the value read back differs from the target",
	OutcomeFailed:   "failed",
	OutcomeStopped:  "stopped",
	OutcomeUnknown:  "outcome unknown: kakapo stopped while it ran",
}

// BuildAuditRows merges the sources into one list, newest first (C2.3): every setting difference attributed
// by A5 (the action or trace row it is attributed to is not listed again), every other action of kakapo's,
// and every other change seen on the server or recorded by hand.
func BuildAuditRows(in AuditInputs) []AuditRow {
	out := []AuditRow{}
	usedAction, usedChange := map[string]bool{}, map[int64]bool{}
	actionBy := map[string]AuditAction{}
	for _, a := range in.Actions {
		actionBy[a.Start.ActionID] = a
	}
	for i, c := range in.Changes {
		at := AttributeSettingChange(c, in.Actions, in.Seen)
		before, after := c.Before, c.After
		row := AuditRow{Key: fmt.Sprintf("s%d-%s", i, c.Item), Headline: c.Headline(), Who: at.Who, Source: SourceBaseline,
			Database: c.Database(), Item: c.Item, Before: valueWords(c.Item, c.From), After: valueWords(c.Item, c.To), Screens: []string{}}
		switch at.Kind {
		case ByKakapo:
			usedAction[at.ActionID] = true
			row.ByKakapo, row.At, row.Source = true, at.At, SourceKakapo
			if a, ok := actionBy[at.ActionID]; ok {
				row.Statement, row.Rollback, row.Outcome = a.Start.Statement, a.Start.Rollback, a.Outcome()
				if a.End != nil {
					row.Result = a.End.Result
				}
			}
		case ByTrace:
			usedChange[at.ChangeID] = true
			row.At, row.Source = at.At, SourceTrace
		default:
			row.From, row.To = &before, &after
		}
		if h := RuleForSetting(c, in.Context); h != nil {
			row.Flag, row.Rule, row.Tell, row.Screens = h.Flag, h.Rule, h.Tell, h.Screens
		}
		out = append(out, row)
	}
	for _, a := range in.Actions {
		if usedAction[a.Start.ActionID] {
			continue
		}
		r := a.Start
		at := r.StartedAt
		db := r.Database
		if actionScope(r) == ScopeServer {
			db = ""
		}
		row := AuditRow{Key: "a" + r.ActionID, At: &at, Headline: r.Description, Who: kakapoWho(r), ByKakapo: true, Source: SourceKakapo,
			Database: db, Statement: r.Statement, Rollback: r.Rollback, Before: r.ValueBefore, Outcome: a.Outcome(), Result: r.Result, Screens: []string{}}
		if a.End != nil {
			row.After, row.Result = a.End.ValueAfter, a.End.Result
		}
		if h := RuleForAction(r); h != nil {
			row.Flag, row.Rule, row.Tell, row.Screens = h.Flag, h.Rule, h.Tell, h.Screens
			if db == "" {
				row.Screens = []string{ScreenMemory}
			}
		}
		out = append(out, row)
	}
	for _, c := range in.Seen {
		if usedChange[c.ID] {
			continue
		}
		at := c.ChangedAt.UTC()
		row := AuditRow{Key: fmt.Sprintf("c%d", c.ID), At: &at, Headline: c.Description, Database: c.Database, Statement: c.Statement, Screens: []string{}}
		switch c.Result {
		case ChangeRecordedByHand:
			row.Source, row.Who = SourceByHand, "Recorded by hand in kakapo"
		default:
			row.Source = SourceTrace
			row.Who = "Seen in SQL Server's trace"
			if what, w := seenSplit(c.Description); w != "" {
				row.Headline, row.Who = what, "By "+w+", from SQL Server's trace"
			}
		}
		if sc, ok := seenSettingChange(c); ok {
			row.Item, row.Before, row.After = sc.Item, valueWords(sc.Item, sc.From), valueWords(sc.Item, sc.To)
			if h := RuleForSetting(sc, in.Context); h != nil {
				row.Flag, row.Rule, row.Tell, row.Screens = h.Flag, h.Rule, h.Tell, h.Screens
			}
		}
		out = append(out, row)
	}
	for i := range out {
		out[i].Line = auditLine(out[i])
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].When().After(out[j].When()) })
	return out
}

// auditLine is a row as one line of the Copy text.
func auditLine(r AuditRow) string {
	t := ""
	switch {
	case r.At != nil:
		t = when(*r.At)
	case r.From != nil && r.To != nil:
		t = "between " + when(*r.From) + " and " + when(*r.To)
	}
	flag := ""
	if r.Flag == FlagRed || r.Flag == FlagAmber {
		flag = " [" + strings.ToUpper(r.Flag) + "]"
	}
	s := t + flag + " " + r.Headline + ". " + r.Who
	if r.Outcome != "" {
		s += "; " + actionOutcomeWords[r.Outcome]
	}
	return strings.TrimSpace(s)
}

// FilterAuditRows keeps the rows of one filter (C2.3): who is "" (all), "kakapo" or "outside"; flagged keeps
// red and amber only; from and to bound the row's time (nil is open).
func FilterAuditRows(rows []AuditRow, who string, flagged bool, from, to *time.Time) []AuditRow {
	out := []AuditRow{}
	for _, r := range rows {
		switch {
		case who == "kakapo" && !r.ByKakapo, who == "outside" && r.ByKakapo:
			continue
		case flagged && r.Flag != FlagRed && r.Flag != FlagAmber:
			continue
		case from != nil && r.When().Before(*from), to != nil && r.When().After(*to):
			continue
		}
		out = append(out, r)
	}
	return out
}

// ProblemWindow is the 7 days before a problem's time (C2.1).
const ProblemWindow = 7 * 24 * time.Hour

// BuildAuditVerdict is the verdict line (C2.1), one sentence, with its tone. With a problem time, rows are
// the changes in the 7 days before it and label says the time; without, rows are every change and
// watchedSince is when kakapo first read this server's settings (nil: never).
func BuildAuditVerdict(rows []AuditRow, problem bool, label string, watchedSince *time.Time) (string, string) {
	reds, ambers, kakapo := []string{}, 0, 0
	for _, r := range rows {
		switch r.Flag {
		case FlagRed:
			reds = append(reds, r.Headline)
		case FlagAmber:
			ambers++
		}
		if r.ByKakapo {
			kakapo++
		}
	}
	tone := FlagGrey
	switch {
	case len(reds) > 0:
		tone = FlagRed
	case ambers > 0:
		tone = FlagAmber
	}
	if problem {
		if len(rows) == 0 {
			return "No kakapo action and no setting change in the 7 days before " + label + ".", "green"
		}
		s := plural(int64(len(rows)), "change", "changes") + " in the 7 days before " + label
		if len(reds) == 0 {
			return s + ", none of them red.", tone
		}
		return s + ", " + strconv.Itoa(len(reds)) + " of them red: " + strings.Join(reds, "; ") + ".", tone
	}
	if watchedSince == nil {
		return "kakapo has not read this server's settings yet: " + plural(int64(len(rows)), "change", "changes") +
			" from kakapo's own log and SQL Server's history.", tone
	}
	return "Since kakapo started watching this server on " + watchedSince.UTC().Format("2 Jan 2006") + ": " +
		plural(int64(len(rows)), "change", "changes") + ", " + strconv.Itoa(kakapo) + " of them by kakapo.", tone
}

// AuditCopy is the Copy text for a ticket: the verdict, then every listed change as one line each.
func AuditCopy(verdict string, rows []AuditRow) string {
	lines := []string{verdict, ""}
	for _, r := range rows {
		lines = append(lines, r.Line)
	}
	return strings.Join(lines, "\n")
}

// --- the memory line ---

// MemoryPoint is one read's SQL Server memory in use and target, and the cap in force (nil: no cap or not
// read).
type MemoryPoint struct {
	At       time.Time `json:"at"`
	SQLMB    *int64    `json:"sqlMB,omitempty"`
	TargetMB *int64    `json:"targetMB,omitempty"`
	CapMB    *int64    `json:"capMB,omitempty"`
}

// MemoryGapAfter is the longest time between two reads that is not said as a gap.
const MemoryGapAfter = 48 * time.Hour

// MemoryGaps says where the memory line has no readings: "kakapo was not opened between 2 Nov and 9 Nov".
func MemoryGaps(points []MemoryPoint) []string {
	out := []string{}
	for i := 1; i < len(points); i++ {
		a, b := points[i-1].At.UTC(), points[i].At.UTC()
		if b.Sub(a) > MemoryGapAfter {
			out = append(out, "kakapo was not opened between "+a.Format("2 Jan")+" and "+b.Format("2 Jan")+", so there is no reading in between.")
		}
	}
	return out
}

// CapMB is max server memory from a snapshot: nil for no cap or not read.
func CapMB(items map[string]*string) *int64 {
	v := items[ItemMaxServerMemory]
	if v == nil {
		return nil
	}
	n, ok := num(*v)
	if !ok || n >= UnlimitedMemoryMB {
		return nil
	}
	return &n
}

// ChangesPage is the What changed? screen (C2): the verdict, the memory line and one page of the merged
// list, with the counts the filters show, and every failure as a sentence.
type ChangesPage struct {
	ServerName   string        `json:"serverName"`
	Verdict      string        `json:"verdict"`
	Tone         string        `json:"tone"`
	ProblemAt    *time.Time    `json:"problemAt,omitempty"`
	ProblemLabel string        `json:"problemLabel,omitempty"`
	From         *time.Time    `json:"from,omitempty"`
	To           *time.Time    `json:"to,omitempty"`
	WatchedSince *time.Time    `json:"watchedSince,omitempty"`
	Live         bool          `json:"live"`
	Notes        []string      `json:"notes"`
	Memory       MemoryLine    `json:"memory"`
	Rows         []AuditRow    `json:"rows"`
	Page         int           `json:"page"`
	PageSize     int           `json:"pageSize"`
	More         bool          `json:"more"`
	Listed       int           `json:"listed"`
	Counts       ChangesCounts `json:"counts"`
	Copy         string        `json:"copy"`
}

// ChangesCounts are the rows of the window per filter.
type ChangesCounts struct {
	All     int `json:"all"`
	Kakapo  int `json:"kakapo"`
	Outside int `json:"outside"`
	Flagged int `json:"flagged"`
}

// MemoryLine is the memory line above the list (C2.2): SQL Server's memory in use, its target and the cap at
// every read, the Memory screen's snapshots as points, and the gaps in words.
type MemoryLine struct {
	Points    []MemoryPoint     `json:"points"`
	Snapshots []MemorySnapPoint `json:"snapshots"`
	Gaps      []string          `json:"gaps"`
}

// MemorySnapPoint is a saved Memory screen snapshot on the line.
type MemorySnapPoint struct {
	ID int64     `json:"id"`
	At time.Time `json:"at"`
}

// CountAuditRows counts the rows per filter.
func CountAuditRows(rows []AuditRow) ChangesCounts {
	c := ChangesCounts{All: len(rows)}
	for _, r := range rows {
		if r.ByKakapo {
			c.Kakapo++
		} else {
			c.Outside++
		}
		if r.Flag == FlagRed || r.Flag == FlagAmber {
			c.Flagged++
		}
	}
	return c
}

// SettingChangesOf diffs every pair of consecutive reads that point at different snapshots, with the
// actions whose before and after snapshots the reads were.
func SettingChangesOf(reads []BaselineReadRow, baselines map[int64]Baseline) []SettingChange {
	out := []SettingChange{}
	for i := 1; i < len(reads); i++ {
		a, b := reads[i-1], reads[i]
		if a.BaselineID == b.BaselineID {
			continue
		}
		prev, ok1 := baselines[a.BaselineID]
		next, ok2 := baselines[b.BaselineID]
		if !ok1 || !ok2 {
			continue
		}
		for _, c := range DiffBaselines(prev, next, a.ReadAt, b.ReadAt) {
			if a.Reason == BaselineBeforeAction {
				c.BeforeAction = a.ActionID
			}
			if b.Reason == BaselineAfterAction {
				c.AfterAction = b.ActionID
			}
			out = append(out, c)
		}
	}
	return out
}
