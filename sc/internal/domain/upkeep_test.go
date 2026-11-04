package domain

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

// goodUpkeep is a looked-after SQL Server 2022 Standard instance and database: Agent running and
// automatic, a maintenance job, no failures, an operator and every alert, nothing in the trace.
func goodUpkeep() UpkeepFacts {
	return UpkeepFacts{
		ServerName: `SQL01\WMS`, ProductVersion: "16.0.4255.3", EngineEdition: 2,
		Database: "WarehouseLive", State: "ONLINE", CanChange: true,
		AgentStatus: "Running", AgentStartup: "Automatic",
		FailedJobs: []FailedJob{}, MaintenanceJob: "IndexOptimize - USER_DATABASES", ShrinkJobs: []ShrinkJob{},
		MissingAlerts: []string{}, Operators: 1, AlertsOn: 10,
		TraceRunning: true, TraceCommands: []TraceCommand{}, SevereErrors: []SevereErrors{},
		ServerNow: setupNow, UTCOffsetMinutes: 120,
	}
}

func upkeepDef() CardDef { d, _ := FindCardDef(CardUpkeep); return d }

func runUpkeep(f UpkeepFacts) (Card, []Finding, []CheckStatus) {
	return BuildUpkeepCard(f, upkeepDef())
}

func stale(schema, table, stats string, rows, mods, tableRows int64) StaleStat {
	return StaleStat{Schema: schema, Table: table, Stats: stats, Rows: rows, Modifications: mods,
		LastUpdated: setupNow.AddDate(0, 0, -10), TableRows: tableRows}
}

func TestUpkeepGreen(t *testing.T) {
	c, fs, cs := runUpkeep(goodUpkeep())
	if c.Status != StatusGreen || c.StatusText != "No problem found" || c.ToDo+c.ForClientIT+c.Fixable != 0 || len(fs) != 0 {
		t.Errorf("green: %+v %+v", c, fs)
	}
	if want := `SQL Server Agent is running, "IndexOptimize - USER_DATABASES" runs and no job failed in 7 days.`; c.Verdict != want {
		t.Errorf("verdict %q", c.Verdict)
	}
	if c.AsOf == nil || !c.AsOf.Equal(time.Date(2026, 11, 3, 9, 21, 0, 0, time.UTC)) {
		t.Errorf("asOf %v", c.AsOf)
	}
	wantFacts(t, c, "SQL Agent=running, automatic/normal", "Failed jobs, 7 days=0/normal", "Maintenance job=found/normal",
		"Told about errors=operator and 10 alerts/normal")
	if len(cs) != 7 {
		t.Fatalf("checks %+v", cs)
	}
	for i, x := range cs {
		if x.CheckID != []string{"MNT01", "MNT02", "MNT03", "MNT04", "MNT05", "MNT06", "MNT07"}[i] || x.Status != CheckPassed || x.Note != "" || x.Title == "" {
			t.Errorf("check %+v", x)
		}
	}
	// A maintenance plan instead of a job.
	f := goodUpkeep()
	f.MaintenanceJob, f.MaintenancePlans = "", 2
	if c, _, _ := runUpkeep(f); c.Verdict != "SQL Server Agent is running, maintenance runs and no job failed in 7 days." {
		t.Errorf("plan verdict %q", c.Verdict)
	}
}

func TestUpkeepMNT01(t *testing.T) {
	f := goodUpkeep()
	f.AgentStatus, f.AgentStartup = "Stopped", "Manual"
	c, fs, cs := runUpkeep(f)
	x := one(t, fs, "MNT01")
	if x.Severity != SeverityHigh || x.Owner != OwnerClientIT || x.When != WhenOnline || x.Effort != EffortQuickWin ||
		x.Object != `SQL01\WMS` || x.Finding != "SQL Server Agent is not running." || x.Evidence != "Status: Stopped, startup: Manual." ||
		x.Why != mnt01StoppedWhy || x.DoThis != mnt01StoppedDo || x.ThenCheck != "Re-run: MNT01 gone." {
		t.Errorf("stopped %+v", x)
	}
	if c.Status != StatusAmber || c.StatusText != "1 thing to do" || c.Verdict != x.Finding || c.ForClientIT != 1 || statusOf(cs, "MNT01").Status != CheckFinding {
		t.Errorf("card %+v", c)
	}
	wantFacts(t, c, "SQL Agent=stopped/worse", "Failed jobs, 7 days=0/normal", "Maintenance job=found/normal",
		"Told about errors=operator and 10 alerts/normal")

	f.AgentStartup = ""
	if _, fs, _ = runUpkeep(f); one(t, fs, "MNT01").Evidence != "Status: Stopped, startup: ?." {
		t.Errorf("no startup %+v", fs)
	}
	f.AgentStatus = "Paused"
	if c, _, _ = runUpkeep(f); c.Facts[0].Value != "paused" || c.Facts[0].Tone != ToneWorse {
		t.Errorf("paused %+v", c.Facts[0])
	}

	f = goodUpkeep()
	f.AgentStartup = "Manual"
	c, fs, _ = runUpkeep(f)
	x = one(t, fs, "MNT01")
	if x.Severity != SeverityLow || x.Finding != "SQL Server Agent will not start automatically after a reboot." ||
		x.Evidence != "Startup type: Manual." || x.DoThis != mnt01ManualDo || x.Why != mnt01ManualWhy {
		t.Errorf("manual %+v", x)
	}
	if c.Facts[0].Value != "not automatic" || c.Facts[0].Tone != ToneBad {
		t.Errorf("manual fact %+v", c.Facts[0])
	}

	f = goodUpkeep()
	f.AgentStatus, f.AgentStartup = "", ""
	c, fs, cs = runUpkeep(f)
	none(t, fs, "MNT01")
	if st := statusOf(cs, "MNT01"); st.Status != CheckSkipped || st.Note != "Agent service status is not visible on this platform." {
		t.Errorf("not visible %+v", st)
	}
	if c.Verdict != `"IndexOptimize - USER_DATABASES" runs and no job failed in 7 days.` || c.Facts[0].Value != "could not be read" {
		t.Errorf("not visible card %q %+v", c.Verdict, c.Facts[0])
	}
	f.AgentError = "the login does not have VIEW SERVER STATE."
	if _, _, cs = runUpkeep(f); statusOf(cs, "MNT01").Note != agentNotRead+f.AgentError {
		t.Errorf("agent error %+v", statusOf(cs, "MNT01"))
	}
}

func TestUpkeepExpress(t *testing.T) {
	f := goodUpkeep()
	f.EngineEdition = 4
	f.AgentStatus, f.AgentStartup, f.MaintenanceJob, f.Operators, f.AlertsOn = "", "", "", 0, 0
	f.ShrinkJobs = []ShrinkJob{{Job: "never read", Step: 1, Command: "DBCC SHRINKFILE(1)"}}
	c, fs, cs := runUpkeep(f)
	x := one(t, fs, "MNT01")
	if x.Severity != SeverityInfo || x.When != WhenNone || x.Effort != EffortNone || x.Finding != "SQL Server Express has no SQL Server Agent." ||
		x.Why != "Backups, integrity checks and statistics maintenance do not run on their own." ||
		x.DoThis != "Make sure backups and maintenance are scheduled another way, e.g. Windows Task Scheduler running sqlcmd." {
		t.Errorf("express %+v", x)
	}
	if len(fs) != 1 {
		t.Errorf("express findings %+v", fs)
	}
	for _, id := range []string{"MNT02", "MNT03", "MNT07"} {
		if st := statusOf(cs, id); st.Status != CheckSkipped || st.Note != "Not applicable to Express edition (no SQL Server Agent)." {
			t.Errorf("%s %+v", id, st)
		}
	}
	if statusOf(cs, "MNT01").Status != CheckPassed || statusOf(cs, "MNT04").Status != CheckPassed {
		t.Errorf("express checks %+v", cs)
	}
	if c.Status != StatusGreen || c.Verdict != "SQL Server Express has no SQL Server Agent." {
		t.Errorf("express card %+v", c)
	}
	wantFacts(t, c, "SQL Agent=none (Express)/normal", "Failed jobs, 7 days=not checked (Express)/normal",
		"Maintenance job=not checked (Express)/normal", "Told about errors=not checked (Express)/normal")
}

func TestUpkeepMNT02(t *testing.T) {
	f := goodUpkeep()
	f.FailedJobs = []FailedJob{
		{Name: "DatabaseBackup - USER_DATABASES - FULL", Fails: 3, LastDate: time.Date(2026, 11, 2, 0, 0, 0, 0, time.UTC),
			Step: "Backup", Message: "Executed as user: NT SERVICE\\SQLAgent. The disk is full.", HasStep: true},
		{Name: "Nightly cleanup", Fails: 1, LastDate: time.Date(2026, 10, 30, 0, 0, 0, 0, time.UTC)},
	}
	c, fs, cs := runUpkeep(f)
	got := findingsOf(fs, "MNT02")
	if len(got) != 2 {
		t.Fatalf("MNT02 %+v", fs)
	}
	b, o := got[0], got[1]
	if b.Severity != SeverityHigh || b.Owner != OwnerUs || b.Effort != EffortModerate || b.When != WhenOnline || b.Object != `SQL01\WMS` ||
		b.Finding != `Agent job "DatabaseBackup - USER_DATABASES - FULL" failed 3 time(s) in the last 7 days.` ||
		b.Evidence != `Last failure: 2026-11-02. Step "Backup": Executed as user: NT SERVICE\SQLAgent. The disk is full.` ||
		b.Why != mnt02Why || b.DoThis != mnt02Do || b.ThenCheck != "Re-run after 7 days: MNT02 gone." {
		t.Errorf("backup job %+v", b)
	}
	if o.Severity != SeverityMedium || o.Finding != `Agent job "Nightly cleanup" failed 1 time(s) in the last 7 days.` || o.Evidence != "Last failure: 2026-10-30." {
		t.Errorf("other job %+v", o)
	}
	if c.StatusText != "2 things to do" || c.Verdict != b.Finding || statusOf(cs, "MNT02").Status != CheckFinding {
		t.Errorf("card %+v", c)
	}
	if c.Facts[1].Value != "2" || c.Facts[1].Tone != ToneWorse {
		t.Errorf("fact %+v", c.Facts[1])
	}
	f.FailedJobs = f.FailedJobs[1:]
	if c, _, _ = runUpkeep(f); c.Facts[1].Value != "1" || c.Facts[1].Tone != ToneBad {
		t.Errorf("fact %+v", c.Facts[1])
	}
	// LEFT(..., 4000)
	f.FailedJobs[0].Step, f.FailedJobs[0].Message, f.FailedJobs[0].HasStep = "s", strings.Repeat("x", 5000), true
	if _, fs, _ = runUpkeep(f); len([]rune(one(t, fs, "MNT02").Evidence)) != 4000 {
		t.Error("evidence not cut at 4000")
	}
}

func TestUpkeepMNT03(t *testing.T) {
	f := goodUpkeep()
	f.MaintenanceJob = ""
	c, fs, _ := runUpkeep(f)
	x := one(t, fs, "MNT03")
	if x.Severity != SeverityMedium || x.Effort != EffortModerate || x.Finding != "No enabled index or statistics maintenance job was found." ||
		x.Why != mnt03Why || x.DoThis != mnt03Do || x.Evidence != "" || x.Object != `SQL01\WMS` || x.FixID != FixMaintenanceJob {
		t.Errorf("MNT03 %+v", x)
	}
	if c.Facts[2].Value != "none found" || c.Facts[2].Tone != ToneBad {
		t.Errorf("fact %+v", c.Facts[2])
	}
	f.MaintenancePlans = 1
	if _, fs, _ = runUpkeep(f); len(findingsOf(fs, "MNT03")) != 0 {
		t.Errorf("plan counts: %+v", fs)
	}
}

func TestUpkeepMNT04(t *testing.T) {
	f := goodUpkeep()
	f.StaleStats = []StaleStat{
		stale("dbo", "Big", "PK_Big", 900000, 300000, 900000),
		stale("dbo", "Big", "IX_Big_Date", 900000, 400000, 900000),
		stale("dbo", "Small", "PK_Small", 10000, 2000, 10000),
		stale("dbo", "Mid", "PK_Mid", 50000, 20000, 50000),
		stale("wms", "Tx", "PK_Tx", 70000, 30000, 70000),
		stale("wms", "Log", "PK_Log", 20000, 9000, 20000),
		stale("dbo", "Six", "PK_Six", 12000, 2500, 12000),
	}
	c, fs, cs := runUpkeep(f)
	x := one(t, fs, "MNT04")
	if x.Severity != SeverityMedium || x.Owner != OwnerUs || x.When != WhenOnline || x.Effort != EffortQuickWin || x.Object != "WarehouseLive" ||
		x.Finding != "7 statistics on large tables are out of date (over 20% of rows changed, not updated for 7+ days)." ||
		x.Why != mnt04Why ||
		x.DoThis != "Update them outside peak hours: USE [WarehouseLive]; EXEC sp_updatestats;  Then schedule regular statistics maintenance (MNT03)." ||
		x.Evidence != "Most changed tables: dbo.Big (400000 changes / 900000 rows), wms.Tx (30000 changes / 70000 rows), dbo.Mid (20000 changes / 50000 rows), wms.Log (9000 changes / 20000 rows), dbo.Six (2500 changes / 12000 rows)" ||
		x.FixID != FixUpdateStatistics || x.ThenCheck != "Re-run after the job: MNT04 empty." {
		t.Errorf("MNT04 %+v", x)
	}
	if c.Fixable != 1 || statusOf(cs, "MNT04").Status != CheckFinding {
		t.Errorf("card %+v", c)
	}
	// Not changeable from here: the finding stays, without the fix.
	f.CanChange = false
	if c, fs, _ = runUpkeep(f); one(t, fs, "MNT04").FixID != "" || c.Fixable != 0 {
		t.Errorf("read-only %+v", fs)
	}
	f.StatsError = "VIEW DATABASE STATE permission denied"
	_, fs, cs = runUpkeep(f)
	none(t, fs, "MNT04")
	if st := statusOf(cs, "MNT04"); st.Status != CheckSkipped || st.Note != "Could not read statistics for this database: VIEW DATABASE STATE permission denied" {
		t.Errorf("stats error %+v", st)
	}
}

func TestUpkeepMNT05(t *testing.T) {
	last := time.Date(2026, 11, 2, 22, 5, 30, 0, time.UTC)
	f := goodUpkeep()
	f.TraceCommands = []TraceCommand{
		{Kind: TraceShrink, Database: "WarehouseLive", Count: 4, LastAt: last, Login: `CORP\dba`, App: "SQLAgent - TSQL JobStep", Sample: "DBCC SHRINKFILE (2, 0)"},
		{Kind: TraceCache, Count: 1, LastAt: last, Sample: "DBCC FREEPROCCACHE"},
	}
	f.ShrinkJobs = []ShrinkJob{{Job: "Nightly shrink", Step: 2, Command: "DBCC SHRINKDATABASE(N'WarehouseLive')" + strings.Repeat(" ", 400)}}
	_, fs, cs := runUpkeep(f)
	got := findingsOf(fs, "MNT05")
	if len(got) != 3 {
		t.Fatalf("MNT05 %+v", got)
	}
	byFinding := map[string]Finding{}
	for _, x := range got {
		byFinding[x.Finding] = x
		if x.Severity != SeverityMedium || x.Effort != EffortQuickWin || x.When != WhenOnline || x.Owner != OwnerUs {
			t.Errorf("MNT05 %+v", x)
		}
	}
	s := byFinding["A database or file was shrunk 4 time(s) in the last 7 days."]
	if s.Object != "WarehouseLive" || s.Why != mnt05ShrinkWhy || s.DoThis != mnt05ShrinkDo ||
		s.Evidence != `Last: 2026-11-02 22:05 by CORP\dba from SQLAgent - TSQL JobStep. Example: DBCC SHRINKFILE (2, 0)` {
		t.Errorf("shrink %+v", s)
	}
	cc := byFinding["The plan cache or buffer pool was cleared 1 time(s) in the last 7 days."]
	if cc.Object != `SQL01\WMS` || cc.Why != mnt05CacheWhy || cc.DoThis != mnt05CacheDo ||
		cc.Evidence != "Last: 2026-11-02 22:05 by ? from ?. Example: DBCC FREEPROCCACHE" {
		t.Errorf("cache %+v", cc)
	}
	j := byFinding[`Agent job "Nightly shrink" (step 2) shrinks a database or file.`]
	// Phase 4d: the object is the job, and the finding offers remove-shrink-step.
	if j.Object != "Nightly shrink" || j.FixID != FixRemoveShrinkStep || j.Why != mnt05JobWhy || j.DoThis != mnt05JobDo || len(j.Evidence) != 300 {
		t.Errorf("job %+v", j)
	}
	if statusOf(cs, "MNT05").Status != CheckFinding {
		t.Errorf("MNT05 check %+v", statusOf(cs, "MNT05"))
	}

	// Growth thresholds: 19 nothing, 20 Low, 99 Low, 100 Medium.
	for n, want := range map[int]Severity{19: "", 20: SeverityLow, 99: SeverityLow, 100: SeverityMedium} {
		f := goodUpkeep()
		f.Growth = &TraceGrowth{Count: n, Seconds: 1234, LogGrows: 5, LastAt: last}
		_, fs, _ := runUpkeep(f)
		got := findingsOf(fs, "MNT05")
		if want == "" {
			if len(got) != 0 {
				t.Errorf("%d growths: %+v", n, got)
			}
			continue
		}
		if len(got) != 1 || got[0].Severity != want || got[0].Object != "WarehouseLive" ||
			got[0].Finding != fmtGrowth(n) || got[0].Why != mnt05GrowthWhy || got[0].DoThis != mnt05GrowthDo ||
			got[0].Evidence != "Data file growths: "+nInt(n-5)+", log growths: 5. Last: 2026-11-02 22:05." {
			t.Errorf("%d growths: %+v", n, got)
		}
	}

	// The trace not running, or not readable: skipped with the note; the job part still runs.
	f = goodUpkeep()
	f.TraceRunning = false
	if _, _, cs = runUpkeep(f); statusOf(cs, "MNT05").Status != CheckSkipped || statusOf(cs, "MNT05").Note != "The default trace is not running." {
		t.Errorf("not running %+v", statusOf(cs, "MNT05"))
	}
	f.TraceError = "the login does not have ALTER TRACE."
	f.ShrinkJobs = []ShrinkJob{{Job: "Nightly shrink", Step: 1, Command: "DBCC SHRINKFILE(2)"}}
	_, fs, cs = runUpkeep(f)
	if st := statusOf(cs, "MNT05"); st.Status != CheckFinding || st.Note != "Could not read the default trace (needs ALTER TRACE): the login does not have ALTER TRACE." ||
		len(findingsOf(fs, "MNT05")) != 1 {
		t.Errorf("trace error with a job %+v", st)
	}
}

func fmtGrowth(n int) string {
	return "Files grew " + nInt(n) + " times in the last 7 days (1,234 seconds spent growing)."
}

func nInt(n int) string { return n0int(int64(n)) }

func TestUpkeepMNT06(t *testing.T) {
	last := time.Date(2026, 11, 1, 3, 0, 0, 0, time.UTC)
	f := goodUpkeep()
	f.SevereErrors = []SevereErrors{{Count: 2, LastAt: last, Sample: "Error: 17310"}, {Database: "WarehouseLive", Count: 1, LastAt: last, Sample: "Error: 824"}}
	_, fs, cs := runUpkeep(f)
	got := findingsOf(fs, "MNT06")
	if len(got) != 2 {
		t.Fatalf("MNT06 %+v", got)
	}
	for _, x := range got {
		if x.Severity != SeverityHigh || x.When != WhenNone || x.Effort != EffortModerate || x.Owner != OwnerClientIT ||
			x.Why != mnt06ErrorsWhy || x.DoThis != mnt06ErrorsDo {
			t.Errorf("MNT06 %+v", x)
		}
	}
	objects := got[0].Object + "|" + got[1].Object
	if !strings.Contains(objects, `SQL01\WMS`) || !strings.Contains(objects, "WarehouseLive") {
		t.Errorf("objects %s", objects)
	}
	for _, x := range got {
		if x.Object == `SQL01\WMS` && (x.Finding != "The error log recorded 2 error(s) of severity 20 or higher in the last 7 days." ||
			x.Evidence != "Last: 2026-11-01 03:00. Example: Error: 17310") {
			t.Errorf("instance-wide %+v", x)
		}
	}
	if statusOf(cs, "MNT06").Status != CheckFinding {
		t.Error("MNT06 check")
	}

	// Dumps: within 7 days High, else Medium.
	for days, want := range map[int]Severity{7: SeverityHigh, 8: SeverityMedium} {
		f := goodUpkeep()
		dl := setupNow.AddDate(0, 0, -days)
		f.Dumps, f.DumpLast, f.DumpFile = 3, &dl, `C:\Dumps\SQLDump0003.mdmp`
		_, fs, _ := runUpkeep(f)
		x := one(t, fs, "MNT06")
		if x.Severity != want || x.Object != `SQL01\WMS` || x.Finding != "SQL Server wrote 3 memory dump(s) in the last 30 days." ||
			x.Why != mnt06DumpsWhy || x.DoThis != mnt06DumpsDo ||
			x.Evidence != "Last dump: "+sqlTime(dl)+`. File: C:\Dumps\SQLDump0003.mdmp.` {
			t.Errorf("dumps %d days: %+v", days, x)
		}
	}

	// Dumps unreadable but the trace read: MNT06 still ran. Both unreadable: skipped.
	f = goodUpkeep()
	f.DumpsError = "the login does not have VIEW SERVER STATE."
	if _, _, cs = runUpkeep(f); statusOf(cs, "MNT06").Status != CheckPassed {
		t.Errorf("dumps error only %+v", statusOf(cs, "MNT06"))
	}
	f.TraceError = "the login does not have ALTER TRACE."
	if _, _, cs = runUpkeep(f); statusOf(cs, "MNT06").Status != CheckSkipped ||
		statusOf(cs, "MNT06").Note != "Could not read the default trace (needs ALTER TRACE): the login does not have ALTER TRACE. Could not read the memory dumps (sys.dm_server_memory_dumps): the login does not have VIEW SERVER STATE." {
		t.Errorf("both unreadable %+v", statusOf(cs, "MNT06"))
	}
}

func TestUpkeepMNT07(t *testing.T) {
	f := goodUpkeep()
	f.Operators = 0
	c, fs, _ := runUpkeep(f)
	x := one(t, fs, "MNT07")
	if x.Severity != SeverityLow || x.Owner != OwnerClientIT || x.Effort != EffortQuickWin ||
		x.Finding != "No operator with an email address exists, so no alert or job failure can reach anyone." ||
		x.Why != mnt07Why || x.DoThis != mnt07Do || x.Evidence != "Operators with email: 0. Alerts enabled: 10." ||
		x.ThenCheck != "RAISERROR('Alert test' 18 1) WITH LOG reaches the operator's mailbox." || x.FixID != FixOperatorAlerts {
		t.Errorf("no operator %+v", x)
	}
	if c.Facts[3].Value != "no operator" || c.Facts[3].Tone != ToneBad {
		t.Errorf("fact %+v", c.Facts[3])
	}
	f = goodUpkeep()
	f.MissingAlerts, f.AlertsOn = []string{"severity 19", "20", "error 823"}, 7
	c, fs, _ = runUpkeep(f)
	if x = one(t, fs, "MNT07"); x.Finding != "No alert for: severity 19, 20, error 823." || x.Evidence != "Operators with email: 1. Alerts enabled: 7." {
		t.Errorf("missing %+v", x)
	}
	if c.Facts[3].Value != "3 alerts missing" || c.Facts[3].Tone != ToneBad {
		t.Errorf("fact %+v", c.Facts[3])
	}
}

func TestUpkeepMsdbUnreadable(t *testing.T) {
	f := goodUpkeep()
	msg := "The SELECT permission was denied on the object 'sysjobs', database 'msdb', schema 'dbo'."
	f.JobsError, f.MaintenanceError, f.ShrinkJobsError, f.AlertsError = msg, msg, msg, msg
	f.StaleStats = []StaleStat{stale("dbo", "Big", "PK_Big", 900000, 300000, 900000)}
	c, fs, cs := runUpkeep(f)
	for _, id := range []string{"MNT02", "MNT03", "MNT05", "MNT07"} {
		if st := statusOf(cs, id); st.Status != CheckSkipped || st.Note != "msdb is not readable with this login. "+msg {
			t.Errorf("%s %+v", id, st)
		}
	}
	if statusOf(cs, "MNT01").Status != CheckPassed || statusOf(cs, "MNT06").Status != CheckPassed || statusOf(cs, "MNT04").Status != CheckFinding {
		t.Errorf("the rest run: %+v", cs)
	}
	if c.Status != StatusAmber || c.Verdict != one(t, fs, "MNT04").Finding {
		t.Errorf("card %+v", c)
	}
	wantFacts(t, c, "SQL Agent=running, automatic/normal", "Failed jobs, 7 days=could not be read/normal",
		"Maintenance job=could not be read/normal", "Told about errors=could not be read/normal")
	f.StaleStats = nil
	if c, _, _ = runUpkeep(f); c.Status != StatusGreen || c.Verdict != "SQL Server Agent is running." {
		t.Errorf("green without msdb %+v", c)
	}
}

func TestUpkeepGrey(t *testing.T) {
	f := goodUpkeep()
	vss, trace := "the login does not have VIEW SERVER STATE.", "the login does not have ALTER TRACE."
	f.AgentError, f.DumpsError, f.TraceError = vss, vss, trace
	f.JobsError, f.MaintenanceError, f.ShrinkJobsError, f.AlertsError = "denied", "denied", "denied", "denied"
	f.StaleStats = []StaleStat{stale("dbo", "Big", "PK_Big", 900000, 300000, 900000)}
	c, fs, cs := runUpkeep(f)
	if c.Status != StatusGrey || c.StatusText != "Could not be read" || c.Verdict != "" || c.AsOf != nil ||
		c.Note != "Could not read the instance's upkeep (needs VIEW SERVER STATE, ALTER TRACE and msdb): "+vss {
		t.Errorf("grey %+v", c)
	}
	if len(findingsOf(fs, "MNT04")) != 1 || statusOf(cs, "MNT04").Status != CheckFinding || statusOf(cs, "MNT01").Status != CheckSkipped {
		t.Errorf("grey keeps MNT04: %+v %+v", fs, cs)
	}
	// One instance area readable: not grey.
	f.DumpsError = ""
	if c, _, _ = runUpkeep(f); c.Status == StatusGrey {
		t.Errorf("dumps readable: %+v", c)
	}

	f = goodUpkeep()
	f.Error = "could not read the upkeep facts: timeout"
	c, fs, cs = runUpkeep(f)
	if c.Status != StatusGrey || c.Note != f.Error || len(fs) != 0 || len(cs) != 7 || statusOf(cs, "MNT07").Note != f.Error {
		t.Errorf("error %+v %+v", c, cs)
	}
	f = goodUpkeep()
	f.State = "RESTORING"
	if c, _, _ = runUpkeep(f); c.Status != StatusGrey || c.Note != "WarehouseLive is RESTORING, not online, so its upkeep was not checked." {
		t.Errorf("offline %+v", c)
	}
}

func staleFacts() UpkeepFacts {
	f := goodUpkeep()
	f.StaleStats = []StaleStat{
		stale("dbo", "Big", "PK_Big", 900000, 300000, 900000),
		stale("dbo", "Big", "IX_Big_Date", 900000, 400000, 900000),
		stale("dbo", "Small", "PK_Small", 10000, 2000, 10000),
		stale("wms", "Odd]Name", "PK_Odd", 50000, 20000, 50000),
		stale("wms", "Tx", "PK_Tx", 70000, 30000, 12000), // partition stats say fewer rows: sorted by them
	}
	return f
}

func TestUpdateStatisticsFix(t *testing.T) {
	f := staleFacts()
	p, fix, err := BuildUpkeepFix(FixUpdateStatistics, f)
	if err != nil {
		t.Fatal(err)
	}
	want := Fix{ID: "update-statistics", Title: "Update statistics", Scope: ScopeDatabase, CheckID: "MNT04", Setting: "Statistics",
		Current: "5 statistics on 4 tables out of date", Target: "none out of date"}
	if !reflect.DeepEqual(fix, want) {
		t.Errorf("fix\n got %+v\nwant %+v", fix, want)
	}
	wantStmt := "UPDATE STATISTICS [dbo].[Small];\nUPDATE STATISTICS [wms].[Tx];\nUPDATE STATISTICS [wms].[Odd]]Name];\nUPDATE STATISTICS [dbo].[Big];"
	if p.Statement != wantStmt {
		t.Errorf("statement\n%s", p.Statement)
	}
	if p.Rollback != "No rollback: updated statistics are never worse than stale ones." || p.Database != "WarehouseLive" || p.EngineVersion != "16.0.4255.3" {
		t.Errorf("preview %+v", p)
	}
	if !reflect.DeepEqual(p.Changes, []Drift{{Setting: "Statistics", Current: "5 statistics on 4 tables out of date", Target: "none out of date"}}) {
		t.Errorf("changes %+v", p.Changes)
	}
	wantWarn := []string{
		"Updating statistics reads each table; on large tables this takes minutes and competes with users. Run it outside peak hours.",
		"This does not create a maintenance job (MNT03); it fixes today's statistics once.",
		"Needs ALTER on the tables (db_owner or db_ddladmin).",
	}
	if !reflect.DeepEqual(p.Warnings, wantWarn) {
		t.Errorf("warnings %q", p.Warnings)
	}
	if got := AvailableUpkeepFixes(f, func() []Finding { _, fs, _ := runUpkeep(f); return fs }()); len(got) != 1 || !reflect.DeepEqual(got[0], want) {
		t.Errorf("available %+v", got)
	}
	if f.UpdateStatisticsTables() != 4 || UpdateStatisticsDescription("WarehouseLive", 4) != "Statistics on WarehouseLive: 4 tables updated" {
		t.Error("description")
	}
	if StaleStatsValue(0) != "none out of date" || StaleStatsValue(2) != "2 still out of date" {
		t.Error("read-back values")
	}

	// Nothing stale, or the database cannot be changed: nothing to apply.
	if _, _, err := BuildUpkeepFix(FixUpdateStatistics, goodUpkeep()); !errors.Is(err, ErrFixNotApplicable) {
		t.Errorf("nothing stale: %v", err)
	}
	f.CanChange = false
	if _, _, err := BuildUpkeepFix(FixUpdateStatistics, f); !errors.Is(err, ErrFixNotApplicable) {
		t.Errorf("read-only: %v", err)
	}
	if got := AvailableUpkeepFixes(goodUpkeep(), []Finding{}); got == nil || len(got) != 0 {
		t.Errorf("none available %+v", got)
	}
	// Another card's fix, or none.
	for _, id := range []string{"auto-close", "nothing"} {
		if _, _, err := BuildUpkeepFix(id, staleFacts()); !errors.Is(err, ErrUnknownFix) {
			t.Errorf("%s: %v", id, err)
		}
	}
	// The Setup card never builds it.
	if _, _, err := BuildFix(FixUpdateStatistics, goodFacts(), "", nil); !errors.Is(err, ErrFixNotApplicable) {
		t.Errorf("setup BuildFix: %v", err)
	}
	if d, ok := FindFixDef(FixUpdateStatistics); !ok || d.Card() != CardUpkeep {
		t.Errorf("registry %+v", d)
	}
	if d, _ := FindFixDef("auto-close"); d.Card() != CardSetup {
		t.Errorf("auto-close card %s", d.Card())
	}
}

func TestUpkeepDetailJSON(t *testing.T) {
	f := staleFacts()
	c, fs, cs := runUpkeep(f)
	b, _ := json.Marshal(CardDetail{Card: c, Findings: fs, Checks: cs, Fixes: AvailableUpkeepFixes(f, fs)})
	s := string(b)
	if !strings.Contains(s, `"fixId":"update-statistics"`) || strings.Count(s, `"fixId"`) != 1 ||
		!strings.Contains(s, `"fixes":[{"id":"update-statistics","title":"Update statistics","scope":"database","checkId":"MNT04","setting":"Statistics","current":"5 statistics on 4 tables out of date","target":"none out of date","needsRestart":false}]`) ||
		strings.Contains(s, "checkdbRoom") {
		t.Errorf("upkeep detail JSON: %s", s)
	}
	b, _ = json.Marshal(CardDetail{Card: Card{ID: CardUpkeep}, Findings: []Finding{}, Checks: []CheckStatus{}})
	if !strings.Contains(string(b), `"fixes":[]`) {
		t.Errorf("no fixes: %s", b)
	}
}

func TestBuildCardsWithUpkeep(t *testing.T) {
	f := goodUpkeep()
	cards := BuildCards(nil, nil, nil, nil, &f, nil)
	if cards[3].ID != CardUpkeep || cards[3].Status != StatusGreen || len(cards[3].Facts) != 4 {
		t.Errorf("upkeep card %+v", cards[3])
	}
	if cards := BuildCards(nil, nil, nil, nil, nil, nil); cards[3].Status != StatusGrey || cards[3].StatusText != "Not checked yet" {
		t.Errorf("no facts %+v", cards[3])
	}
}

func TestUpkeepCatalogue(t *testing.T) {
	checks := ChecksFor(CardUpkeep)
	if len(checks) != 7 {
		t.Fatalf("upkeep checks: %d", len(checks))
	}
	owners := map[string]string{"MNT01": OwnerClientIT, "MNT02": OwnerUs, "MNT03": OwnerUs, "MNT04": OwnerUs, "MNT05": OwnerUs,
		"MNT06": OwnerClientIT, "MNT07": OwnerClientIT}
	for _, c := range checks {
		if c.Title == "" || c.Question == "" || c.ThenCheck == "" || c.Owner != owners[c.ID] {
			t.Errorf("check %+v", c)
		}
	}
}

// TestUpkeepMNT05Phase3f covers docs/phase3f-contract.md items 7 and 16: a single-plan eviction is
// Info, AUTO_SHRINK events are Medium with the auto-shrink fix while the option is on, and the
// slowest growth of a second or more is evidence.
func TestUpkeepMNT05Phase3f(t *testing.T) {
	last := time.Date(2026, 11, 2, 22, 5, 30, 0, time.UTC)
	f := goodUpkeep()
	f.AutoShrinkOn = true
	f.TraceCommands = []TraceCommand{
		{Kind: TraceEvict, Database: "WarehouseLive", Count: 3, LastAt: last, Login: `CORP\dba`, App: "SSMS", Sample: "DBCC FREEPROCCACHE (0x0600050012345678)"},
		{Kind: TraceAutoShrink, Database: "WarehouseLive", Count: 6, LastAt: last},
		{Kind: TraceAutoShrink, Database: "Archive", Count: 2, LastAt: last},
	}
	c, fs, cs := runUpkeep(f)
	got := findingsOf(fs, "MNT05")
	if len(got) != 3 {
		t.Fatalf("MNT05 %+v", got)
	}
	by := map[string]Finding{}
	for _, x := range got {
		by[x.Finding] = x
	}
	ev := by["One plan was evicted from the cache 3 time(s) in the last 7 days."]
	if ev.Severity != SeverityInfo || ev.When != WhenNone || ev.Effort != EffortNone || ev.Why != mnt05EvictWhy ||
		ev.DoThis != "No action." || ev.FixID != "" || ev.Object != "WarehouseLive" ||
		ev.Evidence != `Last: 2026-11-02 22:05 by CORP\dba from SSMS. Example: DBCC FREEPROCCACHE (0x0600050012345678)` {
		t.Errorf("evict %+v", ev)
	}
	as := by["AUTO_SHRINK ran 6 times in the last 7 days on WarehouseLive."]
	if as.Severity != SeverityMedium || as.When != WhenOnline || as.Why != dbc03Why || as.FixID != FixAutoShrink ||
		as.Evidence != "Last: 2026-11-02 22:05 by ? from ?." {
		t.Errorf("auto-shrink here %+v", as)
	}
	other := by["AUTO_SHRINK ran 2 times in the last 7 days on Archive."]
	if other.Severity != SeverityMedium || other.FixID != "" || other.Object != "Archive" {
		t.Errorf("auto-shrink elsewhere %+v", other)
	}
	if statusOf(cs, "MNT05").Status != CheckFinding || c.Fixable != 1 {
		t.Errorf("check %+v fixable %d", statusOf(cs, "MNT05"), c.Fixable)
	}
	fixes := AvailableUpkeepFixes(f, fs)
	if len(fixes) != 1 || fixes[0].ID != FixAutoShrink || fixes[0].CheckID != "DBC03" || fixes[0].Current != "ON" || fixes[0].Target != "OFF" {
		t.Errorf("fixes %+v", fixes)
	}
	// Already switched off, or not changeable from here: no fix.
	f.AutoShrinkOn = false
	if _, fs, _ = runUpkeep(f); len(AvailableUpkeepFixes(f, fs)) != 0 {
		t.Errorf("auto-shrink off: %+v", AvailableUpkeepFixes(f, fs))
	}
	f.AutoShrinkOn, f.CanChange = true, false
	_, fs, _ = runUpkeep(f)
	for _, x := range findingsOf(fs, "MNT05") {
		if x.FixID != "" {
			t.Errorf("cannot change: %+v", x)
		}
	}

	// An eviction alone is information: the check passes and the card stays green.
	f = goodUpkeep()
	f.TraceCommands = []TraceCommand{{Kind: TraceEvict, Count: 1, LastAt: last, Sample: "DBCC FREEPROCCACHE(0x06)"}}
	c, fs, cs = runUpkeep(f)
	if c.Status != StatusGreen || statusOf(cs, "MNT05").Status != CheckPassed || len(findingsOf(fs, "MNT05")) != 1 {
		t.Errorf("evict alone %+v %+v", c, statusOf(cs, "MNT05"))
	}

	// The slowest growth: a second or more is evidence, less is not.
	for ms, want := range map[int64]string{999: "", 1000: " The slowest growth took 1.0 s (WarehouseLive_log, 2026-11-02 21:00).",
		4230: " The slowest growth took 4.2 s (WarehouseLive_log, 2026-11-02 21:00)."} {
		f := goodUpkeep()
		f.Growth = &TraceGrowth{Count: 25, Seconds: 30, LogGrows: 5, LastAt: last,
			SlowestMs: ms, SlowestFile: "WarehouseLive_log", SlowestAt: time.Date(2026, 11, 2, 21, 0, 0, 0, time.UTC)}
		_, fs, _ := runUpkeep(f)
		x := one(t, fs, "MNT05")
		if x.Evidence != "Data file growths: 20, log growths: 5. Last: 2026-11-02 22:05."+want {
			t.Errorf("%d ms: %q", ms, x.Evidence)
		}
	}
}

// TestUpkeepMNT07Silent covers docs/phase3f-contract.md item 8: alerts that notify no one.
func TestUpkeepMNT07Silent(t *testing.T) {
	f := goodUpkeep()
	f.SilentAlerts = []string{"Error 823", "Severity 024"}
	c, fs, cs := runUpkeep(f)
	x := one(t, fs, "MNT07")
	if x.Severity != SeverityLow || x.When != WhenOnline || x.Effort != EffortQuickWin || x.Owner != OwnerClientIT ||
		x.Finding != "2 alert(s) exist but tell no one: Error 823, Severity 024." || x.Why != mnt07SilentWhy ||
		x.DoThis != mnt07SilentDo || x.Evidence != "Operators with email: 1. Alerts enabled: 10." || x.Object != `SQL01\WMS` ||
		x.FixID != FixOperatorAlerts {
		t.Errorf("silent %+v", x)
	}
	if c.Facts[3].Value != "2 alerts tell no one" || c.Facts[3].Tone != ToneBad || statusOf(cs, "MNT07").Status != CheckFinding {
		t.Errorf("fact %+v check %+v", c.Facts[3], statusOf(cs, "MNT07"))
	}
	// Silent and missing alerts: two findings, the fact names the missing ones.
	f.MissingAlerts = []string{"25"}
	c, fs, _ = runUpkeep(f)
	if len(findingsOf(fs, "MNT07")) != 2 || c.Facts[3].Value != "1 alerts missing" {
		t.Errorf("silent and missing %+v %+v", findingsOf(fs, "MNT07"), c.Facts[3])
	}
	// No operator at all: only the no-operator finding.
	f = goodUpkeep()
	f.Operators, f.SilentAlerts = 0, []string{"Error 823"}
	_, fs, _ = runUpkeep(f)
	if x := one(t, fs, "MNT07"); x.Finding != mnt07NoOperator {
		t.Errorf("no operator %+v", x)
	}
}

func TestUpkeepMNT03Wording(t *testing.T) {
	if !strings.HasPrefix(mnt03Do, "Nightly statistics updates (Ola Hallengren's IndexOptimize with @StatisticsModificationLevel = 5 and no index work) matter most; rebuild or reorganise indexes weekly or not at all.") {
		t.Errorf("MNT03 do this %q", mnt03Do)
	}
}
