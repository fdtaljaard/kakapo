package service

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"sqlmonitor/internal/domain"
	"sqlmonitor/internal/store"
)

// Every path here fails before the dial or at it (an .invalid host); nothing reaches a SQL Server.
func TestCardDetailRefusesBeforeReading(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "test.duckdb"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	svc := New(st, fakeCreds{})

	var se *Error
	if _, err := svc.CardDetail(ctx, 42, "WarehouseLive", "backups"); !errors.As(err, &se) || se.Kind != KindNotFound {
		t.Errorf("unknown server: want KindNotFound, got %v", err)
	}
	srv, err := st.InsertServer(ctx, domain.ServerInput{Name: "client", Host: "nowhere.invalid", Auth: domain.AuthSQL, Username: "m"},
		domain.ConnectionInfo{})
	if err != nil {
		t.Fatal(err)
	}
	for _, card := range []string{"nothing", "", "Backups"} {
		if _, err := svc.CardDetail(ctx, srv.ID, "WarehouseLive", card); !errors.As(err, &se) || se.Kind != KindNotFound {
			t.Errorf("card %q: want KindNotFound, got %v", card, err)
		}
	}
	if _, err := svc.CardDetail(ctx, srv.ID, " ", "backups"); !errors.As(err, &se) || se.Kind != KindNotFound {
		t.Errorf("no database name: want KindNotFound, got %v", err)
	}
	// A SQL login without a stored password: unreachable, for every real card.
	for _, def := range domain.CardDefs {
		if _, err := svc.CardDetail(ctx, srv.ID, "WarehouseLive", string(def.ID)); !errors.As(err, &se) || se.Kind != KindUnreachable {
			t.Errorf("card %s: want KindUnreachable, got %v", def.ID, err)
		}
	}
}

func TestDetailWithoutChecks(t *testing.T) {
	for _, def := range domain.CardDefs {
		got := detailWithoutChecks(def)
		if got.Card.ID != def.ID || got.Findings == nil || len(got.Findings) != 0 || got.Checks == nil || len(got.Checks) != 0 {
			t.Errorf("%s detail: %+v", def.ID, got)
		}
		b, _ := json.Marshal(got)
		if !strings.Contains(string(b), `"findings":[]`) || !strings.Contains(string(b), `"checks":[]`) {
			t.Errorf("%s detail JSON: %s", def.ID, b)
		}
		if got.Card.Status != domain.StatusGrey || got.Card.StatusText != "Not checked yet" || got.Card.Note != def.Note {
			t.Errorf("%s detail card: %+v", def.ID, got.Card)
		}
		if def.ID == domain.CardSetup && !strings.Contains(string(b), `"fixes":[]`) {
			t.Errorf("setup detail JSON carries no fixes: %s", b)
		}
	}
}

// The generic fixes refuse before dialling, or at the dial (an .invalid host); nothing reaches a SQL Server.
func TestFixesRefuseBeforeApplying(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "test.duckdb"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	svc := New(st, fakeCreds{})
	srv, err := st.InsertServer(ctx, domain.ServerInput{Name: "client", Host: "nowhere.invalid", Auth: domain.AuthSQL, Username: "m"},
		domain.ConnectionInfo{})
	if err != nil {
		t.Fatal(err)
	}
	var se *Error
	for _, tc := range []struct {
		server int64
		fix    string
		kind   Kind
	}{
		{srv.ID, "nothing", KindNotFound},
		{42, "auto-close", KindNotFound},
		{42, "query-store", KindNotFound},
		{srv.ID, "auto-close", KindUnreachable},
		{srv.ID, "query-store", KindUnreachable},
		{42, "update-statistics", KindNotFound},
		{srv.ID, "update-statistics", KindUnreachable},
	} {
		if _, err := svc.PreviewFix(ctx, tc.server, "WarehouseLive", tc.fix); !errors.As(err, &se) || se.Kind != tc.kind {
			t.Errorf("preview %d %s: want kind %d, got %v", tc.server, tc.fix, tc.kind, err)
		}
		if _, err := svc.ApplyFix(ctx, tc.server, "WarehouseLive", tc.fix); !errors.As(err, &se) || se.Kind != tc.kind {
			t.Errorf("apply %d %s: want kind %d, got %v", tc.server, tc.fix, tc.kind, err)
		}
	}
}

// The Design card (docs/phase5n-contract.md): a database that is not online is not read, and its card
// says why; the card page carries no fold then.
func TestDesignFactsNotOnline(t *testing.T) {
	f := readDesignFacts(context.Background(), nil, domain.DatabaseStatus{Name: "WarehouseLive", State: "RESTORING"}, nil)
	def, _ := domain.FindCardDef(domain.CardDesign)
	c, findings, checks := domain.BuildDesignCard(f, def)
	if c.Status != domain.StatusGrey || c.Note != "WarehouseLive is RESTORING, not online, so its design was not checked." || len(findings) != 0 ||
		len(checks) != 2 || checks[0].Status != domain.CheckSkipped || checks[1].Status != domain.CheckSkipped {
		t.Errorf("design card %+v %+v", c, checks)
	}
	if domain.BuildDesignForeignKeys(f) != nil {
		t.Error("a fold for a database that was not read")
	}
}
