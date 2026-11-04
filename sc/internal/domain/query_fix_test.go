package domain

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func planForceFacts() PlanForceFacts {
	now := time.Date(2026, 11, 3, 10, 0, 0, 0, time.UTC)
	return PlanForceFacts{
		Database: "GraniteLive", ProductVersion: "16.0.4255.2", QueryStoreState: "READ_WRITE", PlanID: 12, Found: true, QueryID: 345,
		QueryHash: "0x9F3A00112233AABB",
		Plans: []QueryStorePlanFact{
			{PlanID: 12, QueryID: 345, Runs: 10000, Duration: 10000 * 120000, LastRun: qPtime(now.Add(-26 * time.Hour))},
			{PlanID: 15, QueryID: 345, Runs: 1240, Duration: 1240 * 840000, LastRun: qPtime(now.Add(-15 * time.Minute))},
		},
	}
}

func refusal(t *testing.T, err error, invalid bool, contains string) {
	t.Helper()
	var qe *QueryFixError
	if !errors.As(err, &qe) || qe.Invalid != invalid || !strings.Contains(qe.Reason, contains) {
		t.Errorf("error %v, want refusal (invalid %v) with %q", err, invalid, contains)
	}
}

func TestForcePlanPreview(t *testing.T) {
	p, fix, err := BuildPlanForceFix(FixForcePlan, planForceFacts())
	if err != nil {
		t.Fatal(err)
	}
	if p.Statement != "EXEC [GraniteLive].sys.sp_query_store_force_plan @query_id = 345, @plan_id = 12;" ||
		p.Rollback != "EXEC [GraniteLive].sys.sp_query_store_unforce_plan @query_id = 345, @plan_id = 12;" {
		t.Errorf("statements %q / %q", p.Statement, p.Rollback)
	}
	if len(p.Changes) != 1 || p.Changes[0].Setting != "Plan for query 345" || p.Changes[0].Current != "plan 15 in use, not forced, avg 840 ms" ||
		p.Changes[0].Target != "plan 12 forced, avg 120 ms" || fix.Options["planId"] != "12" || fix.CheckID != "QRY01" {
		t.Errorf("changes %+v fix %+v", p.Changes, fix)
	}
	w := strings.Join(p.Warnings, "\n")
	for _, s := range []string{"Force plan 12 for this query: avg 120 ms against 840 ms for the plan now in use (plan 15).",
		"Forcing hides regressions", "Stop forcing it when the code", "need ALTER on the database GraniteLive"} {
		if !strings.Contains(w, s) {
			t.Errorf("warnings lack %q:\n%s", s, w)
		}
	}
	if strings.Contains(w, "not faster") || strings.Contains(w, "failed before") {
		t.Errorf("unexpected warnings:\n%s", w)
	}

	// Forcing the slower plan, with a forced plan already there and earlier failures.
	f := planForceFacts()
	f.PlanID = 15
	f.Plans[0].Forced = true
	f.Plans[1].ForceFailures, f.Plans[1].ForceFailureReason, f.Plans[1].Runs = 2, "NO_PLAN", 4
	f.Plans[1].Duration = 4 * 840000
	p, _, err = BuildPlanForceFix(FixForcePlan, f)
	if err != nil {
		t.Fatal(err)
	}
	w = strings.Join(p.Warnings, "\n")
	if p.Rollback != "EXEC [GraniteLive].sys.sp_query_store_force_plan @query_id = 345, @plan_id = 12;" ||
		p.Changes[0].Current != "plan 12 forced, avg 120 ms" ||
		!strings.Contains(w, "Plan 15 is not faster than plan 12") || !strings.Contains(w, "ran only 4 times") ||
		!strings.Contains(w, "Forcing plan 15 has failed before (2 times, no_plan)") {
		t.Errorf("second preview %q\n%s", p.Rollback, w)
	}

	// Refusals: already forced, not there, Query Store off.
	f = planForceFacts()
	f.Plans[0].Forced = true
	_, _, err = BuildPlanForceFix(FixForcePlan, f)
	refusal(t, err, false, "plan 12 is already forced")
	f = planForceFacts()
	f.PlanID, f.Found = 99, false
	_, _, err = BuildPlanForceFix(FixForcePlan, f)
	refusal(t, err, false, "there is no plan 99")
	f = planForceFacts()
	f.QueryStoreState = "OFF"
	_, _, err = BuildPlanForceFix(FixForcePlan, f)
	refusal(t, err, false, "Query Store is off on GraniteLive")

	// Read-only Query Store: allowed, with its warning.
	f = planForceFacts()
	f.QueryStoreState = "READ_ONLY"
	if p, _, err = BuildPlanForceFix(FixForcePlan, f); err != nil || !strings.Contains(strings.Join(p.Warnings, " "), "read-only") {
		t.Errorf("read-only %v %+v", err, p.Warnings)
	}
}

func TestUnforcePlanPreview(t *testing.T) {
	f := planForceFacts()
	_, _, err := BuildPlanForceFix(FixUnforcePlan, f)
	refusal(t, err, false, "plan 12 is not forced")

	f.Plans[0].Forced = true
	p, fix, err := BuildPlanForceFix(FixUnforcePlan, f)
	if err != nil {
		t.Fatal(err)
	}
	if p.Statement != "EXEC [GraniteLive].sys.sp_query_store_unforce_plan @query_id = 345, @plan_id = 12;" ||
		p.Rollback != "EXEC [GraniteLive].sys.sp_query_store_force_plan @query_id = 345, @plan_id = 12;" ||
		fix.Current != "plan 12 forced, avg 120 ms" || fix.Target != "no plan forced" ||
		!strings.Contains(strings.Join(p.Warnings, " "), "SQL Server goes back to choosing the plan itself") {
		t.Errorf("unforce %+v %+v", p, fix)
	}

	// Read back and the change log line.
	after, ok := PlanForceReadBack(FixUnforcePlan, planForceFacts())
	if after != "plan 12 not forced" || !ok {
		t.Errorf("read back %q %v", after, ok)
	}
	if after, ok := PlanForceReadBack(FixForcePlan, planForceFacts()); ok || after != "plan 12 not forced" {
		t.Errorf("force read back %q %v", after, ok)
	}
	if d := PlanForceDescription(FixUnforcePlan, f, fix); d != "Stopped forcing plan 12 for query 345 (0x9F3A00112233AABB) in GraniteLive: plan 12 forced, avg 120 ms -> no plan forced" {
		t.Errorf("description %q", d)
	}
}

func TestEvictPlanPreview(t *testing.T) {
	created := time.Date(2026, 11, 3, 8, 0, 0, 0, time.UTC)
	f := CachedPlanFacts{Handle: "0x06000500AABBCCDDEEFF00112233445566778899", Found: true, ObjectType: "Proc", ObjectName: "dbo.PickList",
		UseCount: 300, Created: &created}
	p, fix, err := BuildEvictPlanFix(f, "GraniteLive", "16.0.4255.2")
	if err != nil {
		t.Fatal(err)
	}
	if p.Statement != "DBCC FREEPROCCACHE (0x06000500AABBCCDDEEFF00112233445566778899);" || p.Rollback != "" || p.Database != "" ||
		fix.Scope != ScopeServer || p.Changes[0].Current != "in the cache since 2026-11-03 08:00, used 300 times" || p.Changes[0].Target != "removed from the cache" {
		t.Errorf("evict %+v %+v", p, fix)
	}
	w := strings.Join(p.Warnings, "\n")
	for _, s := range []string{"Remove this plan from the cache so SQL Server compiles a fresh one. Harmless; the next run compiles.",
		"The plan is the whole procedure dbo.PickList", "ALTER SERVER STATE"} {
		if !strings.Contains(w, s) {
			t.Errorf("warnings lack %q:\n%s", s, w)
		}
	}
	_, _, err = BuildEvictPlanFix(CachedPlanFacts{Handle: f.Handle}, "GraniteLive", "")
	refusal(t, err, false, "no longer in the plan cache")
	if after, ok := EvictPlanReadBack(CachedPlanFacts{}); !ok || after != "removed from the plan cache" {
		t.Errorf("read back %q", after)
	}
	if d := EvictPlanDescription(f, "0x9F3A00112233AABB"); d != "Removed plan 0x06000500AABBCCDD… from the plan cache (query 0x9F3A00112233AABB), procedure dbo.PickList" {
		t.Errorf("description %q", d)
	}
}

func TestUpdateTableStatisticsPreview(t *testing.T) {
	now := time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)
	f := TableStatsFacts{Database: "GraniteLive", Schema: "dbo", Table: "TrackingEntity", Found: true, Rows: 4200000, Stats: 5,
		OldestUpdate: qPtime(now.Add(-31 * 24 * time.Hour)), MaxModPct: 48, ServerNow: now}
	p, fix, err := BuildTableStatsFix(f)
	if err != nil {
		t.Fatal(err)
	}
	if p.Statement != "UPDATE STATISTICS [dbo].[TrackingEntity] WITH FULLSCAN;" || p.Rollback != "" ||
		p.Changes[0].Setting != "Statistics on dbo.TrackingEntity" ||
		p.Changes[0].Current != "5 statistics, 4.2 M rows, oldest updated 2026-10-03 (31 days ago), 48% of rows changed" ||
		p.Changes[0].Target != "updated now, full scan" || fix.Options["table"] != "[dbo].[TrackingEntity]" {
		t.Errorf("preview %+v", p)
	}
	w := strings.Join(p.Warnings, "\n")
	for _, s := range []string{"has 4.2 M rows: a full scan reads the whole table", "It runs online", "Needs ALTER on the table"} {
		if !strings.Contains(w, s) {
			t.Errorf("warnings lack %q:\n%s", s, w)
		}
	}
	if TableStatsTimeout(f.Rows) != 10*time.Minute {
		t.Error("timeout")
	}

	big := f
	big.Rows = 25_000_000
	p, fix, _ = BuildTableStatsFix(big)
	if p.Statement != "UPDATE STATISTICS [dbo].[TrackingEntity] WITH SAMPLE 25 PERCENT;" || fix.Target != "updated now, 25% sample" ||
		!strings.Contains(strings.Join(p.Warnings, " "), "25% sample") || TableStatsTimeout(big.Rows) != 30*time.Minute {
		t.Errorf("big %+v", p)
	}

	small := f
	small.Rows, small.Schema, small.Table = 300, "we]ird", "T"
	if p, _, _ = BuildTableStatsFix(small); p.Statement != "UPDATE STATISTICS [we]]ird].[T] WITH FULLSCAN;" ||
		!strings.Contains(strings.Join(p.Warnings, " "), "this takes seconds") {
		t.Errorf("small %+v", p)
	}

	_, _, err = BuildTableStatsFix(TableStatsFacts{Database: "GraniteLive", Schema: "dbo", Table: "Nope"})
	refusal(t, err, false, "there is no table dbo.Nope in GraniteLive")
	_, _, err = BuildTableStatsFix(TableStatsFacts{Database: "GraniteLive", Schema: "dbo", Table: "Empty", Found: true})
	refusal(t, err, false, "has no statistics yet")

	// Read back: every statistic updated after the update started.
	after := f
	after.OldestUpdate = qPtime(now.Add(30 * time.Second))
	if s, ok := TableStatsReadBack(after, now); !ok || s != "oldest statistics updated 2026-11-03 12:00:30" {
		t.Errorf("read back %q %v", s, ok)
	}
	if _, ok := TableStatsReadBack(f, now); ok {
		t.Error("stale statistics read back as verified")
	}
	if d := TableStatsDescription(f, p.Statement); !strings.HasPrefix(d, "Updated statistics on dbo.TrackingEntity (5 statistics, 4.2 M rows") || !strings.HasSuffix(d, "full scan") {
		t.Errorf("description %q", d)
	}
}

func TestQueryFixOptions(t *testing.T) {
	if id, err := ParsePlanID(" 12 "); err != nil || id != 12 {
		t.Errorf("plan id %d %v", id, err)
	}
	for _, bad := range []string{"", "0", "-3", "12; DROP", "x"} {
		_, err := ParsePlanID(bad)
		refusal(t, err, true, "planId")
	}
	if h, b, err := ParsePlanHandle("0x06000500aabb"); err != nil || h != "0x06000500AABB" || len(b) != 6 {
		t.Errorf("handle %q %v %v", h, b, err)
	}
	for _, bad := range []string{"", "0x", "06000500", "0x0600050", "0xGG", "0x" + strings.Repeat("A", 130), "0x06); DROP TABLE x;--"} {
		_, _, err := ParsePlanHandle(bad)
		refusal(t, err, true, "planHandle")
	}
	for in, want := range map[string][2]string{
		"[dbo].[TrackingEntity]": {"dbo", "TrackingEntity"}, "dbo.Pick": {"dbo", "Pick"}, "Pick": {"dbo", "Pick"},
		"[we.ird].[a]]b]": {"we.ird", "a]b"}, " sales . Order ": {"sales", "Order"},
	} {
		s, tb, err := ParseTableOption(in)
		if err != nil || s != want[0] || tb != want[1] {
			t.Errorf("ParseTableOption(%q) = %q %q %v", in, s, tb, err)
		}
	}
	for _, bad := range []string{"", "a.b.c", "[dbo", ".x", "dbo."} {
		_, _, err := ParseTableOption(bad)
		refusal(t, err, true, "table")
	}
}
