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

// Every path here fails before the dial or at it (an .invalid host); nothing reaches a SQL Server.
func TestBlockingRefusesBeforeReading(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "test.duckdb"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	svc := New(st, fakeCreds{})

	var se *Error
	if _, err := svc.Blocking(ctx, 42, "WMS"); !errors.As(err, &se) || se.Kind != KindNotFound {
		t.Errorf("unknown server: want KindNotFound, got %v", err)
	}
	srv, err := st.InsertServer(ctx, domain.ServerInput{Name: "client", Host: "nowhere.invalid", Auth: domain.AuthSQL, Username: "m"},
		domain.ConnectionInfo{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Blocking(ctx, srv.ID, " "); !errors.As(err, &se) || se.Kind != KindNotFound {
		t.Errorf("no database name: want KindNotFound, got %v", err)
	}
	if _, err := svc.Blocking(ctx, srv.ID, "WMS"); !errors.As(err, &se) || se.Kind != KindUnreachable {
		t.Errorf("no stored password: want KindUnreachable, got %v", err)
	}
	for _, fix := range []string{domain.FixKillSession, domain.FixRCSI} {
		if _, ok := registeredFixFor(fix); !ok {
			t.Errorf("%s is not registered", fix)
		}
		opts := map[string]string{"sessionId": "83"}
		if _, err := svc.PreviewFixWithOptions(ctx, srv.ID, "WMS", fix, opts); !errors.As(err, &se) || se.Kind != KindUnreachable {
			t.Errorf("preview %s: want KindUnreachable, got %v", fix, err)
		}
		if _, err := svc.ApplyFixWithOptions(ctx, srv.ID, "WMS", fix, opts); !errors.As(err, &se) || se.Kind != KindUnreachable {
			t.Errorf("apply %s: want KindUnreachable, got %v", fix, err)
		}
	}
	if cs, err := st.ListChanges(ctx, srv.ID, "WMS", 10); err != nil || len(cs) != 0 {
		t.Errorf("changes after refusals: %v %v", cs, err)
	}
}

// The kill preview between the read and the apply: a gone session, a new connection with the same id,
// a refused session (no change, so Apply refuses), and the change log line kept for the apply.
func TestKillFixGuards(t *testing.T) {
	now := time.Date(2026, 11, 3, 10, 14, 0, 0, time.UTC)
	login := now.Add(-2 * time.Hour)
	target := domain.KillTarget{Found: true, ServerName: `SQL01\WMS`, ServerNow: now, Behind: 3,
		Session: domain.BlockingSession{Session: 83, Login: "appsvc", Host: "WMS-APP01", Program: "AppScheduler", Status: "sleeping",
			IsUser: true, OpenTran: 1, TranStarted: now.Add(-6 * time.Minute), LoginTime: login, Database: "WMS"}}
	var se *Error

	g := &genericFix{}
	if err := buildKillFix(g, target, map[string]string{"sessionId": "83"}); err != nil {
		t.Fatal(err)
	}
	if g.preview.Statement != "KILL 83;" || g.preview.Options["loginTime"] != domain.KillLoginTime(login) ||
		g.options[killDescriptionKey] == "" || g.preview.Options[killDescriptionKey] != "" || killApplyRefusal(g) != nil {
		t.Errorf("kill preview: %+v, options %v", g.preview, g.options)
	}

	// Apply re-reads: the same session still, a new login with the same id, or gone.
	echo := g.preview.Options
	if err := buildKillFix(&genericFix{}, target, echo); err != nil {
		t.Errorf("same session: %v", err)
	}
	moved := target
	moved.Session.LoginTime = login.Add(time.Hour)
	if err := buildKillFix(&genericFix{}, moved, echo); !errors.As(err, &se) || se.Kind != KindConflict {
		t.Errorf("new connection with id 83: want KindConflict, got %v", err)
	}
	if err := buildKillFix(&genericFix{}, domain.KillTarget{ServerName: `SQL01\WMS`}, echo); !errors.As(err, &se) || se.Kind != KindNotFound ||
		se.Error() != `session 83 is no longer connected to SQL01\WMS: there is nothing to kill` {
		t.Errorf("gone: want KindNotFound, got %v", err)
	}

	system := target
	system.Session.Session, system.Session.IsUser = 7, false
	g = &genericFix{}
	if err := buildKillFix(g, system, map[string]string{"sessionId": "7"}); err != nil {
		t.Fatal(err)
	}
	if err := killApplyRefusal(g); !errors.As(err, &se) || se.Kind != KindConflict ||
		se.Error() != "Session 7 is a SQL Server system session: SQL Server needs it, so kakapo does not kill it." {
		t.Errorf("system session apply: want KindConflict, got %v", err)
	}
}

func TestRCSIFixAlreadyOn(t *testing.T) {
	var se *Error
	if err := buildRCSIFix(&genericFix{}, domain.RCSIFacts{Database: "WMS", RCSI: true}, nil); !errors.As(err, &se) || se.Kind != KindNotFound {
		t.Errorf("already on: want KindNotFound, got %v", err)
	}
	g := &genericFix{}
	if err := buildRCSIFix(g, domain.RCSIFacts{Database: "WMS"}, nil); err != nil || g.fix.Target != "ON" || g.preview.Rollback == "" {
		t.Errorf("off: %v %+v", err, g.preview)
	}
}
