package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"sqlmonitor/internal/domain"
)

func TestInsertCardRead(t *testing.T) {
	ctx := context.Background()
	st, err := Open(ctx, filepath.Join(t.TempDir(), "test.duckdb"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	card := domain.Card{ID: domain.CardBackups, Status: domain.StatusAmber, StatusText: "1 thing to do",
		Verdict: "Database uses FULL recovery but its log has never been backed up.", ToDo: 1}
	findings := []domain.Finding{
		{CheckID: "BAK02", Severity: domain.SeverityHigh, Owner: domain.OwnerClientIT, When: domain.WhenOnline,
			Effort: domain.EffortQuickWin, Object: "WarehouseLive", Finding: card.Verdict, Why: "w", DoThis: "d",
			ThenCheck: "t", Evidence: "Log file size: 512 MB."},
		{CheckID: "BAK05", Severity: domain.SeverityInfo, Owner: domain.OwnerClientIT, When: domain.WhenNone,
			Effort: domain.EffortNone, Object: "WarehouseLive", Finding: "You could lose up to 24 hours of data."},
	}
	checks := []domain.CheckStatus{{CheckID: "BAK02", Status: domain.CheckFinding},
		{CheckID: "BAK03", Status: domain.CheckSkipped, Note: "not on this version"}}

	for want := int64(1); want <= 2; want++ {
		id, err := st.InsertCardRead(ctx, 7, "WarehouseLive", card, findings, checks)
		if err != nil {
			t.Fatal(err)
		}
		if id != want {
			t.Errorf("card read id %d, want %d", id, want)
		}
	}

	var status, verdict, cardID string
	var toDo int
	var at time.Time
	if err := st.db.QueryRowContext(ctx, `SELECT card, status, verdict, to_do, captured_at FROM card_read WHERE id = 2`).
		Scan(&cardID, &status, &verdict, &toDo, &at); err != nil {
		t.Fatal(err)
	}
	if cardID != "backups" || status != "amber" || verdict != card.Verdict || toDo != 1 ||
		time.Since(asUTC(at)) > time.Minute || time.Since(asUTC(at)) < 0 {
		t.Errorf("card_read: %s %s %q %d %v (want UTC now)", cardID, status, verdict, toDo, at)
	}
	var n int
	if err := st.db.QueryRowContext(ctx, `SELECT count(*) FROM finding WHERE card_read_id = 2`).Scan(&n); err != nil || n != 2 {
		t.Errorf("findings of read 2: %d %v", n, err)
	}
	var when, evidence string
	if err := st.db.QueryRowContext(ctx, `SELECT when_, evidence FROM finding WHERE card_read_id = 1 AND check_id = 'BAK02'`).
		Scan(&when, &evidence); err != nil || when != "Online" || evidence != "Log file size: 512 MB." {
		t.Errorf("finding row: %q %q %v", when, evidence, err)
	}
	var note *string
	if err := st.db.QueryRowContext(ctx, `SELECT note FROM check_status WHERE card_read_id = 1 AND check_id = 'BAK02'`).Scan(&note); err != nil || note != nil {
		t.Errorf("passed check note should be NULL: %v %v", note, err)
	}
	if err := st.db.QueryRowContext(ctx, `SELECT count(*) FROM check_status`).Scan(&n); err != nil || n != 4 {
		t.Errorf("check statuses: %d %v", n, err)
	}

	// A grey read with no findings is kept too.
	grey := domain.Card{ID: domain.CardBackups, Status: domain.StatusGrey, StatusText: "Could not be read"}
	if _, err := st.InsertCardRead(ctx, 7, "Archive", grey, []domain.Finding{}, nil); err != nil {
		t.Fatal(err)
	}
	if err := st.db.QueryRowContext(ctx, `SELECT count(*) FROM card_read`).Scan(&n); err != nil || n != 3 {
		t.Errorf("card reads: %d %v", n, err)
	}
}

// LastFindings reads one check's findings from the latest read of a card (docs/phase5o-contract.md,
// section 5): an older read and other cards are not read.
func TestLastFindings(t *testing.T) {
	ctx := context.Background()
	st, err := Open(ctx, filepath.Join(t.TempDir(), "test.duckdb"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if fs, err := st.LastFindings(ctx, 7, "Shop", domain.CardDesign, "IDX04"); err != nil || len(fs) != 0 {
		t.Fatalf("never read: %+v %v", fs, err)
	}
	card := domain.Card{ID: domain.CardDesign, Status: domain.StatusAmber}
	old := domain.Finding{CheckID: "IDX04", Severity: domain.SeverityLow, Object: "[dbo].[Old]", Evidence: "Safe to disable: [IX_O] (kept in [IX_P])."}
	if _, err := st.InsertCardRead(ctx, 7, "Shop", card, []domain.Finding{old}, nil); err != nil {
		t.Fatal(err)
	}
	time.Sleep(2 * time.Millisecond)
	now := []domain.Finding{
		{CheckID: "IDX14", Severity: domain.SeverityLow, Object: "[dbo].[Lines]"},
		{CheckID: "IDX04", Severity: domain.SeverityMedium, Owner: domain.OwnerUs, When: domain.WhenOnline, Effort: domain.EffortQuickWin,
			Object: "[dbo].[Orders]", Finding: "IX_B is a copy of IX_A.", Evidence: "Safe to disable: [IX_B] (kept in [IX_A])."},
	}
	if _, err := st.InsertCardRead(ctx, 7, "Shop", card, now, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := st.InsertCardRead(ctx, 7, "Shop", domain.Card{ID: domain.CardStorage, Status: domain.StatusGreen}, nil, nil); err != nil {
		t.Fatal(err)
	}
	fs, err := st.LastFindings(ctx, 7, "Shop", domain.CardDesign, "IDX04")
	if err != nil || len(fs) != 1 || fs[0].Object != "[dbo].[Orders]" || fs[0].Severity != domain.SeverityMedium || fs[0].Effort != domain.EffortQuickWin ||
		fs[0].Evidence != "Safe to disable: [IX_B] (kept in [IX_A])." || fs[0].Finding != "IX_B is a copy of IX_A." {
		t.Errorf("last IDX04 findings %+v %v", fs, err)
	}
	if fs, _ := st.LastFindings(ctx, 8, "Shop", domain.CardDesign, "IDX04"); len(fs) != 0 {
		t.Errorf("another server's findings %+v", fs)
	}
}
