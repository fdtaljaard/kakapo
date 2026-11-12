package service

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"sqlmonitor/internal/collect"
	"sqlmonitor/internal/domain"
	"sqlmonitor/internal/store"
)

// q5LiveDB is the throwaway database the live test creates and drops.
const q5LiveDB = "kakapo_5q_test"

// q5Procedure is a home-grown index procedure in the common shape (docs/phase5q-contract.md, Facts).
const q5Procedure = `CREATE PROCEDURE dbo.RebuildIndexes
AS
BEGIN
    SET NOCOUNT ON;
    DECLARE @Table varchar(max), @Index varchar(max), @Fragmentation decimal(5,2), @SQL nvarchar(max);
    DECLARE c CURSOR FOR
        SELECT t.name, i.name, s.avg_fragmentation_in_percent
        FROM sys.dm_db_index_physical_stats (DB_ID(), NULL, NULL, NULL, NULL) AS s
        JOIN sys.tables t ON t.object_id = s.object_id
        JOIN sys.indexes i ON i.object_id = s.object_id AND s.index_id = i.index_id
        ORDER BY s.avg_fragmentation_in_percent DESC;
    OPEN c;
    FETCH NEXT FROM c INTO @Table, @Index, @Fragmentation;
    WHILE @@FETCH_STATUS = 0
    BEGIN
        IF @Fragmentation > 30
        BEGIN
            SET @SQL = 'ALTER INDEX [' + @Index + '] ON [' + @Table + '] REBUILD';
            EXEC sp_executesql @SQL;
        END
        FETCH NEXT FROM c INTO @Table, @Index, @Fragmentation;
    END
    CLOSE c;
    DEALLOCATE c;
END`

// TestPhase5qLive runs phase 5q's live checks (docs/phase5q-contract.md, Done when) on a throwaway
// database when SQLMONITOR_TEST_HOST (Windows authentication) names a server and SQLMONITOR_TEST_5Q=1
// allows writing to it: three fragmented tables over 2,000 pages, the home-grown procedure run once
// (MNT09 from its fingerprint, then from an Agent job that names it, and MNT03's index-only title), the
// query page's measurement of one index, and reorganize-index applied through the real fix. Everything
// is dropped afterwards.
func TestPhase5qLive(t *testing.T) {
	host := os.Getenv("SQLMONITOR_TEST_HOST")
	if host == "" || os.Getenv("SQLMONITOR_TEST_5Q") != "1" {
		t.Skip("set SQLMONITOR_TEST_HOST and SQLMONITOR_TEST_5Q=1 to run phase 5q's checks on a live instance")
	}
	ctx := context.Background()
	db, err := sql.Open("sqlserver", collect.ConnString(domain.ServerInput{Host: host, Auth: domain.AuthWindows}))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	exec := func(q string) {
		t.Helper()
		if _, err := db.ExecContext(ctx, q); err != nil {
			t.Fatalf("%v\n%s", err, q)
		}
	}
	var n int
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM sys.databases WHERE name = @p1", q5LiveDB).Scan(&n); err != nil || n != 0 {
		t.Fatalf("%s exists already (%v)", q5LiveDB, err)
	}
	const job = "kakapo 5q test: nightly indexes"
	exec("CREATE DATABASE [" + q5LiveDB + "]")
	defer func() {
		db.ExecContext(ctx, "IF EXISTS (SELECT 1 FROM msdb.dbo.sysjobs WHERE name = N'"+job+"') EXEC msdb.dbo.sp_delete_job @job_name = N'"+job+"';")
		// A pooled connection may still be in the database: put this one in master first.
		if _, err := db.ExecContext(ctx, "USE master; ALTER DATABASE ["+q5LiveDB+"] SET SINGLE_USER WITH ROLLBACK IMMEDIATE; DROP DATABASE ["+q5LiveDB+"];"); err != nil {
			t.Errorf("could not drop %s: %v", q5LiveDB, err)
		}
	}()
	exec("ALTER DATABASE [" + q5LiveDB + "] SET RECOVERY SIMPLE")
	// Three tables of about 3,000 pages, filled in batches of random keys so they fragment (one insert is sorted).
	for _, name := range []string{"Orders", "Lines", "Stock"} {
		exec("USE [" + q5LiveDB + "]; CREATE TABLE dbo." + name + " (id uniqueidentifier NOT NULL DEFAULT NEWID() PRIMARY KEY, pad char(700) NOT NULL DEFAULT 'x'); " +
			"DECLARE @i int = 0; WHILE @i < 30 BEGIN INSERT dbo." + name + " (pad) SELECT TOP (1000) 'x' FROM sys.all_columns; SET @i += 1; END; USE master;")
	}
	exec("USE [" + q5LiveDB + "]; EXEC (N'" + strings.ReplaceAll(q5Procedure, "'", "''") + "'); USE master;")

	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "live.duckdb"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	srv, err := st.InsertServer(ctx, domain.ServerInput{Name: "SQL1", Host: host, Auth: domain.AuthWindows}, domain.ConnectionInfo{})
	if err != nil {
		t.Fatal(err)
	}
	svc := New(st, fakeCreds{}, "test")

	// The query page's measurement of a fragmented index, and the fix.
	pk := func() string {
		var name string
		if err := db.QueryRowContext(ctx, "SELECT name FROM ["+q5LiveDB+"].sys.indexes WHERE object_id = OBJECT_ID(N'["+q5LiveDB+"].dbo.Lines') AND index_id = 1").Scan(&name); err != nil {
			t.Fatal(err)
		}
		return name
	}()
	shape, err := svc.IndexShape(ctx, srv.ID, q5LiveDB, "dbo", "Lines", pk)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("measured: %s", shape.Text)
	if !shape.Measured || shape.Pages < 2000 || shape.Fragmentation < 50 || shape.Action == nil || shape.Action.FixID != domain.FixReorganizeIndex {
		t.Fatalf("shape %+v", shape)
	}
	g, err := svc.prepareGenericFix(ctx, srv.ID, q5LiveDB, domain.FixReorganizeIndex, shape.Action.Options)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("preview: %s\n%s\n%s", g.preview.Lead, g.preview.Statement, strings.Join(g.preview.Warnings, "\n"))
	res, err := applyReorganizeIndex(ctx, svc, srv.ID, g)
	g.db.Close()
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("read-back: %s (verified %v)", res.After, res.Verified)
	if !res.Verified {
		t.Errorf("not verified: %+v", res)
	}
	changes, err := st.ListChanges(ctx, srv.ID, q5LiveDB, 10)
	if err != nil || len(changes) != 1 || changes[0].ChangeType != "INDEX" || !strings.HasPrefix(changes[0].Description, "Reorganised "+pk) {
		t.Errorf("change log %+v %v", changes, err)
	}

	// MNT09: the procedure run once rebuilds the other two tables' indexes (Lines is in order now) and
	// the fingerprint shows them; a third is needed, so Lines is fragmented again first.
	exec("USE [" + q5LiveDB + "]; DECLARE @i int = 0; WHILE @i < 10 BEGIN INSERT dbo.Lines (pad) SELECT TOP (1000) 'y' FROM sys.all_columns; SET @i += 1; END; USE master;")
	exec("USE [" + q5LiveDB + "]; EXEC dbo.RebuildIndexes; USE master;")
	f, err := collect.UpkeepFacts(ctx, db, q5LiveDB)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("procedures %+v, other steps %d, hour %v, count %d, error %q, jobs error %q",
		f.IndexProcedures, len(f.IndexJobSteps), f.RebuildHour, f.RebuildCount, f.IndexJobError, f.IndexJobStepsError)
	if f.IndexJobError != "" || len(f.IndexProcedures) != 1 || f.IndexProcedures[0].Name != "RebuildIndexes" || !f.IndexProcedures[0].Rebuilds ||
		f.RebuildCount < 3 || f.RebuildHour == nil {
		t.Fatalf("facts %+v", f)
	}
	mnt09 := func(f domain.UpkeepFacts) []domain.Finding {
		_, fs, _ := domain.BuildUpkeepCard(f, upkeepCardDef())
		out := []domain.Finding{}
		for _, x := range fs {
			if x.CheckID == "MNT09" || x.CheckID == "MNT03" {
				out = append(out, x)
			}
		}
		return out
	}
	for _, x := range mnt09(f) {
		t.Logf("%s: %s", x.CheckID, x.Finding)
	}
	// With an Agent job that names it (enabled, never run).
	exec("EXEC msdb.dbo.sp_add_job @job_name = N'" + job + "', @enabled = 1; " +
		"EXEC msdb.dbo.sp_add_jobstep @job_name = N'" + job + "', @step_name = N'run', @subsystem = N'TSQL', @database_name = N'" + q5LiveDB + "', @command = N'EXEC dbo.RebuildIndexes;';")
	f, err = collect.UpkeepFacts(ctx, db, q5LiveDB)
	if err != nil {
		t.Fatal(err)
	}
	if len(f.IndexProcedures) != 1 || len(f.IndexProcedures[0].Jobs) != 1 || f.IndexProcedures[0].Jobs[0] != job {
		t.Fatalf("job not seen: %+v (%s)", f.IndexProcedures, f.IndexJobStepsError)
	}
	got := mnt09(f)
	found := false
	for _, x := range got {
		t.Logf("%s: %s\n  %s", x.CheckID, x.Finding, x.Evidence)
		found = found || (x.CheckID == "MNT09" && strings.Contains(x.Finding, `job "`+job+`"`))
	}
	if !found {
		t.Errorf("MNT09 with the job: %+v", got)
	}
}

// upkeepCardDef is the Upkeep card's definition.
func upkeepCardDef() domain.CardDef { d, _ := domain.FindCardDef(domain.CardUpkeep); return d }
