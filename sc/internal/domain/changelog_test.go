package domain

import (
	"errors"
	"strings"
	"testing"
	"time"
)

// install-change-log (docs/phase5w-contract.md, B5): the contract's DDL, only what is missing, then the copy.
func TestBuildInstallChangeLogFix(t *testing.T) {
	p, fix, err := BuildInstallChangeLogFix(ChangeLogFacts{ServerName: "SQL1"}, 12)
	if err != nil {
		t.Fatal(err)
	}
	if fix.ID != FixInstallChangeLog || fix.Current != ChangeLogAbsent || fix.Target != ChangeLogPresent || fix.CheckID != "CHG02" {
		t.Fatalf("fix %+v", fix)
	}
	for _, want := range []string{"USE master;\nGO\nCREATE TABLE dbo.kakapo_ChangeLog (", "CONSTRAINT UQ_kakapo_ChangeLog_Action UNIQUE (ActionID, Phase)",
		"DEFAULT ORIGINAL_LOGIN()", "AFTER UPDATE, DELETE", "THROW 50001, N'kakapo_ChangeLog is append-only",
		"EXEC sys.sp_addextendedproperty @name = N'kakapo'"} {
		if !strings.Contains(p.Statement, want) {
			t.Errorf("statement lacks %q", want)
		}
	}
	if strings.Count(p.Statement, "\nGO\n") != 3 || strings.HasSuffix(p.Statement, "GO") {
		t.Errorf("batches:\n%s", p.Statement)
	}
	if len(p.Changes) != 4 || p.Changes[3].Current != "12 rows in kakapo only" {
		t.Errorf("changes %+v", p.Changes)
	}
	if len(p.Warnings) != 4 || p.Warnings[1] != warnChangeLogSize || p.Warnings[3] != "Needs CREATE TABLE in master (sysadmin has it)." ||
		!strings.Contains(p.Warnings[2], "the change is refused") {
		t.Errorf("warnings %q", p.Warnings)
	}
	if !strings.Contains(p.Rollback, "DROP TRIGGER dbo.kakapo_ChangeLog_AppendOnly") || !strings.Contains(p.Rollback, "DROP TABLE dbo.kakapo_ChangeLog") {
		t.Errorf("rollback %s", p.Rollback)
	}

	// The table there, its trigger missing: only the trigger.
	p, fix, err = BuildInstallChangeLogFix(ChangeLogFacts{Table: true, Property: true}, 0)
	if err != nil || strings.Contains(p.Statement, "CREATE TABLE") || !strings.Contains(p.Statement, "CREATE TRIGGER") ||
		strings.Contains(p.Statement, "sp_addextendedproperty") || fix.Current != ChangeLogTriggerMissing || p.Changes[1].Current != "0 rows in kakapo only" {
		t.Fatalf("trigger missing: %+v %v\n%s", fix, err, p.Statement)
	}
	// Disabled: enabled again.
	p, _, err = BuildInstallChangeLogFix(ChangeLogFacts{Table: true, Trigger: true, TriggerOff: true, Property: true}, 1)
	if err != nil || !strings.Contains(p.Statement, "ENABLE TRIGGER dbo.kakapo_ChangeLog_AppendOnly ON dbo.kakapo_ChangeLog;") {
		t.Fatalf("disabled: %v\n%s", err, p.Statement)
	}
	// All there: nothing to do.
	if _, _, err := BuildInstallChangeLogFix(ChangeLogFacts{Table: true, Trigger: true, Property: true}, 3); !errors.Is(err, ErrFixNotApplicable) {
		t.Fatalf("installed: %v", err)
	}
}

func TestChangeLogReadBacks(t *testing.T) {
	ok := ChangeLogFacts{Table: true, Trigger: true, Property: true}
	for _, c := range []struct {
		f           ChangeLogFacts
		local, held int
		copyErr     string
		text        string
		verified    bool
	}{
		{ok, 4, 4, "", "present, holding 4 rows of kakapo's 4", true},
		{ok, 4, 3, "", "present, holding 3 rows of kakapo's 4", false},
		{ok, 4, 1, "timeout", "present, holding 1 row of kakapo's 4; the copy stopped: timeout", false},
		{ChangeLogFacts{Table: true, Trigger: true, TriggerOff: true, Property: true}, 0, 0, "", "present, holding 0 rows of kakapo's 0, but its trigger is disabled", false},
		{ChangeLogFacts{}, 2, 0, "", "missing in master: the table, the trigger", false},
		{ChangeLogFacts{Table: true, Trigger: true}, 2, 2, "", "missing in master: the extended property", false},
	} {
		text, v := ChangeLogInstallReadBack(c.f, c.local, c.held, c.copyErr)
		if text != c.text || v != c.verified {
			t.Errorf("%+v: %q %v", c, text, v)
		}
	}
	if text, v := ChangeLogRemoveReadBack(ChangeLogFacts{}); text != ChangeLogAbsent || !v {
		t.Errorf("removed: %q %v", text, v)
	}
	if text, v := ChangeLogRemoveReadBack(ChangeLogFacts{Table: true}); text != "still in master: "+ChangeLogTriggerMissing || v {
		t.Errorf("still there: %q %v", text, v)
	}
}

func TestBuildRemoveChangeLogFix(t *testing.T) {
	p, fix, err := BuildRemoveChangeLogFix(ChangeLogFacts{Table: true, Trigger: true, Property: true, Rows: 8}, "LAPTOP-7")
	if err != nil || fix.Current != ChangeLogPresent || fix.Target != ChangeLogAbsent || len(p.Changes) != 2 ||
		p.Changes[0].Current != "present, 8 rows" {
		t.Fatalf("%+v %+v %v", p, fix, err)
	}
	if p.Warnings[0] != "Anyone on the server loses the record of what kakapo did here. kakapo's own copy on LAPTOP-7 stays complete." {
		t.Errorf("warning %q", p.Warnings[0])
	}
	if !strings.Contains(p.Statement, "DROP TRIGGER") || !strings.Contains(p.Statement, "DROP TABLE dbo.kakapo_ChangeLog") {
		t.Errorf("statement %s", p.Statement)
	}
	if _, _, err := BuildRemoveChangeLogFix(ChangeLogFacts{}, "x"); !errors.Is(err, ErrFixNotApplicable) {
		t.Errorf("absent: %v", err)
	}
}

func TestChangeLogCatchUp(t *testing.T) {
	local := []ActionLogRow{{ActionID: "A", Phase: ActionCopied}, {ActionID: "B", Phase: ActionStarted}, {ActionID: "B", Phase: ActionEnded},
		{ActionID: "C", Phase: ActionStarted}}
	// The table holds A (upper case from SQL Server) and B's started row; C is new, B's ended row failed.
	held := map[string]bool{ChangeLogKey("a", ActionCopied): true, ChangeLogKey("B", ActionStarted): true, ChangeLogKey("Z", ActionStarted): true}
	missing, lost := ChangeLogCatchUp(local, held, 2)
	if len(missing) != 2 || missing[0].ActionID != "B" || missing[0].Phase != ActionEnded || missing[1].ActionID != "C" || lost != 0 {
		t.Fatalf("missing %+v lost %d", missing, lost)
	}
	// Emptied (TRUNCATE): kakapo knew of 4, the table holds none of them.
	missing, lost = ChangeLogCatchUp(local, map[string]bool{}, 4)
	if len(missing) != 4 || lost != 4 {
		t.Fatalf("truncated: %d %d", len(missing), lost)
	}
	// Another install's rows are not kakapo's to put back, and are shown, not copied.
	server := []ActionLogRow{{ActionID: "B", Phase: ActionStarted}, {ActionID: "Z", Phase: ActionStarted, KakapoHost: "DESK-2", StartedAt: time.Unix(5, 0)}}
	if o := ServerOnlyActions(server, local); len(o) != 1 || o[0].Start.ActionID != "Z" {
		t.Fatalf("others %+v", o)
	}
}

// CHG02 (B7): only when kakapo acted on the server and the table's state was read as absent; its fix is
// install-change-log.
func TestCHG02(t *testing.T) {
	for _, c := range []struct {
		acted bool
		state string
		fires bool
	}{{true, ChangeLogAbsent, true}, {false, ChangeLogAbsent, false}, {true, ChangeLogPresent, false}, {true, "", false}} {
		b := &upkeepBuild{f: UpkeepFacts{ServerName: "SQL1", KakapoActed: c.acted, ChangeLog: c.state}, findings: []Finding{}, skipped: map[string]string{}}
		b.chg02()
		if (len(b.findings) == 1) != c.fires {
			t.Fatalf("%+v: %+v", c, b.findings)
		}
		if c.fires {
			x := b.findings[0]
			if x.CheckID != "CHG02" || x.Severity != SeverityLow || x.Owner != OwnerUs || x.FixID != FixInstallChangeLog ||
				x.Finding != "kakapo has changed this server, and its log is not on it." {
				t.Fatalf("finding %+v", x)
			}
			fixes := AvailableUpkeepFixes(b.f, b.findings)
			if len(fixes) != 1 || fixes[0].ID != FixInstallChangeLog || fixes[0].Scope != ScopeServer {
				t.Fatalf("fixes %+v", fixes)
			}
		}
	}
}

// C3's "log removed": the table gone outside kakapo is red; with kakapo's remove fix it is said, grey.
func TestChangeLogRemovedRule(t *testing.T) {
	a0 := time.Date(2026, 11, 12, 8, 0, 0, 0, time.UTC)
	c := SettingChange{Item: ItemChangeLog, Kind: SettingChanged, From: ChangeLogPresent, To: ChangeLogAbsent, Before: a0, After: a0.Add(time.Hour)}
	if h := RuleForSetting(c, FlagContext{}); h == nil || h.Rule != "log-removed" || h.Flag != FlagRed || len(h.Screens) != 0 {
		t.Fatalf("removed: %+v", h)
	}
	if h := RuleForSetting(SettingChange{Item: ItemChangeLog, Kind: SettingChanged, From: ChangeLogAbsent, To: ChangeLogPresent}, FlagContext{}); h != nil {
		t.Fatalf("installed: %+v", h)
	}
	rows := BuildAuditRows(AuditInputs{Changes: []SettingChange{c}})
	if len(rows) != 1 || rows[0].Flag != FlagRed || rows[0].ByKakapo {
		t.Fatalf("outside: %+v", rows)
	}
	remove := AuditAction{Start: ActionLogRow{ActionID: "r1", Phase: ActionStarted, FixID: FixRemoveChangeLog, StartedAt: a0.Add(time.Minute),
		WindowsUser: "DOMAIN\\jane", Description: "Remove kakapo's change log from this server on SQL1"}}
	rows = BuildAuditRows(AuditInputs{Changes: []SettingChange{c}, Actions: []AuditAction{remove}})
	if len(rows) != 1 || rows[0].Flag != FlagGrey || !rows[0].ByKakapo || rows[0].Tell != "Removed with kakapo's remove fix; kakapo's own copy is complete." {
		t.Fatalf("by kakapo: %+v", rows)
	}
	// Another install's action, from the server's table.
	other := AuditAction{Start: ActionLogRow{ActionID: "z", Phase: ActionStarted, FixID: "maxdop", KakapoHost: "DESK-2", WindowsUser: "DOMAIN\\sam",
		StartedAt: a0, Description: "MAXDOP"}}
	rows = BuildAuditRows(AuditInputs{Others: []AuditAction{other}})
	if len(rows) != 1 || rows[0].Who != "By kakapo on DESK-2 (DOMAIN\\sam), from kakapo's log on the server" || rows[0].Source != SourceServerLog ||
		!rows[0].ByKakapo || rows[0].Outcome != OutcomeUnknown {
		t.Fatalf("other install: %+v", rows)
	}
}

// The maintenance procedure's history clean-up and HistoryCleanupCommand never touch kakapo's change log
// (B5): kakapo never deletes from it.
func TestMaintenanceCleanupNeverNamesTheChangeLog(t *testing.T) {
	for name, text := range map[string]string{"HistoryCleanupCommand": HistoryCleanupCommand(30), "maintenance procedure": maintProcBody,
		"install": MaintInstallStatement(true)} {
		if strings.Contains(strings.ToLower(text), "kakapo_changelog") {
			t.Errorf("%s names kakapo_ChangeLog", name)
		}
	}
}

func TestChangeLogHelperLine(t *testing.T) {
	if _, ok := changeLogHelperLine(nil); ok {
		t.Fatal("unread is shown")
	}
	h, _ := changeLogHelperLine(&ChangeLogFacts{})
	if h.State != HelperNotInstalled || h.FixID != FixInstallChangeLog {
		t.Fatalf("absent %+v", h)
	}
	h, _ = changeLogHelperLine(&ChangeLogFacts{Table: true, Trigger: true, Property: true, Rows: 3})
	if h.State != HelperInstalled || h.FixID != FixRemoveChangeLog || !strings.Contains(h.Sentence, "holding 3 rows") {
		t.Fatalf("installed %+v", h)
	}
	h, _ = changeLogHelperLine(&ChangeLogFacts{Table: true, Trigger: true, TriggerOff: true, Property: true})
	if h.FixID != FixInstallChangeLog || h.FixLabel != "Repair" || !strings.Contains(h.Sentence, "its trigger disabled") {
		t.Fatalf("disabled %+v", h)
	}
}
