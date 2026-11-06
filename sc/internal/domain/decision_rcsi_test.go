package domain

import (
	"strings"
	"testing"
	"time"
)

var decNow = time.Date(2026, 11, 6, 10, 0, 0, 0, time.UTC)

func decFacts() *RowVersioningFacts {
	vs, db := int64(120*1024), int64(2048)
	adr := false
	return &RowVersioningFacts{Database: "Shop", ReadAt: decNow, ProductVersion: "16.0.1000.6", SnapshotIsolation: "OFF", ADR: &adr,
		CompatibilityLevel: 160, TempdbVersionStoreKB: &vs, DatabaseVersionKB: &db, Users: 4}
}

// decHistory is a history read now from source, with the two removable classes as given.
func decHistory(source string, writes, reads BlockingClass) *BlockingHistory {
	writes.Class, reads.Class = WaitWritesBehindReads, WaitReadsBehindWrites
	h := &BlockingHistory{CapturedAt: decNow.Add(-time.Hour), Days: 7, Source: source, ThresholdSeconds: 5,
		Classes: []BlockingClass{writes, reads, {Class: WaitLockingReaders}, {Class: WaitSchemaLocks}, {Class: WaitWritesBehindWrites, Episodes: 40, TotalWaitSec: 900},
			{Class: WaitUnknown, Episodes: 1}}}
	return h
}

func decInputs(h *BlockingHistory) RowVersioningInputs {
	return RowVersioningInputs{Database: "Shop", Now: decNow, Facts: decFacts(), Blocking: h, Helper: &XESession{Exists: true}}
}

func decDeadlocks(withReader int) *DeadlockReport {
	r := &DeadlockReport{CapturedAt: decNow.Add(-time.Hour), Days: 7, Total: withReader + 1, Source: DeadlockSourceFile,
		Pairs: []DeadlockPair{{Procedures: [2]string{"dbo.A", "dbo.B"}, Count: withReader + 1, WithReader: withReader}}}
	r.CountReaders()
	return r
}

func TestRowVersioningVerdicts(t *testing.T) {
	two := BlockingClass{Episodes: 2, TotalWaitSec: 30, LongestWaitSec: 20, MostAtOnce: 1, Days: 1}
	one := BlockingClass{Episodes: 1, TotalWaitSec: 12, LongestWaitSec: 12, MostAtOnce: 1, Days: 1}
	open := func(min int) *OpenTransaction {
		return &OpenTransaction{Session: 57, Program: "SSMS", Host: "DESK1", Status: "sleeping", Minutes: min}
	}
	for _, c := range []struct {
		name    string
		in      func() RowVersioningInputs
		verdict string
		tag     string
		has     string
		actions string // the actions' fix ids or pages
	}{
		{"on", func() RowVersioningInputs {
			in := decInputs(decHistory(BlockSourceKakapo, two, two))
			in.Facts.RCSI = true
			in.Facts.Oldest = open(45)
			return in
		}, DecisionOn, "On", "Readers do not wait for writers here. What still blocks is writers against writers or a lock hint. The version store in tempdb holds 120.0 MB now, 2.0 MB of it from this database.", ""},
		{"on with TMP03", func() RowVersioningInputs {
			in := decInputs(nil)
			in.Facts.RCSI = true
			in.TMP03 = &Finding{CheckID: "TMP03", Finding: "TempDB is 70% full."}
			return in
		}, DecisionOn, "On", "", "cards/storage"},
		{"fix first at 30 minutes", func() RowVersioningInputs {
			in := decInputs(decHistory(BlockSourceKakapo, two, two))
			in.Facts.Oldest = open(30)
			return in
		}, DecisionFixFirst, "Fix first", "Before deciding on row versioning, deal with the transaction session 57 has kept open here for 30 minutes (ACT02).", "blocking"},
		{"not at 29 minutes", func() RowVersioningInputs {
			in := decInputs(decHistory(BlockSourceKakapo, two, one))
			in.Facts.Oldest = open(29)
			return in
		}, DecisionWorthConsidering, "Worth considering", "", "rcsi"},
		{"fix first: TempDB cannot hold more", func() RowVersioningInputs {
			in := decInputs(decHistory(BlockSourceKakapo, two, one))
			in.Tempdb = &TempdbRoom{AllocMB: 8000, FreeMB: 100, NoRoom: true}
			in.TMP03 = &Finding{CheckID: "TMP03", Finding: "TempDB is 98% full and its files cannot grow."}
			return in
		}, DecisionFixFirst, "Fix first", "make room in TempDB (TMP03)", "cards/storage"},
		{"TempDB does not matter with ADR", func() RowVersioningInputs {
			in := decInputs(decHistory(BlockSourceKakapo, two, one))
			adr := true
			in.Facts.ADR = &adr
			in.Tempdb = &TempdbRoom{AllocMB: 8000, FreeMB: 100, NoRoom: true}
			return in
		}, DecisionWorthConsidering, "Worth considering", "", "rcsi"},
		{"no history", func() RowVersioningInputs { return decInputs(nil) }, DecisionNotEnoughEvidence, "Not enough evidence",
			"the blocking history has not been read in the last 7 days.", "cards/performance"},
		{"no history, no session", func() RowVersioningInputs {
			in := decInputs(nil)
			in.Helper = &XESession{}
			return in
		}, DecisionNotEnoughEvidence, "Not enough evidence", "", FixInstallXESession},
		{"history older than 7 days", func() RowVersioningInputs {
			h := decHistory(BlockSourceKakapo, two, two)
			h.CapturedAt = decNow.AddDate(0, 0, -8)
			return decInputs(h)
		}, DecisionNotEnoughEvidence, "Not enough evidence", "has not been read in the last 7 days", "cards/performance"},
		{"history without classes", func() RowVersioningInputs {
			h := decHistory(BlockSourceKakapo, two, two)
			h.Classes = nil
			return decInputs(h)
		}, DecisionNotEnoughEvidence, "Not enough evidence", "Read the blocking history again to tell readers from writers.", "cards/performance"},
		{"system_health only with nothing", func() RowVersioningInputs {
			in := decInputs(decHistory(DeadlockSourceFile, one, BlockingClass{}))
			in.Helper = nil
			return in
		}, DecisionNotEnoughEvidence, "Not enough evidence", "only system_health's five-minute snapshots", FixInstallXESession},
		{"worth considering at 2 episodes", func() RowVersioningInputs { return decInputs(decHistory(BlockSourceKakapo, one, one)) },
			DecisionWorthConsidering, "Worth considering", "Writes waited 12 s for reads and reads 12 s for writes over 7 days; READ COMMITTED SNAPSHOT would remove those waits.", "rcsi"},
		{"strongly at 10 episodes", func() RowVersioningInputs {
			return decInputs(decHistory(BlockSourceKakapo, BlockingClass{Episodes: 10, TotalWaitSec: 100, LongestWaitSec: 10}, BlockingClass{}))
		}, DecisionWorthConsidering, "Strongly worth considering", "", "rcsi"},
		{"strongly at a 60 s wait", func() RowVersioningInputs {
			return decInputs(decHistory(BlockSourceKakapo, BlockingClass{}, BlockingClass{Episodes: 2, TotalWaitSec: 70, LongestWaitSec: 60}))
		}, DecisionWorthConsidering, "Strongly worth considering", "", "rcsi"},
		{"strongly at 10 sessions at once", func() RowVersioningInputs {
			return decInputs(decHistory(BlockSourceKakapo, BlockingClass{}, BlockingClass{Episodes: 10, TotalWaitSec: 70, LongestWaitSec: 9, MostAtOnce: 10}))
		}, DecisionWorthConsidering, "Strongly worth considering", "", "rcsi"},
		{"amber with system_health only, even at High", func() RowVersioningInputs {
			in := decInputs(decHistory(DeadlockSourceFile, BlockingClass{Episodes: 12, TotalWaitSec: 4200, LongestWaitSec: 600}, BlockingClass{}))
			in.Helper = &XESession{}
			return in
		}, DecisionWorthConsidering, "Worth considering", "kakapo's event session would show every wait over 5 seconds.", "rcsi," + FixInstallXESession},
		{"by deadlocks with a reader at 3", func() RowVersioningInputs {
			in := decInputs(decHistory(BlockSourceKakapo, BlockingClass{}, BlockingClass{}))
			in.Deadlocks = decDeadlocks(3)
			return in
		}, DecisionWorthConsidering, "Worth considering", "3 of 4 deadlocks in 7 days had a reader in them; READ COMMITTED SNAPSHOT would remove those deadlocks.", "rcsi"},
		{"strongly by deadlocks with a reader at 10", func() RowVersioningInputs {
			in := decInputs(decHistory(BlockSourceKakapo, BlockingClass{}, BlockingClass{}))
			in.Deadlocks = decDeadlocks(10)
			return in
		}, DecisionWorthConsidering, "Strongly worth considering", "", "rcsi"},
		{"2 deadlocks with a reader are not enough", func() RowVersioningInputs {
			in := decInputs(decHistory(BlockSourceKakapo, BlockingClass{}, BlockingClass{}))
			in.Deadlocks = decDeadlocks(2)
			return in
		}, DecisionNotNeeded, "Not needed now", "0 writes behind reads and 0 reads behind writes in 7 days, 2 deadlocks with a reader.", ""},
		{"writes behind writes alone never", func() RowVersioningInputs { return decInputs(decHistory(BlockSourceKakapo, one, BlockingClass{})) },
			DecisionNotNeeded, "Not needed now", "Readers and writers did not wait for each other enough to change this: 1 write behind reads and 0 reads behind writes in 7 days.", ""},
		{"facts not read", func() RowVersioningInputs {
			in := decInputs(nil)
			in.Facts, in.FactsError = nil, "login failed"
			return in
		}, DecisionNotEnoughEvidence, "Not enough evidence", "could not be read (login failed)", ""},
	} {
		d := BuildRowVersioningDecision(c.in())
		acts := []string{}
		for _, a := range d.Actions {
			if a.FixID != "" {
				acts = append(acts, a.FixID)
			} else {
				acts = append(acts, a.To)
			}
		}
		if d.Verdict != c.verdict || d.Tag != c.tag || !strings.Contains(d.Sentence, c.has) || strings.Join(acts, ",") != c.actions {
			t.Errorf("%s: %s %q %q %v", c.name, d.Verdict, d.Tag, d.Sentence, acts)
		}
		if d.CheckID != "DEC01" || d.Title != "Row versioning (READ COMMITTED SNAPSHOT)" || d.Decision != DecisionRowVersioning {
			t.Errorf("%s: catalogue %+v", c.name, d)
		}
	}
}

// What kakapo found, what it costs and the facts.
func TestRowVersioningLines(t *testing.T) {
	writes := BlockingClass{Episodes: 3, TotalWaitSec: 7 * 4200, LongestWaitSec: 95, MostAtOnce: 2, Days: 5,
		Waiters: []BlockingParty{{Program: "Scanner", Login: "picker", Host: "HH-01"}}, Blockers: []BlockingParty{{Program: "Report Server", Host: "RPT01"}}}
	h := decHistory(BlockSourceKakapo, writes, BlockingClass{})
	in := decInputs(h)
	in.Deadlocks = decDeadlocks(1)
	in.Facts.Oldest = &OpenTransaction{Session: 57, Program: "SSMS", Host: "DESK1", Status: "sleeping", Minutes: 6}
	in.Tempdb = &TempdbRoom{AllocMB: 8000, FreeMB: 6000, CanGrow: true}
	si := in.Facts
	si.SnapshotIsolation = "ON"
	d := BuildRowVersioningDecision(in)
	found := map[string]DecisionLine{}
	for _, l := range d.Found {
		found[l.Label] = l
	}
	if l := found["Writes waiting for reads"]; l.Text != "1 h 10 min a day on average, on 5 of 7 days; longest 1 min 35 s (3 waits). Waiting: Scanner (picker, HH-01). Blocking: Report Server (RPT01)." ||
		l.Window != "last 7 days, kakapo's event session" {
		t.Errorf("writes line %+v", l)
	}
	if l := found["Reads waiting for writes"]; l.Text != "None in 7 days." {
		t.Errorf("reads line %+v", l)
	}
	if l := found["Not removed by RCSI"]; l.Text != "Writes waiting for writes 40 (15 min), readers that keep their locks 0 (0 s), schema locks 0 (0 s)." {
		t.Errorf("not removed %+v", l)
	}
	if l := found["Deadlocks with a reader"]; l.Text != "1 of 2 deadlocks in 7 days had a reader in them; the most: dbo.A / dbo.B (1)." {
		t.Errorf("deadlocks %+v", l)
	}
	if l := found["Evidence"]; !strings.Contains(l.Text, "blocked process threshold 5 s; 1 wait kakapo could not place") {
		t.Errorf("evidence %+v", l)
	}
	costs := map[string]string{}
	for _, l := range d.Costs {
		costs[l.Label] = l.Text
	}
	if costs["Version store"] != "120.0 MB in tempdb now, 2.0 MB of it from this database." ||
		costs["Longest open transaction here"] != "Session 57 (SSMS on DESK1, sleeping) has had a transaction open in Shop for 6 minutes." ||
		costs["Other sessions in the database"] != "4 now; WITH ROLLBACK IMMEDIATE disconnects them for a moment when the setting changes." ||
		costs["TempDB"] != "8,000 MB, 6,000 MB free, can grow." {
		t.Errorf("costs %+v", costs)
	}
	if !strings.Contains(d.NotMeasured, "about twice the version data written per minute times the longest transaction in minutes; the longest here now is 6 minutes.") {
		t.Errorf("not measured %q", d.NotMeasured)
	}
	if len(d.Facts) != 1 || d.Facts[0].Label != "Snapshot isolation is on" || !strings.Contains(d.Facts[0].Text, "every change here is already versioned") {
		t.Errorf("facts %+v", d.Facts)
	}
	if d.Evidence != "Writes waited 8 h 10 min for reads and reads 0 s for writes over 7 days; READ COMMITTED SNAPSHOT would remove those waits." {
		t.Errorf("evidence line %q", d.Evidence)
	}

	// ADR: the persistent version store, and the fact.
	adr, pvs := true, int64(5120)
	in.Facts.ADR, in.Facts.PVSKB = &adr, &pvs
	in.Facts.RCSI = true
	d = BuildRowVersioningDecision(in)
	if !strings.HasSuffix(d.Sentence, "Row versions are kept in this database's persistent version store (ADR): 5.0 MB now.") {
		t.Errorf("on with ADR %q", d.Sentence)
	}
	if d.Facts[len(d.Facts)-1].Label != "Accelerated database recovery is on" {
		t.Errorf("ADR fact %+v", d.Facts)
	}
}

func TestDecisionRecords(t *testing.T) {
	today := time.Date(2026, 11, 6, 15, 0, 0, 0, time.UTC)
	r, err := ParseDecisionInput(DecisionInput{Choice: "keep", Verdict: DecisionWorthConsidering, Reason: " waiting for the vendor "}, today)
	if err != nil || r.ReviewOn != "2027-02-04" || r.Reason != "waiting for the vendor" || r.Decision != DecisionRowVersioning {
		t.Errorf("keep %+v %v", r, err)
	}
	if r, err := ParseDecisionInput(DecisionInput{Choice: "later", Verdict: DecisionNotEnoughEvidence, ReviewOn: "2026-12-01"}, today); err != nil || r.ReviewOn != "2026-12-01" {
		t.Errorf("later %+v %v", r, err)
	}
	for _, bad := range []DecisionInput{
		{Choice: "keep", Verdict: DecisionOn},
		{Choice: "change", Verdict: DecisionOn, Reason: "x"},
		{Choice: "maybe", Verdict: DecisionOn, Reason: "x"},
		{Choice: "later", Verdict: "great"},
		{Choice: "later", Verdict: DecisionOn, ReviewOn: "next week"},
	} {
		if _, err := ParseDecisionInput(bad, today); err == nil {
			t.Errorf("%+v accepted", bad)
		}
	}
	change := int64(12)
	at := time.Date(2026, 11, 6, 9, 0, 0, 0, time.UTC)
	for _, c := range []struct {
		r    DecisionRecord
		want string
	}{
		{DecisionRecord{Choice: ChoiceKeep, Verdict: DecisionWorthConsidering, Reason: "waiting for the vendor", ReviewOn: "2027-02-04", DecidedAt: at},
			"Kept off on 2026-11-06: waiting for the vendor; review by 2027-02-04"},
		{DecisionRecord{Choice: ChoiceKeep, Verdict: DecisionOn, Reason: "fine", ReviewOn: "2027-02-04", DecidedAt: at}, "Kept on on 2026-11-06: fine; review by 2027-02-04"},
		{DecisionRecord{Choice: ChoiceLater, Verdict: DecisionNotEnoughEvidence, ReviewOn: "2026-12-01", DecidedAt: at}, "Left for later on 2026-11-06; review by 2026-12-01"},
		{DecisionRecord{Choice: ChoiceChange, Verdict: DecisionWorthConsidering, ChangeID: &change, DecidedAt: at}, "Turned on on 2026-11-06 (change 12)"},
	} {
		if got := RecordLine(c.r); got != c.want {
			t.Errorf("record line %q, want %q", got, c.want)
		}
	}
	// The row shows the latest record; one whose review date has passed is due.
	in := decInputs(nil)
	in.Records = []DecisionRecord{
		{Choice: ChoiceKeep, Verdict: DecisionNotEnoughEvidence, Reason: "vendor", ReviewOn: "2026-11-05", DecidedAt: at.AddDate(0, -3, 0)},
		{Choice: ChoiceLater, Verdict: DecisionNotEnoughEvidence, ReviewOn: "2026-10-01", DecidedAt: at.AddDate(0, -4, 0)},
	}
	d := BuildRowVersioningDecision(in)
	if d.Record == nil || d.Record.Choice != ChoiceKeep || !d.ReviewDue || !strings.HasPrefix(d.RecordLine, "Kept off on 2026-08-06: vendor") || len(d.History) != 2 ||
		!d.History[1].ReviewDue {
		t.Errorf("latest record %+v", d)
	}
	in.Records[0].ReviewOn = "2026-11-06"
	if d := BuildRowVersioningDecision(in); d.ReviewDue {
		t.Error("due only once the date has passed")
	}
}

// The rcsi preview with the decision: the open transaction first from ACT02's 5 minutes, the evidence
// line, the cost lines, the warnings; Apply held back from 30 minutes; ADR's line in place of tempdb's.
func TestRCSIPreviewWithDecision(t *testing.T) {
	in := decInputs(decHistory(BlockSourceKakapo, BlockingClass{Episodes: 1, TotalWaitSec: 4200, LongestWaitSec: 4200}, BlockingClass{Episodes: 1, TotalWaitSec: 60}))
	in.Facts.Oldest = &OpenTransaction{Session: 57, Program: "SSMS", Host: "DESK1", Status: "sleeping", Minutes: 5}
	d := BuildRowVersioningDecision(in)
	p, _, err := BuildRCSIFix(RCSIFactsOf(*in.Facts), &d)
	if err != nil {
		t.Fatal(err)
	}
	if p.Warnings[0] != "Session 57 (SSMS on DESK1, sleeping) has had a transaction open in Shop for 5 minutes: WITH ROLLBACK IMMEDIATE would roll it back. Find out what it is first (ACT02)." ||
		p.Warnings[1] != "Writes waited 1 h 10 min for reads and reads 1 min for writes over 7 days; READ COMMITTED SNAPSHOT would remove those waits." ||
		p.Warnings[2] != "Version store: 120.0 MB in tempdb now, 2.0 MB of it from this database." || p.Held != "" {
		t.Errorf("warnings %q held %q", p.Warnings, p.Held)
	}
	in.Facts.Oldest.Minutes = 4
	if p, _, _ := BuildRCSIFix(RCSIFactsOf(*in.Facts), &d); strings.Contains(p.Warnings[0], "Session 57") {
		t.Errorf("under ACT02's 5 minutes it is not named first: %q", p.Warnings[0])
	}
	in.Facts.Oldest.Minutes = 30
	d = BuildRowVersioningDecision(in)
	p, _, _ = BuildRCSIFix(RCSIFactsOf(*in.Facts), &d)
	if p.Held != "Apply is held back: session 57 (SSMS on DESK1) has had a transaction open in Shop for 30 minutes. Deal with it first (ACT02)." ||
		p.Statement == "" || p.Warnings[1] != "Writes waited 1 h 10 min for reads and reads 1 min for writes over 7 days; READ COMMITTED SNAPSHOT would remove those waits." {
		t.Errorf("held %q statement %q warnings %q", p.Held, p.Statement, p.Warnings[:2])
	}
	// Not Worth considering: no evidence. ADR: the versions stay in the database, no 14-byte line, no tempdb room.
	adr := true
	f := RCSIFactsOf(*decFacts())
	f.ADR = adr
	nd := BuildRowVersioningDecision(decInputs(nil))
	p, _, _ = BuildRCSIFix(f, &nd)
	all := strings.Join(p.Warnings, "\n")
	if p.Warnings[0] != "kakapo has no evidence that readers and writers wait for each other here." || !strings.Contains(all, rcsiWarnADR) ||
		strings.Contains(all, "14 bytes") || strings.Contains(all, rcsiWarnTempdb) || len(p.Needs) != 0 || !strings.Contains(all, rcsiWarnReaders) {
		t.Errorf("ADR preview %q", p.Warnings)
	}
}
