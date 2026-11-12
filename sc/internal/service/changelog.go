package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"sync"
	"time"

	"sqlmonitor/internal/collect"
	"sqlmonitor/internal/domain"
)

// kakapo's change log on the server (docs/phase5w-contract.md, B5-B7). install-change-log creates
// master.dbo.kakapo_ChangeLog and copies kakapo's own log of the server into it; remove-change-log drops it.
// While it is there, every action's started and ended rows are written there too (startAction, endAction),
// and every baseline read puts back the rows it lacks (catchUpChangeLog), noting the rows it lost. kakapo's
// local log stays the primary: nothing here writes to it.

func init() {
	registerFix(domain.FixInstallChangeLog, prepareInstallChangeLog, applyInstallChangeLog)
	registerFix(domain.FixRemoveChangeLog, prepareRemoveChangeLog, applyRemoveChangeLog)
}

// changeLogWriteTimeout bounds one write or one catch-up on the server.
const changeLogWriteTimeout = 2 * time.Minute

// The server reads and the write, as variables so the tests can stand in for a server. A nil connection
// has no table: nothing to write, and a catch-up cannot run.
var (
	changeLogWriter = func(ctx context.Context, db *sql.DB, r domain.ActionLogRow) (bool, bool, error) {
		if db == nil {
			return false, false, nil
		}
		return collect.WriteChangeLogRow(ctx, db, r)
	}
	changeLogKeysReader = func(ctx context.Context, db *sql.DB) (map[string]bool, bool, error) {
		if db == nil {
			return nil, false, errors.New("no connection")
		}
		return collect.ReadChangeLogKeys(ctx, db)
	}
	changeLogInstalledReader = func(ctx context.Context, db *sql.DB) (bool, error) {
		if db == nil {
			return false, errors.New("no connection")
		}
		return collect.ChangeLogInstalled(ctx, db)
	}
)

// changeLogLocks serialises, per server, the catch-up and the direct writes of this process, so the count
// of rows kakapo knows the table holds stays exact.
var changeLogLocks sync.Map

func changeLogLock(serverID int64) *sync.Mutex {
	m, _ := changeLogLocks.LoadOrStore(serverID, &sync.Mutex{})
	return m.(*sync.Mutex)
}

func changeLogSyncKey(serverID int64) string { return fmt.Sprintf("changelog.sync.%d", serverID) }

// changeLogSync reads what kakapo remembers of the table on a server; empty when nothing is kept.
func (s *Service) changeLogSync(ctx context.Context, serverID int64) domain.ChangeLogSync {
	var st domain.ChangeLogSync
	v, err := s.store.Setting(ctx, changeLogSyncKey(serverID))
	if err != nil || v == "" {
		return st
	}
	if err := json.Unmarshal([]byte(v), &st); err != nil {
		log.Printf("server %d: what kakapo kept of its change log on the server could not be read: %v", serverID, err)
	}
	return st
}

func (s *Service) saveChangeLogSync(ctx context.Context, serverID int64, st domain.ChangeLogSync) {
	b, err := json.Marshal(st)
	if err == nil {
		err = s.store.SetSetting(ctx, changeLogSyncKey(serverID), string(b))
	}
	if err != nil {
		log.Printf("server %d: what kakapo knows of its change log on the server could not be kept: %v", serverID, err)
	}
}

// catchUpChangeLog puts in the server's table every row of kakapo's own log of the server that it lacks,
// oldest first (B6), and notes the rows it lost since the last catch-up. It returns how many of kakapo's rows
// the table holds afterwards and how many kakapo has; installed false (nothing done) when the table is not
// there.
func (s *Service) catchUpChangeLog(ctx context.Context, db *sql.DB, serverID int64) (held, local int, installed bool, err error) {
	mu := changeLogLock(serverID)
	mu.Lock()
	defer mu.Unlock()
	wctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), changeLogWriteTimeout)
	defer cancel()
	keys, installed, err := changeLogKeysReader(wctx, db)
	if err != nil {
		return 0, 0, installed, err
	}
	st := s.changeLogSync(wctx, serverID)
	if !installed {
		if st.Expected != 0 || st.CheckedAt != nil {
			st.Expected, st.CheckedAt = 0, nil
			s.saveChangeLogSync(wctx, serverID, st)
		}
		return 0, 0, false, nil
	}
	rows, err := s.store.ListActionLog(wctx, serverID)
	if err != nil {
		return 0, 0, true, err
	}
	missing, lost := domain.ChangeLogCatchUp(rows, keys, st.Expected)
	now := time.Now().UTC().Truncate(time.Millisecond)
	if lost > 0 {
		since := now
		if st.CheckedAt != nil {
			since = *st.CheckedAt
		}
		st.Losses = append(st.Losses, domain.ChangeLogLoss{N: lost, Since: since, Found: now})
		log.Printf("server %d: kakapo's change log on the server lost %d rows since %s; kakapo puts back the ones it holds", serverID, lost, since)
	}
	held = len(rows) - len(missing)
	var werr error
	for _, r := range missing {
		ok, _, e := changeLogWriter(wctx, db, r)
		if e != nil {
			werr = e
			break
		}
		if !ok {
			werr = errors.New("the table went away during the copy")
			break
		}
		held++
	}
	st.Expected = held
	if werr == nil {
		st.CheckedAt = &now
	}
	s.saveChangeLogSync(wctx, serverID, st)
	return held, len(rows), true, werr
}

// writeServerRow writes one row of kakapo's own log in the server's table, when the table is there (B6).
// installed false when it is not: nothing to write.
func (s *Service) writeServerRow(ctx context.Context, db *sql.DB, r domain.ActionLogRow) (bool, error) {
	mu := changeLogLock(r.ServerID)
	mu.Lock()
	defer mu.Unlock()
	wctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), changeLogWriteTimeout)
	defer cancel()
	installed, written, err := changeLogWriter(wctx, db, r)
	if err != nil || !written {
		return installed, err
	}
	st := s.changeLogSync(wctx, r.ServerID)
	st.Expected++
	s.saveChangeLogSync(wctx, r.ServerID, st)
	return true, nil
}

// warnNoChangeLog adds B7's warning to a fix's preview while kakapo's change log is not on the server. Not
// on the change log's own fixes; nothing when the table's state cannot be read.
func warnNoChangeLog(ctx context.Context, db *sql.DB, fixID string, p *domain.FixPreview) {
	if fixID == domain.FixInstallChangeLog || fixID == domain.FixRemoveChangeLog {
		return
	}
	rctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	installed, err := changeLogInstalledReader(rctx, db)
	if err != nil || installed {
		return
	}
	_, host := whoRuns()
	if p.Warnings == nil {
		p.Warnings = []string{}
	}
	p.Warnings = append(p.Warnings, domain.ChangeLogMissingWarning(host))
}

// chg02Facts fills in what CHG02 reads beside the Upkeep facts: the table's state on the server and whether
// kakapo's own log holds an action there. Left blank when either cannot be read (CHG02 stays quiet).
func (s *Service) chg02Facts(ctx context.Context, db *sql.DB, serverID int64, f *domain.UpkeepFacts) {
	if f.Error != "" {
		return
	}
	acted, err := s.store.HasActions(ctx, serverID)
	if err != nil {
		return
	}
	rctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	installed, err := changeLogInstalledReader(rctx, db)
	if err != nil {
		return
	}
	f.KakapoActed = acted
	f.ChangeLog = domain.ChangeLogAbsent
	if installed {
		f.ChangeLog = domain.ChangeLogPresent
	}
}

// --- install-change-log ---

const changeLogPresentKey = "kakapo.changeLogTable"

func prepareInstallChangeLog(ctx context.Context, s *Service, g *genericFix, _ domain.FixDef, _ map[string]string) error {
	rctx, cancel := suReadCtx(ctx)
	defer cancel()
	f, err := collect.ReadChangeLog(rctx, g.db)
	if err != nil {
		return newError(KindUnreachable, "could not read kakapo's change log on %s: %v", g.srv.Name, err)
	}
	rows, err := s.store.ListActionLog(ctx, g.srv.ID)
	if err != nil {
		return err
	}
	p, fix, err := domain.BuildInstallChangeLogFix(f, len(rows))
	if errors.Is(err, domain.ErrFixNotApplicable) {
		return newError(KindNotFound, "kakapo's change log is already on %s: nothing to install", suServerLabel(g, f.ServerName))
	}
	if err != nil {
		return err
	}
	suKeep(g, suDescriptionKey, domain.ChangeLogInstallDescription(suServerLabel(g, f.ServerName), f))
	suKeep(g, changeLogPresentKey, boolText(f.Table))
	return suPreviewed(g, p, fix)
}

func applyInstallChangeLog(ctx context.Context, s *Service, serverID int64, g *genericFix) (domain.FixResult, error) {
	if err := suApplyRefusal(g); err != nil {
		return domain.FixResult{}, err
	}
	change := suChange(serverID, g, domain.ChangeLogObject, domain.ChangeTypeHelper, g.options[suDescriptionKey])
	if g.options[changeLogPresentKey] != "true" {
		// A new table: what kakapo knew of an earlier one no longer counts (its losses stay on record).
		st := s.changeLogSync(ctx, serverID)
		st.Expected, st.CheckedAt = 0, nil
		s.saveChangeLogSync(ctx, serverID, st)
	}
	if err := collect.ExecFixBatches(ctx, g.db, g.preview.Statement); err != nil {
		rctx, cancel := suReadCtx(ctx)
		defer cancel()
		refused := newError(KindUnreachable, "SQL Server refused to install kakapo's change log on %s: %v. Needs CREATE TABLE in master (sysadmin has it).", g.srv.Name, err)
		if after, rerr := collect.ReadChangeLog(rctx, g.db); rerr == nil && (after.Table || after.Trigger) && g.options[changeLogPresentKey] != "true" {
			change.Result = "failed part way (" + err.Error() + "); now: " + after.State()
			if _, lerr := s.insertChange(ctx, change); lerr != nil {
				return domain.FixResult{}, lerr
			}
		}
		return domain.FixResult{}, refused
	}
	// B6's catch-up: kakapo's own log of the server, into the table.
	held, local, _, copyErr := s.catchUpChangeLog(ctx, g.db, serverID)
	copyText := ""
	if copyErr != nil {
		copyText = copyErr.Error()
	}
	rctx, cancel := suReadCtx(ctx)
	defer cancel()
	after, err := collect.ReadChangeLog(rctx, g.db)
	if err != nil {
		return domain.FixResult{}, s.suLogUnread(ctx, change, err, "kakapo's change log")
	}
	text, ok := domain.ChangeLogInstallReadBack(after, local, held, copyText)
	return s.suLog(ctx, change, g, g.fix.Current, text, ok)
}

// --- remove-change-log ---

func prepareRemoveChangeLog(ctx context.Context, s *Service, g *genericFix, _ domain.FixDef, _ map[string]string) error {
	rctx, cancel := suReadCtx(ctx)
	defer cancel()
	f, err := collect.ReadChangeLog(rctx, g.db)
	if err != nil {
		return newError(KindUnreachable, "could not read kakapo's change log on %s: %v", g.srv.Name, err)
	}
	_, host := whoRuns()
	p, fix, err := domain.BuildRemoveChangeLogFix(f, host)
	if errors.Is(err, domain.ErrFixNotApplicable) {
		return newError(KindNotFound, "kakapo's change log is not on %s: nothing to remove", suServerLabel(g, f.ServerName))
	}
	if err != nil {
		return err
	}
	suKeep(g, suDescriptionKey, domain.ChangeLogRemoveDescription(suServerLabel(g, f.ServerName), f))
	return suPreviewed(g, p, fix)
}

func applyRemoveChangeLog(ctx context.Context, s *Service, serverID int64, g *genericFix) (domain.FixResult, error) {
	if err := suApplyRefusal(g); err != nil {
		return domain.FixResult{}, err
	}
	change := suChange(serverID, g, domain.ChangeLogObject, domain.ChangeTypeHelper, g.options[suDescriptionKey])
	mu := changeLogLock(serverID)
	mu.Lock()
	err := collect.ExecFixBatches(ctx, g.db, g.preview.Statement)
	mu.Unlock()
	rctx, cancel := suReadCtx(ctx)
	defer cancel()
	after, rerr := collect.ReadChangeLog(rctx, g.db)
	if err != nil {
		refused := newError(KindUnreachable, "SQL Server refused to remove kakapo's change log on %s: %v. Needs sysadmin (objects in master).", g.srv.Name, err)
		if rerr == nil && !after.Trigger && after.Table {
			change.Result = "failed part way (" + err.Error() + "); now: " + after.State()
			if _, lerr := s.insertChange(ctx, change); lerr != nil {
				return domain.FixResult{}, lerr
			}
		}
		return domain.FixResult{}, refused
	}
	if rerr != nil {
		return domain.FixResult{}, s.suLogUnread(ctx, change, rerr, "The removal of kakapo's change log")
	}
	if !after.Table {
		st := s.changeLogSync(ctx, serverID)
		st.Expected, st.CheckedAt = 0, nil
		s.saveChangeLogSync(ctx, serverID, st)
	}
	text, ok := domain.ChangeLogRemoveReadBack(after)
	return s.suLog(ctx, change, g, g.fix.Current, text, ok)
}
