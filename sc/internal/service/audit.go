package service

import (
	"context"
	"crypto/rand"
	"database/sql"
	"fmt"
	"log"
	"os"
	"os/user"
	"strconv"
	"strings"
	"sync"
	"time"

	"sqlmonitor/internal/collect"
	"sqlmonitor/internal/domain"
)

// The change audit (docs/phase5w-contract.md), step one: the settings baseline on every read and around
// every action (A2), and kakapo's own log of every action, a started row before the statement runs and an
// ended row after (B1-B4). Every action enters through ApplyFixWithOptions or ApplyQueryStoreFix, which call
// startAction after the preview checks and endAction when the apply returns (or a long run ends).

const baselineTimeout = time.Minute

// takeBaseline reads the settings baseline on db and keeps it (A2, A3). A failed baseline never fails the
// page or the action: it is logged and nil is returned.
func (s *Service) takeBaseline(ctx context.Context, db *sql.DB, serverID int64, reason, actionID string) *domain.BaselineFacts {
	if db == nil {
		return nil
	}
	rctx, cancel := context.WithTimeout(ctx, baselineTimeout)
	defer cancel()
	readAt := time.Now().UTC().Truncate(time.Microsecond)
	f, err := collect.ReadBaseline(rctx, db)
	if err != nil {
		log.Printf("server %d: the settings baseline (%s) could not be read: %v", serverID, reason, err)
		return nil
	}
	if _, _, err := s.store.RecordBaseline(ctx, serverID, f, readAt, reason, actionID); err != nil {
		log.Printf("server %d: the settings baseline (%s) could not be kept: %v", serverID, reason, err)
		return nil
	}
	return &f
}

// --- the change rows an action writes ---

type changeIDsKey struct{}

// changeIDs collects the ids of the change rows written under one action's context, for its ended row.
type changeIDs struct {
	mu  sync.Mutex
	ids []int64
}

func (c *changeIDs) add(id int64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.ids = append(c.ids, id)
}

func (c *changeIDs) list() []int64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]int64(nil), c.ids...)
}

// insertChange writes a change row as every fix does (the tuning report, the query page, the indexes page
// and Recent changes read it, unchanged), and notes its id on the action that is running.
func (s *Service) insertChange(ctx context.Context, c domain.Change) (int64, error) {
	id, err := s.store.InsertChange(ctx, c)
	if err == nil {
		if col, ok := ctx.Value(changeIDsKey{}).(*changeIDs); ok {
			col.add(id)
		}
	}
	return id, err
}

// --- kakapo's own log ---

// actionInfo is what an action's started row says, from the prepared fix.
type actionInfo struct {
	serverID   int64
	serverName string
	database   string
	def        domain.FixDef
	fix        domain.Fix
	preview    domain.FixPreview
	before     string
}

// action is an action in progress: its started row and the change rows it writes.
type action struct {
	row domain.ActionLogRow
	ids *changeIDs
}

var (
	whoOnce             sync.Once
	whoUser, whoMachine string
)

// whoRuns is the Windows user running kakapo and the machine it runs on (B3).
func whoRuns() (string, string) {
	whoOnce.Do(func() {
		if u, err := user.Current(); err == nil {
			whoUser = u.Username
		}
		whoMachine, _ = os.Hostname()
	})
	return whoUser, whoMachine
}

func newActionID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// startAction writes an action's started row (B1, B6): the snapshot before it on the action's connection,
// then the row; a started row that cannot be written refuses the action, so nothing runs unlogged. The
// returned context collects the change rows the action writes.
func (s *Service) startAction(ctx context.Context, db *sql.DB, in actionInfo) (context.Context, *action, error) {
	id := newActionID()
	s.takeBaseline(ctx, db, in.serverID, domain.BaselineBeforeAction, id)
	login := ""
	if db != nil {
		lctx, cancel := context.WithTimeout(ctx, 15*time.Second)
		login, _ = collect.SQLLogin(lctx, db)
		cancel()
	}
	winUser, host := whoRuns()
	now := time.Now().UTC().Truncate(time.Microsecond)
	object := in.fix.Setting
	if object == "" {
		object = in.def.Setting
	}
	row := domain.ActionLogRow{ActionID: id, Phase: domain.ActionStarted, ServerID: in.serverID, LoggedAt: now, StartedAt: now,
		WindowsUser: winUser, SQLLogin: login, KakapoHost: host, KakapoVersion: s.version, Database: in.database, Object: object,
		ChangeType: domain.ActionChangeType(in.def), FixID: in.def.ID,
		Description: domain.ActionDescription(in.def, in.fix, in.serverName, in.database),
		Statement:   in.preview.Statement, Rollback: in.preview.Rollback, ValueBefore: in.before}
	if err := s.store.InsertActionLog(ctx, row); err != nil {
		return ctx, nil, newError(KindConflict, "kakapo could not write its own log of this change, so nothing was run: %v", err)
	}
	col := &changeIDs{}
	return context.WithValue(ctx, changeIDsKey{}, col), &action{row: row, ids: col}, nil
}

// endAction writes an action's ended row (B1, B6) when its apply returned, failed or was stopped, with the
// value before and after, the outcome, the result and the change rows it wrote; then the snapshot after it,
// on the action's connection (a fresh one when db is nil or no longer answers). A failure is logged: the
// action has run.
func (s *Service) endAction(ctx context.Context, db *sql.DB, a *action, res domain.FixResult, err error, stopped bool) {
	if a == nil {
		return
	}
	keep := context.WithoutCancel(ctx)
	end := time.Now().UTC().Truncate(time.Microsecond)
	row := a.row
	row.Phase, row.LoggedAt, row.EndedAt = domain.ActionEnded, end, &end
	ms := end.Sub(row.StartedAt).Milliseconds()
	row.DurationMs = &ms
	if res.Before != "" {
		row.ValueBefore = res.Before
	}
	if err == nil {
		row.ValueAfter = res.After
	}
	row.Outcome = domain.ActionOutcome(res, err, stopped)
	row.Result = domain.ActionResult(res, err)
	ids := a.ids.list()
	if res.ChangeID != 0 && !containsID(ids, res.ChangeID) {
		ids = append(ids, res.ChangeID)
	}
	parts := make([]string, len(ids))
	for i, id := range ids {
		parts[i] = strconv.FormatInt(id, 10)
	}
	row.ChangeIDs = strings.Join(parts, ",")
	if e := s.store.InsertActionLog(keep, row); e != nil {
		log.Printf("server %d: kakapo's log could not take the end of action %s (%s): %v", row.ServerID, row.ActionID, row.FixID, e)
	}
	if s.takeBaseline(keep, db, row.ServerID, domain.BaselineAfterAction, row.ActionID) == nil {
		if fresh, _, cerr := s.connect(keep, row.ServerID); cerr == nil {
			s.takeBaseline(keep, fresh, row.ServerID, domain.BaselineAfterAction, row.ActionID)
			fresh.Close()
		}
	}
}

func containsID(ids []int64, id int64) bool {
	for _, x := range ids {
		if x == id {
			return true
		}
	}
	return false
}

// genericActionInfo is the started row's content for a prepared generic fix.
func genericActionInfo(serverID int64, def domain.FixDef, g *genericFix) actionInfo {
	name := g.facts.ServerName
	if name == "" {
		name = g.srv.Name
	}
	return actionInfo{serverID: serverID, serverName: name, database: g.database.Name, def: def, fix: g.fix, preview: g.preview,
		before: g.fix.Current}
}
