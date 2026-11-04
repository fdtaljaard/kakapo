package domain

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// The Setup card's actions (docs/phase4d-contract.md, part 1), each built through the fix registry
// from what was read just before, never from the options alone: the options only say which flag,
// configuration or action. multi-user opens a database left in SINGLE_USER or RESTRICTED_USER
// mode (DBC13), reconfigure activates or reverts the settings waiting for RECONFIGURE (INS14),
// trace-flag-off turns one global trace flag off (INS15), scoped-config-reset puts one database
// scoped configuration back to its default (DBC14) and min-server-memory sets min server memory
// back to 0 (INS16). A preview that cannot be applied (a wrong option, someone working in the
// database) has warnings and no changes, so Apply is disabled.

// Ids of the Setup card's actions.
const (
	FixMultiUser         = "multi-user"
	FixReconfigure       = "reconfigure"
	FixTraceFlagOff      = "trace-flag-off"
	FixScopedConfigReset = "scoped-config-reset"
	FixMinServerMemory   = "min-server-memory"
)

// The options of the Setup card's actions.
const (
	OptionReconfigureAction = "action"  // reconfigure: activate or revert
	OptionPendingSeen       = "pending" // reconfigure: the pending settings the preview was built from
	OptionTraceFlag         = "flag"    // trace-flag-off: the flag number
	OptionScopedName        = "name"    // scoped-config-reset: the configuration
)

// The reconfigure actions, the select's options.
const (
	ReconfigureActivate = "activate"
	ReconfigureRevert   = "revert"
)

// ReconfigureChoices are the values of the reconfigure fix's action select.
var ReconfigureChoices = []string{ReconfigureActivate, ReconfigureRevert}

const minServerMemorySetting = "min server memory (MB)"

// suFix is the Fix of a fix id, without its values.
func suFix(id string) Fix {
	d, _ := FindFixDef(id)
	return Fix{ID: d.ID, Title: d.Title, Scope: d.Scope, CheckID: d.CheckID, Setting: d.Setting}
}

// suSQLString is a T-SQL (non-Unicode) string literal, as sp_configure's name is written.
func suSQLString(s string) string { return "'" + strings.ReplaceAll(s, "'", "''") + "'" }

// --- multi-user (DBC13) ---

// UserAccessSession is another session using a database: who, and what it runs now.
type UserAccessSession struct {
	Session int
	Login   string
	Host    string
	Program string
	Command string // the running request's command (DBCC, RESTORE DATABASE, SELECT); "" when idle
}

// UserAccessFacts is what the multi-user fix reads about one database.
type UserAccessFacts struct {
	ServerName     string
	ProductVersion string
	Database       string
	UserAccess     string              // user_access_desc: MULTI_USER, SINGLE_USER, RESTRICTED_USER
	Sessions       []UserAccessSession // the other sessions in the database, at most 10
	SessionsError  string              // who is connected could not be read
}

// busy is the session running a restore or a DBCC command in the database, which the fix must
// not interrupt.
func (f UserAccessFacts) busy() (UserAccessSession, bool) {
	for _, s := range f.Sessions {
		c := strings.ToUpper(strings.TrimSpace(s.Command))
		if strings.HasPrefix(c, "DBCC") || strings.HasPrefix(c, "RESTORE") {
			return s, true
		}
	}
	return UserAccessSession{}, false
}

func (s UserAccessSession) words() string {
	w := fmt.Sprintf("session %d (%s on %s", s.Session, orQ(s.Login), orQ(s.Host))
	if s.Program != "" {
		w += ", " + s.Program
	}
	return w + ")"
}

// BuildMultiUserFix builds the multi-user preview: ALTER DATABASE ... SET MULTI_USER WITH ROLLBACK
// IMMEDIATE, the mode it had as the rollback. ErrFixNotApplicable when the database is open to
// everyone already. A session running DBCC or a restore in it blocks the fix.
func BuildMultiUserFix(f UserAccessFacts) (FixPreview, Fix, error) {
	mode := strings.ToUpper(strings.TrimSpace(f.UserAccess))
	if mode != "SINGLE_USER" && mode != "RESTRICTED_USER" {
		return FixPreview{}, Fix{}, ErrFixNotApplicable
	}
	db := QuoteName(f.Database)
	fix := suFix(FixMultiUser)
	fix.Current, fix.Target = mode, "MULTI_USER"
	p := FixPreview{Database: f.Database, EngineVersion: f.ProductVersion, Changes: []Drift{}, Warnings: []string{}}
	blocked := false
	if s, ok := f.busy(); ok {
		blocked = true
		p.Warnings = append(p.Warnings, fmt.Sprintf("%s is running %s in %s now: kakapo does not interrupt it. Wait until it finishes, then preview again.",
			upperFirst(s.words()), s.Command, f.Database))
	}
	p.Warnings = append(p.Warnings, fmt.Sprintf("Whoever set %s mode may be in the middle of a restore or a repair: ask them first.", mode))
	switch {
	case f.SessionsError != "":
		p.Warnings = append(p.Warnings, "kakapo could not see who is connected to "+f.Database+" ("+f.SessionsError+"): ask before you apply.")
	case len(f.Sessions) == 0:
		p.Warnings = append(p.Warnings, "Nobody else is connected to "+f.Database+" now.")
	default:
		who := make([]string, 0, len(f.Sessions))
		for _, s := range f.Sessions {
			who = append(who, s.words())
		}
		p.Warnings = append(p.Warnings, "Connected to "+f.Database+" now: "+strings.Join(who, ", ")+
			". WITH ROLLBACK IMMEDIATE disconnects them and undoes their unfinished work.")
	}
	p.Warnings = append(p.Warnings, permission(ScopeDatabase, f.Database))
	if blocked {
		return p, fix, nil
	}
	p.Changes = append(p.Changes, Drift{Setting: fix.Setting, Current: mode, Target: fix.Target})
	p.Statement = "ALTER DATABASE " + db + " SET MULTI_USER WITH ROLLBACK IMMEDIATE;"
	p.Rollback = "ALTER DATABASE " + db + " SET " + mode + " WITH ROLLBACK IMMEDIATE;"
	return p, fix, nil
}

// MultiUserReadBack is what the read-back of user_access_desc says, and whether it is MULTI_USER.
func MultiUserReadBack(access string) (string, bool) {
	a := strings.ToUpper(strings.TrimSpace(access))
	return a, a == "MULTI_USER"
}

// --- reconfigure (INS14) and min-server-memory (INS16) ---

// PendingSetting is a server setting whose configured value differs from the value in use.
type PendingSetting struct {
	Name       string
	Value      int64 // configured: what the next RECONFIGURE activates
	ValueInUse int64
	Dynamic    bool // takes effect at RECONFIGURE; false: only after a restart
	Advanced   bool // sp_configure needs 'show advanced options' to set it
}

// ServerConfigFacts is what the reconfigure and min-server-memory fixes read from
// sys.configurations.
type ServerConfigFacts struct {
	ServerName        string
	ProductVersion    string
	Pending           []PendingSetting // value <> value_in_use
	ShowAdvanced      int64            // value_in_use of 'show advanced options'
	MinServerMemoryMB int64            // value_in_use
	MaxServerMemoryMB int64            // value_in_use
}

// pending are the settings waiting for RECONFIGURE, without min server memory configured 0 that
// SQL Server shows as 16 in use (SetupFacts.Pending leaves it out too), 'show advanced options'
// last.
func (f ServerConfigFacts) pending() []PendingSetting {
	out := []PendingSetting{}
	var show *PendingSetting
	for _, p := range f.Pending {
		switch {
		case strings.EqualFold(p.Name, minServerMemorySetting) && p.ValueInUse == 16 && p.Value == 0:
			continue
		case strings.EqualFold(p.Name, "show advanced options"):
			q := p
			show = &q
			continue
		}
		out = append(out, p)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	if show != nil {
		out = append(out, *show)
	}
	return out
}

// PendingConfigs are the pending settings as SetupFacts carries them (for warnPending).
func (f ServerConfigFacts) PendingConfigs() []PendingConfig {
	out := []PendingConfig{}
	for _, p := range f.pending() {
		out = append(out, PendingConfig{Name: p.Name, Value: p.Value, ValueInUse: p.ValueInUse})
	}
	return out
}

// pendingOtherThan are the pending settings a RECONFIGURE of one setting also activates.
func (f ServerConfigFacts) pendingOtherThan(setting string) []PendingConfig {
	out := []PendingConfig{}
	for _, p := range f.PendingConfigs() {
		if strings.EqualFold(p.Name, setting) || strings.EqualFold(p.Name, "show advanced options") {
			continue
		}
		out = append(out, p)
	}
	return out
}

// PendingFingerprint names the pending settings and their values: the reconfigure fix echoes it in
// its options, so Apply refuses when the list changed since the preview.
func PendingFingerprint(ps []PendingSetting) string {
	parts := make([]string, 0, len(ps))
	for _, p := range ps {
		parts = append(parts, fmt.Sprintf("%s=%d/%d", p.Name, p.ValueInUse, p.Value))
	}
	return strings.Join(parts, ";")
}

// planCacheSettings are the settings whose RECONFIGURE clears the plan cache (Microsoft's list
// under "Server configuration options", sp_configure).
var planCacheSettings = []string{
	"access check cache bucket count", "access check cache quota", "clr enabled", "cost threshold for parallelism",
	"cross db ownership chaining", "index create memory (KB)", "max degree of parallelism", "max server memory (MB)",
	"max text repl size (B)", "max worker threads", "min memory per query (KB)", "min server memory (MB)",
	"query governor cost limit", "query wait (s)", "remote query timeout (s)", "user options",
}

func clearsPlanCache(name string) bool {
	for _, s := range planCacheSettings {
		if strings.EqualFold(s, name) {
			return true
		}
	}
	return false
}

func pendingNames(ps []PendingSetting) string {
	names := make([]string, 0, len(ps))
	for _, p := range ps {
		names = append(names, p.Name)
	}
	return strings.Join(names, ", ")
}

const (
	reconfigureHelp     = "activate runs RECONFIGURE, so the new values take effect. revert sets each one back to the value SQL Server runs with now, so nothing changes."
	reconfigureBadWarn  = "Choose activate or revert."
	reconfigureChanged  = "The pending settings changed since the preview: close this and preview again."
	reconfigureRevertOK = "Each setting goes back to the value SQL Server runs with now: nothing changes in how the server behaves, and no RECONFIGURE is needed."
)

// BuildReconfigureFix builds the reconfigure preview with the action option (activate when not
// given): RECONFIGURE (never WITH OVERRIDE), or one sp_configure per setting back to the value in
// use. ErrFixNotApplicable when nothing is pending. A wrong action, or a pending list that differs
// from the one the preview echoed, leaves the preview without changes.
func BuildReconfigureFix(f ServerConfigFacts, options map[string]string) (FixPreview, Fix, error) {
	ps := f.pending()
	if len(ps) == 0 {
		return FixPreview{}, Fix{}, ErrFixNotApplicable
	}
	typed, given := options[OptionReconfigureAction]
	action := strings.ToLower(strings.TrimSpace(typed))
	if !given || action == "" {
		action = ReconfigureActivate
	}
	fp := PendingFingerprint(ps)
	fix := suFix(FixReconfigure)
	fix.Current = fmt.Sprintf("%d pending", len(ps))
	fix.Options = map[string]string{OptionReconfigureAction: action}
	p := FixPreview{EngineVersion: f.ProductVersion, Changes: []Drift{}, Warnings: []string{},
		Options: map[string]string{OptionReconfigureAction: action, OptionPendingSeen: fp}}
	in := FixInput{Name: OptionReconfigureAction, Label: "What to do", Value: action, Help: reconfigureHelp, Kind: "select", Options: ReconfigureChoices}

	blocked := false
	if seen, ok := options[OptionPendingSeen]; ok && seen != "" && seen != fp {
		p.Warnings, blocked = append(p.Warnings, reconfigureChanged), true
	}
	switch action {
	case ReconfigureActivate:
		fix.Target = "activated"
		for _, s := range ps {
			p.Changes = append(p.Changes, Drift{Setting: s.Name, Current: fmt.Sprint(s.ValueInUse), Target: fmt.Sprint(s.Value)})
		}
		p.Warnings = append(p.Warnings, "RECONFIGURE activates all of them at once: "+PendingList(f.PendingConfigs())+".")
		cache, restart := []PendingSetting{}, []PendingSetting{}
		for _, s := range ps {
			if clearsPlanCache(s.Name) {
				cache = append(cache, s)
			}
			if !s.Dynamic {
				restart = append(restart, s)
			}
		}
		if len(cache) > 0 {
			p.Warnings = append(p.Warnings, "Activating "+pendingNames(cache)+" clears the plan cache: queries compile again for a few minutes.")
		}
		if len(restart) > 0 {
			p.Warnings = append(p.Warnings, pendingNames(restart)+" take effect only when SQL Server restarts: they stay pending until then.")
		}
		p.Statement = "RECONFIGURE;"
		back := []string{"EXEC sp_configure 'show advanced options', 1; RECONFIGURE;"}
		for _, s := range ps {
			back = append(back, fmt.Sprintf("EXEC sp_configure %s, %d;", suSQLString(s.Name), s.ValueInUse))
		}
		p.Rollback = strings.Join(append(back, "RECONFIGURE;"), " ")
	case ReconfigureRevert:
		fix.Target = "set back to the values in use"
		set, back := []string{}, []string{}
		for _, s := range ps {
			p.Changes = append(p.Changes, Drift{Setting: s.Name, Current: fmt.Sprintf("pending %d", s.Value), Target: fmt.Sprintf("stays %d", s.ValueInUse)})
			set = append(set, fmt.Sprintf("EXEC sp_configure %s, %d;", suSQLString(s.Name), s.ValueInUse))
			back = append(back, fmt.Sprintf("EXEC sp_configure %s, %d;", suSQLString(s.Name), s.Value))
			if s.Advanced && f.ShowAdvanced == 0 && !strings.EqualFold(s.Name, "show advanced options") {
				blocked = true
				p.Warnings = append(p.Warnings, s.Name+" is an advanced setting and 'show advanced options' is off, so it cannot be set back without a RECONFIGURE, which would activate it. Use activate, or set it back by hand.")
			}
		}
		p.Warnings = append(p.Warnings, reconfigureRevertOK, "Whoever changed them may still want the new values: ask first.")
		p.Statement, p.Rollback = strings.Join(set, " "), strings.Join(back, " ")
	default:
		in.Value, in.Warning = strings.TrimSpace(typed), reconfigureBadWarn
		p.Options[OptionReconfigureAction] = in.Value
		p.Warnings, blocked = append(p.Warnings, reconfigureBadWarn), true
	}
	p.Inputs = []FixInput{in}
	p.Warnings = append(p.Warnings, warnServer(f.ServerName), permission(ScopeServer, ""))
	if blocked {
		p.Changes, p.Statement, p.Rollback = []Drift{}, "", ""
	}
	return p, fix, nil
}

// ReconfigureReadBack says what is still pending of the settings the preview named, read again
// after the statement: activate is verified when only settings that need a restart are left,
// revert when none is.
func ReconfigureReadBack(action string, names []string, after ServerConfigFacts) (string, bool) {
	still, restartOnly := []string{}, true
	for _, s := range after.pending() {
		for _, n := range names {
			if strings.EqualFold(s.Name, n) {
				w := s.Name
				if !s.Dynamic {
					w += " (takes effect after a restart)"
				} else {
					restartOnly = false
				}
				still = append(still, w)
			}
		}
	}
	if len(still) == 0 {
		return "none pending", true
	}
	text := "still pending: " + strings.Join(still, ", ")
	if action == ReconfigureActivate {
		return text, restartOnly
	}
	return text, false
}

// ReconfigureDescription is the change log line of the reconfigure fix.
func ReconfigureDescription(action, server string, changes []Drift) string {
	parts := make([]string, 0, len(changes))
	for _, c := range changes {
		if action == ReconfigureRevert {
			parts = append(parts, c.Setting+" "+strings.TrimPrefix(c.Target, "stays ")+" (was pending "+strings.TrimPrefix(c.Current, "pending ")+")")
		} else {
			parts = append(parts, c.Setting+" "+c.Current+" -> "+c.Target)
		}
	}
	if action == ReconfigureRevert {
		return "Pending settings on " + server + " set back to the values in use: " + strings.Join(parts, ", ")
	}
	return "RECONFIGURE on " + server + " activated: " + strings.Join(parts, ", ")
}

// minMemoryHigh is INS16's rule: min server memory at 90% of max or more, max set.
func (f ServerConfigFacts) minMemoryHigh() bool {
	return f.MaxServerMemoryMB > 0 && f.MaxServerMemoryMB < unlimitedMemory && f.MinServerMemoryMB*10 >= f.MaxServerMemoryMB*9
}

// BuildMinServerMemoryFix builds the min-server-memory preview: sp_configure 'min server memory
// (MB)', 0 with RECONFIGURE, as the plain server fixes write it. ErrFixNotApplicable when INS16's
// rule does not hold.
func BuildMinServerMemoryFix(f ServerConfigFacts) (FixPreview, Fix, error) {
	if !f.minMemoryHigh() {
		return FixPreview{}, Fix{}, ErrFixNotApplicable
	}
	fix := suFix(FixMinServerMemory)
	fix.Current, fix.Target = fmt.Sprintf("%d MB", f.MinServerMemoryMB), "0 MB"
	p := FixPreview{EngineVersion: f.ProductVersion, Warnings: []string{},
		Changes:   []Drift{{Setting: fix.Setting, Current: fix.Current, Target: fix.Target}},
		Statement: spConfigure(minServerMemorySetting, 0),
		Rollback:  spConfigure(minServerMemorySetting, f.MinServerMemoryMB),
	}
	p.Warnings = append(p.Warnings, warnServer(f.ServerName),
		"SQL Server can give memory back to Windows again when Windows runs short; it keeps what it uses now until then.")
	if pend := f.pendingOtherThan(minServerMemorySetting); len(pend) > 0 {
		p.Warnings = append(p.Warnings, warnPending(pend))
	}
	p.Warnings = append(p.Warnings, permission(ScopeServer, ""))
	return p, fix, nil
}

// MinServerMemoryReadBack is the value read back; SQL Server shows 0 configured as 16 MB in use.
func MinServerMemoryReadBack(inUseMB int64) (string, bool) {
	return fmt.Sprintf("%d MB", inUseMB), inUseMB <= 16
}

// MinServerMemoryDescription is the change log line, naming what its RECONFIGURE also activated.
func MinServerMemoryDescription(fix Fix, f ServerConfigFacts) string {
	return FixChangeDescription(fix, f.ServerName, "", f.pendingOtherThan(minServerMemorySetting)...)
}

// --- trace-flag-off (INS15) ---

// TraceFlagFacts is what the trace-flag-off fix reads: the global trace flags, and the -T flags in
// the startup parameters (sys.dm_server_registry).
type TraceFlagFacts struct {
	ServerName     string
	ProductVersion string
	Flags          []int // on for the whole server (DBCC TRACESTATUS(-1))
	StartupFlags   []int // -T in the startup parameters
	StartupError   string
}

// ParseStartupFlag reads a startup parameter: -T1117 is flag 1117.
func ParseStartupFlag(arg string) (int, bool) {
	a := strings.TrimSpace(arg)
	if len(a) < 3 || a[0] != '-' || (a[1] != 'T' && a[1] != 't') {
		return 0, false
	}
	n, err := strconv.Atoi(a[2:])
	return n, err == nil && n > 0
}

func suHasInt(list []int, n int) bool {
	for _, x := range list {
		if x == n {
			return true
		}
	}
	return false
}

// TraceFlagOffOffered says whether an INS15 finding about one flag offers trace-flag-off: every
// flag but the startup-only ones, which need a restart.
func TraceFlagOffOffered(flag int) bool {
	tf, ok := traceFlags[flag]
	return !ok || tf.kind != "startup"
}

// BuildTraceFlagOffFix builds the trace-flag-off preview for the flag option (the lowest flag on
// when not given): DBCC TRACEOFF(n, -1), DBCC TRACEON(n, -1) as the rollback. ErrFixNotApplicable
// when no global flag is on. A flag that is not on leaves the preview without changes.
func BuildTraceFlagOffFix(f TraceFlagFacts, options map[string]string) (FixPreview, Fix, error) {
	flags := append([]int(nil), f.Flags...)
	if len(flags) == 0 {
		return FixPreview{}, Fix{}, ErrFixNotApplicable
	}
	sort.Ints(flags)
	choices := make([]string, 0, len(flags))
	for _, n := range flags {
		choices = append(choices, strconv.Itoa(n))
	}
	typed, given := options[OptionTraceFlag]
	typed = strings.TrimSpace(typed)
	n := flags[0]
	warn := ""
	if given && typed != "" {
		v, err := strconv.Atoi(typed)
		switch {
		case err != nil || v <= 0:
			warn = "Choose one of the trace flags that are on: " + strings.Join(choices, ", ") + "."
		case !suHasInt(flags, v):
			warn = fmt.Sprintf("Trace flag %d is not on for the whole server: choose one that is (%s).", v, strings.Join(choices, ", "))
		}
		n = v
	}
	value := strconv.Itoa(n)
	if warn != "" {
		value = typed
	}
	fix := suFix(FixTraceFlagOff)
	fix.Setting, fix.Current, fix.Target = "Trace flag "+value, "on", "off"
	fix.Options = map[string]string{OptionTraceFlag: value}
	p := FixPreview{EngineVersion: f.ProductVersion, Changes: []Drift{}, Warnings: []string{}, Options: map[string]string{OptionTraceFlag: value},
		Inputs: []FixInput{{Name: OptionTraceFlag, Label: "Trace flag", Value: value, Kind: "select", Options: choices, Warning: warn,
			Help: "The global trace flags on now (DBCC TRACESTATUS(-1))."}}}
	if warn != "" {
		p.Warnings = append(p.Warnings, warn, warnServer(f.ServerName), traceFlagPermission(f.ServerName))
		return p, fix, nil
	}
	switch {
	case suHasInt(f.StartupFlags, n):
		p.Warnings = append(p.Warnings, fmt.Sprintf("-T%d is in the startup parameters, so it comes back at the next restart: copy this for IT to remove it in Configuration Manager.", n))
	case f.StartupError != "":
		p.Warnings = append(p.Warnings, fmt.Sprintf("If the flag is in the startup parameters (-T%d), it comes back at the next restart: copy this for IT to remove it in Configuration Manager.", n))
	default:
		p.Warnings = append(p.Warnings, fmt.Sprintf("-T%d is not in the startup parameters, so it stays off after a restart (unless a startup job turns it on again).", n))
	}
	if tf, ok := traceFlags[n]; ok {
		p.Warnings = append(p.Warnings, fmt.Sprintf("What trace flag %d does: %s", n, tf.text))
	} else {
		p.Warnings = append(p.Warnings, fmt.Sprintf("kakapo does not know what trace flag %d does: look it up first; the application vendor may need it.", n))
	}
	p.Warnings = append(p.Warnings, warnServer(f.ServerName), traceFlagPermission(f.ServerName))
	p.Changes = append(p.Changes, Drift{Setting: fix.Setting, Current: "on (global)", Target: "off"})
	p.Statement = fmt.Sprintf("DBCC TRACEOFF(%d, -1);", n)
	p.Rollback = fmt.Sprintf("DBCC TRACEON(%d, -1);", n)
	return p, fix, nil
}

func traceFlagPermission(server string) string {
	return "Needs sysadmin on " + server + " (DBCC TRACEOFF)."
}

// TraceFlagOffReadBack says whether the flag is off after the statement, from the global flags
// read again.
func TraceFlagOffReadBack(flag int, after []int) (string, bool) {
	if suHasInt(after, flag) {
		return "still on", false
	}
	return "off", true
}

// --- scoped-config-reset (DBC14) ---

// ScopedConfigFacts is what the scoped-config-reset fix reads: the database's scoped
// configurations and the server's MAXDOP.
type ScopedConfigFacts struct {
	ServerName     string
	ProductVersion string
	Database       string
	Configs        []ScopedConfig
	ServerMaxDop   int // -1 when not read
}

// NonDefaultScoped are the configurations DBC14 compares whose value differs from the default on
// this version, by name, upper case.
func NonDefaultScoped(configs []ScopedConfig, major int) []ScopedConfig {
	out := []ScopedConfig{}
	for _, c := range configs {
		d, ok := scopedDefaultFor(c.Name, major)
		if !ok || scopedValue(c.Value) == d.value {
			continue
		}
		out = append(out, ScopedConfig{Name: strings.ToUpper(c.Name), Value: c.Value})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func scopedConfigPermission(db string) string {
	return "Needs the ALTER ANY DATABASE SCOPED CONFIGURATION permission in " + db + " (db_owner)."
}

// scopedStatement is the ALTER DATABASE SCOPED CONFIGURATION that sets one configuration, run in
// the database (the USE makes the copied script run there too).
func scopedStatement(db, name, value string) string {
	return "USE " + QuoteName(db) + ";\nALTER DATABASE SCOPED CONFIGURATION SET " + name + " = " + value + ";"
}

// BuildScopedConfigResetFix builds the scoped-config-reset preview for the name option (the first
// configuration that differs from the default when not given): the configuration set to its
// default (MAXDOP to 0), the previous value as the rollback. ErrFixNotApplicable when every
// configuration has its default. A name that is not one of those differing leaves the preview
// without changes.
func BuildScopedConfigResetFix(f ScopedConfigFacts, options map[string]string) (FixPreview, Fix, error) {
	major := EngineMajor(f.ProductVersion)
	nd := NonDefaultScoped(f.Configs, major)
	if len(nd) == 0 {
		return FixPreview{}, Fix{}, ErrFixNotApplicable
	}
	choices := make([]string, 0, len(nd))
	for _, c := range nd {
		choices = append(choices, c.Name)
	}
	typed, given := options[OptionScopedName]
	name := strings.ToUpper(strings.TrimSpace(typed))
	if !given || name == "" {
		name = nd[0].Name
	}
	var cur *ScopedConfig
	for i := range nd {
		if nd[i].Name == name {
			cur = &nd[i]
		}
	}
	fix := suFix(FixScopedConfigReset)
	fix.Options = map[string]string{OptionScopedName: name}
	p := FixPreview{Database: f.Database, EngineVersion: f.ProductVersion, Changes: []Drift{}, Warnings: []string{},
		Options: map[string]string{OptionScopedName: name}}
	in := FixInput{Name: OptionScopedName, Label: "Configuration", Value: name, Kind: "select", Options: choices,
		Help: "The database scoped configurations of " + f.Database + " that differ from the default."}
	if cur == nil {
		in.Warning = "kakapo resets only a configuration that differs from its default: choose one of " + strings.Join(choices, ", ") + "."
		if d, ok := scopedDefaultFor(name, major); ok {
			in.Warning = fmt.Sprintf("%s already has its default value (%s): choose another.", name, scopedText(d.value, d.flag))
		}
		in.Value = strings.TrimSpace(typed)
		p.Options[OptionScopedName] = in.Value
		fix.Setting = in.Value
		p.Inputs = []FixInput{in}
		p.Warnings = append(p.Warnings, in.Warning, scopedConfigPermission(f.Database))
		return p, fix, nil
	}
	d, _ := scopedDefaultFor(name, major)
	current := scopedText(scopedValue(cur.Value), d.flag)
	target := scopedText(d.value, d.flag)
	fix.Setting, fix.Current, fix.Target = name, current, target
	p.Inputs = []FixInput{in}
	p.Changes = append(p.Changes, Drift{Setting: name, Current: current, Target: target})
	p.Statement = scopedStatement(f.Database, name, target)
	if cur.Value != "" && sqlWord.MatchString(current) && current != "NULL" {
		p.Rollback = scopedStatement(f.Database, name, current)
	} else {
		p.Warnings = append(p.Warnings, fmt.Sprintf("The previous value of %s (%q) is not in the rollback; set it by hand if you roll back.", name, cur.Value))
	}
	switch name {
	case "MAXDOP":
		if f.ServerMaxDop >= 0 {
			p.Warnings = append(p.Warnings, fmt.Sprintf("Queries in %s follow the server's MAXDOP (%d) again.", f.Database, f.ServerMaxDop))
		} else {
			p.Warnings = append(p.Warnings, "Queries in "+f.Database+" follow the server's MAXDOP again.")
		}
	case "LEGACY_CARDINALITY_ESTIMATION":
		p.Warnings = append(p.Warnings, "Queries switch to the newer cardinality estimator: most get the same or better plans, a few may get slower. Watch them in Query Store after the change (CFG01).")
	}
	p.Warnings = append(p.Warnings,
		"It was set on purpose once (often a workaround for one query, or a vendor's requirement): ask first.",
		"Queries in "+f.Database+" compile again, and their plans may change.",
		scopedConfigPermission(f.Database))
	return p, fix, nil
}

// ScopedConfigReadBack says what the configuration's value is after the statement, and whether it
// is the default.
func ScopedConfigReadBack(name string, after ScopedConfigFacts) (string, bool) {
	major := EngineMajor(after.ProductVersion)
	d, ok := scopedDefaultFor(name, major)
	for _, c := range after.Configs {
		if strings.EqualFold(c.Name, name) {
			v := scopedValue(c.Value)
			return scopedText(v, d.flag), ok && v == d.value
		}
	}
	return "not found", false
}

// --- the findings' fixes ---

// setupActionIDs are the Setup card's registered actions, which AvailableFixes lists one per
// finding with the finding's options.
var setupActionIDs = []string{FixMultiUser, FixReconfigure, FixTraceFlagOff, FixScopedConfigReset, FixMinServerMemory}

func isSetupAction(id string) bool {
	for _, x := range setupActionIDs {
		if x == id {
			return true
		}
	}
	return false
}

// setupActionFix is the Fix a Setup finding with one of the actions offers, with the values the
// card read and the options the preview needs (the flag, the configuration).
func setupActionFix(f SetupFacts, x Finding) (Fix, bool) {
	fix := suFix(x.FixID)
	switch x.FixID {
	case FixMultiUser:
		fix.Current, fix.Target = strings.ToUpper(f.UserAccess), "MULTI_USER"
	case FixReconfigure:
		fix.Current, fix.Target = fmt.Sprintf("%d pending", len(f.Pending())), "activated"
		fix.Options = map[string]string{OptionReconfigureAction: ReconfigureActivate}
	case FixTraceFlagOff:
		fix.Setting, fix.Current, fix.Target = "Trace flag "+x.Object, "on", "off"
		fix.Options = map[string]string{OptionTraceFlag: x.Object}
	case FixScopedConfigReset:
		cur, def := "", ""
		for _, c := range f.ScopedConfigs {
			if strings.EqualFold(c.Name, x.Object) {
				d, _ := scopedDefaultFor(c.Name, f.Major())
				cur, def = scopedText(scopedValue(c.Value), d.flag), scopedText(d.value, d.flag)
			}
		}
		fix.Setting, fix.Current, fix.Target = x.Object, cur, def
		fix.Options = map[string]string{OptionScopedName: x.Object}
	case FixMinServerMemory:
		fix.Current, fix.Target = fmt.Sprintf("%d MB", f.MinServerMemoryMB), "0 MB"
	default:
		return Fix{}, false
	}
	return fix, true
}
