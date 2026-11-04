package service

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"sqlmonitor/internal/domain"
	"sqlmonitor/internal/store"
)

// The query page's four fixes are registered, each with its prepare and apply.
func TestQueryFixesRegistered(t *testing.T) {
	for _, id := range []string{domain.FixForcePlan, domain.FixUnforcePlan, domain.FixEvictPlan, domain.FixUpdateStatisticsTable} {
		if _, ok := registeredFixFor(id); !ok {
			t.Errorf("%s is not registered", id)
		}
		if d, ok := domain.FindFixDef(id); !ok || d.CheckID != "QRY01" {
			t.Errorf("%s: fix definition %+v", id, d)
		}
	}
}

// A domain refusal becomes the service's kind: wrong options 400, nothing to apply 404.
func TestQueryFixErrorKinds(t *testing.T) {
	var se *Error
	_, err := domain.ParsePlanID("x")
	if err := qFixError(err); !errors.As(err, &se) || se.Kind != KindInvalid {
		t.Errorf("invalid option: %v", err)
	}
	_, _, err = domain.BuildPlanForceFix(domain.FixForcePlan, domain.PlanForceFacts{Database: "GraniteLive", QueryStoreState: "OFF"})
	if err := qFixError(err); !errors.As(err, &se) || se.Kind != KindNotFound || se.Msg != "nothing to apply: Query Store is off on GraniteLive: plans can only be forced while it is on. Turn it on first (the Setup card)." {
		t.Errorf("refusal: %v", err)
	}
	other := errors.New("boom")
	if qFixError(other) != other {
		t.Error("another error is passed through")
	}
}

// Every path here fails before the dial or at it (an .invalid host); nothing reaches a SQL Server.
func TestQueryDetailRefusesBeforeReading(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "test.duckdb"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	svc := New(st, fakeCreds{})
	var se *Error
	if _, err := svc.QueryDetail(ctx, 42, "GraniteLive", "0x9F3A00112233AABB"); !errors.As(err, &se) || se.Kind != KindNotFound {
		t.Errorf("unknown server: want KindNotFound, got %v", err)
	}
	srv, err := st.InsertServer(ctx, domain.ServerInput{Name: "client", Host: "nowhere.invalid", Auth: domain.AuthSQL, Username: "m"},
		domain.ConnectionInfo{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.QueryDetail(ctx, srv.ID, "GraniteLive", "0x9F3A"); !errors.As(err, &se) || se.Kind != KindInvalid {
		t.Errorf("bad hash: want KindInvalid, got %v", err)
	}
	if _, err := svc.QueryDetail(ctx, srv.ID, " ", "0x9F3A00112233AABB"); !errors.As(err, &se) || se.Kind != KindNotFound {
		t.Errorf("no database: want KindNotFound, got %v", err)
	}
	if _, err := svc.QueryDetail(ctx, srv.ID, "GraniteLive", "0x9F3A00112233AABB"); !errors.As(err, &se) || se.Kind != KindUnreachable {
		t.Errorf("no stored password: want KindUnreachable, got %v", err)
	}
	// The fixes refuse at the dial too, before anything is read.
	for _, id := range []string{domain.FixForcePlan, domain.FixEvictPlan, domain.FixUpdateStatisticsTable} {
		if _, err := svc.PreviewFixWithOptions(ctx, srv.ID, "GraniteLive", id, map[string]string{"planId": "12"}); !errors.As(err, &se) || se.Kind != KindUnreachable {
			t.Errorf("%s: want KindUnreachable, got %v", id, err)
		}
	}
}
