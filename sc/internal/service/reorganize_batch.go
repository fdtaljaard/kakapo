package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"sqlmonitor/internal/collect"
	"sqlmonitor/internal/domain"
)

// reorganize-indexes (docs/phase5t-contract.md, section 3): the Indexes page's "Reorganise selected".
// A long fix that runs 5q's reorganize-index once per chosen index, in order. Unlike the maintenance
// plan's composite (docs/phase5b-contract.md) it goes on after a failure, runs in the background, and
// can be stopped; each index writes its own change row, so a query's verdict compares per index.
func init() {
	registerLongFix(domain.FixReorganizeIndexes, prepareReorganizeIndexes, applyReorganizeIndexes,
		longFix{timeout: indexApplyTimeout + runReadMargin, completed: reorganizeIndexesCompleted, timeoutFor: reorganizeIndexesTimeout})
}

// reorganizeIndexesTimeout is a batch's outer bound: one index's bound for each index, and the margin.
func reorganizeIndexesTimeout(g *genericFix) time.Duration {
	return time.Duration(max(len(g.preview.Changes), 1))*indexApplyTimeout + runReadMargin
}

func prepareReorganizeIndexes(ctx context.Context, s *Service, g *genericFix, _ domain.FixDef, options map[string]string) error {
	chosen, err := domain.ParseBatchIndexes(options[domain.OptionIndexes])
	if err != nil {
		return newError(KindInvalid, "the chosen indexes: %v", err)
	}
	var f domain.BatchFacts
	if len(chosen) > 0 {
		readCtx, cancel := context.WithTimeout(ctx, shapeReadTimeout)
		defer cancel()
		f, err = collect.IndexBasics(readCtx, g.db, g.database.Name, chosen)
		if err != nil {
			return newError(KindUnreachable, "could not read the chosen indexes in %s: %v", g.database.Name, err)
		}
		f.StaleTables = staleTablesOf(readCtx, g, f.Basics)
	}
	g.preview, g.fix = domain.BuildReorganizeBatchFix(g.database.Name, chosen, f, options)
	if s.runs.pendingFor(g.srv.ID, domain.FixReorganizeIndexes, g.database.Name) {
		g.preview.Held = "A batch of reorganised indexes is already running in " + g.database.Name + ". Wait for it to finish, or stop it, before starting another."
	}
	return nil
}

// staleTablesOf are the tables of the chosen indexes whose statistics are old (5j's test). A read that
// fails leaves the warning out rather than blocking the batch.
func staleTablesOf(ctx context.Context, g *genericFix, basics []domain.IndexBasicsFact) []string {
	seen := map[string]bool{}
	var tables []domain.ShowplanObject
	for _, b := range basics {
		key := strings.ToLower(b.Schema + "." + b.Table)
		if !b.TableFound || seen[key] {
			continue
		}
		seen[key] = true
		tables = append(tables, domain.ShowplanObject{Schema: b.Schema, Table: b.Table})
	}
	if len(tables) == 0 {
		return nil
	}
	facts, now, err := collect.ReadTableStatistics(ctx, g.db, g.database.Name, tables)
	if err != nil {
		return nil
	}
	return domain.LensStaleTables(facts, now)
}

// batchProgress keeps a batch run's items and tells the run after each change.
type batchProgress struct {
	s     *Service
	runID string
	items []domain.RunItem
}

func (p *batchProgress) set(i int, state, text string, changeID int64) {
	p.items[i].State, p.items[i].Text, p.items[i].ChangeID = state, text, changeID
	p.s.runs.setItems(p.runID, p.items)
}

func applyReorganizeIndexes(ctx context.Context, s *Service, serverID int64, g *genericFix) (domain.FixResult, error) {
	p := g.preview
	if len(p.Changes) == 0 || p.Statement == "" {
		msg := "there is no index to reorganise as previewed"
		if len(p.Warnings) > 0 {
			msg = p.Warnings[0]
		}
		return domain.FixResult{}, newError(KindConflict, "%s", msg)
	}
	chosen, err := domain.ParseBatchIndexes(p.Options[domain.OptionIndexes])
	if err != nil {
		return domain.FixResult{}, newError(KindInvalid, "the chosen indexes: %v", err)
	}
	// Only the indexes the preview kept (it leaves out the ones that cannot be reorganised).
	planned := map[string]bool{}
	for _, c := range p.Changes {
		planned[c.Setting] = true
	}
	var list []domain.BatchIndex
	for _, c := range chosen {
		if planned[c.Label()] {
			list = append(list, c)
		}
	}
	started := time.Now().UTC().Truncate(time.Microsecond)
	prog := &batchProgress{s: s, runID: g.runID}
	for _, c := range list {
		prog.items = append(prog.items, domain.RunItem{Label: c.Label(), State: domain.ItemWaiting})
	}
	s.runs.setItems(g.runID, prog.items)

	keep := context.WithoutCancel(ctx)
	stopped := func() bool { return ctx.Err() != nil || s.runs.stopRequested(g.runID) }
	for i, c := range list {
		if stopped() {
			for j := i; j < len(list); j++ {
				prog.items[j].State, prog.items[j].Text = domain.ItemStopped, "not started: the run was stopped"
			}
			s.runs.setItems(g.runID, prog.items)
			break
		}
		prog.set(i, domain.ItemRunning, "measuring", 0)
		reorganizeOne(ctx, keep, s, serverID, g, c, i, prog, stopped)
	}
	summary, verified := domain.BatchSummary(prog.items)
	var lastChange int64
	for _, it := range prog.items {
		if it.ChangeID != 0 {
			lastChange = it.ChangeID
		}
	}
	return domain.FixResult{Preview: p, Before: g.fix.Current, After: summary, Verified: verified, ChangeID: lastChange, AppliedAt: started}, nil
}

// reorganizeOne works on one index of the batch: measure it, skip it when there is nothing to gain,
// reorganise and log it through 5q's apply. A failure is the item's, not the batch's.
func reorganizeOne(ctx, keep context.Context, s *Service, serverID int64, g *genericFix, c domain.BatchIndex, i int, prog *batchProgress, stopped func() bool) {
	child := &genericFix{db: g.db, srv: g.srv, database: g.database, options: domain.ReorganizeIndexOptions(c.Schema, c.Table, c.Index)}
	before, err := readIndexShape(ctx, child, c.Schema, c.Table, c.Index)
	if err != nil {
		if stopped() {
			prog.set(i, domain.ItemStopped, "stopped while measuring", 0)
			return
		}
		prog.set(i, domain.ItemFailed, err.Error(), 0)
		return
	}
	child.preview, child.fix = domain.BuildReorganizeIndexFix(before, child.options)
	if len(child.preview.Changes) == 0 || child.preview.Statement == "" {
		why := "it can no longer be reorganised"
		if len(child.preview.Warnings) > 0 {
			why = child.preview.Warnings[0]
		}
		prog.set(i, domain.ItemSkipped, why, 0)
		return
	}
	if why := domain.ReorganizeSkip(before); why != "" {
		prog.set(i, domain.ItemSkipped, why, 0)
		return
	}
	prog.set(i, domain.ItemRunning, fmt.Sprintf("reorganising %s pages", fmtInt(before.Pages)), 0)
	res, err := reorganizeAndLog(ctx, s, serverID, child, c.Schema, c.Table, c.Index, before)
	switch {
	case err != nil && stopped():
		// The statement was cancelled part-way: the pages it moved stay moved. Measure again and say so.
		prog.set(i, domain.ItemStopped, interruptedNote(keep, s, serverID, child, c, before), 0)
	case err != nil:
		var se *Error
		msg := err.Error()
		if errors.As(err, &se) {
			msg = se.Msg
		}
		prog.set(i, domain.ItemFailed, msg, 0)
	case res.Verified:
		prog.set(i, domain.ItemDone, res.After, res.ChangeID)
	default:
		prog.set(i, domain.ItemDiffers, res.After, res.ChangeID)
	}
}

// interruptedNote measures an index a stop interrupted and logs what changed as a change row, so the
// query page's verdict splits there too. It uses a context that outlives the stop.
func interruptedNote(keep context.Context, s *Service, serverID int64, g *genericFix, c domain.BatchIndex, before domain.IndexShapeFacts) string {
	readCtx, cancel := context.WithTimeout(keep, shapeReadTimeout)
	defer cancel()
	after, err := collect.IndexShape(readCtx, g.db, g.database.Name, c.Schema, c.Table, c.Index)
	if err != nil {
		after = domain.IndexShapeFacts{Error: err.Error()}
	}
	text := domain.ReorganizeInterruptedText(before, after)
	change := domain.Change{
		ServerID: serverID, Database: g.database.Name, ChangedAt: time.Now().UTC().Truncate(time.Microsecond),
		Object: domain.IndexObject(c.Schema, c.Table, before.Index), ChangeType: "INDEX",
		Description: domain.ReorganizeChangeDescription(before, g.database.Name), Statement: g.preview.Statement, Result: text + ", differs",
	}
	if _, err := s.insertChange(keep, change); err != nil {
		return text + " (the change log could not be written: " + err.Error() + ")"
	}
	return text
}

func fmtInt(n int64) string { return fmt.Sprintf("%d", n) }

// reorganizeIndexesCompleted is the batch's check after a lost connection: it does not resume. The
// indexes already done have their change rows; the page shows which were reorganised.
func reorganizeIndexesCompleted(ctx context.Context, s *Service, g *genericFix, startedAt time.Time) (recovered, bool, error) {
	return recovered{after: "the batch stopped where it was: the indexes already reorganised have their change rows, and applying it again carries on with the rest"}, false, nil
}
