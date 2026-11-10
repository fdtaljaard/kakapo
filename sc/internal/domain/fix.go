package domain

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

// Fix scopes.
const (
	ScopeServer   = "server"
	ScopeDatabase = "database"
)

// Fix is a named change the app can make, derived from a finding (docs/phase3c-contract.md).
type Fix struct {
	ID           string `json:"id"`
	Title        string `json:"title"`
	Scope        string `json:"scope"` // server, database
	CheckID      string `json:"checkId"`
	Setting      string `json:"setting"` // "max server memory (MB)", "AUTO_CLOSE"
	Current      string `json:"current"` // "2147483647 MB", "ON"
	Target       string `json:"target"`  // "26624 MB", "OFF"
	NeedsRestart bool   `json:"needsRestart"`
	// Options are the values the preview needs to build this particular fix (a finding's table, flag,
	// index columns, session id): the UI sends them back as the preview's options
	// (docs/phase4c-contract.md). Empty for a fix with no such values.
	Options map[string]string `json:"options,omitempty"`
}

// FixResult is a generic fix that ran: the preview it ran, the value read before and after, and
// whether the value read after is the target. A long fix (docs/phase4f-contract.md) answers at
// once with Pending, its RunID and StartedAt and the preview: the outcome is the run's result.
type FixResult struct {
	Preview   FixPreview `json:"preview"`
	Before    string     `json:"before"`
	After     string     `json:"after"`
	Verified  bool       `json:"verified"`
	ChangeID  int64      `json:"changeId"`
	AppliedAt time.Time  `json:"appliedAt"`
	Pending   bool       `json:"pending,omitempty"`
	RunID     string     `json:"runId,omitempty"`
	StartedAt *time.Time `json:"startedAt,omitempty"`
}

// Errors of BuildFix. Both mean there is nothing to apply: the API answers 404.
var (
	ErrUnknownFix       = errors.New("unknown fix")
	ErrFixNotApplicable = errors.New("the finding behind this fix did not fire")
)

// FixQueryStore is the id of the phase 1 Query Store fix, reachable through the generic routes.
const FixQueryStore = "query-store"

// FixDef is what the app knows about one fix before anything is read.
type FixDef struct {
	ID      string
	Title   string
	Scope   string
	CheckID string
	Setting string // the sp_configure name, or the database option
}

// FixDefs are the fixes the app offers, in the order the contract lists them.
var FixDefs = []FixDef{
	{"max-server-memory", "Max server memory", ScopeServer, "INS02", "max server memory (MB)"},
	{"maxdop", "Max degree of parallelism", ScopeServer, "INS04", "max degree of parallelism"},
	{"cost-threshold", "Cost threshold for parallelism", ScopeServer, "INS05", "cost threshold for parallelism"},
	{"optimize-adhoc", "Optimize for ad hoc workloads", ScopeServer, "INS06", "optimize for ad hoc workloads"},
	{"backup-compression", "Backup compression default", ScopeServer, "INS10", "backup compression default"},
	{"auto-close", "AUTO_CLOSE off", ScopeDatabase, "DBC02", "AUTO_CLOSE"},
	{"auto-shrink", "AUTO_SHRINK off", ScopeDatabase, "DBC03", "AUTO_SHRINK"},
	{"page-verify", "Page verify CHECKSUM", ScopeDatabase, "DBC04", "PAGE_VERIFY"},
	{"auto-statistics", "Automatic statistics on", ScopeDatabase, "DBC05", "AUTO_CREATE_STATISTICS, AUTO_UPDATE_STATISTICS"},
	{"indirect-checkpoints", "Indirect checkpoints", ScopeDatabase, "DBC09", "TARGET_RECOVERY_TIME"},
	{FixQueryStore, "Query Store", ScopeDatabase, "DBC10", "QUERY_STORE"},
	{FixUpdateStatistics, "Update statistics", ScopeDatabase, "MNT04", "Statistics"},
	// Phase 4c (docs/phase4c-contract.md): the Performance card's actions, built through the registry.
	{"create-index", "Create the suggested index", ScopeDatabase, "WRK03", "Index"},
	{"drop-index", "Drop an index", ScopeDatabase, "WRK03", "Index"},
	// Phase 5o (docs/phase5o-contract.md, section 3): IDX04's safe copy, disabled so it can be rebuilt.
	{FixDisableIndex, "Disable a duplicate index", ScopeDatabase, "IDX04", "Index"},
	// Phase 5q (docs/phase5q-contract.md, section 4): an index a query reads in full, reorganised.
	{FixReorganizeIndex, "Reorganise an index", ScopeDatabase, "QRY01", "Index"},
	{"kill-session", "Kill the blocking session", ScopeServer, "ACT01", "Session"},
	// Phase 5m (docs/phase5m-contract.md, section 4): the row versioning decision's fix.
	{"rcsi", "Turn on READ COMMITTED SNAPSHOT", ScopeDatabase, "DEC01", "READ_COMMITTED_SNAPSHOT"},
	{"force-plan", "Force the faster plan", ScopeDatabase, "QRY01", "Plan"},
	{"unforce-plan", "Stop forcing the plan", ScopeDatabase, "QRY01", "Plan"},
	{"evict-plan", "Remove the plan from the cache", ScopeServer, "QRY01", "Plan"},
	{"update-statistics-table", "Update statistics on one table", ScopeDatabase, "QRY01", "Statistics"},
	// Phase 4d (docs/phase4d-contract.md): the other cards' remaining actions.
	{"multi-user", "Open the database to everyone (MULTI_USER)", ScopeDatabase, "DBC13", "user_access"},
	{"reconfigure", "Activate or revert the pending server settings", ScopeServer, "INS14", "RECONFIGURE"},
	{"trace-flag-off", "Turn a trace flag off", ScopeServer, "INS15", "Trace flag"},
	{"scoped-config-reset", "Reset a database scoped configuration", ScopeDatabase, "DBC14", "Scoped configuration"},
	{"compat-level", "Raise the compatibility level", ScopeDatabase, "DBC06", "COMPATIBILITY_LEVEL"},
	{"min-server-memory", "Min server memory back to 0", ScopeServer, "INS16", "min server memory (MB)"},
	{"operator-alerts", "Create the operator and alerts", ScopeServer, "MNT07", "Alerts"},
	{"maintenance-job", "Create the statistics job", ScopeServer, "MNT03", "Maintenance job"},
	{"remove-shrink-step", "Remove the shrink step", ScopeServer, "MNT05", "Job step"},
	{"autogrowth", "Set a fixed autogrowth", ScopeDatabase, "STO02", "FILEGROWTH"},
	{"add-tempdb-files", "Add TempDB data files", ScopeServer, "TMP01", "TempDB files"},
	{"recovery-model", "Change the recovery model", ScopeDatabase, "DBC08", "RECOVERY"},
	{"checkdb-job", "Create the weekly integrity check", ScopeServer, "BAK03", "CHECKDB job"},
	{"checkdb-now", "Run DBCC CHECKDB now", ScopeDatabase, "BAK03", "CHECKDB"},
	{"suspect-pages-clear", "Clear the old suspect-page records", ScopeServer, "BAK04", "suspect_pages"},
	// Phase 4e (docs/phase4e-contract.md): the TempDB file set in one fix.
	{"tempdb-files", "Set up the TempDB files", ScopeServer, "TMP02", "TempDB files"},
	// Phase 5a (docs/phase5a-contract.md): archive or delete the old rows of a table that only grows.
	{"archive-table", "Archive or delete old rows", ScopeDatabase, "IDX10", "Archive"},
	{"remove-archive", "Stop archiving a table", ScopeDatabase, "IDX10", "Archive"},
	// Phase 5b (docs/phase5b-contract.md): the maintenance plan.
	{"history-cleanup", "Prune msdb history weekly", ScopeServer, "MNT08", "msdb history"},
	{"maintenance-plan", "Set up the maintenance plan", ScopeServer, "MNT03", "Maintenance plan"},
	// Phase 5e (docs/phase5e-contract.md): the first installed helper, an Extended Events session.
	{"install-xe-session", "Keep every block and deadlock on the server", ScopeServer, "BLK01", "Event session kakapo"},
	{"remove-xe-session", "Remove kakapo's event session", ScopeServer, "BLK01", "Event session kakapo"},
	// Phase 5f (docs/phase5f-contract.md): the default trace back on, for the change log.
	{FixEnableDefaultTrace, "Switch SQL Server's default trace back on", ScopeServer, "CHG01", DefaultTraceSetting},
	{FixBackupSchedule, "Create a backup schedule", ScopeDatabase, "BAK01", "Backup schedule"},
	{FixRemoveBackupSchedule, "Remove kakapo's backup jobs", ScopeDatabase, "BAK01", "Backup schedule"},
	// Phase 5p (docs/phase5p-contract.md): the maintenance plan as one procedure, for SQL Server Express.
	{FixInstallMaintenanceProc, "Install the maintenance procedure", ScopeServer, "MNT03", "Maintenance procedure"},
	{FixRemoveMaintenanceProc, "Remove kakapo's maintenance procedure", ScopeServer, "MNT03", "Maintenance procedure"},
}

// Card is the card whose finding offers this fix.
func (d FixDef) Card() CardID {
	c, _ := CheckByID(d.CheckID)
	return c.Card
}

// FindFixDef returns a fix by its id.
func FindFixDef(id string) (FixDef, bool) {
	for _, d := range FixDefs {
		if d.ID == id {
			return d, true
		}
	}
	return FixDef{}, false
}

// Warnings shared by the server fixes.
const (
	warnPlanCache  = "SQL Server clears its plan cache: queries recompile for a few minutes."
	warnMemoryRoom = "The suggestion leaves room for Windows only. If other software runs on this server (another instance, SSRS, a web shop, the application's own services), set a lower value by hand."
	warnThirdParty = "Ignore if a third-party tool takes the backups."
	warnPageVerify = "Pages are protected as they are next written; an index rebuild protects them all."
	warnVendor     = "Check first that the application vendor does not require this (rare)."
)

func warnServer(server string) string {
	return "This is a server setting: it changes every database on " + server + "."
}

// warnPending is the warning of a server fix whose RECONFIGURE also activates settings someone else
// changed and left pending (INS14).
func warnPending(ps []PendingConfig) string {
	return "RECONFIGURE also activates these pending changes: " + PendingList(ps) + "."
}

// permission is the line a preview ends its warnings with: the permission the change needs.
func permission(scope, dbName string) string {
	if scope == ScopeServer {
		return "Needs the ALTER SETTINGS permission on the server (sysadmin or serveradmin)."
	}
	return "Needs the ALTER permission on the database " + dbName + "."
}

// PermissionHint is what a refused fix names: the permission its scope needs.
func PermissionHint(scope, dbName string) string { return permission(scope, dbName) }

// spConfigure is the statement that sets one server configuration, as the scan and Apply-DoNow
// write it.
func spConfigure(name string, value int64) string {
	return fmt.Sprintf("EXEC sp_configure 'show advanced options', 1; RECONFIGURE; EXEC sp_configure '%s', %d; RECONFIGURE;", name, value)
}

// ConfigValue writes a server configuration value as a fix shows it: "26624 MB" for max server
// memory, the number otherwise.
func ConfigValue(fixID string, v int64) string {
	if fixID == "max-server-memory" {
		return fmt.Sprintf("%d MB", v)
	}
	return fmt.Sprint(v)
}

// DatabaseOptions are the sys.databases columns the database fixes change.
type DatabaseOptions struct {
	AutoClose         bool
	AutoShrink        bool
	PageVerify        string
	AutoCreateStats   bool
	AutoUpdateStats   bool
	TargetRecoverySec int
}

// Options are the database options as read with the setup facts.
func (f SetupFacts) Options() DatabaseOptions {
	return DatabaseOptions{AutoClose: f.AutoClose, AutoShrink: f.AutoShrink, PageVerify: f.PageVerify,
		AutoCreateStats: f.AutoCreateStats, AutoUpdateStats: f.AutoUpdateStats, TargetRecoverySec: f.TargetRecoverySec}
}

func onOff(v bool) string {
	if v {
		return "ON"
	}
	return "OFF"
}

// DatabaseValue writes the value a database fix changes as the fix shows it: "ON", "CHECKSUM",
// "60 SECONDS", "create ON, update OFF".
func DatabaseValue(fixID string, o DatabaseOptions) string {
	switch fixID {
	case "auto-close":
		return onOff(o.AutoClose)
	case "auto-shrink":
		return onOff(o.AutoShrink)
	case "page-verify":
		return o.PageVerify
	case "auto-statistics":
		return "create " + onOff(o.AutoCreateStats) + ", update " + onOff(o.AutoUpdateStats)
	case "indirect-checkpoints":
		return fmt.Sprintf("%d SECONDS", o.TargetRecoverySec)
	}
	return ""
}

// ConfigFor is the current value of a server fix's configuration in the facts.
func (f SetupFacts) ConfigFor(fixID string) int64 {
	switch fixID {
	case "max-server-memory":
		return f.MaxServerMemoryMB
	case "maxdop":
		return int64(f.MaxDop)
	case "cost-threshold":
		return int64(f.CostThreshold)
	case "optimize-adhoc":
		return int64(f.OptimizeAdHoc)
	case "backup-compression":
		return int64(f.BackupCompression)
	}
	return 0
}

// AvailableFixes are the fixes of the findings that fired on this read (BuildSetupCard's), in the
// order the findings rank. The registered actions of docs/phase4d-contract.md (trace-flag-off,
// scoped-config-reset, ...) are listed once per finding with the options the preview needs.
func AvailableFixes(f SetupFacts, findings []Finding) []Fix {
	out := []Fix{}
	seen := map[string]bool{}
	for _, x := range findings {
		if x.FixID == "" {
			continue
		}
		if isSetupAction(x.FixID) {
			fix, ok := setupActionFix(f, x)
			key := x.FixID + "\x00" + fix.Setting
			if ok && !seen[key] {
				seen[key] = true
				out = append(out, fix)
			}
			continue
		}
		if seen[x.FixID] {
			continue
		}
		seen[x.FixID] = true
		if _, fix, err := buildFix(x.FixID, f, f.Database); err == nil {
			out = append(out, fix)
		}
	}
	return out
}

// BuildFix builds the preview and the fix for one fix id from what was read: the statement from
// the read values, never from client input, the rollback that puts the previous value back, and
// the warnings. qs is the database's Query Store status for the query-store fix (it replaces the
// one in the facts when given). ErrUnknownFix for an id the app does not know,
// ErrFixNotApplicable when the finding behind it did not fire (or the database cannot be changed
// from here, so the finding carries no fix).
func BuildFix(fixID string, f SetupFacts, dbName string, qs *QueryStoreStatus) (FixPreview, Fix, error) {
	if d, ok := FindFixDef(fixID); !ok {
		return FixPreview{}, Fix{}, ErrUnknownFix
	} else if d.Card() != CardSetup {
		return FixPreview{}, Fix{}, ErrFixNotApplicable // another card's fix (BuildUpkeepFix)
	}
	if qs != nil {
		f.DB.QueryStore = qs
	}
	if dbName != "" {
		f.Database = dbName
	}
	def, _ := FindCardDef(CardSetup)
	_, findings, _ := BuildSetupCard(f, def)
	fired := false
	for _, x := range findings {
		fired = fired || x.FixID == fixID
	}
	if !fired {
		return FixPreview{}, Fix{}, ErrFixNotApplicable
	}
	return buildFix(fixID, f, f.Database)
}

func buildFix(fixID string, f SetupFacts, dbName string) (FixPreview, Fix, error) {
	d, ok := FindFixDef(fixID)
	if !ok || d.Card() != CardSetup {
		return FixPreview{}, Fix{}, ErrUnknownFix
	}
	fix := Fix{ID: d.ID, Title: d.Title, Scope: d.Scope, CheckID: d.CheckID, Setting: d.Setting}
	p := FixPreview{Database: dbName, EngineVersion: f.ProductVersion, Changes: []Drift{}, Warnings: []string{}}
	if d.Scope == ScopeServer {
		p.Database = ""
	}
	db := QuoteName(dbName)

	switch d.Scope {
	case ScopeServer:
		cur := f.ConfigFor(fixID)
		var target int64
		switch fixID {
		case "max-server-memory":
			target = SuggestedMaxMemoryMB(f.PhysicalMB)
			fix.CheckID = "INS02"
			if f.MaxServerMemoryMB < unlimitedMemory {
				fix.CheckID = "INS03"
			}
		case "maxdop":
			target = int64(f.SuggestedMaxDop())
		case "cost-threshold":
			target = 50
		case "optimize-adhoc", "backup-compression":
			target = 1
		}
		fix.Current, fix.Target = ConfigValue(fixID, cur), ConfigValue(fixID, target)
		p.Statement = spConfigure(d.Setting, target)
		p.Rollback = spConfigure(d.Setting, cur)
		p.Warnings = append(p.Warnings, warnServer(f.ServerName))
		switch fixID {
		case "backup-compression":
			p.Warnings = append(p.Warnings, warnThirdParty)
		case "max-server-memory":
			p.Warnings = append(p.Warnings, warnPlanCache, warnMemoryRoom)
		default:
			p.Warnings = append(p.Warnings, warnPlanCache)
		}
		if pend := f.PendingOtherThan(d.Setting); len(pend) > 0 {
			p.Warnings = append(p.Warnings, warnPending(pend))
		}

	case ScopeDatabase:
		o := f.Options()
		fix.Current = DatabaseValue(fixID, o)
		t := o
		switch fixID {
		case "auto-close":
			t.AutoClose = false
			p.Statement = "ALTER DATABASE " + db + " SET AUTO_CLOSE OFF;"
			p.Rollback = "ALTER DATABASE " + db + " SET AUTO_CLOSE ON;"
		case "auto-shrink":
			t.AutoShrink = false
			p.Statement = "ALTER DATABASE " + db + " SET AUTO_SHRINK OFF;"
			p.Rollback = "ALTER DATABASE " + db + " SET AUTO_SHRINK ON;"
		case "page-verify":
			t.PageVerify = "CHECKSUM"
			p.Statement = "ALTER DATABASE " + db + " SET PAGE_VERIFY CHECKSUM;"
			if sqlWord.MatchString(o.PageVerify) {
				p.Rollback = "ALTER DATABASE " + db + " SET PAGE_VERIFY " + o.PageVerify + ";"
			} else {
				p.Warnings = append(p.Warnings, fmt.Sprintf("The previous value of PAGE_VERIFY (%q) is not in the rollback; set it by hand if you roll back.", o.PageVerify))
			}
			p.Warnings = append(p.Warnings, warnPageVerify)
		case "auto-statistics":
			t.AutoCreateStats, t.AutoUpdateStats = true, true
			set, back, names := []string{}, []string{}, []string{}
			if !o.AutoCreateStats {
				set = append(set, "ALTER DATABASE "+db+" SET AUTO_CREATE_STATISTICS ON;")
				back = append(back, "ALTER DATABASE "+db+" SET AUTO_CREATE_STATISTICS OFF;")
				names = append(names, "AUTO_CREATE_STATISTICS")
			}
			if !o.AutoUpdateStats {
				set = append(set, "ALTER DATABASE "+db+" SET AUTO_UPDATE_STATISTICS ON;")
				back = append(back, "ALTER DATABASE "+db+" SET AUTO_UPDATE_STATISTICS OFF;")
				names = append(names, "AUTO_UPDATE_STATISTICS")
			}
			fix.Setting = strings.Join(names, ", ")
			p.Statement, p.Rollback = strings.Join(set, " "), strings.Join(back, " ")
			p.Warnings = append(p.Warnings, warnVendor)
		case "indirect-checkpoints":
			t.TargetRecoverySec = 60
			p.Statement = "ALTER DATABASE " + db + " SET TARGET_RECOVERY_TIME = 60 SECONDS;"
			p.Rollback = fmt.Sprintf("ALTER DATABASE %s SET TARGET_RECOVERY_TIME = %d SECONDS;", db, o.TargetRecoverySec)
		case FixQueryStore:
			q := f.DB.QueryStore
			if q == nil {
				return FixPreview{}, Fix{}, ErrFixNotApplicable
			}
			qp := BuildQueryStoreFix(q.Options(), f.Major(), dbName)
			qp.EngineVersion = f.ProductVersion
			if qp.Statement == "" {
				return FixPreview{}, Fix{}, ErrFixNotApplicable
			}
			fix.Current, fix.Target = q.Summary, "recommended settings"
			qp.Warnings = append(qp.Warnings, permission(d.Scope, dbName))
			return qp, fix, nil
		}
		fix.Target = DatabaseValue(fixID, t)
	}

	if fixID == "auto-statistics" {
		// One row per option that changes.
		if !f.AutoCreateStats {
			p.Changes = append(p.Changes, Drift{Setting: "AUTO_CREATE_STATISTICS", Current: "OFF", Target: "ON"})
		}
		if !f.AutoUpdateStats {
			p.Changes = append(p.Changes, Drift{Setting: "AUTO_UPDATE_STATISTICS", Current: "OFF", Target: "ON"})
		}
	} else {
		p.Changes = append(p.Changes, Drift{Setting: fix.Setting, Current: fix.Current, Target: fix.Target})
	}
	p.Warnings = append(p.Warnings, permission(d.Scope, dbName))
	return p, fix, nil
}

// FixChangeDescription is the change log line for a fix: "{setting} on {server or database}:
// {current} -> {target}". For a server fix, pending are the other settings its RECONFIGURE
// activated (SetupFacts.PendingOtherThan); they are named after "; RECONFIGURE also activated: ".
func FixChangeDescription(fix Fix, serverName, dbName string, pending ...PendingConfig) string {
	on := dbName
	if fix.Scope == ScopeServer {
		on = serverName
	}
	s := fmt.Sprintf("%s on %s: %s -> %s", fix.Setting, on, fix.Current, fix.Target)
	if fix.Scope == ScopeServer && len(pending) > 0 {
		s += "; RECONFIGURE also activated: " + PendingList(pending)
	}
	return s
}
