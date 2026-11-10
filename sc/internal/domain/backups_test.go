package domain

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// serverNow is the server's clock in every test: 09:21 on 3 November, two hours ahead of UTC.
var serverNow = time.Date(2026, 11, 3, 9, 21, 0, 0, time.UTC)

func at(t time.Time) *time.Time { return &t }

func mbPtr(n int64) *int64 { return &n }

var backupsDef = CardDefs[0]

// facts is an online database created a year ago, CHECKDB 3 days ago, nothing backed up yet.
func facts(model string) BackupFacts {
	return BackupFacts{Database: "WarehouseLive", State: "ONLINE", RecoveryModel: model,
		CreateDate: serverNow.AddDate(-1, 0, 0), LastGoodCheckDb: at(serverNow.AddDate(0, 0, -3)),
		LogSizeMB: mbPtr(512), ServerNow: serverNow, UTCOffsetMinutes: 120}
}

// night is 22:00 server time, d days before today.
func night(d int) time.Time {
	return time.Date(2026, 11, 3-d, 22, 0, 0, 0, time.UTC)
}

// addFull records a full backup, as collect would: last full, restorable point and 7-day count.
func addFull(f *BackupFacts, t time.Time) {
	if f.LastFull == nil || t.After(*f.LastFull) {
		f.LastFull = at(t)
	}
	if !t.Before(serverNow.AddDate(0, 0, -7)) {
		f.RestorePoints = append(f.RestorePoints, t)
		f.Fulls7++
	}
}

// nightlyFulls adds a full backup at 22:00 for each of the last eight nights, except the skipped ones.
func nightlyFulls(f *BackupFacts, skip ...int) {
	for d := 1; d <= 8; d++ {
		skipped := false
		for _, s := range skip {
			skipped = skipped || s == d
		}
		if !skipped {
			addFull(f, night(d))
		}
	}
}

// logsEvery15 adds a log backup every 15 minutes over the last 7 days, the latest 15 minutes ago.
func logsEvery15(f *BackupFacts) {
	for k := 672; k >= 1; k-- {
		t := serverNow.Add(-time.Duration(k) * 15 * time.Minute)
		f.RestorePoints = append(f.RestorePoints, t)
		f.Logs7++
		f.LastLog = at(t)
	}
}

func build(f BackupFacts) (Card, []Finding, []CheckStatus) {
	return BuildBackupsCard(f, backupsDef)
}

func findingOf(t *testing.T, fs []Finding, id string) Finding {
	t.Helper()
	for _, f := range fs {
		if f.CheckID == id {
			return f
		}
	}
	t.Fatalf("no %s finding in %+v", id, fs)
	return Finding{}
}

func wantNoFinding(t *testing.T, fs []Finding, id string) {
	t.Helper()
	for _, f := range fs {
		if f.CheckID == id {
			t.Errorf("unexpected %s finding: %+v", id, f)
		}
	}
}

// forIT is the findings left to Client IT: all of them unless given (BAK02 is ours when kakapo can fix it).
func wantCard(t *testing.T, c Card, status, statusText, verdict string, toDo int, forIT ...int) {
	t.Helper()
	if c.Status != status || c.StatusText != statusText || c.Verdict != verdict {
		t.Errorf("card %s %q %q, want %s %q %q", c.Status, c.StatusText, c.Verdict, status, statusText, verdict)
	}
	// Fixable counts the findings with a fix; since phase 4d BAK03, BAK04 and DBC08 carry one
	// (TestBackupActionFindings checks the count).
	wantIT := toDo
	if len(forIT) > 0 {
		wantIT = forIT[0]
	}
	if c.ToDo != toDo || c.ForClientIT != wantIT {
		t.Errorf("toDo %d forClientIT %d, want %d %d", c.ToDo, c.ForClientIT, toDo, wantIT)
	}
}

func wantChecks(t *testing.T, checks []CheckStatus, want ...string) {
	t.Helper()
	got := []string{}
	for _, c := range checks {
		s := c.CheckID + "=" + c.Status
		if c.Note != "" {
			s += ":" + c.Note
		}
		got = append(got, s)
	}
	if strings.Join(got, "; ") != strings.Join(want, "; ") {
		t.Errorf("checks\n got %q\nwant %q", got, want)
	}
}

func wantFinding(t *testing.T, f Finding, severity Severity, when, effort, finding string) {
	t.Helper()
	if f.Severity != severity || f.When != when || f.Effort != effort || f.Finding != finding {
		t.Errorf("%s: %s %s %s %q\nwant %s %s %s %q", f.CheckID, f.Severity, f.When, f.Effort, f.Finding, severity, when, effort, finding)
	}
	// BAK02 is ours when kakapo can fix it (it can switch the recovery model), else Client IT's.
	owner := OwnerClientIT
	if f.CheckID == "BAK02" && f.FixID != "" {
		owner = OwnerUs
	}
	if f.Owner != owner || f.Title == "" || f.ThenCheck == "" || f.Why == "" || f.DoThis == "" || f.Evidence == "" || f.Object != "WarehouseLive" {
		t.Errorf("%s: incomplete finding %+v", f.CheckID, f)
	}
}

const (
	bak01Sentence = "No full backup of this database has been recorded."
	bak02Never    = "Database uses FULL recovery but its log has never been backed up."
)

func TestBackupsNeverBackedUp(t *testing.T) {
	f := facts("FULL")
	f.CreateDate = serverNow.AddDate(0, -1, 0)
	c, fs, checks := build(f)
	wantCard(t, c, StatusRed, "Needs attention today", bak01Sentence, 2, 1)
	wantFinding(t, findingOf(t, fs, "BAK01"), SeverityCritical, WhenOnline, EffortQuickWin, bak01Sentence)
	if e := fs[0].Evidence; e != "Last full: never. Last differential: never." {
		t.Errorf("BAK01 evidence %q", e)
	}
	wantFinding(t, findingOf(t, fs, "BAK02"), SeverityHigh, WhenOnline, EffortQuickWin, bak02Never)
	wantNoFinding(t, fs, "BAK05")
	wantFacts(t, c, "Worst-case data loss=no backup/worse", "Last full backup=never/worse",
		"Last CHECKDB=3 days ago/normal", "Log backups=none in FULL recovery/bad")
	wantChecks(t, checks, "BAK01=finding", "BAK02=finding", "BAK03=passed", "BAK04=passed", "BAK05=skipped:Not judged: no full backup yet (BAK01).", "BAK06=passed", "DBC12=passed", "DBC08=passed")
	if want := time.Date(2026, 11, 3, 7, 21, 0, 0, time.UTC); c.AsOf == nil || !c.AsOf.Equal(want) {
		t.Errorf("asOf %v, want %v (server time in UTC)", c.AsOf, want)
	}
}

func TestBackupsCreatedToday(t *testing.T) {
	f := facts("SIMPLE")
	f.CreateDate = serverNow.Add(-3 * time.Hour)
	f.LastGoodCheckDb = at(serverNow.Add(-time.Hour))
	c, fs, _ := build(f)
	wantCard(t, c, StatusAmber, "1 thing to do", bak01Sentence, 1)
	wantFinding(t, findingOf(t, fs, "BAK01"), SeverityMedium, WhenOnline, EffortQuickWin, bak01Sentence)
	wantFacts(t, c, "Worst-case data loss=no backup/worse", "Last full backup=never/worse",
		"Last CHECKDB=today 08:21/normal", "Log backups=not needed (SIMPLE)/normal")
}

func TestBackupsFullWithoutLogBackups(t *testing.T) {
	f := facts("FULL")
	nightlyFulls(&f)
	c, fs, checks := build(f)
	wantCard(t, c, StatusAmber, "1 thing to do", bak02Never, 1, 0)
	b2 := findingOf(t, fs, "BAK02")
	wantFinding(t, b2, SeverityHigh, WhenOnline, EffortQuickWin, bak02Never)
	if b2.Evidence != "Log file size: 512 MB." || b2.Why != bak02Why || b2.DoThis != bak02Do {
		t.Errorf("BAK02 texts: %+v", b2)
	}
	b5 := findingOf(t, fs, "BAK05")
	wantFinding(t, b5, SeverityInfo, WhenNone, EffortNone,
		"You could lose up to 24 hours of data: the longest gap between backups in the last 7 days.")
	if b5.DoThis != bak05DoFull {
		t.Errorf("BAK05 do this in FULL: %q", b5.DoThis)
	}
	wantFacts(t, c, "Worst-case data loss=24 hours/normal", "Last full backup=1 day ago/normal",
		"Last CHECKDB=3 days ago/normal", "Log backups=none in FULL recovery/bad")
	wantChecks(t, checks, "BAK01=passed", "BAK02=finding", "BAK03=passed", "BAK04=passed", "BAK05=finding", "BAK06=passed", "DBC12=passed", "DBC08=passed")

	// A last log backup of 30 hours ago says so, in hours as DATEDIFF counts them.
	f.LastLog = at(serverNow.Add(-30*time.Hour - 30*time.Minute))
	_, fs, _ = build(f)
	if got := findingOf(t, fs, "BAK02").Finding; got != "Last log backup was 31 hours ago." {
		t.Errorf("stale log backup: %q", got)
	}
	// One within 24 hours: no BAK02.
	f.LastLog = at(serverNow.Add(-23 * time.Hour))
	_, fs, _ = build(f)
	wantNoFinding(t, fs, "BAK02")
}

func TestBackupsFullWith15MinuteLogs(t *testing.T) {
	f := facts("FULL")
	nightlyFulls(&f)
	logsEvery15(&f)
	c, fs, checks := build(f)
	verdict := "You could lose up to 15 minutes of data: the longest gap between backups in the last 7 days."
	wantCard(t, c, StatusGreen, "No problem found", verdict, 0)
	if len(fs) != 1 {
		t.Errorf("want only the BAK05 information, got %+v", fs)
	}
	b5 := findingOf(t, fs, "BAK05")
	wantFinding(t, b5, SeverityInfo, WhenNone, EffortNone, verdict)
	wantEvidence := "Backups in the last 7 days: 7 full, 0 differential, 672 log. Last full 2026-11-02 22:00, last log 2026-11-03 09:06. Recovery model FULL."
	if b5.Evidence != wantEvidence {
		t.Errorf("BAK05 evidence\n got %q\nwant %q", b5.Evidence, wantEvidence)
	}
	wantFacts(t, c, "Worst-case data loss=15 minutes/normal", "Last full backup=1 day ago/normal",
		"Last CHECKDB=3 days ago/normal", "Log backups=every 15 min on average/normal")
	wantChecks(t, checks, "BAK01=passed", "BAK02=passed", "BAK03=passed", "BAK04=passed", "BAK05=finding", "BAK06=passed", "DBC12=passed", "DBC08=passed")
}

func TestBackupsSimpleNightlyFull(t *testing.T) {
	f := facts("SIMPLE")
	nightlyFulls(&f)
	if w := WorstGapMinutes(f); w != 1440 {
		t.Fatalf("worst gap %d, want 1440", w)
	}
	c, fs, checks := build(f)
	verdict := "You could lose up to 24 hours of data: the longest gap between backups in the last 7 days."
	wantCard(t, c, StatusGreen, "No problem found", verdict, 0)
	b5 := findingOf(t, fs, "BAK05")
	wantFinding(t, b5, SeverityInfo, WhenNone, EffortNone, verdict) // 1440 is not more than 1500
	if b5.DoThis != bak05DoSimple {
		t.Errorf("BAK05 do this in SIMPLE: %q", b5.DoThis)
	}
	d := findingOf(t, fs, "DBC08")
	wantFinding(t, d, SeverityInfo, WhenDecideFirst, EffortModerate, "Database uses the SIMPLE recovery model.")
	if d.Evidence != "recovery_model = SIMPLE" {
		t.Errorf("DBC08 evidence %q", d.Evidence)
	}
	wantFacts(t, c, "Worst-case data loss=24 hours/normal", "Last full backup=1 day ago/normal",
		"Last CHECKDB=3 days ago/normal", "Log backups=not needed (SIMPLE)/normal")
	wantChecks(t, checks, "BAK01=passed", "BAK02=passed", "BAK03=passed", "BAK04=passed", "BAK05=finding", "BAK06=passed", "DBC12=passed", "DBC08=finding")
}

func TestBackupsWorstGapThreeDays(t *testing.T) {
	f := facts("SIMPLE")
	nightlyFulls(&f, 3, 4) // nothing between the nights 5 and 2 days ago
	c, fs, checks := build(f)
	sentence := "You could lose up to 3.0 days of data: the longest gap between backups in the last 7 days."
	wantCard(t, c, StatusAmber, "1 thing to do", sentence, 1)
	wantFinding(t, findingOf(t, fs, "BAK05"), SeverityHigh, WhenDecideFirst, EffortModerate, sentence)
	wantFacts(t, c, "Worst-case data loss=3.0 days/worse", "Last full backup=1 day ago/normal",
		"Last CHECKDB=3 days ago/normal", "Log backups=not needed (SIMPLE)/normal")
	wantChecks(t, checks, "BAK01=passed", "BAK02=passed", "BAK03=passed", "BAK04=passed", "BAK05=finding", "BAK06=passed", "DBC12=passed", "DBC08=finding")

	// A 26-hour gap is Medium, and the fact turns bad rather than worse.
	f = facts("SIMPLE")
	for _, d := range []int{1, 2, 3, 4, 5, 6, 7} {
		addFull(&f, night(d).Add(-time.Duration(d)*2*time.Hour)) // 22:00, 20:00, 18:00 ...: 26 hours apart
	}
	c, fs, _ = build(f)
	wantFinding(t, findingOf(t, fs, "BAK05"), SeverityMedium, WhenDecideFirst, EffortModerate,
		"You could lose up to 26 hours of data: the longest gap between backups in the last 7 days.")
	if c.Facts[0].Tone != ToneBad {
		t.Errorf("Medium gap tone %q", c.Facts[0].Tone)
	}
}

func TestBackupsNothingInSevenDays(t *testing.T) {
	f := facts("FULL")
	addFull(&f, serverNow.AddDate(0, 0, -10))
	f.LastLog = at(serverNow.AddDate(0, 0, -10))
	c, fs, _ := build(f)
	wantCard(t, c, StatusRed, "Needs attention today", bak05Lost7, 2, 1)
	wantFinding(t, findingOf(t, fs, "BAK05"), SeverityCritical, WhenDecideFirst, EffortModerate, bak05Lost7)
	wantFinding(t, findingOf(t, fs, "BAK02"), SeverityHigh, WhenOnline, EffortQuickWin, "Last log backup was 240 hours ago.")
	wantFacts(t, c, "Worst-case data loss=7.0 days/worse", "Last full backup=10 days ago/bad",
		"Last CHECKDB=3 days ago/normal", "Log backups=none in FULL recovery/bad")
}

func TestBackupsCheckDB(t *testing.T) {
	f := facts("SIMPLE")
	nightlyFulls(&f)

	f.LastGoodCheckDb = at(time.Date(1900, 1, 1, 0, 0, 0, 0, time.UTC))
	c, fs, checks := build(f)
	never := "No successful integrity check (DBCC CHECKDB) has ever been recorded."
	wantCard(t, c, StatusAmber, "1 thing to do", never, 1)
	b3 := findingOf(t, fs, "BAK03")
	wantFinding(t, b3, SeverityHigh, WhenOnline, EffortQuickWin, never)
	if b3.Evidence != "Last good CHECKDB: never." || b3.DoThis != bak03Do {
		t.Errorf("BAK03 never texts: %+v", b3)
	}
	if c.Facts[2] != (Fact{"Last CHECKDB", "never", ToneWorse}) {
		t.Errorf("never fact %+v", c.Facts[2])
	}
	wantChecks(t, checks, "BAK01=passed", "BAK02=passed", "BAK03=finding", "BAK04=passed", "BAK05=finding", "BAK06=passed", "DBC12=passed", "DBC08=finding")

	f.LastGoodCheckDb = at(serverNow.AddDate(0, 0, -20))
	c, fs, _ = build(f)
	b3 = findingOf(t, fs, "BAK03")
	wantFinding(t, b3, SeverityMedium, WhenOnline, EffortQuickWin, "Last successful integrity check was 20 days ago.")
	if b3.Evidence != "Last good CHECKDB: 2026-10-14 09:21." {
		t.Errorf("BAK03 evidence %q", b3.Evidence)
	}
	if c.Facts[2] != (Fact{"Last CHECKDB", "20 days ago", ToneBad}) {
		t.Errorf("20 days fact %+v", c.Facts[2])
	}

	f.LastGoodCheckDb = at(serverNow.AddDate(0, 0, -13))
	_, fs, _ = build(f)
	wantNoFinding(t, fs, "BAK03")

	f.LastGoodCheckDb = nil
	c, fs, checks = build(f)
	wantNoFinding(t, fs, "BAK03")
	wantCard(t, c, StatusGreen, "No problem found",
		"You could lose up to 24 hours of data: the longest gap between backups in the last 7 days.", 0)
	if c.Facts[2] != (Fact{"Last CHECKDB", "unknown on this version", ToneNormal}) {
		t.Errorf("unknown fact %+v", c.Facts[2])
	}
	wantChecks(t, checks, "BAK01=passed", "BAK02=passed", "BAK03=skipped:"+checkDbUnknownNote, "BAK04=passed", "BAK05=finding", "BAK06=passed", "DBC12=passed", "DBC08=finding")
	f.EngineVersion = "13.0.4001.0" // 2016 SP1: does not know
	if _, _, checks = build(f); checks[2].Note != checkDbUnknownNote {
		t.Errorf("2016 SP1 note %q", checks[2].Note)
	}

	// A version that knows the date but gave none for this database: say so, not "this version".
	for _, v := range []string{"13.0.5026.0", "16.0.1000.6"} {
		f.EngineVersion = v
		c, _, checks = build(f)
		if checks[2].Note != checkDbNotGiven || c.Facts[2] != (Fact{"Last CHECKDB", "unknown", ToneNormal}) {
			t.Errorf("%s: note %q fact %+v", v, checks[2].Note, c.Facts[2])
		}
	}
}

func TestBackupsSuspectPages(t *testing.T) {
	f := facts("FULL")
	nightlyFulls(&f)
	logsEvery15(&f)
	f.SuspectPages, f.SuspectLast = 2, at(time.Date(2026, 10, 30, 14, 5, 0, 0, time.UTC))
	c, fs, checks := build(f)
	sentence := "SQL Server has recorded 2 damaged (suspect) page(s)."
	wantCard(t, c, StatusRed, "Needs attention today", sentence, 1)
	b4 := findingOf(t, fs, "BAK04")
	wantFinding(t, b4, SeverityCritical, WhenNone, EffortNone, sentence)
	if b4.Evidence != "Most recent: 2026-10-30 14:05." || b4.DoThis != bak04Do {
		t.Errorf("BAK04 texts %+v", b4)
	}
	if c.Facts[3] != (Fact{"Damaged pages", "2", ToneWorse}) {
		t.Errorf("damaged pages fact %+v", c.Facts[3])
	}
	wantChecks(t, checks, "BAK01=passed", "BAK02=passed", "BAK03=passed", "BAK04=finding", "BAK05=finding", "BAK06=passed", "DBC12=passed", "DBC08=passed")
}

func TestBackupsAvailabilityGroupBump(t *testing.T) {
	f := facts("FULL")
	f.IsAgMember = true
	f.LastGoodCheckDb = at(time.Date(1900, 1, 1, 0, 0, 0, 0, time.UTC))
	c, fs, _ := build(f)
	b1 := findingOf(t, fs, "BAK01")
	wantFinding(t, b1, SeverityHigh, WhenOnline, EffortQuickWin, bak01Sentence) // Critical + 1
	if !strings.HasSuffix(b1.Evidence, " Database is in an Availability Group: backups may run on another replica.") {
		t.Errorf("BAK01 AG evidence %q", b1.Evidence)
	}
	b2 := findingOf(t, fs, "BAK02")
	wantFinding(t, b2, SeverityMedium, WhenOnline, EffortQuickWin, bak02Never) // High + 1
	if b2.Evidence != "Log file size: 512 MB. Database is in an Availability Group: log backups may run on another replica." {
		t.Errorf("BAK02 AG evidence %q", b2.Evidence)
	}
	b3 := findingOf(t, fs, "BAK03")
	wantFinding(t, b3, SeverityMedium, WhenOnline, EffortQuickWin, "No successful integrity check (DBCC CHECKDB) has ever been recorded.")
	if b3.Evidence != "Last good CHECKDB: never. Database is in an Availability Group: checks may run on another replica." {
		t.Errorf("BAK03 AG evidence %q", b3.Evidence)
	}
	// No Critical left: amber, and BAK01 (High) leads.
	wantCard(t, c, StatusAmber, "3 things to do", bak01Sentence, 3)

	// BAK05 is bumped only beyond 25 hours, and never past Info.
	f = facts("SIMPLE")
	f.IsAgMember = true
	nightlyFulls(&f)
	_, fs, _ = build(f)
	if b5 := findingOf(t, fs, "BAK05"); b5.Severity != SeverityInfo ||
		!strings.HasSuffix(b5.Evidence, "Recovery model SIMPLE. Availability Group: backups may run on another replica.") {
		t.Errorf("BAK05 AG, 24 hours: %+v", b5)
	}
	f = facts("SIMPLE")
	f.IsAgMember = true
	nightlyFulls(&f, 3, 4)
	_, fs, _ = build(f)
	if b5 := findingOf(t, fs, "BAK05"); b5.Severity != SeverityMedium {
		t.Errorf("BAK05 AG, 3 days: %s, want Medium (High + 1)", b5.Severity)
	}
	if agBump(5, true) != 5 || agBump(4, true) != 5 || agBump(1, false) != 1 {
		t.Error("agBump must add one for an AG member, never past 5")
	}
}

func TestBackupsMsdbUnreadable(t *testing.T) {
	f := facts("FULL")
	f.MsdbError = "The SELECT permission was denied on the object 'backupset', database 'msdb', schema 'dbo'."
	f.LastGoodCheckDb = at(serverNow.AddDate(0, 0, -20))
	c, fs, checks := build(f)
	note := "Could not read msdb backup history: " + f.MsdbError
	if c.Status != StatusGrey || c.StatusText != "Could not be read" || c.Note != note || c.Verdict != "" ||
		c.AsOf != nil || c.Facts == nil || len(c.Facts) != 0 || c.ToDo != 0 {
		t.Errorf("msdb unreadable card: %+v", c)
	}
	// What does not need msdb still ran.
	if len(fs) != 1 || fs[0].CheckID != "BAK03" {
		t.Errorf("findings without msdb: %+v", fs)
	}
	skip := "=skipped:" + note
	wantChecks(t, checks, "BAK01"+skip, "BAK02"+skip, "BAK03=finding", "BAK04"+skip, "BAK05"+skip, "BAK06"+skip, "DBC12=passed", "DBC08=passed")
}

func TestBackupsNotOnlineOrNotRead(t *testing.T) {
	f := facts("FULL")
	f.State = "RESTORING"
	c, fs, checks := build(f)
	note := "WarehouseLive is RESTORING, not online, so its backups were not checked."
	if c.Status != StatusGrey || c.StatusText != "Could not be read" || c.Note != note || c.AsOf != nil || len(fs) != 0 {
		t.Errorf("not online: %+v %+v", c, fs)
	}
	for _, s := range checks {
		if s.Status != CheckSkipped || s.Note != note {
			t.Errorf("not online check %+v", s)
		}
	}
	if len(checks) != 8 {
		t.Errorf("want eight checks, got %d", len(checks))
	}

	c, fs, _ = build(BackupFacts{Database: "WarehouseLive", Error: "cannot reach the server"})
	if c.Status != StatusGrey || c.Note != "cannot reach the server" || fs == nil || len(fs) != 0 {
		t.Errorf("not read: %+v", c)
	}
	b, _ := json.Marshal(CardDetail{Card: c, Findings: fs, Checks: []CheckStatus{}})
	if !strings.Contains(string(b), `"findings":[]`) || !strings.Contains(string(b), `"checks":[]`) || !strings.Contains(string(b), `"facts":[]`) {
		t.Errorf("detail JSON: %s", b)
	}
}

func TestRankFindings(t *testing.T) {
	f := facts("SIMPLE")
	f.CreateDate = serverNow.AddDate(0, -1, 0)
	f.LastGoodCheckDb = at(time.Date(1900, 1, 1, 0, 0, 0, 0, time.UTC))
	f.SuspectPages, f.SuspectLast = 1, at(serverNow)
	_, fs, _ := build(f)
	got := []string{}
	for _, x := range fs {
		got = append(got, x.CheckID+"/"+string(x.Severity))
	}
	// Critical BAK01 (quick win) before Critical BAK04 (no effort); then High BAK03; Info DBC08 last.
	if want := "BAK01/Critical BAK04/Critical BAK03/High DBC08/Info"; strings.Join(got, " ") != want {
		t.Errorf("ranked %q, want %q", strings.Join(got, " "), want)
	}

	fs = []Finding{
		{CheckID: "Z", Severity: SeverityHigh, Effort: EffortNone, When: WhenNone},
		{CheckID: "Y", Severity: SeverityHigh, Effort: EffortQuickWin, When: WhenDecideFirst},
		{CheckID: "X", Severity: SeverityHigh, Effort: EffortQuickWin, When: WhenOnline},
		{CheckID: "B", Severity: SeverityHigh, Effort: EffortQuickWin, When: WhenOnline},
		{CheckID: "A", Severity: SeverityInfo, Effort: EffortQuickWin, When: WhenOnline},
		{CheckID: "C", Severity: SeverityCritical, Effort: EffortProject, When: WhenWindow},
	}
	RankFindings(fs)
	got = got[:0]
	for _, x := range fs {
		got = append(got, x.CheckID)
	}
	if strings.Join(got, "") != "CBXYZA" {
		t.Errorf("ranked %q, want CBXYZA", strings.Join(got, ""))
	}
}

func TestSeverityAndWhenMapping(t *testing.T) {
	for rank, want := range map[int]Severity{0: SeverityCritical, 1: SeverityCritical, 2: SeverityHigh, 3: SeverityMedium,
		4: SeverityLow, 5: SeverityInfo, 6: SeverityInfo} {
		if got := SeverityFromRank(rank); got != want {
			t.Errorf("rank %d: %s, want %s", rank, got, want)
		}
	}
	for _, s := range []Severity{SeverityCritical, SeverityHigh, SeverityMedium, SeverityLow} {
		if !s.Actionable() {
			t.Errorf("%s should be actionable", s)
		}
	}
	if SeverityInfo.Actionable() {
		t.Error("Info is not actionable")
	}
	for ct, want := range map[string]string{"Online": WhenOnline, "Offline": WhenWindow, "Planned": WhenDecideFirst,
		"None": WhenNone, "": WhenNone} {
		if got := WhenFromChangeType(ct); got != want {
			t.Errorf("change type %q: %q, want %q", ct, got, want)
		}
	}
}

func TestFormatGap(t *testing.T) {
	for minutes, want := range map[int]string{0: "0 minutes", 15: "15 minutes", 119: "119 minutes", 120: "2 hours",
		149: "2 hours", 150: "3 hours", 1440: "24 hours", 1530: "26 hours", 2879: "48 hours", 2880: "2.0 days",
		3672: "2.6 days", 4320: "3.0 days", 10080: "7.0 days"} {
		if got := FormatGap(minutes); got != want {
			t.Errorf("%d minutes: %q, want %q", minutes, got, want)
		}
	}
}

func TestCheckCatalogue(t *testing.T) {
	ids := []string{}
	for _, c := range ChecksFor(CardBackups) {
		ids = append(ids, c.ID)
		// DBC12 (delayed durability) is ours to change; every other backup check is Client IT's.
		owner := OwnerClientIT
		if c.ID == "DBC12" {
			owner = OwnerUs
		}
		if c.Title == "" || c.Question == "" || c.Owner != owner || c.ThenCheck == "" {
			t.Errorf("incomplete check %+v", c)
		}
	}
	if strings.Join(ids, ",") != "BAK01,BAK02,BAK03,BAK04,BAK05,BAK06,DBC12,DBC08" {
		t.Errorf("backups checks %v", ids)
	}
}

func TestBuildCardsWithBackups(t *testing.T) {
	f := facts("SIMPLE")
	nightlyFulls(&f)
	cards := BuildCards(nil, &f, nil, nil, nil, nil, nil)
	if cards[0].ID != CardBackups || cards[0].Status != StatusGreen || cards[0].Verdict == "" || len(cards[0].Facts) != 4 {
		t.Errorf("live backups card: %+v", cards[0])
	}
	for _, c := range cards[1:] {
		if c.Status != StatusGrey || c.StatusText != "Not checked yet" {
			t.Errorf("grey card %s: %+v", c.ID, c)
		}
	}
	if def, ok := FindCardDef("backups"); !ok || def.Title != "Backups and data safety" {
		t.Errorf("FindCardDef backups: %+v %v", def, ok)
	}
	if _, ok := FindCardDef("nothing"); ok {
		t.Error("FindCardDef found an unknown card")
	}
}

func TestBackupsNulFulls(t *testing.T) {
	f := facts("FULL")
	nightlyFulls(&f)
	logsEvery15(&f)
	f.LastRealFull = f.LastFull
	f.NulFulls = []time.Time{night(20), night(12)}
	c, fs, checks := build(f)
	last := "2026-10-22 22:00"
	sentence := "2 full backup(s) in the last 30 days were written to NUL (thrown away), the last on " + last +
		": the differentials taken between it and the next real full backup cannot be restored."
	wantCard(t, c, StatusAmber, "1 thing to do", sentence, 1)
	b6 := findingOf(t, fs, "BAK06")
	wantFinding(t, b6, SeverityHigh, WhenOnline, EffortQuickWin, sentence)
	if b6.Why != bak06Why || b6.DoThis != bak06Do ||
		b6.Evidence != "Full backups to NUL in 30 days: 2, the last "+last+". Last real full backup: 2026-11-02 22:00. Differentials based on a NUL full are left out of the worst-case data loss (BAK05)." {
		t.Errorf("BAK06 texts %+v", b6)
	}
	wantChecks(t, checks, "BAK01=passed", "BAK02=passed", "BAK03=passed", "BAK04=passed", "BAK05=finding", "BAK06=finding", "DBC12=passed", "DBC08=passed")

	// The last full went to NUL: every differential since then is lost.
	f.NulFulls = []time.Time{night(1).Add(6 * time.Hour)}
	f.LastRealFull = at(night(1))
	_, fs, _ = build(f)
	if got := findingOf(t, fs, "BAK06").Finding; !strings.HasSuffix(got, "the last on 2026-11-03 04:00: every differential since then cannot be restored.") {
		t.Errorf("NUL after the last real full: %q", got)
	}
	f.LastRealFull = nil
	_, fs, _ = build(f)
	if e := findingOf(t, fs, "BAK06").Evidence; !strings.Contains(e, "Last real full backup: none.") {
		t.Errorf("no real full: %q", e)
	}

	// None: no finding; msdb unreadable: skipped with the other msdb checks.
	f.NulFulls = []time.Time{}
	_, fs, _ = build(f)
	wantNoFinding(t, fs, "BAK06")
	f.NulFulls, f.MsdbError = []time.Time{night(2)}, "denied"
	_, fs, checks = build(f)
	wantNoFinding(t, fs, "BAK06")
	if checks[5].CheckID != "BAK06" || checks[5].Status != CheckSkipped {
		t.Errorf("BAK06 without msdb: %+v", checks[5])
	}
}

func TestBackupsDelayedDurability(t *testing.T) {
	f := facts("FULL")
	nightlyFulls(&f)
	logsEvery15(&f)
	for _, c := range []struct {
		mode string
		want Severity
	}{{"DISABLED", ""}, {"", ""}, {"ALLOWED", SeverityLow}, {"FORCED", SeverityMedium}} {
		f.DelayedDurability = c.mode
		card, fs, checks := build(f)
		if c.want == "" {
			wantNoFinding(t, fs, "DBC12")
			if card.Status != StatusGreen || checks[6].Status != CheckPassed {
				t.Errorf("%q: %+v %+v", c.mode, card, checks[6])
			}
			continue
		}
		x := findingOf(t, fs, "DBC12")
		sentence := "WarehouseLive has delayed durability " + c.mode + ": a committed transaction can be lost if SQL Server crashes."
		if x.Severity != c.want || x.Finding != sentence || x.Owner != OwnerUs || x.When != WhenOnline || x.Effort != EffortQuickWin ||
			x.Why != dbc12Why || !strings.Contains(x.DoThis, "ALTER DATABASE [WarehouseLive] SET DELAYED_DURABILITY = DISABLED;") ||
			x.Evidence != "delayed_durability = "+c.mode || x.ThenCheck == "" {
			t.Errorf("%q: %+v", c.mode, x)
		}
		// Ours, not Client IT's: to do, but not for Client IT.
		if card.Status != StatusAmber || card.Verdict != sentence || card.ToDo != 1 || card.ForClientIT != 0 {
			t.Errorf("%q card %+v", c.mode, card)
		}
		if checks[6].CheckID != "DBC12" || checks[6].Status != CheckFinding {
			t.Errorf("%q check %+v", c.mode, checks[6])
		}
	}
	f.DelayedDurability, f.DurabilityError = "", "denied"
	if _, _, checks := build(f); checks[6].Status != CheckSkipped || checks[6].Note != "Could not read the delayed durability setting: denied" {
		t.Errorf("unreadable: %+v", checks[6])
	}
}

func TestBackupsNeverFullNamesRestore(t *testing.T) {
	f := facts("FULL")
	f.LastFull = nil
	r := f.ServerNow.AddDate(0, 0, -1)
	f.LastRestore = &r
	_, fs, _ := build(f)
	if x := findingOf(t, fs, "BAK01"); x.Finding != "No backup has been taken here since it was restored on "+r.Format("2 Jan 2006")+"." {
		t.Errorf("%q", x.Finding)
	}
}
