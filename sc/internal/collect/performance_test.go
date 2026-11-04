package collect

import (
	"context"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"sqlmonitor/internal/domain"
)

// A file-target event as fn_xe_file_target_read_file returns it, trimmed: two procedures deadlock
// on a key lock and a page lock in Laylax.
const deadlockEventXML = `<event name="xml_deadlock_report" package="sqlserver" timestamp="2026-11-02T08:15:03.616Z">
<data name="xml_report"><type name="xml" package="package0"></type><value><deadlock>
 <victim-list><victimProcess id="process2"/></victim-list>
 <process-list>
  <process id="process1" taskpriority="0" lockMode="U" currentdb="7" currentdbname="Laylax" loginname="appsvc" hostname="APP01" clientapp="AppScheduler">
   <executionStack>
    <frame procname="Laylax.dbo.IntegrationPost" line="12" sqlhandle="0x01">UPDATE dbo.Stock SET Qty = Qty - 1 WHERE ID = @id</frame>
   </executionStack>
   <inputbuf>Proc [Database Id = 7 Object Id = 1]</inputbuf>
  </process>
  <process id="process2" taskpriority="0" lockMode="X" currentdb="7" currentdbname="Laylax" loginname="scanner" hostname="HH12" clientapp="Scanner">
   <executionStack>
    <frame procname="adhoc" line="1" sqlhandle="0x02">UPDATE dbo.Pick SET Done = 1</frame>
    <frame procname="Laylax.dbo.USP_PICKING" line="40" sqlhandle="0x03">EXEC sp_executesql @s</frame>
   </executionStack>
   <inputbuf>EXEC dbo.USP_PICKING 5</inputbuf>
  </process>
 </process-list>
 <resource-list>
  <keylock hobtid="72057594043236352" dbid="7" objectname="Laylax.dbo.Stock" indexname="PK_Stock" id="lock1" mode="X" associatedObjectId="72057594043236352">
   <owner-list><owner id="process2" mode="X"/></owner-list>
   <waiter-list><waiter id="process1" mode="U" requestType="wait"/></waiter-list>
  </keylock>
  <pagelock fileid="1" pageid="300" dbid="7" subresource="FULL" objectname="Laylax.dbo.Pick" id="lock2" mode="IX" associatedObjectId="72057594043301888">
   <owner-list><owner id="process1" mode="IX"/></owner-list>
   <waiter-list><waiter id="process2" mode="S" requestType="wait"/></waiter-list>
  </pagelock>
 </resource-list>
</deadlock></value></data></event>`

func TestParseDeadlockEvent(t *testing.T) {
	e, err := ParseDeadlockEvent(deadlockEventXML)
	if err != nil {
		t.Fatal(err)
	}
	if !e.Time.Equal(time.Date(2026, 11, 2, 8, 15, 3, 616000000, time.UTC)) || e.Database != "Laylax" {
		t.Errorf("event %+v", e)
	}
	v, w := e.Victim, e.Winner
	if v.ID != "process2" || v.Procedure != "dbo.USP_PICKING" || v.Statement != "UPDATE dbo.Pick SET Done = 1" || v.LockMode != "X" ||
		v.Database != "Laylax" || v.Login != "scanner" || v.Host != "HH12" {
		t.Errorf("victim %+v", v)
	}
	if w.ID != "process1" || w.Procedure != "dbo.IntegrationPost" || !strings.HasPrefix(w.Statement, "UPDATE dbo.Stock") {
		t.Errorf("winner %+v", w)
	}
	if strings.Join(e.Objects, ",") != "dbo.Pick,dbo.Stock" {
		t.Errorf("objects %q", e.Objects)
	}
	if strings.Join(e.LockModes, "; ") != "keylock dbo.Stock: held X, wanted U; pagelock dbo.Pick: held IX, wanted S" {
		t.Errorf("locks %q", e.LockModes)
	}

	// Ad hoc on both sides, no object names: the statement and the hobt.
	adhoc := strings.NewReplacer(`procname="Laylax.dbo.IntegrationPost"`, `procname="adhoc"`,
		`<frame procname="Laylax.dbo.USP_PICKING" line="40" sqlhandle="0x03">EXEC sp_executesql @s</frame>`, ``,
		`objectname="Laylax.dbo.Stock" `, ``).Replace(deadlockEventXML)
	e, err = ParseDeadlockEvent(adhoc)
	if err != nil {
		t.Fatal(err)
	}
	if e.Victim.Procedure != "" || e.Victim.Label() != "ad hoc: UPDATE dbo.Pick SET Done = 1" || e.Winner.Procedure != "" ||
		strings.Join(e.Objects, ",") != "dbo.Pick,hobt 72057594043236352" || e.Database != "Laylax" {
		t.Errorf("ad hoc %+v", e)
	}

	// A ring-buffer event is the same element; something that is not a deadlock is refused.
	if _, err := ParseDeadlockEvent(`<event name="xml_deadlock_report" timestamp="2026-11-02T08:15:03Z"><data name="xml_report"><value/></data></event>`); err == nil {
		t.Error("an event without a graph")
	}
	if _, err := ParseDeadlockEvent("not xml"); err == nil {
		t.Error("not XML")
	}
}

func TestSplitObject(t *testing.T) {
	for in, want := range map[string][2]string{
		"Laylax.dbo.Stock": {"Laylax", "dbo.Stock"}, "dbo.Stock": {"", "dbo.Stock"}, "": {"", ""},
	} {
		if db, o := splitObject(in); db != want[0] || o != want[1] {
			t.Errorf("%q: %q %q", in, db, o)
		}
	}
}

func TestBenignValues(t *testing.T) {
	v := benignValues()
	if !strings.HasPrefix(v, "(N'BROKER_EVENTHANDLER'), ") || !strings.HasSuffix(v, "(N'XE_LIVE_TARGET_TVF')") ||
		strings.Count(v, "(N'") != len(domain.BenignWaits) {
		t.Errorf("benign values: %s", v)
	}
	if !strings.Contains(performanceInstanceQuery, "INSERT @benign (wait_type) VALUES (N'BROKER_EVENTHANDLER')") {
		t.Error("the instance query carries the benign list")
	}
}

// likeLiterals are the patterns of LIKE comparisons, which are text to match, not statements.
var likeLiterals = regexp.MustCompile(`LIKE N'[^']*'`)

// The reads of this card are SELECTs only: no statement that writes, outside temporary tables
// and table variables. The noise patterns QRY01 filters on ('ALTER INDEX%', 'UPDATE STATISTICS%')
// are LIKE patterns, not statements, and are left out of the scan.
func TestPerformanceReadsOnlySelect(t *testing.T) {
	for name, q := range map[string]string{"instance": performanceInstanceQuery, "query store": queryStoreTopQuery,
		"plan cache": planCacheTopQuery, "deadlocks": deadlockQuery} {
		upper := strings.ToUpper(likeLiterals.ReplaceAllString(q, "LIKE N''"))
		for _, verb := range []string{"ALTER ", "DROP ", "TRUNCATE ", "DBCC ", "CREATE INDEX", "SP_CONFIGURE", "KILL ", "MERGE "} {
			if strings.Contains(upper, verb) {
				t.Errorf("%s query contains %q", name, verb)
			}
		}
		for _, line := range strings.Split(upper, "\n") {
			for _, verb := range []string{"INSERT ", "UPDATE ", "DELETE "} {
				if i := strings.Index(line, verb); i >= 0 && !strings.Contains(line[i:], "@") && !strings.Contains(line[i:], "#") &&
					!strings.Contains(line, "UPDATE T SET") { // t is #top's alias
					t.Errorf("%s query writes: %s", name, line)
				}
			}
		}
	}
}

// Every batch starts with the kakapo tag, once (docs/phase3f-contract.md, item 2).
func TestTag(t *testing.T) {
	if got := Tag("SELECT 1;"); got != "/* kakapo */ SELECT 1;" {
		t.Errorf("tag %q", got)
	}
	if got := Tag(Tag("SELECT 1;")); got != "/* kakapo */ SELECT 1;" {
		t.Errorf("tagged twice %q", got)
	}
	for name, q := range map[string]string{"instance": performanceInstanceQuery, "query store": queryStoreTopQuery,
		"plan cache": planCacheTopQuery, "deadlocks": deadlockQuery} {
		if !strings.HasPrefix(q, KakapoTag+" ") {
			t.Errorf("%s query does not start with the tag: %.60s", name, q)
		}
	}
}

// QRY01 leaves out kakapo's own statements and the noise patterns, in both sources.
func TestNoiseFilter(t *testing.T) {
	f := noiseFilter("x.stmt")
	if !strings.HasPrefix(f, "ISNULL(x.stmt, N'') NOT LIKE N'%/* kakapo */%'") {
		t.Errorf("filter %s", f)
	}
	for _, p := range []string{"%FROM sys.%", "ALTER INDEX%", "UPDATE STATISTICS%", "%SELECT StatMan%", "DBCC%", "(@[_]msparam%"} {
		if !strings.Contains(f, "LTRIM(ISNULL(x.stmt, N'')) NOT LIKE N'"+p+"'") {
			t.Errorf("filter misses %s: %s", p, f)
		}
	}
	if !strings.Contains(queryStoreTopQuery, noiseFilter("qt.query_sql_text")) {
		t.Error("the Query Store read does not filter the noise")
	}
	if !strings.Contains(planCacheTopQuery, noiseFilter("x.stmt")) || !strings.Contains(planCacheTopQuery, "ISNULL(st.text, N'') NOT LIKE N'%/* kakapo */%'") {
		t.Error("the plan cache read does not filter the noise and the tagged batches")
	}
}

// Health events as fn_xe_file_target_read_file returns them, trimmed.
const healthSystemXML = `<event name="sp_server_diagnostics_component_result" package="sqlserver" timestamp="2026-11-02T03:10:00.123Z">
<data name="component"><type name="component_type" package="sqlserver"></type><value>1</value><text>SYSTEM</text></data>
<data name="state"><type name="state" package="sqlserver"></type><value>2</value><text>WARNING</text></data>
<data name="data"><type name="xml" package="package0"></type><value><system spinlockBackoffs="0" sickSpinlockType="none" sickSpinlockTypeAfterAv="none"
 latchWarnings="0" isAccessViolationOccurred="0" writeAccessViolationCount="0" totalDumpRequests="1" intervalDumpRequests="1"
 nonYieldingTasksReported="1" pageFaults="0" systemCpuUtilization="40" sqlCpuUtilization="35" BadPagesDetected="0" BadPagesFixed="0"
 LastBadPageAddress="0x0"/></value></data>
</event>`

const healthIOXML = `<event name="sp_server_diagnostics_component_result" package="sqlserver" timestamp="2026-11-02T04:15:00Z">
<data name="component"><value>4</value><text>IO_SUBSYSTEM</text></data>
<data name="state"><value>2</value><text>WARNING</text></data>
<data name="data"><value><ioSubsystem ioLatchTimeouts="0" intervalLongIos="3" totalLongIos="7"><longestPendingRequests>
 <pendingRequest duration="16200" filePath="D:\Data\Laylax.mdf" offset="0" handle="0x1"/>
 <pendingRequest duration="15100" filePath="E:\Log\Laylax_log.ldf" offset="0" handle="0x2"/>
</longestPendingRequests></ioSubsystem></value></data>
</event>`

func TestParseHealthEvent(t *testing.T) {
	e, err := ParseHealthEvent(healthSystemXML)
	if err != nil {
		t.Fatal(err)
	}
	if !e.At.Equal(time.Date(2026, 11, 2, 3, 10, 0, 123000000, time.UTC)) || e.Component != "SYSTEM" || e.State != "warning" ||
		e.Detail != "1 non-yielding task (a worker that would not give up its CPU); 1 memory dump since startup (1 in this interval)." {
		t.Errorf("system %+v", e)
	}
	e, err = ParseHealthEvent(healthIOXML)
	if err != nil {
		t.Fatal(err)
	}
	if e.Component != "IO_SUBSYSTEM" || e.State != "warning" ||
		e.Detail != `7 long I/Os (over 15 s) since startup; the longest pending request has waited 16.2 s on D:\Data\Laylax.mdf.` {
		t.Errorf("io %+v", e)
	}
	if _, err := ParseHealthEvent(`<event timestamp="2026-11-02T04:15:00Z"></event>`); err == nil {
		t.Error("an event without a component")
	}
	if _, err := ParseHealthEvent("not xml"); err == nil {
		t.Error("not XML")
	}
}

// TestPerformanceFactsLive reads the performance facts of one database on a real instance,
// read-only, when SQLMONITOR_TEST_HOST (Windows authentication) and SQLMONITOR_TEST_DB name one.
func TestPerformanceFactsLive(t *testing.T) {
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
	f, err := PerformanceFacts(ctx, db, name)
	if err != nil {
		t.Fatal(err)
	}
	if f.State == "" || f.ServerNow.IsZero() || f.StartedAt.IsZero() || f.WaitsError != "" || len(f.Waits) == 0 ||
		f.Schedulers == nil || f.Grants == nil || f.Batches == 0 {
		t.Errorf("facts: %+v", f)
	}
	for _, w := range f.Waits {
		if domain.IsBenignWait(w.Type) {
			t.Errorf("benign wait read: %s", w.Type)
		}
	}
	t.Logf("uptime %d h, %d waits, top %+v", f.UptimeHours, len(f.Waits), f.Waits[0])
	t.Logf("counters: deadlocks %d, batches %d, compiles %d, recompiles %d %q", f.Deadlocks, f.Batches, f.Compiles, f.Recompiles, f.CountersError)
	t.Logf("schedulers %+v %q; grants %+v %q", *f.Schedulers, f.SchedulersError, *f.Grants, f.GrantsError)
	t.Logf("blockers %+v; open %+v; idle %+v %q", f.Blockers, f.OpenTransactions, f.IdleTransactions, f.TransactionsError)
	t.Logf("loads %+v %q; missing %+v %q", f.Loads, f.LoadError, f.MissingIndexes, f.MissingIndexError)
	t.Logf("queries from %s (QS %s %q), total %d, error %q", f.QuerySource, f.QueryStoreState, f.QueryStoreError, f.TotalCPU, f.QueriesError)
	for _, q := range f.TopQueries {
		t.Logf("  %s %s cpu %d execs %d wait %q plan %d chars big %v: %.80s", q.QueryHash, q.ObjectName, q.CPU, q.Execs, q.TopWait, len(q.Plan), q.PlanTooBig, q.Text)
	}

	in, err := ReadDeadlocks(ctx, db, domain.DeadlockDays, domain.XEFiles{})
	events, health, source := in.Events, in.Health, in.Source
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("deadlocks: %d from %s; health events %d", len(events), source, len(health))
	for _, h := range health {
		t.Logf("  %s %s %s: %s", h.At, h.Component, h.State, h.Detail)
	}
	for _, e := range events {
		t.Logf("  %s %s: %s / %s on %v", e.Time, e.Database, e.Victim.Label(), e.Winner.Label(), e.Objects)
	}
}
