package collect

import (
	"database/sql"
	"strings"
	"testing"
	"time"

	"sqlmonitor/internal/domain"
)

// The helper reads are tagged once with the safe session first and read-only (docs/phase5e-contract.md,
// Tests): nothing creates, alters, drops or configures; each area that needs a permission is in its own
// TRY with an error column; the dynamic batch is tagged too. The folder check is xp_fileexist's answer.
func TestHelperReads(t *testing.T) {
	for name, q := range map[string]string{"session": Tag(xeSessionQuery), "files": Tag(xeFilesQuery), "folder": Tag(folderExistsQuery)} {
		suReadOnly(t, name, q)
		upper := strings.ToUpper(q)
		for _, verb := range []string{"CREATE EVENT", "DROP EVENT", "STATE = START", "STATE = STOP"} {
			if strings.Contains(upper, verb) {
				t.Errorf("%s query contains %q", name, verb)
			}
		}
		if n, tagged := strings.Count(q, "sp_executesql N'"), strings.Count(q, "N'/* kakapo */"); n != tagged {
			t.Errorf("%s query: %d dynamic batches, %d tagged", name, n, tagged)
		}
	}
	if n := strings.Count(xeSessionQuery, "BEGIN TRY"); n != 4 {
		t.Errorf("%d TRY blocks in the session read: the catalog, the running session, the threshold, the other sessions", n)
	}
	for _, want := range []string{"FROM sys.server_event_sessions AS s WHERE s.name = N'kakapo'", "sys.server_event_session_events", "sys.server_event_session_targets",
		"f.name = N'max_file_size'", "f.name = N'max_rollover_files'", "f.name = N'filename'", "t.target_id = f.object_id",
		"FROM sys.dm_xe_sessions AS x WHERE x.name = N'kakapo'", "DATEADD(MINUTE, @offset, x.create_time)", "t.execution_count",
		"(EventFileTarget/File/@name)[1]", "c.name = N'blocked process threshold (s)'", "e.name = N'blocked_process_report' AND s.name <> N'kakapo'",
		"SERVERPROPERTY('ErrorLogFileName')", "SELECT kind, name FROM @names ORDER BY kind, name;"} {
		if !strings.Contains(xeSessionQuery, want) {
			t.Errorf("session read lacks %q", want)
		}
	}
	if !strings.Contains(xeFilesQuery, "sys.dm_os_enumerate_filesystem(@folder, N''kakapo*.xel'')") || strings.Count(xeFilesQuery, "BEGIN TRY") != 1 ||
		!strings.Contains(xeFilesQuery, "IF @major < 14") {
		t.Errorf("files read\n%s", xeFilesQuery)
	}
	if !strings.Contains(folderExistsQuery, "EXEC master.dbo.xp_fileexist @folder") {
		t.Errorf("folder check\n%s", folderExistsQuery)
	}
	// The deadlock read takes kakapo's files first, in their own TRY, and a failure of system_health's
	// files never drops kakapo's events.
	for _, want := range []string{"IF ISNULL(@kpath, N'') <> N''", "WHERE object_name = N''xml_deadlock_report''", "@p = @kpath, @days = @days",
		"N''kakapo''", "DELETE @events WHERE src IS NULL;", "SELECT @source, @err, @fileErr, @healthErr, @kread, @kakapoErr;", "SELECT event_xml, src FROM @events;"} {
		if !strings.Contains(deadlockQuery, want) {
			t.Errorf("deadlock read lacks %q", want)
		}
	}
	if strings.Index(deadlockQuery, "@kpath") > strings.Index(deadlockQuery, "N'system_health'") {
		t.Error("the deadlock read does not take kakapo's files first")
	}
	if strings.Index(blockingHistoryQuery, "@kpath, NULL, NULL, NULL") > strings.Index(blockingHistoryQuery, "N'system_health'") {
		t.Error("the blocking history does not take kakapo's files first")
	}
}

// The session state from the read: kakapo's shape marked, the dispatch latency in seconds, the start in
// UTC, the other sessions, and the parts that failed named.
func TestXESessionFrom(t *testing.T) {
	r := xeRow{exists: true, running: true, startup: true,
		dispatch: sql.NullInt64{Int64: 30000, Valid: true}, maxMB: sql.NullInt64{Int64: 50, Valid: true}, maxFiles: sql.NullInt64{Int64: 4, Valid: true},
		execs: sql.NullInt64{Int64: 12, Valid: true}, threshold: sql.NullInt64{Int64: 5, Valid: true},
		created: sql.NullTime{Time: time.Date(2026, 11, 4, 8, 0, 0, 0, time.Local), Valid: true},
		file:    sql.NullString{String: `D:\Log\kakapo.xel`, Valid: true}, current: sql.NullString{String: `D:\Log\kakapo_0_1.xel`, Valid: true}}
	names := [][2]string{{"event", "sqlserver.blocked_process_report"}, {"event", "sqlserver.xml_deadlock_report"}, {"target", "package0.event_file"},
		{"other", "MonitorTool"}}
	s := xeSessionFrom(r, names)
	if !s.Ours || s.DispatchSec != 30 || s.MaxFileMB != 50 || s.MaxFiles != 4 || s.EventsCaptured != 12 || s.ThresholdSeconds != 5 || !s.StartupState ||
		s.StartedAt == nil || s.StartedAt.Location() != time.UTC || s.StartedAt.Hour() != 8 || len(s.OtherSessions) != 1 || s.Error != "" ||
		s.FilePattern() != `D:\Log\kakapo*.xel` {
		t.Errorf("session %+v", s)
	}
	r.threshold, r.running, r.runErr = sql.NullInt64{}, false, sql.NullString{String: "VIEW SERVER STATE permission was denied", Valid: true}
	s = xeSessionFrom(r, names[:1])
	if s.Ours || s.ThresholdSeconds != -1 || s.StartedAt != nil || s.Error != "could not read the running sessions (VIEW SERVER STATE permission was denied)" {
		t.Errorf("partial %+v", s)
	}
}

// The file-first source selection: kakapo's files alone when they cover the window, kakapo and then
// system_health's file or ring buffer when they begin inside it, system_health when they were not read;
// the parameters are empty without kakapo's session.
func TestHistorySource(t *testing.T) {
	since := time.Date(2026, 10, 28, 10, 0, 0, 0, time.UTC)
	early, late := since.Add(-time.Hour), since.Add(48*time.Hour)
	for _, c := range []struct {
		read bool
		cut  *time.Time
		sh   string
		want string
	}{
		{false, nil, "file", "file"},
		{false, nil, "ring-buffer", "ring-buffer"},
		{true, &early, "", "kakapo"},
		{true, &late, "file", "kakapo+file"},
		{true, &late, "ring-buffer", "kakapo+ring-buffer"},
		{true, nil, "file", "kakapo+file"},
		{true, &late, "", "kakapo"}, // system_health could not be read
	} {
		if got := historySource(c.read, c.cut, since, c.sh); got != c.want {
			t.Errorf("historySource(%v, %v, %q) = %q, want %q", c.read, c.cut, c.sh, got, c.want)
		}
	}
	if p, s := kakapoParams(domain.XEFiles{}); p != "" || s != "" {
		t.Errorf("no session %q %q", p, s)
	}
	started := time.Date(2026, 11, 4, 8, 15, 30, 0, time.UTC)
	if p, s := kakapoParams(domain.XEFiles{Pattern: `D:\Log\kakapo*.xel`, StartedAt: &started}); p != `D:\Log\kakapo*.xel` || s != "2026-11-04T08:15:30.000" {
		t.Errorf("session %q %q", p, s)
	}
}
