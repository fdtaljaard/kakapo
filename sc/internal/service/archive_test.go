package service

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"sqlmonitor/internal/domain"
	"sqlmonitor/internal/store"
)

func TestArchiveFixesRegistered(t *testing.T) {
	for _, id := range []string{domain.FixArchiveTable, domain.FixRemoveArchive} {
		r, ok := registeredFixFor(id)
		if !ok {
			t.Errorf("%s is not registered", id)
		}
		if r.long != nil {
			t.Errorf("%s runs in the background; it should answer at once", id)
		}
		if _, ok := domain.FindFixDef(id); !ok {
			t.Errorf("%s has no definition", id)
		}
	}
}

func TestArchiveTableOption(t *testing.T) {
	var se *Error
	if schema, table, err := archiveTableTarget(map[string]string{domain.OptionTable: "[dbo].[MailLog]"}, "Shop"); err != nil || schema != "dbo" || table != "MailLog" {
		t.Errorf("%s %s %v", schema, table, err)
	}
	if _, _, err := archiveTableTarget(map[string]string{domain.OptionTable: "[Shop].[dbo].[MailLog]"}, "shop"); err != nil {
		t.Errorf("own database: %v", err)
	}
	if _, _, err := archiveTableTarget(map[string]string{domain.OptionTable: "Other.dbo.MailLog"}, "Shop"); !errors.As(err, &se) || se.Kind != KindConflict {
		t.Errorf("another database: want KindConflict, got %v", err)
	}
	for _, bad := range []map[string]string{nil, {domain.OptionTable: " "}, {domain.OptionTable: "[dbo"}} {
		if _, _, err := archiveTableTarget(bad, "Shop"); !errors.As(err, &se) || se.Kind != KindInvalid {
			t.Errorf("%v: want KindInvalid, got %v", bad, err)
		}
	}
}

// A preview with a bad option (or a blocking reason) has no changes, and Apply refuses it with the
// reason before touching the server (the connection is nil here).
func TestArchiveApplyRefusesWithoutChanges(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 11, 4, 10, 0, 0, 0, time.UTC)
	f := domain.ArchiveFacts{Database: "Shop", Schema: "dbo", Table: "MailLog", ServerName: "SQL01", ServerNow: now, Found: true,
		Rows: 1000, MB: 1, AgentStatus: "Running",
		Columns: []domain.ArchiveColumn{{Name: "Id", Type: "int"}, {Name: "LoggedAt", Type: "datetime2", DateIndexed: true}},
		Target:  domain.ArchiveTarget{Database: "Shop", Schema: "archive", Table: "MailLog", Read: true}}
	for _, options := range []map[string]string{
		{domain.OptionTable: "[dbo].[MailLog]", domain.OptionArchiveKeepMonths: "0"},
		{domain.OptionTable: "[dbo].[MailLog]", domain.OptionArchiveCondition: "1 = 1; DROP TABLE dbo.MailLog"},
		{domain.OptionTable: "[dbo].[MailLog]", domain.OptionArchiveTo: "Shop_Archive"},
	} {
		g := &genericFix{srv: domain.Server{ID: 1, Name: "SQL01"}, database: domain.DatabaseStatus{Name: "Shop", State: "ONLINE"}}
		g.preview, g.fix = domain.BuildArchiveFix(f, options)
		if len(g.preview.Changes) != 0 || g.preview.Statement != "" || len(g.preview.Warnings) == 0 {
			t.Errorf("%v: %+v", options, g.preview)
			continue
		}
		var se *Error
		if _, err := applyArchiveTable(ctx, nil, 1, g); !errors.As(err, &se) || se.Kind != KindConflict || se.Error() != g.preview.Warnings[0] {
			t.Errorf("%v apply: want KindConflict with the warning, got %v", options, err)
		}
	}
	// Good options: changes, a statement and a rollback.
	p, _ := domain.BuildArchiveFix(f, map[string]string{domain.OptionTable: "[dbo].[MailLog]"})
	if len(p.Changes) != 4 || p.Statement == "" || p.Rollback == "" {
		t.Errorf("good options: %+v", p)
	}
	// The remove fix with nothing to run refuses too.
	g := &genericFix{srv: domain.Server{ID: 1, Name: "SQL01"}, database: domain.DatabaseStatus{Name: "Shop", State: "ONLINE"},
		preview: domain.FixPreview{Warnings: []string{"nothing to remove"}}}
	var se *Error
	if _, err := applyRemoveArchive(ctx, nil, 1, g); !errors.As(err, &se) || se.Kind != KindConflict {
		t.Errorf("remove apply: want KindConflict, got %v", err)
	}
}

// The picker's list refuses before reading: an unknown server, no database name, a server it
// cannot reach.
func TestListTablesRefusesBeforeReading(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "test.duckdb"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	svc := New(st, fakeCreds{})
	var se *Error
	if _, err := svc.ListTables(ctx, 42, "Shop"); !errors.As(err, &se) || se.Kind != KindNotFound {
		t.Errorf("unknown server: want KindNotFound, got %v", err)
	}
	srv, err := st.InsertServer(ctx, domain.ServerInput{Name: "client", Host: "nowhere.invalid", Auth: domain.AuthSQL, Username: "m"},
		domain.ConnectionInfo{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ListTables(ctx, srv.ID, " "); !errors.As(err, &se) || se.Kind != KindNotFound {
		t.Errorf("no database: want KindNotFound, got %v", err)
	}
	if _, err := svc.ListTables(ctx, srv.ID, "Shop"); !errors.As(err, &se) || se.Kind != KindUnreachable {
		t.Errorf("no stored password: want KindUnreachable, got %v", err)
	}
	// The fixes refuse the same way before reading anything.
	if _, err := svc.PreviewFixWithOptions(ctx, srv.ID, "Shop", domain.FixArchiveTable, map[string]string{domain.OptionTable: "[dbo].[MailLog]"}); !errors.As(err, &se) || se.Kind != KindUnreachable {
		t.Errorf("archive preview: want KindUnreachable, got %v", err)
	}
}
