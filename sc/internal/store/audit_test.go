package store

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"sqlmonitor/internal/domain"
)

// Migration 14 creates the three tables and seeds action_log with kakapo's own changes only: one copied row
// per change that is neither recorded by hand nor seen on the server.
func TestMigration14SeedsActionLog(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "old.duckdb")
	raw, err := sql.Open("duckdb", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := raw.ExecContext(ctx, `CREATE TABLE schema_version (version INTEGER NOT NULL, applied_at TIMESTAMP NOT NULL DEFAULT current_timestamp)`); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 13; i++ {
		if _, err := raw.ExecContext(ctx, migrations[i]); err != nil {
			t.Fatalf("migration %d: %v", i+1, err)
		}
		if _, err := raw.ExecContext(ctx, `INSERT INTO schema_version (version) VALUES (?)`, i+1); err != nil {
			t.Fatal(err)
		}
	}
	at := time.Date(2026, 10, 28, 9, 0, 0, 0, time.UTC)
	for _, r := range []struct{ db, object, result string }{
		{"Sales", "max server memory (MB)", "10240, verified"},
		{"Sales", "Index", "differs"},
		{"Sales", "dbo.p", domain.ChangeSeenOnServer},
		{"Sales", "dbo.q", domain.ChangeRecordedByHand},
	} {
		if _, err := raw.ExecContext(ctx, `INSERT INTO change (server_id, db_name, changed_at, object, change_type, description, statement, result)
			VALUES (1, ?, ?, ?, 'SETTING', 'd', 's', ?)`, r.db, at, r.object, r.result); err != nil {
			t.Fatal(err)
		}
	}
	raw.Close()

	st, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	rows, err := st.ListActionLog(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("seeded %d rows, want 2: %+v", len(rows), rows)
	}
	r := rows[0]
	if r.Phase != domain.ActionCopied || r.Object != "max server memory (MB)" || r.Outcome != domain.OutcomeVerified ||
		r.WindowsUser != "" || r.ChangeIDs != "1" || !r.StartedAt.Equal(at) || len(r.ActionID) < 32 {
		t.Fatalf("copied row %+v", r)
	}
	if rows[1].Outcome != domain.OutcomeDiffers || rows[1].ActionID == r.ActionID {
		t.Fatalf("second copied row %+v", rows[1])
	}
}

// A read with the same items adds a baseline_read row and no baseline row; a different one adds both. The
// first read of a server is "first".
func TestRecordBaseline(t *testing.T) {
	ctx := context.Background()
	st, err := Open(ctx, filepath.Join(t.TempDir(), "test.duckdb"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	mb := int64(4096)
	f := domain.BaselineFacts{Items: map[string]*string{"config/a": domain.Str("1"), "db/x/auto_close": nil}, Errors: map[string]string{"jobs": "no right"},
		SQLMemoryMB: &mb}
	at := time.Date(2026, 11, 12, 9, 0, 0, 0, time.UTC)
	id1, stored, err := st.RecordBaseline(ctx, 1, f, at, domain.BaselineRead, "")
	if err != nil || !stored {
		t.Fatalf("first: %v %v", stored, err)
	}
	id2, stored, err := st.RecordBaseline(ctx, 1, f, at.Add(time.Minute), domain.BaselineBeforeAction, "A1")
	if err != nil || stored || id2 != id1 {
		t.Fatalf("same items: %d %v %v", id2, stored, err)
	}
	f.Items["config/a"] = domain.Str("2")
	id3, stored, err := st.RecordBaseline(ctx, 1, f, at.Add(2*time.Minute), domain.BaselineAfterAction, "A1")
	if err != nil || !stored || id3 == id1 {
		t.Fatalf("changed items: %d %v %v", id3, stored, err)
	}
	reads, err := st.BaselineReads(ctx, 1)
	if err != nil || len(reads) != 3 {
		t.Fatalf("reads %+v %v", reads, err)
	}
	if reads[0].Reason != domain.BaselineFirst || reads[1].ActionID != "A1" || reads[1].BaselineID != id1 || reads[2].BaselineID != id3 ||
		reads[0].SQLMemoryMB == nil || *reads[0].SQLMemoryMB != 4096 || !reads[2].ReadAt.Equal(at.Add(2*time.Minute)) {
		t.Fatalf("reads %+v", reads)
	}
	bs, err := st.Baselines(ctx, 1, []int64{id1, id3})
	if err != nil || len(bs) != 2 || *bs[id3].Items["config/a"] != "2" || bs[id1].Items["db/x/auto_close"] != nil || bs[id1].Errors["jobs"] != "no right" {
		t.Fatalf("baselines %+v %v", bs, err)
	}
	var n int
	if err := st.db.QueryRowContext(ctx, `SELECT count(*) FROM baseline`).Scan(&n); err != nil || n != 2 {
		t.Fatalf("baseline rows %d %v", n, err)
	}
}

// kakapo's own log keeps both rows of an action.
func TestActionLogRows(t *testing.T) {
	ctx := context.Background()
	st, err := Open(ctx, filepath.Join(t.TempDir(), "test.duckdb"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	at := time.Date(2026, 11, 12, 9, 0, 0, 0, time.UTC)
	row := domain.ActionLogRow{ActionID: "A", Phase: domain.ActionStarted, ServerID: 3, LoggedAt: at, StartedAt: at, WindowsUser: "u",
		SQLLogin: "l", KakapoHost: "h", KakapoVersion: "v", Database: "Sales", Object: "o", ChangeType: "SETTING", FixID: "maxdop",
		Description: "d", Statement: "s", ValueBefore: "0"}
	if err := st.InsertActionLog(ctx, row); err != nil {
		t.Fatal(err)
	}
	end := at.Add(time.Second)
	ms := int64(1000)
	row.Phase, row.EndedAt, row.DurationMs, row.ValueAfter, row.Outcome, row.ChangeIDs = domain.ActionEnded, &end, &ms, "4", domain.OutcomeVerified, "7"
	if err := st.InsertActionLog(ctx, row); err != nil {
		t.Fatal(err)
	}
	rows, err := st.ListActionLog(ctx, 3)
	if err != nil || len(rows) != 2 || rows[1].EndedAt == nil || !rows[1].EndedAt.Equal(end) || *rows[1].DurationMs != 1000 || rows[0].ValueBefore != "0" {
		t.Fatalf("rows %+v %v", rows, err)
	}
}

// The store has no UPDATE or DELETE for the change audit's tables.
func TestAuditTablesAreAppendOnly(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	bad := regexp.MustCompile(`(?i)\b(UPDATE|DELETE\s+FROM|TRUNCATE(\s+TABLE)?|DROP\s+TABLE)\s+(IF\s+EXISTS\s+)?(baseline|baseline_read|action_log)\b`)
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		if m := bad.FindString(string(b)); m != "" {
			t.Errorf("%s changes an append-only table: %q", f, m)
		}
	}
}
