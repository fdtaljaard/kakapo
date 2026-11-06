package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"sqlmonitor/internal/domain"
)

// Migration 12 keeps every decision record; the latest comes first, one per choice, with its review date
// and the change that applied it.
func TestDecisionRecords(t *testing.T) {
	ctx := context.Background()
	st, err := Open(ctx, filepath.Join(t.TempDir(), "test.duckdb"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	if rs, err := st.ListDecisions(ctx, 1, "WarehouseLive", domain.DecisionRowVersioning); err != nil || len(rs) != 0 {
		t.Fatalf("none yet: %+v %v", rs, err)
	}
	at := time.Date(2026, 11, 6, 9, 0, 0, 0, time.UTC)
	change := int64(12)
	for i, r := range []domain.DecisionRecord{
		{Choice: domain.ChoiceKeep, Verdict: domain.DecisionWorthConsidering, Reason: "waiting for the vendor", ReviewOn: "2027-02-04"},
		{Choice: domain.ChoiceLater, Verdict: domain.DecisionNotEnoughEvidence, ReviewOn: "2027-02-05"},
		{Choice: domain.ChoiceChange, Verdict: domain.DecisionWorthConsidering, ChangeID: &change},
	} {
		r.ServerID, r.Database, r.Decision, r.DecidedBy = 1, "WarehouseLive", domain.DecisionRowVersioning, `CORP\consultant`
		r.DecidedAt = at.Add(time.Duration(i) * time.Hour)
		got, err := st.InsertDecision(ctx, r)
		if err != nil || got.ID == 0 {
			t.Fatalf("insert %d: %+v %v", i, got, err)
		}
	}
	if _, err := st.InsertDecision(ctx, domain.DecisionRecord{ServerID: 2, Database: "WarehouseLive", Decision: domain.DecisionRowVersioning,
		DecidedAt: at, DecidedBy: "x", Choice: domain.ChoiceKeep, Verdict: domain.DecisionOn, Reason: "other server"}); err != nil {
		t.Fatal(err)
	}
	rs, err := st.ListDecisions(ctx, 1, "WarehouseLive", domain.DecisionRowVersioning)
	if err != nil || len(rs) != 3 {
		t.Fatalf("records %+v %v", rs, err)
	}
	if rs[0].Choice != domain.ChoiceChange || rs[0].ChangeID == nil || *rs[0].ChangeID != 12 || rs[0].ReviewOn != "" || rs[0].Reason != "" {
		t.Errorf("latest %+v", rs[0])
	}
	if rs[1].Choice != domain.ChoiceLater || rs[1].ReviewOn != "2027-02-05" || rs[1].ChangeID != nil {
		t.Errorf("second %+v", rs[1])
	}
	if k := rs[2]; k.Choice != domain.ChoiceKeep || k.Reason != "waiting for the vendor" || k.ReviewOn != "2027-02-04" ||
		k.DecidedBy != `CORP\consultant` || !k.DecidedAt.Equal(at) || k.Verdict != domain.DecisionWorthConsidering {
		t.Errorf("oldest %+v", k)
	}
	if _, err := st.InsertDecision(ctx, domain.DecisionRecord{ReviewOn: "soon"}); err == nil {
		t.Error("a review date that is not a date is refused")
	}
}
