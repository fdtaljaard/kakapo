package domain

import (
	"strings"
	"testing"
	"time"
)

const xeLog = `C:\Program Files\Microsoft SQL Server\MSSQL16.SQL2022\MSSQL\Log\ERRORLOG`

// xeOurs is kakapo's session as collect reads it once installed with the defaults.
func xeOurs(running bool) XESession {
	started := time.Date(2026, 11, 4, 8, 15, 0, 0, time.UTC)
	s := XESession{Exists: true, Running: running, Events: []string{"sqlserver.xml_deadlock_report", "sqlserver.blocked_process_report"},
		Targets: []string{"package0.event_file"}, FileName: `C:\Program Files\Microsoft SQL Server\MSSQL16.SQL2022\MSSQL\Log\kakapo.xel`,
		MaxFileMB: 50, MaxFiles: 4, DispatchSec: 30, StartupState: true, EventsCaptured: 1234, ThresholdSeconds: 5}
	if running {
		s.StartedAt = &started
		s.CurrentFile = `C:\Program Files\Microsoft SQL Server\MSSQL16.SQL2022\MSSQL\Log\kakapo_0_134070000000000000.xel`
	}
	s.Ours = s.HasKakapoShape()
	return s
}

func xeInstallFacts() XEInstallFacts {
	return XEInstallFacts{
		HelperFacts:   HelperFacts{ServerName: `SQL01\SQL2022`, ProductVersion: "16.0.4135.4", EngineEdition: 3, ErrorLogFile: xeLog, Session: XESession{ThresholdSeconds: 0}},
		FolderChecked: true, FolderExists: true,
	}
}

// The install with the defaults: the threshold, the session to the error log's folder, the two change
// rows, the warnings in the contract's order, the rollback with the previous threshold.
func TestInstallXESessionDefaults(t *testing.T) {
	p, fix, err := BuildInstallXESessionFix(xeInstallFacts(), nil)
	if err != nil {
		t.Fatal(err)
	}
	want := "SET XACT_ABORT ON;\n" +
		"EXEC sp_configure 'show advanced options', 1; RECONFIGURE; EXEC sp_configure 'blocked process threshold (s)', 5; RECONFIGURE;\n" +
		"CREATE EVENT SESSION [kakapo] ON SERVER\n" +
		"    ADD EVENT sqlserver.blocked_process_report,\n" +
		"    ADD EVENT sqlserver.xml_deadlock_report\n" +
		`    ADD TARGET package0.event_file (SET filename = N'C:\Program Files\Microsoft SQL Server\MSSQL16.SQL2022\MSSQL\Log\kakapo.xel', max_file_size = 50, max_rollover_files = 4)` + "\n" +
		"    WITH (MAX_MEMORY = 4096 KB, EVENT_RETENTION_MODE = ALLOW_SINGLE_EVENT_LOSS, MAX_DISPATCH_LATENCY = 30 SECONDS,\n" +
		"          STARTUP_STATE = ON, TRACK_CAUSALITY = OFF);\n" +
		"ALTER EVENT SESSION [kakapo] ON SERVER STATE = START;"
	if p.Statement != want {
		t.Errorf("statement\n%s\nwant\n%s", p.Statement, want)
	}
	rollback := "IF EXISTS (SELECT 1 FROM sys.dm_xe_sessions WHERE name = N'kakapo') ALTER EVENT SESSION [kakapo] ON SERVER STATE = STOP;\n" +
		"IF EXISTS (SELECT 1 FROM sys.server_event_sessions WHERE name = N'kakapo') DROP EVENT SESSION [kakapo] ON SERVER;\n" +
		"EXEC sp_configure 'show advanced options', 1; RECONFIGURE; EXEC sp_configure 'blocked process threshold (s)', 0; RECONFIGURE;"
	if p.Rollback != rollback {
		t.Errorf("rollback\n%s", p.Rollback)
	}
	if len(p.Changes) != 2 ||
		p.Changes[0] != (Drift{Setting: "Event session kakapo", Current: "none",
			Target: `blocked_process_report and xml_deadlock_report to C:\Program Files\Microsoft SQL Server\MSSQL16.SQL2022\MSSQL\Log\kakapo*.xel, 50 MB x 4, starts with SQL Server`}) ||
		p.Changes[1] != (Drift{Setting: "Blocked process threshold", Current: "0 (off)", Target: "5 s"}) {
		t.Errorf("changes %+v", p.Changes)
	}
	wantWarn := []string{
		`This is a server setting: it changes every database on SQL01\SQL2022.`,
		"Every block over 5 s is written to the file with both statements; on a server with constant blocking the files turn over faster (at most 200 MB on disk, never more).",
		"The session costs almost nothing while nothing blocks; the blocked process report is produced by the lock monitor, which already runs.",
		"Deadlocks are also kept here, so the deadlock report no longer depends on system_health's retention.",
		"Needs ALTER ANY EVENT SESSION and ALTER SETTINGS: sysadmin.",
	}
	if strings.Join(p.Warnings, "\n") != strings.Join(wantWarn, "\n") {
		t.Errorf("warnings\n%s", strings.Join(p.Warnings, "\n"))
	}
	if len(p.Inputs) != 5 || p.Inputs[0].Name != OptionXEThreshold || p.Inputs[0].Advanced || p.Inputs[0].Value != "5" ||
		p.Inputs[1].Name != OptionXEFolder || !p.Inputs[1].Advanced || p.Inputs[1].Value != `C:\Program Files\Microsoft SQL Server\MSSQL16.SQL2022\MSSQL\Log` ||
		p.Inputs[2].Value != "50" || p.Inputs[3].Value != "4" || p.Inputs[4].Value != "30" || !p.Inputs[4].Advanced {
		t.Errorf("inputs %+v", p.Inputs)
	}
	if p.Options[OptionXEThreshold] != "5" || p.Options[OptionXEFiles] != "4" {
		t.Errorf("options %v", p.Options)
	}
	if fix.ID != FixInstallXESession || fix.CheckID != "BLK01" || fix.Scope != ScopeServer || fix.Setting != "Event session kakapo" || fix.Current != "none" {
		t.Errorf("fix %+v", fix)
	}
	if d := XEInstallDescription(`SQL01\SQL2022`, p, nil); d != `Created the event session kakapo on SQL01\SQL2022: blocked_process_report and xml_deadlock_report to `+
		`C:\Program Files\Microsoft SQL Server\MSSQL16.SQL2022\MSSQL\Log\kakapo*.xel, 50 MB x 4, starts with SQL Server; blocked process threshold 0 (off) -> 5 s` {
		t.Errorf("description %q", d)
	}
}

// The options change the statement; a threshold set by someone else is warned about and put back by the
// rollback; the pending settings RECONFIGURE also activates are named (INS14); a Linux folder keeps /.
func TestInstallXESessionOptions(t *testing.T) {
	f := xeInstallFacts()
	f.Session.ThresholdSeconds = 20
	f.Session.OtherSessions = []string{"MonitorTool"}
	f.Pending = []PendingConfig{{Name: "max degree of parallelism", Value: 4, ValueInUse: 0}, {Name: BlockedThresholdSetting, Value: 20, ValueInUse: 0}}
	p, fix, err := BuildInstallXESessionFix(f, map[string]string{OptionXEThreshold: " 10 ", OptionXEFolder: `D:\XE\`, OptionXEFileMB: "100", OptionXEFiles: "6", OptionXEDispatch: "10"})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"'blocked process threshold (s)', 10; RECONFIGURE;", `SET filename = N'D:\XE\kakapo.xel', max_file_size = 100, max_rollover_files = 6)`,
		"MAX_DISPATCH_LATENCY = 10 SECONDS"} {
		if !strings.Contains(p.Statement, want) {
			t.Errorf("statement lacks %q\n%s", want, p.Statement)
		}
	}
	if !strings.HasSuffix(p.Rollback, "'blocked process threshold (s)', 20; RECONFIGURE;") {
		t.Errorf("rollback %s", p.Rollback)
	}
	all := strings.Join(p.Warnings, "\n")
	for _, want := range []string{"The blocked process threshold is 20 s now: kakapo sets it to 10 s, and the rollback puts 20 s back.",
		"The event session MonitorTool also records the blocked process report: it follows the new threshold too.",
		"RECONFIGURE also activates these pending changes: max degree of parallelism",
		"at most 600 MB on disk"} {
		if !strings.Contains(all, want) {
			t.Errorf("warnings lack %q\n%s", want, all)
		}
	}
	if strings.Contains(all, "pending changes: max degree of parallelism 0 -> 4, blocked") {
		t.Errorf("the threshold itself is named as pending\n%s", all)
	}
	if p.Changes[1].Current != "20 s" || p.Changes[1].Target != "10 s" || fix.Options[OptionXEFolder] != `D:\XE` {
		t.Errorf("changes %+v options %v", p.Changes, fix.Options)
	}
	// Linux.
	f = xeInstallFacts()
	f.ErrorLogFile = "/var/opt/mssql/log/errorlog"
	if folder := XEFolder(f.HelperFacts, nil); folder != "/var/opt/mssql/log" {
		t.Errorf("linux folder %q", folder)
	}
	p, _, _ = BuildInstallXESessionFix(f, nil)
	if !strings.Contains(p.Statement, "SET filename = N'/var/opt/mssql/log/kakapo.xel'") {
		t.Errorf("linux statement\n%s", p.Statement)
	}
}

// Every blocking case leaves the preview without changes, a statement or a rollback, its reason first;
// kakapo's own session already there is nothing to apply.
func TestInstallXESessionBlocked(t *testing.T) {
	foreign := XESession{Exists: true, Events: []string{"sqlserver.blocked_process_report"}, Targets: []string{"package0.ring_buffer"}}
	for _, c := range []struct {
		name    string
		mod     func(*XEInstallFacts)
		options map[string]string
		want    string
	}{
		{"threshold 0", nil, map[string]string{OptionXEThreshold: "0"}, "Keep blocks longer than (seconds) must be a whole number from 1 to 60."},
		{"threshold 61", nil, map[string]string{OptionXEThreshold: "61"}, "from 1 to 60"},
		{"threshold text", nil, map[string]string{OptionXEThreshold: "five"}, "from 1 to 60"},
		{"file size", nil, map[string]string{OptionXEFileMB: "5"}, "File size (MB) must be a whole number from 10 to 500."},
		{"files", nil, map[string]string{OptionXEFiles: "11"}, "Files kept must be a whole number from 2 to 10."},
		{"dispatch", nil, map[string]string{OptionXEDispatch: "-1"}, "from 5 to 60"},
		{"folder relative", nil, map[string]string{OptionXEFolder: `Logs\XE`}, "The folder must be a full path on the server"},
		{"folder missing", func(f *XEInstallFacts) { f.FolderExists = false }, nil, `does not exist on SQL01\SQL2022 as the SQL Server service sees it`},
		{"not kakapo's", func(f *XEInstallFacts) { f.Session = foreign }, nil, "A session named kakapo already exists"},
		{"2008 R2", func(f *XEInstallFacts) { f.ProductVersion = "10.50.6000.34" }, nil, "needs SQL Server 2012 or later"},
		{"Azure SQL Database", func(f *XEInstallFacts) { f.EngineEdition = 5 }, nil, "Azure SQL Database has no server event sessions"},
		{"Managed Instance", func(f *XEInstallFacts) { f.EngineEdition = 8 }, nil, "Managed Instance"},
	} {
		f := xeInstallFacts()
		if c.mod != nil {
			c.mod(&f)
		}
		p, _, err := BuildInstallXESessionFix(f, c.options)
		if err != nil {
			t.Errorf("%s: %v", c.name, err)
			continue
		}
		if len(p.Changes) != 0 || p.Statement != "" || p.Rollback != "" || len(p.Inputs) != 5 {
			t.Errorf("%s: applicable %+v", c.name, p)
		}
		if len(p.Warnings) == 0 || !strings.Contains(p.Warnings[0], c.want) {
			t.Errorf("%s: warnings %q", c.name, p.Warnings)
		}
	}
	// A folder that could not be checked is a warning, not a block.
	f := xeInstallFacts()
	f.FolderChecked, f.FolderExists, f.FolderError = false, false, "xp_fileexist needs sysadmin"
	if p, _, _ := BuildInstallXESessionFix(f, nil); len(p.Changes) != 2 || !strings.Contains(p.Warnings[0], "could not check that") {
		t.Errorf("unchecked folder %+v", p)
	}
	f = xeInstallFacts()
	f.Session = xeOurs(false)
	if _, _, err := BuildInstallXESessionFix(f, nil); err != ErrFixNotApplicable {
		t.Errorf("kakapo's session exists: %v", err)
	}
}

// The read-back: verified only when kakapo's session runs with the threshold set.
func TestXEReadBacks(t *testing.T) {
	if s, ok := XEInstallReadBack(xeOurs(true), 5); !ok || s != "session kakapo running, blocked process threshold 5 s" {
		t.Errorf("running %q %v", s, ok)
	}
	if s, ok := XEInstallReadBack(xeOurs(false), 5); ok || s != "session kakapo created, but not running, blocked process threshold 5 s" {
		t.Errorf("stopped %q %v", s, ok)
	}
	if _, ok := XEInstallReadBack(xeOurs(true), 10); ok {
		t.Error("another threshold verified")
	}
	if s, ok := XEInstallReadBack(XESession{ThresholdSeconds: 0}, 5); ok || s != "no session kakapo, blocked process threshold 0 (off)" {
		t.Errorf("none %q %v", s, ok)
	}
	if s, ok := XERemoveReadBack(XESession{ThresholdSeconds: 0}, 0); !ok || s != "no session kakapo, blocked process threshold 0 (off)" {
		t.Errorf("removed %q %v", s, ok)
	}
	if _, ok := XERemoveReadBack(xeOurs(true), 5); ok {
		t.Error("a session left verified")
	}
}

// The remove: the drop and the threshold back to 0, the files named, the rollback creating the session
// again as it was read; nothing to apply without a session; a session kakapo did not create is not removed.
func TestRemoveXESession(t *testing.T) {
	f := XERemoveFacts{HelperFacts: HelperFacts{ServerName: `SQL01\SQL2022`, ProductVersion: "16.0.4135.4", Session: xeOurs(true)},
		Files: []string{`C:\Log\kakapo_0_1.xel`, `C:\Log\kakapo_0_2.xel`}}
	p, fix, err := BuildRemoveXESessionFix(f, nil)
	if err != nil {
		t.Fatal(err)
	}
	if p.Statement != XERemoveStatement(0) || !strings.HasSuffix(p.Statement, "'blocked process threshold (s)', 0; RECONFIGURE;") {
		t.Errorf("statement\n%s", p.Statement)
	}
	if p.Rollback != XEInstallStatement(`C:\Program Files\Microsoft SQL Server\MSSQL16.SQL2022\MSSQL\Log`, 5, 50, 4, 30) {
		t.Errorf("rollback\n%s", p.Rollback)
	}
	if len(p.Changes) != 2 || p.Changes[0].Target != "none (the files stay)" ||
		p.Changes[1] != (Drift{Setting: "Blocked process threshold", Current: "5 s", Target: "0 (off)"}) {
		t.Errorf("changes %+v", p.Changes)
	}
	if p.Warnings[0] != `The .xel files stay on disk: C:\Log\kakapo_0_1.xel, C:\Log\kakapo_0_2.xel. Delete them by hand when you no longer need them (at most 200 MB).` ||
		!strings.Contains(p.Warnings[1], "falls back to system_health") || p.Warnings[len(p.Warnings)-1] != xePermission {
		t.Errorf("warnings %q", p.Warnings)
	}
	if fix.ID != FixRemoveXESession || !strings.HasPrefix(fix.Current, "running, ") || len(p.Inputs) != 1 || !p.Inputs[0].Advanced || p.Inputs[0].Value != "0" {
		t.Errorf("fix %+v inputs %+v", fix, p.Inputs)
	}
	if d := XERemoveDescription("SQL01", p, f.Session.FilePattern(), nil); !strings.HasSuffix(d, "; blocked process threshold 5 s -> 0 (off)") ||
		!strings.Contains(d, `files stay in C:\Program Files\Microsoft SQL Server\MSSQL16.SQL2022\MSSQL\Log\kakapo*.xel`) {
		t.Errorf("description %q", d)
	}
	// Without the list, the pattern; another session on the report keeps the threshold by default.
	f.Files, f.Session.OtherSessions = nil, []string{"MonitorTool"}
	p, _, _ = BuildRemoveXESessionFix(f, nil)
	if !strings.Contains(p.Warnings[0], `kakapo*.xel. Delete them`) || p.Changes[1].Target != "5 s (unchanged)" {
		t.Errorf("pattern / other session %q %+v", p.Warnings[0], p.Changes)
	}
	if p, _, _ := BuildRemoveXESessionFix(f, map[string]string{OptionXEThreshold: "-3"}); len(p.Changes) != 0 || p.Statement != "" {
		t.Errorf("bad threshold applicable %+v", p)
	}
	f.Session = XESession{Exists: true, Events: []string{"sqlserver.lock_acquired"}, Targets: []string{"package0.event_file"}, FileName: `C:\x\kakapo.xel`}
	if p, _, _ := BuildRemoveXESessionFix(f, nil); len(p.Changes) != 0 || !strings.Contains(p.Warnings[0], "was not created by kakapo") {
		t.Errorf("foreign %+v", p)
	}
	f.Session = XESession{}
	if _, _, err := BuildRemoveXESessionFix(f, nil); err != ErrFixNotApplicable {
		t.Errorf("no session: %v", err)
	}
}

// kakapo's shape, the file pattern and what the reads take from the session.
func TestXESessionShape(t *testing.T) {
	s := xeOurs(true)
	if !s.Ours || s.FilePattern() != `C:\Program Files\Microsoft SQL Server\MSSQL16.SQL2022\MSSQL\Log\kakapo*.xel` || s.Files().Pattern != s.FilePattern() ||
		s.Files().StartedAt == nil {
		t.Errorf("ours %+v %q", s, s.FilePattern())
	}
	other := s
	other.Events = append(other.Events, "sqlserver.lock_timeout")
	if other.HasKakapoShape() {
		t.Error("a third event is kakapo's")
	}
	other = s
	other.FileName = `C:\Log\blocking.xel`
	if other.HasKakapoShape() {
		t.Error("another file is kakapo's")
	}
	if (XESession{}).Files().Pattern != "" || (XESession{Exists: true}).Files().Pattern != "" {
		t.Error("files without kakapo's session")
	}
	if p := (XESession{FileName: "/var/opt/mssql/log/kakapo.xel"}).FilePattern(); p != "/var/opt/mssql/log/kakapo*.xel" {
		t.Errorf("linux pattern %q", p)
	}
	if d := ErrorLogFolder(`D:\ERRORLOG`); d != `D:\` {
		t.Errorf("drive folder %q", d)
	}
}

// The instance page's row: nothing installed with Create, running with Remove and the facts, stopped,
// someone else's session, and a read that failed.
func TestBuildInstalledHelpers(t *testing.T) {
	none := BuildInstalledHelpers(HelperFacts{ServerName: "SQL01", Session: XESession{ThresholdSeconds: 0}}, "Shop")
	h := none.Helpers[0]
	if h.State != HelperNotInstalled || h.FixID != FixInstallXESession || h.FixLabel != "Create" || none.Database != "Shop" ||
		!strings.HasPrefix(none.Sentence, "Nothing installed. kakapo's event session would keep every block over 5 seconds") {
		t.Errorf("none %+v", none)
	}
	run := BuildInstalledHelpers(HelperFacts{Session: xeOurs(true)}, "")
	h = run.Helpers[0]
	if h.State != HelperRunning || h.FixID != FixRemoveXESession || h.FixLabel != "Remove" ||
		h.Sentence != `kakapo's event session is running since 2026-11-04 08:15 UTC, keeping every block over 5 s and every deadlock in `+
			`C:\Program Files\Microsoft SQL Server\MSSQL16.SQL2022\MSSQL\Log\kakapo*.xel; 1,234 events captured so far.` ||
		!strings.HasPrefix(run.Sentence, "1 helper installed by kakapo: ") || len(h.Details) != 4 {
		t.Errorf("running %+v", run)
	}
	off := xeOurs(true)
	off.ThresholdSeconds = 0
	if s := BuildInstalledHelpers(HelperFacts{Session: off}, "").Helpers[0].Sentence; !strings.Contains(s, "threshold is 0 (off), so it keeps deadlocks only") {
		t.Errorf("threshold off %q", s)
	}
	if h := BuildInstalledHelpers(HelperFacts{Session: xeOurs(false)}, "").Helpers[0]; h.State != HelperStopped || h.FixID != FixRemoveXESession {
		t.Errorf("stopped %+v", h)
	}
	foreign := XESession{Exists: true, Events: []string{"sqlserver.lock_acquired"}}
	if h := BuildInstalledHelpers(HelperFacts{Session: foreign}, "").Helpers[0]; h.State != HelperForeign || h.FixID != "" {
		t.Errorf("foreign %+v", h)
	}
	if h := BuildInstalledHelpers(HelperFacts{Session: XESession{Error: "could not read the session catalog (denied)"}}, "").Helpers[0]; h.State != HelperUnknown || h.FixID != "" {
		t.Errorf("unknown %+v", h)
	}
}

// The deadlocks of both sources: kakapo's from the time its files begin, system_health's before.
func TestMergeKakapoDeadlocks(t *testing.T) {
	day := func(d int) time.Time { return time.Date(2026, 11, d, 10, 0, 0, 0, time.UTC) }
	kakapo := []DeadlockEvent{{Time: day(3), Database: "k1"}, {Time: day(4), Database: "k2"}}
	sh := []DeadlockEvent{{Time: day(1), Database: "s1"}, {Time: day(3), Database: "s-dup"}, {Time: day(4), Database: "s-dup2"}}
	started := day(2)
	got, since := MergeKakapoDeadlocks(kakapo, sh, &started)
	if since == nil || !since.Equal(day(2)) || len(got) != 3 || got[0].Database != "s1" || got[1].Database != "k1" || got[2].Database != "k2" {
		t.Errorf("merge %+v since %v", got, since)
	}
	// No start known (the session stopped): its first deadlock is the cut.
	got, since = MergeKakapoDeadlocks(kakapo, sh, nil)
	if since == nil || !since.Equal(day(3)) || len(got) != 3 {
		t.Errorf("no start %+v since %v", got, since)
	}
	got, since = MergeKakapoDeadlocks(nil, sh, nil)
	if since != nil || len(got) != 3 {
		t.Errorf("nothing of kakapo %+v", got)
	}
	r := BuildDeadlockReport(DeadlockInputs{Database: "k1", CapturedAt: day(5), Days: 7, Source: KakapoSource("file", false), Events: got, KakapoSince: &started})
	if r.Source != "kakapo+file" || r.KakapoSince == nil {
		t.Errorf("report %+v", r)
	}
}
