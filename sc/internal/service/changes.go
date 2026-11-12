package service

import (
	"context"
	"strings"
	"time"

	"sqlmonitor/internal/collect"
	"sqlmonitor/internal/domain"
	"sqlmonitor/internal/store"
)

// The What changed? screen (docs/phase5w-contract.md, section C). Opening it is a read: it takes a baseline,
// harvests SQL Server's trace as the database page does, and reads each database's last log backup; each
// failure is a sentence on the page, never a failed page. When the server cannot be reached the page shows
// kakapo's own record and says so.

const (
	changesPageSize   = 100
	changesReadTime   = 2 * time.Minute
	memoryPointsShown = 600
)

// ChangesQuery is what the screen asks for: a problem's time (server time as Record a change takes it, or
// RFC 3339), or a time range; the page (0 is the newest 100); the filters.
type ChangesQuery struct {
	ProblemAt string
	From, To  *time.Time
	Page      int
	Who       string // "", "kakapo", "outside"
	Flagged   bool
}

// Changes builds the What changed? screen. KindNotFound for an unknown server, KindInvalid for a problem time
// that cannot be read.
func (s *Service) Changes(ctx context.Context, serverID int64, q ChangesQuery) (domain.ChangesPage, error) {
	srv, err := s.GetServer(ctx, serverID)
	if err != nil {
		return domain.ChangesPage{}, err
	}
	page := domain.ChangesPage{ServerName: srv.Name, Notes: []string{}, Page: q.Page, PageSize: changesPageSize}
	var problem *domain.RecordedChange
	if strings.TrimSpace(q.ProblemAt) != "" {
		rc, err := domain.ParseRecordChange(domain.RecordChangeInput{Object: "x", ChangeType: "OTHER", Description: "x", ChangedAt: q.ProblemAt})
		if err != nil {
			return page, newError(KindInvalid, "the problem's time must be server time like 2026-11-03T14:20, not %q", q.ProblemAt)
		}
		problem = &rc
	}

	flags := domain.FlagContext{LastLogBackup: map[string]time.Time{}}
	var serverLog []domain.ActionLogRow
	offset, offsetKnown := 0, false
	db, _, err := s.connect(ctx, serverID)
	if err != nil {
		page.Notes = append(page.Notes, "kakapo could not reach "+srv.Name+", so this is kakapo's own record, without a fresh read: "+err.Error())
	} else {
		defer db.Close()
		page.Live = true
		rctx, cancel := context.WithTimeout(ctx, changesReadTime)
		defer cancel()
		if s.takeBaseline(rctx, db, serverID, domain.BaselineRead, "") == nil {
			page.Notes = append(page.Notes, "The settings could not be read just now; the list is as of kakapo's last read.")
		}
		f, err := collect.ReadTraceHarvest(rctx, db)
		switch {
		case err != nil:
			page.Notes = append(page.Notes, "SQL Server's trace could not be read: "+err.Error())
		default:
			offset, offsetKnown = f.UTCOffsetMinutes, true
			if _, err := s.harvestSchemaChanges(ctx, serverID, f); err != nil {
				return page, err
			}
			switch {
			case f.TraceError != "":
				page.Notes = append(page.Notes, "SQL Server's trace could not be read, so who made a change is not known: "+f.TraceError)
			case !f.TraceRunning:
				page.Notes = append(page.Notes, "SQL Server's default trace is off, so who made a change is not known. CHG01 on the Upkeep card switches it back on.")
			}
		}
		// kakapo's change log on the server (B6): the baseline above caught it up; its rows show the actions
		// another kakapo install wrote there.
		if rows, err := collect.ReadChangeLogRows(rctx, db, serverID); err != nil {
			page.Notes = append(page.Notes, "kakapo's change log on the server could not be read: "+err.Error())
		} else {
			serverLog = rows
		}
		if lb, err := collect.LastLogBackups(rctx, db); err != nil {
			page.Notes = append(page.Notes, "The log backups could not be read from msdb, so a switch to the full recovery model stays red: "+err.Error())
		} else {
			flags.LastLogBackup = lb
		}
	}

	// The window: the 7 days before the problem, or the range asked for.
	from, to := q.From, q.To
	if problem != nil {
		at := problem.At
		if !problem.Exact {
			if !offsetKnown {
				page.Notes = append(page.Notes, "The server's clock could not be read, so the problem's time is taken as UTC.")
			}
			at = at.Add(-time.Duration(offset) * time.Minute)
		}
		at = at.UTC()
		start := at.Add(-domain.ProblemWindow)
		page.ProblemAt, from, to = &at, &start, &at
		page.ProblemLabel = problem.At.Format("2 Jan 2006 15:04")
		if problem.Exact {
			page.ProblemLabel += " UTC"
		} else {
			page.ProblemLabel += " (server time)"
		}
	}
	page.From, page.To = from, to

	reads, err := s.store.BaselineReads(ctx, serverID)
	if err != nil {
		return page, err
	}
	ids, seenID := []int64{}, map[int64]bool{}
	for _, r := range reads {
		if !seenID[r.BaselineID] {
			seenID[r.BaselineID] = true
			ids = append(ids, r.BaselineID)
		}
	}
	baselines, err := s.store.Baselines(ctx, serverID, ids)
	if err != nil {
		return page, err
	}
	changes := domain.SettingChangesOf(reads, baselines)
	qs, err := s.store.QueryStoreReadOnlyChanges(ctx, serverID)
	if err != nil {
		return page, err
	}
	changes = append(changes, qs...)
	logRows, err := s.store.ListActionLog(ctx, serverID)
	if err != nil {
		return page, err
	}
	seen, err := s.store.ListAuditChanges(ctx, serverID)
	if err != nil {
		return page, err
	}
	rows := domain.BuildAuditRows(domain.AuditInputs{Changes: changes, Actions: domain.GroupActions(logRows), Seen: seen, Context: flags,
		Others: domain.ServerOnlyActions(serverLog, logRows), Losses: s.changeLogSync(ctx, serverID).Losses})
	inWindow := domain.FilterAuditRows(rows, "", false, from, to)
	if len(reads) > 0 {
		w := reads[0].ReadAt
		page.WatchedSince = &w
		page.Notes = append(page.Notes, "kakapo has watched this server's settings since "+w.Format("2 Jan 2006")+
			"; changes before that come only from kakapo's own log and SQL Server's own history.")
	}
	page.Verdict, page.Tone = domain.BuildAuditVerdict(inWindow, problem != nil, page.ProblemLabel, page.WatchedSince)

	page.Counts = domain.CountAuditRows(inWindow)
	listed := domain.FilterAuditRows(inWindow, q.Who, q.Flagged, nil, nil)
	page.Listed = len(listed)
	page.Copy = domain.AuditCopy(page.Verdict, listed)
	start := q.Page * changesPageSize
	if start > len(listed) {
		start = len(listed)
	}
	end := start + changesPageSize
	if end > len(listed) {
		end = len(listed)
	}
	page.Rows, page.More = listed[start:end], end < len(listed)

	page.Memory, err = s.memoryLine(ctx, serverID, reads, baselines, from, to)
	if err != nil {
		return page, err
	}
	return page, nil
}

// memoryLine is the memory line of the window: every read's memory and the cap in force, at most
// memoryPointsShown of them (evenly kept), the Memory screen's snapshots, and the gaps.
func (s *Service) memoryLine(ctx context.Context, serverID int64, reads []domain.BaselineReadRow, baselines map[int64]domain.Baseline,
	from, to *time.Time) (domain.MemoryLine, error) {
	line := domain.MemoryLine{Points: []domain.MemoryPoint{}, Snapshots: []domain.MemorySnapPoint{}, Gaps: []string{}}
	in := func(t time.Time) bool { return (from == nil || !t.Before(*from)) && (to == nil || !t.After(*to)) }
	pts := []domain.MemoryPoint{}
	for _, r := range reads {
		if !in(r.ReadAt) || (r.SQLMemoryMB == nil && r.TargetMemoryMB == nil) {
			continue
		}
		p := domain.MemoryPoint{At: r.ReadAt, SQLMB: r.SQLMemoryMB, TargetMB: r.TargetMemoryMB}
		if b, ok := baselines[r.BaselineID]; ok {
			p.CapMB = domain.CapMB(b.Items)
		}
		pts = append(pts, p)
	}
	line.Gaps = domain.MemoryGaps(pts)
	if n := len(pts); n > memoryPointsShown {
		kept := make([]domain.MemoryPoint, 0, memoryPointsShown)
		for i := 0; i < memoryPointsShown; i++ {
			kept = append(kept, pts[i*(n-1)/(memoryPointsShown-1)])
		}
		pts = kept
	}
	line.Points = pts
	snaps, err := s.store.ListMemorySnapshots(ctx, serverID, store.MemoryKindInstance, "", 1000)
	if err != nil {
		return line, err
	}
	for i := len(snaps) - 1; i >= 0; i-- {
		if in(snaps[i].TakenAt) {
			line.Snapshots = append(line.Snapshots, domain.MemorySnapPoint{ID: snaps[i].ID, At: snaps[i].TakenAt})
		}
	}
	return line, nil
}
