package store

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"sqlmonitor/internal/domain"
)

func TestListChangesAndUpdateServerTested(t *testing.T) {
	ctx := context.Background()
	st, err := Open(ctx, filepath.Join(t.TempDir(), "test.duckdb"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	a, err := st.InsertServer(ctx, domain.ServerInput{Name: "a", Host: "a", Auth: domain.AuthWindows}, domain.ConnectionInfo{})
	if err != nil {
		t.Fatal(err)
	}
	b, err := st.InsertServer(ctx, domain.ServerInput{Name: "b", Host: "b", Auth: domain.AuthWindows}, domain.ConnectionInfo{})
	if err != nil {
		t.Fatal(err)
	}

	base := time.Date(2026, 11, 3, 8, 0, 0, 0, time.UTC)
	rows := []struct {
		server int64
		db     string
	}{{a.ID, "WarehouseLive"}, {a.ID, "WarehouseLive"}, {b.ID, "WarehouseLive"}, {a.ID, "Other"}, {a.ID, "WarehouseLive"}}
	for i, r := range rows {
		c := domain.Change{ServerID: r.server, Database: r.db, ChangedAt: base.Add(time.Duration(i) * time.Hour), Object: "Query Store",
			ChangeType: "SETTING", Description: "d", Statement: "s"}
		if i == 0 {
			c.Result = "On"
		}
		if _, err := st.InsertChange(ctx, c); err != nil {
			t.Fatal(err)
		}
	}

	// Only server a, only WarehouseLive, newest first.
	got, err := st.ListChanges(ctx, a.ID, "WarehouseLive", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 || got[0].ID != 5 || got[1].ID != 2 || got[2].ID != 1 {
		t.Fatalf("newest first for server a, WarehouseLive: %+v", got)
	}
	if got[2].Database != "WarehouseLive" || got[2].Result != "On" || !got[2].ChangedAt.Equal(base) || got[2].ChangedAt.Location() != time.UTC {
		t.Errorf("row 1: %+v", got[2])
	}
	if got[0].Result != "" {
		t.Errorf("null result comes back empty: %+v", got[0])
	}
	if got, _ := st.ListChanges(ctx, a.ID, "WarehouseLive", 2); len(got) != 2 {
		t.Errorf("limit 2: %d rows", len(got))
	}
	if got, _ := st.ListChanges(ctx, a.ID, "Other", 10); len(got) != 1 || got[0].ID != 4 {
		t.Errorf("other database: %+v", got)
	}
	for _, c := range []struct {
		server int64
		db     string
	}{{99, "WarehouseLive"}, {a.ID, "Nope"}, {a.ID, "warehouselive"}} {
		if got, _ := st.ListChanges(ctx, c.server, c.db, 10); got == nil || len(got) != 0 {
			t.Errorf("server %d database %q: %v", c.server, c.db, got)
		}
	}

	at := time.Date(2026, 11, 3, 9, 21, 0, 0, time.UTC)
	if err := st.UpdateServerTested(ctx, a.ID, "16.0.1000.6", "Developer Edition (64-bit)", at); err != nil {
		t.Fatal(err)
	}
	srv, err := st.GetServer(ctx, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	if srv.LastVersion != "16.0.1000.6" || srv.LastEdition != "Developer Edition (64-bit)" || srv.LastTestedAt == nil || !srv.LastTestedAt.Equal(at) {
		t.Errorf("updated server: %+v", srv)
	}
	if err := st.UpdateServerTested(ctx, 99, "x", "y", at); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown server: want ErrNotFound, got %v", err)
	}
}

func TestUpdateServerDatabase(t *testing.T) {
	ctx := context.Background()
	st, err := Open(ctx, filepath.Join(t.TempDir(), "test.duckdb"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	a, err := st.InsertServer(ctx, domain.ServerInput{Name: "a", Host: "a", Auth: domain.AuthWindows}, domain.ConnectionInfo{ProductVersion: "16.0"})
	if err != nil {
		t.Fatal(err)
	}
	srv, err := st.UpdateServerDatabase(ctx, a.ID, "WarehouseLive")
	if err != nil {
		t.Fatal(err)
	}
	if srv.ID != a.ID || srv.Database != "WarehouseLive" || srv.Name != "a" || srv.LastVersion != "16.0" {
		t.Errorf("returned server: %+v", srv)
	}
	if got, _ := st.GetServer(ctx, a.ID); got.Database != "WarehouseLive" {
		t.Errorf("stored server: %+v", got)
	}
	if srv, _ = st.UpdateServerDatabase(ctx, a.ID, "Other DB"); srv.Database != "Other DB" {
		t.Errorf("changed again: %+v", srv)
	}
	if _, err := st.UpdateServerDatabase(ctx, 99, "x"); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown server: want ErrNotFound, got %v", err)
	}
}

func TestUpdateServer(t *testing.T) {
	ctx := context.Background()
	st, err := Open(ctx, filepath.Join(t.TempDir(), "test.duckdb"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	a, err := st.InsertServer(ctx, domain.ServerInput{Name: "a", Host: "a", Auth: domain.AuthSQL, Username: "sa", Database: "WarehouseLive"},
		domain.ConnectionInfo{ProductVersion: "15.0", Edition: "Express"})
	if err != nil {
		t.Fatal(err)
	}
	in := domain.ServerInput{Name: "b", Host: `b\SQL2022`, Auth: domain.AuthWindows, Encrypt: true, TrustCert: true}
	srv, err := st.UpdateServer(ctx, a.ID, in, domain.ConnectionInfo{ProductVersion: "16.0", Edition: "Developer"})
	if err != nil {
		t.Fatal(err)
	}
	if srv.ID != a.ID || srv.Name != "b" || srv.Host != `b\SQL2022` || srv.Auth != domain.AuthWindows || srv.Username != "" ||
		srv.Database != "" || !srv.Encrypt || !srv.TrustCert || srv.LastVersion != "16.0" || srv.LastEdition != "Developer" ||
		!srv.CreatedAt.Equal(a.CreatedAt) || srv.LastTestedAt == nil || srv.LastTestedAt.Before(*a.LastTestedAt) {
		t.Errorf("returned server: %+v (was %+v)", srv, a)
	}
	if got, _ := st.GetServer(ctx, a.ID); got.Name != "b" || got.Database != "" || got.Username != "" {
		t.Errorf("stored server: %+v", got)
	}
	if _, err := st.UpdateServer(ctx, 99, in, domain.ConnectionInfo{}); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown server: want ErrNotFound, got %v", err)
	}
}

// The tuning report's changes: this database's and the server-level ones in the window, newest first.
func TestListChangesSince(t *testing.T) {
	ctx := context.Background()
	st, err := Open(ctx, filepath.Join(t.TempDir(), "test.duckdb"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	a, err := st.InsertServer(ctx, domain.ServerInput{Name: "a", Host: "a", Auth: domain.AuthWindows}, domain.ConnectionInfo{})
	if err != nil {
		t.Fatal(err)
	}
	base := time.Date(2026, 11, 3, 8, 0, 0, 0, time.UTC)
	for i, db := range []string{"WarehouseLive", "", "Other", "WarehouseLive"} {
		c := domain.Change{ServerID: a.ID, Database: db, ChangedAt: base.Add(time.Duration(i) * 24 * time.Hour), Object: "o", ChangeType: "SETTING",
			Description: "d", Result: domain.ChangeRecordedByHand}
		if _, err := st.InsertChange(ctx, c); err != nil {
			t.Fatal(err)
		}
	}
	got, err := st.ListChangesSince(ctx, a.ID, "WarehouseLive", base.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].ID != 4 || got[1].ID != 2 || got[1].Database != "" || got[0].Result != domain.ChangeRecordedByHand || got[0].Statement != "" {
		t.Errorf("since: %+v", got)
	}
	if got, _ := st.ListChangesSince(ctx, a.ID, "WarehouseLive", base); len(got) != 3 {
		t.Errorf("from the first: %d", len(got))
	}
}

// The per-hour replace of a harvest (docs/phase5f-contract.md): one transaction, rows recomputed for
// the window's hours replace the changes seen on the server there; older hours, other servers and
// the other rows of the log are never touched; a row recomputed the same keeps its id.
func TestReplaceSeenChanges(t *testing.T) {
	ctx := context.Background()
	st, err := Open(ctx, filepath.Join(t.TempDir(), "test.duckdb"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	a, _ := st.InsertServer(ctx, domain.ServerInput{Name: "a", Host: "a", Auth: domain.AuthWindows}, domain.ConnectionInfo{})
	b, _ := st.InsertServer(ctx, domain.ServerInput{Name: "b", Host: "b", Auth: domain.AuthWindows}, domain.ConnectionInfo{})

	since := time.Date(2026, 11, 5, 4, 0, 0, 0, time.UTC)
	seen := func(server int64, db string, at time.Time, object string) domain.Change {
		return domain.Change{ServerID: server, Database: db, ChangedAt: at, Object: object, ChangeType: "PROCEDURE",
			Description: "Procedure " + object + " altered by x", Result: domain.ChangeSeenOnServer}
	}
	old := seen(a.ID, "W", since.Add(-time.Hour), "dbo.Old") // before the window: kept
	fix := domain.Change{ServerID: a.ID, Database: "W", ChangedAt: since.Add(time.Hour), Object: "Query Store", ChangeType: "SETTING",
		Description: "d", Statement: "s", Result: "On, verified"} // a fix: kept
	other := seen(b.ID, "W", since.Add(time.Hour), "dbo.P1") // another server: kept
	ids := map[string]int64{}
	for name, c := range map[string]domain.Change{"old": old, "fix": fix, "other": other} {
		if ids[name], err = st.InsertChange(ctx, c); err != nil {
			t.Fatal(err)
		}
	}

	p1 := seen(a.ID, "W", since.Add(time.Hour), "dbo.P1")
	p2 := seen(a.ID, "X", since.Add(2*time.Hour), "dbo.P2")
	if added, deleted, err := st.ReplaceSeenChanges(ctx, a.ID, since, []domain.Change{p1, p2}); err != nil || added != 2 || deleted != 0 {
		t.Fatalf("first harvest: %d %d %v", added, deleted, err)
	}
	first, _ := st.ListChanges(ctx, a.ID, "W", 10)
	// The same harvest again adds nothing and keeps the ids.
	if added, deleted, err := st.ReplaceSeenChanges(ctx, a.ID, since, []domain.Change{p2, p1}); err != nil || added != 0 || deleted != 0 {
		t.Fatalf("second harvest: %d %d %v", added, deleted, err)
	}
	again, _ := st.ListChanges(ctx, a.ID, "W", 10)
	if len(first) != 3 || len(again) != 3 || first[0].ID != again[0].ID {
		t.Errorf("W after two harvests: %+v / %+v", first, again)
	}
	// The hour recomputed as a deploy in W: dbo.P1 goes, the deploy comes; X's hour has nothing now.
	deploy := domain.Change{ServerID: a.ID, Database: "W", ChangedAt: since.Add(time.Hour + 30*time.Minute), Object: "6 objects",
		ChangeType: domain.ChangeTypeDeploy, Description: "Deploy by x", Statement: "Procedure dbo.P1 altered\nProcedure dbo.P3 created",
		Result: domain.ChangeSeenOnServer}
	if added, deleted, err := st.ReplaceSeenChanges(ctx, a.ID, since, []domain.Change{deploy}); err != nil || added != 1 || deleted != 2 {
		t.Fatalf("recomputed: %d %d %v", added, deleted, err)
	}
	got, _ := st.ListChanges(ctx, a.ID, "W", 10)
	if len(got) != 3 || got[0].ChangeType != domain.ChangeTypeDeploy || got[0].Statement != deploy.Statement || got[0].Result != domain.ChangeSeenOnServer ||
		got[1].ID != ids["fix"] || got[2].ID != ids["old"] {
		t.Errorf("W: %+v", got)
	}
	if x, _ := st.ListChanges(ctx, a.ID, "X", 10); len(x) != 0 {
		t.Errorf("X: %+v", x)
	}
	if bb, _ := st.ListChanges(ctx, b.ID, "W", 10); len(bb) != 1 || bb[0].ID != ids["other"] {
		t.Errorf("server b: %+v", bb)
	}

	// The setting that keeps the newest event seen.
	if v, err := st.Setting(ctx, "schemachanges.seen.1"); err != nil || v != "" {
		t.Errorf("unset: %q %v", v, err)
	}
	for _, v := range []string{"2026-11-05T05:00:00Z", "2026-11-05T06:00:00Z"} {
		if err := st.SetSetting(ctx, "schemachanges.seen.1", v); err != nil {
			t.Fatal(err)
		}
	}
	if v, _ := st.Setting(ctx, "schemachanges.seen.1"); v != "2026-11-05T06:00:00Z" {
		t.Errorf("setting %q", v)
	}
}
