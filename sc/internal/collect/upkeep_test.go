package collect

import (
	"context"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"sqlmonitor/internal/domain"
)

func TestUpkeepQueries(t *testing.T) {
	// MNT04 / STA01 thresholds, as the scan writes them.
	for _, want := range []string{"sp.rows >= 10000", "sp.modification_counter >= sp.rows * 0.2", "sp.last_updated < DATEADD(DAY, -7, GETDATE())",
		"o.type = 'U' AND o.is_ms_shipped = 0"} {
		if !strings.Contains(staleStatsQuery, want) || !strings.Contains(staleStatsCountQuery, want) {
			t.Errorf("stale statistics rule lacks %q", want)
		}
	}
	if !strings.Contains(staleStatsQuery, "p.index_id IN (0, 1)") {
		t.Error("table rows from the heap or clustered index")
	}
	// The AUTO_CLOSE open, then the Agent and each msdb, trace and dump area in its own TRY.
	// Phase 5b: MNT08's history read is the ninth; phase 5q: Express's maintenance procedure read the tenth.
	if n := strings.Count(upkeepInstanceQuery, "BEGIN TRY"); n != 10 {
		t.Errorf("%d TRY blocks", n)
	}
	// Phase 3f: plan-handle evictions apart from cache clears, auto-shrink events, the slowest
	// growth, and the alerts that notify no one.
	for _, want := range []string{"EventClass IN (22, 92, 93, 94, 95, 116)", "N'%FREEPROCCACHE%(%0x%'", "N'%FREEPROCCACHE%(%@%'",
		"N'evict'", "N'autoshrink'", "EventClass IN (94, 95)", "ORDER BY Duration DESC", "msdb.dbo.sysnotifications",
		"(n.notification_method & 1) = 1", "is_auto_shrink_on", "SELECT name FROM @silent",
		"MIN(backup_finish_date)", "FROM msdb.dbo.sysjobhistory", "sp[_]delete[_]backuphistory", "sp[_]purge[_]jobhistory", "@historyError"} {
		if !strings.Contains(upkeepInstanceQuery, want) {
			t.Errorf("upkeep query lacks %q", want)
		}
	}
	if domain.TraceEvict != "evict" || domain.TraceAutoShrink != "autoshrink" {
		t.Error("the trace kinds the query writes must match the domain's")
	}
	if got := runDate(20261102); !got.Equal(time.Date(2026, 11, 2, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("runDate %v", got)
	}
}

// TestUpkeepFactsLive reads the upkeep facts of one database on a real instance, read-only, when
// SQLMONITOR_TEST_HOST (Windows authentication) and SQLMONITOR_TEST_DB name one.
func TestUpkeepFactsLive(t *testing.T) {
	host, name := os.Getenv("SQLMONITOR_TEST_HOST"), os.Getenv("SQLMONITOR_TEST_DB")
	if host == "" || name == "" {
		t.Skip("set SQLMONITOR_TEST_HOST and SQLMONITOR_TEST_DB to read a live instance")
	}
	ctx := context.Background()
	db, err := Open(ctx, domain.ServerInput{Host: host, Auth: domain.AuthWindows})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	f, err := UpkeepFacts(ctx, db, name)
	if err != nil {
		t.Fatal(err)
	}
	if f.State == "" || f.ServerName == "" || f.ServerNow.IsZero() || f.FailedJobs == nil || f.MissingAlerts == nil {
		t.Errorf("facts: %+v", f)
	}
	t.Logf("%s: agent %q/%q %q, failed jobs %d %q, maint %q plans %d %q, shrink jobs %d %q, alerts missing %v ops %d on %d %q, trace %v %q cmds %d growth %+v errors %d, dumps %d %v %q %q, stale %d %q",
		name, f.AgentStatus, f.AgentStartup, f.AgentError, len(f.FailedJobs), f.JobsError, f.MaintenanceJob, f.MaintenancePlans, f.MaintenanceError,
		len(f.ShrinkJobs), f.ShrinkJobsError, f.MissingAlerts, f.Operators, f.AlertsOn, f.AlertsError, f.TraceRunning, f.TraceError,
		len(f.TraceCommands), f.Growth, len(f.SevereErrors), f.Dumps, f.DumpLast, f.DumpFile, f.DumpsError, len(f.StaleStats), f.StatsError)
	t.Logf("trace oldest %v newest %v, %d DDL events, login %q", f.TraceOldestUTC, f.TraceNewestUTC, len(f.SchemaChanges), f.LoginName)
	for i, x := range f.SchemaChanges {
		if x.Database == name && i >= len(f.SchemaChanges)-40 {
			t.Logf("  %+v", x)
		}
	}
	n, err := ReadStaleStatsCount(ctx, db, name)
	if err != nil || n != len(f.StaleStats) {
		t.Errorf("stale count %d %v, facts %d", n, err, len(f.StaleStats))
	}
	if _, err := UpkeepFacts(ctx, db, "NoSuchDatabase_x9"); err == nil {
		t.Error("an unknown database should be an error")
	}
}

// The change log's read (docs/phase5f-contract.md): the DDL rows are one more result set of the Upkeep
// read, from the same @trace table (no second fn_trace_gettable), tagged and read-only; the
// enable-default-trace read too.
func TestSchemaChangeRead(t *testing.T) {
	q := Tag(upkeepInstanceQuery)
	if !strings.HasPrefix(q, KakapoTag+" "+safeSession) {
		t.Errorf("not tagged with the safe session first: %.80s", q)
	}
	// The trace part and the result sets (the msdb reads before it match job commands, ALTER INDEX
	// among them, as text inside sp_executesql).
	readOnly(t, "upkeep trace", strings.ReplaceAll(q[strings.Index(q, "/* MNT05 / MNT06 Events"):], "ALTER TRACE", "the ALTER_TRACE permission"))
	if n := strings.Count(upkeepInstanceQuery, "fn_trace_gettable"); n != 1 {
		t.Errorf("the trace is read %d times", n)
	}
	if n := strings.Count(upkeepInstanceQuery, "BEGIN TRY"); n != 10 {
		t.Errorf("%d TRY blocks", n)
	}
	for _, want := range []string{
		"x.EventClass IN (46, 47, 164) AND x.EventSubClass = 1 AND x.DatabaseID > 4",
		"ISNULL(x.ApplicationName, N'') NOT IN (N'kakapo', N'SQLMonitor')",
		"NOT (x.EventClass = 164 AND x.ObjectType = 22601)",
		"(x.ObjectType <> 16964 OR x.EventClass = 164)",
		"MIN(t.StartTime) OVER () AS Oldest, MAX(t.StartTime) OVER () AS Newest",
		"FROM @trace AS t\n", "WHERE t.EventClass IN (46, 47, 164)",
		"DATEADD(MINUTE, @toUtc, t.StartTime)", "@toUtc int = -DATEPART(TZOFFSET, SYSDATETIMEOFFSET())",
		"OBJECT_SCHEMA_NAME(t.ObjectID, t.DatabaseID)", "SUSER_SNAME()",
		// MNT05 keeps its last 7 days.
		"x.StartTime >= DATEADD(DAY, -7, GETDATE()) AND x.EventClass IN (22, 92, 93, 94, 95, 116)",
	} {
		if !strings.Contains(upkeepInstanceQuery, want) {
			t.Errorf("upkeep query lacks %q", want)
		}
	}
	for _, c := range domain.SchemaObjectTypeCodes() {
		if !strings.Contains(schemaObjectTypeList, strconv.Itoa(c)) {
			t.Errorf("object type %d not read", c)
		}
	}
	if strings.Contains(schemaObjectTypeList, strconv.Itoa(domain.ObjTypeStatistics)) {
		t.Error("statistics are left out")
	}
	if cs := ConnString(domain.ServerInput{Host: "h", Auth: domain.AuthWindows}); !strings.Contains(cs, "app name="+domain.BlockingAppName+";") ||
		!strings.Contains(upkeepInstanceQuery, "N'"+domain.BlockingAppName+"'") {
		t.Errorf("kakapo's own connections are left out by their app name: %s", cs)
	}
	suReadOnly(t, "default trace", Tag(defaultTraceQuery))
	if !strings.Contains(defaultTraceQuery, "name = N'default trace enabled'") || !strings.Contains(defaultTraceQuery, "is_default = 1 AND status = 1") {
		t.Error("default trace read")
	}
}
