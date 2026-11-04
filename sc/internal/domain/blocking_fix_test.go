package domain

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func killTarget() KillTarget {
	f := blockingFixture()
	return KillTarget{Found: true, Session: f.Sessions[2], Behind: 2, OwnSession: 140, ServerName: `SQL01\WMS`, ProductVersion: "16.0.4135.4", ServerNow: blkNow}
}

func TestKillSessionPreview(t *testing.T) {
	p, fix, err := BuildKillSessionFix(killTarget())
	if err != nil {
		t.Fatal(err)
	}
	if fix.ID != FixKillSession || fix.Setting != "Session 83" || fix.Target != "killed" || fix.Options["sessionId"] != "83" {
		t.Errorf("fix %+v", fix)
	}
	if p.Statement != "KILL 83;" || p.Rollback != "" || len(p.Changes) != 1 || p.Database != "WMS" ||
		p.Changes[0].Current != "sleeping; last ran dbo.Prescript_Packing_MasterItem: UPDATE dbo.MasterItem SET LastCounted = GETDATE() WHERE MasterItemID = @id" ||
		p.Options["sessionId"] != "83" || p.Options["loginTime"] != "2026-11-03T08:14:00.000" {
		t.Errorf("preview %+v", p)
	}
	want := []string{
		"Session 83: login granite on WMS-APP01, program GraniteScheduler, database WMS, sleeping. 2 sessions wait behind it.",
		"Its transaction has been open for 6 minutes and has written 120.0 MB of log.",
		"Its work is rolled back: everything that transaction changed is undone.",
		"The rollback takes about as long as the work did: up to about 6 minutes here, and the sessions behind it keep waiting until it ends.",
		"Tell whoever owns it (granite on WMS-APP01) before you kill it.",
		KillPermission,
	}
	if strings.Join(p.Warnings, "\n") != strings.Join(want, "\n") {
		t.Errorf("warnings:\n%s", strings.Join(p.Warnings, "\n"))
	}
	if d := KillChangeDescription(killTarget()); d != `Killed session 83 (granite, GraniteScheduler on WMS-APP01) on SQL01\WMS: transaction open 6 minutes, 120.0 MB of log rolled back; 2 sessions wait behind it` {
		t.Errorf("description %q", d)
	}

	// A SQL Agent job running, without a transaction: the job named, nothing to roll back.
	job := blockingFixture().Sessions[0]
	job.OpenTran, job.TranStarted = 0, time.Time{}
	p, _, _ = BuildKillSessionFix(KillTarget{Found: true, Session: job, ServerNow: blkNow})
	if p.Changes[0].Current != "running INSERT INTO dbo.Stock (ItemID, Qty) SELECT ItemID, Qty FROM staging.Stock" ||
		p.Warnings[1] != "It is the SQL Agent job Nightly stock import: this run of the job fails, and the job's history says so." ||
		p.Warnings[2] != "It has no open transaction, so nothing is rolled back and it ends at once." {
		t.Errorf("job: %q %q", p.Changes, p.Warnings)
	}
}

// Refused: a system session, kakapo's own, one already rolling back; gone is ErrSessionGone.
func TestKillSessionRefusals(t *testing.T) {
	for name, tc := range map[string]struct {
		edit func(*KillTarget)
		want string
	}{
		"system id":   {func(k *KillTarget) { k.Session.Session = 12 }, "Session 12 is a SQL Server system session"},
		"background":  {func(k *KillTarget) { k.Session.IsUser = false }, "Session 83 is a SQL Server system session"},
		"own":         {func(k *KillTarget) { k.OwnSession = 83 }, "Session 83 is kakapo's own connection"},
		"own program": {func(k *KillTarget) { k.Session.Program = BlockingAppName }, "Session 83 is kakapo's own connection"},
		"rolling":     {func(k *KillTarget) { k.Session.Command = "KILLED/ROLLBACK" }, "Session 83 is already killed and is rolling its work back"},
	} {
		k := killTarget()
		tc.edit(&k)
		p, _, err := BuildKillSessionFix(k)
		if err != nil || len(p.Changes) != 0 || p.Statement != "" || !strings.HasPrefix(p.Warnings[0], tc.want) {
			t.Errorf("%s: %v %+v", name, err, p)
		}
	}
	if _, _, err := BuildKillSessionFix(KillTarget{}); !errors.Is(err, ErrSessionGone) {
		t.Errorf("gone: %v", err)
	}
}

func TestKillOptionsAndReadBack(t *testing.T) {
	for v, ok := range map[string]bool{"83": true, " 51 ": true, "0": false, "-1": false, "40000": false, "83;DROP": false, "": false} {
		if _, err := ParseSessionID(v); (err == nil) != ok {
			t.Errorf("ParseSessionID(%q): %v", v, err)
		}
	}
	login := time.Date(2026, 11, 3, 8, 14, 0, 0, time.UTC)
	opts := map[string]string{"sessionId": "83", "loginTime": KillLoginTime(login)}
	if KillLoginChanged(opts, login) || !KillLoginChanged(opts, login.Add(time.Second)) || KillLoginChanged(map[string]string{"sessionId": "83"}, login) {
		t.Error("login time check")
	}
	lt := KillLoginTime(login)
	for name, tc := range map[string]struct {
		rb       KillReadBack
		after    string
		verified bool
	}{
		"gone":     {KillReadBack{}, "Session 83 is gone.", true},
		"reused":   {KillReadBack{Present: true, LoginTime: login.Add(time.Minute), Status: "sleeping"}, "Session 83 is gone.", true},
		"rollback": {KillReadBack{Present: true, LoginTime: login, Command: "KILLED/ROLLBACK", PercentComplete: 40, RemainingMs: 30000, Message: "SPID 83: transaction rollback in progress."}, "Session 83 is rolling its work back: 40% done, about 30 seconds left. SQL Server: SPID 83: transaction rollback in progress.", true},
		"still":    {KillReadBack{Present: true, LoginTime: login, Status: "sleeping"}, "Session 83 is still connected (sleeping).", false},
	} {
		after, ok := KillResult(83, lt, tc.rb)
		if after != tc.after || ok != tc.verified {
			t.Errorf("%s: %q %v", name, after, ok)
		}
	}
	if KillStatement(83) != "KILL 83;" || KillStatusOnlyStatement(83) != "KILL 83 WITH STATUSONLY;" {
		t.Error("statements")
	}
}

func TestRCSIPreview(t *testing.T) {
	p, fix, err := BuildRCSIFix(RCSIFacts{Database: "WMS", Users: 37, ProductVersion: "16.0.4135.4"})
	if err != nil {
		t.Fatal(err)
	}
	if fix.ID != FixRCSI || fix.Current != "OFF" || fix.Target != "ON" || len(p.Changes) != 1 ||
		p.Changes[0] != (Drift{Setting: "READ_COMMITTED_SNAPSHOT", Current: "OFF", Target: "ON"}) ||
		p.Statement != "ALTER DATABASE [WMS] SET READ_COMMITTED_SNAPSHOT ON WITH ROLLBACK IMMEDIATE;" ||
		p.Rollback != "ALTER DATABASE [WMS] SET READ_COMMITTED_SNAPSHOT OFF WITH ROLLBACK IMMEDIATE;" {
		t.Errorf("preview %+v %+v", p, fix)
	}
	want := []string{
		"WITH ROLLBACK IMMEDIATE disconnects every user of WMS for a moment and undoes their open transactions (37 sessions are connected now), so apply it in a quiet minute.",
		"Row versions are kept in tempdb: watch its size for a few days (the Storage card's tempdb checks).",
		"Readers stop waiting for writers; writers still wait for writers, so blocking between two updates stays.",
		"Check with the vendor that the application does not rely on readers being blocked to keep two users off the same row (rare).",
		"Needs the ALTER permission on the database WMS.",
	}
	if strings.Join(p.Warnings, "\n") != strings.Join(want, "\n") {
		t.Errorf("warnings:\n%s", strings.Join(p.Warnings, "\n"))
	}
	if p, _, _ := BuildRCSIFix(RCSIFacts{Database: "a]b"}); !strings.Contains(p.Statement, "[a]]b]") || !strings.Contains(p.Warnings[0], "nobody else is connected now") {
		t.Errorf("quoted %q %q", p.Statement, p.Warnings[0])
	}
	if _, _, err := BuildRCSIFix(RCSIFacts{Database: "WMS", RCSI: true}); !errors.Is(err, ErrFixNotApplicable) {
		t.Errorf("already on: %v", err)
	}
}
