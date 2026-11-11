package collect

import (
	"context"
	"database/sql"
	"os"
	"strings"
	"testing"

	// Shared memory, for a local instance whose TCP is not listening: SQLMONITOR_TEST_HOST=lpc:localhost\SQLEXPRESS.
	_ "github.com/microsoft/go-mssqldb/sharedmemory"

	"sqlmonitor/internal/domain"
)

// TestPrimaryKeyLive adds and removes primary keys on two scratch heaps, when SQLMONITOR_TEST_HOST
// (Windows authentication) and SQLMONITOR_TEST_PK_DB name a database made for it
// (docs/phase5s-contract.md, Tests): DES01's read, the identity column's key (preview, statement,
// read-back, rollback), then the new column's, held while a procedure inserts without a column list.
// The scratch tables and the procedure are dropped at the end.
func TestPrimaryKeyLive(t *testing.T) {
	host, dbName := os.Getenv("SQLMONITOR_TEST_HOST"), os.Getenv("SQLMONITOR_TEST_PK_DB")
	if host == "" || dbName == "" {
		t.Skip("set SQLMONITOR_TEST_HOST and SQLMONITOR_TEST_PK_DB (a scratch database) to run")
	}
	db, err := sql.Open("sqlserver", ConnString(domain.ServerInput{Host: host, Auth: domain.AuthWindows}))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	run := func(stmt string) {
		t.Helper()
		if err := ExecFixInDatabase(ctx, db, dbName, stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
	cleanup := func() {
		for _, s := range []string{
			"DROP PROCEDURE IF EXISTS dbo.kakapo_pk_add_bare;",
			"DROP TABLE IF EXISTS dbo.kakapo_pk_ident;",
			"DROP TABLE IF EXISTS dbo.kakapo_pk_bare;",
		} {
			_ = ExecFixInDatabase(ctx, db, dbName, s)
		}
	}
	cleanup()
	defer cleanup()
	run(`CREATE TABLE dbo.kakapo_pk_ident (MoveId int IDENTITY(1, 1) NOT NULL, Ref varchar(20) NOT NULL, Body nvarchar(100) NULL);
INSERT dbo.kakapo_pk_ident (Ref, Body) SELECT TOP (12000) CONCAT('R', ROW_NUMBER() OVER (ORDER BY (SELECT NULL))), N'body'
FROM sys.all_columns AS a CROSS JOIN sys.all_columns AS b;
CREATE INDEX IX_kakapo_pk_ident_Ref ON dbo.kakapo_pk_ident (Ref);`)
	run(`CREATE TABLE dbo.kakapo_pk_bare (Ref varchar(20) NOT NULL, Body nvarchar(100) NULL);
INSERT dbo.kakapo_pk_bare (Ref, Body) SELECT TOP (500) 'R', N'body' FROM sys.all_columns;`)
	run(`CREATE PROCEDURE dbo.kakapo_pk_add_bare AS INSERT INTO dbo.kakapo_pk_bare VALUES ('x', N'y');`)

	// DES01's read: both scratch tables, as heaps without a key.
	tables, err := ReadNoKeyTables(ctx, db, dbName)
	if err != nil {
		t.Fatal(err)
	}
	found := map[string]domain.NoKeyTableFact{}
	for _, x := range tables {
		found[x.Table] = x
	}
	ident, bare := found["kakapo_pk_ident"], found["kakapo_pk_bare"]
	if ident.Rows != 12000 || !ident.Heap || ident.Identity != "MoveId" || ident.IdentityType != "int" || ident.Nonclustered != 1 || ident.HasPrimaryKey {
		t.Errorf("ident %+v", ident)
	}
	if bare.Rows != 500 || !bare.Heap || bare.Identity != "" {
		t.Errorf("bare %+v", bare)
	}
	t.Logf("%d tables without a primary key in %s", len(tables), dbName)

	// The identity column's key: clustered on the heap, verified, rolled back.
	f, err := PrimaryKeyFacts(ctx, db, dbName, "dbo", "kakapo_pk_ident")
	if err != nil || !f.Found {
		t.Fatalf("facts %+v %v", f, err)
	}
	p, fix := domain.BuildAddPrimaryKeyFix(f, map[string]string{domain.OptionTable: "[dbo].[kakapo_pk_ident]"})
	if p.Held != "" || len(p.Changes) != 1 {
		t.Fatalf("identity preview %+v", p)
	}
	t.Logf("%s | %s", p.Statement, fix.Target)
	if err := ExecFix(ctx, db, p.Statement); err != nil {
		t.Fatal(err)
	}
	after, err := PrimaryKeyFacts(ctx, db, dbName, "dbo", "kakapo_pk_ident")
	if err != nil {
		t.Fatal(err)
	}
	if text, ok := domain.PrimaryKeyReadBack(after, p); !ok || after.Table.Heap {
		t.Errorf("identity read back %q %v", text, ok)
	} else {
		t.Logf("read back: %s", text)
	}
	if err := ExecFix(ctx, db, p.Rollback); err != nil {
		t.Fatal(err)
	}
	if back, _ := PrimaryKeyFacts(ctx, db, dbName, "dbo", "kakapo_pk_ident"); back.Table.HasPrimaryKey || !back.Table.Heap {
		t.Errorf("rollback left %+v", back.Table)
	}

	// The new column: held by the procedure, then by the missing confirm, then applied and rolled back.
	f, err = PrimaryKeyFacts(ctx, db, dbName, "dbo", "kakapo_pk_bare")
	if err != nil {
		t.Fatal(err)
	}
	opts := map[string]string{domain.OptionTable: "[dbo].[kakapo_pk_bare]", domain.OptionPKConfirm: "true"}
	if p, _ = domain.BuildAddPrimaryKeyFix(f, opts); !strings.HasPrefix(p.Held, "dbo.kakapo_pk_add_bare inserts into kakapo_pk_bare without a column list") {
		t.Errorf("held by the procedure: %q (code %+v)", p.Held, f.Code)
	}
	run("DROP PROCEDURE dbo.kakapo_pk_add_bare;")
	f, _ = PrimaryKeyFacts(ctx, db, dbName, "dbo", "kakapo_pk_bare")
	if p, _ = domain.BuildAddPrimaryKeyFix(f, map[string]string{domain.OptionTable: "[dbo].[kakapo_pk_bare]"}); !strings.HasPrefix(p.Held, "Tick ") {
		t.Errorf("held until confirmed: %q", p.Held)
	}
	p, _ = domain.BuildAddPrimaryKeyFix(f, opts)
	if p.Held != "" {
		t.Fatalf("still held: %q", p.Held)
	}
	t.Logf("%s", p.Statement)
	if err := ExecFix(ctx, db, p.Statement); err != nil {
		t.Fatal(err)
	}
	after, err = PrimaryKeyFacts(ctx, db, dbName, "dbo", "kakapo_pk_bare")
	if err != nil {
		t.Fatal(err)
	}
	if text, ok := domain.PrimaryKeyReadBack(after, p); !ok || after.Table.Rows != 500 {
		t.Errorf("new column read back %q %v (rows %d)", text, ok, after.Table.Rows)
	} else {
		t.Logf("read back: %s", text)
	}
	if err := ExecFix(ctx, db, p.Rollback); err != nil {
		t.Fatal(err)
	}
	back, _ := PrimaryKeyFacts(ctx, db, dbName, "dbo", "kakapo_pk_bare")
	if back.Table.HasPrimaryKey || back.Table.Identity != "" || !back.Table.Heap {
		t.Errorf("rollback left %+v", back.Table)
	}
	for _, c := range back.Index.Columns {
		if c.Name == domain.DefaultKeyColumn {
			t.Errorf("rollback left the column")
		}
	}
}
