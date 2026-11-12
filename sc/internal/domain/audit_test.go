package domain

import (
	"errors"
	"strings"
	"testing"
	"time"
)

var a0 = time.Date(2026, 11, 2, 8, 0, 0, 0, time.UTC)

func snap(kv ...string) Baseline {
	b := Baseline{Items: map[string]*string{}, Errors: map[string]string{}}
	for i := 0; i+1 < len(kv); i += 2 {
		if kv[i+1] == "<null>" {
			b.Items[kv[i]] = nil
			continue
		}
		b.Items[kv[i]] = Str(kv[i+1])
	}
	return b
}

func items(cs []SettingChange) []string {
	out := []string{}
	for _, c := range cs {
		out = append(out, c.Kind+" "+c.Item+" "+c.From+">"+c.To)
	}
	return out
}

func TestDiffBaselinesNullIsNeverAChange(t *testing.T) {
	prev := snap("db/Sales/present", "yes", "db/Sales/delayed_durability", "<null>", "db/Sales/recovery_model", "SIMPLE")
	next := snap("db/Sales/present", "yes", "db/Sales/delayed_durability", "DISABLED", "db/Sales/recovery_model", "<null>")
	if got := DiffBaselines(prev, next, a0, a0.Add(time.Hour)); len(got) != 0 {
		t.Fatalf("NULL on either side made changes: %v", items(got))
	}
	// An item missing from one side (an area not read) is not a change either.
	if got := DiffBaselines(snap("config/x", "1"), snap(), a0, a0); len(got) != 0 {
		t.Fatalf("a missing item made a change: %v", items(got))
	}
}

func TestDiffBaselinesGroupsAddedAndRemoved(t *testing.T) {
	prev := snap("db/Old/present", "yes", "db/Old/recovery_model", "FULL", "file/Old/Old_log/present", "LOG", "file/Old/Old_log/growth", "64 MB",
		"job/Nightly backup/present", "yes", "job/Nightly backup/backup", "yes", "job/Nightly backup/enabled", "1")
	next := snap("db/New_test/present", "yes", "db/New_test/recovery_model", "SIMPLE", "file/New_test/New_test/present", "ROWS",
		"job/Stats/present", "yes", "job/Stats/enabled", "1")
	got := DiffBaselines(prev, next, a0, a0.Add(time.Hour))
	want := []string{"added db/New_test >", "removed db/Old >", "removed job/Nightly backup >", "added job/Stats >"}
	if strings.Join(items(got), "|") != strings.Join(want, "|") {
		t.Fatalf("got %v\nwant %v", items(got), want)
	}
	if got[2].Props["backup"] != "yes" || got[2].Headline() != "Agent job Nightly backup removed (it takes backups)" {
		t.Fatalf("removed job: %+v %q", got[2].Props, got[2].Headline())
	}
	// Not read the second time: nothing was removed.
	next.Errors[AreaDatabases], next.Errors[AreaJobs], next.Errors[AreaFiles] = "no right", "no right", "no right"
	if got := DiffBaselines(prev, next, a0, a0); len(got) != 0 {
		t.Fatalf("an area not read made changes: %v", items(got))
	}
}

func TestDiffBaselinesRestartVersionPending(t *testing.T) {
	prev := snap(ItemStartTime, "2026-11-01T06:00:00", ItemVersion, "16.0.1000.6", ItemCU, "RTM",
		ItemMaxServerMemory, "10240")
	next := snap(ItemStartTime, "2026-11-02T07:30:00", ItemVersion, "16.0.4135.4", ItemCU, "CU15",
		ItemMaxServerMemory, "10240", "config.pending/max server memory (MB)", "2147483647")
	got := DiffBaselines(prev, next, a0, a0.Add(time.Hour))
	want := []string{
		"pending config.pending/max server memory (MB) 10240>2147483647",
		"version server/version 16.0.1000.6 RTM>16.0.4135.4 CU15",
		"restart server/start_time 2026-11-01T06:00:00>2026-11-02T07:30:00",
	}
	if strings.Join(items(got), "|") != strings.Join(want, "|") {
		t.Fatalf("got %v\nwant %v", items(got), want)
	}
	if h := got[0].Headline(); h != "max server memory (MB) set to no cap but not yet in use (RECONFIGURE not run)" {
		t.Fatalf("pending headline %q", h)
	}
	if h := got[2].Headline(); h != "SQL Server restarted at 2026-11-02 07:30:00 (server time)" {
		t.Fatalf("restart headline %q", h)
	}
	// The value then moves: the change itself, the pending item's going is not a change.
	after := snap(ItemStartTime, "2026-11-02T07:30:00", ItemVersion, "16.0.4135.4", ItemCU, "CU15", ItemMaxServerMemory, "2147483647")
	got = DiffBaselines(next, after, a0, a0)
	if len(got) != 1 || got[0].Headline() != "max server memory (MB) changed from 10240 to no cap" {
		t.Fatalf("got %v", items(got))
	}
}

func TestBaselineHashIsOrderFree(t *testing.T) {
	a := BaselineFacts{Items: map[string]*string{"a": Str("1"), "b": nil}, Errors: map[string]string{}}
	b := BaselineFacts{Items: map[string]*string{"b": nil, "a": Str("1")}, Errors: map[string]string{}}
	c := BaselineFacts{Items: map[string]*string{"a": Str("1"), "b": Str("")}, Errors: map[string]string{}}
	if a.Hash() != b.Hash() || a.Hash() == c.Hash() {
		t.Fatal("hash: same items must match, NULL and empty must differ")
	}
}

func TestParseConfigChange(t *testing.T) {
	o, f, to, ok := ParseConfigChange("Configuration option 'max server memory (MB)' changed from 2147483647 to 98304. Run the RECONFIGURE statement to install.")
	if !ok || o != "max server memory (MB)" || f != "2147483647" || to != "98304" {
		t.Fatalf("got %q %q %q %v", o, f, to, ok)
	}
	if _, _, _, ok := ParseConfigChange("Error: 17053, Severity: 16"); ok {
		t.Fatal("not a configuration change")
	}
	since := a0
	facts := []SettingChangeFact{
		{At: a0.Add(time.Hour), Text: "Configuration option 'show advanced options' changed from 1 to 1. Run the RECONFIGURE statement to install.", Login: "DOM\\jane"},
		{At: a0.Add(2 * time.Hour), Text: "Configuration option 'cost threshold for parallelism' changed from 5 to 50. Run the RECONFIGURE statement to install.", Program: "kakapo"},
		{At: a0.Add(-time.Hour), Text: "Configuration option 'max degree of parallelism' changed from 0 to 4. Run the RECONFIGURE statement to install."},
		{At: a0.Add(3 * time.Hour), Text: "2026-11-12 12:49:16.95 spid85      Configuration option 'max server memory (MB)' changed from 2147483647 to 98304. Run the RECONFIGURE statement to install.",
			Login: "DOMAIN\\jane", Host: "APP-SRV01", Program: "Microsoft SQL Server Management Studio"},
	}
	got := BuildSeenSettingChanges(7, facts, since)
	if len(got) != 1 {
		t.Fatalf("from = to, kakapo's own and before since must be left out: %+v", got)
	}
	c := got[0]
	if c.Database != "" || c.ChangeType != "SETTING" || c.Object != "max server memory (MB)" || c.Result != ChangeSeenOnServer ||
		c.Description != "max server memory (MB) changed from 2147483647 to 98304 by DOMAIN\\jane on APP-SRV01 (Microsoft SQL Server Management Studio)" {
		t.Fatalf("server-level row: %+v", c)
	}
}

func TestAttributionOrder(t *testing.T) {
	c := SettingChange{Item: ItemCostThreshold, Kind: SettingChanged, From: "5", To: "50", Before: a0, After: a0.Add(10 * time.Second)}
	action := AuditAction{Start: ActionLogRow{ActionID: "A1", Phase: ActionStarted, StartedAt: a0.Add(2 * time.Second), FixID: "cost-threshold",
		WindowsUser: `DOM\jane`, KakapoHost: "LAPTOP-7", Database: "Sales"}}
	seen := []Change{{ID: 9, ChangedAt: a0.Add(3 * time.Second), Object: "cost threshold for parallelism", ChangeType: "SETTING",
		Description: "cost threshold for parallelism changed from 5 to 50 by DOM\\bob on PC1 (SSMS)", Result: ChangeSeenOnServer}}

	if a := AttributeSettingChange(c, []AuditAction{action}, seen); a.Kind != ByKakapo || a.ActionID != "A1" || a.Who != `Set by kakapo: DOM\jane on LAPTOP-7` {
		t.Fatalf("kakapo first: %+v", a)
	}
	// An action of another setting does not take it.
	other := action
	other.Start.FixID = "maxdop"
	if a := AttributeSettingChange(c, []AuditAction{other}, seen); a.Kind != ByTrace || a.ChangeID != 9 || a.Who != "By DOM\\bob on PC1 (SSMS), from SQL Server's trace" {
		t.Fatalf("then the trace: %+v", a)
	}
	// An action outside the bracket does not take it either; nor a trace row of another option.
	late := action
	late.Start.StartedAt = a0.Add(time.Hour)
	seen[0].Object = "max degree of parallelism"
	a := AttributeSettingChange(c, []AuditAction{late}, seen)
	if a.Kind != ByUnknown || a.Who != "Changed outside kakapo. SQL Server's trace no longer holds who." {
		t.Fatalf("otherwise unknown, never blank: %+v", a)
	}
	// A database option: the database altered, seen in the trace (5f's row).
	db := SettingChange{Item: "db/Sales/recovery_model", Kind: SettingChanged, From: "SIMPLE", To: "FULL", Before: a0, After: a0.Add(time.Hour)}
	alt := []Change{{ID: 4, Database: "Sales", ChangedAt: a0.Add(time.Minute), Object: "Sales", ChangeType: "SETTING",
		Description: "Database Sales altered by sa on HOST (SSMS)", Result: ChangeSeenOnServer}}
	if a := AttributeSettingChange(db, nil, alt); a.Kind != ByTrace || a.ChangeID != 4 {
		t.Fatalf("database altered: %+v", a)
	}
}

func TestAuditRulesRowByRow(t *testing.T) {
	lb := map[string]time.Time{}
	ctx := FlagContext{LastLogBackup: lb}
	s := func(item, kind, from, to string) SettingChange {
		return SettingChange{Item: item, Kind: kind, From: from, To: to, Before: a0, After: a0.Add(time.Hour)}
	}
	cases := []struct {
		name string
		c    SettingChange
		rule string
		flag string
	}{
		{"cap removed", s(ItemMaxServerMemory, SettingChanged, "10240", "2147483647"), "memory-cap-raised", FlagRed},
		{"cap raised", s(ItemMaxServerMemory, SettingChanged, "10240", "20480"), "memory-cap-raised", FlagRed},
		{"cap lowered", s(ItemMaxServerMemory, SettingChanged, "20480", "10240"), "memory-cap-lowered", FlagAmber},
		{"min raised", s(ItemMinServerMemory, SettingChanged, "0", "4096"), "min-memory-raised", FlagAmber},
		{"min lowered", s(ItemMinServerMemory, SettingChanged, "4096", "0"), "", ""},
		{"lock pages", s(ItemMemoryModel, SettingChanged, "CONVENTIONAL", "LOCK_PAGES"), "lock-pages", FlagAmber},
		{"maxdop", s(ItemMaxDop, SettingChanged, "0", "4"), "parallelism", FlagAmber},
		{"cost threshold", s(ItemCostThreshold, SettingChanged, "5", "50"), "parallelism", FlagAmber},
		{"recovery full", s("db/Sales/recovery_model", SettingChanged, "SIMPLE", "FULL"), "recovery-full", FlagRed},
		{"recovery simple", s("db/Sales/recovery_model", SettingChanged, "FULL", "SIMPLE"), "", ""},
		{"backup job removed", SettingChange{Item: "job/Nightly", Kind: SettingRemoved, Props: map[string]string{"backup": "yes"}}, "backup-job", FlagRed},
		{"other job removed", SettingChange{Item: "job/Stats", Kind: SettingRemoved, Props: map[string]string{"backup": "no"}}, "", ""},
		{"backup job disabled", SettingChange{Item: "job/Nightly/enabled", Kind: SettingChanged, From: "1", To: "0", Props: map[string]string{"backup": "yes"}}, "backup-job", FlagRed},
		{"auto shrink on", s("db/Sales/auto_shrink", SettingChanged, "OFF", "ON"), "auto-shrink-close", FlagRed},
		{"auto close on", s("db/Sales/auto_close", SettingChanged, "OFF", "ON"), "auto-shrink-close", FlagRed},
		{"auto close off", s("db/Sales/auto_close", SettingChanged, "ON", "OFF"), "", ""},
		{"compat", s("db/Sales/compatibility_level", SettingChanged, "130", "160"), "compatibility-level", FlagAmber},
		{"rcsi on", s("db/Sales/rcsi", SettingChanged, "OFF", "ON"), "row-versioning", FlagAmber},
		{"snapshot on", s("db/Sales/snapshot_isolation", SettingChanged, "OFF", "ON"), "row-versioning", FlagAmber},
		{"qs off", s("db/Sales/query_store", SettingChanged, "ON", "OFF"), "query-store-off", FlagAmber},
		{"qs read-only", s("db/Sales/query_store_state", SettingChanged, "READ_WRITE", "READ_ONLY"), "query-store-off", FlagAmber},
		{"db added", SettingChange{Item: "db/Sales", Kind: SettingAdded}, "database-added", FlagAmber},
		{"test db added", SettingChange{Item: "db/Sales_test", Kind: SettingAdded}, "database-added", FlagRed},
		{"trace flags", s(ItemTraceFlags, SettingChanged, "none", "3226"), "trace-flags", FlagAmber},
		{"version", SettingChange{Item: ItemVersion, Kind: SettingVersion, From: "a", To: "b"}, "version", FlagAmber},
		{"restart", SettingChange{Item: ItemStartTime, Kind: SettingRestart}, "restart", FlagGrey},
		{"growth", s("file/Sales/Sales/growth", SettingChanged, "10%", "64 MB"), "", ""},
	}
	for _, tc := range cases {
		h := RuleForSetting(tc.c, ctx)
		switch {
		case tc.rule == "" && h != nil:
			t.Errorf("%s: matched %s", tc.name, h.Rule)
		case tc.rule != "" && (h == nil || h.Rule != tc.rule || h.Flag != tc.flag):
			t.Errorf("%s: got %+v, want %s %s", tc.name, h, tc.rule, tc.flag)
		}
	}
	// A server-level change links to Memory only.
	if h := RuleForSetting(cases[6].c, ctx); len(h.Screens) != 1 || h.Screens[0] != ScreenMemory {
		t.Errorf("server-level screens: %v", h.Screens)
	}
	if h := RuleForSetting(cases[8].c, ctx); strings.Join(h.Screens, ",") != "backups,storage" {
		t.Errorf("database screens: %v", h.Screens)
	}
	// Recovery to full goes grey after a later log backup.
	lb["sales"] = a0.Add(2 * time.Hour)
	if h := RuleForSetting(cases[8].c, ctx); h.Flag != FlagGrey {
		t.Errorf("recovery full after a log backup: %+v", h)
	}
	lb["sales"] = a0.Add(30 * time.Minute)
	if h := RuleForSetting(cases[8].c, ctx); h.Flag != FlagRed {
		t.Errorf("a log backup before the change does not count: %+v", h)
	}
	// kakapo's own: a reorganise fills the cache.
	if h := RuleForAction(ActionLogRow{FixID: FixReorganizeIndexes}); h == nil || h.Flag != FlagGrey || h.Rule != "kakapo-cache" {
		t.Errorf("reorganise: %+v", h)
	}
	if h := RuleForAction(ActionLogRow{FixID: "cost-threshold"}); h != nil {
		t.Errorf("a setting fix is flagged by its setting, not the action: %+v", h)
	}
}

func TestActionOutcome(t *testing.T) {
	cases := []struct {
		res     FixResult
		err     error
		stopped bool
		want    string
	}{
		{FixResult{Verified: true}, nil, false, OutcomeVerified},
		{FixResult{}, nil, false, OutcomeDiffers},
		{FixResult{}, errors.New("refused"), false, OutcomeFailed},
		{FixResult{}, errors.New("context canceled"), true, OutcomeStopped},
		{FixResult{Verified: true}, nil, true, OutcomeStopped},
	}
	for _, c := range cases {
		if got := ActionOutcome(c.res, c.err, c.stopped); got != c.want {
			t.Errorf("%+v %v %v: %s, want %s", c.res, c.err, c.stopped, got, c.want)
		}
	}
	started := ActionLogRow{ActionID: "X", Phase: ActionStarted, StartedAt: a0}
	acts := GroupActions([]ActionLogRow{started})
	if len(acts) != 1 || acts[0].Outcome() != OutcomeUnknown {
		t.Fatalf("started without ended is unknown: %+v", acts)
	}
	ended := ActionLogRow{ActionID: "X", Phase: ActionEnded, StartedAt: a0, Outcome: OutcomeFailed}
	acts = GroupActions([]ActionLogRow{started, ended})
	if len(acts) != 1 || acts[0].Outcome() != OutcomeFailed {
		t.Fatalf("joined: %+v", acts)
	}
}

func TestBuildAuditRowsAndVerdict(t *testing.T) {
	changes := []SettingChange{
		{Item: ItemMaxServerMemory, Kind: SettingChanged, From: "10240", To: "2147483647", Before: a0, After: a0.Add(time.Hour)},
		{Item: ItemCostThreshold, Kind: SettingChanged, From: "5", To: "50", Before: a0.Add(2 * time.Hour), After: a0.Add(2*time.Hour + 5*time.Second), AfterAction: "A1"},
	}
	actions := []AuditAction{
		{Start: ActionLogRow{ActionID: "A1", Phase: ActionStarted, StartedAt: a0.Add(2*time.Hour + time.Second), FixID: "cost-threshold", WindowsUser: "u", KakapoHost: "h",
			Database: "Sales", Description: "Cost threshold", Statement: "EXEC sp_configure"},
			End: &ActionLogRow{ActionID: "A1", Phase: ActionEnded, Outcome: OutcomeVerified, Result: "50, verified"}},
		{Start: ActionLogRow{ActionID: "A2", Phase: ActionStarted, StartedAt: a0.Add(3 * time.Hour), FixID: FixReorganizeIndexes, Database: "Sales", Description: "Reorganise 3 indexes"}},
	}
	seen := []Change{{ID: 3, ChangedAt: a0.Add(30 * time.Minute), Database: "Sales", Object: "dbo.p", ChangeType: "PROCEDURE",
		Description: "Procedure dbo.p altered by x on y (SSMS)", Result: ChangeSeenOnServer}}
	rows := BuildAuditRows(AuditInputs{Changes: changes, Actions: actions, Seen: seen})
	if len(rows) != 4 {
		t.Fatalf("rows: %d %+v", len(rows), rows)
	}
	if rows[0].Key != "aA2" || rows[0].Outcome != OutcomeUnknown || rows[0].Flag != FlagGrey {
		t.Fatalf("newest first, the reorganise with no end: %+v", rows[0])
	}
	if !rows[1].ByKakapo || rows[1].Item != ItemCostThreshold || rows[1].Statement != "EXEC sp_configure" || rows[1].Database != "" {
		t.Fatalf("the diff attributed to the action, the action not listed again, a server setting: %+v", rows[1])
	}
	if rows[2].Flag != FlagRed || rows[2].From == nil || rows[2].ByKakapo {
		t.Fatalf("the cap removed outside kakapo, between two reads: %+v", rows[2])
	}
	if rows[3].Source != SourceTrace || rows[3].Who != "By x on y (SSMS), from SQL Server's trace" || rows[3].Headline != "Procedure dbo.p altered" {
		t.Fatalf("seen on the server: %+v", rows[3])
	}

	v, tone := BuildAuditVerdict(rows, false, "", &a0)
	if v != "Since kakapo started watching this server on 2 Nov 2026: 4 changes, 2 of them by kakapo." || tone != FlagRed {
		t.Fatalf("without a time: %q %s", v, tone)
	}
	problem := a0.Add(90 * time.Minute)
	from := problem.Add(-ProblemWindow)
	in := FilterAuditRows(rows, "", false, &from, &problem)
	v, tone = BuildAuditVerdict(in, true, "2 Nov 09:30", nil)
	if v != "2 changes in the 7 days before 2 Nov 09:30, 1 of them red: max server memory (MB) changed from 10240 to no cap." || tone != FlagRed {
		t.Fatalf("with a time: %q %s", v, tone)
	}
	v, tone = BuildAuditVerdict(nil, true, "1 Nov 09:30", nil)
	if v != "No kakapo action and no setting change in the 7 days before 1 Nov 09:30." || tone != "green" {
		t.Fatalf("with a time and nothing: %q %s", v, tone)
	}
	if got := FilterAuditRows(rows, "kakapo", false, nil, nil); len(got) != 2 {
		t.Fatalf("by kakapo: %d", len(got))
	}
	if got := FilterAuditRows(rows, "outside", true, nil, nil); len(got) != 1 || got[0].Flag != FlagRed {
		t.Fatalf("outside, red and amber: %+v", got)
	}
	cp := AuditCopy("verdict", rows)
	if !strings.HasPrefix(cp, "verdict\n\n") || strings.Count(cp, "\n") != 5 || !strings.Contains(cp, "[RED] max server memory (MB) changed from 10240 to no cap") {
		t.Fatalf("copy:\n%s", cp)
	}
}

func TestSeenSplit(t *testing.T) {
	for _, c := range [][3]string{
		{`Procedure dbo.p altered by DOM\jane on PC1 (SSMS)`, "Procedure dbo.p altered", `DOM\jane on PC1 (SSMS)`},
		{`Deploy by DOM\b on B1 (DacFx): 40 objects (12 procedures, 28 tables)`, "Deploy: 40 objects (12 procedures, 28 tables)", `DOM\b on B1 (DacFx)`},
		{"no one", "no one", ""},
	} {
		if what, who := seenSplit(c[0]); what != c[1] || who != c[2] {
			t.Errorf("%q: %q, %q", c[0], what, who)
		}
	}
}

func TestMemoryGaps(t *testing.T) {
	p := []MemoryPoint{{At: a0}, {At: a0.Add(time.Hour)}, {At: a0.Add(7 * 24 * time.Hour)}}
	g := MemoryGaps(p)
	if len(g) != 1 || g[0] != "kakapo was not opened between 2 Nov and 9 Nov, so there is no reading in between." {
		t.Fatalf("gaps: %v", g)
	}
	cap := CapMB(map[string]*string{ItemMaxServerMemory: Str("2147483647")})
	if cap != nil {
		t.Fatal("no cap is nil")
	}
}
