package domain

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// The first installed helper (docs/phase5e-contract.md; SCOPE section 3 item 9): kakapo's Extended
// Events session, which keeps every block over the blocked process threshold and every deadlock in
// files beside the error log, so the blocking history (BLK01) and the deadlock report (DLK01) stop
// depending on system_health's five-minute snapshot and its retention. install-xe-session creates it
// and sets the threshold; remove-xe-session drops it, resets the threshold and leaves the files.
// The instance page's "Installed helpers" row says where it stands (BuildInstalledHelpers).

// Ids of the helper's fixes.
const (
	FixInstallXESession = "install-xe-session"
	FixRemoveXESession  = "remove-xe-session"
)

// XESessionName is the session's name; xeFileName its file target's file, beside the error log.
const (
	XESessionName = "kakapo"
	xeFileName    = "kakapo.xel"
	// XEObject is the change row object of the helper's fixes.
	XEObject = "Event session kakapo"
	// ChangeTypeHelper is the change row type of a helper kakapo installs or removes.
	ChangeTypeHelper = "HELPER"
	// BlockedThresholdSetting is the sp_configure name of the blocked process threshold.
	BlockedThresholdSetting = "blocked process threshold (s)"
)

// The events and target that make a session kakapo's: a session named kakapo with exactly these
// two events and one event_file target writing kakapo.xel. SQL Server keeps no description for an
// event session, so the shape is the mark.
var (
	xeEvents  = []string{"sqlserver.blocked_process_report", "sqlserver.xml_deadlock_report"}
	xeTargets = []string{"package0.event_file"}
)

// The options of install-xe-session (and remove-xe-session's threshold).
const (
	OptionXEThreshold = "thresholdSeconds"
	OptionXEFolder    = "folder"
	OptionXEFileMB    = "fileMB"
	OptionXEFiles     = "files"
	OptionXEDispatch  = "dispatchSeconds"
)

// The defaults and ranges of the options. 5 seconds is the threshold sp_HumanEvents and
// Performance Monitor Lite set (DarlingData sp_HumanEvents.sql 1182; Lite
// install/21_setup_blocked_process_xe.sql): what users notice.
const (
	DefaultXEThreshold = 5
	DefaultXEFileMB    = 50
	DefaultXEFiles     = 4
	DefaultXEDispatch  = 30
	maxXEThresholdOff  = 86400 // sp_configure's own upper bound, for the remove fix
)

// XESession is kakapo's event session as read (collect.ReadHelpers): whether a session named kakapo
// exists (sys.server_event_sessions) and runs (sys.dm_xe_sessions), its events and targets, its file
// target's settings, when it started, how often its file target ran, and the blocked process
// threshold in use. Error says which part could not be read.
type XESession struct {
	Exists         bool       `json:"exists"`
	Running        bool       `json:"running"`
	Ours           bool       `json:"ours"` // the shape kakapo creates (HasKakapoShape)
	Events         []string   `json:"events"`
	Targets        []string   `json:"targets"`
	FileName       string     `json:"fileName,omitempty"`    // the file target's filename as configured
	CurrentFile    string     `json:"currentFile,omitempty"` // the file it writes now (running only)
	MaxFileMB      int        `json:"maxFileMB,omitempty"`
	MaxFiles       int        `json:"maxFiles,omitempty"`
	DispatchSec    int        `json:"dispatchSeconds,omitempty"`
	StartupState   bool       `json:"startupState"`
	StartedAt      *time.Time `json:"startedAt,omitempty"` // UTC, while it runs
	EventsCaptured int64      `json:"eventsCaptured"`      // the file target's execution_count
	// ThresholdSeconds is the blocked process threshold (s) in use; -1 when it could not be read.
	ThresholdSeconds int `json:"thresholdSeconds"`
	// OtherSessions are the other event sessions that record the blocked process report.
	OtherSessions []string `json:"otherSessions,omitempty"`
	Error         string   `json:"error,omitempty"`
}

// HelperFacts is what collect.ReadHelpers reads: the server, its error log file and kakapo's
// session.
type HelperFacts struct {
	ServerName     string
	ProductVersion string
	EngineEdition  int
	ErrorLogFile   string // SERVERPROPERTY('ErrorLogFileName')
	Session        XESession
}

// HasKakapoShape says whether a session has the events and the target kakapo creates, writing
// kakapo.xel.
func (s XESession) HasKakapoShape() bool {
	return s.Exists && sameNames(s.Events, xeEvents) && sameNames(s.Targets, xeTargets) &&
		strings.EqualFold(xeBase(s.FileName), xeFileName)
}

func sameNames(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	g := make([]string, len(got))
	for i, x := range got {
		g[i] = strings.ToLower(strings.TrimSpace(x))
	}
	sort.Strings(g)
	for i, x := range want {
		if g[i] != x {
			return false
		}
	}
	return true
}

// xeBase is the file name of a path, without its folder.
func xeBase(path string) string {
	if i := strings.LastIndexAny(path, `\/`); i >= 0 {
		return path[i+1:]
	}
	return path
}

// xeDir is the folder of a path, without the trailing separator (a bare drive keeps it); "" when the
// path has no folder.
func xeDir(path string) string {
	i := strings.LastIndexAny(path, `\/`)
	if i < 0 {
		return ""
	}
	d := path[:i]
	switch {
	case d == "":
		d = "/"
	case len(d) == 2 && d[1] == ':':
		d += `\`
	}
	return d
}

// xeJoin is folder + name with the folder's own separator (/ on Linux).
func xeJoin(folder, name string) string {
	if strings.HasPrefix(folder, "/") {
		return strings.TrimRight(folder, "/") + "/" + name
	}
	return joinFolder(folder, name)
}

// ErrorLogFolder is the folder of the error log file, where the session's files go by default.
func ErrorLogFolder(errorLogFile string) string { return xeDir(strings.TrimSpace(errorLogFile)) }

// Folder is the folder the session's file target writes to.
func (s XESession) Folder() string { return xeDir(s.FileName) }

// FilePattern is every file of the session's target, rolled-over ones included: {folder}\kakapo*.xel.
func (s XESession) FilePattern() string {
	if s.FileName == "" {
		return ""
	}
	base := xeBase(s.FileName)
	if i := strings.LastIndex(base, "."); i > 0 {
		base = base[:i]
	}
	if d := s.Folder(); d != "" {
		return xeJoin(d, base+"*.xel")
	}
	return base + "*.xel"
}

// XEFiles is where the blocking history and the deadlock report read kakapo's session from: the
// file pattern and when the session started (UTC); a zero Pattern when kakapo's session does not
// exist.
type XEFiles struct {
	Pattern   string
	StartedAt *time.Time
}

// Files is what the reads take from the session: its files when it is kakapo's.
func (s XESession) Files() XEFiles {
	if !s.Exists || !s.Ours || s.FilePattern() == "" {
		return XEFiles{}
	}
	return XEFiles{Pattern: s.FilePattern(), StartedAt: s.StartedAt}
}

// validXEFolder says whether a folder can go into the statement: a full Windows path (a drive or a
// share, as the backup folder) or an absolute Linux path.
func validXEFolder(s string) bool {
	if ValidBackupFolder(s) {
		return true
	}
	if !strings.HasPrefix(s, "/") || utf8.RuneCountInString(s) > 200 || strings.ContainsAny(s, `*?"<>|\`) {
		return false
	}
	for _, r := range s {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}

// XEFolder is the folder the install writes to: the folder option when given, else the error log's
// folder, normalised. The service checks it with xp_fileexist before building the preview.
func XEFolder(f HelperFacts, options map[string]string) string {
	v, ok := options[OptionXEFolder]
	if !ok || strings.TrimSpace(v) == "" {
		v = ErrorLogFolder(f.ErrorLogFile)
	}
	v = strings.TrimSpace(v)
	if v == "/" {
		return v
	}
	return normalizeFolder(v)
}

// ValidXEFolder says whether the folder can be checked and put in the statement.
func ValidXEFolder(folder string) bool { return validXEFolder(folder) }

// XEInstallFacts is what install-xe-session is built from: the helper facts, the folder it writes
// to and whether it exists as the SQL Server service sees it, and the settings waiting for
// RECONFIGURE (ServerConfigFacts.PendingConfigs).
type XEInstallFacts struct {
	HelperFacts
	Folder        string
	FolderChecked bool   // xp_fileexist answered
	FolderExists  bool   // ... and the folder is there
	FolderError   string // why it could not be checked
	Pending       []PendingConfig
}

// XERemoveFacts is what remove-xe-session is built from: the helper facts, the session's files on
// disk (or why they could not be listed) and the settings waiting for RECONFIGURE.
type XERemoveFacts struct {
	HelperFacts
	Files      []string
	FilesError string
	Pending    []PendingConfig
}

// xeIntInput reads one whole-number option in a range: the value, the input, and the warning when the
// value given is not a whole number in the range.
func xeIntInput(options map[string]string, name, label, help string, def, lo, hi int, advanced bool) (int, FixInput, string) {
	in := FixInput{Name: name, Label: label, Value: strconv.Itoa(def), Kind: "text", Help: help, Advanced: advanced}
	typed, given := options[name]
	if !given {
		return def, in, ""
	}
	v := strings.TrimSpace(typed)
	n, err := strconv.Atoi(v)
	if err != nil || !allDigits(v) || n < lo || n > hi {
		warn := fmt.Sprintf("%s must be a whole number from %d to %d.", label, lo, hi)
		in.Value, in.Warning = v, warn
		return def, in, warn
	}
	in.Value = strconv.Itoa(n)
	return n, in, ""
}

// thresholdText is "0 (off)" or "5 s".
func thresholdText(n int) string {
	switch {
	case n < 0:
		return "not read"
	case n == 0:
		return "0 (off)"
	}
	return fmt.Sprintf("%d s", n)
}

// xeSessionDDL creates and starts kakapo's session.
//
// Source: DarlingData sp_HumanEvents.sql 788-809, 1776-1778 (MIT): the blocking session's shape
// (sqlserver.blocked_process_report to a package0.event_file target with max_file_size and
// max_rollover_files, EVENT_RETENTION_MODE = ALLOW_SINGLE_EVENT_LOSS, TRACK_CAUSALITY = OFF), here with
// xml_deadlock_report beside it, a fixed MAX_MEMORY, our dispatch latency and STARTUP_STATE = ON so it
// survives a restart. Our own DDL.
func xeSessionDDL(folder string, fileMB, files, dispatch int) string {
	return "CREATE EVENT SESSION [kakapo] ON SERVER\n" +
		"    ADD EVENT sqlserver.blocked_process_report,\n" +
		"    ADD EVENT sqlserver.xml_deadlock_report\n" +
		fmt.Sprintf("    ADD TARGET package0.event_file (SET filename = %s, max_file_size = %d, max_rollover_files = %d)\n",
			nstr(xeJoin(folder, xeFileName)), fileMB, files) +
		fmt.Sprintf("    WITH (MAX_MEMORY = 4096 KB, EVENT_RETENTION_MODE = ALLOW_SINGLE_EVENT_LOSS, MAX_DISPATCH_LATENCY = %d SECONDS,\n", dispatch) +
		"          STARTUP_STATE = ON, TRACK_CAUSALITY = OFF);\n" +
		"ALTER EVENT SESSION [kakapo] ON SERVER STATE = START;"
}

// XEInstallStatement is install-xe-session's statement: the threshold through sp_configure (as the
// server fixes write it), then the session created and started. XACT_ABORT stops the batch at the
// first error, so a session that cannot be created is not then started.
func XEInstallStatement(folder string, threshold, fileMB, files, dispatch int) string {
	return "SET XACT_ABORT ON;\n" + spConfigure(BlockedThresholdSetting, int64(threshold)) + "\n" + xeSessionDDL(folder, fileMB, files, dispatch)
}

// XERemoveStatement stops and drops kakapo's session (each only when it is there) and sets the
// threshold: remove-xe-session's statement and install-xe-session's rollback. The files stay.
func XERemoveStatement(threshold int) string {
	return "IF EXISTS (SELECT 1 FROM sys.dm_xe_sessions WHERE name = N'kakapo') ALTER EVENT SESSION [kakapo] ON SERVER STATE = STOP;\n" +
		"IF EXISTS (SELECT 1 FROM sys.server_event_sessions WHERE name = N'kakapo') DROP EVENT SESSION [kakapo] ON SERVER;\n" +
		spConfigure(BlockedThresholdSetting, int64(threshold))
}

// xePermission is the line the helper's previews end with.
const xePermission = "Needs ALTER ANY EVENT SESSION and ALTER SETTINGS: sysadmin."

// xeFilesWords is "D:\Log\kakapo*.xel, 50 MB x 4".
func xeFilesWords(pattern string, fileMB, files int) string {
	return fmt.Sprintf("%s, %d MB x %d", pattern, fileMB, files)
}

// xeOthersWarn names the other sessions that record the blocked process report.
func xeOthersWarn(others []string) string {
	if len(others) == 1 {
		return "The event session " + others[0] + " also records the blocked process report: it follows the new threshold too."
	}
	return "The event sessions " + strings.Join(others, ", ") + " also record the blocked process report: they follow the new threshold too."
}

// BuildInstallXESessionFix builds install-xe-session's preview with the options (thresholdSeconds;
// under More options folder, fileMB, files, dispatchSeconds). ErrFixNotApplicable when kakapo's session
// exists already. A bad option, a folder that is not there, a session named kakapo that kakapo did not
// create, a server older than 2012 or an Azure SQL server leave the preview without changes.
func BuildInstallXESessionFix(f XEInstallFacts, options map[string]string) (FixPreview, Fix, error) {
	s := f.Session
	if s.Exists && s.Ours {
		return FixPreview{}, Fix{}, ErrFixNotApplicable
	}
	blocked := []string{}
	threshold, thIn, w := xeIntInput(options, OptionXEThreshold, "Keep blocks longer than (seconds)",
		"A block shorter than this is not recorded; 5 is what users notice. 1 to 60.", DefaultXEThreshold, 1, 60, false)
	if w != "" {
		blocked = append(blocked, w)
	}
	folder := f.Folder
	if folder == "" {
		folder = XEFolder(f.HelperFacts, options)
	}
	folderIn := FixInput{Name: OptionXEFolder, Label: "Folder", Value: folder, Kind: "text", Advanced: true,
		Help: "Where the session's files go, as the SQL Server service sees it. By default the error log's folder, where every DBA looks."}
	switch {
	case !validXEFolder(folder):
		folderIn.Warning = "The folder must be a full path on the server, for example D:\\SQLLogs."
		blocked = append(blocked, folderIn.Warning)
	case f.FolderChecked && !f.FolderExists:
		folderIn.Warning = fmt.Sprintf("The folder %s does not exist on %s as the SQL Server service sees it: choose one that does.", folder, orQ(f.ServerName))
		blocked = append(blocked, folderIn.Warning)
	}
	fileMB, mbIn, w := xeIntInput(options, OptionXEFileMB, "File size (MB)", "Each file's size before the session starts the next one. 10 to 500.",
		DefaultXEFileMB, 10, 500, true)
	if w != "" {
		blocked = append(blocked, w)
	}
	files, filesIn, w := xeIntInput(options, OptionXEFiles, "Files kept", "How many files the session keeps; the oldest is deleted when a new one starts. 2 to 10.",
		DefaultXEFiles, 2, 10, true)
	if w != "" {
		blocked = append(blocked, w)
	}
	dispatch, dispIn, w := xeIntInput(options, OptionXEDispatch, "Write to the file within (seconds)",
		"How long events may wait in memory before they are written. 5 to 60.", DefaultXEDispatch, 5, 60, true)
	if w != "" {
		blocked = append(blocked, w)
	}
	if s.Exists && !s.Ours {
		blocked = append(blocked, fmt.Sprintf("A session named kakapo already exists on %s and kakapo did not create it (its events or its target differ): kakapo leaves it alone. Drop or rename it by hand if nobody needs it.", orQ(f.ServerName)))
	}
	if m := EngineMajor(f.ProductVersion); m > 0 && m < 11 {
		blocked = append(blocked, fmt.Sprintf("kakapo's session needs SQL Server 2012 or later; this server runs %s.", f.ProductVersion))
	}
	switch f.EngineEdition {
	case 5:
		blocked = append(blocked, "Azure SQL Database has no server event sessions: kakapo cannot install its session there.")
	case 8:
		blocked = append(blocked, "On Azure SQL Managed Instance a file target needs a blob storage container and credential, which kakapo does not set up.")
	}

	inputs := []FixInput{thIn, folderIn, mbIn, filesIn, dispIn}
	opts := map[string]string{}
	for _, in := range inputs {
		opts[in.Name] = in.Value
	}
	pattern := xeJoin(folder, "kakapo*.xel")
	cur := s.ThresholdSeconds
	fix := suFix(FixInstallXESession)
	fix.Current = "none"
	fix.Target = fmt.Sprintf("every block over %d s and every deadlock to %s", threshold, pattern)
	fix.Options = opts
	p := FixPreview{EngineVersion: f.ProductVersion, Changes: []Drift{}, Warnings: []string{}, Inputs: inputs, Options: opts}
	p.Warnings = append(p.Warnings, blocked...)
	if f.FolderError != "" && len(blocked) == 0 {
		p.Warnings = append(p.Warnings, fmt.Sprintf("kakapo could not check that %s exists (%s): make sure it does, or SQL Server refuses to start the session.", folder, f.FolderError))
	}
	if cur > 0 && cur != threshold {
		p.Warnings = append(p.Warnings, fmt.Sprintf("The blocked process threshold is %d s now: kakapo sets it to %d s, and the rollback puts %d s back.", cur, threshold, cur))
	}
	if len(s.OtherSessions) > 0 {
		p.Warnings = append(p.Warnings, xeOthersWarn(s.OtherSessions))
	}
	p.Warnings = append(p.Warnings, warnServer(orQ(f.ServerName)))
	if pend := pendingWithout(f.Pending, BlockedThresholdSetting); len(pend) > 0 {
		p.Warnings = append(p.Warnings, warnPending(pend))
	}
	p.Warnings = append(p.Warnings,
		fmt.Sprintf("Every block over %d s is written to the file with both statements; on a server with constant blocking the files turn over faster (at most %s MB on disk, never more).",
			threshold, n0int(int64(fileMB*files))),
		"The session costs almost nothing while nothing blocks; the blocked process report is produced by the lock monitor, which already runs.",
		"Deadlocks are also kept here, so the deadlock report no longer depends on system_health's retention.",
		xePermission)
	if len(blocked) > 0 {
		return p, fix, nil
	}
	p.Changes = append(p.Changes,
		Drift{Setting: XEObject, Current: "none",
			Target: fmt.Sprintf("blocked_process_report and xml_deadlock_report to %s, %d MB x %d, starts with SQL Server", pattern, fileMB, files)},
		Drift{Setting: "Blocked process threshold", Current: thresholdText(cur), Target: thresholdText(threshold)})
	p.Statement = XEInstallStatement(folder, threshold, fileMB, files, dispatch)
	p.Rollback = XERemoveStatement(max(cur, 0))
	return p, fix, nil
}

// pendingWithout are the pending settings a RECONFIGURE of one setting also activates.
func pendingWithout(ps []PendingConfig, setting string) []PendingConfig {
	out := []PendingConfig{}
	for _, p := range ps {
		if strings.EqualFold(p.Name, setting) || strings.EqualFold(p.Name, "show advanced options") {
			continue
		}
		out = append(out, p)
	}
	return out
}

// XEInstallDescription is install-xe-session's change log line, naming what its RECONFIGURE also
// activated.
func XEInstallDescription(server string, p FixPreview, pending []PendingConfig) string {
	s := "Created the event session kakapo on " + orQ(server)
	for _, c := range p.Changes {
		switch c.Setting {
		case XEObject:
			s += ": " + c.Target
		case "Blocked process threshold":
			s += "; blocked process threshold " + c.Current + " -> " + c.Target
		}
	}
	if pend := pendingWithout(pending, BlockedThresholdSetting); len(pend) > 0 {
		s += "; RECONFIGURE also activated: " + PendingList(pend)
	}
	return s
}

// XEInstallReadBack says what was read after the install: verified when kakapo's session exists and
// runs and the threshold in use is the one set.
func XEInstallReadBack(after XESession, threshold int) (string, bool) {
	state := "no session kakapo"
	switch {
	case after.Exists && after.Running:
		state = "session kakapo running"
	case after.Exists:
		state = "session kakapo created, but not running"
	}
	text := state + ", blocked process threshold " + thresholdText(after.ThresholdSeconds)
	return text, after.Exists && after.Ours && after.Running && after.ThresholdSeconds == threshold
}

// BuildRemoveXESessionFix builds remove-xe-session's preview with the threshold option (under More
// options: 0, off, by default; the threshold in use when another session records the blocked process
// report). ErrFixNotApplicable when there is no session named kakapo. A session named kakapo that
// kakapo did not create, or a bad threshold, leaves the preview without changes. The rollback creates
// the session again with its settings as read.
func BuildRemoveXESessionFix(f XERemoveFacts, options map[string]string) (FixPreview, Fix, error) {
	s := f.Session
	if !s.Exists {
		return FixPreview{}, Fix{}, ErrFixNotApplicable
	}
	cur := s.ThresholdSeconds
	def := 0
	if len(s.OtherSessions) > 0 && cur > 0 {
		def = cur
	}
	blocked := []string{}
	threshold, thIn, w := xeIntInput(options, OptionXEThreshold, "Blocked process threshold after (seconds)",
		"0 turns the blocked process report off, as SQL Server ships. Keep the value when another tool's session uses it.",
		def, 0, maxXEThresholdOff, true)
	if w != "" {
		blocked = append(blocked, w)
	}
	if !s.Ours {
		blocked = append(blocked, fmt.Sprintf("The session named kakapo on %s was not created by kakapo (its events or its target differ): kakapo does not remove it.", orQ(f.ServerName)))
	}
	opts := map[string]string{OptionXEThreshold: thIn.Value}
	pattern := s.FilePattern()
	state := "stopped"
	if s.Running {
		state = "running"
	}
	fix := suFix(FixRemoveXESession)
	fix.Current = state + ", " + orQ(pattern)
	fix.Target = "none"
	fix.Options = opts
	p := FixPreview{EngineVersion: f.ProductVersion, Changes: []Drift{}, Warnings: []string{}, Inputs: []FixInput{thIn}, Options: opts}
	p.Warnings = append(p.Warnings, blocked...)

	files := orQ(pattern)
	if len(f.Files) > 0 {
		files = strings.Join(f.Files, ", ")
	}
	size := ""
	if s.MaxFileMB > 0 && s.MaxFiles > 0 {
		size = fmt.Sprintf(" (at most %s MB)", n0int(int64(s.MaxFileMB*s.MaxFiles)))
	}
	p.Warnings = append(p.Warnings,
		"The .xel files stay on disk: "+files+". Delete them by hand when you no longer need them"+size+".",
		"The blocking history falls back to system_health's five-minute snapshots, so shorter blocks are no longer kept; the deadlock report keeps deadlocks only as long as system_health does.")
	if len(s.OtherSessions) > 0 {
		p.Warnings = append(p.Warnings, xeOthersWarn(s.OtherSessions))
	}
	p.Warnings = append(p.Warnings, warnServer(orQ(f.ServerName)))
	if pend := pendingWithout(f.Pending, BlockedThresholdSetting); len(pend) > 0 {
		p.Warnings = append(p.Warnings, warnPending(pend))
	}
	p.Warnings = append(p.Warnings, xePermission)
	if len(blocked) > 0 {
		return p, fix, nil
	}
	target := thresholdText(threshold)
	if threshold == cur {
		target += " (unchanged)"
	}
	p.Changes = append(p.Changes,
		Drift{Setting: XEObject, Current: state + ", " + xeFilesWords(orQ(pattern), s.MaxFileMB, s.MaxFiles), Target: "none (the files stay)"},
		Drift{Setting: "Blocked process threshold", Current: thresholdText(cur), Target: target})
	p.Statement = XERemoveStatement(threshold)
	if folder := s.Folder(); folder != "" && s.MaxFileMB > 0 && s.MaxFiles > 0 {
		dispatch := s.DispatchSec
		if dispatch <= 0 {
			dispatch = DefaultXEDispatch
		}
		p.Rollback = XEInstallStatement(folder, max(cur, 0), s.MaxFileMB, s.MaxFiles, dispatch)
	} else {
		p.Warnings = append(p.Warnings, "The session's file settings could not be read, so the rollback is not given: create it again from the Installed helpers row.")
	}
	return p, fix, nil
}

// XERemoveDescription is remove-xe-session's change log line.
func XERemoveDescription(server string, p FixPreview, pattern string, pending []PendingConfig) string {
	s := "Removed the event session kakapo on " + orQ(server) + "; its files stay in " + orQ(pattern)
	for _, c := range p.Changes {
		if c.Setting == "Blocked process threshold" {
			s += "; blocked process threshold " + c.Current + " -> " + strings.TrimSuffix(c.Target, " (unchanged)")
		}
	}
	if pend := pendingWithout(pending, BlockedThresholdSetting); len(pend) > 0 {
		s += "; RECONFIGURE also activated: " + PendingList(pend)
	}
	return s
}

// XERemoveReadBack says what was read after the remove: verified when no session named kakapo is
// left and the threshold in use is the one set.
func XERemoveReadBack(after XESession, threshold int) (string, bool) {
	state := "no session kakapo"
	if after.Exists {
		state = "session kakapo still there"
	}
	return state + ", blocked process threshold " + thresholdText(after.ThresholdSeconds), !after.Exists && after.ThresholdSeconds == threshold
}

// --- the instance page's row ---

// InstalledHelpers is the instance page's "Installed helpers" row (GET /api/servers/{id}/helpers):
// the sentence first, then each helper with its state and the fix the row offers. Database is the
// database the fix routes go through (a server fix still runs on a connection to one); "" when no
// user database is online.
type InstalledHelpers struct {
	ServerName string            `json:"serverName"`
	Database   string            `json:"database"`
	Sentence   string            `json:"sentence"`
	Helpers    []InstalledHelper `json:"helpers"`
}

// The states of a helper.
const (
	HelperRunning      = "running"
	HelperStopped      = "stopped"
	HelperNotInstalled = "not installed"
	HelperForeign      = "not kakapo's"
	HelperUnknown      = "unknown"
)

// InstalledHelper is one helper as the row shows it: what it is, where it stands in one sentence,
// the facts behind it, and the fix the row offers (Create or Remove).
type InstalledHelper struct {
	ID       string     `json:"id"`
	Title    string     `json:"title"`
	State    string     `json:"state"`
	Sentence string     `json:"sentence"`
	Details  []string   `json:"details"`
	FixID    string     `json:"fixId,omitempty"`
	FixLabel string     `json:"fixLabel,omitempty"`
	Session  *XESession `json:"session,omitempty"`
}

// BuildInstalledHelpers says where kakapo's helpers stand on a server, from what was read.
func BuildInstalledHelpers(f HelperFacts, database string) InstalledHelpers {
	s := f.Session
	h := InstalledHelper{ID: "xe-session", Title: "kakapo's event session (blocks and deadlocks)", Details: []string{}}
	session := s
	h.Session = &session
	pattern := s.FilePattern()
	switch {
	case !s.Exists && s.Error != "":
		h.State = HelperUnknown
		h.Sentence = "kakapo could not see whether its event session is installed: " + s.Error
	case !s.Exists:
		h.State, h.FixID, h.FixLabel = HelperNotInstalled, FixInstallXESession, "Create"
		h.Sentence = fmt.Sprintf("Nothing installed. kakapo's event session would keep every block over %d seconds and every deadlock on the server, so the blocking history and the deadlock report no longer depend on system_health's five-minute snapshots.", DefaultXEThreshold)
	case !s.Ours:
		h.State = HelperForeign
		h.Sentence = "A session named kakapo exists on this server, but kakapo did not create it (its events or its target differ): kakapo leaves it alone."
	case s.Running:
		h.State, h.FixID, h.FixLabel = HelperRunning, FixRemoveXESession, "Remove"
		since := ""
		if s.StartedAt != nil {
			since = " since " + utcText(*s.StartedAt)
		}
		if s.ThresholdSeconds == 0 {
			h.Sentence = fmt.Sprintf("kakapo's event session is running%s, but the blocked process threshold is 0 (off), so it keeps deadlocks only: Remove and Create set the threshold again.", since)
		} else {
			h.Sentence = fmt.Sprintf("kakapo's event session is running%s, keeping every block over %s and every deadlock in %s; %s captured so far.",
				since, thresholdWords(s.ThresholdSeconds), orQ(pattern), plural(s.EventsCaptured, "event", "events"))
		}
	default:
		h.State, h.FixID, h.FixLabel = HelperStopped, FixRemoveXESession, "Remove"
		h.Sentence = fmt.Sprintf("kakapo's event session exists but is stopped, so nothing new is kept; what it kept is still read from %s.", orQ(pattern))
	}
	if s.Exists {
		if pattern != "" {
			h.Details = append(h.Details, "Files: "+xeFilesWords(pattern, s.MaxFileMB, s.MaxFiles))
		}
		if s.CurrentFile != "" {
			h.Details = append(h.Details, "Writing now: "+s.CurrentFile)
		}
		h.Details = append(h.Details, "Blocked process threshold: "+thresholdText(s.ThresholdSeconds))
		if s.StartupState {
			h.Details = append(h.Details, "Starts with SQL Server")
		}
		if s.Error != "" {
			h.Details = append(h.Details, "Not everything could be read: "+s.Error)
		}
	}
	if len(s.OtherSessions) > 0 {
		h.Details = append(h.Details, "Other sessions recording the blocked process report: "+strings.Join(s.OtherSessions, ", "))
	}
	out := InstalledHelpers{ServerName: f.ServerName, Database: database, Helpers: []InstalledHelper{h}}
	switch h.State {
	case HelperRunning, HelperStopped:
		out.Sentence = "1 helper installed by kakapo: " + h.Sentence
	default:
		out.Sentence = h.Sentence
	}
	return out
}

// thresholdWords is "5 s", or "the blocked process threshold" when it was not read.
func thresholdWords(n int) string {
	if n < 0 {
		return "the blocked process threshold"
	}
	return fmt.Sprintf("%d s", n)
}
