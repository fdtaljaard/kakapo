package domain

import (
	"strings"
	"testing"
)

func fnd(id string, sev Severity, text string) Finding {
	return Finding{CheckID: id, Severity: sev, Finding: text, Object: "obj " + id}
}

func flagIDs(fs []RedFlag) string {
	out := []string{}
	for _, f := range fs {
		out = append(out, f.CheckID)
	}
	return strings.Join(out, ",")
}

// Each listed check at High is a flag and at Medium is not; a High finding of an unlisted check is
// not; any Critical is.
func TestRedFlagsWhichFindings(t *testing.T) {
	listed := map[CardID][]string{
		CardPerformance: {"ACT01", "PRS01", "WRK06", "DLK01", "BLK01", "WRK05"},
		CardStorage:     {"STO01", "STO04", "STO11", "STO12", "TMP03"},
		CardBackups:     {"BAK01", "BAK02", "BAK05"},
		CardUpkeep:      {"MNT02"},
	}
	for card, ids := range listed {
		for _, id := range ids {
			if r := BuildRedFlags(map[CardID][]Finding{card: {fnd(id, SeverityHigh, "x")}}); len(r.Flags) != 1 || r.Flags[0].Card != card {
				t.Errorf("%s High is not a flag", id)
			}
			if r := BuildRedFlags(map[CardID][]Finding{card: {fnd(id, SeverityMedium, "x")}}); len(r.Flags) != 0 {
				t.Errorf("%s Medium is a flag", id)
			}
		}
	}
	for card, id := range map[CardID]string{CardPerformance: "QRY01", CardStorage: "STO07", CardSetup: "INS01", CardBackups: "BAK03", CardUpkeep: "MNT01"} {
		if r := BuildRedFlags(map[CardID][]Finding{card: {fnd(id, SeverityHigh, "x")}}); len(r.Flags) != 0 {
			t.Errorf("unlisted %s High is a flag", id)
		}
		if r := BuildRedFlags(map[CardID][]Finding{card: {fnd(id, SeverityCritical, "x")}}); len(r.Flags) != 1 {
			t.Errorf("%s Critical is not a flag", id)
		}
	}
	if r := BuildRedFlags(nil); len(r.Flags) != 0 || r.More != 0 || r.Rest == nil || r.Flags == nil {
		t.Errorf("none: %+v", r)
	}
}

// The order: Critical first, then the table's order, then the cards' order and their own rank; three
// shown and how many more; the sentence, the object and where the button goes.
func TestRedFlagsOrder(t *testing.T) {
	r := BuildRedFlags(map[CardID][]Finding{
		CardUpkeep:      {fnd("MNT02", SeverityHigh, "Agent job \"Backup\" failed 3 time(s) in the last 7 days.")},
		CardStorage:     {fnd("STO01", SeverityHigh, "L: has 3.1 GB free."), fnd("TMP03", SeverityHigh, "TempDB is 80% full.")},
		CardBackups:     {fnd("BAK04", SeverityCritical, "3 suspect pages.")},
		CardPerformance: {fnd("PRS01", SeverityCritical, "94% threads."), fnd("WRK06", SeverityHigh, "40 deadlocks a day."), fnd("ACT01", SeverityHigh, "Session 83 is blocking 14 session(s); the longest has waited 360 s.")},
	})
	if got := flagIDs(r.Flags) + "|" + flagIDs(r.Rest); got != "PRS01,BAK04,ACT01|WRK06,STO01,TMP03,MNT02" || r.More != 4 {
		t.Errorf("order %s, more %d", got, r.More)
	}
	act := r.Flags[2]
	if act.To != "blocking" || act.Sentence != "Session 83 is blocking 14 session(s); the longest has waited 360 s." || act.Object != "obj ACT01" ||
		act.Card != CardPerformance || act.Severity != SeverityHigh {
		t.Errorf("ACT01 flag %+v", act)
	}
	if r.Flags[1].To != "cards/backups" || r.Rest[1].To != "cards/storage" {
		t.Errorf("to: %s %s", r.Flags[1].To, r.Rest[1].To)
	}
	// Two findings of one check keep the card's own rank.
	two := BuildRedFlags(map[CardID][]Finding{CardStorage: {fnd("STO01", SeverityHigh, "first"), fnd("STO01", SeverityHigh, "second")}})
	if two.Flags[0].Sentence != "first" || two.Flags[1].Sentence != "second" {
		t.Errorf("own rank %+v", two.Flags)
	}
}

// The live ACT01 and PRS01 replace the overview's, and the rest is kept.
func TestRedFlagsWithLive(t *testing.T) {
	r := BuildRedFlags(map[CardID][]Finding{
		CardStorage:     {fnd("STO01", SeverityHigh, "disk"), fnd("STO11", SeverityHigh, "log")},
		CardBackups:     {fnd("BAK01", SeverityHigh, "no backup")},
		CardPerformance: {fnd("ACT01", SeverityHigh, "old block"), fnd("PRS01", SeverityHigh, "old pressure")},
	})
	if flagIDs(r.Flags) != "ACT01,PRS01,STO01" || r.More != 2 {
		t.Fatalf("overview %s %d", flagIDs(r.Flags), r.More)
	}
	// Blocking is gone and pressure is still there: the next one moves up.
	live := LiveRedFlags([]Finding{fnd("PRS01", SeverityHigh, "new pressure")})
	got := r.WithLive(live)
	if flagIDs(got.Flags)+"|"+flagIDs(got.Rest) != "PRS01,STO01,STO11|BAK01" || got.Flags[0].Sentence != "new pressure" {
		t.Errorf("with live %s|%s %+v", flagIDs(got.Flags), flagIDs(got.Rest), got.Flags[0])
	}
	// A new block comes first.
	got = r.WithLive(LiveRedFlags([]Finding{fnd("ACT01", SeverityHigh, "new block"), fnd("ACT01", SeverityMedium, "short")}))
	if flagIDs(got.Flags) != "ACT01,STO01,STO11" || got.Flags[0].Sentence != "new block" || got.More != 1 {
		t.Errorf("new block %s", flagIDs(got.Flags))
	}
}

// LiveFindings are the card's own ACT01 and PRS01: the same findings the card raises from the same facts.
func TestLiveFindingsAreTheCards(t *testing.T) {
	f := PerformanceFacts{Database: "Shop", State: "ONLINE", ServerName: "SQL01",
		Blockers:   []Blocker{{Session: 83, Blocked: 14, LongestSec: 360, Database: "Shop", Login: "app", Host: "APP01", Status: "sleeping"}},
		Schedulers: &SchedulerLoad{MaxWorkers: 576, CurrentWorkers: 545, OnlineSchedulers: 8},
		Grants:     &MemoryGrants{Holders: []GrantHolder{}}}
	live := LiveFindings(f)
	def, _ := FindCardDef(CardPerformance)
	_, card, _ := BuildPerformanceCard(f, def, nil, nil)
	want := []Finding{}
	for _, x := range card {
		if x.CheckID == "ACT01" || x.CheckID == "PRS01" {
			want = append(want, x)
		}
	}
	if len(live) != 2 || len(want) != 2 {
		t.Fatalf("live %d, card %d", len(live), len(want))
	}
	for i := range live {
		if live[i] != want[i] {
			t.Errorf("live %+v\ncard %+v", live[i], want[i])
		}
	}
	if live[0].CheckID != "PRS01" || live[0].Severity != SeverityCritical || live[1].CheckID != "ACT01" || live[1].Severity != SeverityHigh {
		t.Errorf("live %s %s, %s %s", live[0].CheckID, live[0].Severity, live[1].CheckID, live[1].Severity)
	}
}
